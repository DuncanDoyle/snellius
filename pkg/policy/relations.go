package policy

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Subject is the object side of a relation: a ref plus the section or port it names, if any.
type Subject struct {
	Ref objects.Ref
	// SectionName is empty when the relation names the whole object.
	SectionName string
	// Port is a backend or parentRef port; 0 when none.
	Port int32
}

// RelationKind is how a policy relates to a subject (design §6.5).
type RelationKind string

// The relation kinds.
const (
	// RelationTargets: a targetRef names the subject (declared).
	RelationTargets RelationKind = "targets"
	// RelationTargetsParent: a targetRef names a parent the subject declares (declared, design §5.3).
	RelationTargetsParent RelationKind = "targets-parent"
	// RelationAttachedOnAncestor: a current Attached=True ancestor is the subject (controller-reported).
	RelationAttachedOnAncestor RelationKind = "attached-on-ancestor"
)

// Relation links a policy to a subject, with its source and the declared path that justified it.
type Relation struct {
	Policy objects.Ref
	// Subject is the subject, with the section or port the targetRef or ancestor names.
	Subject Subject
	Kind    RelationKind
	Source  Source
	// Via are the parentRefs walked, in order; targets-parent only.
	Via []Subject
}

// Relations returns how the policies in in.Policies relate to s, sorted by
// policy namespace and name (then group and kind, which tell apart policies
// that share a name). The order is never precedence.
//
//   - targets: a targetRef names s. A subject without a section matches every
//     targetRef naming the object; one with a section matches only that section.
//   - targets-parent: a targetRef names a parent s declares in spec.parentRefs
//     (design §5.3), with Via the hops walked.
//   - attached-on-ancestor: a current ancestor with Attached=True is s, matched as targets are.
//     A policy of one of in.ListOnlyKinds makes none: its ancestors feed only its findings (D-129).
//
// s must be readable, and every hop readable and present, through in.Lookup, or the relation is not made.
func Relations(s Subject, in Inputs) []Relation {
	l := in.Lookup
	if l == nil || !l.CanGet(s.Ref) {
		return nil
	}
	var out []Relation
	for _, p := range in.Policies {
		for _, t := range p.Targets {
			if !t.Selector && t.Ref(p.Ref.Namespace) == s.Ref && sectionMatches(s, t.SectionName, 0) {
				out = append(out, Relation{Policy: p.Ref, Subject: Subject{Ref: s.Ref, SectionName: t.SectionName},
					Kind: RelationTargets, Source: SourceDeclared})
			}
		}
		if slices.Contains(in.ListOnlyKinds, objects.GroupKind{Group: p.Ref.Group, Kind: p.Ref.Kind}) {
			continue
		}
		for _, a := range p.Ancestors {
			if a.Current && a.Ref.Ref() == s.Ref && sectionMatches(s, a.Ref.SectionName, a.Ref.Port) && Attached(a, p.Generation) {
				out = append(out, Relation{Policy: p.Ref, Subject: Subject{Ref: s.Ref, SectionName: a.Ref.SectionName, Port: a.Ref.Port},
					Kind: RelationAttachedOnAncestor, Source: SourceControllerReported})
			}
		}
	}
	if u, ok := l.Object(s.Ref); ok {
		for _, path := range parentPaths(u, l, in.IsListenerSet) {
			out = append(out, parentRelations(s, path, in.Policies)...)
		}
	}
	return sortRelations(out)
}

// sectionMatches reports whether a targetRef or ancestor with section and port relates
// to s: any of the object's for a whole-object subject, else the same section and port.
func sectionMatches(s Subject, section string, port int32) bool {
	return s.SectionName == "" && s.Port == 0 || s.SectionName == section && s.Port == port
}

// Attached reports whether a's Attached condition is True, whatever its reason, and
// describes generation gen of the policy: an older one is no attachment evidence (D-135).
func Attached(a Ancestor, gen int64) bool {
	c := objects.FindCondition(a.Conditions, "Attached")
	return c != nil && c.Status == metav1.ConditionTrue && c.ObservedGeneration >= gen
}

// hop is one parentRef walked; unsectioned means only targetRefs without a section relate.
type hop struct {
	via         []Subject
	unsectioned bool
}

// parentPaths returns the parent hops of u that exist in l and are readable: each
// parentRef of a route, a ListenerSet's own parentRef, and for a route's ListenerSet
// parent also its Gateway (design §7 item 5).
func parentPaths(u *unstructured.Unstructured, l Lookup, listenerSet func(objects.GroupKind) bool) []hop {
	ref := objects.RefOf(u)
	isLS := func(group, kind string) bool { return listenerSet(objects.GroupKind{Group: group, Kind: kind}) }
	var parents []objects.ParentRef
	switch {
	case isLS(ref.Group, ref.Kind):
		parents = []objects.ParentRef{objects.ListenerSetParent(u)}
	default:
		parents = objects.RouteParentRefs(u)
	}
	// present returns the hop's object when the caller may get it and l holds it.
	present := func(r objects.Ref) (*unstructured.Unstructured, bool) {
		if !l.CanGet(r) {
			return nil, false
		}
		return l.Object(r)
	}
	var out []hop
	for _, p := range parents {
		first := Subject{Ref: p.Ref(), SectionName: p.SectionName, Port: p.Port}
		parent, ok := present(first.Ref)
		if !ok {
			continue
		}
		out = append(out, hop{via: []Subject{first}, unsectioned: p.SectionName == ""})
		// The only two-hop join: a route's ListenerSet parent to the ListenerSet's Gateway.
		if isLS(ref.Group, ref.Kind) || !isLS(p.Group, p.Kind) {
			continue
		}
		second := Subject{Ref: objects.ListenerSetParent(parent).Ref()}
		if _, ok := present(second.Ref); ok {
			out = append(out, hop{via: []Subject{first, second}, unsectioned: true})
		}
	}
	return out
}

// parentRelations relates the policies whose targetRefs name the last hop of path.
func parentRelations(s Subject, path hop, policies []Policy) []Relation {
	last := path.via[len(path.via)-1]
	var out []Relation
	for _, p := range policies {
		for _, t := range p.Targets {
			if t.Selector || t.Ref(p.Ref.Namespace) != last.Ref {
				continue
			}
			if t.SectionName == "" || !path.unsectioned && t.SectionName == last.SectionName {
				out = append(out, Relation{Policy: p.Ref, Subject: s, Kind: RelationTargetsParent, Source: SourceDeclared,
					Via: slices.Clone(path.via)})
				break
			}
		}
	}
	return out
}

// upstreamListenerSets are Gateway API's ListenerSet kinds, the default of Inputs.ListenerSetKinds.
var upstreamListenerSets = []objects.GroupKind{{Group: objects.GatewayAPIGroup, Kind: "ListenerSet"}, {Group: "gateway.networking.x-k8s.io", Kind: "XListenerSet"}}

// IsListenerSet reports whether gk is one of in's ListenerSet kinds.
func (in Inputs) IsListenerSet(gk objects.GroupKind) bool {
	if in.ListenerSetKinds == nil {
		return slices.Contains(upstreamListenerSets, gk)
	}
	return slices.Contains(in.ListenerSetKinds, gk)
}

// sortRelations sorts rs by policy namespace, name, group and kind, then relation
// kind and path, and drops exact repeats.
func sortRelations(rs []Relation) []Relation {
	key := func(r Relation) string {
		var b strings.Builder
		b.WriteString(string(r.Kind))
		for _, v := range append([]Subject{r.Subject}, r.Via...) {
			fmt.Fprintf(&b, "\x00%s\x00%s\x00%s\x00%s\x00%s\x00%d", v.Ref.Group, v.Ref.Kind, v.Ref.Namespace, v.Ref.Name, v.SectionName, v.Port)
		}
		return b.String()
	}
	slices.SortStableFunc(rs, func(a, b Relation) int {
		pa, pb := a.Policy, b.Policy
		return cmp.Or(strings.Compare(pa.Namespace, pb.Namespace), strings.Compare(pa.Name, pb.Name),
			strings.Compare(pa.Group, pb.Group), strings.Compare(pa.Kind, pb.Kind), strings.Compare(key(a), key(b)))
	})
	return slices.CompactFunc(rs, func(a, b Relation) bool { return a.Policy == b.Policy && key(a) == key(b) })
}
