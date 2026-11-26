package own

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Wording for KGWUI-013 and 014 (design §7.3).
const (
	// RefKindNotServedMessage is explanation 1 for one ref kind: kind, group.
	RefKindNotServedMessage = "No kind `%s` in group `%s`. Check the group."
	// RefKindsNotServedMessage is explanation 1 when the refs name several kinds.
	RefKindsNotServedMessage = "Some refs name a kind their group does not serve. Check the groups."
	// coreGroup names the core API group in messages.
	coreGroup = "core"
	// refServiceMissingMessage is explanation 2: namespace/name.
	refServiceMissingMessage = "Service `%s` does not exist."
	// refServicePortMessage is explanation 3: namespace/name, the port, the Service's ports.
	refServicePortMessage = "Service `%s` has no port %d (ports: %s)."
)

// serviceKind is the core Service kind.
const serviceKind = "Service"

// writtenRef is one ref of a failing field, with defaults applied.
type writtenRef struct {
	RefLocation
	// delegation marks a backendRef that delegates to child routes: it has no
	// served kind of its own.
	delegation bool
	// port is the written port, set when hasPort.
	port    int32
	hasPort bool
}

// ObjRef decodes a written ref map with defKind and namespace ns as defaults,
// and reports whether it delegates (group and kind both written).
func ObjRef(m map[string]any, defKind, ns string) (objects.Ref, bool) {
	group, hasGroup := m["group"].(string)
	kind, hasKind := m["kind"].(string)
	r := objects.Ref{Group: group, Kind: kind, Namespace: ns}
	r.Name, _ = m["name"].(string)
	if !hasKind || kind == "" {
		r.Kind = defKind
	}
	if n, _ := m["namespace"].(string); n != "" {
		r.Namespace = n
	}
	// As objects.DecodeHTTPRoute classifies delegation: group and kind both written.
	del := hasGroup && hasKind && (isHTTPRoute(group, kind) || group == "delegation.kgateway.dev" && kind == "label")
	return r, del
}

// routeBackendRefs returns every spec.rules[].backendRefs[] entry of route u and
// every RequestMirror backendRef in their filters and the rules'. Group defaults
// to core, kind to Service and namespace to u's.
func routeBackendRefs(u *unstructured.Unstructured) []writtenRef {
	rules, _, _ := unstructured.NestedSlice(u.Object, "spec", "rules")
	var out []writtenRef
	add := func(m map[string]any, path string) {
		r, del := ObjRef(m, serviceKind, u.GetNamespace())
		p, hasPort := m["port"].(int64)
		out = append(out, writtenRef{RefLocation{Path: path, Ref: r}, del, int32(p), hasPort}) //nolint:gosec // a port fits in int32
	}
	mirrors := func(fs any, rule, ref int) {
		for k, f := range asList(fs) {
			if m, ok := asMap(asMap(f)["requestMirror"])["backendRef"].(map[string]any); ok {
				add(m, RouteRefPath(rule, ref, k))
			}
		}
	}
	for i, r := range rules {
		rm := asMap(r)
		for j, b := range asList(rm["backendRefs"]) {
			add(asMap(b), RouteRefPath(i, j, -1))
			mirrors(asMap(b)["filters"], i, j)
		}
		mirrors(rm["filters"], i, -1)
	}
	return out
}

// RouteRefPath is where a route writes a ref, as KGWUI-013/014 candidates
// name it: backendRefs[ref] of rule, or with filter >= 0 that filter's
// mirror, on the rule itself when ref is -1.
func RouteRefPath(rule, ref, filter int) string {
	path := fmt.Sprintf("spec.rules[%d]", rule)
	if ref >= 0 {
		path += fmt.Sprintf(".backendRefs[%d]", ref)
	}
	if filter >= 0 {
		path += fmt.Sprintf(".filters[%d].requestMirror.backendRef", filter)
	}
	return path
}

// delegationRoute reports whether route u delegates or is a delegation child:
// a delegation cycle is RefNotPermitted too, so no ref can be singled out.
func delegationRoute(ic InstallContext, u *unstructured.Unstructured, v routeView, refs []writtenRef) bool {
	if !ic.Delegation {
		return false
	}
	if slices.ContainsFunc(refs, func(r writtenRef) bool { return r.delegation }) {
		return true
	}
	for _, p := range asList(asMap(u.Object["spec"])["parentRefs"]) {
		if g, _ := asMap(p)["group"].(string); isHTTPRoute(g, fmt.Sprint(asMap(p)["kind"])) {
			return true
		}
	}
	return slices.ContainsFunc(v.status, func(s objects.RouteParentStatus) bool { return isHTTPRoute(s.ParentRef.Group, s.ParentRef.Kind) })
}

// notServed reports whether discovery says r's group serves no kind r.Kind.
// A group discovery did not list completely is unknown, never not served.
func notServed(ic InstallContext, r objects.Ref) bool {
	return ic.Catalog.Known(r.Group) && !ic.Catalog.Served(r.Group, r.Kind)
}

// refs reports KGWUI-013 and 014 on a route: a current ResolvedRefs=False on
// a parent, with the refs its rules write as candidates.
func refs(ic InstallContext, u *unstructured.Unstructured, named Named, o *out, w *walk) {
	w = w.of(ic, u)
	if w.ignored {
		return
	}
	v := w.v
	var written []writtenRef
	w.eachCurrentBlock(func(_ objects.ParentRef, cond metav1.Condition, extra string, owner []string) {
		code, slug, ok := RefRuleFor(cond, false)
		if !ok {
			return
		}
		if written == nil {
			written = routeBackendRefs(u)
			for i := range written {
				if !ic.Delegation {
					// Without delegation a backendRef to an HTTPRoute is an ordinary ref.
					written[i].delegation = false
				}
				// Each target is named, present or absent; a delegation selector is not an object.
				if r := written[i].Ref; !written[i].delegation || r.Kind != "label" && r.Name != "*" {
					named.Object(r)
				}
			}
		}
		o.add(refsFinding(ic, u, v, cond, code, slug, extra, owner, written, named))
	})
}

// refsFinding is the 013 or 014 for cond on route u. Explanation 1 (kind not
// served) wins over the controller's message.
func refsFinding(ic InstallContext, u *unstructured.Unstructured, v routeView, cond metav1.Condition, code, slug, extra string, owner []string,
	refs []writtenRef, named Named,
) Finding {
	attributable := !delegationRoute(ic, u, v, refs)
	// field holds the refs the condition can be about: 014 every cross-namespace
	// ref, delegating ones included; 013 every ref.
	field := refs
	if code == CodeRefNotPermitted {
		field = nil
		for _, r := range refs {
			if r.Ref.Namespace != u.GetNamespace() {
				field = append(field, r)
			}
		}
	}
	// Rule 1 runs on every ref first, so a local wrong group is never a missing grant.
	var bad []RefLocation
	for _, r := range refs {
		if !r.delegation && notServed(ic, r.Ref) {
			bad = append(bad, r.RefLocation)
		}
	}
	f := Finding{code: code, slug: slug, severity: severityError, message: ConditionMessage(cond), detail: ConditionDetail(cond, extra),
		since: since(cond), key: slices.Clone(owner)}
	ev := &RefsEvidence{Explanation: RefExplainController}
	if len(bad) > 0 {
		ev.Explanation, ev.Candidates, f.message = RefExplainKindNotServed, bad, KindNotServedMessage(bad)
	} else {
		for _, r := range field {
			ev.Candidates = append(ev.Candidates, r.RefLocation)
		}
		delegationCondition(ic, &f, u, cond)
	}
	// Explanations 2 and 3 are 013's: a 014 keeps the grant diagnosis (§7.3).
	if len(bad) == 0 && attributable && code == CodeRefUnresolved {
		serviceExplanation(ic, &f, ev, field, named)
	}
	ev.Attributed = attributable && len(ev.Candidates) == 1
	ev.Unknown = slices.ContainsFunc(ev.Candidates, func(c RefLocation) bool { return !ic.Catalog.Known(c.Ref.Group) })
	if ev.Attributed && len(bad) > 0 && ic.Catalog.Complete() {
		if gs := ic.Catalog.GroupsWithKind(bad[0].Ref.Kind); len(gs) == 1 {
			ev.Group = &gs[0]
		}
	}
	f.facts = ev
	return f
}

// KindNotServedMessage is explanation 1's message for the refs in bad.
func KindNotServedMessage(bad []RefLocation) string {
	first := bad[0].Ref
	for _, b := range bad[1:] {
		if b.Ref.Group != first.Group || b.Ref.Kind != first.Kind {
			return RefKindsNotServedMessage
		}
	}
	group := first.Group
	if group == "" {
		group = coreGroup
	}
	return fmt.Sprintf(RefKindNotServedMessage, first.Kind, group)
}

// serviceExplanation applies explanations 2 and 3 to f when exactly one ref of
// field is a core Service ref with a Service the cache lacks or a port it
// lacks, and every other ref is a core Service ref the cache shows resolving.
// A ref the UI cannot check may be the cause, so it leaves the finding as is:
// absence from a cache that did not read a namespace says nothing.
func serviceExplanation(ic InstallContext, f *Finding, ev *RefsEvidence, field []writtenRef, named Named) {
	// Collection is checked per ref's namespace below, so a failure elsewhere changes nothing.
	svcs := objects.GroupKind{Kind: serviceKind}
	var hit *writtenRef
	var hitPorts []servicePort
	hitFound := false
	for i, r := range field {
		if r.delegation || r.Ref.Group != "" || r.Ref.Kind != serviceKind {
			return
		}
		if !ic.Collected(svcs, r.Ref.Namespace) {
			return
		}
		svc, found := named.Object(r.Ref)
		var ports []servicePort
		if found {
			ports = servicePorts(svc)
		}
		// A ref with no port has no port to explain.
		if found && (!r.hasPort || servicePortIndex(ports, r.port) >= 0) {
			continue
		}
		if hit != nil {
			return // two refs may fail: leave it to the controller's reason
		}
		hit, hitPorts, hitFound = &field[i], ports, found
	}
	if hit == nil {
		return
	}
	name := hit.Ref.Namespace + "/" + hit.Ref.Name
	ev.Candidates = []RefLocation{hit.RefLocation}
	// Every Service inspected attributes the failure, so each is evidence (§14 rule 12).
	for _, r := range field {
		f.evidence = append(f.evidence, r.Ref)
	}
	if !hitFound {
		ev.Explanation, f.detail = RefExplainServiceMissing, RefServiceSentence(RefExplainServiceMissing, name, 0, nil)+" "+f.detail
		return
	}
	for _, p := range hitPorts {
		ev.Ports = append(ev.Ports, p.port)
	}
	ev.Explanation, f.detail = RefExplainServicePort, RefServiceSentence(RefExplainServicePort, name, hit.port, ev.Ports)+" "+f.detail
}

// servicePort is one port of a Service: its number and protocol.
type servicePort struct {
	port     int32
	protocol string
}

// servicePorts reads spec.ports of Service svc.
func servicePorts(svc *unstructured.Unstructured) []servicePort {
	var out []servicePort
	for _, p := range asList(asMap(svc.Object["spec"])["ports"]) {
		n, _ := asMap(p)["port"].(int64)
		proto, _ := asMap(p)["protocol"].(string)
		out = append(out, servicePort{int32(n), proto}) //nolint:gosec // a port fits in int32
	}
	return out
}

// servicePortIndex is the index of port in ps, a TCP one first.
func servicePortIndex(ps []servicePort, port int32) int {
	if i := slices.IndexFunc(ps, func(p servicePort) bool { return p.port == port && (p.protocol == "" || p.protocol == "TCP") }); i >= 0 {
		return i
	}
	return slices.IndexFunc(ps, func(p servicePort) bool { return p.port == port })
}

// RefServiceSentence is explanation 2 or 3 in plain words for Service name
// ("namespace/name"): it does not exist, or has no port among ports.
func RefServiceSentence(explanation, name string, port int32, ports []int32) string {
	if explanation == RefExplainServiceMissing {
		return fmt.Sprintf(refServiceMissingMessage, name)
	}
	ps := make([]string, len(ports))
	for i, p := range ports {
		ps[i] = strconv.Itoa(int(p))
	}
	return fmt.Sprintf(refServicePortMessage, name, port, strings.Join(ps, ", "))
}
