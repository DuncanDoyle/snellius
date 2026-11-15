package sar_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

const routesGroup = "gateway.networking.k8s.io"

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// small is the Typical tier cut down so a restricted viewer's route list
// stays under the 500-check limit: one get per route it cannot list.
var small = func() scale.Tier {
	t := scale.Typical
	t.Name, t.Gateways, t.HTTPRoutes, t.ListenerSets, t.Namespaces, t.Backends = "small", 10, 80, 2, 4, 6
	return t
}()

// fixedSnap serves one snapshot.
type fixedSnap struct{ s *cache.Snapshot }

func (f fixedSnap) Snapshot() *cache.Snapshot { return f.s }

// world is the model of the small tier and its install's API base.
func world(t *testing.T) (api.ModelSource, string) {
	t.Helper()
	return worldOf(t, scale.Snapshot(small, 1))
}

// worldOf is the model of snap and its install's API base.
func worldOf(t *testing.T, snap *cache.Snapshot) (api.ModelSource, string) {
	t.Helper()
	for _, objs := range snap.Objects {
		slices.SortFunc(objs, func(x, y *unstructured.Unstructured) int {
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
	}
	e := engine.New(engine.Config{Snapshots: fixedSnap{snap}, Served: testutil.ServedUpstream(), Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: health.Upstream(),
		Now: func() time.Time { return testutil.Now }})
	m := e.Compute()
	if len(m.Installs) != 1 {
		t.Fatalf("%d installs, want 1", len(m.Installs))
	}
	return e, "/api/v1/installs/" + m.Installs[0].Resolved.ID()
}

// servedKinds maps every upstream kind and Deployments to their resources.
type servedKinds struct{}

func (servedKinds) Lookup(group, kind string) (sar.Kind, bool) {
	if group == "apps" && kind == "Deployment" {
		return sar.Kind{Group: "apps", Resource: "deployments", Namespaced: true}, true
	}
	for _, s := range testutil.ServedUpstream() {
		if s.Group == group && s.Kind == kind {
			return sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced}, true
		}
	}
	return sar.Kind{}, false
}

func (k servedKinds) All() ([]sar.Kind, string) {
	var out []sar.Kind
	for _, s := range testutil.ServedUpstream() {
		out = append(out, sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced})
	}
	return out, "upstream"
}

type userKey struct{}

// identityFrom is the injected IdentityFrom: the user a test request names.
func identityFrom(ctx context.Context) (oidc.Identity, bool) {
	u, ok := ctx.Value(userKey{}).(string)
	return oidc.Identity{Username: u}, ok && u != ""
}

// checker answers SARs through decide and records them.
type checker struct {
	mu     sync.Mutex
	calls  []sar.Attributes
	decide func(user string, a sar.Attributes, n int) (bool, error)
}

func (c *checker) Check(_ context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	c.mu.Lock()
	c.calls = append(c.calls, a)
	n := len(c.calls)
	c.mu.Unlock()
	return c.decide(id.Username, a, n)
}

func (c *checker) recorded() []sar.Attributes {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.calls)
}

// serve runs the API in complete secure mode with authz, the user taken from
// X-User. Commit reads t0 unless opts set a clock that moves.
func serve(t *testing.T, src api.ModelSource, authz api.Authorizer, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	opts = append([]api.HandlerOption{api.WithSecureMode(true), api.WithCommitClock(func() time.Time { return t0 })}, opts...)
	h := api.NewHandler(src, fstest.MapFS{}, authz, opts...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// noLimit lifts the global SAR limits: the fake clocks here never refill the rate.
const noLimit = 1 << 30

func newAuthz(c sar.Checker, clk *clocktesting.FakeClock) *sar.Authorizer {
	return sar.New(sar.Options{Checker: c, Kinds: servedKinds{}, Clock: clk, IdentityFrom: identityFrom, MaxInFlight: noLimit, Rate: noLimit, Burst: noLimit})
}

// scanAuthz is newAuthz with --authz-named-get-scan.
func scanAuthz(c sar.Checker, clk *clocktesting.FakeClock) *sar.Authorizer {
	return sar.New(sar.Options{Checker: c, Kinds: servedKinds{}, Clock: clk, IdentityFrom: identityFrom, NamedGetScan: true,
		MaxInFlight: noLimit, Rate: noLimit, Burst: noLimit})
}

// call returns the status and body of GET path as user.
func call(t *testing.T, srv *httptest.Server, user, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest("GET", srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, b
}

// routeNames is the "namespace/name" of each route row user sees, sorted.
func routeNames(t *testing.T, srv *httptest.Server, user, base string) []string {
	t.Helper()
	code, b := call(t, srv, user, base+"/routes?limit=200")
	if code != http.StatusOK {
		t.Fatalf("%s: routes %d %s", user, code, b)
	}
	var p api.Page[api.RouteRow]
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range p.Items {
		out = append(out, r.Ref.Namespace+"/"+r.Ref.Name)
	}
	slices.Sort(out)
	return out
}

// isRoutes reports whether a is about HTTPRoutes.
func isRoutes(a sar.Attributes) bool { return a.Group == routesGroup && a.Resource == "httproutes" }

// grant answers the two viewers: "team" may list HTTPRoutes in ns-000; "named"
// may get the HTTPRoutes in names only (a resourceNames grant).
func grant(names map[string]bool) func(string, sar.Attributes, int) (bool, error) {
	return func(user string, a sar.Attributes, _ int) (bool, error) {
		switch user {
		case "team":
			return isRoutes(a) && a.Namespace == testutil.ScaleNamespace(0) && (a.Verb == "list" || a.Verb == "get"), nil
		case "named":
			return isRoutes(a) && a.Verb == "get" && names[a.Namespace+"/"+a.Name], nil
		}
		return false, nil
	}
}

// Two viewers over SARs see what the same grants give as a plain Authorizer:
// a namespace list grant and, with the named get scan, a resourceNames grant,
// each only its own routes.
func TestTwoViewersOverSAR(t *testing.T) {
	src, base := world(t)
	ns0 := testutil.ScaleNamespace(0) + "/"
	names := map[string]bool{}
	for _, n := range routeNames(t, serve(t, src, api.AllowAll{}), "", base) {
		if len(names) < 3 && !strings.HasPrefix(n, ns0) {
			names[n] = true
		}
	}
	srv := serve(t, src, scanAuthz(&checker{decide: grant(names)}, clocktesting.NewFakeClock(t0)))
	var seen []string
	for user, own := range map[string]func(string) bool{
		"team":  func(n string) bool { return strings.HasPrefix(n, ns0) },
		"named": func(n string) bool { return names[n] },
	} {
		ref := serve(t, src, api.AuthorizerFunc(func(_ *http.Request, r objects.Ref) bool {
			return r.Group == routesGroup && r.Kind == "HTTPRoute" && own(r.Namespace+"/"+r.Name)
		}))
		got, want := routeNames(t, srv, user, base), routeNames(t, ref, user, base)
		if len(got) == 0 || !slices.Equal(got, want) || slices.ContainsFunc(got, func(n string) bool { return !own(n) }) {
			t.Errorf("%s sees %v, want %v", user, got, want)
		}
		if slices.ContainsFunc(got, func(n string) bool { return slices.Contains(seen, n) }) {
			t.Errorf("%s shares rows with the other viewer: %v", user, got)
		}
		seen = append(seen, got...)
	}
}

// A resourceNames-only grant on one HTTPRoute opens its detail, but the route
// list and the Gateway's route counts leave it out (D-100). With the named get
// scan the list and counts include it.
func TestResourceNamesGrantDetailNotList(t *testing.T) {
	src, base := world(t)
	var target string
	var gw objects.Ref
	all := serve(t, src, api.AllowAll{})
	for _, n := range routeNames(t, all, "", base) {
		_, b := call(t, all, "", base+"/routes/"+routesGroup+"/HTTPRoute/"+n)
		var d api.RouteDetail
		if err := json.Unmarshal(b, &d); err != nil {
			t.Fatal(err)
		}
		if len(d.ParentRefs) == 1 && d.ParentRefs[0].Target.Kind == "Gateway" && len(d.Serves) > 0 {
			target, gw = n, d.ParentRefs[0].Target.Ref
			break
		}
	}
	if target == "" {
		t.Fatal("no route attached to one Gateway")
	}
	// The viewer may list Gateways in the Gateway's namespace and get the one route.
	decide := func(_ string, a sar.Attributes, _ int) (bool, error) {
		if a.Group == routesGroup && a.Resource == "gateways" && a.Verb == "list" && a.Namespace == gw.Namespace {
			return true, nil
		}
		return isRoutes(a) && a.Verb == "get" && a.Namespace+"/"+a.Name == target, nil
	}
	counted := func(srv *httptest.Server) int {
		code, b := call(t, srv, "named", base+"/gateways/"+gw.Namespace+"/"+gw.Name)
		var d api.GatewayDetail
		if code != http.StatusOK || json.Unmarshal(b, &d) != nil {
			t.Fatalf("gateway detail %d %s", code, b)
		}
		return d.RouteCounts.Accepted + d.RouteCounts.NotAccepted + d.RouteCounts.Pending + d.RouteCounts.Unwatched
	}
	for _, scan := range []bool{false, true} {
		c := &checker{decide: decide}
		a := newAuthz(c, clocktesting.NewFakeClock(t0))
		if scan {
			a = scanAuthz(c, clocktesting.NewFakeClock(t0))
		}
		srv := serve(t, src, a)
		if code, b := call(t, srv, "named", base+"/routes/"+routesGroup+"/HTTPRoute/"+target); code != http.StatusOK {
			t.Fatalf("scan %v: detail of %s: %d %s", scan, target, code, b)
		}
		want := []string(nil)
		if scan {
			want = []string{target}
		}
		if got := routeNames(t, srv, "named", base); !slices.Equal(got, want) {
			t.Errorf("scan %v: /routes lists %v, want %v", scan, got, want)
		}
		if n := counted(srv); (n > 0) != scan {
			t.Errorf("scan %v: the Gateway counts %d routes", scan, n)
		}
	}
}

// The third SAR of a request fails: the request answers 503, whether it falls
// in the full-read evaluation or in a per-object check after it.
func TestSARFailsMidRequest(t *testing.T) {
	src, base := world(t)
	for _, perObject := range []bool{false, true} {
		var n int
		var mu sync.Mutex
		allow := grant(nil)
		c := &checker{decide: func(user string, a sar.Attributes, _ int) (bool, error) {
			if perObject && a.Verb == "list" && a.Namespace == "" {
				return false, nil // the full read is denied and not counted
			}
			mu.Lock()
			n++
			third := n == 3
			mu.Unlock()
			if third {
				return false, sar.ErrCheck
			}
			return allow(user, a, 0)
		}}
		srv := serve(t, src, newAuthz(c, clocktesting.NewFakeClock(t0)))
		if code, body := call(t, srv, "team", base+"/routes?limit=200"); code != http.StatusServiceUnavailable || string(body) != `{"error":"authz-unavailable"}`+"\n" {
			t.Errorf("perObject=%v: %d %s", perObject, code, body)
		}
	}
}

// A SubjectAccessReview with an evaluation error, even allowed, answers 503.
func TestEvaluationErrorIs503(t *testing.T) {
	src, base := world(t)
	kube := kubefake.NewClientset()
	kube.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: true, EvaluationError: "webhook unavailable"}}, nil
	})
	srv := serve(t, src, newAuthz(sar.NewKubeChecker(kube), clocktesting.NewFakeClock(t0)))
	for _, p := range []string{base + "/routes", base + "/gateways", "/api/v1/installs"} {
		if code, body := call(t, srv, "team", p); code != http.StatusServiceUnavailable || string(body) != `{"error":"authz-unavailable"}`+"\n" {
			t.Errorf("GET %s: %d %s", p, code, body)
		}
	}
}

// A revoked grant stays visible until its decision's start plus the TTL, and no later.
func TestRevokedAfterTTLAtHandler(t *testing.T) {
	src, base := world(t)
	clk := clocktesting.NewFakeClock(t0)
	var mu sync.Mutex
	revoked := false
	team := grant(nil)
	c := &checker{decide: func(user string, a sar.Attributes, n int) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return !revoked && func() bool { ok, _ := team(user, a, n); return ok }(), nil
	}}
	srv := serve(t, src, newAuthz(c, clk), api.WithCommitClock(clk.Now))
	before := routeNames(t, srv, "team", base)
	if len(before) == 0 {
		t.Fatal("team sees no routes")
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	clk.SetTime(t0.Add(sar.DefaultAllowTTL - time.Nanosecond))
	if got := routeNames(t, srv, "team", base); !slices.Equal(got, before) {
		t.Errorf("before the TTL: %v, want %v", got, before)
	}
	clk.SetTime(t0.Add(sar.DefaultAllowTTL))
	if got := routeNames(t, srv, "team", base); len(got) != 0 {
		t.Errorf("at the TTL a revoked viewer still sees %v", got)
	}
}

// A full reader sees AllowAll's answers byte for byte, after only the
// cluster-wide list SARs of the full read.
func TestFullReaderMatchesAllowAll(t *testing.T) {
	src, base := world(t)
	c := &checker{decide: func(_ string, a sar.Attributes, _ int) (bool, error) {
		return a.Verb == "list" && a.Namespace == "", nil
	}}
	srv, ref := serve(t, src, newAuthz(c, clocktesting.NewFakeClock(t0))), serve(t, src, api.AllowAll{})
	for _, p := range []string{base + "/routes?limit=200", base + "/gateways", base + "/backends"} {
		gc, got := call(t, srv, "admin", p)
		wc, want := call(t, ref, "admin", p)
		if gc != http.StatusOK || gc != wc || string(got) != string(want) {
			t.Errorf("GET %s: %d %d, differs from AllowAll", p, gc, wc)
		}
	}
	kinds, _ := servedKinds{}.All()
	calls := c.recorded()
	for _, a := range calls {
		if a.Verb != "list" || a.Namespace != "" || a.Name != "" {
			t.Errorf("per-object SAR %+v", a)
		}
	}
	if len(calls) != len(kinds) {
		t.Errorf("%d SARs, want one list per kind (%d)", len(calls), len(kinds))
	}
}

// countedFull counts the times a request asks the full read.
type countedFull struct {
	*sar.Authorizer
	mu sync.Mutex
	n  int
}

func (c *countedFull) FullRead(r *http.Request) (bool, error) {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
	return c.Authorizer.FullRead(r)
}

func (c *countedFull) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// isClusterList reports whether a is a cluster-wide list, as the full read asks.
func isClusterList(a sar.Attributes) bool { return a.Verb == "list" && a.Namespace == "" }

// namedOnly allows only a get on the route ns/name, while granted.
func namedOnly(ns, name string, granted func() bool) func(string, sar.Attributes, int) (bool, error) {
	return func(_ string, a sar.Attributes, _ int) (bool, error) {
		return granted() && isRoutes(a) && a.Verb == "get" && a.Namespace == ns && a.Name == name, nil
	}
}

// A named get allowed under a denied list expires at its own 60 s; the list
// denial lasts 5 min. At 60 s only the get is sent again: renewed, the detail
// stays 200; revoked, it is 404.
func TestMixedListGetExpiry(t *testing.T) {
	src, base := world(t)
	ns, name, _ := strings.Cut(routeNames(t, serve(t, src, api.AllowAll{}), "", base)[0], "/")
	clk := clocktesting.NewFakeClock(t0)
	var mu sync.Mutex
	granted := true
	c := &checker{decide: namedOnly(ns, name, func() bool { mu.Lock(); defer mu.Unlock(); return granted })}
	srv := serve(t, src, newAuthz(c, clk), api.WithCommitClock(clk.Now))
	detail := base + "/routes/" + routesGroup + "/HTTPRoute/" + ns + "/" + name
	get := sar.Attributes{Verb: "get", Group: routesGroup, Resource: "httproutes", Namespace: ns, Name: name}
	// expect fetches the detail and checks its status and the SARs it sent.
	expect := func(when string, code int, sent []sar.Attributes) {
		t.Helper()
		n := len(c.recorded())
		if got, b := call(t, srv, "named", detail); got != code {
			t.Fatalf("%s: detail %d %s, want %d", when, got, b, code)
		}
		if got := c.recorded()[n:]; !slices.Equal(got, sent) {
			t.Errorf("%s: sent %+v, want %+v", when, got, sent)
		}
	}
	if code, b := call(t, srv, "named", detail); code != http.StatusOK {
		t.Fatalf("detail %d %s", code, b)
	}
	if !slices.Contains(c.recorded(), get) {
		t.Fatal("the named get was never sent")
	}
	clk.SetTime(t0.Add(sar.DefaultAllowTTL - time.Nanosecond))
	expect("before the allow expires", http.StatusOK, nil)
	clk.SetTime(t0.Add(sar.DefaultAllowTTL))
	expect("renewed at 60 s", http.StatusOK, []sar.Attributes{get})
	mu.Lock()
	granted = false
	mu.Unlock()
	clk.SetTime(t0.Add(2 * sar.DefaultAllowTTL))
	expect("revoked at 120 s", http.StatusNotFound, []sar.Attributes{get})
}

// Resource YAML is a purely named operation: no full read, no cluster-wide list.
func TestNamedOnlyDoesNotProbeFullRead(t *testing.T) {
	src, base := world(t)
	ns, name, _ := strings.Cut(routeNames(t, serve(t, src, api.AllowAll{}), "", base)[0], "/")
	c := &checker{decide: namedOnly(ns, name, func() bool { return true })}
	a := &countedFull{Authorizer: newAuthz(c, clocktesting.NewFakeClock(t0))}
	srv := serve(t, src, a)
	if code, b := call(t, srv, "named", base+"/resources/"+routesGroup+"/HTTPRoute/"+ns+"/"+name); code != http.StatusOK {
		t.Fatalf("YAML %d %s", code, b)
	}
	if n := a.count(); n != 0 {
		t.Errorf("YAML asked the full read %d times", n)
	}
	if slices.ContainsFunc(c.recorded(), isClusterList) {
		t.Errorf("YAML sent a cluster-wide list: %+v", c.recorded())
	}
}

// /routes asks the full read once per request; its cached "false" sends no
// cluster-wide list on the next request.
func TestListProbesFullReadOnce(t *testing.T) {
	src, base := world(t)
	c := &checker{decide: grant(nil)}
	a := &countedFull{Authorizer: newAuthz(c, clocktesting.NewFakeClock(t0))}
	srv := serve(t, src, a)
	clusterLists := func() int {
		n := 0
		for _, at := range c.recorded() {
			if isClusterList(at) {
				n++
			}
		}
		return n
	}
	routeNames(t, srv, "team", base)
	if n := a.count(); n != 1 {
		t.Fatalf("one /routes request asked the full read %d times", n)
	}
	first := clusterLists()
	if first == 0 {
		t.Fatal("no cluster-wide list for the full read")
	}
	routeNames(t, srv, "team", base)
	if n, lists := a.count(), clusterLists(); n != 2 || lists != first {
		t.Errorf("second request: %d full reads, %d cluster-wide lists (first %d)", n, lists, first)
	}
}

// Denials are not tracked at commit: two slow denied SARs move the clock past
// the 5 min of the first denials, and the response is still written after one
// build, with no SAR sent twice.
func TestCommitDenyNotTracked(t *testing.T) {
	src, base := world(t)
	clk := clocktesting.NewFakeClock(t0)
	var mu sync.Mutex
	slow := 0
	c := &checker{decide: func(_ string, a sar.Attributes, _ int) (bool, error) {
		if isClusterList(a) {
			return false, nil
		}
		mu.Lock()
		defer mu.Unlock()
		// The first SAR ends at 4 min; the second starts there and ends past t0 + 5 min.
		switch slow++; slow {
		case 1:
			clk.SetTime(t0.Add(4 * time.Minute))
		case 2:
			clk.SetTime(t0.Add(sar.DenyTTL + time.Second))
		}
		return false, nil
	}}
	// One SAR at a time: each must start after the previous one moved the clock.
	srv := serve(t, src, serialOnly{newAuthz(c, clk)}, api.WithCommitClock(clk.Now))
	if got := routeNames(t, srv, "nobody", base); got != nil {
		t.Errorf("nobody sees %v", got)
	}
	if slow < 2 {
		t.Fatalf("%d per-namespace SARs, want at least 2", slow)
	}
	calls := c.recorded()
	for i, a := range calls {
		if slices.Contains(calls[:i], a) {
			t.Errorf("%+v sent twice: the response was rebuilt", a)
		}
	}
}

// An allow used at t, a SAR delayed past t + 60 s, and a revocation in
// between: the response is rebuilt and never shows the revoked routes.
func TestRevokedDuringSlowRequest(t *testing.T) {
	src, base := world(t)
	clk := clocktesting.NewFakeClock(t0)
	ns0 := sar.Attributes{Verb: "list", Group: routesGroup, Resource: "httproutes", Namespace: testutil.ScaleNamespace(0)}
	var mu sync.Mutex
	revoked, allowed, after := false, false, 0
	team := grant(nil)
	c := &checker{decide: func(user string, a sar.Attributes, n int) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		if allowed && !isClusterList(a) {
			// After the allow: one SAR ends at t + 30 s, the next starts there and ends past t + 60 s.
			switch after++; after {
			case 1:
				clk.SetTime(t0.Add(30 * time.Second))
			case 2:
				clk.SetTime(t0.Add(sar.DefaultAllowTTL + time.Second))
				revoked = true
			}
		}
		ok, _ := team(user, a, n)
		ok = ok && !revoked
		allowed = allowed || (ok && a == ns0)
		return ok, nil
	}}
	srv := serve(t, src, newAuthz(c, clk), api.WithCommitClock(clk.Now))
	if got := routeNames(t, srv, "team", base); got != nil {
		t.Errorf("after the revocation team sees %v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if after < 2 {
		t.Fatalf("%d SARs after the allow, want the slow one", after)
	}
	n := 0
	for _, a := range c.recorded() {
		if a == ns0 {
			n++
		}
	}
	if n != 2 {
		t.Errorf("the list in %s was sent %d times, want again on the rebuild", ns0.Namespace, n)
	}
}

// serialOnly is an Authorizer without ResolveLists: every member is decided on its own.
type serialOnly struct{ a *sar.Authorizer }

func (s serialOnly) Authorize(r *http.Request, ref objects.Ref) bool { return s.a.Authorize(r, ref) }
func (s serialOnly) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	return s.a.AuthorizeErr(r, ref)
}
func (s serialOnly) AuthorizeNamedErr(r *http.Request, ref objects.Ref) (bool, error) {
	return s.a.AuthorizeNamedErr(r, ref)
}
func (s serialOnly) CanList(r *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return s.a.CanList(r, gk, ns)
}
func (s serialOnly) FullRead(r *http.Request) (bool, error) { return s.a.FullRead(r) }

// wide is a tier with a Gateway and Backend per namespace, for viewers allowed ten of twelve.
var wide = func() scale.Tier {
	t := small
	t.Name, t.Gateways, t.HTTPRoutes, t.Namespaces, t.Backends = "wide", 12, 72, 12, 12
	return t
}()

// inNamespaces allows every list and get in the first n scale namespaces.
func inNamespaces(n int) func(string, sar.Attributes, int) (bool, error) {
	return func(_ string, a sar.Attributes, _ int) (bool, error) {
		for i := range n {
			if a.Namespace == testutil.ScaleNamespace(i) {
				return true, nil
			}
		}
		return false, nil
	}
}

// objectsOf returns the kind objects of snap.
func objectsOf(snap *cache.Snapshot, kind string) []*unstructured.Unstructured {
	for _, objs := range snap.Objects {
		if len(objs) > 0 && objs[0].GetKind() == kind {
			return objs
		}
	}
	return nil
}

// renamed is u named name, with a UID of its own.
func renamed(u *unstructured.Unstructured, ns, name string) *unstructured.Unstructured {
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetUID(types.UID(u.GetKind() + "/" + ns + "/" + name))
	return u
}

// addTo adds u to snap under the kind it shares with an existing object.
func addTo(t *testing.T, snap *cache.Snapshot, u *unstructured.Unstructured) {
	t.Helper()
	for gvr, objs := range snap.Objects {
		if len(objs) > 0 && objs[0].GetKind() == u.GetKind() {
			snap.Objects[gvr] = append(objs, u)
			return
		}
	}
	t.Fatalf("no %s in the snapshot", u.GetKind())
}

// removeFrom drops kind ns/name from snap.
func removeFrom(t *testing.T, snap *cache.Snapshot, kind, ns, name string) {
	t.Helper()
	for gvr, objs := range snap.Objects {
		if i := slices.IndexFunc(objs, func(u *unstructured.Unstructured) bool {
			return u.GetKind() == kind && u.GetNamespace() == ns && u.GetName() == name
		}); i >= 0 {
			snap.Objects[gvr] = slices.Delete(objs, i, i+1)
			return
		}
	}
	t.Fatalf("no %s %s/%s", kind, ns, name)
}

// firstIn returns a copy of the first kind object in ns.
func firstIn(t *testing.T, snap *cache.Snapshot, kind, ns string) *unstructured.Unstructured {
	t.Helper()
	for _, objs := range snap.Objects {
		for _, u := range objs {
			if u.GetKind() == kind && u.GetNamespace() == ns {
				return u.DeepCopy()
			}
		}
	}
	t.Fatalf("no %s in %s", kind, ns)
	return nil
}

// hiddenRoute is a copy of an HTTPRoute of ns renamed name, in namespace at,
// on Gateway gw of ns and sending to Backend be of beNS.
func hiddenRoute(t *testing.T, snap *cache.Snapshot, ns, at, name, gw, beNS, be string) *unstructured.Unstructured {
	t.Helper()
	u := renamed(firstIn(t, snap, "HTTPRoute", ns), at, name)
	u.Object["spec"] = map[string]any{
		"parentRefs": []any{map[string]any{"name": gw, "namespace": ns}},
		"rules":      []any{map[string]any{"backendRefs": []any{map[string]any{"group": kinds.KgatewayGroup, "kind": "Backend", "name": be, "namespace": beNS}}}},
	}
	delete(u.Object, "status")
	return u
}

// bodies returns the body of each path for user; each must answer 200.
func bodies(t *testing.T, srv *httptest.Server, user, base string, paths []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, p := range paths {
		code, b := call(t, srv, user, base+p)
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", p, code, b)
		}
		out[p] = string(b)
	}
	return out
}

// masked is b without deniedInInstall, which a hidden create or delete changes until 7.4.
func masked(t *testing.T, b string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(b), &m); err != nil {
		return b
	}
	delete(m, "deniedInInstall")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

var listPages = []string{"/routes?limit=200", "/gateways?limit=200", "/backends?limit=200", "/paths?limit=200"}

func TestPrefetchMatchesSerial(t *testing.T) {
	src, base := world(t)
	snap := scale.Snapshot(small, 1)
	var named []string
	for _, u := range objectsOf(snap, "HTTPRoute") {
		if u.GetNamespace() == testutil.ScaleNamespace(1) && len(named) < 3 {
			named = append(named, u.GetName())
		}
	}
	grants := map[string]func(string, sar.Attributes, int) (bool, error){
		"routes in one namespace": func(_ string, a sar.Attributes, _ int) (bool, error) {
			return isRoutes(a) && a.Namespace == testutil.ScaleNamespace(0), nil
		},
		"two namespaces": inNamespaces(2),
		"named gets and backends": func(_ string, a sar.Attributes, _ int) (bool, error) {
			return a.Verb == "get" && isRoutes(a) && a.Namespace == testutil.ScaleNamespace(1) && slices.Contains(named, a.Name) ||
				a.Verb == "list" && a.Resource == "backends", nil
		},
	}
	for name, decide := range grants {
		for _, scan := range []bool{false, true} {
			mk := newAuthz
			if scan {
				mk = scanAuthz
			}
			pre := serve(t, src, mk(&checker{decide: decide}, clocktesting.NewFakeClock(t0)))
			ser := serve(t, src, serialOnly{mk(&checker{decide: decide}, clocktesting.NewFakeClock(t0))})
			for _, p := range []string{"/routes?limit=200", "/backends?limit=200", "/paths?limit=200"} {
				pc, pb := call(t, pre, "v", base+p)
				sc, sb := call(t, ser, "v", base+p)
				if pc != http.StatusOK || pc != sc || string(pb) != string(sb) {
					t.Errorf("%s, scan %v, %s: prefetched %d, serial %d; bodies equal %v", name, scan, p, pc, sc, string(pb) == string(sb))
				}
				// The scan shows a route granted by name only; without it the list denial is final.
				if shown := strings.Contains(string(pb), `"name":"`+named[0]+`"`); p == "/routes?limit=200" && name == "named gets and backends" && shown != scan {
					t.Errorf("scan %v: named route shown %v", scan, shown)
				}
			}
		}
	}
}

func TestHiddenCreateDeleteUnchanged(t *testing.T) {
	denied := testutil.ScaleNamespace(11)
	base0 := scale.Snapshot(wide, 1)
	created := scale.Snapshot(wide, 1)
	addTo(t, created, renamed(firstIn(t, created, "Gateway", denied), denied, "hidden-gw"))
	addTo(t, created, renamed(firstIn(t, created, "Backend", denied), denied, "hidden-be"))
	addTo(t, created, hiddenRoute(t, created, denied, denied, "hidden", "hidden-gw", denied, "hidden-be"))
	deleted := scale.Snapshot(wide, 1)
	removeFrom(t, deleted, "Backend", denied, firstIn(t, deleted, "Backend", denied).GetName())

	var want map[string]string
	for i, snap := range []*cache.Snapshot{base0, created, deleted} {
		src, base := worldOf(t, snap)
		srv := serve(t, src, newAuthz(&checker{decide: inNamespaces(10)}, clocktesting.NewFakeClock(t0)))
		got := bodies(t, srv, "v", base, listPages)
		if i == 0 {
			want = got
			continue
		}
		if i == 1 && got[listPages[0]] == want[listPages[0]] {
			t.Error("the created route did not change deniedInInstall: it is not in the install")
		}
		for _, p := range listPages {
			if masked(t, got[p]) != masked(t, want[p]) {
				t.Errorf("world %d, %s changed:\n got %.300s\nwant %.300s", i, p, got[p], want[p])
			}
		}
	}
}

// coldSARs is the SARs one request for path sends as user on a fresh
// Authorizer over src, after its full read; and the response.
func coldSARs(t *testing.T, src api.ModelSource, base, path string, decide func(string, sar.Attributes, int) (bool, error)) ([]sar.Attributes, int, string) {
	t.Helper()
	return coldSARsOf(t, src, base, path, decide, false)
}

// coldSARsOf is coldSARs, through an Authorizer without ResolveLists when serial.
func coldSARsOf(t *testing.T, src api.ModelSource, base, path string, decide func(string, sar.Attributes, int) (bool, error), serial bool) ([]sar.Attributes, int, string) {
	t.Helper()
	c := &checker{decide: decide}
	a := newAuthz(c, clocktesting.NewFakeClock(t0))
	r, cancel := asUser("v")
	defer cancel()
	if _, err := a.FullRead(r); err != nil {
		t.Fatal(err)
	}
	var az api.Authorizer = a
	if serial {
		az = serialOnly{a}
	}
	n := len(c.recorded())
	code, b := call(t, serve(t, src, az), "v", base+path)
	return c.recorded()[n:], code, string(b)
}

func TestHiddenRootAddsNoBucket(t *testing.T) {
	team := func(_ string, a sar.Attributes, _ int) (bool, error) {
		return isRoutes(a) && a.Namespace == testutil.ScaleNamespace(0), nil
	}
	fresh := func(a sar.Attributes) bool { return a.Namespace == "fresh" }
	failFresh := func(u string, a sar.Attributes, n int) (bool, error) {
		if fresh(a) {
			return false, sar.ErrCheck
		}
		return team(u, a, n)
	}
	hidden := func(snap *cache.Snapshot, at string) {
		ns := testutil.ScaleNamespace(3)
		addTo(t, snap, hiddenRoute(t, snap, ns, at, "hidden", firstIn(t, snap, "Gateway", ns).GetName(), "fresh", "be"))
	}
	for _, path := range []string{"/routes?limit=200", "/paths?limit=200", "/backends?limit=200"} {
		src0, base := worldOf(t, scale.Snapshot(small, 1))
		sars0, code, want := coldSARs(t, src0, base, path, team)
		if code != http.StatusOK || len(sars0) >= 400 {
			t.Fatalf("%s base: %d, %d SARs", path, code, len(sars0))
		}
		t.Logf("%s: %d SARs cold", path, len(sars0))
		withHidden := scale.Snapshot(small, 1)
		hidden(withHidden, testutil.ScaleNamespace(3))
		src1, _ := worldOf(t, withHidden)
		for _, decide := range []func(string, sar.Attributes, int) (bool, error){team, failFresh} {
			sars, code, got := coldSARs(t, src1, base, path, decide)
			if code != http.StatusOK || slices.ContainsFunc(sars, fresh) || len(sars) != len(sars0) || masked(t, got) != masked(t, want) {
				t.Errorf("%s with the hidden route: %d, %d SARs (base %d), fresh asked %v\n%v\n%v", path, code, len(sars), len(sars0), slices.ContainsFunc(sars, fresh), sars, sars0)
			}
			// Without the prefetch, a hidden route never asks about its targets either.
			sars, code, got = coldSARsOf(t, src1, base, path, decide, true)
			if code != http.StatusOK || slices.ContainsFunc(sars, fresh) || masked(t, got) != masked(t, want) {
				t.Errorf("%s serial with the hidden route: %d, fresh asked %v", path, code, slices.ContainsFunc(sars, fresh))
			}
		}
		// Padding: hidden routes in new namespaces bring the request to the 500-check cap.
		padded := func(withRoute bool) *cache.Snapshot {
			snap := scale.Snapshot(small, 1)
			ns := testutil.ScaleNamespace(3)
			gw := firstIn(t, snap, "Gateway", ns).GetName()
			for i := range 500 - len(sars0) {
				pad := fmt.Sprintf("pad-%03d", i)
				addTo(t, snap, hiddenRoute(t, snap, ns, pad, "p", gw, pad, "be"))
			}
			if withRoute {
				hidden(snap, "pad-000")
			}
			return snap
		}
		for _, withRoute := range []bool{false, true} {
			src, _ := worldOf(t, padded(withRoute))
			sars, code, _ := coldSARs(t, src, base, path, team)
			if code != http.StatusOK || len(sars) != 500 || slices.ContainsFunc(sars, fresh) {
				t.Errorf("%s at the cap, hidden route %v: %d, %d SARs", path, withRoute, code, len(sars))
			}
		}
	}
}

func TestParallelErrorDiscardsResponse(t *testing.T) {
	many := small
	many.Name, many.Namespaces, many.HTTPRoutes, many.Gateways, many.Backends = "many", 300, 600, 300, 300
	src, base := worldOf(t, scale.Snapshot(many, 1))
	sars, code, _ := coldSARs(t, src, base, "/routes?limit=200", inNamespaces(10))
	if code != http.StatusOK || len(sars) < 300 || len(sars) >= 500 {
		t.Fatalf("clean run: %d, %d SARs; want 200 and 300 to 499", code, len(sars))
	}
	var mu sync.Mutex
	n := 0
	allow := inNamespaces(10)
	_, code, body := coldSARs(t, src, base, "/routes?limit=200", func(u string, a sar.Attributes, k int) (bool, error) {
		mu.Lock()
		n++
		fail := n == 150
		mu.Unlock()
		if fail {
			return false, sar.ErrCheck
		}
		return allow(u, a, k)
	})
	if code != http.StatusServiceUnavailable || body != `{"error":"authz-unavailable"}`+"\n" {
		t.Fatalf("one of %d checks failed: %d %.200s", len(sars), code, body)
	}
}

// Concurrent requests over the prefetch path, under -race: each view stays on
// its handler's goroutine and every body equals the serial one.
func TestViewSingleGoroutine(t *testing.T) {
	src, base := worldOf(t, scale.Snapshot(wide, 1))
	decide := inNamespaces(10)
	want := bodies(t, serve(t, src, serialOnly{newAuthz(&checker{decide: decide}, clocktesting.NewFakeClock(t0))}), "v", base, listPages)
	srv := serve(t, src, newAuthz(&checker{decide: decide}, clocktesting.NewFakeClock(t0)))
	var wg sync.WaitGroup
	for range 4 {
		for _, p := range listPages {
			wg.Go(func() {
				code, b := call(t, srv, "v", base+p)
				if code != http.StatusOK || string(b) != want[p] {
					t.Errorf("%s: %d, equal %v", p, code, string(b) == want[p])
				}
			})
		}
	}
	wg.Wait()
}
