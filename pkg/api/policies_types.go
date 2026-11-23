package api

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// PoliciesResponse is GET …/policies (Phase 8 design §6.2): the install's
// policies the caller may view, sorted by namespace, name, group and kind.
// Total and Facets count readable policies only.
type PoliciesResponse struct {
	Page[PolicyRow]
	// Facets are the kind, feature and namespace filter values and the has-problems
	// count, each counted with every filter but its own.
	Facets PolicyFacetsDTO `json:"facets"`
}

// PolicyRow is one policy in the list.
type PolicyRow struct {
	// Ref is the policy, kind with group; its link opens the policy detail.
	Ref LinkedRef `json:"ref"`
	// Targets are the targetRefs, then the targetSelectors, as written; refs carry their check.
	Targets []PolicyTargetDTO `json:"targets"`
	// Accepted is the worst Accepted over the current ancestors; nil when none reports one.
	Accepted *PolicyAcceptedDTO `json:"accepted,omitempty"`
	// AttachedOn are the current ancestors that report Attached=True.
	AttachedOn []PolicyAttachedDTO `json:"attachedOn"`
	// Features are the spec sections other than the targets, by key.
	Features []PolicyFeatureDTO `json:"features"`
	Problems PolicyProblemsDTO  `json:"problems"`
}

// PolicyTargetDTO is one targetRef or targetSelector as written (declared).
// Ref and Health are set for a detail's targetRefs only: Ref is the named
// object in the policy's namespace, its link none and Health empty unless the
// caller may get it by name.
type PolicyTargetDTO struct {
	Index       int               `json:"index"`
	Group       string            `json:"group"`
	Kind        string            `json:"kind"`
	Name        string            `json:"name,omitempty"`
	SectionName string            `json:"sectionName,omitempty"`
	Selector    bool              `json:"selector,omitempty"`
	MatchLabels map[string]string `json:"matchLabels,omitempty"`
	Source      policy.Source     `json:"source"`
	// Check is a targetRef's check (design §5.2); selectors have none.
	Check  *PolicyCheckDTO `json:"check,omitempty"`
	Ref    *LinkedRef      `json:"ref,omitempty"`
	Health health.State    `json:"health,omitempty"`
	// HealthSource is Health's source, inferred; empty without Health.
	HealthSource policy.Source `json:"healthSource,omitempty"`
}

// PolicyCheckDTO is a target check: found, not-found, section-not-found or unknown.
type PolicyCheckDTO struct {
	Outcome string        `json:"outcome"`
	Source  policy.Source `json:"source"`
}

// PolicyAcceptedDTO is the worst Accepted condition over a policy's current
// ancestors (inferred): False before True, the first in status order.
type PolicyAcceptedDTO struct {
	Status metav1.ConditionStatus `json:"status"`
	Reason string                 `json:"reason"`
	Source policy.Source          `json:"source"`
}

// PolicyAttachedDTO is a current ancestor that reports Attached=True (controller-reported).
type PolicyAttachedDTO struct {
	objects.ParentRef
	Source policy.Source `json:"source"`
}

// PolicyFeatureDTO is one spec section (declared). Label is the pack's name
// for Key, empty without one; Value is the section as written, detail only.
type PolicyFeatureDTO struct {
	Key    string        `json:"key"`
	Label  string        `json:"label,omitempty"`
	Value  any           `json:"value,omitempty"`
	Source policy.Source `json:"source"`
}

// PolicyProblemsDTO says whether a policy has problems (inferred): a current
// ancestor condition False, a condition the pack reads as a warning, or a
// target check not-found or section-not-found the caller may know of.
type PolicyProblemsDTO struct {
	Has    bool          `json:"has"`
	Source policy.Source `json:"source"`
}

// PolicyFacetsDTO are the policies list's filter values.
type PolicyFacetsDTO struct {
	Kinds       []PolicyKindFacetDTO      `json:"kinds"`
	Features    []PolicyFeatureFacetDTO   `json:"features"`
	Namespaces  []PolicyNamespaceFacetDTO `json:"namespaces"`
	HasProblems int                       `json:"hasProblems"`
}

// PolicyKindFacetDTO is a policy kind and its readable policies.
type PolicyKindFacetDTO struct {
	Group    string `json:"group"`
	Kind     string `json:"kind"`
	Policies int    `json:"policies"`
}

// PolicyFeatureFacetDTO is a spec key and the readable policies that set it.
type PolicyFeatureFacetDTO struct {
	Key      string `json:"key"`
	Policies int    `json:"policies"`
}

// PolicyNamespaceFacetDTO is a namespace and its readable policies.
type PolicyNamespaceFacetDTO struct {
	Namespace string `json:"namespace"`
	Policies  int    `json:"policies"`
}

// PolicyDetail is GET …/policies/{group}/{kind}/{ns}/{name} (Phase 8 design §6.3).
type PolicyDetail struct {
	Install string    `json:"install"`
	Ref     LinkedRef `json:"ref"`
	// YAML is the install-scoped YAML path of the policy, empty when it is not admitted.
	YAML       string `json:"yaml,omitempty"`
	Generation int64  `json:"generation"`
	// Interpreted is true when the install's pack reads this kind.
	Interpreted bool                  `json:"interpreted"`
	Targets     []PolicyTargetDTO     `json:"targets"`
	Ancestors   []PolicyAncestorDTO   `json:"ancestors"`
	Accepted    *PolicyAcceptedDTO    `json:"accepted,omitempty"`
	AttachedOn  []PolicyAttachedDTO   `json:"attachedOn"`
	Features    []PolicyFeatureDTO    `json:"features"`
	Annotations []PolicyAnnotationDTO `json:"annotations"`
	Problems    PolicyProblemsDTO     `json:"problems"`
	CollectedAt time.Time             `json:"collectedAt"`
}

// PolicyAncestorDTO is one status.ancestors entry with its conditions
// (controller-reported, Source). NotCurrent says why an entry is left out of
// the summaries: other-controller, not-owned, not-fresh with Why, or
// unreadable. Link is none and Health empty unless the caller may get the
// ancestor by name. Snellius derives Current, NotCurrent, Why, Summary, each
// condition's Stale and Health; each carries its own source field.
type PolicyAncestorDTO struct {
	Ref         LinkedRef    `json:"ref"`
	SectionName string       `json:"sectionName,omitempty"`
	Port        int32        `json:"port,omitempty"`
	Health      health.State `json:"health,omitempty"`
	// HealthSource is Health's source, inferred; empty without Health.
	HealthSource policy.Source `json:"healthSource,omitempty"`
	Controller   string        `json:"controller"`
	Conditions   []Condition   `json:"conditions"`
	// StaleSource is the source of every condition's Stale: inferred.
	StaleSource policy.Source `json:"staleSource"`
	Current     bool          `json:"current"`
	NotCurrent  string        `json:"notCurrent,omitempty"`
	Why         string        `json:"why,omitempty"`
	// CurrentSource is the source of Current, NotCurrent and Why: inferred.
	CurrentSource policy.Source `json:"currentSource"`
	Summary       bool          `json:"summary,omitempty"`
	// SummarySource is Summary's source: inferred from the pack, unknown without one.
	SummarySource policy.Source      `json:"summarySource"`
	Readings      []PolicyReadingDTO `json:"readings"`
	Source        policy.Source      `json:"source"`
}

// PolicyReadingDTO is the pack's reading of one ancestor condition (inferred).
type PolicyReadingDTO struct {
	Type    string        `json:"type"`
	Warning bool          `json:"warning,omitempty"`
	Note    string        `json:"note,omitempty"`
	Source  policy.Source `json:"source"`
}

// PolicyAnnotationDTO is a precedence annotation as written (declared); kgateway evaluates it.
type PolicyAnnotationDTO struct {
	Key    string        `json:"key"`
	Value  string        `json:"value"`
	Source policy.Source `json:"source"`
}

// PolicyRelationsDTO are the readable policies that name a subject (Phase 8 design §6.5),
// each group sorted by policy namespace and name, never by precedence (D-126).
// A group that does not apply to the subject's kind is omitted.
type PolicyRelationsDTO struct {
	// Targeting are the policies whose targetRefs name the subject, or one of its sections.
	Targeting []PolicyRelationDTO `json:"targeting"`
	// TargetingParent are the policies whose targetRefs name a parent the subject
	// declares (design §5.3); routes and ListenerSets only.
	TargetingParent *[]PolicyRelationDTO `json:"targetingParent,omitempty"`
	// AttachedHere are the policies with a current Attached=True ancestor that is the
	// subject; Gateways, ListenerSets and backends only.
	AttachedHere *[]PolicyRelationDTO `json:"attachedHere,omitempty"`
	// Selectors backs the note that policies selecting by label are not listed (D-125).
	Selectors PolicySelectorsDTO `json:"selectors"`
}

// PolicyRelationDTO is one relation between a policy and the subject. SectionName and
// Port are the section or port the targetRef or ancestor names; Via is the declared
// parentRef path a targets-parent relation walked, the policy naming its last hop.
// Condition, Note and NoteSource are set on attached-on-ancestor relations only.
type PolicyRelationDTO struct {
	Policy      LinkedRef           `json:"policy"`
	Relation    policy.RelationKind `json:"relation"`
	Source      policy.Source       `json:"source"`
	SectionName string              `json:"sectionName,omitempty"`
	Port        int32               `json:"port,omitempty"`
	Via         []PolicyHopDTO      `json:"via,omitempty"`
	// Condition is the ancestor's Attached condition, verbatim.
	Condition *Condition `json:"condition,omitempty"`
	// Note is the pack's granularity note for that condition; NoteSource is inferred.
	Note       string        `json:"note,omitempty"`
	NoteSource policy.Source `json:"noteSource,omitempty"`
}

// PolicyHopDTO is one parentRef on a relation's path, as written, linked by name.
type PolicyHopDTO struct {
	Ref         LinkedRef `json:"ref"`
	SectionName string    `json:"sectionName,omitempty"`
	Port        int32     `json:"port,omitempty"`
}

// PolicySelectorsDTO is what the subject's "select by label" note links to: the
// Policies list in Namespace, filtered to Kinds. Kinds are the kinds of the readable
// policies in Namespace whose targetSelectors name the subject's kind (declared).
type PolicySelectorsDTO struct {
	Namespace string              `json:"namespace"`
	Kinds     []objects.GroupKind `json:"kinds"`
	Source    policy.Source       `json:"source"`
}
