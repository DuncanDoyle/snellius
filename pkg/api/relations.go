package api

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// relationGroups says which relation groups a subject kind carries beyond
// targeting (Phase 8 design §6.5).
type relationGroups struct{ parent, attached bool }

// The relation groups per subject kind.
var (
	gatewayRelations     = relationGroups{attached: true}
	listenerSetRelations = relationGroups{parent: true, attached: true}
	routeRelations       = relationGroups{parent: true}
	backendRelations     = relationGroups{attached: true}
)

// relatedPolicy is one readable policy as relations read it.
type relatedPolicy struct {
	u *unstructured.Unstructured
	p policy.Policy
}

// policyRelations are the relations of install i's policies to subject s for the caller of v
// (design §6.5), made by policy.Relations with the policies endpoints' guard and Lookup. Only
// policies the caller may view as members take part (design §7 items 1 and 5).
func (h *handler) policyRelations(p *engine.Published, i *engine.InstallModel, v *view, l linker, s objects.Ref, g relationGroups) PolicyRelationsDTO {
	// A candidate the downstream authoriser denies asks no check: a failing one must not
	// answer the request for a policy it never shows (8.2 review #1).
	lsKinds := policy.Inputs{ListenerSetKinds: listenerSetKinds(p.Model.Served)}
	all := slices.DeleteFunc(candidatePolicies(p.Model, installPolicies(i), s, lsKinds), func(u *unstructured.Unstructured) bool {
		return v.downstreamDenies(objects.RefOf(u), false)
	})
	v.prefetch(func(add func(objects.Ref)) {
		for _, u := range all {
			add(objects.RefOf(u))
		}
	})
	pr := h.policyReader(p, i, v)
	out := PolicyRelationsDTO{Targeting: []PolicyRelationDTO{}, Selectors: PolicySelectorsDTO{Namespace: s.Namespace, Kinds: []objects.GroupKind{},
		Source: policy.SourceDeclared}}
	byRef := map[objects.Ref]relatedPolicy{}
	var in []policy.Policy
	for _, u := range all {
		ref := objects.RefOf(u)
		if !v.can(ref) {
			continue
		}
		pol := policy.Read(u, pr.interp, pr.guard)
		// Only an ancestor that is s can relate, so no other is judged.
		pol.Ancestors = slices.DeleteFunc(pol.Ancestors, func(a policy.Ancestor) bool { return a.Ref.Ref() != s })
		pr.gate(&pol)
		gk := objects.GroupKind{Group: ref.Group, Kind: ref.Kind}
		if ref.Namespace == s.Namespace && !slices.Contains(out.Selectors.Kinds, gk) &&
			slices.ContainsFunc(pol.Targets, func(t policy.Target) bool { return t.Selector && t.Group == s.Group && t.Kind == s.Kind }) {
			out.Selectors.Kinds = append(out.Selectors.Kinds, gk)
		}
		byRef[ref] = relatedPolicy{u: u, p: pol}
		in = append(in, pol)
	}
	slices.SortFunc(out.Selectors.Kinds, func(a, b objects.GroupKind) int {
		return compareAffected(objects.Ref{Group: a.Group, Kind: a.Kind}, objects.Ref{Group: b.Group, Kind: b.Kind})
	})
	parent, attached := []PolicyRelationDTO{}, []PolicyRelationDTO{}
	rin := policy.Inputs{Policies: in, Lookup: pr.in.Lookup, ListenerSetKinds: lsKinds.ListenerSetKinds, ListOnlyKinds: servedListOnly(p.Model.Served)}
	for _, r := range policy.Relations(policy.Subject{Ref: s}, rin) {
		d := PolicyRelationDTO{Policy: linkedPolicy(v, i.Resolved.ID(), r.Policy), Relation: r.Kind, Source: r.Source, SectionName: r.Subject.SectionName, Port: r.Subject.Port}
		switch r.Kind {
		case policy.RelationTargets:
			out.Targeting = append(out.Targeting, d)
		case policy.RelationTargetsParent:
			for _, hop := range r.Via {
				d.Via = append(d.Via, PolicyHopDTO{Ref: l.linkedNamed(hop.Ref), SectionName: hop.SectionName, Port: hop.Port})
			}
			parent = append(parent, d)
		case policy.RelationAttachedOnAncestor:
			attachedCondition(&d, byRef[r.Policy], r.Subject)
			attached = append(attached, d)
		}
	}
	if g.parent {
		out.TargetingParent = &parent
	}
	if g.attached {
		out.AttachedHere = &attached
	}
	return out
}

// candidatePolicies are the policies of all that can relate to subject s, judged from declared
// and reported data alone, before any access check: a targetRef naming s or one of the parents
// policy.Relations may walk, an ancestor that is s, or a targetSelector of s's kind in s's namespace.
// Any other policy makes no relation and no selector kind for any viewer, so leaving it out
// changes no response; only the checks asked shrink.
func candidatePolicies(m *engine.Model, all []*unstructured.Unstructured, s objects.Ref, ls policy.Inputs) []*unstructured.Unstructured {
	named := map[objects.Ref]bool{s: true}
	for _, r := range declaredParents(m, s, ls) {
		named[r] = true
	}
	return slices.DeleteFunc(slices.Clone(all), func(u *unstructured.Unstructured) bool {
		ns := u.GetNamespace()
		refs, sels := computed.DecodeTargets(u)
		return !slices.ContainsFunc(refs, func(t computed.TargetRef) bool {
			return named[policy.Target{Group: t.Group, Kind: t.Kind, Name: t.Name}.Ref(ns)]
		}) && !slices.ContainsFunc(objects.PolicyAncestors(u), func(a objects.AncestorStatus) bool { return a.AncestorRef.Ref() == s }) &&
			!(ns == s.Namespace && slices.ContainsFunc(sels, func(t computed.TargetSelector) bool { return t.Group == s.Group && t.Kind == s.Kind }))
	})
}

// declaredParents are the parents policy.Relations may walk from s (design §5.3): a route's
// parentRefs and, for a ListenerSet among them, its parent Gateway; a ListenerSet's own parentRef.
// ls classifies ListenerSets as Relations does. It may name more than Relations walks, never fewer.
func declaredParents(m *engine.Model, s objects.Ref, ls policy.Inputs) []objects.Ref {
	u, _ := stored(m, s)
	if u == nil {
		return nil
	}
	isLS := func(r objects.Ref) bool { return ls.IsListenerSet(objects.GroupKind{Group: r.Group, Kind: r.Kind}) }
	if isLS(s) {
		return []objects.Ref{objects.ListenerSetParent(u).Ref()}
	}
	var out []objects.Ref
	for _, pr := range objects.RouteParentRefs(u) {
		out = append(out, pr.Ref())
		if ls, _ := stored(m, pr.Ref()); ls != nil && isLS(pr.Ref()) {
			out = append(out, objects.ListenerSetParent(ls).Ref())
		}
	}
	return out
}

// listenerSetKinds are the served ListenerSet kinds, upstream and downstream (E7).
func listenerSetKinds(served []kinds.Served) []objects.GroupKind {
	out := []objects.GroupKind{}
	for _, k := range served {
		if k.Category == kinds.CategoryListenerSet {
			out = append(out, objects.GroupKind{Group: k.Group, Kind: k.Kind})
		}
	}
	return out
}

// servedListOnly are the served list-only kinds, whose ancestors relate to nothing (D-129).
func servedListOnly(served []kinds.Served) []objects.GroupKind {
	var out []objects.GroupKind
	for _, k := range served {
		if k.ListOnly {
			out = append(out, objects.GroupKind{Group: k.Group, Kind: k.Kind})
		}
	}
	return out
}

// attachedCondition sets d's Attached condition, verbatim, and the pack's note on it,
// from rp's first current ancestor that is sub and reports Attached=True.
func attachedCondition(d *PolicyRelationDTO, rp relatedPolicy, sub policy.Subject) {
	for _, a := range rp.p.Ancestors {
		if !a.Current || !policy.Attached(a, rp.p.Generation) || a.Ref.SectionName != sub.SectionName || a.Ref.Port != sub.Port {
			continue
		}
		c := objects.FindCondition(a.Conditions, "Attached")
		dc := condition(rp.u, *c)
		d.Condition = &dc
		if k := slices.IndexFunc(a.Readings, func(r policy.Reading) bool { return r.Type == "Attached" && r.Note != "" }); k >= 0 {
			d.Note, d.NoteSource = a.Readings[k].Note, a.Readings[k].Source
		}
		return
	}
}
