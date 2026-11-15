package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

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
