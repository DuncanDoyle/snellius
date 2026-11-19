//go:build bench

package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/endpoints"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/kgateway"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/paths"
)

// LargeHandlers computes the Large tier once and returns a builder of
// backendHandler-like handlers over it under authz, after opts.
func LargeHandlers(tb testing.TB) (func(authz Authorizer, opts ...HandlerOption) http.Handler, *engine.Model) {
	return TierHandlers(tb, scale.Large)
}

// TierHandlers is LargeHandlers over tier.
func TierHandlers(tb testing.TB, tier scale.Tier) (func(authz Authorizer, opts ...HandlerOption) http.Handler, *engine.Model) {
	tb.Helper()
	snap := scale.Snapshot(tier, 1)
	for _, objs := range snap.Objects {
		slices.SortFunc(objs, func(x, y *unstructured.Unstructured) int {
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
	}
	e, m := snapshotEngine(snap, tier)
	return func(authz Authorizer, opts ...HandlerOption) http.Handler {
		r := endpoints.Bounded(synthReader{snap: m.Snapshot, n: endpoints.DefaultBounds.MaxEndpoints}, endpoints.DefaultBounds, testutil.FakeClock())
		base := []HandlerOption{withPathMemo(paths.NewMemo(clocktesting.NewFakeClock(m.ComputedAt))), WithScopedReader(r),
			WithBackendConsumers(kgateway.BackendConsumers)}
		return NewHandler(e, fstest.MapFS{}, authz, append(base, opts...)...)
	}, m
}

// DeepestChild is deepestChild for the external bench tests.
func DeepestChild(ix *explore.Index, can func(objects.Ref) bool) (objects.Ref, int) {
	return deepestChild(ix, can)
}

// HeapBaseline is the 2.1 retained-heap baseline of the D-057 gates.
const HeapBaseline = heapBaseline

// SettledWorstHeap warms the worst gated D-057 tier as TestBudgetHeapAPIWarm's
// child does, up to its settled figure, and returns what must stay alive.
func SettledWorstHeap(t *testing.T) []any {
	e, m, w := pathsEngine(t, scale.Large.Contradicting().Worst())
	h := backendHandler(e, m, nil)
	for _, i := range m.Installs {
		pathsGet(t, h, "/api/v1/installs/"+i.Resolved.ID()+"/paths")
		serviceDetails(t, h, i.Resolved.ID(), 200)
	}
	urls := serviceDetails(t, h, m.Installs[0].Resolved.ID(), 48)
	for _, u := range urls[:24] {
		detailGet(t, h, u)
	}
	m = e.Compute()
	return []any{e, m, w, h}
}
