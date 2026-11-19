package health

import (
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// writtenRef is one ref of a failing listener field, with defaults applied.
type writtenRef struct {
	RefLocation
}

// RouteRefPath is where a route writes a ref, as KGWUI-013/014 candidates
// name it: backendRefs[ref] of rule, or with filter >= 0 that filter's
// mirror, on the rule itself when ref is -1.
func RouteRefPath(rule, ref, filter int) string { return own.RouteRefPath(rule, ref, filter) }

// RefServiceSentence is explanation 2 or 3 in plain words for Service name
// ("namespace/name"): it does not exist, or has no port among ports.
func RefServiceSentence(explanation, name string, port int32, ports []int32) string {
	return own.RefServiceSentence(explanation, name, port, ports)
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func asList(v any) []any { l, _ := v.([]any); return l }

// listenerRefs returns the certificateRefs of the listener named name of
// Gateway or ListenerSet u (kind Secret, u's namespace by default), and the
// CA certificate refs of the Gateway's frontend TLS for its port. complete is
// false for a ListenerSet whose Gateway is not owned or has CA refs, whose
// refs live on another object.
func (in Input) listenerRefs(u *unstructured.Unstructured, name string) (out []writtenRef, complete bool) {
	gateway := func(g *unstructured.Unstructured) bool {
		gvk := g.GroupVersionKind()
		return gvk.Group == objects.GatewayAPIGroup && gvk.Kind == "Gateway"
	}
	var ls []objects.Listener
	var parent *unstructured.Unstructured
	if gateway(u) {
		ls, parent = in.views().Gateway(u).Listeners, u
	} else {
		v := in.views().ListenerSet(u)
		ls = v.Listeners
		for _, g := range in.Owned.Gateways {
			if r := objects.RefOf(g); r.Name == v.Parent.Name && r.Namespace == v.Parent.Namespace {
				parent = g
			}
		}
	}
	port, found := int32(0), false
	for i, l := range ls {
		if l.Name != name {
			continue
		}
		port, found = l.Port, true
		if l.TLS == nil {
			continue
		}
		for j, r := range l.TLS.CertificateRefs {
			out = append(out, writtenRef{RefLocation{Path: fmt.Sprintf("spec.listeners[%d].tls.certificateRefs[%d]", i, j), Ref: r}})
		}
	}
	if parent == nil {
		return out, false
	}
	ca := gatewayCARefs(parent, port, found)
	if parent != u {
		return out, len(ca) == 0
	}
	return append(out, ca...), true
}

// listenerPolicyCARefs reports whether an owned ListenerPolicy that may target
// Gateway or ListenerSet u, or a ListenerSet's Gateway, sets
// clientCertificateValidation.caCertificateRefs; kgateway resolves those per
// listener. A targetSelector counts as a target.
func (in Input) listenerPolicyCARefs(u *unstructured.Unstructured) bool {
	self := objects.RefOf(u)
	owners := []objects.Ref{self}
	if self.Group != objects.GatewayAPIGroup || self.Kind != "Gateway" {
		pr := in.views().ListenerSet(u).Parent
		owners = append(owners, objects.Ref{Group: objects.GatewayAPIGroup, Kind: "Gateway", Namespace: pr.Namespace, Name: pr.Name})
	}
	ca := func(cfg any) bool {
		return len(asList(asMap(asMap(cfg)["clientCertificateValidation"])["caCertificateRefs"])) > 0
	}
	for _, p := range in.Owned.Policies {
		gvk := p.Obj.GroupVersionKind()
		if gvk.Group != "gateway.kgateway.dev" || gvk.Kind != "ListenerPolicy" {
			continue
		}
		spec := asMap(p.Obj.Object["spec"])
		targets := false
		for _, o := range owners {
			if o.Namespace != p.Obj.GetNamespace() {
				continue
			}
			targets = targets || len(asList(spec["targetSelectors"])) > 0
			for _, t := range asList(spec["targetRefs"]) {
				g, _ := asMap(t)["group"].(string)
				k, _ := asMap(t)["kind"].(string)
				n, _ := asMap(t)["name"].(string)
				targets = targets || g == o.Group && k == o.Kind && n == o.Name
			}
		}
		if !targets {
			continue
		}
		if ca(spec["default"]) {
			return true
		}
		for _, pp := range asList(spec["perPort"]) {
			if ca(asMap(pp)["listener"]) {
				return true
			}
		}
	}
	return false
}

// gatewayCARefs returns the frontend validation caCertificateRefs of Gateway g
// that apply to port: the perPort entry for it, else the default. Without a
// known port it returns every entry's.
func gatewayCARefs(g *unstructured.Unstructured, port int32, known bool) []writtenRef {
	front := asMap(asMap(asMap(g.Object["spec"])["tls"])["frontend"])
	var out []writtenRef
	refs := func(tls any, path string) {
		for k, r := range asList(asMap(asMap(tls)["validation"])["caCertificateRefs"]) {
			ref, _ := own.ObjRef(asMap(r), "", g.GetNamespace())
			out = append(out, writtenRef{RefLocation{Path: fmt.Sprintf("%s.validation.caCertificateRefs[%d]", path, k), Ref: ref}})
		}
	}
	perPort := false
	for n, pp := range asList(front["perPort"]) {
		p, _ := asMap(pp)["port"].(int64)
		if !known || int32(p) == port { //nolint:gosec // a port fits in int32
			refs(asMap(pp)["tls"], fmt.Sprintf("spec.tls.frontend.perPort[%d].tls", n))
			perPort = known
		}
	}
	if !perPort {
		refs(front["default"], "spec.tls.frontend.default")
	}
	return out
}

// notServed reports whether discovery says r's group serves no kind r.Kind.
// A group discovery did not list completely is unknown, never not served.
func (in Input) notServed(r objects.Ref) bool {
	return in.Catalog.Known(r.Group) && !in.Catalog.Served(r.Group, r.Kind)
}

// listenerRefsFinding returns the 013 or 014 finding for cond on a listener
// of Gateway or ListenerSet u, or false for a condition KGWUI-002 keeps. A
// route's 013 and 014 come from package own. Explanation 1 (kind not served)
// wins over the controller's message.
func (in Input) listenerRefsFinding(u *unstructured.Unstructured, cond metav1.Condition, extra string, owner []string) (Finding, bool) {
	code, slug, ok := own.RefRuleFor(cond, true)
	if !ok {
		return Finding{}, false
	}
	// attributable is false when the condition may concern a ref outside refs.
	refs, attributable := in.listenerRefs(u, owner[1])
	attributable = attributable && !in.listenerPolicyCARefs(u)
	// field holds the refs the condition can be about: 014 every cross-namespace ref; 013 every ref.
	field := refs
	var bad []RefLocation
	if code == CodeRefNotPermitted {
		field = nil
		for _, r := range refs {
			if r.Ref.Namespace != u.GetNamespace() {
				field = append(field, r)
			}
		}
		// Rule 1 runs on every ref first, so a local wrong group is never a missing grant.
		for _, r := range refs {
			if in.notServed(r.Ref) {
				bad = append(bad, r.RefLocation)
			}
		}
	}
	f := Finding{Code: code, Slug: slug, Severity: SeverityError, Install: in.Install.ID(), Resource: ref(u),
		Message: message(cond), Detail: detail(cond, extra), Since: since(cond), Key: slices.Clone(owner)}
	ev := &RefsEvidence{Explanation: RefExplainController}
	if len(bad) > 0 {
		ev.Explanation, ev.Candidates, f.Message = RefExplainKindNotServed, bad, own.KindNotServedMessage(bad)
	} else {
		for _, r := range field {
			ev.Candidates = append(ev.Candidates, r.RefLocation)
		}
	}
	ev.Attributed = attributable && len(ev.Candidates) == 1
	ev.Unknown = slices.ContainsFunc(ev.Candidates, func(c RefLocation) bool { return !in.Catalog.Known(c.Ref.Group) })
	if ev.Attributed && len(bad) > 0 && in.Catalog.Complete() {
		if gs := in.Catalog.GroupsWithKind(bad[0].Ref.Kind); len(gs) == 1 {
			ev.Group = &gs[0]
		}
	}
	f.Facts = ev
	return f, true
}
