package api_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kgateway"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

// The Phase 8 API boundary tests (design §11): the policies list and detail
// over the rule world, through a real SAR authoriser in secure mode.

const (
	kgwGroup  = kinds.KgatewayGroup
	acmeGroup = "policy.acme.example"
	// otherController writes ancestors no install here owns.
	otherController = "example.com/other"
)

// tp is a TrafficPolicy ns/name with targetRefs and ancestors.
func tp(ns, name string, targets []any, ancestors ...testutil.PolicyAncestor) *unstructured.Unstructured {
	u := testutil.TrafficPolicy(ns, name, ancestors...)
	u.Object["spec"] = map[string]any{"targetRefs": targets}
	return u
}

// targetRef is a targetRef to group/kind name, sectioned when section is set.
func targetRef(group, kind, name, section string) map[string]any {
	m := map[string]any{"group": group, "kind": kind, "name": name}
	if section != "" {
		m["sectionName"] = section
	}
	return m
}

// ancestor is an ancestor entry on ref by controller with Accepted and Attached as given.
func ancestor(ref objects.ParentRef, controller string, accepted, attached metav1.ConditionStatus) testutil.PolicyAncestor {
	return testutil.PolicyAncestor{Ref: ref, Controller: controller, Conds: []metav1.Condition{
		testutil.Cond("Accepted", accepted, "Accepted"), testutil.Cond("Attached", attached, "Attached")}}
}

// Policy fixture names.
const (
	tpAncestors   = "tp-ancestors"
	tpTargets     = "tp-hidden-targets"
	tpVisible     = "tp-visible"
	tpHiddenOld   = "tp-hidden-changed"
	tpHiddenGone  = "tp-hidden-deleted"
	tpHiddenNew   = "tp-hidden-new"
	ancGwNew      = "anc-gw-new"
	ancGwChanged  = "anc-gw-changed"
	ancGwDeleted  = "anc-gw-deleted"
	tgtGwNew      = "tgt-gw-new"
	tgtGwChanged  = "tgt-gw-changed"
	tgtGwDeleted  = "tgt-gw-deleted"
	absentRoute   = "absent-route"
	policyTwin    = "twin"
	policiesPath  = ruleBase + "/policies"
	gatewaysGroup = objects.GatewayAPIGroup
)

// hiddenGwRef is the parentRef to Gateway hiddenNS/name.
func hiddenGwRef(name string) objects.ParentRef { return testutil.GwRef(hiddenNS, name) }

// policyWorld adds tp-ancestors (hidden ancestors), tp-hidden-targets (targets the restricted
// viewer may not read), tp-visible, and two hidden policies to change and delete.
func policyWorld(s ruleSnap) {
	gw := gw0()[0]
	s.add(tp(ns0, tpAncestors, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")},
		ancestor(gw, testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue),
		ancestor(hiddenGwRef("hidden-gw"), testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue),
		ancestor(hiddenGwRef(ancGwNew), otherController, metav1.ConditionFalse, metav1.ConditionFalse),
		ancestor(hiddenGwRef(ancGwChanged), otherController, metav1.ConditionFalse, metav1.ConditionFalse),
		ancestor(hiddenGwRef(ancGwDeleted), otherController, metav1.ConditionFalse, metav1.ConditionFalse)))
	s.add(testutil.Gateway(hiddenNS, ancGwChanged, scale.Class, nil), testutil.Gateway(hiddenNS, ancGwDeleted, scale.Class, nil))
	s.add(tp(kindLimitedNS, tpTargets, []any{targetRef(gatewaysGroup, "Gateway", tgtGwNew, ""),
		targetRef(gatewaysGroup, "Gateway", tgtGwChanged, "https"), targetRef(gatewaysGroup, "Gateway", tgtGwDeleted, "")}))
	s.add(testutil.Gateway(kindLimitedNS, tgtGwChanged, scale.Class, nil), testutil.Gateway(kindLimitedNS, tgtGwDeleted, scale.Class, nil))
	visible := tp(ns0, tpVisible, []any{targetRef(gatewaysGroup, "HTTPRoute", absentRoute, "")})
	visible.Object["spec"].(map[string]any)["cors"] = map[string]any{"allowOrigins": []any{"https://example.com"}}
	visible.SetAnnotations(map[string]string{kgateway.PolicyWeightAnnotation: "10"})
	s.add(visible)
	route := []any{targetRef(gatewaysGroup, "HTTPRoute", "hidden-route", "")}
	s.add(tp(hiddenNS, tpHiddenOld, route, ancestor(hiddenGwRef("hidden-gw"), testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue)),
		tp(hiddenNS, tpHiddenGone, route, ancestor(hiddenGwRef("hidden-gw"), testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue)))
}

// setListener renames the first spec listener of Gateway u.
func setListener(u *unstructured.Unstructured, name string) {
	u.Object["spec"].(map[string]any)["listeners"].([]any)[0].(map[string]any)["name"] = name
}

// policyChanges are the hidden policy, target and ancestor changes (design §11).
var policyChanges = []ruleChange{
	{name: "policyCreated", apply: func(s ruleSnap) {
		s.add(tp(hiddenNS, tpHiddenNew, []any{targetRef(gatewaysGroup, "HTTPRoute", "hidden-route", "")},
			ancestor(hiddenGwRef("hidden-gw"), testutil.Controller, metav1.ConditionFalse, metav1.ConditionFalse)))
	}},
	{name: "policyChanged", apply: func(s ruleSnap) {
		u := s.get("TrafficPolicy", hiddenNS, tpHiddenOld)
		u.Object["spec"].(map[string]any)["cors"] = map[string]any{}
		u.Object["status"] = map[string]any{"ancestors": []any{}}
	}},
	{name: "policyDeleted", apply: func(s ruleSnap) { s.remove("TrafficPolicy", hiddenNS, tpHiddenGone) }},
	// A policy kind the viewer may not read, in a namespace it reads.
	{name: "policyKindHidden", apply: func(s ruleSnap) {
		s.add(listenerPolicy(kindLimitedNS, "limited-listeners", testutil.ScaleGateway(9), metav1.ConditionFalse))
	}},
	{name: "targetCreated", apply: func(s ruleSnap) { s.add(testutil.Gateway(kindLimitedNS, tgtGwNew, scale.Class, nil)) }},
	{name: "targetChanged", apply: func(s ruleSnap) { setListener(s.get("Gateway", kindLimitedNS, tgtGwChanged), "https") }},
	{name: "targetDeleted", apply: func(s ruleSnap) { s.remove("Gateway", kindLimitedNS, tgtGwDeleted) }},
	{name: "ancestorCreated", apply: func(s ruleSnap) { s.add(testutil.Gateway(hiddenNS, ancGwNew, scale.Class, nil)) }},
	{name: "ancestorChanged", apply: func(s ruleSnap) {
		setCondition(s.get("Gateway", hiddenNS, ancGwChanged), "Programmed", metav1.ConditionFalse)
		setCondition(s.get("Gateway", hiddenNS, "hidden-gw"), "Programmed", metav1.ConditionFalse)
	}},
	{name: "ancestorDeleted", apply: func(s ruleSnap) { s.remove("Gateway", hiddenNS, ancGwDeleted) }},
}

// policyChecker is ruleChecker, except that the restricted viewers may not
// read Gateways in kindLimitedNS: the hidden targets live there.
type policyChecker struct{}

func (policyChecker) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username != fullUser && a.Resource == "gateways" && a.Namespace == kindLimitedNS {
		return false, nil
	}
	return ruleChecker{}.Check(ctx, id, a)
}

// acmeTrafficPolicy is a downstream TrafficPolicy kind in another group.
var acmeTrafficPolicy = kinds.Served{Descriptor: kinds.Descriptor{Group: acmeGroup, Kind: "TrafficPolicy", Resource: "trafficpolicies",
	Versions: []string{"v1"}, Namespaced: true, Category: kinds.CategoryPolicy, DisplayName: "TrafficPolicies"}, Version: "v1"}

// policyKinds is ruleKinds plus the downstream TrafficPolicy.
type policyKinds struct{}

func (policyKinds) Lookup(group, kind string) (sar.Kind, bool) {
	if group == acmeGroup && kind == "TrafficPolicy" {
		return sar.Kind{Group: acmeGroup, Resource: "trafficpolicies", Namespaced: true}, true
	}
	return ruleKinds{}.Lookup(group, kind)
}

func (policyKinds) All() ([]sar.Kind, string) {
	all, v := ruleKinds{}.All()
	return append(all, sar.Kind{Group: acmeGroup, Resource: "trafficpolicies", Namespaced: true}), v + "+acme"
}

// interpreters gives every install the kgateway policy interpreter.
var interpreters = api.WithPolicyInterpreters(func(install.Install) policy.Interpreter { return kgateway.PolicyInterpreter() })

// policyServe serves src in secure mode through SAR over policyChecker, the
// user taken from X-User; narrow, when set, wraps the SAR authoriser.
func policyServe(t *testing.T, src api.ModelSource, narrow func(api.Authorizer) api.Authorizer, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	return policyServeWith(t, src, policyChecker{}, narrow, opts...)
}

// policyServeWith is policyServe answering SARs through c.
func policyServeWith(t *testing.T, src api.ModelSource, c sar.Checker, narrow func(api.Authorizer) api.Authorizer, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	const unlimited = 1 << 30
	var az api.Authorizer = sar.New(sar.Options{Checker: c, Kinds: policyKinds{}, Clock: clocktesting.NewFakeClock(ruleCommit),
		IdentityFrom: ruleIdentity, MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	if narrow != nil {
		az = narrow(az)
	}
	opts = append([]api.HandlerOption{api.WithSecureMode(), api.WithCommitClock(func() time.Time { return ruleCommit }), interpreters}, opts...)
	h := api.NewHandler(src, fstest.MapFS{}, az, opts...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ruleUserKey{}, r.Header.Get("X-User"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// policyEngine computes s, with served kinds served, as worldWith does.
func policyEngine(t *testing.T, s ruleSnap, served []kinds.Served) *engine.Engine {
	t.Helper()
	r := &ruleEngine{t: t, h: &holder{}, s: s}
	clock := func() time.Time { return r.now }
	r.e = engine.New(engine.Config{Snapshots: r.h, Served: served, Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: health.Upstream(),
		Now: clock, Collected: api.RuleCollected(ruleInstall, clock, nil)})
	for _, at := range ruleSteps {
		r.compute(at)
	}
	return r.e
}

// policyBase is the rule world plus policyWorld, with c applied.
func policyBase(t *testing.T, c ruleChange) *engine.Engine {
	t.Helper()
	s := ruleSnapshot(t)
	policyWorld(s)
	return worldWith(t, s, c)
}

// policyPath is the detail path of the policy group/kind ns/name.
func policyPath(group, kind, ns, name string) string {
	if group == "" {
		group = "-"
	}
	return policiesPath + "/" + group + "/" + kind + "/" + ns + "/" + name
}

// tpPath is the detail path of the kgateway TrafficPolicy ns/name.
func tpPath(ns, name string) string { return policyPath(kgwGroup, "TrafficPolicy", ns, name) }

// policySurfaces are the policy responses no hidden change may move for the restricted viewer.
var policySurfaces = []string{
	policiesPath + "?limit=200",
	policiesPath + "?limit=200&hasProblems=true",
	policiesPath + "?limit=200&hasProblems=false",
	policiesPath + "?limit=200&namespace=" + kindLimitedNS,
	policiesPath + "?limit=200&kind=" + kgwGroup + "/TrafficPolicy&feature=cors",
	policiesPath + "?limit=1",
	tpPath(ns0, tpAncestors),
	tpPath(kindLimitedNS, tpTargets),
	tpPath(ns0, tpVisible),
	tpPath(hiddenNS, tpHiddenOld),
	tpPath(hiddenNS, tpHiddenNew),
}

// policyPairs serves the policy base world and each policy change.
func policyPairs(t *testing.T) []rulePair {
	t.Helper()
	before := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	out := make([]rulePair, 0, len(policyChanges))
	for _, c := range policyChanges {
		out = append(out, rulePair{change: c, before: before, after: policyServe(t, policyBase(t, c), nil)})
	}
	return out
}

// A hidden policy, target or ancestor change moves no byte the restricted viewer is served;
// each change reaches the full reader, so the check is not vacuous.
func TestPoliciesHiddenChangesUnchanged(t *testing.T) {
	for _, p := range policyPairs(t) {
		reached := false
		for _, path := range policySurfaces {
			bc, bb := fetch(t, p.before, restrictedUser, path)
			ac, ab := fetch(t, p.after, restrictedUser, path)
			if bc != ac || bb != ab {
				t.Errorf("%s: %s changed for the restricted viewer:\n  %d %.400s\n  vs %d %.400s", p.change.name, path, bc, bb, ac, ab)
			}
			_, fb := fetch(t, p.before, fullUser, path)
			_, fa := fetch(t, p.after, fullUser, path)
			reached = reached || fb != fa
		}
		if !reached {
			t.Errorf("%s reaches no policy surface for the full reader", p.change.name)
		}
	}
}

// policyRowOf is the list row of the policy group/kind ns/name.
func policyRowOf(t *testing.T, rs api.PoliciesResponse, group, ns, name string) api.PolicyRow {
	t.Helper()
	for _, r := range rs.Items {
		if r.Ref.Group == group && r.Ref.Namespace == ns && r.Ref.Name == name {
			return r
		}
	}
	t.Fatalf("no row %s %s/%s", group, ns, name)
	return api.PolicyRow{}
}

// outcomes are a row's or detail's target check outcomes in target order, "-" for a selector.
func outcomes(ts []api.PolicyTargetDTO) []string {
	var out []string
	for _, t := range ts {
		o := "-"
		if t.Check != nil {
			o = t.Check.Outcome
		}
		out = append(out, o)
	}
	return out
}

// The full reader and the restricted viewer: readable policies only, in
// namespace and name order; checks, summaries and links per what each may read.
func TestPoliciesReaders(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	full := decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200")
	restricted := decode[api.PoliciesResponse](t, srv, restrictedUser, policiesPath+"?limit=200")

	names := func(rs api.PoliciesResponse) []string {
		var out []string
		for _, r := range rs.Items {
			out = append(out, r.Ref.Namespace+"/"+r.Ref.Name)
		}
		return out
	}
	if got := names(full); !slices.IsSortedFunc(full.Items, func(a, b api.PolicyRow) int {
		return strings.Compare(a.Ref.Namespace+"/"+a.Ref.Name, b.Ref.Namespace+"/"+b.Ref.Name)
	}) || !slices.Contains(got, hiddenNS+"/"+tpHiddenOld) || full.Total != len(full.Items) {
		t.Errorf("full reader rows %v (total %d)", got, full.Total)
	}
	for _, n := range names(restricted) {
		if strings.HasPrefix(n, hiddenNS+"/") {
			t.Errorf("restricted viewer sees hidden policy %s", n)
		}
	}
	if restricted.Total != len(restricted.Items) || restricted.Total >= full.Total || restricted.DeniedInInstall != 0 {
		t.Errorf("restricted total %d of %d rows (full %d), denied %d", restricted.Total, len(restricted.Items), full.Total, restricted.DeniedInInstall)
	}
	for _, f := range restricted.Facets.Namespaces {
		if f.Namespace == hiddenNS {
			t.Errorf("restricted facets count the hidden namespace: %+v", f)
		}
	}

	// Checks: found and section-not-found need get, not-found needs list.
	want := map[string][2][]string{
		tpTargets: {{computed.TargetNotFound, computed.TargetSectionNotFound, computed.TargetFound},
			{computed.TargetUnknown, computed.TargetUnknown, computed.TargetUnknown}},
		tpVisible: {{computed.TargetNotFound}, {computed.TargetNotFound}},
	}
	for name, w := range want {
		ns := ns0
		if name == tpTargets {
			ns = kindLimitedNS
		}
		for k, rs := range []api.PoliciesResponse{full, restricted} {
			if got := outcomes(policyRowOf(t, rs, kgwGroup, ns, name).Targets); !slices.Equal(got, w[k]) {
				t.Errorf("%s viewer %d: checks %v, want %v", name, k, got, w[k])
			}
		}
	}
	// Has problems follows the checks the viewer may know of.
	if !policyRowOf(t, full, kgwGroup, kindLimitedNS, tpTargets).Problems.Has || policyRowOf(t, restricted, kgwGroup, kindLimitedNS, tpTargets).Problems.Has {
		t.Error("tp-hidden-targets: has problems should be true for the full reader only")
	}

	// The summaries use only current ancestors; the other controller's False is left
	// out, and the restricted viewer's summaries leave out hidden-gw too.
	for k, rs := range []api.PoliciesResponse{full, restricted} {
		row := policyRowOf(t, rs, kgwGroup, ns0, tpAncestors)
		if row.Accepted == nil || row.Accepted.Status != metav1.ConditionTrue || row.Accepted.Source != policy.SourceInferred || row.Problems.Has ||
			len(row.AttachedOn) != 2-k || row.AttachedOn[0].Source != policy.SourceControllerReported || row.Ref.Link != api.Link(tpPath(ns0, tpAncestors)) {
			t.Errorf("viewer %d: tp-ancestors row %+v", k, row)
		}
	}
	vis := policyRowOf(t, restricted, kgwGroup, ns0, tpVisible)
	if len(vis.Features) != 1 || vis.Features[0].Key != "cors" || vis.Features[0].Label != "CORS" || vis.Features[0].Value != nil || vis.Accepted != nil {
		t.Errorf("tp-visible row %+v", vis)
	}

	// Detail: the hidden ancestor is plain text with no health; the readable one links with health.
	d := decode[api.PolicyDetail](t, srv, restrictedUser, tpPath(ns0, tpAncestors))
	fd := decode[api.PolicyDetail](t, srv, fullUser, tpPath(ns0, tpAncestors))
	if len(d.Ancestors) != 5 || d.Ancestors[0].Ref.Link != api.LinkDetail || d.Ancestors[0].Health == "" || !d.Ancestors[0].Current {
		t.Fatalf("readable ancestor %+v", d.Ancestors)
	}
	for k, a := range d.Ancestors[1:] {
		if a.Ref.Link != api.LinkNone || a.Health != "" {
			t.Errorf("hidden ancestor %d linked: %+v", k+1, a)
		}
	}
	if a := fd.Ancestors[2]; a.Current || a.NotCurrent != policy.NotCurrentOtherController || a.Source != policy.SourceControllerReported || len(a.Conditions) != 2 {
		t.Errorf("other controller's ancestor %+v", a)
	}
	// The restricted viewer gets no judgment about an ancestor it may not read.
	for k, a := range d.Ancestors[1:] {
		if a.Current || a.NotCurrent != policy.NotCurrentUnreadable || a.Why != "" || len(a.Conditions) != 2 {
			t.Errorf("hidden ancestor %d: %+v", k+1, a)
		}
	}
	if fd.Ancestors[1].Ref.Link != api.LinkDetail || fd.Ancestors[1].Health == "" {
		t.Errorf("full reader's hidden-gw ancestor %+v", fd.Ancestors[1])
	}
	if tg := d.Targets[0]; tg.Ref == nil || tg.Ref.Link != api.LinkDetail || tg.Ref.Namespace != ns0 || tg.Health == "" || tg.Check == nil || tg.Check.Outcome != computed.TargetFound {
		t.Errorf("readable target %+v", tg)
	}
	td := decode[api.PolicyDetail](t, srv, restrictedUser, tpPath(kindLimitedNS, tpTargets))
	for _, tg := range td.Targets {
		if tg.Ref.Link != api.LinkNone || tg.Health != "" || tg.Check.Source != policy.SourceUnknown {
			t.Errorf("hidden target %+v", tg)
		}
	}
	vd := decode[api.PolicyDetail](t, srv, restrictedUser, tpPath(ns0, tpVisible))
	if len(vd.Annotations) != 1 || vd.Annotations[0].Value != "10" || vd.Features[0].Value == nil || !vd.Interpreted {
		t.Errorf("tp-visible detail %+v", vd)
	}
}

// A hidden policy's detail is the absent policy's, byte for byte; the full reader gets it.
func TestPolicyDetailDeniedIsAbsent(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	hc, hidden := fetch(t, srv, restrictedUser, tpPath(hiddenNS, tpHiddenOld))
	ac, absent := fetch(t, srv, restrictedUser, tpPath(hiddenNS, "no-such-policy"))
	if hc != http.StatusNotFound || hc != ac || hidden != absent {
		t.Errorf("hidden %d %q, absent %d %q", hc, hidden, ac, absent)
	}
	// Readable namespace, unreadable kind.
	srvKind := policyServe(t, policyBase(t, policyChanges[slices.IndexFunc(policyChanges, func(c ruleChange) bool { return c.name == "policyKindHidden" })]), nil)
	lp := policyPath(kgwGroup, "ListenerPolicy", kindLimitedNS, "limited-listeners")
	kc, kb := fetch(t, srvKind, restrictedUser, lp)
	xc, xb := fetch(t, srvKind, restrictedUser, policyPath(kgwGroup, "ListenerPolicy", kindLimitedNS, "no-such-policy"))
	if kc != http.StatusNotFound || kc != xc || kb != xb {
		t.Errorf("kind-hidden %d %q, absent %d %q", kc, kb, xc, xb)
	}
	if code, _ := fetch(t, srv, fullUser, tpPath(hiddenNS, tpHiddenOld)); code != http.StatusOK {
		t.Errorf("full reader: %d", code)
	}
}

// denyNamed allows every member and every list but refuses one name by name:
// a downstream authoriser narrowing SAR (D-119).
type denyNamed struct{ name string }

func (denyNamed) Authorize(*http.Request, objects.Ref) bool { return true }

func (d denyNamed) AuthorizeNamedErr(_ *http.Request, ref objects.Ref) (bool, error) {
	return ref.Name != d.name, nil
}

func (denyNamed) CanList(*http.Request, objects.GroupKind, string) (bool, error) { return true, nil }

// denyList allows every member and every name but refuses the list of kinds in ns.
type denyList struct {
	ns    string
	kinds []string
}

func (denyList) Authorize(*http.Request, objects.Ref) bool { return true }

func (denyList) AuthorizeNamedErr(*http.Request, objects.Ref) (bool, error) { return true, nil }

func (d denyList) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return ns != d.ns || !slices.Contains(d.kinds, gk.Kind), nil
}

// A target or ancestor is judged only when the viewer may list its kind in its
// namespace: named get alone gives unknown and unreadable (8.1 re-review: budget).
func TestPolicyJudgedOnlyWithList(t *testing.T) {
	e := policyBase(t, ruleChange{name: "base"})
	srv := policyServe(t, e, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyList{ns: ns0, kinds: []string{"HTTPRoute", "Gateway"}}}
	})
	d := decode[api.PolicyDetail](t, srv, fullUser, tpPath(ns0, tpAncestors))
	if got := outcomes(d.Targets); !slices.Equal(got, []string{computed.TargetUnknown}) {
		t.Errorf("target check %v, want unknown", got)
	}
	if a := d.Ancestors[0]; a.Current || a.NotCurrent != policy.NotCurrentUnreadable {
		t.Errorf("ancestor gw-000: current %v, not current %q; want unreadable", a.Current, a.NotCurrent)
	}
}

// A downstream authoriser that allows the list and denies the named target
// gets unknown, never found; an absent target in a listable namespace is still not found.
func TestPolicyTargetNarrowedIsUnknown(t *testing.T) {
	e := policyBase(t, ruleChange{name: "base"})
	plain := policyServe(t, e, nil)
	narrowed := policyServe(t, e, func(a api.Authorizer) api.Authorizer {
		return api.Narrowed{Base: a, Policy: denyNamed{name: namesHidden}}
	})
	for _, c := range []struct {
		srv  *httptest.Server
		want string
	}{{plain, computed.TargetFound}, {narrowed, computed.TargetUnknown}} {
		d := decode[api.PolicyDetail](t, c.srv, fullUser, tpPath(ns0, tpAncestors))
		if got := d.Targets[0].Check.Outcome; got != c.want {
			t.Errorf("names-hidden check %q, want %q", got, c.want)
		}
		if c.want == computed.TargetUnknown && (d.Targets[0].Ref.Link != api.LinkNone || d.Targets[0].Health != "") {
			t.Errorf("a target denied by name is linked: %+v", d.Targets[0])
		}
		rows := decode[api.PoliciesResponse](t, c.srv, fullUser, policiesPath+"?limit=200")
		if got := outcomes(policyRowOf(t, rows, kgwGroup, ns0, tpAncestors).Targets); !slices.Equal(got, []string{c.want}) {
			t.Errorf("list check %v, want %q", got, c.want)
		}
		if got := outcomes(policyRowOf(t, rows, kgwGroup, ns0, tpVisible).Targets); !slices.Equal(got, []string{computed.TargetNotFound}) {
			t.Errorf("absent target %v", got)
		}
	}
}

// In a scoped snapshot whose HTTPRoute watch is denied in the hidden namespace
// only, a target check in ns-000, where routes are collected, still answers (8.1 review #6).
func TestPolicyTargetWatchPerNamespace(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "scoped", apply: func(s ruleSnap) {
		s.NamespaceAccess = map[schema.GroupVersionResource]map[string]cache.Access{}
		for _, n := range s.Namespaces {
			s.RequestedNamespaces = append(s.RequestedNamespaces, n.Name)
		}
		slices.Sort(s.RequestedNamespaces)
		for gvr := range s.Access {
			s.NamespaceAccess[gvr] = map[string]cache.Access{}
			for _, ns := range s.RequestedNamespaces {
				s.NamespaceAccess[gvr][ns] = cache.Access{OK: true}
			}
		}
		routes := testutil.ServedUpstream()[slices.IndexFunc(testutil.ServedUpstream(), func(k kinds.Served) bool { return k.Kind == "HTTPRoute" })].GVR()
		denied := cache.Access{Err: "forbidden", Denied: true}
		s.NamespaceAccess[routes][hiddenNS], s.Access[routes] = denied, denied
	}}), nil)
	for _, user := range []string{fullUser, restrictedUser} {
		d := decode[api.PolicyDetail](t, srv, user, tpPath(ns0, tpVisible))
		if got := outcomes(d.Targets); !slices.Equal(got, []string{computed.TargetNotFound}) {
			t.Errorf("%s: check %v in a collected namespace, want not-found", user, got)
		}
	}
}

// A target the narrowing authoriser denies by name reads unknown whether it
// exists or not: list and detail bytes match for both viewers (8.1 review #1).
func TestPolicyTargetNarrowedExistenceHidden(t *testing.T) {
	const route, pol = "narrowed-route", "tp-narrowed"
	addPolicy := func(s ruleSnap) { s.add(tp(ns0, pol, []any{targetRef(gatewaysGroup, "HTTPRoute", route, "")})) }
	narrow := func(a api.Authorizer) api.Authorizer { return api.Narrowed{Base: a, Policy: denyNamed{name: route}} }
	absent := policyServe(t, policyBase(t, ruleChange{name: "absent", apply: addPolicy}), narrow)
	present := policyServe(t, policyBase(t, ruleChange{name: "present", apply: func(s ruleSnap) {
		addPolicy(s)
		s.add(routeTo(ns0, route, gw0(), ns0, "be-0000", true))
	}}), narrow)
	for _, user := range []string{fullUser, restrictedUser} {
		for _, path := range []string{policiesPath + "?limit=200", policiesPath + "?limit=200&hasProblems=true",
			policiesPath + "?limit=200&hasProblems=false", tpPath(ns0, pol)} {
			ac, ab := fetch(t, absent, user, path)
			pc, pb := fetch(t, present, user, path)
			if ac != pc || ab != pb {
				t.Errorf("%s %s: absent and present target differ:\n  %d %.400s\n  vs %d %.400s", user, path, ac, ab, pc, pb)
			}
		}
		d := decode[api.PolicyDetail](t, absent, user, tpPath(ns0, pol))
		if got := outcomes(d.Targets); !slices.Equal(got, []string{computed.TargetUnknown}) {
			t.Errorf("%s: absent narrowed target %v, want unknown", user, got)
		}
	}
}

// Two exact duplicate ancestor entries (one Gateway, one controller, one condition
// type, different messages) give two KGWUI-002s with distinct IDs whose detail URLs
// resolve; a hidden sibling that starts a finding moves neither (8.1 fix2: duplicate 002).
func TestPolicyAncestorDuplicateIDs(t *testing.T) {
	const pol, hiddenGw = "tp-duplicates", "anc-first"
	dup := func(msg string) testutil.PolicyAncestor {
		c := testutil.Cond("Accepted", metav1.ConditionFalse, "Invalid")
		c.Message = msg
		return testutil.PolicyAncestor{Ref: gw0()[0], Controller: testutil.Controller, Conds: []metav1.Condition{c}}
	}
	addPolicy := func(s ruleSnap) {
		s.add(tp(ns0, pol, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")},
			ancestor(hiddenGwRef(hiddenGw), testutil.Controller, metav1.ConditionFalse, metav1.ConditionTrue), dup("first"), dup("second")))
	}
	absent := policyServe(t, policyBase(t, ruleChange{name: "absent", apply: addPolicy}), nil)
	present := policyServe(t, policyBase(t, ruleChange{name: "present", apply: func(s ruleSnap) {
		addPolicy(s)
		s.add(testutil.Gateway(hiddenNS, hiddenGw, scale.Class, nil))
	}}), nil)
	ids := func(srv *httptest.Server) map[string]string {
		out := map[string]string{}
		page := decode[map[string]any](t, srv, restrictedUser, ruleBase+"/findings?limit=200&code=KGWUI-002")
		for _, x := range page["items"].([]any) {
			f := x.(map[string]any)
			if res, _ := f["resource"].(map[string]any); res != nil && res["name"] == pol {
				d, _ := f["detail"].(string)
				out[d] = f["id"].(string)
			}
		}
		return out
	}
	before, after := ids(absent), ids(present)
	distinct := map[string]bool{}
	for _, id := range before {
		distinct[id] = true
	}
	if len(before) != 2 || len(distinct) != 2 || !maps.Equal(before, after) {
		t.Fatalf("duplicate IDs without the hidden Gateway %v, with it %v", before, after)
	}
	for _, id := range before {
		code, body := fetch(t, present, restrictedUser, ruleBase+"/findings/"+id)
		if code != http.StatusOK {
			t.Errorf("detail %s: %d %.200s", id, code, body)
		}
	}
}

// A readable policy's KGWUI-002 about its readable ListenerSet ancestor keeps its ID,
// and its detail URL resolves, when a hidden Gateway ancestor that sorts before it
// starts or stops a finding of its own (8.1 re-review: renumbered 002).
func TestPolicyAncestorFindingIDHiddenSibling(t *testing.T) {
	const pol, hiddenGw = "tp-two-ancestors", "anc-first"
	lsRef := objects.ParentRef{Group: gatewaysGroup, Kind: "ListenerSet", Namespace: ns0, Name: lsAllName}
	addPolicy := func(s ruleSnap) {
		s.add(tp(ns0, pol, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")},
			ancestor(hiddenGwRef(hiddenGw), testutil.Controller, metav1.ConditionFalse, metav1.ConditionTrue),
			ancestor(lsRef, testutil.Controller, metav1.ConditionFalse, metav1.ConditionTrue)))
	}
	absent := policyServe(t, policyBase(t, ruleChange{name: "absent", apply: addPolicy}), nil)
	present := policyServe(t, policyBase(t, ruleChange{name: "present", apply: func(s ruleSnap) {
		addPolicy(s)
		s.add(testutil.Gateway(hiddenNS, hiddenGw, scale.Class, nil))
	}}), nil)
	// ids are the restricted viewer's 002 IDs on the policy, by detail.
	ids := func(srv *httptest.Server, user string) map[string]string {
		out := map[string]string{}
		page := decode[map[string]any](t, srv, user, ruleBase+"/findings?limit=200&code=KGWUI-002")
		for _, x := range page["items"].([]any) {
			f := x.(map[string]any)
			if res, _ := f["resource"].(map[string]any); res != nil && res["name"] == pol {
				d, _ := f["detail"].(string)
				out[d] = f["id"].(string)
			}
		}
		return out
	}
	before, after := ids(absent, restrictedUser), ids(present, restrictedUser)
	if len(before) != 1 || !maps.Equal(before, after) {
		t.Fatalf("restricted IDs without the hidden Gateway %v, with it %v", before, after)
	}
	if full := ids(present, fullUser); len(full) <= len(ids(absent, fullUser)) {
		t.Errorf("the hidden Gateway's finding does not reach the full reader: %v", full)
	}
	for _, id := range before {
		for _, srv := range []*httptest.Server{absent, present} {
			if code, body := fetch(t, srv, restrictedUser, ruleBase+"/findings/"+id); code != http.StatusOK {
				t.Errorf("detail %s: %d %.200s", id, code, body)
			}
		}
	}
}

// A readable ListenerSet ancestor whose parent Gateway is hidden: creating or
// deleting that Gateway moves no byte for the restricted viewer, though it makes
// the ListenerSet owned (8.1 re-review: ListenerSet ownership).
func TestPolicyListenerSetHiddenParentUnchanged(t *testing.T) {
	const pol, ls, hiddenGw = "tp-ls-ancestor", "ls-hidden-parent", "anc-ls-gw"
	lsRef := objects.ParentRef{Group: gatewaysGroup, Kind: "ListenerSet", Namespace: ns0, Name: ls}
	addWorld := func(s ruleSnap) {
		s.add(testutil.ListenerSet(ns0, ls, hiddenGwRef(hiddenGw), nil))
		s.add(tp(ns0, pol, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")},
			ancestor(gw0()[0], testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue),
			ancestor(lsRef, testutil.Controller, metav1.ConditionFalse, metav1.ConditionTrue)))
	}
	absent := policyServe(t, policyBase(t, ruleChange{name: "absent", apply: addWorld}), nil)
	present := policyServe(t, policyBase(t, ruleChange{name: "present", apply: func(s ruleSnap) {
		addWorld(s)
		s.add(testutil.Gateway(hiddenNS, hiddenGw, scale.Class, nil))
	}}), nil)
	for _, path := range []string{policiesPath + "?limit=200", policiesPath + "?limit=200&hasProblems=true", policiesPath + "?limit=200&hasProblems=false",
		tpPath(ns0, pol)} {
		ac, ab := fetch(t, absent, restrictedUser, path)
		pc, pb := fetch(t, present, restrictedUser, path)
		if ac != pc || ab != pb {
			t.Errorf("%s: the hidden parent Gateway shows:\n  %d %.600s\n  vs %d %.600s", path, ac, ab, pc, pb)
		}
	}
	_, fa := fetch(t, absent, fullUser, tpPath(ns0, pol))
	if _, fp := fetch(t, present, fullUser, tpPath(ns0, pol)); fa == fp {
		t.Error("the hidden Gateway does not reach the full reader")
	}
}

// Creating or deleting a hidden Gateway that a readable policy names as a
// fresh ancestor of this install's controller moves no byte for the restricted
// viewer; the full reader sees it become current (8.1 review #2).
func TestPolicyHiddenOwnedAncestorUnchanged(t *testing.T) {
	const pol, hiddenGw = "tp-owned-ancestor", "anc-gw-owned"
	addPolicy := func(s ruleSnap) {
		s.add(tp(ns0, pol, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")},
			ancestor(gw0()[0], testutil.Controller, metav1.ConditionTrue, metav1.ConditionTrue),
			ancestor(hiddenGwRef(hiddenGw), testutil.Controller, metav1.ConditionFalse, metav1.ConditionTrue)))
	}
	absent := policyServe(t, policyBase(t, ruleChange{name: "absent", apply: addPolicy}), nil)
	present := policyServe(t, policyBase(t, ruleChange{name: "present", apply: func(s ruleSnap) {
		addPolicy(s)
		s.add(testutil.Gateway(hiddenNS, hiddenGw, scale.Class, nil))
	}}), nil)
	paths := []string{policiesPath + "?limit=200", policiesPath + "?limit=200&hasProblems=true", policiesPath + "?limit=200&hasProblems=false",
		tpPath(ns0, pol)}
	for _, path := range paths {
		ac, ab := fetch(t, absent, restrictedUser, path)
		pc, pb := fetch(t, present, restrictedUser, path)
		if ac != pc || ab != pb {
			t.Errorf("%s: the hidden Gateway's existence shows:\n  %d %.600s\n  vs %d %.600s", path, ac, ab, pc, pb)
		}
	}
	if _, a := fetch(t, absent, fullUser, tpPath(ns0, pol)); a == func() string { _, p := fetch(t, present, fullUser, tpPath(ns0, pol)); return p }() {
		t.Error("the hidden Gateway does not reach the full reader")
	}
	d := decode[api.PolicyDetail](t, present, restrictedUser, tpPath(ns0, pol))
	if a := d.Ancestors[1]; a.Current || a.NotCurrent != policy.NotCurrentUnreadable || a.Why != "" || a.Ref.Link != api.LinkNone || a.Health != "" {
		t.Errorf("hidden ancestor %+v", a)
	}
}

// The detail gives an ancestor's derived fields their own source apart from its
// controller-reported conditions: current, summary, each condition's staleness and
// a link's health are inferred; so is a target's health (8.1 review #8).
func TestPolicyDetailDerivedSources(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	for _, user := range []string{fullUser, restrictedUser} {
		d := decode[map[string]any](t, srv, user, tpPath(ns0, tpAncestors))
		healths := 0
		for _, x := range d["ancestors"].([]any) {
			a := x.(map[string]any)
			if a["source"] != string(policy.SourceControllerReported) || a["currentSource"] != string(policy.SourceInferred) ||
				a["summarySource"] != string(policy.SourceInferred) || a["staleSource"] != string(policy.SourceInferred) {
				t.Errorf("%s: ancestor sources %v", user, a)
			}
			if _, linked := a["health"]; linked != (a["healthSource"] == string(policy.SourceInferred)) {
				t.Errorf("%s: health %v with source %v", user, a["health"], a["healthSource"])
			}
			if _, linked := a["health"]; linked {
				healths++
			}
		}
		tg := d["targets"].([]any)[0].(map[string]any)
		if tg["source"] != string(policy.SourceDeclared) || tg["health"] == nil || tg["healthSource"] != string(policy.SourceInferred) {
			t.Errorf("%s: target sources %v", user, tg)
		}
		if healths == 0 {
			t.Errorf("%s: no linked ancestor health", user)
		}
	}
}

// Two policies with one kind, namespace and name in two groups are two rows and two details.
func TestPolicyGroupCollision(t *testing.T) {
	s := ruleSnapshot(t)
	served := append(testutil.ServedUpstream(), acmeTrafficPolicy)
	s.Access[acmeTrafficPolicy.GVR()] = s.Access[testutil.ServedUpstream()[0].GVR()]
	acme := tp(ns0, policyTwin, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")})
	acme.SetAPIVersion(acmeGroup + "/v1")
	acme.Object["spec"].(map[string]any)["cors"] = map[string]any{}
	kgw := tp(ns0, policyTwin, []any{targetRef(gatewaysGroup, "HTTPRoute", namesHidden, "")})
	kgw.Object["spec"].(map[string]any)["cors"] = map[string]any{}
	s.Objects[acmeTrafficPolicy.GVR()] = append(s.Objects[acmeTrafficPolicy.GVR()], acme)
	s.add(kgw)
	srv := policyServe(t, policyEngine(t, s, served), nil)
	rows := decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200&namespace="+ns0)
	a, k := policyRowOf(t, rows, acmeGroup, ns0, policyTwin), policyRowOf(t, rows, kgwGroup, ns0, policyTwin)
	if a.Ref.Kind != "TrafficPolicy" || k.Ref.Kind != "TrafficPolicy" || a.Features[0].Label != "" || k.Features[0].Label != "CORS" {
		t.Errorf("rows %+v / %+v", a, k)
	}
	ad := decode[api.PolicyDetail](t, srv, fullUser, policyPath(acmeGroup, "TrafficPolicy", ns0, policyTwin))
	kd := decode[api.PolicyDetail](t, srv, fullUser, tpPath(ns0, policyTwin))
	if ad.Ref.Group != acmeGroup || kd.Ref.Group != kgwGroup || ad.Interpreted || !kd.Interpreted || ad.YAML == kd.YAML {
		t.Errorf("details %+v / %+v", ad.Ref, kd.Ref)
	}
	kindRows := decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200&kind="+acmeGroup+"/TrafficPolicy")
	if len(kindRows.Items) != 1 || kindRows.Items[0].Ref.Group != acmeGroup {
		t.Errorf("kind filter rows %+v", kindRows.Items)
	}
}

// A policy's link opens its detail page, in the list and the detail; the detail's
// YAML path agrees with the install-scoped YAML endpoint.
func TestPolicyDetailLinks(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	for _, user := range []string{fullUser, restrictedUser} {
		d := decode[api.PolicyDetail](t, srv, user, tpPath(ns0, tpVisible))
		want := ruleBase + "/resources/" + kgwGroup + "/TrafficPolicy/" + ns0 + "/" + tpVisible
		// The link is the policy detail endpoint's own path (D-136, 8.2 review #8).
		if d.YAML != want || d.Ref.Link != api.Link(tpPath(ns0, tpVisible)) {
			t.Fatalf("%s: yaml %q link %q, want %q and %q", user, d.YAML, d.Ref.Link, want, tpPath(ns0, tpVisible))
		}
		if row := policyRowOf(t, decode[api.PoliciesResponse](t, srv, user, policiesPath+"?limit=200"), kgwGroup, ns0, tpVisible); row.Ref.Link != api.Link(tpPath(ns0, tpVisible)) {
			t.Errorf("%s: row link %q, want %q", user, row.Ref.Link, tpPath(ns0, tpVisible))
		}
		if code, _ := fetch(t, srv, user, string(d.Ref.Link)); code != http.StatusOK {
			t.Errorf("%s: the link answers %d", user, code)
		}
		code, body := fetch(t, srv, user, d.YAML)
		var obj map[string]any
		if code != http.StatusOK || json.Unmarshal([]byte(body), &obj) != nil {
			t.Fatalf("%s: YAML endpoint %d %.200s", user, code, body)
		}
		if md, _ := obj["metadata"].(map[string]any); md["name"] != tpVisible || obj["kind"] != "TrafficPolicy" {
			t.Errorf("%s: YAML endpoint served %v", user, md)
		}
	}
}

// List and detail requests are audited: the list as list access with its
// filters, the detail as a read of its subject, a refused one as denied.
func TestPolicyAudit(t *testing.T) {
	var mu sync.Mutex
	var events []api.AuditEvent
	hear := api.WithRequestAudit(func(ev api.AuditEvent) { mu.Lock(); events = append(events, ev); mu.Unlock() }, true)
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil, hear)
	last := func() api.AuditEvent { mu.Lock(); defer mu.Unlock(); return events[len(events)-1] }

	fetch(t, srv, fullUser, policiesPath+"?limit=5&namespace="+ns0+"&feature=cors&hasProblems=true&kind="+kgwGroup+"/TrafficPolicy")
	if ev := last(); ev.Kind != api.AuditList || ev.Route != "GET /api/v1/installs/{id}/policies" || ev.Filters["namespace"] != ns0 ||
		ev.Filters["feature"] != "cors" || ev.Filters["hasProblems"] != "true" || ev.Filters["kind"] != kgwGroup+"/TrafficPolicy" {
		t.Errorf("list event %+v", ev)
	}
	ref := objects.Ref{Group: kgwGroup, Kind: "TrafficPolicy", Namespace: ns0, Name: tpVisible}
	fetch(t, srv, fullUser, tpPath(ns0, tpVisible))
	if ev := last(); ev.Kind != api.AuditRead || ev.Subject == nil || *ev.Subject != ref || ev.Denied || !slices.Contains(ev.Returned, ref) {
		t.Errorf("detail event %+v", ev)
	}
	fetch(t, srv, restrictedUser, tpPath(hiddenNS, tpHiddenOld))
	if ev := last(); ev.Kind != api.AuditRead || !ev.Denied || ev.Status != http.StatusNotFound {
		t.Errorf("denied detail event %+v", ev)
	}
}

// forbidden are the words Snellius never writes about a policy (design §3.2).
var forbidden = regexp.MustCompile(`(?i)effective|applied|active`)

// verbatim are the fields whose values come from objects as written or as the
// controller reported them, not from Snellius.
var verbatim = map[string]bool{"conditions": true, "value": true, "matchLabels": true, "name": true, "namespace": true, "sectionName": true,
	"group": true, "kind": true, "controller": true, "key": true, "reason": true, "install": true, "yaml": true, "next": true, "prev": true}

// labels collects every JSON key and every string value Snellius writes in v.
func labels(v any, out *[]string) {
	switch x := v.(type) {
	case map[string]any:
		for k, e := range x {
			*out = append(*out, k)
			if !verbatim[k] {
				labels(e, out)
			}
		}
	case []any:
		for _, e := range x {
			labels(e, out)
		}
	case string:
		*out = append(*out, x)
	}
}

// No label Snellius writes in the policies API says "effective", "applied" or "active".
func TestPolicyLabelsCopy(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	var ls []string
	for _, path := range append(slices.Clone(policySurfaces), tpPath(ns0, tpAncestors)) {
		var v any
		code, body := fetch(t, srv, fullUser, path)
		if code != http.StatusOK && path != tpPath(hiddenNS, tpHiddenNew) {
			t.Fatalf("%s: %d", path, code)
		}
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		labels(v, &ls)
	}
	ls = append(ls, string(policy.SourceDeclared), string(policy.SourceControllerReported), string(policy.SourceControllerReportedContribution),
		string(policy.SourceInferred), string(policy.SourceUnknown), policy.NotCurrentOtherController, policy.NotCurrentNotOwned,
		policy.NotCurrentNotFresh, policy.NotCurrentUnreadable, policy.NoteGatewayWide, policy.NoteBackendPort)
	interp := kgateway.PolicyInterpreter()
	for _, d := range kgateway.Kinds() {
		gk := objects.GroupKind{Group: d.Group, Kind: d.Kind}
		for _, key := range featureKeys {
			ls = append(ls, interp.FeatureLabel(gk, key))
		}
	}
	for _, l := range ls {
		if forbidden.MatchString(l) {
			t.Errorf("label %q says %q", l, forbidden.FindString(l))
		}
	}
	if len(ls) < 50 {
		t.Fatalf("only %d labels checked", len(ls))
	}
}

// featureKeys are the kgateway policy spec keys the pack may label (v2.3–v2.5).
var featureKeys = []string{"transformation", "extProc", "extAuth", "rateLimit", "cors", "csrf", "headerModifiers", "requestMirror",
	"autoHostRewrite", "buffer", "httpUpgrade", "timeouts", "retry", "internalRedirect", "rbac", "jwtAuth", "urlRewrite", "compression",
	"basicAuth", "apiKeyAuth", "oauth2", "tracing", "faultInjection", "acl", "statPrefix", "default", "perPort", "accessLog", "upgradeConfig",
	"acceptHttp10", "defaultHostForHttp10", "xffNumTrustedHops", "serverHeaderTransformation", "streamIdleTimeout", "idleTimeout",
	"connectTimeout", "dns", "perConnectionBufferLimitBytes", "tcpKeepalive", "commonHttpProtocolOptions", "http1ProtocolOptions",
	"http2ProtocolOptions", "tls", "loadBalancer", "healthCheck", "outlierDetection", "circuitBreakers", "upstreamProxyProtocol", "tunnel",
	"sessionPersistence", "status", "body", "bodyFormat"}

// Paging walks the restricted viewer's readable policies through the next cursor, one per page.
func TestPoliciesPaging(t *testing.T) {
	srv := policyServe(t, policyBase(t, ruleChange{name: "base"}), nil)
	all := decode[api.PoliciesResponse](t, srv, restrictedUser, policiesPath+"?limit=200")
	var seen []objects.Ref
	path := policiesPath + "?limit=1"
	for range len(all.Items) + 1 {
		pg := decode[api.PoliciesResponse](t, srv, restrictedUser, path)
		for _, r := range pg.Items {
			seen = append(seen, r.Ref.Ref)
		}
		if pg.Next == "" {
			break
		}
		path = policiesPath + "?limit=1&cursor=" + url.QueryEscape(pg.Next)
	}
	var want []objects.Ref
	for _, r := range all.Items {
		want = append(want, r.Ref.Ref)
	}
	if !slices.Equal(seen, want) {
		t.Errorf("paged %v, want %v", seen, want)
	}
	if code, _ := fetch(t, srv, restrictedUser, policiesPath+"?cursor=bogus"); code != http.StatusBadRequest {
		t.Errorf("bad cursor: %d", code)
	}
	if code, _ := fetch(t, srv, restrictedUser, policiesPath+"?sort=name"); code != http.StatusBadRequest {
		t.Errorf("unknown parameter: %d", code)
	}
}

// kgateway v2.3–v2.5 keep Attached=False/Pending beside a definitive Accepted. A fresh
// rejection is current, Not accepted · Invalid, with a problem and its KGWUI-002; a pending
// or stale block stays not current and says which.
func TestPolicyAncestorAttachedPending(t *testing.T) {
	attachedPending := testutil.Cond("Attached", metav1.ConditionFalse, "Pending")
	rejected := testutil.Cond("Accepted", metav1.ConditionFalse, "Invalid")
	rejected.Message = "extauth: " + ns0 + "/no-such-authz: gateway extension not found"
	staleRejected, staleAttached := rejected, attachedPending
	staleRejected.ObservedGeneration, staleAttached.ObservedGeneration = 0, 0
	cases := []struct {
		name       string
		conds      []metav1.Condition
		current    bool
		notCurrent string
		accepted   string // "status/reason" of the row's Accepted; "" for none
		problems   bool
		finding    string // the policy's finding code; "" for none
	}{
		{"tp-rejected", []metav1.Condition{rejected, attachedPending}, true, "", "False/Invalid", true, health.CodeConditionFalse},
		{"tp-accepted", []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionTrue, "Valid"), attachedPending}, true, "", "True/Valid", false, ""},
		{"tp-pending", []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionFalse, "Pending"), attachedPending}, false, "pending", "", false, health.CodeRouteUnattached},
		{"tp-stale", []metav1.Condition{staleRejected, staleAttached}, false, "stale", "", false, health.CodeRouteUnattached},
	}
	srv := policyServe(t, policyBase(t, ruleChange{name: "attached-pending", apply: func(s ruleSnap) {
		for _, c := range cases {
			s.add(tp(ns0, c.name, []any{targetRef(gatewaysGroup, "Gateway", testutil.ScaleGateway(0), "")},
				testutil.PolicyAncestor{Ref: gw0()[0], Controller: testutil.Controller, Conds: c.conds}))
		}
	}}), nil)
	list := decode[api.PoliciesResponse](t, srv, fullUser, policiesPath+"?limit=200")
	findings := decode[map[string]any](t, srv, fullUser, ruleBase+"/findings?limit=200&namespace="+ns0)
	for _, c := range cases {
		a := decode[api.PolicyDetail](t, srv, fullUser, tpPath(ns0, c.name)).Ancestors[0]
		if a.Current != c.current || a.NotCurrent != c.notCurrent {
			t.Errorf("%s: ancestor current %v, notCurrent %q (%s); want %v, %q", c.name, a.Current, a.NotCurrent, a.Why, c.current, c.notCurrent)
		}
		row := policyRowOf(t, list, kgwGroup, ns0, c.name)
		acc := ""
		if row.Accepted != nil {
			acc = string(row.Accepted.Status) + "/" + row.Accepted.Reason
		}
		if acc != c.accepted || row.Problems.Has != c.problems {
			t.Errorf("%s: row Accepted %q, has problems %v; want %q, %v", c.name, acc, row.Problems.Has, c.accepted, c.problems)
		}
		var codes []string
		for _, x := range findings["items"].([]any) {
			f := x.(map[string]any)
			if res, _ := f["resource"].(map[string]any); res != nil && res["name"] == c.name {
				codes = append(codes, f["code"].(string))
			}
		}
		if want := []string{c.finding}; c.finding == "" && len(codes) != 0 || c.finding != "" && !slices.Equal(codes, want) {
			t.Errorf("%s: findings %v, want %q", c.name, codes, c.finding)
		}
	}
}
