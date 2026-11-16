package explore

import "github.com/DuncanDoyle/snellius/pkg/objects"

// Permissions is a viewer's authority over named objects and kinds (design
// §8.1). Get is a named get; List is a list of gk in namespace.
type Permissions interface {
	Get(objects.Ref) bool
	List(gk objects.GroupKind, namespace string) bool
}

var (
	gatewayGK       = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "Gateway"}
	httpRouteGK     = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}
	endpointSliceGK = objects.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}
)

// PathIncomplete reports whether the viewer's permissions leave route id's
// traffic path unassessable (design §8.1). It reads only the refs written in
// the route and in the readable routes of its chain, never an object they name.
func (ix *Index) PathIncomplete(id RowID, p Permissions) bool {
	seen := map[objects.Ref]bool{ix.Routes[id].Ref: true}
	queue := []RowID{id}
	for len(queue) > 0 {
		r := &ix.Routes[queue[0]]
		queue = queue[1:]
		if refsIncomplete(r, p) {
			return true
		}
		for _, parent := range ix.ParentRoutes[r.Ref] {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			if !p.Get(parent) {
				return true
			}
			if pid, ok := ix.Row(parent); ok {
				queue = append(queue, pid)
			}
		}
	}
	return false
}

// refsIncomplete reports whether p lacks a get or list for a ref written in r.
func refsIncomplete(r *RouteRow, p Permissions) bool {
	v := r.View
	if v == nil {
		return false
	}
	for _, pr := range v.ParentRefs {
		ref := pr.Ref()
		if !p.Get(ref) || ref.Group == gatewayGK.Group && ref.Kind == gatewayGK.Kind && !p.List(gatewayGK, ref.Namespace) {
			return true
		}
	}
	for _, rule := range v.Rules {
		for _, b := range rule.BackendRefs {
			if d := b.Delegation; d != nil {
				if !p.List(httpRouteGK, d.Namespace) {
					return true
				}
				continue
			}
			if !p.Get(b.Ref) || !p.List(endpointSliceGK, b.Ref.Namespace) {
				return true
			}
		}
	}
	return false
}
