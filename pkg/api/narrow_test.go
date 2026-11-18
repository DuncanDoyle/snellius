package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// namedBatch is batchFake that allows every named ref.
type namedBatch struct{ *batchFake }

func (namedBatch) AuthorizeNamedErr(*http.Request, objects.Ref) (bool, error) { return true, nil }

// listPolicy allows every ref and every list.
type listPolicy struct{ AllowAll }

func (listPolicy) CanList(*http.Request, objects.GroupKind, string) (bool, error) { return true, nil }

// fullPolicy allows every ref and answers the full read with all.
type fullPolicy struct {
	AllowAll
	all bool
}

func (p fullPolicy) FullRead(*http.Request) (bool, error) { return p.all, nil }

var (
	allowEvery = AuthorizerFunc(func(*http.Request, objects.Ref) bool { return true })
	denySecret = AuthorizerFunc(func(_ *http.Request, ref objects.Ref) bool { return ref.Name != "secret" })
)

// narrowedView is the secure-mode view of a request through a.
func narrowedView(a Authorizer) *view {
	r, _ := withAuthzState(httptest.NewRequest(http.MethodGet, "/api/v1/installs", nil))
	return (&handler{authz: a, secure: true}).view(r)
}

// A policy narrows the base's answers and never widens them; the base's batch still answers lists.
func TestNarrowedOnlyNarrows(t *testing.T) {
	shop := ListQuery{Kind: routeGK, Namespace: "shop"}
	for name, policy := range map[string]Authorizer{"allow-all": allowEvery, "deny-secret": denySecret} {
		f := &batchFake{allowed: map[ListQuery]bool{shop: true}}
		v := narrowedView(Narrowed{Base: namedBatch{f}, Policy: policy})
		v.prefetchLists([]ListQuery{shop, {Kind: routeGK, Namespace: "blog"}})
		if len(f.batches) != 1 {
			t.Errorf("%s: the base's batch was not used: %v", name, f.batches)
		}
		if !v.can(routeIn("shop", "web")) || v.can(routeIn("blog", "web")) {
			t.Errorf("%s: shop/web %v, blog/web %v; want only shop", name, v.can(routeIn("shop", "web")), v.can(routeIn("blog", "web")))
		}
		if got, want := v.can(routeIn("shop", "secret")), name == "allow-all"; got != want {
			t.Errorf("%s: shop/secret as a member %v, want %v", name, got, want)
		}
		if got, want := v.canNamed(routeIn("blog", "secret")), name == "allow-all"; got != want {
			t.Errorf("%s: blog/secret by name %v, want %v", name, got, want)
		}
		// A policy that cannot answer lists narrows every list to a denial.
		if v.canList(routeGK, "shop") {
			t.Errorf("%s: a list the policy cannot answer is allowed", name)
		}
	}
	f := &batchFake{allowed: map[ListQuery]bool{shop: true}}
	v := narrowedView(Narrowed{Base: f, Policy: listPolicy{}})
	if !v.canList(routeGK, "shop") || v.canList(routeGK, "blog") {
		t.Error("a list-answering policy does not AND with the base's lists")
	}
}

// The full read holds only when the base and the policy both say so.
func TestNarrowedFullRead(t *testing.T) {
	for _, c := range []struct {
		base   bool
		policy Authorizer
		want   bool
	}{
		{true, allowEvery, false},
		{true, fullPolicy{all: true}, true},
		{true, fullPolicy{all: false}, false},
		{false, fullPolicy{all: true}, false},
	} {
		v := narrowedView(Narrowed{Base: &fullAuthz{errAuthz: &errAuthz{}, all: c.base}, Policy: c.policy})
		if got := v.readsAll(); got != c.want {
			t.Errorf("base %v, policy %T: full read %v, want %v", c.base, c.policy, got, c.want)
		}
	}
}

// One authoriser's full read never answers another's in the same request.
func TestFullReadIsPerAuthorizer(t *testing.T) {
	r, _ := withAuthzState(httptest.NewRequest(http.MethodGet, "/api/ext/x/y", nil))
	other := &fullAuthz{errAuthz: &errAuthz{}, all: true}
	sar := &fullAuthz{errAuthz: &errAuthz{}, all: false}
	if !ViewerOf(r, other).ReadsAll() {
		t.Fatal("the separate FullReader's yes is not a full read")
	}
	if ViewerOf(r, Narrowed{Base: sar, Policy: fullPolicy{all: true}}).ReadsAll() {
		t.Error("the narrowed view got a full read its base denies")
	}
	if sar.readCount() != 1 {
		t.Errorf("the narrowed view's base was asked %d times, want 1", sar.readCount())
	}
	// The same authoriser is still asked once per request.
	ViewerOf(r, other).ReadsAll()
	if other.readCount() != 1 {
		t.Errorf("the separate FullReader was asked %d times, want 1", other.readCount())
	}
}

// A narrowed authoriser keeps the base's admission, and extensions' Visible ANDs the policy.
func TestNarrowedKeepsAdmission(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	a := &admitAuthz{expiringAuthz: &expiringAuthz{clock: clk, spend: true}}
	deny1 := AuthorizerFunc(func(_ *http.Request, ref objects.Ref) bool { return ref.Name != "1" })
	srv, b, _ := expiringServer(t, Narrowed{Base: a, Policy: deny1}, clk, 3)
	code, _, body := fetchRaw(t, srv, "/api/ext/b/n")
	if code != http.StatusOK || body != `{"build":2,"visible":2}`+"\n" || b.count() != 2 {
		t.Errorf("%d %q after %d builds", code, body, b.count())
	}
	if a.admits.Load() != 1 || a.releases.Load() != 1 {
		t.Errorf("%d admits, %d releases; want 1, 1", a.admits.Load(), a.releases.Load())
	}
}

// In secure mode the extension helpers deny everything when given no authoriser.
func TestExtHelpersNilAuthorizerFailsClosed(t *testing.T) {
	ref := routeIn("shop", "web")
	items := []objects.Ref{ref}
	secureReq, _ := withAuthzState(httptest.NewRequest(http.MethodGet, "/api/ext/x/y", nil))
	if ViewerOf(secureReq, nil).Can(ref) {
		t.Error("secure ViewerOf(nil) allows a ref")
	}
	if vis, hidden := Visible(secureReq, nil, items, func(r objects.Ref) objects.Ref { return r }); len(vis) != 0 || hidden != 0 {
		t.Errorf("secure Visible(nil) = %v, %d; want none, 0", vis, hidden)
	}
	localReq := httptest.NewRequest(http.MethodGet, "/api/ext/x/y", nil)
	if !ViewerOf(localReq, nil).Can(ref) {
		t.Error("local ViewerOf(nil) denies a ref")
	}
	if vis, _ := Visible(localReq, nil, items, func(r objects.Ref) objects.Ref { return r }); len(vis) != 1 {
		t.Errorf("local Visible(nil) = %v; want every item", vis)
	}
}
