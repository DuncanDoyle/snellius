// Package own produces own-object findings (D-106): findings decided only by
// their resource and the objects it names, given the install context. Its
// imports are allowlisted and it holds no package state, so an analyzer can
// reach nothing else.
package own

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// ConditionTypes are an implementation's route condition types beyond
// Gateway API's (Phase 5 design §9).
type ConditionTypes struct {
	// RouteOptional are judged on a route parent when present.
	RouteOptional []string
	// RouteRejected carry a replaced or dropped rule, KGWUI-015, when False.
	RouteRejected []string
}

// InstallContext is the install context an own-object analyzer may read (D-106, D-107).
type InstallContext struct {
	Install         string                      // the install ID
	ControllerNames []string                    // status entries written by this install's controller
	Owns            func(objects.Ref) bool      // install membership (D-107)
	InScope         func(namespace string) bool // the scope, Namespace labels included (D-107)
	Version         string                      // the version rule selection uses
	Conditions      ConditionTypes              // the rule set's condition types
	Delegation      bool                        // false when the install has delegation off
	Catalog         kinds.Catalog               // served kinds (API discovery, visible to every viewer)
	PolicyKinds     []objects.GroupKind         // served policy kinds, ListOnly excluded
	Views           objects.Decoder             // decodes the object it is given, nothing else
	Now             time.Time

	// Served are the kinds the UI watches, with their categories.
	Served []kinds.Served
	// Experimental reports whether the controller watches a served kind only
	// with its experimental Gateway API features on (D-018).
	Experimental func(kinds.Served) bool
	// Collected reports whether the cache read kind gk: its watch is OK and,
	// for a namespace other than "" in a scoped snapshot, it read that
	// namespace. A kind the UI does not watch counts as read.
	Collected func(gk objects.GroupKind, namespace string) bool
	// TargetFromStatus is true when the rule set reads a policy's
	// TargetNotFound from status as KGWUI-016 (D-020 rule 5).
	TargetFromStatus bool
}

// Named resolves the objects a resource names. Every ref asked is recorded
// as the finding's basis, whether the object exists or not.
type Named interface {
	Object(ref objects.Ref) (*unstructured.Unstructured, bool)
	// FirstSeen is grace by first sight of the resource or an object Object returned.
	FirstSeen(u *unstructured.Unstructured) time.Time
}

// Finding is an own-object finding. Its fields are unexported, so only own
// builds one; pkg/health reads it through accessors.
type Finding struct {
	code, slug, severity string
	message, detail      string
	since                *time.Time
	key                  []string
	evidence             []objects.Ref
	facts                any
	parent               *objects.ParentRef
	basis                []objects.Ref
	strict               bool
	sealed               bool
}

// Basis are the named objects this finding alone rests on, a subset of what its
// analyzer named; nil when it rests on everything the analyzer named for the resource.
func (f Finding) Basis() []objects.Ref { return slices.Clone(f.basis) }

// StrictBasis reports whether each object of Basis must be readable as a population
// member and by name, as a policy page needs for an ancestor (D-131 item 3, D-133).
func (f Finding) StrictBasis() bool { return f.strict }

// Code is the finding code, such as KGWUI-002.
func (f Finding) Code() string { return f.code }

// Slug is the code's slug.
func (f Finding) Slug() string { return f.slug }

// Severity is "error", "warning" or "info".
func (f Finding) Severity() string { return f.severity }

// Message is the finding's message.
func (f Finding) Message() string { return f.message }

// Detail is the finding's detail.
func (f Finding) Detail() string { return f.detail }

// Since is when the problem started, nil when the status does not say.
func (f Finding) Since() *time.Time { return f.since }

// Key is the code's discriminator among findings on the resource; a copy.
func (f Finding) Key() []string { return slices.Clone(f.key) }

// Evidence are the objects, other than the resource, that Detail or Facts quote; a copy.
func (f Finding) Evidence() []objects.Ref { return slices.Clone(f.evidence) }

// Facts is the typed evidence, a type from evidence.go, or nil.
func (f Finding) Facts() any { return f.facts }

// Parent is the route parentRef a KGWUI-003 is about, nil for other findings.
func (f Finding) Parent() *objects.ParentRef { return f.parent }

// Sealed is true only for a Finding built inside own.
func (f Finding) Sealed() bool { return f.sealed }

// Result is what one evaluation returns.
type Result struct {
	findings []Finding
	wakeAt   time.Time
}

// Findings are the evaluation's findings.
func (r Result) Findings() []Finding { return r.findings }

// WakeAt is the earliest time the findings may change; zero for none.
func (r Result) WakeAt() time.Time { return r.wakeAt }

// Kind selects an analyzer. The set is closed: only these constants exist.
type Kind uint8

// The analyzers.
const (
	Status  Kind = iota + 1 // 002 from own status
	Parents                 // 003 on a route
	Refs                    // 013, 014 on a route
	Rules                   // 015 on a route
)

// Evaluate dispatches k to an unexported analyzer. It and EvaluateRoute are the only entry points.
func Evaluate(k Kind, ic InstallContext, resource *unstructured.Unstructured, named Named) Result {
	return evaluate(k, ic, resource, named, &walk{}, &out{})
}

// EvaluateRoute returns, for each ks[i], what Evaluate(ks[i], ic, resource, named[i])
// returns, from one walk of the route's parents; nil when the lengths differ.
func EvaluateRoute(ks []Kind, ic InstallContext, resource *unstructured.Unstructured, named []Named) []Result {
	if len(named) != len(ks) {
		return nil
	}
	w := &walk{}
	rs, outs := make([]Result, len(ks)), make([]out, len(ks))
	for i, k := range ks {
		rs[i] = evaluate(k, ic, resource, named[i], w, &outs[i])
	}
	return rs
}

// evaluate runs analyzer k into the empty o; w is shared by the analyzers of one resource.
func evaluate(k Kind, ic InstallContext, resource *unstructured.Unstructured, named Named, w *walk, o *out) Result {
	o.now = ic.Now
	switch k {
	case Status:
		status(ic, resource, named, o, w)
	case Parents:
		parents(ic, resource, named, o, w)
	case Refs:
		refs(ic, resource, named, o, w)
	case Rules:
		rules(ic, resource, named, o, w)
	default:
		return Result{}
	}
	return Result{findings: o.fs, wakeAt: o.wake}
}

// out collects one evaluation's findings and wake time.
type out struct {
	now  time.Time
	fs   []Finding
	wake time.Time
}

// add seals f and keeps it.
func (o *out) add(f Finding) {
	f.sealed = true
	o.fs = append(o.fs, f)
}

// wakeAt keeps at when it is after Now and before the current wake time.
func (o *out) wakeAt(at time.Time) {
	if at.After(o.now) && (o.wake.IsZero() || at.Before(o.wake)) {
		o.wake = at
	}
}

// Codes, slugs and severities the analyzers emit.
const (
	CodeConditionFalse  = "KGWUI-002"
	CodeRouteUnattached = "KGWUI-003"
	CodeRefUnresolved   = "KGWUI-013"
	CodeRefNotPermitted = "KGWUI-014"
	CodeRuleReplaced    = "KGWUI-015"

	severityError   = "error"
	severityWarning = "warning"
	severityInfo    = "info"
)

// Grace is how long a resource may lack current status before it is reported
// as not yet processed.
const Grace = 60 * time.Second

// Discriminator owners: self, a parent, a listener, an ancestor, or a parent
// found only in status, which pkg/health replaces by its rank.
const (
	OwnerSelf         = "self"
	OwnerParent       = "parent"
	OwnerListener     = "listener"
	OwnerAncestor     = "ancestor"
	OwnerStatusParent = "status-parent"
)

// NoControllerStatus is the detail of a block the controller never wrote.
const NoControllerStatus = "no status from the controller"

// KgatewayProgrammed is kgateway's route condition type.
const KgatewayProgrammed = "kgateway.dev/Programmed"

// GatewayRequired are the required condition types of a Gateway or
// ListenerSet block (design §5.2); optional types are judged only when present.
func GatewayRequired() []string { return []string{"Accepted", "Programmed"} }

// ListenerRequired are a listener block's required condition types.
func ListenerRequired() []string { return []string{"Accepted", "Programmed"} }

// ListenerOptional are a listener block's optional condition types.
func ListenerOptional() []string { return []string{"ResolvedRefs"} }

// RouteRequired are a route parent block's required condition types.
func RouteRequired() []string { r := routeRequired(); return r[:] }

// routeRequired is RouteRequired as an array, which a hot loop keeps on the stack.
func routeRequired() [2]string { return [2]string{"Accepted", "ResolvedRefs"} }

// PolicyRequired are a policy ancestor block's required condition types.
func PolicyRequired() []string { return []string{"Accepted"} }

// PolicyOptional are a policy ancestor block's optional condition types.
func PolicyOptional() []string { return []string{"Attached"} }

// MaxStatusBytes bounds a status message quoted in a detail or evidence (design §18.2).
const MaxStatusBytes = 1024

// truncatedMarker ends a clipped display string.
const truncatedMarker = "…[truncated]"

// clipDisplay cuts p to limit bytes at a UTF-8 boundary and adds a marker.
func clipDisplay(p string, limit int) string {
	if len(p) <= limit {
		return p
	}
	n := limit
	for n > 0 && !utf8.RuneStart(p[n]) {
		n--
	}
	return strings.ToValidUTF8(p[:n], "�") + truncatedMarker
}

// reasonMessage turns a condition reason into operator language.
func reasonMessage(reason string) (string, bool) {
	switch reason {
	case "BackendNotFound":
		return "Backend not found", true
	case "RefNotPermitted":
		return "Reference not permitted (missing ReferenceGrant)", true
	case "InvalidKind":
		return "Unsupported backend kind", true
	case "NoMatchingParent":
		return "No matching listener on the parent", true
	case "NotAllowedByListeners":
		return "Not allowed by the Gateway's listeners", true
	case "NoMatchingListenerHostname":
		return "No listener hostname matches", true
	case "UnsupportedValue":
		return "Unsupported value in the route", true
	case "RouteRuleDropped":
		return "Rule dropped because of invalid configuration", true
	case "RouteRuleReplaced":
		return "Rule replaced by a direct response", true
	case "Invalid":
		return "Invalid configuration", true
	case "TargetNotFound":
		return "Policy target not found", true
	case "PartiallyValid":
		return "Partially valid; some settings were ignored", true
	case "Overridden":
		return "Overridden by another policy", true
	case "AddressNotAssigned":
		return "No address assigned", true
	case "InvalidParameters":
		return "Invalid GatewayClass parameters", true
	case "PortUnavailable":
		return "Listener port unavailable", true
	case "HostnameConflict":
		return "Listener hostname conflicts with another listener", true
	case "ProtocolConflict":
		return "Listener protocol conflicts with another listener", true
	case "InvalidCertificateRef":
		return "Invalid certificate reference", true
	}
	return "", false
}

// ConditionMessage returns operator language for c, falling back to "<Type> is <Status> (<Reason>)".
func ConditionMessage(c metav1.Condition) string {
	if m, ok := reasonMessage(c.Reason); ok {
		return m
	}
	return fmt.Sprintf("%s is %s (%s)", c.Type, c.Status, c.Reason)
}

// ConditionDetail returns the raw condition for the secondary detail field.
func ConditionDetail(c metav1.Condition, extra string) string {
	d := fmt.Sprintf("%s=%s, reason %s", c.Type, c.Status, c.Reason)
	if c.Message != "" {
		d += ": " + clipDisplay(c.Message, MaxStatusBytes)
	}
	if extra != "" {
		d += " (" + extra + ")"
	}
	return d
}

// since is c's lastTransitionTime, nil when unset.
func since(c metav1.Condition) *time.Time {
	if c.LastTransitionTime.IsZero() {
		return nil
	}
	t := c.LastTransitionTime.Time
	return &t
}

// within formats extra as a trailing " (extra)", or "" when empty.
func within(extra string) string {
	if extra == "" {
		return ""
	}
	return " (" + extra + ")"
}

// BlockFreshness returns why a status block of u is not current, its kind
// and, for a stale block, the lowest observed generation; "" when it is
// current. Kinds are tried in §10.1 order (D-006).
func BlockFreshness(u *unstructured.Unstructured, cs []metav1.Condition, required, optional []string) (why, kind string, observed int64) {
	return blockFreshness(u, cs, required, optional, true)
}

// PolicyFreshness is BlockFreshness for one status.ancestors entry of policy u.
// Accepted alone decides (D-135): kgateway keeps Attached=False/Pending beside a
// definitive Accepted, and an older Attached does not make the block stale.
func PolicyFreshness(u *unstructured.Unstructured, cs []metav1.Condition) (why, kind string, observed int64) {
	return blockFreshness(u, cs, PolicyRequired(), PolicyOptional(), false)
}

// blockFreshness is BlockFreshness; pendingOptional makes an optional condition judged too, stale or Pending.
func blockFreshness(u *unstructured.Unstructured, cs []metav1.Condition, required, optional []string, pendingOptional bool) (why, kind string, observed int64) {
	if crdDefaultOnly(cs) {
		return NoControllerStatus, FreshnessNeverWritten, 0
	}
	types := [2][]string{required, optional}
	judged := types[:1]
	if pendingOptional {
		judged = types[:]
	}
	stale, gen := false, u.GetGeneration() // objects.IsStale's comparison, with the generation read once
	for _, ts := range judged {
		for _, t := range ts {
			if c := objects.FindCondition(cs, t); c != nil && c.ObservedGeneration < gen && (!stale || c.ObservedGeneration < observed) {
				stale, observed = true, c.ObservedGeneration
			}
		}
	}
	if stale {
		return "Status describes an older generation", FreshnessStale, observed
	}
	for _, t := range required {
		if objects.FindCondition(cs, t) == nil {
			return fmt.Sprintf("No %s condition", t), FreshnessPending, 0
		}
	}
	for _, ts := range judged {
		for _, t := range ts {
			if c := objects.FindCondition(cs, t); c != nil && c.Reason == "Pending" {
				return t + " is Pending", FreshnessPending, 0
			}
		}
	}
	for _, t := range required {
		if objects.FindCondition(cs, t).Status == metav1.ConditionUnknown {
			return t + " is Unknown", FreshnessPending, 0
		}
	}
	return "", "", 0
}

// crdDefaultOnly reports whether every condition of a non-empty block is the
// Gateway API CRD default: reason Pending, "Waiting for controller", at the Unix epoch.
func crdDefaultOnly(cs []metav1.Condition) bool {
	return len(cs) > 0 && !slices.ContainsFunc(cs, func(c metav1.Condition) bool {
		return c.Reason != "Pending" || c.Message != "Waiting for controller" || !c.LastTransitionTime.Time.Equal(time.Unix(0, 0))
	})
}

// RefKey is r's group, kind, namespace and name.
func RefKey(r objects.Ref) []string { return []string{r.Group, r.Kind, r.Namespace, r.Name} }

// appendWrittenKey appends a parentRef as written to k, keyed as computed-rules §7 keys
// a report, plus the written port, so refs differing only by port stay apart.
func appendWrittenKey(k []string, w objects.WrittenRef) []string {
	port := ""
	if w.PortSet {
		port = strconv.Itoa(int(w.Port))
	}
	return append(k, w.Group, w.Kind, w.Namespace, w.Name, w.SectionName, port)
}

// routeView is the part of a route's view the analyzers read.
type routeView struct {
	spec    []objects.ParentRef
	written []objects.WrittenRef
	status  []objects.RouteParentStatus
	http    bool
}

// viewOf decodes route u through views.
func viewOf(views objects.Decoder, u *unstructured.Unstructured) routeView {
	return viewAs(views, u, u.GroupVersionKind())
}

// viewAs decodes route u, of kind gvk, through views.
func viewAs(views objects.Decoder, u *unstructured.Unstructured, gvk schema.GroupVersionKind) routeView {
	if gvk.Group == objects.GatewayAPIGroup && gvk.Kind == "HTTPRoute" {
		v := views.HTTPRoute(u)
		return routeView{v.ParentRefs, v.WrittenRefs, v.Parents, true}
	}
	v := views.Route(u)
	return routeView{v.ParentRefs, v.WrittenRefs, v.Parents, false}
}

// ParentKey is the discriminator owner of route u's parentRef p: p as written
// in u's spec, else a status-only owner from u's status entry for p, else p itself.
func ParentKey(views objects.Decoder, u *unstructured.Unstructured, p objects.ParentRef) []string {
	return parentKey(viewOf(views, u), p)
}

func parentKey(v routeView, p objects.ParentRef) []string {
	if i := slices.Index(v.spec, p); i >= 0 && i < len(v.written) {
		return appendWrittenKey([]string{OwnerParent}, v.written[i])
	}
	w := objects.WrittenRef{Group: p.Group, Kind: p.Kind, Namespace: p.Namespace, Name: p.Name, SectionName: p.SectionName, Port: p.Port, PortSet: p.Port != 0}
	if j := slices.IndexFunc(v.status, func(s objects.RouteParentStatus) bool { return s.ParentRef == p }); j >= 0 {
		w = v.status[j].Written
	}
	return appendWrittenKey([]string{OwnerStatusParent}, w)
}

// ParentDesc describes p as "Kind ns/name", plus its section and port when set.
func ParentDesc(p objects.ParentRef) string {
	d := p.Kind + " " + p.Namespace + "/" + p.Name
	if p.SectionName != "" {
		d += " section " + p.SectionName
	}
	if p.Port != 0 {
		d += fmt.Sprintf(" port %d", p.Port)
	}
	return d
}

// served returns u's served kind.
func served(ic InstallContext, group, kind string) (kinds.Served, bool) {
	for _, k := range ic.Served {
		if k.Group == group && k.Kind == kind {
			return k, true
		}
	}
	return kinds.Served{}, false
}

// inCategory reports whether group/kind is a served kind of one of cs.
func inCategory(ic InstallContext, group, kind string, cs ...kinds.Category) bool {
	k, ok := served(ic, group, kind)
	return ok && slices.Contains(cs, k.Category)
}

// isHTTPRoute reports whether group/kind is HTTPRoute.
func isHTTPRoute(group, kind string) bool {
	return group == objects.GatewayAPIGroup && kind == "HTTPRoute"
}

// experimental returns gvk's served kind and whether the controller watches
// it only with its experimental Gateway API features on.
func experimental(ic InstallContext, gvk schema.GroupVersionKind) (kinds.Served, bool) {
	k, ok := served(ic, gvk.Group, gvk.Kind)
	return k, ok && ic.Experimental != nil && ic.Experimental(k)
}

// experimentalHint is ExperimentalHint for served kind k, or "" for a stable kind.
func experimentalHint(k kinds.Served, exp bool) string {
	if !exp {
		return ""
	}
	return fmt.Sprintf(ExperimentalHint, k.Kind+" "+k.Version)
}

// ExperimentalHint is appended to the detail of a "not processed" finding on
// an experimental-only kind (D-018 §2); %s is the kind and served version.
const ExperimentalHint = "; the controller watches %s only when its experimental Gateway API features are on, and they may be off here"

// ignored reports whether a route with view v is left unjudged: its kind is
// experimental-only (exp) and only other controllers wrote its status (D-018 §1).
func ignored(ic InstallContext, v routeView, exp bool) bool {
	if !exp {
		return false
	}
	for _, p := range v.status {
		if slices.Contains(ic.ControllerNames, p.ControllerName) {
			return false
		}
	}
	return len(v.status) > 0
}

// eachRouteParent calls fn for route u's parentRefs of this install, as
// install.Assign decides them: spec parentRefs naming a Gateway or ListenerSet
// it owns, then the status parents that are HTTPRoutes it owns (design §4.2).
func eachRouteParent(ic InstallContext, u *unstructured.Unstructured, v routeView, fn func(p objects.ParentRef)) {
	for _, p := range v.spec {
		if inCategory(ic, p.Group, p.Kind, kinds.CategoryGateway, kinds.CategoryListenerSet) && ic.Owns(p.Ref()) {
			fn(p)
		}
	}
	if !v.http || !ic.Delegation {
		return
	}
	delegated := func(e objects.RouteParentStatus) bool {
		return isHTTPRoute(e.ParentRef.Group, e.ParentRef.Kind) && slices.Contains(ic.ControllerNames, e.ControllerName) &&
			(len(v.spec) == 0 || slices.Contains(v.spec, e.ParentRef)) && ic.Owns(e.ParentRef.Ref())
	}
	for i, e := range v.status {
		if !delegated(e) || !ic.InScope(u.GetNamespace()) {
			continue
		}
		// Each reported parentRef counts once.
		if !slices.ContainsFunc(v.status[:i], func(d objects.RouteParentStatus) bool { return d.ParentRef == e.ParentRef && delegated(d) }) {
			fn(e.ParentRef)
		}
	}
}

// statusFor is route u's status entry for parent p from this install's controller, or nil.
func statusFor(ic InstallContext, v routeView, p objects.ParentRef) *objects.RouteParentStatus {
	for i := range v.status {
		if v.status[i].ParentRef == p && slices.Contains(ic.ControllerNames, v.status[i].ControllerName) {
			return &v.status[i]
		}
	}
	return nil
}

// walk is what the route analyzers of one resource share: its view and its
// parents of this install with their status blocks. It is built from ic and u
// alone, on first use, and reads nothing through Named.
type walk struct {
	built   bool
	v       routeView
	ignored bool
	kind    kinds.Served // the route's served kind
	exp     bool         // its kind is experimental-only
	blocks  []block
}

// block is one parent of the walk with this install's status block for it.
type block struct {
	p  objects.ParentRef
	st *objects.RouteParentStatus // nil when the controller wrote none
	// why, kind and observed are the block's BlockFreshness; why is "" when current.
	why, kind string
	observed  int64
	// falses are a current block's False conditions, in type order.
	falses []*metav1.Condition
	extra  string   // "parent " and ParentDesc(p), built on first use
	owner  []string // parentKey(p), built on first use; analyzers copy it
}

// of returns w, built for route u on the first call.
func (w *walk) of(ic InstallContext, u *unstructured.Unstructured) *walk {
	if w.built {
		return w
	}
	gvk := u.GroupVersionKind()
	w.kind, w.exp = experimental(ic, gvk)
	w.built, w.v = true, viewAs(ic.Views, u, gvk)
	if w.ignored = ignored(ic, w.v, w.exp); w.ignored {
		return w
	}
	rr := routeRequired()
	required, optional := rr[:], ic.Conditions.RouteOptional
	eachRouteParent(ic, u, w.v, func(p objects.ParentRef) {
		b := block{p: p, st: statusFor(ic, w.v, p)}
		if b.st != nil {
			b.why, b.kind, b.observed = BlockFreshness(u, b.st.Conditions, required, optional)
		}
		if b.st != nil && b.why == "" {
			for _, ts := range [2][]string{required, optional} {
				for _, t := range ts {
					if cond := objects.FindCondition(b.st.Conditions, t); cond != nil && cond.Status == metav1.ConditionFalse {
						b.falses = append(b.falses, cond)
					}
				}
			}
		}
		w.blocks = append(w.blocks, b)
	})
	return w
}

// desc is "parent " and the block's ParentDesc.
func (b *block) desc() string {
	if b.extra == "" {
		b.extra = "parent " + ParentDesc(b.p)
	}
	return b.extra
}

// key is the block's parentKey; callers copy it before they change it.
func (b *block) key(v routeView) []string {
	if b.owner == nil {
		b.owner = parentKey(v, b.p)
	}
	return b.owner
}

// eachCurrentBlock calls fn for each False condition of each current status
// block of the walk's route, in parent and type order; owner is the parent's key.
func (w *walk) eachCurrentBlock(fn func(p objects.ParentRef, cond metav1.Condition, extra string, owner []string)) {
	for i := range w.blocks {
		b := &w.blocks[i]
		for _, cond := range b.falses {
			fn(b.p, *cond, b.desc(), b.key(w.v))
		}
	}
}

// RefRuleFor returns the code and slug for cond on a route parent or a
// listener; false leaves the condition to KGWUI-002 (D-030).
func RefRuleFor(cond metav1.Condition, listener bool) (code, slug string, ok bool) {
	if cond.Type != "ResolvedRefs" || cond.Status != metav1.ConditionFalse {
		return "", "", false
	}
	switch {
	case cond.Reason == "RefNotPermitted":
		return CodeRefNotPermitted, "ref-not-permitted", true
	case !listener, cond.Reason == "InvalidCertificateRef":
		return CodeRefUnresolved, "ref-unresolved", true
	}
	return "", "", false
}

// ruleReplacedReason reports whether cond is a current RouteRuleReplaced or
// RouteRuleDropped on one of rejected's types: kgateway's are Accepted on 2.3
// and kgateway.dev/Programmed on 2.4.
func ruleReplacedReason(cond metav1.Condition, rejected []string) bool {
	return cond.Status == metav1.ConditionFalse && slices.Contains(rejected, cond.Type) &&
		(cond.Reason == "RouteRuleReplaced" || cond.Reason == "RouteRuleDropped")
}

// asMap and asList read an unstructured value as a map or a list, nil otherwise.
func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }

func asList(v any) []any { l, _ := v.([]any); return l }
