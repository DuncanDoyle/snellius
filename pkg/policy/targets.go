package policy

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Lookup answers what target checks and parent joins may read (design §5.2,
// §7). The API passes one that applies the viewer's access; a one-shot CLI
// passes its snapshot and the user's own permissions.
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
	lookup := func(gk objects.GroupKind, ns, name string) (*unstructured.Unstructured, bool) {
		return in.Lookup.Object(objects.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: ns, Name: name})
	}
	served := func(gk objects.GroupKind) bool { return in.Lookup.Watched(gk, ns) }
	// Selectors are left out: the targeter would match them (D-125).
	rs := in.Targeter.PolicyTargets(computed.TargetInput{Policy: p.Ref, TargetRefs: refs, Lookup: lookup, Served: served, ScopeKnown: true})
	for _, r := range rs {
		if r.Index < 0 || r.Index >= len(refs) {
			continue
		}
		if outcome := gate(in.Lookup, ns, refs[r.Index], r.Outcome); outcome != computed.TargetUnknown {
			out[r.Index] = TargetCheck{Index: r.Index, Outcome: outcome, Source: SourceInferred}
		}
	}
	return out
}

// gate keeps a targeter's outcome only when the caller may read what it rests on
// (design §7 item 4): every outcome needs named get; not found also needs list in the namespace.
// A name the caller may not get is unknown whether or not it exists (D-119).
func gate(l Lookup, ns string, r computed.TargetRef, outcome string) string {
	gk := objects.GroupKind{Group: r.Group, Kind: r.Kind}
	if !l.Watched(gk, ns) || !l.CanGet(objects.Ref{Group: r.Group, Kind: r.Kind, Namespace: ns, Name: r.Name}) {
		return computed.TargetUnknown
	}
	switch outcome {
	case computed.TargetFound, computed.TargetSectionNotFound:
		return outcome
	case computed.TargetNotFound:
		if l.CanList(gk, ns) {
			return outcome
		}
	}
	return computed.TargetUnknown
}
