package api

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/engine"
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
	all := installPolicies(i)
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
	for _, r := range policy.Relations(policy.Subject{Ref: s}, policy.Inputs{Policies: in, Lookup: pr.in.Lookup}) {
		d := PolicyRelationDTO{Policy: l.linked(r.Policy), Relation: r.Kind, Source: r.Source, SectionName: r.Subject.SectionName, Port: r.Subject.Port}
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

// attachedCondition sets d's Attached condition, verbatim, and the pack's note on it,
// from rp's first current ancestor that is sub and reports Attached=True.
func attachedCondition(d *PolicyRelationDTO, rp relatedPolicy, sub policy.Subject) {
	for _, a := range rp.p.Ancestors {
		c := objects.FindCondition(a.Conditions, "Attached")
		if !a.Current || c == nil || c.Status != metav1.ConditionTrue || a.Ref.SectionName != sub.SectionName || a.Ref.Port != sub.Port {
			continue
		}
		dc := condition(rp.u, *c)
		d.Condition = &dc
		if k := slices.IndexFunc(a.Readings, func(r policy.Reading) bool { return r.Type == "Attached" && r.Note != "" }); k >= 0 {
			d.Note, d.NoteSource = a.Readings[k].Note, a.Readings[k].Source
		}
		return
	}
}
