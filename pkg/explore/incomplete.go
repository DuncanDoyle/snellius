package explore

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

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
// traffic path unassessable (design §8.1). It reads only the route objects
// the viewer can get and their refs, never an object a ref names or the
// reported edges, which hidden objects decide.
func (ix *Index) PathIncomplete(id RowID, p Permissions) bool {
	seen := map[objects.Ref]bool{ix.Routes[id].Ref: true}
	queue := []pathData{ix.routeData(&ix.Routes[id])}
	for len(queue) > 0 {
		d := queue[0]
		queue = queue[1:]
		if ix.refsIncomplete(d, p) {
			return true
		}
		for _, parent := range d.routeParents {
			if seen[parent] {
				continue
			}
			seen[parent] = true
			if !p.Get(parent) {
				return true
			}
			if pid, ok := ix.Row(parent); ok {
				queue = append(queue, ix.routeData(&ix.Routes[pid]))
			} else if u := ix.object(parent); u != nil {
				queue = append(queue, ix.routeData(&RouteRow{Ref: parent, Obj: u, View: objects.DecodeHTTPRoute(u)}))
			}
		}
	}
	return false
}

// pathData is the part of a route object that PathIncomplete reads.
type pathData struct {
	parents      []objects.ParentRef
	routeParents []objects.Ref
	targets      []objects.BackendRef
}

// routeData decodes r's own object, any route kind: its spec parentRefs, the
// HTTPRoutes in those and in the install's status.parents, and its backendRefs.
func (ix *Index) routeData(r *RouteRow) pathData {
	var d pathData
	var status []objects.RouteParentStatus
	if r.View != nil {
		d.parents, status = r.View.ParentRefs, r.View.Parents
		for _, rule := range r.View.Rules {
			d.targets = append(d.targets, rule.BackendRefs...)
		}
	} else if r.Obj != nil {
		v := objects.DecodeRoute(r.Obj)
		d.parents, status = v.ParentRefs, v.Parents
		for _, t := range v.Targets {
			if !t.Mirror {
				d.targets = append(d.targets, objects.BackendRef{Ref: t.Ref})
			}
		}
	}
	add := func(ref objects.Ref) {
		if ref.Group == httpRouteGK.Group && ref.Kind == httpRouteGK.Kind && !slices.Contains(d.routeParents, ref) {
			d.routeParents = append(d.routeParents, ref)
		}
	}
	for _, pr := range d.parents {
		add(pr.Ref())
	}
	for _, st := range status {
		if slices.Contains(ix.controllers, st.ControllerName) {
			add(st.ParentRef.Ref())
		}
	}
	return d
}

// refsIncomplete reports whether p lacks a get or list for a ref in d.
// RequestMirror targets are not path rows (D-082), so they are not checked.
func (ix *Index) refsIncomplete(d pathData, p Permissions) bool {
	for _, pr := range d.parents {
		ref := pr.Ref()
		if !p.Get(ref) {
			return true
		}
		switch {
		case ref.Group == gatewayGK.Group && ref.Kind == gatewayGK.Kind:
			if !p.List(gatewayGK, ref.Namespace) {
				return true
			}
		case ix.listenerSetGatewayIncomplete(ref, p):
			return true
		}
	}
	for _, b := range d.targets {
		if dl := b.Delegation; dl != nil && !ix.noDelegation {
			if !p.List(httpRouteGK, dl.Namespace) {
				return true
			}
			if dl.Mode == objects.DelegationName && !p.Get(objects.Ref{Group: httpRouteGK.Group, Kind: httpRouteGK.Kind, Namespace: dl.Namespace, Name: dl.Name}) {
				return true
			}
			continue
		}
		if !p.Get(b.Ref) || !p.List(endpointSliceGK, b.Ref.Namespace) {
			return true
		}
	}
	return false
}

// listenerSetGatewayIncomplete reports whether ref is a ListenerSet whose own
// spec.parentRef Gateway cannot be got or its namespace listed. The
// ListenerSet is read from its own object, member of the install or not.
func (ix *Index) listenerSetGatewayIncomplete(ref objects.Ref, p Permissions) bool {
	// parentObjs holds only ListenerSets of any served kind and HTTPRoutes.
	u := ix.object(ref)
	if u == nil || ref.Group == httpRouteGK.Group && ref.Kind == httpRouteGK.Kind {
		return false
	}
	g := objects.DecodeListenerSet(u).Parent.Ref()
	return !p.Get(g) || !p.List(gatewayGK, g.Namespace)
}

// object returns the snapshot ListenerSet or HTTPRoute ref names, or nil.
func (ix *Index) object(ref objects.Ref) *unstructured.Unstructured { return ix.parentObjs[ref] }
