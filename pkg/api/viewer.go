package api

import (
	"net/http"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Viewer is what the caller of one request may see, for extension handlers
// that serve cluster objects (design §7.2). It answers as the core handlers
// do, so an extension applies the same rule (D-085). One request uses it, on one goroutine.
// Install.Owned.Backends depends on hidden consumer routes: use Backends instead.
type Viewer interface {
	// Secure reports whether the request runs in secure mode.
	Secure() bool
	// ReadsAll reports whether the caller has the full read (design §6.4).
	ReadsAll() bool
	// Can reports whether the caller may see ref as a population member: a row, count or aggregate.
	Can(objects.Ref) bool
	// CanNamed reports whether the caller may see ref by name: a detail page's subject.
	CanNamed(objects.Ref) bool
	// CanList reports whether the caller may list gk in namespace, "" meaning cluster-wide.
	CanList(gk objects.GroupKind, namespace string) bool
	// Shown reports whether the caller sees f: always outside secure mode and
	// under the full read; else only an own-object finding on readable objects (D-106).
	Shown(*health.Finding) bool
	// Backends are install i's Backends the caller may see. In secure mode
	// without the full read, only those a readable route of i names (D-107).
	Backends(i *engine.InstallModel) []*unstructured.Unstructured
}

// ViewerOf is the Viewer of r's caller through a, the handler's Authorizer
// (nil means AllowAll). It is secure when r carries the commit wrapper's state.
func ViewerOf(r *http.Request, a Authorizer) Viewer {
	if a == nil {
		a = AllowAll{}
	}
	return viewer{newView(r, a, secureRequest(r))}
}

// VisibleTo returns the items whose ref v may see as population members, in
// order. It sends no hidden count: in secure mode a hidden object leaves no trace (D-085).
// Its input must not depend on hidden objects: for Install.Owned.Backends use Viewer.Backends.
func VisibleTo[T any](v Viewer, items []T, ref func(T) objects.Ref) []T {
	out := make([]T, 0, len(items))
	for _, it := range items {
		if v.Can(ref(it)) {
			out = append(out, it)
		}
	}
	return out
}

// viewer adapts a view to Viewer.
type viewer struct{ v *view }

func (x viewer) Secure() bool                                 { return x.v.secure }
func (x viewer) ReadsAll() bool                               { return x.v.readsAll() }
func (x viewer) Can(ref objects.Ref) bool                     { return x.v.can(ref) }
func (x viewer) CanNamed(ref objects.Ref) bool                { return x.v.canNamed(ref) }
func (x viewer) CanList(gk objects.GroupKind, ns string) bool { return x.v.canList(gk, ns) }
func (x viewer) Shown(f *health.Finding) bool                 { return x.v.shown(f) }
func (x viewer) Backends(i *engine.InstallModel) []*unstructured.Unstructured {
	return x.v.backends(i)
}

// bypassKey marks a request of a handler built with WithProjectionBypass.
type bypassKey struct{}

// secureRequest reports whether r runs in secure mode: it carries the commit
// wrapper's state, and the projection bypass is off.
func secureRequest(r *http.Request) bool {
	bypassed, _ := r.Context().Value(bypassKey{}).(bool)
	return authzStateOf(r) != nil && !bypassed
}
