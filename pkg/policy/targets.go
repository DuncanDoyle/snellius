package policy

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Lookup answers what target checks and parent joins may read (design §5.2,
// §7). The API passes one that applies the viewer's access; a one-shot CLI
// passes its snapshot and the user's own permissions. CheckTargets never passes
// a targeter more of it than the one target that targeter checks.
type Lookup interface {
	// Object returns the collected object named ref, whatever the caller may read;
	// false when the collection has none.
	Object(ref objects.Ref) (*unstructured.Unstructured, bool)
	// Watched reports whether kind gk is served and its collection holds namespace ns:
	// false when the watch is denied, pending or scoped away from ns.
	Watched(gk objects.GroupKind, ns string) bool
	// CanGet reports whether the caller may get the object named ref.
	CanGet(ref objects.Ref) bool
	// CanList reports whether the caller may list kind gk in namespace ns.
	CanList(gk objects.GroupKind, ns string) bool
}

// Inputs are what CheckTargets and Relations may use. The caller decides what
// goes in (design §5.1): Policies holds only the policies it may use.
type Inputs struct {
	// Policies are the policies, read by Read.
	Policies []Policy
	// Lookup answers reads and access; nil makes every check unknown and every relation absent.
	Lookup Lookup
	// Targeter is the install's rule set's PolicyTargeter; nil makes every check unknown.
	// CheckTargets calls it once per targetRef, and its Lookup and Served then answer
	// only that target's object and kind: a check rests on its own target alone (design §7 item 4).
	Targeter computed.PolicyTargeter
	// ScopeKnown is false when the install's discovery scope is unknown (D-024).
	ScopeKnown bool
}

// TargetCheck is the outcome of one targetRef (design §5.2).
type TargetCheck struct {
	// Index is the Target's index.
	Index int
	// Outcome is computed.TargetFound, TargetNotFound, TargetSectionNotFound or TargetUnknown.
	Outcome string
	// Source is inferred for a known outcome, unknown otherwise.
	Source Source
}

// CheckTargets checks p's targetRefs through in.Targeter, the same answer
// KGWUI-016 uses. Selectors are never matched (D-125) and get no check.
func CheckTargets(p Policy, in Inputs) []TargetCheck {
	var refs []computed.TargetRef
	for _, t := range p.Targets {
		if !t.Selector {
			refs = append(refs, computed.TargetRef{Group: t.Group, Kind: t.Kind, Name: t.Name, SectionName: t.SectionName})
		}
	}
	if len(refs) == 0 {
		return nil
	}
	out := make([]TargetCheck, len(refs))
	for i := range out {
		out[i] = TargetCheck{Index: i, Outcome: computed.TargetUnknown, Source: SourceUnknown}
	}
	if in.Targeter == nil || in.Lookup == nil || !in.ScopeKnown {
		return out
	}
	ns := p.Ref.Namespace
	for i, tr := range refs {
		// One call per target, reading that target alone: the gate authorises only it.
		target := objects.Ref{Group: tr.Group, Kind: tr.Kind, Namespace: ns, Name: tr.Name}
		gk := objects.GroupKind{Group: tr.Group, Kind: tr.Kind}
		lookup := func(g objects.GroupKind, n, name string) (*unstructured.Unstructured, bool) {
			if r := (objects.Ref{Group: g.Group, Kind: g.Kind, Namespace: n, Name: name}); r != target {
				return nil, false
			}
			return in.Lookup.Object(target)
		}
		served := func(g objects.GroupKind) bool { return g == gk && in.Lookup.Watched(gk, ns) }
		// Selectors are left out: the targeter would match them (D-125).
		rs := in.Targeter.PolicyTargets(computed.TargetInput{Policy: p.Ref, TargetRefs: []computed.TargetRef{tr}, Lookup: lookup, Served: served,
			ScopeKnown: true})
		for _, r := range rs {
			if r.Index != 0 {
				continue
			}
			if outcome := gate(in.Lookup, ns, tr, r.Outcome); outcome != computed.TargetUnknown {
				out[i] = TargetCheck{Index: i, Outcome: outcome, Source: SourceInferred}
			}
		}
	}
	return out
}

// gate keeps a targeter's outcome only when the caller may read what it rests on
// (design §7 item 4): every outcome needs named get; not found also needs list in the namespace.
// A name the caller may not get is unknown whether or not it exists (D-119). An
// outcome already unknown asks nothing, so no check is spent on it.
func gate(l Lookup, ns string, r computed.TargetRef, outcome string) string {
	gk := objects.GroupKind{Group: r.Group, Kind: r.Kind}
	switch outcome {
	case computed.TargetFound, computed.TargetSectionNotFound, computed.TargetNotFound:
	default:
		return computed.TargetUnknown
	}
	if !l.Watched(gk, ns) || !l.CanGet(objects.Ref{Group: r.Group, Kind: r.Kind, Namespace: ns, Name: r.Name}) {
		return computed.TargetUnknown
	}
	if outcome == computed.TargetNotFound && !l.CanList(gk, ns) {
		return computed.TargetUnknown
	}
	return outcome
}
