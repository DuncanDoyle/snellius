package explore

import (
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

var (
	sigWeb    = objects.Ref{Kind: "Service", Namespace: "shop", Name: "web"}
	sigHidden = objects.Ref{Kind: "Service", Namespace: "vault", Name: "secret"}
)

// toService is a rule sending to Service ns/name port 80.
func toService(ns, name string) map[string]any {
	return map[string]any{"backendRefs": []any{map[string]any{"name": name, "namespace": ns, "port": int64(80)}}}
}

// signalWorld has routes on infra/edge: cart to shop/web; absent to the
// missing shop/missing; unattached, not accepted, to shop/web; bare with no
// backendRef; free with no parentRef and no backendRef (no path rows);
// vaulted to the hidden vault/secret; and both to shop/web and vault/secret.
// Findings are only those a test adds.
func signalWorld(t *testing.T, findings ...health.Finding) *Index {
	t.Helper()
	in := newWorld(
		testutil.WithRules(testutil.HTTPRoute("shop", "cart", []objects.ParentRef{gw}, accepted(gw)), toService("shop", "web")),
		testutil.WithRules(testutil.HTTPRoute("shop", "absent", []objects.ParentRef{gw}, accepted(gw)), toService("shop", "missing")),
		testutil.WithRules(testutil.HTTPRoute("shop", "unattached", []objects.ParentRef{gw},
			status(gw, cond("Accepted", metav1.ConditionFalse, "NotAllowedByListeners", ""), resolved)), toService("shop", "web")),
		testutil.HTTPRoute("shop", "bare", []objects.ParentRef{gw}, accepted(gw)),
		testutil.HTTPRoute("shop", "free", nil),
		testutil.WithRules(testutil.HTTPRoute("shop", "vaulted", []objects.ParentRef{gw}, accepted(gw)), toService("vault", "secret")),
		testutil.WithRules(testutil.HTTPRoute("shop", "both", []objects.ParentRef{gw}, accepted(gw)), toService("shop", "web"), toService("vault", "secret")),
	).input(t)
	in.Health.Findings = findings
	return Build(in)
}

// finding is a finding with code and severity on ref.
func finding(code string, sev health.Severity, ref objects.Ref) health.Finding {
	return health.Finding{Code: code, Severity: sev, Resource: &ref, Message: code + " on " + ref.Name}
}

// vaultViewer is a secure-mode viewer that reads everything outside vault:
// it is shown only findings there and may not get or list anything there.
func vaultViewer() Viewer {
	in := func(ref objects.Ref) bool { return ref.Namespace != "vault" }
	return Viewer{Visible: in, Shown: func(f *health.Finding) bool { return in(*f.Resource) },
		Perms: perms{noGet: map[objects.Ref]bool{sigHidden: true}, noList: map[objects.GroupKind]map[string]bool{sliceGK: {"vault": true}}}}
}

type sigWant struct {
	config, path, summary health.State
	pathReason            string
	incomplete            bool
}

func checkSignals(t *testing.T, ix *Index, vw Viewer, want map[string]sigWant) {
	t.Helper()
	for name, w := range want {
		id, _ := row(t, ix, "shop", name)
		config, path, _ := ix.RouteSignals(id, vw)
		st, _, _ := ix.RouteStateOf(id, vw)
		kind := ""
		if path.Reason != nil {
			kind = path.Reason.Kind
		}
		if config.State != w.config || path.State != w.path || st != w.summary || kind != w.pathReason || path.Incomplete != w.incomplete {
			t.Errorf("%s: config %s path %s (%s, incomplete %v) health %s; want config %s path %s (%s, incomplete %v) health %s",
				name, config.State, path.State, kind, path.Incomplete, st, w.config, w.path, w.pathReason, w.incomplete, w.summary)
		}
	}
}

// The incompleteness matrix (design §8.1): a readable ref to an absent
// object is the ordinary finding and complete; an unreadable ref is
// incomplete; an unattached route and one without path rows are not
// attached, their findings in config. The full viewer is never incomplete.
func TestRouteSignalsIncompletenessMatrix(t *testing.T) {
	ix := signalWorld(t, finding(health.CodeRefUnresolved, health.SeverityError, routeRef("shop", "absent")),
		finding(health.CodeConditionFalse, health.SeverityError, routeRef("shop", "unattached")))
	restrictedWant := map[string]sigWant{
		"cart":       {health.StateHealthy, health.StateHealthy, health.StateHealthy, "", false},
		"absent":     {health.StateError, health.StateHealthy, health.StateError, "", false},
		"unattached": {health.StateError, health.StateUnknown, health.StateError, health.ReasonNotAttached, false},
		"bare":       {health.StateHealthy, health.StateHealthy, health.StateHealthy, "", false},
		"free":       {health.StateHealthy, health.StateUnknown, health.StateHealthy, health.ReasonNotAttached, false},
		"vaulted":    {health.StateHealthy, health.StateUnknown, health.StateUnknown, health.ReasonPathIncomplete, true},
	}
	checkSignals(t, ix, vaultViewer(), restrictedWant)
	full := map[string]sigWant{}
	for name, w := range restrictedWant {
		full[name] = sigWant{w.config, health.StateHealthy, w.config, "", false}
		if w.pathReason == health.ReasonNotAttached {
			full[name] = w
		}
	}
	checkSignals(t, ix, Viewer{}, full)
	// The restricted viewer's unreadable ref is incomplete whether the target has findings or not.
	hidden := signalWorld(t, finding(health.CodeBackendNoReadyEndpoints, health.SeverityWarning, sigHidden))
	checkSignals(t, hidden, vaultViewer(), map[string]sigWant{"vaulted": restrictedWant["vaulted"]})
	checkSignals(t, hidden, Viewer{}, map[string]sigWant{"vaulted": {health.StateHealthy, health.StateWarning, health.StateWarning, health.ReasonBackendProblem, false}})
}

// Design §8.3: health is the configuration state lifted by the path, and for
// a restricted viewer at least unknown over an incomplete path; a readable
// fault keeps its state on an incomplete path and names its reason.
func TestRouteSignalsSummaryTable(t *testing.T) {
	ix := signalWorld(t, rejects001(gwObj), finding(health.CodeNamespaceNotWatched, health.SeverityWarning, routeRef("shop", "vaulted")),
		finding(health.CodeBackendNoReadyEndpoints, health.SeverityWarning, sigWeb))
	checkSignals(t, ix, vaultViewer(), map[string]sigWant{
		// Readable faults: the rejecting Gateway's error decides over the backend's warning.
		"cart": {health.StateHealthy, health.StateError, health.StateWarning, health.ReasonGatewayRejecting, false},
		// A readable fault on an incomplete path keeps its state.
		"both": {health.StateHealthy, health.StateError, health.StateWarning, health.ReasonGatewayRejecting, true},
		// Config warning over an incomplete path stays warning.
		"vaulted": {health.StateWarning, health.StateError, health.StateWarning, health.ReasonGatewayRejecting, true},
	})
	// Without the Gateway's 001 the restricted viewer's rows follow the table's other rows.
	ix = signalWorld(t, finding(health.CodeNamespaceNotWatched, health.SeverityWarning, routeRef("shop", "vaulted")),
		finding(health.CodeBackendNoReadyEndpoints, health.SeverityWarning, sigWeb))
	checkSignals(t, ix, vaultViewer(), map[string]sigWant{
		"cart":    {health.StateHealthy, health.StateWarning, health.StateWarning, health.ReasonBackendProblem, false},
		"both":    {health.StateHealthy, health.StateWarning, health.StateWarning, health.ReasonBackendProblem, true},
		"vaulted": {health.StateWarning, health.StateUnknown, health.StateWarning, health.ReasonPathIncomplete, true},
		"bare":    {health.StateHealthy, health.StateHealthy, health.StateHealthy, "", false},
	})
	id, _ := row(t, ix, "shop", "cart")
	if _, p, b := ix.RouteSignals(id, vaultViewer()); b == nil || *b != sigWeb || p.Reason.Code != health.CodeBackendNoReadyEndpoints {
		t.Errorf("cart path reason %+v backend %v", p.Reason, b)
	}
}

// The install-wide unknown never masks a readable fault, and an incomplete
// path without one says path-incomplete (design §8.1).
func TestRouteSignalsUnknownKeepsReadableFault(t *testing.T) {
	ix := signalWorld(t, finding(health.CodeBackendNoReadyEndpoints, health.SeverityWarning, sigWeb))
	vw := vaultViewer()
	vw.Unknown = true
	checkSignals(t, ix, vw, map[string]sigWant{
		"both":    {health.StateUnknown, health.StateWarning, health.StateUnknown, health.ReasonBackendProblem, true},
		"vaulted": {health.StateUnknown, health.StateUnknown, health.StateUnknown, health.ReasonPathIncomplete, true},
		"bare":    {health.StateUnknown, health.StateUnknown, health.StateUnknown, health.ReasonUnknown, false},
	})
}

// Viewer.Path's rows decide the path: their worst state and reason, with
// incompleteness still from permissions; no readable row is not attached.
func TestRouteSignalsFromPathRows(t *testing.T) {
	ix := signalWorld(t)
	listener := &health.RowReason{Kind: health.ReasonPathProblem, Code: "ResolvedRefs"}
	rows := map[string]PathRows{
		"cart":    {State: health.StateError, Reason: listener, Rows: 2},
		"both":    {State: health.StateWarning, Reason: listener, Rows: 1},
		"vaulted": {State: health.StateHealthy, Rows: 1},
		"bare":    {State: health.StateHealthy},
	}
	vw := vaultViewer()
	vw.Path = func(id RowID) PathRows { return rows[ix.Routes[id].Ref.Name] }
	checkSignals(t, ix, vw, map[string]sigWant{
		"cart":    {health.StateHealthy, health.StateError, health.StateHealthy, health.ReasonPathProblem, false},
		"both":    {health.StateHealthy, health.StateWarning, health.StateUnknown, health.ReasonPathProblem, true},
		"vaulted": {health.StateHealthy, health.StateUnknown, health.StateUnknown, health.ReasonPathIncomplete, true},
		"bare":    {health.StateHealthy, health.StateUnknown, health.StateHealthy, health.ReasonNotAttached, false},
	})
}

// Without delegation analysis (D-114) a route naming a parent route has an
// incomplete path, decided by its own spec alone.
func TestRouteSignalsNoDelegationIncomplete(t *testing.T) {
	in := newWorld(testutil.HTTPRoute("shop", "kid", []objects.ParentRef{{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: "shop", Name: "top"}}),
		testutil.HTTPRoute("shop", "top", []objects.ParentRef{gw}, accepted(gw))).input(t)
	in.Health.Findings = nil
	ix := Build(in)
	vw := vaultViewer()
	vw.NoDelegation = true
	checkSignals(t, ix, vw, map[string]sigWant{
		"kid": {health.StateHealthy, health.StateUnknown, health.StateUnknown, health.ReasonPathIncomplete, true},
		"top": {health.StateHealthy, health.StateHealthy, health.StateHealthy, "", false},
	})
}

// A full-read viewer and a viewer without permissions keep today's health,
// flags and reason on every row (design §8.3).
func TestRouteSignalsFullReadUnchanged(t *testing.T) {
	ix := signalWorld(t, rejects001(gwObj), finding(health.CodeBackendNoReadyEndpoints, health.SeverityError, sigHidden),
		finding(health.CodeRefUnresolved, health.SeverityError, routeRef("shop", "absent")))
	all := func(*health.Finding) bool { return true }
	for k := range ix.Routes {
		id := RowID(k) // #nosec G115 -- a row count fits in int32
		want, wf, wr := ix.routeState(id, Viewer{}, true)
		for _, vw := range []Viewer{{}, {Shown: all}} {
			st, f, r := ix.RouteStateOf(id, vw)
			if st != want || st != ix.Routes[k].State || f != wf || (r == nil) != (wr == nil) || r != nil && *r != *wr {
				t.Errorf("%s: %s %v %+v, want %s %v %+v", ix.Routes[k].Ref, st, f, r, want, wf, wr)
			}
			if _, p, _ := ix.RouteSignals(id, vw); p.Incomplete {
				t.Errorf("%s: incomplete without permissions", ix.Routes[k].Ref)
			}
		}
	}
}

// A route without a row decides its path from its own object: incomplete
// over an unreadable ref, else not attached, as it has no path rows.
func TestObjectSignals(t *testing.T) {
	ix := signalWorld(t)
	_, r := row(t, ix, "shop", "vaulted")
	if c, p := ix.ObjectSignals(r.Obj, vaultViewer()); c.State != health.StateHealthy || p.State != health.StateUnknown || !p.Incomplete ||
		p.Reason == nil || p.Reason.Kind != health.ReasonPathIncomplete {
		t.Errorf("restricted: config %+v path %+v", c, p)
	}
	if _, p := ix.ObjectSignals(r.Obj, Viewer{}); p.State != health.StateUnknown || p.Incomplete || p.Reason == nil || p.Reason.Kind != health.ReasonNotAttached {
		t.Errorf("full: path %+v", p)
	}
}

// config= and path= filter on the viewer's signals: values within one OR,
// the two AND; health= and the health sort use the summary.
func TestQuerySignalFilters(t *testing.T) {
	ix := signalWorld(t, finding(health.CodeRefUnresolved, health.SeverityError, routeRef("shop", "absent")),
		finding(health.CodeBackendNoReadyEndpoints, health.SeverityWarning, sigWeb))
	q := func(vw Viewer, q Query) []string {
		t.Helper()
		p, err := ix.QueryRoutesAs(q, vw)
		if err != nil {
			t.Fatal(err)
		}
		return names(ix, p)
	}
	for _, c := range []struct {
		vw   Viewer
		q    Query
		want []string
	}{
		{vaultViewer(), Query{Config: []health.State{health.StateError}}, []string{"absent"}},
		{vaultViewer(), Query{Path: []health.State{health.StateUnknown}}, []string{"free", "vaulted"}},
		{vaultViewer(), Query{Path: []health.State{health.StateUnknown, health.StateWarning}}, []string{"both", "cart", "free", "unattached", "vaulted"}},
		{vaultViewer(), Query{Config: []health.State{health.StateHealthy}, Path: []health.State{health.StateWarning}}, []string{"both", "cart", "unattached"}},
		{vaultViewer(), Query{Health: []health.State{health.StateUnknown}}, []string{"vaulted"}},
		{Viewer{}, Query{Path: []health.State{health.StateUnknown}}, []string{"free"}},
		{Viewer{}, Query{Health: []health.State{health.StateUnknown}}, []string{}},
	} {
		if got := q(c.vw, c.q); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.q, got, c.want)
		}
	}
	// The health sort ranks the restricted viewer's incomplete row as unknown: after the error, before warnings.
	got := q(vaultViewer(), Query{Sort: Sort{Key: sortHealth, Desc: true}})
	if i := slices.Index(got, "vaulted"); i != 1 || got[0] != "absent" {
		t.Errorf("health sort %v", got)
	}
	if _, err := ix.QueryRoutesAs(Query{Path: []health.State{"broken"}}, Viewer{}); err == nil {
		t.Error("path=broken accepted")
	}
}
