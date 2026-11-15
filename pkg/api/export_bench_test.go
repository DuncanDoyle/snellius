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
	tb.Helper()
	snap := scale.Snapshot(scale.Large, 1)
	for _, objs := range snap.Objects {
		slices.SortFunc(objs, func(x, y *unstructured.Unstructured) int {
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
	}
	e, m := snapshotEngine(snap, scale.Large)
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
