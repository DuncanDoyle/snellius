// Package policy is the declared policy model (Phase 8 design §5, E7): one
// policy object as written and as its controller reports it, target checks
// through the rule set's PolicyTargeter, and the relations between policies
// and the objects they name.
//
// It never resolves policy (D-121): nothing here orders policies by
// precedence, combines their fields, matches label selectors, or derives
// attachment from targets. Every value carries a Source (D-122). Lists sort
// by namespace and name only.
//
// The package knows nothing about viewers. Callers pass only what may be used:
// the API passes what the viewer may read, a one-shot CLI passes its snapshot.
package policy

import (
	"slices"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Source says where a policy value comes from (design §3.2, D-122).
type Source string

// The five sources.
const (
	// SourceDeclared is a value written in a spec.
	SourceDeclared Source = "declared"
	// SourceControllerReported is a status condition as the controller wrote it.
	SourceControllerReported Source = "controller-reported"
	// SourceControllerReportedContribution is a policy ID in a merge.* xDS entry (Phase 12).
	SourceControllerReportedContribution Source = "controller-reported-contribution"
	// SourceInferred is a value Snellius computed from declared state or reported status.
	SourceInferred Source = "inferred"
	// SourceUnknown means no source can tell, or the caller may not read the input.
	SourceUnknown Source = "unknown"
)

// Policy is one policy object as declared and as its status reports it.
type Policy struct {
	Ref objects.Ref
	// Generation is metadata.generation, which ancestor freshness compares against.
	Generation int64
	// Targets are spec.targetRefs, then spec.targetSelectors, in spec order (declared).
	Targets []Target
	// Ancestors are status.ancestors in status order (controller-reported).
	Ancestors []Ancestor
	// Annotations are the pack's precedence annotations present on the object (declared).
	Annotations []Annotation
	// Features are the spec sections other than the targets, sorted by key (declared).
	Features []Feature
	// Interpreted is true when a pack interpreter reads this policy's kind.
	Interpreted bool
}

// Target is one spec.targetRefs or spec.targetSelectors entry as written.
type Target struct {
	// Index counts targetRefs first, then targetSelectors, as computed.TargetResult does.
	Index       int
	Group, Kind string
	// Name is set for a targetRef; a selector has none.
	Name        string
	SectionName string
	// Selector is true for a targetSelectors entry; MatchLabels are its labels as written.
	Selector    bool
	MatchLabels map[string]string
	Source      Source
}

// Ref returns the object a targetRef names: its namespace is the policy's (GEP-713).
// It is meaningless for a selector.
func (t Target) Ref(policyNamespace string) objects.Ref {
	return objects.Ref{Group: t.Group, Kind: t.Kind, Namespace: policyNamespace, Name: t.Name}
}

// Why an ancestor is not current (design §6.4).
const (
	// NotCurrentOtherController: the ancestor's controller is not one of the install's.
	NotCurrentOtherController = "other-controller"
	// NotCurrentNotOwned: a Gateway or ListenerSet ancestor the install does not own.
	NotCurrentNotOwned = "not-owned"
	// NotCurrentStale: a condition describes an older generation; Ancestor.Why says so.
	NotCurrentStale = "stale"
	// NotCurrentPending: the controller has not decided the ancestor yet; Ancestor.Why says how.
	NotCurrentPending = "pending"
	// NotCurrentNotFresh: freshness could not be decided (the Guard has no Fresh).
	NotCurrentNotFresh = "not-fresh"
	// NotCurrentUnreadable: the caller may not read the ancestor, so none of the
	// install's judgments about it are shown (design §7 item 3). Read never sets it.
	NotCurrentUnreadable = "unreadable"
)

// Ancestor is one status.ancestors entry. Conditions are copied verbatim,
// whatever their type, status or reason.
type Ancestor struct {
	// Ref is the ancestorRef with the Gateway API defaults filled in.
	Ref        objects.ParentRef
	Controller string
	Conditions []metav1.Condition
	// Current is true when the install's controller wrote the entry, the install owns
	// a Gateway or ListenerSet ancestor, and every condition is fresh.
	Current bool
	// NotCurrent is a NotCurrent* value when Current is false; Why is the freshness reason.
	NotCurrent string
	Why        string
	// CurrentSource is the source of Current, NotCurrent and Why: inferred, from the Guard.
	CurrentSource Source
	// Summary is true when the pack marks the entry as its controller's synthetic
	// missing-target ancestor.
	Summary bool
	// SummarySource is the source of Summary: inferred from the pack, unknown without one.
	SummarySource Source
	// Readings are the pack's readings of conditions, only those it reads (inferred).
	Readings []Reading
	// Source is the source of Ref, Controller and Conditions: controller-reported.
	Source Source
}

// Note keys a pack may put on a Reading. The client words them.
const (
	// NoteGatewayWide: the controller reports this for the whole Gateway or
	// ListenerSet; it does not say which routes or fields are affected.
	NoteGatewayWide = "gateway-wide"
	// NoteBackendPort: the controller reports this for this backend port.
	NoteBackendPort = "backend-port"
)

// Reading is a pack's reading of one ancestor condition (design §8).
type Reading struct {
	// Type is the condition type the reading is about.
	Type string
	// Warning is true when the pack reads the condition as a warning, such as PartiallyValid.
	Warning bool
	// Info is true when the pack reads the condition as worth an info finding, such as Overridden.
	Info bool
	// Note is a Note* key, or "" for none.
	Note string
	// Message is the pack's operator wording of the condition for its finding;
	// "" leaves the core's neutral wording.
	Message string
	Source  Source
}

// zero reports whether r says nothing.
func (r Reading) zero() bool { return !r.Warning && !r.Info && r.Note == "" && r.Message == "" }

// Annotation is a precedence annotation as written (design §5.5). The
// controller uses it to order policies; Snellius never evaluates it.
type Annotation struct {
	Key, Value string
	Source     Source
}

// Feature is one spec section as written.
type Feature struct {
	// Key is the spec key; Label is the pack's name for it, "" without one.
	Key, Label string
	// Value is the section as written (a deep copy).
	Value  any
	Source Source
}

// Interpreter is a pack's reading of its own policy kinds (design §8). Read
// asks it about a kind only when Handles reports true, so a core or
// downstream kind gets no interpretation from another pack.
type Interpreter interface {
	// Handles reports whether gk is one of the pack's policy kinds.
	Handles(gk objects.GroupKind) bool
	// FeatureLabel returns the label of spec section key, or "" to show the key.
	FeatureLabel(gk objects.GroupKind, key string) string
	// PrecedenceAnnotations returns the annotation keys the controller orders policies of gk by.
	PrecedenceAnnotations(gk objects.GroupKind) []string
	// Summary reports whether ancestor is the controller's synthetic missing-target ancestor.
	Summary(gk objects.GroupKind, ancestor objects.ParentRef) bool
	// Condition returns the pack's reading of c on ancestor; the zero Reading means none.
	Condition(gk objects.GroupKind, ancestor objects.ParentRef, c metav1.Condition) Reading
}

// Guard decides which ancestors are current (design §6.4). It carries the
// install's checks that the existing policy findings apply in
// pkg/health/own: the controller names, ownership of a Gateway or ListenerSet
// ancestor, and own.PolicyFreshness.
// A nil function makes no ancestor current.
type Guard struct {
	// ControllerNames are the install's controller names.
	ControllerNames []string
	// Owns reports whether the install may report on ancestor: false for a Gateway or
	// ListenerSet it does not own, true for every other ancestor kind.
	Owns func(ancestor objects.Ref) bool
	// Fresh returns "" when the ancestor's conditions on u are current, or why not and
	// whether that is because they are stale (else pending).
	Fresh func(u *unstructured.Unstructured, conditions []metav1.Condition) (why string, stale bool)
}

// targetsKeys are the spec keys that are targets, not features.
var targetsKeys = []string{"targetRefs", "targetSelectors"}

// Read returns policy u as declared and reported. It is pure: interp may be
// nil (no pack: raw spec keys, nothing interpreted), and g only decides Current.
func Read(u *unstructured.Unstructured, interp Interpreter, g Guard) Policy {
	ref := objects.RefOf(u)
	gk := objects.GroupKind{Group: ref.Group, Kind: ref.Kind}
	if interp != nil && !interp.Handles(gk) {
		interp = nil
	}
	p := Policy{Ref: ref, Generation: u.GetGeneration(), Interpreted: interp != nil}
	p.Targets = readTargets(u)
	p.Ancestors = readAncestors(u, gk, interp, g)
	p.Annotations = readAnnotations(u, gk, interp)
	p.Features = readFeatures(u, gk, interp)
	return p
}

// readTargets decodes the targets as written, refs first.
func readTargets(u *unstructured.Unstructured) []Target {
	refs, sels := computed.DecodeTargets(u)
	out := make([]Target, 0, len(refs)+len(sels))
	for _, r := range refs {
		out = append(out, Target{Index: len(out), Group: r.Group, Kind: r.Kind, Name: r.Name, SectionName: r.SectionName, Source: SourceDeclared})
	}
	for _, s := range sels {
		out = append(out, Target{Index: len(out), Group: s.Group, Kind: s.Kind, SectionName: s.SectionName, Selector: true,
			MatchLabels: s.MatchLabels, Source: SourceDeclared})
	}
	return out
}

// readAncestors copies status.ancestors and decides Current with g.
func readAncestors(u *unstructured.Unstructured, gk objects.GroupKind, interp Interpreter, g Guard) []Ancestor {
	as := objects.PolicyAncestors(u)
	out := make([]Ancestor, 0, len(as))
	for _, a := range as {
		an := Ancestor{Ref: a.AncestorRef, Controller: a.ControllerName, Conditions: slices.Clone(a.Conditions), Source: SourceControllerReported,
			CurrentSource: SourceInferred, SummarySource: SourceUnknown}
		// The order of pkg/health/own policyAncestors: controller, ownership, freshness.
		switch {
		case !slices.Contains(g.ControllerNames, a.ControllerName):
			an.NotCurrent = NotCurrentOtherController
		case g.Owns == nil || !g.Owns(a.AncestorRef.Ref()):
			an.NotCurrent = NotCurrentNotOwned
		case g.Fresh == nil:
			an.NotCurrent, an.Why = NotCurrentNotFresh, "Freshness unknown"
		default:
			if why, stale := g.Fresh(u, a.Conditions); why != "" {
				an.NotCurrent, an.Why = NotCurrentPending, why
				if stale {
					an.NotCurrent = NotCurrentStale
				}
			} else {
				an.Current = true
			}
		}
		if interp != nil {
			an.Summary, an.SummarySource = interp.Summary(gk, a.AncestorRef), SourceInferred
			for _, c := range a.Conditions {
				if r := interp.Condition(gk, a.AncestorRef, c); !r.zero() {
					r.Type, r.Source = c.Type, SourceInferred
					an.Readings = append(an.Readings, r)
				}
			}
		}
		out = append(out, an)
	}
	return out
}

// readAnnotations returns the pack's precedence annotations present on u, in the pack's key order.
func readAnnotations(u *unstructured.Unstructured, gk objects.GroupKind, interp Interpreter) []Annotation {
	if interp == nil {
		return nil
	}
	all := u.GetAnnotations()
	var out []Annotation
	for _, k := range interp.PrecedenceAnnotations(gk) {
		if v, ok := all[k]; ok {
			out = append(out, Annotation{Key: k, Value: v, Source: SourceDeclared})
		}
	}
	return out
}

// readFeatures returns the spec sections other than the targets, sorted by key.
func readFeatures(u *unstructured.Unstructured, gk objects.GroupKind, interp Interpreter) []Feature {
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	var out []Feature
	for k, v := range spec {
		if slices.Contains(targetsKeys, k) {
			continue
		}
		f := Feature{Key: k, Value: runtime.DeepCopyJSONValue(v), Source: SourceDeclared}
		if interp != nil {
			f.Label = interp.FeatureLabel(gk, k)
		}
		out = append(out, f)
	}
	slices.SortFunc(out, func(a, b Feature) int { return strings.Compare(a.Key, b.Key) })
	return out
}
