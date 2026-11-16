package own

import (
	"encoding/json"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// EvidenceFreshness is the kind of FreshnessEvidence.
const EvidenceFreshness = "freshness"

// Freshness kinds of KGWUI-003 (design §10.1), in table order: an object
// that matches two takes the first.
const (
	FreshnessNeverWritten = "never-written"
	FreshnessStale        = "stale"
	FreshnessPending      = "pending"
	FreshnessNoAncestor   = "no-ancestor"
)

// FreshnessRank is kind's place in the §10.1 table; an unknown kind ranks last.
func FreshnessRank(kind string) int {
	switch kind {
	case FreshnessNeverWritten:
		return 0
	case FreshnessStale:
		return 1
	case FreshnessPending:
		return 2
	case FreshnessNoAncestor:
		return 3
	}
	return 4
}

// FreshnessEvidence is why KGWUI-003 judges a status not current (design §10.1).
// Observed, Current and Since are set for a stale status only.
type FreshnessEvidence struct {
	Freshness string `json:"freshness"`
	Observed  *int64 `json:"observed,omitempty"`
	Current   *int64 `json:"current,omitempty"`
	// Since is when the UI first saw the current generation.
	Since *time.Time `json:"since,omitempty"`
}

// EvidenceKind tags the evidence.
func (*FreshnessEvidence) EvidenceKind() string { return EvidenceFreshness }

// MarshalJSON adds the kind tag.
func (e *FreshnessEvidence) MarshalJSON() ([]byte, error) {
	type plain FreshnessEvidence
	return json.Marshal(struct {
		Kind string `json:"kind"`
		*plain
	}{EvidenceFreshness, (*plain)(e)})
}

// EvidenceRefs is the kind of RefsEvidence.
const EvidenceRefs = "refs"

// Explanations of KGWUI-013 and 014, first match wins (design §7.3).
const (
	// RefExplainKindNotServed: discovery says the ref's group serves no such kind.
	RefExplainKindNotServed = "kind-not-served"
	// RefExplainServiceMissing: a core Service ref names a Service that does not exist.
	RefExplainServiceMissing = "service-missing"
	// RefExplainServicePort: the Service exists and has no port the ref names.
	RefExplainServicePort = "service-port"
	// RefExplainController: the controller's reason and message, as written.
	RefExplainController = "controller"
)

// RefLocation is one ref and where its referrer writes it, such as
// "spec.rules[2].backendRefs[0]". Ref has the spec defaults applied.
type RefLocation struct {
	Path string      `json:"path"`
	Ref  objects.Ref `json:"ref"`
}

// RefsEvidence is why KGWUI-013 or 014 names its refs. The condition names a
// parent or listener, not a ref, so Candidates are the refs it may be about;
// Attributed is true only with exactly one.
type RefsEvidence struct {
	Explanation string        `json:"explanation"`
	Candidates  []RefLocation `json:"candidates,omitempty"`
	Attributed  bool          `json:"attributed"`
	// Ports are the Service's ports, set for a service-port explanation.
	Ports []int32 `json:"ports,omitempty"`
	// Group is the one group discovery says serves the candidate's kind, set
	// only for an attributed kind-not-served explanation under complete
	// discovery. Never serialised.
	Group *string `json:"-"`
	// Unknown marks a candidate whose group's served state discovery does not
	// know; it may be a wrong group, so no grant is suggested. Never serialised.
	Unknown bool `json:"-"`
}

// EvidenceKind tags the evidence.
func (*RefsEvidence) EvidenceKind() string { return EvidenceRefs }

// MarshalJSON adds the kind tag.
func (e *RefsEvidence) MarshalJSON() ([]byte, error) {
	type plain RefsEvidence
	return json.Marshal(struct {
		Kind string `json:"kind"`
		*plain
	}{EvidenceRefs, (*plain)(e)})
}

// EvidenceRuleReplaced is the kind of RuleReplacedEvidence.
const EvidenceRuleReplaced = "rule-replaced"

// RuleReplacedEvidence is what KGWUI-015 quotes. MatchIndex is the controller's
// match index, not a rule index, and nil when the message does not parse.
// Policy is the served policy kind the message names, when exactly one is named.
type RuleReplacedEvidence struct {
	MatchIndex *int         `json:"matchIndex,omitempty"`
	Reason     string       `json:"reason"`
	Message    string       `json:"message"`
	Policy     *objects.Ref `json:"policy,omitempty"`
}

// EvidenceKind tags the evidence.
func (*RuleReplacedEvidence) EvidenceKind() string { return EvidenceRuleReplaced }

// MarshalJSON adds the kind tag.
func (e *RuleReplacedEvidence) MarshalJSON() ([]byte, error) {
	type plain RuleReplacedEvidence
	return json.Marshal(struct {
		Kind string `json:"kind"`
		*plain
	}{EvidenceRuleReplaced, (*plain)(e)})
}
