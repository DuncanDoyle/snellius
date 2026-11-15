package sar_test

import (
	"context"
	"encoding/json"
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
	snap := scale.Snapshot(small, 1)
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

// serve runs the API in complete secure mode with authz, the user taken from X-User.
func serve(t *testing.T, src api.ModelSource, authz api.Authorizer) *httptest.Server {
	t.Helper()
	h := api.NewHandler(src, fstest.MapFS{}, authz, api.WithSecureMode(true))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, r.Header.Get("X-User"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newAuthz(c sar.Checker, clk *clocktesting.FakeClock) *sar.Authorizer {
	return sar.New(sar.Options{Checker: c, Kinds: servedKinds{}, Clock: clk, IdentityFrom: identityFrom})
}

// scanAuthz is newAuthz with --authz-named-get-scan.
func scanAuthz(c sar.Checker, clk *clocktesting.FakeClock) *sar.Authorizer {
	return sar.New(sar.Options{Checker: c, Kinds: servedKinds{}, Clock: clk, IdentityFrom: identityFrom, NamedGetScan: true})
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
	srv := serve(t, src, newAuthz(c, clk))
	before := routeNames(t, srv, "team", base)
	if len(before) == 0 {
		t.Fatal("team sees no routes")
	}
	mu.Lock()
	revoked = true
	mu.Unlock()
	clk.SetTime(t0.Add(sar.DefaultTTL - time.Nanosecond))
	if got := routeNames(t, srv, "team", base); !slices.Equal(got, before) {
		t.Errorf("before the TTL: %v, want %v", got, before)
	}
	clk.SetTime(t0.Add(sar.DefaultTTL))
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
