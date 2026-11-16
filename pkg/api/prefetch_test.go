package api

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// batchFake answers list questions from allowed and records each call.
type batchFake struct {
	allowed map[ListQuery]bool
	scan    bool
	err     error
	// batches are the questions of each ResolveLists call; members and lists the per-ref and CanList calls.
	batches [][]ListQuery
	members []objects.Ref
	lists   []ListQuery
}

func (f *batchFake) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, _ := f.AuthorizeErr(r, ref)
	return ok
}

func (f *batchFake) AuthorizeErr(_ *http.Request, ref objects.Ref) (bool, error) {
	f.members = append(f.members, ref)
	return f.allowed[listQueryOf(ref)] || f.scan && ref.Name == "granted", nil
}

func (f *batchFake) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	q := ListQuery{Kind: gk, Namespace: ns}
	f.lists = append(f.lists, q)
	return f.allowed[q], nil
}

func (f *batchFake) ResolveLists(_ *http.Request, qs []ListQuery) ([]bool, error) {
	f.batches = append(f.batches, slices.Clone(qs))
	if f.err != nil {
		return nil, f.err
	}
	out := make([]bool, len(qs))
	for i, q := range qs {
		out[i] = f.allowed[q]
	}
	return out, nil
}

func (f *batchFake) NamedGetScan() bool { return f.scan }

var routeGK = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}

func routeIn(ns, name string) objects.Ref {
	return objects.Ref{Group: routeGK.Group, Kind: routeGK.Kind, Namespace: ns, Name: name}
}

func fakeView(f *batchFake) (*view, *authzState) {
	r, st := withAuthzState(httptest.NewRequest(http.MethodGet, "/api/v1/installs", nil))
	return (&handler{authz: f}).view(r), st
}

// A prefetched denial is a member's answer without the scan; with it, the member is asked by name.
func TestPrefetchedDenialFinalOnlyWithoutScan(t *testing.T) {
	for _, scan := range []bool{false, true} {
		f := &batchFake{allowed: map[ListQuery]bool{{Kind: routeGK, Namespace: "shop"}: true}, scan: scan}
		v, _ := fakeView(f)
		v.prefetchLists([]ListQuery{{Kind: routeGK, Namespace: "shop"}, {Kind: routeGK, Namespace: "blog"}})
		if !v.can(routeIn("shop", "web")) || len(f.members) != 0 {
			t.Fatalf("scan %v: a prefetched allow was asked again: %v", scan, f.members)
		}
		if got := v.can(routeIn("blog", "granted")); got != scan {
			t.Errorf("scan %v: a member granted by name only is %v", scan, got)
		}
		if (len(f.members) == 1) != scan {
			t.Errorf("scan %v: per-member calls %v", scan, f.members)
		}
		f.members = nil
		if v.can(routeIn("other", "x")); len(f.members) != 1 {
			t.Errorf("scan %v: a bucket no round covered was not asked: %v", scan, f.members)
		}
		if !v.canList(routeGK, "shop") || v.canList(routeGK, "blog") || len(f.lists) != 0 {
			t.Errorf("scan %v: canList did not read the table: %v", scan, f.lists)
		}
		if v.canList(routeGK, ""); len(f.lists) != 1 {
			t.Errorf("scan %v: a cluster-wide list was not asked: %v", scan, f.lists)
		}
	}
}

// Round two asks only the dependencies of roots round one admitted, and no round follows an error.
func TestPrefetchRootsFirst(t *testing.T) {
	gw := func(ns string) objects.Ref {
		return objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: ns, Name: "gw"}
	}
	roots := []objects.Ref{routeIn("shop", "web"), routeIn("hidden", "web")}
	deps := map[objects.Ref]objects.Ref{roots[0]: gw("infra"), roots[1]: gw("fresh")}
	run := func(f *batchFake) *authzState {
		v, st := fakeView(f)
		v.prefetch(func(add func(objects.Ref)) {
			for _, r := range roots {
				add(r)
				add(r)
			}
		})
		v.prefetch(func(add func(objects.Ref)) {
			for _, r := range roots {
				if !v.denied(r) {
					add(deps[r])
				}
			}
		})
		return st
	}
	f := &batchFake{allowed: map[ListQuery]bool{listQueryOf(roots[0]): true}}
	run(f)
	want := [][]ListQuery{{listQueryOf(roots[0]), listQueryOf(roots[1])}, {listQueryOf(gw("infra"))}}
	if !slices.EqualFunc(f.batches, want, slices.Equal) {
		t.Fatalf("batches %v, want %v", f.batches, want)
	}
	failing := &batchFake{err: errors.New("unavailable")}
	if st := run(failing); st.failed() == nil || len(failing.batches) != 1 {
		t.Fatalf("after an error: recorded %v, %d batches", st.failed(), len(failing.batches))
	}
}

// bucketAuthz allows every bucket but deny's and fails fail's; each question
// spends a check, and asked records the buckets it was asked.
type bucketAuthz struct {
	deny, fail func(ListQuery) bool
	mu         sync.Mutex
	asked      []ListQuery
}

func (b *bucketAuthz) answer(r *http.Request, q ListQuery) (bool, error) {
	b.mu.Lock()
	b.asked = append(b.asked, q)
	b.mu.Unlock()
	if err := SpendCheck(r); err != nil {
		return false, err
	}
	if b.fail(q) {
		return false, errOutage
	}
	return !b.deny(q), nil
}

func (b *bucketAuthz) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, _ := b.AuthorizeErr(r, ref)
	return ok
}

func (b *bucketAuthz) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	return b.answer(r, listQueryOf(ref))
}

func (b *bucketAuthz) CanList(r *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return b.answer(r, ListQuery{Kind: gk, Namespace: ns})
}

func (b *bucketAuthz) ResolveLists(r *http.Request, qs []ListQuery) ([]bool, error) {
	out := make([]bool, len(qs))
	for i, q := range qs {
		ok, err := b.answer(r, q)
		if err != nil {
			return nil, err
		}
		out[i] = ok
	}
	return out, nil
}

func (b *bucketAuthz) NamedGetScan() bool { return false }

// A not-accepted route in a denied namespace never asks about its other
// parents on /gateways: their failing checks do not turn the answer into a
// 503, and however many there are they spend none of the check limit.
func TestGatewayRowsSkipHiddenRoutesParents(t *testing.T) {
	gwGK := objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "Gateway"}
	fresh := func(q ListQuery) bool { return q.Kind == gwGK && strings.HasPrefix(q.Namespace, "fresh") }
	// One parent whose check fails; then more parents than the check limit, whose checks succeed.
	for _, n := range []int{1, CheckLimit + 1} {
		fails := func(q ListQuery) bool { return n == 1 && fresh(q) }
		routes := []runtime.Object{testutil.Namespace("hidden", map[string]string{"team": "a"})}
		for i := range n {
			other := testutil.GwRef(fmt.Sprintf("fresh-%d", i), "gw")
			routes = append(routes, testutil.HTTPRoute("hidden", fmt.Sprintf("r%d", i), []objects.ParentRef{edgeRef, other},
				rejectedBy(edgeRef, "UnsupportedValue")))
		}
		e, _, _ := startWith(t, setup{}, explorerWorld(routes...)...)
		a := &bucketAuthz{deny: func(q ListQuery) bool { return q.Kind == routeGK && q.Namespace == "hidden" }, fail: fails}
		if code, _, body := fetchRaw(t, secureServer(t, e, a), listBase+"/gateways"); code != http.StatusOK {
			t.Errorf("%d hidden parents: %d %s", n, code, body)
		}
		if slices.ContainsFunc(a.asked, fresh) {
			t.Errorf("%d hidden parents: asked about a hidden route's parent", n)
		}
	}
}
