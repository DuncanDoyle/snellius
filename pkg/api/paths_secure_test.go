package api_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/endpoints"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/paths"
)

// Handler tests of paths and backends in secure mode (Task 35): shown signals
// only, endpoint counts behind EndpointSlice list, and Pods by name only.

// emptySvc is a Service in ns0, where the restricted viewer may list
// EndpointSlices, whose one slice has no ready endpoint.
const emptySvc = "empty-svc"

// withEmpty adds emptySvc and a visible route on gw0 to it.
func withEmpty(s ruleSnap) {
	s.Services = append(s.Services, service(ns0, emptySvc))
	s.EndpointSlices = append(s.EndpointSlices, slice(ns0, emptySvc, 0))
	s.add(serviceRoute(ns0, emptySvc+"-route", gw0(), ns0, emptySvc))
}

// scopedReads records the Services the scoped reader is asked about and
// answers a complete read naming each endpoint's Pod.
type scopedReads struct {
	mu   sync.Mutex
	asks []objects.Ref
}

func (s *scopedReads) Endpoints(_ context.Context, _ string, svc objects.Ref) (endpoints.Result, error) {
	s.mu.Lock()
	s.asks = append(s.asks, svc)
	s.mu.Unlock()
	return endpoints.Result{Complete: endpoints.Complete, ReadAt: testutil.Now, Items: []endpoints.Endpoint{
		{Address: "10.0.0.7", Port: 8080, Ready: true, Serving: true, Pod: svc.Name + "-pod-a"}}}, nil
}

// asked reports whether the reader was asked about Service ns/name.
func (s *scopedReads) asked(ns, name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.ContainsFunc(s.asks, func(r objects.Ref) bool { return r.Namespace == ns && r.Name == name })
}

// svcRef is the Service ns/name.
func svcRef(ns, name string) objects.Ref {
	return objects.Ref{Kind: "Service", Namespace: ns, Name: name}
}

// pathTo is user's first /paths row sending to Service ns/name.
func pathTo(t *testing.T, srv *httptest.Server, user, ns, name string) api.PathRowDTO {
	t.Helper()
	for _, r := range decode[api.PathsResponse](t, srv, user, ruleBase+"/paths?limit=200&q="+name).Rows {
		if r.Backend != nil && r.Backend.Namespace == ns && r.Backend.Name == name {
			return r
		}
	}
	t.Fatalf("%s: no path to %s/%s", user, ns, name)
	return api.PathRowDTO{}
}

// backendRow is user's /backends row of Service ns/name.
func backendRow(t *testing.T, srv *httptest.Server, user, ns, name string) api.BackendRowDTO {
	t.Helper()
	return rowOf(t, decode[api.BackendsResponse](t, srv, user, ruleBase+"/backends?limit=200").Rows,
		func(r api.BackendRowDTO) api.LinkedRef { return r.Ref }, ns, name)
}

// serviceDetail is user's backend detail of Service ns/name.
func serviceDetail(t *testing.T, srv *httptest.Server, user, ns, name string) api.BackendDetail {
	t.Helper()
	return decode[api.BackendDetail](t, srv, user, ruleBase+"/backends/-/Service/"+ns+"/"+name)
}

// hasCode reports whether p has a signal of kind and code.
func hasCode(p api.ProblemDTO, kind, code string) bool {
	return slices.ContainsFunc(p.Signals, func(s api.SignalDTO) bool { return s.Kind == kind && s.Code == code })
}

// notPermitted fails unless e is a not-permitted summary without counts.
func notPermitted(t *testing.T, where string, e api.EndpointsDTO) {
	t.Helper()
	if e.Complete != paths.EndpointsNotPermitted || e.Ready != nil || e.Total != nil || len(e.Items) > 0 || !e.ReadAt.IsZero() {
		t.Errorf("%s: endpoints %+v, want not-permitted with no counts or read time", where, e)
	}
}

// counted fails unless e is a complete read of ready of total.
func counted(t *testing.T, where string, e api.EndpointsDTO, ready, total int) {
	t.Helper()
	if e.Complete != paths.EndpointsComplete || e.Ready == nil || e.Total == nil || *e.Ready != ready || *e.Total != total {
		t.Errorf("%s: endpoints %+v, want complete %d of %d", where, e, ready, total)
	}
}

// Endpoint counts need list on EndpointSlices in the Service's namespace
// (design §7.6). limited-svc sits where the restricted viewer lists Services
// but not EndpointSlices; emptySvc where it lists both.
func TestEndpointsNeedSliceList(t *testing.T) {
	reads := &scopedReads{}
	srv := ruleServe(t, worldPlus(t, withEmpty, change("faulty")), api.WithScopedReader(reads))
	code := health.CodeBackendNoReadyEndpoints

	// Without the grant: not-permitted, no counts, unknown, and no scoped read.
	row := pathTo(t, srv, restrictedUser, kindLimitedNS, limitedSvc)
	notPermitted(t, "/paths", *row.Endpoints)
	be := backendRow(t, srv, restrictedUser, kindLimitedNS, limitedSvc)
	notPermitted(t, "/backends", be.Endpoints[0])
	d := serviceDetail(t, srv, restrictedUser, kindLimitedNS, limitedSvc)
	for _, s := range d.Sources {
		notPermitted(t, "detail", s)
	}
	for _, c := range d.PortChain {
		if len(c.SlicePorts) > 0 || c.NoSlicePort {
			t.Errorf("detail port chain %+v reads slices", c)
		}
	}
	for name, p := range map[string]api.ProblemDTO{"/paths": row.Problem, "/backends": be.Problem, "detail": d.Problem} {
		if p.State != health.StateUnknown || !hasCode(p, paths.SignalCoverage, paths.EndpointsNotPermitted) || hasCode(p, paths.SignalFinding, code) {
			t.Errorf("%s: problem %+v, want unknown from not-permitted endpoints, no 023", name, p)
		}
	}
	if reads.asked(kindLimitedNS, limitedSvc) {
		t.Error("the scoped reader was asked about a Service whose slices the viewer may not list")
	}

	// With the grant: counts as read, unknown at worst; 023 is derived, so absent.
	row = pathTo(t, srv, restrictedUser, ns0, emptySvc)
	counted(t, "/paths (grant)", *row.Endpoints, 0, 2)
	be = backendRow(t, srv, restrictedUser, ns0, emptySvc)
	counted(t, "/backends (grant)", be.Endpoints[0], 0, 2)
	for name, p := range map[string]api.ProblemDTO{"/paths": row.Problem, "/backends": be.Problem} {
		if p.State != health.StateUnknown || !hasCode(p, paths.SignalWaiting, code) || hasCode(p, paths.SignalFinding, code) {
			t.Errorf("%s (grant): problem %+v, want waiting on the empty port, no 023", name, p)
		}
	}
	if serviceDetail(t, srv, restrictedUser, ns0, emptySvc); !reads.asked(ns0, emptySvc) {
		t.Error("the scoped reader was not asked with the EndpointSlice grant")
	}

	// A full reader sees the 023 on both.
	for _, svc := range [][2]string{{kindLimitedNS, limitedSvc}, {ns0, emptySvc}} {
		if p := pathTo(t, srv, fullUser, svc[0], svc[1]).Problem; !hasCode(p, paths.SignalFinding, code) || p.State != health.StateWarning {
			t.Errorf("full %s: problem %+v, want the 023 warning", svc[1], p)
		}
	}
}

// A full reader's paths and backends are those of the handler without the
// projection: Projection.For(nil) changes nothing.
func TestEndpointsFullReadUnchanged(t *testing.T) {
	src := worldPlus(t, withEmpty, change("faulty"))
	reads := &scopedReads{}
	srv, bypass := ruleServe(t, src, api.WithScopedReader(reads)), ruleServe(t, src, api.WithScopedReader(&scopedReads{}), api.WithProjectionBypass())
	for _, p := range []string{"/paths?limit=200", "/backends?limit=200", "/backends/-/Service/" + kindLimitedNS + "/" + limitedSvc,
		"/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + kindLimitedNS + "/" + limitedSvc + "-route"} {
		if _, a := fetch(t, srv, fullUser, ruleBase+p); a != func() string { _, b := fetch(t, bypass, fullUser, ruleBase+p); return b }() {
			t.Errorf("%s differs for the full reader", p)
		}
	}
	counted(t, "full /paths", *pathTo(t, srv, fullUser, kindLimitedNS, limitedSvc).Endpoints, 0, 2)
	if !reads.asked(kindLimitedNS, limitedSvc) {
		t.Error("the full reader's backend detail did not read endpoints")
	}
}

// Every finding signal a viewer sees on a path or backend is a finding that
// viewer is shown, matched by code and resource; a full reader sees more.
func TestPathSignalsShownOnly(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, withEmpty, change("faulty")))
	extra := 0
	for _, user := range []string{restrictedUser, fullUser} {
		shown := map[string]bool{}
		for _, f := range decode[api.Page[api.FindingRow]](t, srv, restrictedUser, ruleBase+"/findings?limit=200").Items {
			if f.Resource != nil {
				shown[f.Code+" "+f.Resource.Namespace+"/"+f.Resource.Name] = true
			}
		}
		var sets []api.ProblemDTO
		for _, r := range decode[api.PathsResponse](t, srv, user, ruleBase+"/paths?limit=200").Rows {
			sets = append(sets, r.Problem)
		}
		for _, r := range decode[api.BackendsResponse](t, srv, user, ruleBase+"/backends?limit=200").Rows {
			sets = append(sets, r.Problem)
		}
		for _, p := range sets {
			for _, s := range p.Signals {
				if s.Kind != paths.SignalFinding || s.Ref == nil || shown[s.Code+" "+s.Ref.Namespace+"/"+s.Ref.Name] {
					continue
				}
				if user == restrictedUser {
					t.Errorf("restricted sees signal %s on %s/%s, a finding it is not shown", s.Code, s.Ref.Namespace, s.Ref.Name)
				}
				extra++
			}
		}
	}
	if extra == 0 {
		t.Fatal("the full reader sees no finding signal the restricted viewer is not shown; the fixture tests nothing")
	}
	// An own-object finding the viewer is shown keeps its signal and title.
	if p := pathTo(t, srv, restrictedUser, ns0, "absent-svc").Problem; !slices.ContainsFunc(p.Signals, func(s api.SignalDTO) bool {
		return s.Kind == paths.SignalFinding && s.Code == health.CodeRefUnresolved && s.Title != ""
	}) {
		t.Errorf("absent-consumer's 013 signal is gone: %+v", p)
	}
}

// Filters, order, summary, facets and revision all run over the viewer's
// projection: a 001 the restricted viewer is not shown leaves its answers as
// in a world without it, while it makes rows error for the full reader.
func TestQueryOverViewerProjection(t *testing.T) {
	without := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	with := ruleServe(t, ruleWorld(t, ruleChange{name: "rejectsGw0", rejects: []ctrlmetrics.GatewayKey{{Namespace: ns0, Name: testutil.ScaleGateway(0)}}}))
	queries := []string{"/paths?limit=200", "/paths?limit=200&state=error", "/paths?limit=200&state=healthy", "/paths?limit=5&sort=problem",
		"/paths?limit=5&sort=problem.desc", "/backends?limit=200", "/backends?limit=200&state=error", "/backends?limit=5&sort=problem"}
	for _, q := range queries {
		_, a := fetch(t, without, restrictedUser, ruleBase+q)
		if _, b := fetch(t, with, restrictedUser, ruleBase+q); a != b {
			t.Errorf("%s: the restricted viewer's answer depends on a 001 it is not shown; differing %v", q, diffPaths(t, a, b))
		}
	}
	for _, q := range []string{"/paths?limit=200&state=error", "/backends?limit=200&state=error"} {
		_, a := fetch(t, without, fullUser, ruleBase+q)
		if _, b := fetch(t, with, fullUser, ruleBase+q); a == b {
			t.Errorf("%s: the 001 does not reach the full reader; the fixture tests nothing", q)
		}
	}
	if r := decode[api.PathsResponse](t, with, fullUser, ruleBase+"/paths?limit=200&state=error"); r.Summary.ByState["error"].Paths == 0 {
		t.Error("no error path for the full reader")
	}
}

// A route detail's trace reads the same projection as /paths: each
// backendRef's state is the worst of its rows', for either viewer.
func TestTraceUsesViewerProjection(t *testing.T) {
	src := worldPlus(t, withEmpty, ruleChange{name: "faultyRejects", apply: change("faulty").apply,
		rejects: []ctrlmetrics.GatewayKey{{Namespace: ns0, Name: testutil.ScaleGateway(0)}}})
	srv := ruleServe(t, src)
	routes := map[string]string{kindLimitedNS: limitedSvc + "-route", ns0: "svc-consumer"}
	states := map[string]health.State{}
	for ns, route := range routes {
		for _, user := range []string{restrictedUser, fullUser} {
			worst := health.StateHealthy
			n := 0
			for _, r := range decode[api.PathsResponse](t, srv, user, ruleBase+"/paths?limit=200&route="+objects.GatewayAPIGroup+"/HTTPRoute/"+ns+"/"+route).Rows {
				n++
				if rank(r.Problem.State) < rank(worst) {
					worst = r.Problem.State
				}
			}
			d := decode[api.RouteDetail](t, srv, user, ruleBase+"/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+ns+"/"+route)
			b := d.Rules[0].BackendRefs[0]
			if n == 0 || b.Problem == nil || b.Problem.State != worst {
				t.Errorf("%s %s: trace %+v, want %s over %d rows", user, route, b.Problem, worst, n)
			}
			for _, s := range d.Serves {
				if user == restrictedUser && s.State == health.StateError {
					t.Errorf("%s: serves %+v is error from a 001 the viewer is not shown", route, s)
				}
			}
			states[user+" "+route] = worst
		}
	}
	if states[restrictedUser+" svc-consumer"] == states[fullUser+" svc-consumer"] {
		t.Errorf("the 001 does not reach the full reader's trace: %v", states)
	}
	if b := decode[api.RouteDetail](t, srv, restrictedUser, ruleBase+"/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+kindLimitedNS+"/"+limitedSvc+"-route").Rules[0].BackendRefs[0]; b.Endpoints == nil {
		t.Error("no endpoints in the trace")
	} else {
		notPermitted(t, "trace", *b.Endpoints)
	}
}

// rank orders problem states, worst first.
func rank(s health.State) int {
	return slices.Index([]health.State{health.StateError, health.StateWarning, health.StateUnknown, health.StateHealthy}, s)
}

// Secure mode never says what was left out (design §7.5); local mode with a
// restricted authoriser still does (D-077).
func TestNoHiddenFlagInSecureMode(t *testing.T) {
	src := ruleWorld(t, ruleChange{name: "base"})
	secure, local := ruleServe(t, src), localServe(t, src)
	exposure := ruleBase + "/backends/" + kinds.KgatewayGroup + "/Backend/" + ns0 + "/be-0000"
	for _, c := range []struct {
		srv  *httptest.Server
		user string
		want bool
	}{{secure, restrictedUser, false}, {local, "", true}} {
		p := decode[api.PathsResponse](t, c.srv, c.user, ruleBase+"/paths?limit=200").Summary.Hidden
		b := decode[api.BackendsResponse](t, c.srv, c.user, ruleBase+"/backends?limit=200").Hidden
		if p != c.want || b != c.want {
			t.Errorf("user %q: paths hidden %v, backends hidden %v; want %v", c.user, p, b, c.want)
		}
		if c.srv == secure {
			if _, body := fetch(t, c.srv, c.user, exposure); strings.Contains(body, `"hidden"`) {
				t.Errorf("backend detail names hidden paths: %.300s", body)
			}
		}
	}
}

// sarLog wraps ruleChecker and records every SAR it answers.
type sarLog struct {
	mu    sync.Mutex
	calls []sar.Attributes
}

func (l *sarLog) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	l.mu.Lock()
	l.calls = append(l.calls, a)
	l.mu.Unlock()
	return ruleChecker{}.Check(ctx, id, a)
}

// An endpoint row shows the Pod name its EndpointSlice writes, as text: the
// response carries no Pod link or state, and no SAR asks about Pods.
func TestPodLinkNamedOnly(t *testing.T) {
	log := &sarLog{}
	srv := ruleServeWith(t, ruleWorld(t, ruleChange{name: "base"}), log, api.WithScopedReader(&scopedReads{}))
	code, body := fetch(t, srv, restrictedUser, ruleBase+"/backends/-/Service/"+ns0+"/shared")
	if code != 200 {
		t.Fatalf("detail %d %s", code, body)
	}
	var d struct {
		Sources []struct {
			Items []map[string]json.RawMessage `json:"items"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(body), &d); err != nil || len(d.Sources) == 0 || len(d.Sources[0].Items) == 0 {
		t.Fatalf("no endpoint rows: %v %s", err, body[strings.Index(body, `"consumers"`):])
	}
	for _, it := range d.Sources[0].Items {
		if string(it["pod"]) != `"shared-pod-a"` {
			t.Errorf("pod %s, want the name as written", it["pod"])
		}
		for k := range it {
			if !slices.Contains([]string{"address", "port", "ready", "serving", "terminating", "pod"}, k) {
				t.Errorf("endpoint row carries %q beside the Pod's name", k)
			}
		}
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	for _, a := range log.calls {
		if a.Resource == "pods" {
			t.Errorf("a SAR asks about Pods: %+v", a)
		}
	}
}
