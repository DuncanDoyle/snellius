// Package health turns cluster state and controller signals into findings and
// a health state per install (design §5).
package health

import (
	"crypto/sha256"
	"reflect"
	"slices"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/platform"
	"github.com/DuncanDoyle/snellius/pkg/process"
	"github.com/DuncanDoyle/snellius/pkg/proxymode"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

// Severity of a finding.
type Severity string

// Severities.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

// State is the rolled-up health of an install.
type State string

// States, from worst to best: error, unknown, warning, healthy.
const (
	StateError   State = "error"
	StateUnknown State = "unknown"
	StateWarning State = "warning"
	StateHealthy State = "healthy"
)

// Stable finding codes. Never reuse a code (design §5.1).
const (
	CodeXDSRejected             = "KGWUI-001"
	CodeConditionFalse          = "KGWUI-002"
	CodeRouteUnattached         = "KGWUI-003"
	CodeNamespaceNotWatched     = "KGWUI-004"
	CodeControllerNotReady      = "KGWUI-005"
	CodeProxyNotReady           = "KGWUI-006"
	CodeScopeMismatch           = "KGWUI-007"
	CodeSignalUnavailable       = "KGWUI-008"
	CodeAmbiguousOwnership      = "KGWUI-009"
	CodeSelfManagedProxy        = "KGWUI-010"
	CodeDelegatedNotAttached    = "KGWUI-011"
	CodeComputedDisagrees       = "KGWUI-012"
	CodeRefUnresolved           = "KGWUI-013"
	CodeRefNotPermitted         = "KGWUI-014"
	CodeProcessRestarting       = "KGWUI-019"
	CodeProbeFailing            = "KGWUI-020"
	CodeRolloutStuck            = "KGWUI-021"
	CodeVersionSkew             = "KGWUI-022"
	CodeBackendNoReadyEndpoints = "KGWUI-023"
	CodeProxyConfigWarming      = "KGWUI-025"
	CodeProxyDisconnected       = "KGWUI-026"
)

// Grace is how long a resource may lack current status before it is reported
// as not yet processed.
const Grace = own.Grace

// Class says who may see a finding (D-106).
type Class uint8

// Classes. The zero value is derived, so every finding is derived unless runOwn marks it.
const (
	// ClassDerived findings are shown only to a viewer with the full read.
	ClassDerived Class = iota
	// ClassOwn findings come from package own and are shown per finding.
	ClassOwn
)

// Class is f's class: ClassOwn only for a finding from package own.
func (f *Finding) Class() Class { return f.class }

// Derived is f without its visibility marks: derived, with no basis. It only
// hides f from more viewers, so any caller may use it.
func (f Finding) Derived() Finding {
	f.class, f.Basis = ClassDerived, nil
	return f
}

// Basis is what decided a finding, for who may see it (D-106).
type Basis struct {
	// Collection marks a finding decided only by Snellius's own collection and access state.
	Collection bool
	// Refs are the objects an own-object analyzer named, present or absent.
	Refs []objects.Ref
	// text is a collection finding's fixed Detail, which names no object, for Project.
	text string
}

// refs are b's Refs; nil for a nil b.
func (b *Basis) refs() []objects.Ref {
	if b == nil {
		return nil
	}
	return b.Refs
}

// collection reports whether b marks a collection finding, by flag or fixed text.
func (b *Basis) collection() bool { return b != nil && (b.Collection || b.text != "") }

// fixedText is b's fixed collection Detail; "" for a nil b.
func (b *Basis) fixedText() string {
	if b == nil {
		return ""
	}
	return b.text
}

// Only conditionsCheck (through runOwn) may mark a finding own-object, and only
// the collection checks may mark output collection; Evaluate clears the rest.
func ownTrusted(c Check) bool { _, ok := c.(conditionsCheck); return ok }

func collectionTrusted(c Check) bool {
	switch c.(type) {
	case cacheCheck, kindsCheck, endpointsCheck:
		return true
	}
	return false
}

// ownedSet is what runOwn produced in one check run, by content. confine
// keeps ClassOwn only on a finding that matches one of them, once each, so
// a copy changed or duplicated after runOwn is derived.
type ownedSet struct {
	// keys are the registered findings' keys, by the digest of the rest of their content.
	keys map[[sha256.Size]byte][][]string
	// scratch holds the last print, reused so a lookup does not allocate.
	scratch []byte
}

// keyField is Finding.Key's index; ownedSet compares keys apart from the rest.
var keyField = func() int {
	f, _ := reflect.TypeFor[Finding]().FieldByName("Key")
	return f.Index[0]
}()

// digest is f's content but its Key as ownedSet compares it: every field,
// exported or not, of f and all it points to (deepPrint); false when it
// cannot be printed.
func (o *ownedSet) digest(f *Finding) ([sha256.Size]byte, bool) {
	p, ok := printFields(o.scratch[:0], reflect.ValueOf(f).Elem(), keyField, 0)
	o.scratch = p
	if !ok {
		return [sha256.Size]byte{}, false
	}
	return sha256.Sum256(p), true
}

// find is the index of f's key among those registered under d, or -1.
func (o *ownedSet) find(d [sha256.Size]byte, f *Finding) int {
	return slices.IndexFunc(o.keys[d], func(k []string) bool { return slices.Equal(k, f.Key) })
}

// rekey runs rewrite, which changes only f's Key; a registered own-object
// finding stays registered under its new key. Only the key normalisers call it.
func (o *ownedSet) rekey(f *Finding, rewrite func()) {
	if o == nil || f.class != ClassOwn {
		rewrite()
		return
	}
	d, ok := o.digest(f)
	i := o.find(d, f)
	rewrite()
	if ok && i >= 0 {
		o.keys[d][i] = slices.Clone(f.Key)
	}
}

// add registers f.
func (o *ownedSet) add(f Finding) {
	if o.keys == nil {
		o.keys = map[[sha256.Size]byte][][]string{}
	}
	if d, ok := o.digest(&f); ok {
		o.keys[d] = append(o.keys[d], slices.Clone(f.Key))
	}
}

// take reports whether f matches a registered finding not yet taken, and takes it.
func (o *ownedSet) take(f *Finding) bool {
	if o == nil {
		return false
	}
	d, ok := o.digest(f)
	if !ok {
		return false
	}
	i := o.find(d, f)
	if i < 0 {
		return false
	}
	ks := o.keys[d]
	ks[i] = ks[len(ks)-1]
	o.keys[d] = ks[:len(ks)-1]
	return true
}

// confine clears from c's output every visibility mark c may not set (D-106),
// and ClassOwn from any finding of a trusted check that owned does not hold.
// It copies fs before it changes it.
func confine(c Check, fs []Finding, cr CheckResult, owned *ownedSet) ([]Finding, CheckResult) {
	own, coll := ownTrusted(c), collectionTrusted(c)
	if !coll {
		cr.Collection, cr.fixed = false, ""
	}
	// A trusted check keeps ClassOwn only on what runOwn produced, unchanged.
	var forged []bool
	if own {
		forged = make([]bool, len(fs))
		for i := range fs {
			forged[i] = fs[i].class != ClassDerived && !owned.take(&fs[i])
		}
	}
	marked := func(i int) bool {
		f := &fs[i]
		return !own && (f.class != ClassDerived || len(f.Basis.refs()) > 0) || own && forged[i] || !coll && f.Basis.collection()
	}
	marks := false
	for i := range fs {
		marks = marks || marked(i)
	}
	if !marks {
		return fs, cr
	}
	// The clone shares each Basis with fs, so a changed one is replaced, not edited.
	fs = slices.Clone(fs)
	for i := range fs {
		if !own || forged[i] {
			fs[i].class = ClassDerived
		}
		if fs[i].Basis == nil {
			continue
		}
		b := *fs[i].Basis
		if !own || forged[i] {
			b.Refs = nil
		}
		if !coll {
			b.Collection, b.text = false, ""
		}
		fs[i].Basis = nil
		if len(b.Refs) > 0 || b.Collection || b.text != "" {
			fs[i].Basis = &b
		}
	}
	return fs, cr
}

// Readable says which objects a viewer may read.
type Readable interface {
	Ref(objects.Ref) bool
}

// Finding is one problem or notable fact about an install.
type Finding struct {
	// ID is stable across recomputes and restarts and unique within the
	// install; Evaluate sets it from Code, Resource and Key (design §5.1).
	ID       string       `json:"id,omitempty"`
	Code     string       `json:"code"`
	Slug     string       `json:"slug"`
	Severity Severity     `json:"severity"`
	Install  string       `json:"install"`
	Resource *objects.Ref `json:"resource,omitempty"`
	Message  string       `json:"message"`
	Detail   string       `json:"detail,omitempty"`
	// Since is when the problem started: a condition's lastTransitionTime, or
	// for a finding returned without one, when the UI first saw its ID.
	Since *time.Time `json:"since,omitempty"`
	// Group is the ID of the group whose cause explains the finding (design §5.2).
	Group string `json:"group,omitempty"`
	// Key is the code's discriminator among findings on one resource (design
	// §5.1); Evaluate hashes it into ID and clears it. Never serialised.
	Key []string `json:"-"`
	// Restricted marks a finding on a resource the caller may not view. The
	// API keeps its code, severity and resource and empties Message, Detail
	// and Since (D-013). Checks never set it.
	Restricted bool `json:"restricted,omitempty"`
	// Evidence are the objects, other than Resource, whose data Detail or
	// Facts quote (KGWUI-011: the parent route). Never serialised.
	Evidence []objects.Ref `json:"-"`
	// Facts are the typed evidence; the API drops them for a caller who may
	// not view Resource and all of Evidence (design §14 rule 12).
	Facts Evidence `json:"evidence,omitempty"`
	// ResourceSince is when the engine first saw Resource's current
	// generation. It replaces Since for a caller who may not view all of
	// Evidence, so Since tells that caller nothing about it. Never serialised.
	ResourceSince *time.Time `json:"-"`
	// class is ClassOwn for a finding from package own, else ClassDerived.
	// Only fromOwn sets it; Class reads it. Never serialised.
	class Class
	// Basis is what decided the finding, nil when nothing did; runOwn sets its
	// Refs. A pointer keeps the other findings small. Never serialised.
	Basis *Basis `json:"-"`
	// parent is the route parentRef a KGWUI-003 or KGWUI-011 finding is about;
	// it orders findings. A pointer keeps the other findings small.
	parent *objects.ParentRef
}

// CheckResult records whether a check could collect current input. A Required
// check that is not OK forces the state to unknown, unless Unavailable is set.
type CheckResult struct {
	Name        string    `json:"name"`
	OK          bool      `json:"ok"`
	Required    bool      `json:"required"`
	CollectedAt time.Time `json:"collectedAt"`
	Error       string    `json:"error,omitempty"`
	// Detail is extra context for an OK check, such as where a scope was read from.
	Detail string `json:"detail,omitempty"`
	// Pending marks a check that is not OK only because resources are still
	// being processed inside the grace period.
	Pending bool `json:"pending,omitempty"`
	// Unavailable says why the check's optional input is missing; RollUp ignores it.
	Unavailable Availability `json:"unavailable,omitempty"`
	// Gaps are the Gateways a per-Gateway input has nothing for, in Ref order.
	// Never serialised: a Gateway page reads its own entry after authorizing it.
	Gaps []GatewayGap `json:"-"`
	// Withheld are the Gateways whose proxies may get no config updates, in Ref order (R105).
	// Never serialised: a Gateway page reads its own entry after authorizing it.
	Withheld []WithheldGateway `json:"-"`
	// ControllerLogs says whether a check that reads the controller log read it
	// (LogsRead or LogsNotRead); empty for other checks. Never serialised.
	ControllerLogs string `json:"-"`
	// Hold says the check is still deciding on some items; nil when it waits on none.
	// Evaluate sets it from the run's RunContext.Hold calls (D-068).
	Hold *Hold `json:"hold,omitempty"`
	// NotAssessed marks a result shown as "Not checked" to a viewer without the full read.
	NotAssessed bool `json:"notAssessed,omitempty"`
	// Collection marks a collection or access failure (a watch denied, the
	// cache not synced). Every viewer sees it, Error replaced by fixed text
	// that names no object. Never serialised.
	Collection bool `json:"-"`
	// fixed is a collection result's fixed Error, for Project.
	fixed string
}

// Hold is what a check waits on before it decides: counts and a time, never
// object refs, so every caller may see it (D-068).
type Hold struct {
	// Until is the earliest moment a held item is decided.
	Until time.Time `json:"until"`
	// Count is how many items are held, in the unit the check raises its finding on.
	Count int `json:"count"`
	// Reason is generic text that never names an object.
	Reason string `json:"reason"`
}

// WithheldGateway is a Gateway whose snapshot deferral recurred after its
// proxies and the controller started: the first and last such line.
type WithheldGateway struct {
	Gateway     objects.Ref
	First, Last time.Time
}

// GatewayGap says why a check's input is not available for one Gateway.
type GatewayGap struct {
	Gateway     objects.Ref
	Unavailable Availability
	Reason      string
}

// Input is everything a check may look at for one install.
type Input struct {
	Install    install.Resolved
	Owned      *install.Owned
	Assignment install.Assignment
	Snapshot   *cache.Snapshot
	// Served are the served kinds; they map a parentRef's kind to its Access.
	Served []kinds.Served
	// KindsChange is how the kinds served now differ from Served, which the cache watches.
	KindsChange kinds.Change
	// Catalog is every kind the API server serves; the zero Catalog knows none.
	Catalog kinds.Catalog
	// FirstSeen returns when the engine first saw u's current generation; the
	// grace period runs from it. Nil means u's creation time.
	FirstSeen func(u *unstructured.Unstructured) time.Time
	Verify    scopeverify.Result
	Now       time.Time
	// Platform decides the wording of readiness findings; zero means Kubernetes.
	Platform platform.Info
	// ProxyMode decides whether a Gateway is self-managed; nil means proxymode.GatewayParameters{}.
	ProxyMode proxymode.Resolver
	// Computed are the install's computed views with their check states; nil means none.
	Computed *computed.Views
	// Views decodes Gateways, ListenerSets and routes; nil means objects.Direct.
	// Its views are shared and read-only.
	Views objects.Decoder
	// Process is the install's process data; a check declares InputSet.Process to read it.
	Process *ProcessData
	// GatewayAPI is the rule-set selection's Gateway API table (design §9.4); nil means none.
	GatewayAPI func(kgatewayMinor string) (bundle string, ok bool)
	// Rules is the install's rule set; one without a PolicyTargeter raises no computed KGWUI-016.
	Rules computed.Selection
	// Conditions are the install's condition types beyond Gateway API's; nil
	// means KgatewayConditions, as for a caller that builds Input itself.
	Conditions *ConditionTypes
}

// ConditionTypes are an implementation's route condition types beyond
// Gateway API's (Phase 5 design §9).
type ConditionTypes = own.ConditionTypes

// KgatewayConditions are kgateway's: kgateway.dev/Programmed, and the
// replaced rule on Accepted (2.3) or kgateway.dev/Programmed (2.4 on).
func KgatewayConditions() ConditionTypes {
	return ConditionTypes{RouteOptional: []string{kgatewayProgrammed}, RouteRejected: []string{"Accepted", kgatewayProgrammed}}
}

// kgatewayProgrammed is kgateway's route condition type.
const kgatewayProgrammed = own.KgatewayProgrammed

// conditions returns in.Conditions, or KgatewayConditions when it is unset.
func (in Input) conditions() ConditionTypes {
	if in.Conditions == nil {
		return KgatewayConditions()
	}
	return *in.Conditions
}

// ProcessData is one install's controller and per-Gateway proxy processes
// (§9.1); Print hashes both, and process.PodRef gives each Pod's ref.
type ProcessData struct {
	Controller process.Set
	Proxies    map[objects.Ref]process.Set
	Print      uint64
}

// views returns in.Views, or objects.Direct when it is unset.
func (in Input) views() objects.Decoder {
	if in.Views == nil {
		return objects.Direct{}
	}
	return in.Views
}

// routeParents returns route u's status.parents, decoded through in.Views.
func (in Input) routeParents(u *unstructured.Unstructured) []objects.RouteParentStatus {
	if gvk := u.GroupVersionKind(); gvk.Group == objects.GatewayAPIGroup && gvk.Kind == "HTTPRoute" {
		return in.views().HTTPRoute(u).Parents
	}
	return in.views().Route(u).Parents
}

// firstSeen returns when the grace period for u's current generation started.
func (in Input) firstSeen(u *unstructured.Unstructured) time.Time {
	return firstSeenBy(in.FirstSeen, u)
}

// firstSeenBy is seen(u), or u's creation time when seen is nil.
func firstSeenBy(seen func(*unstructured.Unstructured) time.Time, u *unstructured.Unstructured) time.Time {
	if seen != nil {
		return seen(u)
	}
	return u.GetCreationTimestamp().Time
}

// proxyMode returns how gw's proxy is run.
func (in Input) proxyMode(gw *unstructured.Unstructured) proxymode.Mode {
	r := in.ProxyMode
	if r == nil {
		r = proxymode.GatewayParameters{}
	}
	return r.Resolve(gw, in.Snapshot, in.Served)
}

// Check produces findings for one concern. Evaluate runs checks in
// registration order.
type Check interface {
	// Name identifies the check and its Store; it is unique among the registered checks.
	Name() string
	// Inputs names what Run reads. Reading an undeclared input is a bug.
	Inputs() InputSet
	Run(ctx RunContext) ([]Finding, CheckResult)
}

// Report is the evaluated health of one install.
type Report struct {
	// Install is the install ID, so a client can reject a response for another install.
	Install     string           `json:"install"`
	State       State            `json:"state"`
	Findings    []Finding        `json:"findings"`
	Checks      []CheckResult    `json:"checks"`
	Counts      map[Severity]int `json:"counts"`
	CollectedAt time.Time        `json:"collectedAt"`
	// Next is the earliest time after CollectedAt at which a check's output
	// may change; zero means none.
	Next time.Time `json:"-"`
}
