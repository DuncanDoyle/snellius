package policy_test

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// world is a Lookup over a fixed set of objects. By default every kind is
// watched everywhere and the caller may get and list everything.
type world struct {
	objs map[objects.Ref]*unstructured.Unstructured
	// unwatched kinds, by "group/kind/namespace".
	unwatched map[string]bool
	// hidden objects the caller may not get by name.
	hidden map[objects.Ref]bool
	// unlisted kinds the caller may not list, by "group/kind/namespace".
	unlisted map[string]bool
}

func newWorld(objs ...*unstructured.Unstructured) *world {
	w := &world{objs: map[objects.Ref]*unstructured.Unstructured{}, unwatched: map[string]bool{}, hidden: map[objects.Ref]bool{}, unlisted: map[string]bool{}}
	for _, u := range objs {
		w.objs[objects.RefOf(u)] = u
	}
	return w
}

func kindKey(gk objects.GroupKind, ns string) string { return gk.Group + "/" + gk.Kind + "/" + ns }

func (w *world) Object(r objects.Ref) (*unstructured.Unstructured, bool) {
	u, ok := w.objs[r]
	return u, ok
}

func (w *world) Watched(gk objects.GroupKind, ns string) bool { return !w.unwatched[kindKey(gk, ns)] }

func (w *world) CanGet(r objects.Ref) bool { return !w.hidden[r] }

func (w *world) CanList(gk objects.GroupKind, ns string) bool { return !w.unlisted[kindKey(gk, ns)] }

// obj builds an object of group/kind with spec.
func obj(group, version, kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	av := version
	if group != "" {
		av = group + "/" + version
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": av, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns, "generation": int64(1)}, "spec": spec}}
}

// gateway is a Gateway with the named listeners.
func gateway(ns, name string, listeners ...string) *unstructured.Unstructured {
	var ls []any
	for i, l := range listeners {
		ls = append(ls, map[string]any{"name": l, "protocol": "HTTP", "port": int64(8080 + i)})
	}
	return obj(gw, "v1", "Gateway", ns, name, map[string]any{"gatewayClassName": "kgateway", "listeners": ls})
}

// route is an HTTPRoute with the given spec.parentRefs, written as maps.
func route(ns, name string, parents ...map[string]any) *unstructured.Unstructured {
	var ps []any
	for _, p := range parents {
		ps = append(ps, p)
	}
	return obj(gw, "v1", "HTTPRoute", ns, name, map[string]any{"parentRefs": ps,
		"rules": []any{map[string]any{"name": "rule-a"}}})
}

// targetRef is a spec.targetRefs entry; section may be "".
func targetRef(group, kind, name, section string) map[string]any {
	m := map[string]any{"group": group, "kind": kind, "name": name}
	if section != "" {
		m["sectionName"] = section
	}
	return m
}

// trafficPolicy is a TrafficPolicy with the given targetRefs and status.ancestors.
func trafficPolicy(ns, name string, refs []map[string]any, ancestors ...map[string]any) *unstructured.Unstructured {
	var rs, as []any
	for _, r := range refs {
		rs = append(rs, r)
	}
	for _, a := range ancestors {
		as = append(as, a)
	}
	u := obj(kgw, "v1alpha1", "TrafficPolicy", ns, name, map[string]any{"targetRefs": rs})
	if len(as) > 0 {
		u.Object["status"] = map[string]any{"ancestors": as}
	}
	return u
}

// ancestor is a status.ancestors entry with Accepted=True and Attached=attached, at observedGeneration gen.
func ancestor(ref map[string]any, controller, attached string, gen int64) map[string]any {
	cond := func(typ, status, reason string) map[string]any {
		return map[string]any{"type": typ, "status": status, "reason": reason, "message": "m", "observedGeneration": gen,
			"lastTransitionTime": "2026-11-01T10:00:00Z"}
	}
	return map[string]any{"ancestorRef": ref, "controllerName": controller,
		"conditions": []any{cond("Accepted", "True", "Valid"), cond("Attached", attached, "Attached")}}
}
