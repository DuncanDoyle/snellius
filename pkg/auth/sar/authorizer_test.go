package sar

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

const gwGroup = "gateway.networking.k8s.io"

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// fakeChecker answers through decide and records every SAR it is asked.
type fakeChecker struct {
	mu     sync.Mutex
	calls  []call
	decide func(id oidc.Identity, a Attributes) (bool, error)
}

type call struct {
	user string
	a    Attributes
}

func (f *fakeChecker) Check(_ context.Context, id oidc.Identity, a Attributes) (bool, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{id.Username, a})
	f.mu.Unlock()
	return f.decide(id, a)
}

func (f *fakeChecker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeChecker) recorded() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

// grants allows exactly the listed (user, attributes) pairs.
func grants(allowed ...call) func(oidc.Identity, Attributes) (bool, error) {
	set := map[call]bool{}
	for _, c := range allowed {
		set[c] = true
	}
	return func(id oidc.Identity, a Attributes) (bool, error) { return set[call{id.Username, a}], nil }
}

type fakeKinds map[objects.GroupKind]Kind

func (k fakeKinds) Lookup(group, kind string) (Kind, bool) {
	v, ok := k[objects.GroupKind{Group: group, Kind: kind}]
	return v, ok
}

func (k fakeKinds) All() ([]Kind, string) {
	out := make([]Kind, 0, len(k))
	for _, v := range k {
		out = append(out, v)
	}
	return out, "test"
}

var testKinds = fakeKinds{
	{Group: gwGroup, Kind: "HTTPRoute"}:    {Group: gwGroup, Resource: "httproutes", Namespaced: true},
	{Group: gwGroup, Kind: "GatewayClass"}: {Group: gwGroup, Resource: "gatewayclasses"},
	{Group: "", Kind: "Namespace"}:         {Resource: "namespaces"},
}

type idKey struct{}

func identityFrom(ctx context.Context) (oidc.Identity, bool) {
	id, ok := ctx.Value(idKey{}).(oidc.Identity)
	return id, ok
}

// request returns a request signed in as id.
func request(id oidc.Identity) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/api/routes", nil)
	return r.WithContext(context.WithValue(r.Context(), idKey{}, id))
}

func user(name string) oidc.Identity { return oidc.Identity{Username: name} }

type fixture struct {
	a       *Authorizer
	checker *fakeChecker
	clock   *clocktesting.FakeClock
	reg     *obs.Registry
}

func newFixture(decide func(oidc.Identity, Attributes) (bool, error)) *fixture {
	f := &fixture{checker: &fakeChecker{decide: decide}, clock: clocktesting.NewFakeClock(t0), reg: obs.NewRegistry()}
	f.a = New(Options{Checker: f.checker, Kinds: testKinds, AllowTTL: 30 * time.Second, Clock: f.clock, Metrics: f.reg, IdentityFrom: identityFrom})
	return f
}

func route(ns, name string) objects.Ref {
	return objects.Ref{Group: gwGroup, Kind: "HTTPRoute", Namespace: ns, Name: name}
}

func listAt(ns string) Attributes {
	return Attributes{Verb: "list", Group: gwGroup, Resource: "httproutes", Namespace: ns}
}

func getAt(ns, name string) Attributes {
	return Attributes{Verb: "get", Group: gwGroup, Resource: "httproutes", Namespace: ns, Name: name}
}

func mustAuthorize(t *testing.T, a *Authorizer, id oidc.Identity, ref objects.Ref) bool {
	t.Helper()
	ok, err := a.AuthorizeErr(request(id), ref)
	if err != nil {
		t.Fatalf("AuthorizeErr(%v) error = %v", ref, err)
	}
	return ok
}

func mustAuthorizeNamed(t *testing.T, a *Authorizer, id oidc.Identity, ref objects.Ref) bool {
	t.Helper()
	ok, err := a.AuthorizeNamedErr(request(id), ref)
	if err != nil {
		t.Fatalf("AuthorizeNamedErr(%v) error = %v", ref, err)
	}
	return ok
}

// A population check asks list only; with NamedGetScan a denied list adds the named get.
func TestPopulationIsListOnly(t *testing.T) {
	decide := grants(call{"bob", getAt("shop", "web")})
	f := newFixture(decide)
	if mustAuthorize(t, f.a, user("bob"), route("shop", "web")) || f.a.Authorize(request(user("bob")), route("shop", "web")) {
		t.Error("a named get grant answers a population check")
	}
	if want := []call{{"bob", listAt("shop")}}; !reflect.DeepEqual(f.checker.recorded(), want) {
		t.Fatalf("SARs = %+v, want only the list", f.checker.recorded())
	}
	scan := &fixture{checker: &fakeChecker{decide: decide}, clock: clocktesting.NewFakeClock(t0), reg: obs.NewRegistry()}
	scan.a = New(Options{Checker: scan.checker, Kinds: testKinds, Clock: scan.clock, IdentityFrom: identityFrom, NamedGetScan: true})
	if !mustAuthorize(t, scan.a, user("bob"), route("shop", "web")) {
		t.Error("NamedGetScan: the named get grant does not show shop/web")
	}
	if want := []call{{"bob", listAt("shop")}, {"bob", getAt("shop", "web")}}; !reflect.DeepEqual(scan.checker.recorded(), want) {
		t.Fatalf("NamedGetScan SARs = %+v\nwant %+v", scan.checker.recorded(), want)
	}
}

func TestNamedIsListThenGet(t *testing.T) {
	f := newFixture(grants(call{"alice", listAt("shop")}, call{"bob", getAt("shop", "web")}))
	t.Run("list allowed sends no get", func(t *testing.T) {
		if !mustAuthorizeNamed(t, f.a, user("alice"), route("shop", "web")) {
			t.Fatal("alice may list routes in shop but is denied")
		}
		if want := []call{{"alice", listAt("shop")}}; !reflect.DeepEqual(f.checker.recorded(), want) {
			t.Fatalf("SARs = %+v, want %+v", f.checker.recorded(), want)
		}
	})
	t.Run("resourceNames grant shows the named object only", func(t *testing.T) {
		if !mustAuthorizeNamed(t, f.a, user("bob"), route("shop", "web")) {
			t.Error("bob's named get grant does not show shop/web")
		}
		if mustAuthorizeNamed(t, f.a, user("bob"), route("shop", "admin")) {
			t.Error("bob sees shop/admin without a grant")
		}
		want := []call{{"alice", listAt("shop")}, {"bob", listAt("shop")}, {"bob", getAt("shop", "web")}, {"bob", getAt("shop", "admin")}}
		if !reflect.DeepEqual(f.checker.recorded(), want) {
			t.Fatalf("SARs = %+v\nwant %+v", f.checker.recorded(), want)
		}
	})
}

func TestClusterScopedKinds(t *testing.T) {
	gc := Attributes{Verb: "list", Group: gwGroup, Resource: "gatewayclasses"}
	nsGet := Attributes{Verb: "get", Resource: "namespaces", Name: "shop"}
	f := newFixture(grants(call{"alice", gc}, call{"alice", nsGet}))
	// A namespace on a cluster-scoped ref is ignored.
	if !mustAuthorize(t, f.a, user("alice"), objects.Ref{Group: gwGroup, Kind: "GatewayClass", Namespace: "stray", Name: "kgw"}) {
		t.Error("GatewayClass denied with a cluster-wide list grant")
	}
	if !mustAuthorizeNamed(t, f.a, user("alice"), objects.Ref{Kind: "Namespace", Name: "shop"}) {
		t.Error("Namespace shop denied with a named get grant")
	}
	want := []call{{"alice", gc}, {"alice", Attributes{Verb: "list", Resource: "namespaces"}}, {"alice", nsGet}}
	if !reflect.DeepEqual(f.checker.recorded(), want) {
		t.Fatalf("SARs = %+v\nwant %+v", f.checker.recorded(), want)
	}
}

func TestUnmappedKindDenied(t *testing.T) {
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) { return true, nil })
	for _, ref := range []objects.Ref{
		{Group: "example.com", Kind: "Widget", Namespace: "shop", Name: "w"},
		route("", "web"), // namespaced kind without a namespace
	} {
		if mustAuthorize(t, f.a, user("alice"), ref) {
			t.Errorf("%+v allowed", ref)
		}
	}
	if ok, err := f.a.CanList(request(user("alice")), objects.GroupKind{Group: "example.com", Kind: "Widget"}, "shop"); ok || err != nil {
		t.Errorf("CanList(unmapped) = %v, %v; want false", ok, err)
	}
	if n := f.checker.count(); n != 0 {
		t.Fatalf("%d SARs sent, want none", n)
	}
}

func TestListErrorStopsGet(t *testing.T) {
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) { return false, checkError(categoryRequest) })
	ok, err := f.a.AuthorizeNamedErr(request(user("alice")), route("shop", "web"))
	if ok || !errors.Is(err, ErrCheck) {
		t.Fatalf("AuthorizeNamedErr() = %v, %v; want ErrCheck", ok, err)
	}
	if want := []call{{"alice", listAt("shop")}}; !reflect.DeepEqual(f.checker.recorded(), want) {
		t.Fatalf("SARs = %+v, want only the list", f.checker.recorded())
	}
}

func TestNoIdentityIsError(t *testing.T) {
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) { return true, nil })
	r := httptest.NewRequest(http.MethodGet, "/api/routes", nil)
	if ok, err := f.a.AuthorizeErr(r, route("shop", "web")); ok || !errors.Is(err, ErrNoIdentity) {
		t.Errorf("AuthorizeErr() = %v, %v; want ErrNoIdentity", ok, err)
	}
	if ok, err := f.a.AuthorizeNamedErr(r, route("shop", "web")); ok || !errors.Is(err, ErrNoIdentity) {
		t.Errorf("AuthorizeNamedErr() = %v, %v; want ErrNoIdentity", ok, err)
	}
	if ok, err := f.a.CanList(r, objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}, "shop"); ok || !errors.Is(err, ErrNoIdentity) {
		t.Errorf("CanList() = %v, %v; want ErrNoIdentity", ok, err)
	}
	if f.a.Authorize(r, route("shop", "web")) {
		t.Error("Authorize() without an identity allowed")
	}
	if n := f.checker.count(); n != 0 {
		t.Fatalf("%d SARs sent, want none", n)
	}
}

func TestAuthorizeReportsError(t *testing.T) {
	fail := true
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) {
		if fail {
			return true, checkError(categoryEvaluation)
		}
		return false, nil
	})
	var reported []error
	f.a.report = func(_ *http.Request, err error) { reported = append(reported, err) }
	if f.a.Authorize(request(user("alice")), route("shop", "web")) {
		t.Error("Authorize() allowed on an error")
	}
	if len(reported) != 1 || !errors.Is(reported[0], ErrCheck) {
		t.Fatalf("reported %v, want one ErrCheck", reported)
	}
	fail = false
	if f.a.Authorize(request(user("alice")), route("shop", "web")) || len(reported) != 1 {
		t.Fatalf("a plain denial: reported %v, want nothing more", reported)
	}
}

func TestCanListIgnoresNamedGet(t *testing.T) {
	f := newFixture(grants(call{"bob", getAt("shop", "web")}))
	gk := objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}
	ok, err := f.a.CanList(request(user("bob")), gk, "shop")
	if ok || err != nil {
		t.Fatalf("CanList() = %v, %v; want false", ok, err)
	}
	if want := []call{{"bob", listAt("shop")}}; !reflect.DeepEqual(f.checker.recorded(), want) {
		t.Fatalf("SARs = %+v, want only the list", f.checker.recorded())
	}
	if !mustAuthorizeNamed(t, f.a, user("bob"), route("shop", "web")) {
		t.Fatal("the named get grant does not show shop/web")
	}
	if ok, err := f.a.CanList(request(user("bob")), gk, "shop"); ok || err != nil {
		t.Fatalf("CanList() after the get = %v, %v; want false", ok, err)
	}
}

func TestCanListClusterWide(t *testing.T) {
	f := newFixture(grants(call{"alice", listAt("")}))
	r := request(user("alice"))
	if ok, err := f.a.CanList(r, objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}, ""); !ok || err != nil {
		t.Fatalf("CanList(cluster-wide) = %v, %v; want true", ok, err)
	}
	if ok, err := f.a.CanList(r, objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}, "shop"); ok || err != nil {
		t.Fatalf("CanList(shop) = %v, %v; want false: no grant names shop", ok, err)
	}
	// A cluster-scoped kind is asked cluster-wide whatever namespace is given.
	if _, err := f.a.CanList(r, objects.GroupKind{Group: gwGroup, Kind: "GatewayClass"}, "shop"); err != nil {
		t.Fatal(err)
	}
	want := []call{{"alice", listAt("")}, {"alice", listAt("shop")}, {"alice", Attributes{Verb: "list", Group: gwGroup, Resource: "gatewayclasses"}}}
	if !reflect.DeepEqual(f.checker.recorded(), want) {
		t.Fatalf("SARs = %+v\nwant %+v", f.checker.recorded(), want)
	}
}

func TestCanListSharesCache(t *testing.T) {
	f := newFixture(grants(call{"alice", listAt("shop")}))
	gk := objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}
	if !mustAuthorize(t, f.a, user("alice"), route("shop", "web")) {
		t.Fatal("list grant denied")
	}
	f.clock.Step(29 * time.Second)
	if ok, err := f.a.CanList(request(user("alice")), gk, "shop"); !ok || err != nil || f.checker.count() != 1 {
		t.Fatalf("CanList() = %v, %v with %d SARs; want a cache hit", ok, err, f.checker.count())
	}
	f.clock.Step(time.Second)
	if _, err := f.a.CanList(request(user("alice")), gk, "shop"); err != nil || f.checker.count() != 2 {
		t.Fatalf("at start + TTL: %d SARs, %v; want a new SAR", f.checker.count(), err)
	}
}
