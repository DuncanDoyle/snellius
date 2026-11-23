package api

import (
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/health/own"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/policy"
)

// WithPolicyInterpreters gives each install's pack policy interpreter (Phase 8
// design §8). Without it, or when it returns nil, features are raw spec keys
// and nothing is interpreted.
func WithPolicyInterpreters(f func(install.Install) policy.Interpreter) HandlerOption {
	return func(c *handlerConfig) { c.interpFor = f }
}

// installPolicies are install i's policies, list-only kinds included, sorted
// by namespace, name, group and kind.
func installPolicies(i *engine.InstallModel) []*unstructured.Unstructured {
	o := i.Owned
	if o == nil {
		return nil
	}
	var out []*unstructured.Unstructured
	for _, p := range o.Policies {
		out = append(out, p.Obj)
	}
	for _, p := range o.Listed {
		if p.Kind.Category == kinds.CategoryPolicy {
			out = append(out, p.Obj)
		}
	}
	slices.SortFunc(out, func(a, b *unstructured.Unstructured) int { return compareAffected(objects.RefOf(a), objects.RefOf(b)) })
	return out
}

// policyGuard is install i's ancestor guard: the guards the policy ancestor
// findings apply (own.policyAncestors), over i's owned Gateways and ListenerSets.
func policyGuard(m *engine.Model, i *engine.InstallModel) policy.Guard {
	owned := map[objects.Ref]bool{}
	if o := i.Owned; o != nil {
		for _, u := range slices.Concat(o.Gateways, o.ListenerSets) {
			owned[objects.RefOf(u)] = true
		}
	}
	ic := own.InstallContext{Served: m.Served, Owns: func(r objects.Ref) bool { return owned[r] }}
	return policy.Guard{
		ControllerNames: i.Resolved.ControllerNames,
		Owns:            func(ar objects.Ref) bool { return own.AncestorOwned(ic, ar) },
		Fresh: func(u *unstructured.Unstructured, cs []metav1.Condition) string {
			why, _, _ := own.BlockFreshness(u, cs, own.PolicyRequired(), own.PolicyOptional())
			return why
		},
	}
}

// policyLookup is the target checks' Lookup for the caller of v (design §7 item 4): Object
// reads the whole collection, so a hidden target is never "not found"; access is CanGet and CanList.
type policyLookup struct {
	m *engine.Model
	v *view
}

func (l policyLookup) Object(ref objects.Ref) (*unstructured.Unstructured, bool) {
	u, _ := stored(l.m, ref)
	return u, u != nil
}

func (l policyLookup) Watched(gk objects.GroupKind, ns string) bool {
	served := slices.ContainsFunc(l.m.Served, func(k kinds.Served) bool { return k.Group == gk.Group && k.Kind == gk.Kind })
	return served && health.Collected(l.m.Snapshot, l.m.Served, gk, ns)
}

func (l policyLookup) CanGet(ref objects.Ref) bool { return l.v.canNamed(ref) }

func (l policyLookup) CanList(gk objects.GroupKind, ns string) bool { return l.v.canList(gk, ns) }

// policyEntry is one readable policy as the caller sees it.
type policyEntry struct {
	u      *unstructured.Unstructured
	p      policy.Policy
	checks []policy.TargetCheck
}

// policyReader reads install i's policies for the caller of v.
type policyReader struct {
	in     policy.Inputs
	interp policy.Interpreter
	guard  policy.Guard
	served []kinds.Served
}

func (h *handler) policyReader(p *engine.Published, i *engine.InstallModel, v *view) policyReader {
	var interp policy.Interpreter
	if h.interpFor != nil {
		interp = h.interpFor(i.Resolved.Install)
	}
	return policyReader{interp: interp, guard: policyGuard(p.Model, i), served: p.Model.Served, in: policy.Inputs{Lookup: policyLookup{m: p.Model, v: v},
		Targeter: i.Rules.PolicyTargeter(), ScopeKnown: i.Resolved.Scope.Mode != install.ScopeUnknown}}
}

// read is u as the caller sees it. An ancestor is never current, and carries none
// of the install's judgments about it, unless the caller may list and get every object
// its ownership rests on: the ancestor, and a ListenerSet's parent Gateway (design §7 item 3).
func (pr policyReader) read(u *unstructured.Unstructured) policyEntry {
	p := policy.Read(u, pr.interp, pr.guard)
	for k := range p.Ancestors {
		// The summary ancestor names no object; its guards read only the policy.
		if a := &p.Ancestors[k]; !a.Summary && !pr.ownershipReadable(a.Ref.Ref()) {
			a.Current, a.NotCurrent, a.Why = false, policy.NotCurrentUnreadable, ""
		}
	}
	return policyEntry{u: u, p: p, checks: policy.CheckTargets(p, pr.in)}
}

// ownershipReadable reports whether ancestor ar is readable and, when ar is a collected
// ListenerSet, so is the Gateway its ownership comes from (D-107).
func (pr policyReader) ownershipReadable(ar objects.Ref) bool {
	l := pr.in.Lookup
	if !readable(l, ar) {
		return false
	}
	if !slices.ContainsFunc(pr.served, func(k kinds.Served) bool {
		return k.Category == kinds.CategoryListenerSet && k.Group == ar.Group && k.Kind == ar.Kind
	}) {
		return true
	}
	ls, ok := l.Object(ar)
	return !ok || readable(l, objects.ListenerSetParent(ls).Ref())
}

// readable reports whether the caller may list ref's kind in its namespace and then get
// ref by name. List first keeps a hidden namespace to one check, not one per name.
func readable(l policy.Lookup, ref objects.Ref) bool {
	return l.CanList(objects.GroupKind{Group: ref.Group, Kind: ref.Kind}, ref.Namespace) && l.CanGet(ref)
}

// problems reports whether e has problems (design §6.2).
func (e *policyEntry) problems() bool {
	for _, a := range e.p.Ancestors {
		if !a.Current {
			continue
		}
		if slices.ContainsFunc(a.Conditions, func(c metav1.Condition) bool { return c.Status == metav1.ConditionFalse }) ||
			slices.ContainsFunc(a.Readings, func(r policy.Reading) bool { return r.Warning }) {
			return true
		}
	}
	return slices.ContainsFunc(e.checks, func(c policy.TargetCheck) bool {
		return c.Outcome == computed.TargetNotFound || c.Outcome == computed.TargetSectionNotFound
	})
}

// accepted is the worst Accepted over e's current ancestors, False before True; nil for none.
func (e *policyEntry) accepted() *PolicyAcceptedDTO {
	var worst *metav1.Condition
	for _, a := range e.p.Ancestors {
		if !a.Current {
			continue
		}
		if c := objects.FindCondition(a.Conditions, "Accepted"); c != nil && (worst == nil || worst.Status == metav1.ConditionTrue && c.Status != metav1.ConditionTrue) {
			worst = c
		}
	}
	if worst == nil {
		return nil
	}
	return &PolicyAcceptedDTO{Status: worst.Status, Reason: worst.Reason, Source: policy.SourceInferred}
}

// attachedOn are e's current ancestors with Attached=True, any reason.
func (e *policyEntry) attachedOn() []PolicyAttachedDTO {
	out := []PolicyAttachedDTO{}
	for _, a := range e.p.Ancestors {
		if c := objects.FindCondition(a.Conditions, "Attached"); a.Current && c != nil && c.Status == metav1.ConditionTrue {
			out = append(out, PolicyAttachedDTO{ParentRef: a.Ref, Source: policy.SourceControllerReported})
		}
	}
	return out
}

// features are e's spec sections; values only when withValues.
func (e *policyEntry) features(withValues bool) []PolicyFeatureDTO {
	out := make([]PolicyFeatureDTO, 0, len(e.p.Features))
	for _, f := range e.p.Features {
		d := PolicyFeatureDTO{Key: f.Key, Label: f.Label, Source: f.Source}
		if withValues {
			d.Value = f.Value
		}
		out = append(out, d)
	}
	return out
}

// linkHealth links ref, read by name, with its health from vw; plain, with
// no health, when the caller may not get it or it is not admitted (design §7 item 3).
type linkHealth func(ref objects.Ref) (Link, health.State)

// healthSource is the source of a linked object's health: inferred, or none without it.
func healthSource(s health.State) policy.Source {
	if s == "" {
		return ""
	}
	return policy.SourceInferred
}

// targets are e's targets with their checks; detail links them through lh.
func (e *policyEntry) targets(lh linkHealth) []PolicyTargetDTO {
	out := make([]PolicyTargetDTO, 0, len(e.p.Targets))
	for _, t := range e.p.Targets {
		d := PolicyTargetDTO{Index: t.Index, Group: t.Group, Kind: t.Kind, Name: t.Name, SectionName: t.SectionName, Selector: t.Selector,
			MatchLabels: t.MatchLabels, Source: t.Source}
		if !t.Selector {
			if k := slices.IndexFunc(e.checks, func(c policy.TargetCheck) bool { return c.Index == t.Index }); k >= 0 {
				d.Check = &PolicyCheckDTO{Outcome: e.checks[k].Outcome, Source: e.checks[k].Source}
			}
			if lh != nil {
				ref := t.Ref(e.p.Ref.Namespace)
				lr := LinkedRef{Ref: ref}
				lr.Link, d.Health = lh(ref)
				d.Ref, d.HealthSource = &lr, healthSource(d.Health)
			}
		}
		out = append(out, d)
	}
	return out
}

// policyHealth links refs for the caller of v and labels each linked one with its state for vw.
func policyHealth(l linker, ix *explore.Index, vw explore.Viewer) linkHealth {
	return func(ref objects.Ref) (Link, health.State) {
		k := l.linkNamed(ref)
		if k == LinkNone {
			return k, ""
		}
		return k, refState(ix, vw, ref)
	}
}

// refState is ref's row state for vw: a route's or ListenerSet's own rule, else StateOf.
func refState(ix *explore.Index, vw explore.Viewer, ref objects.Ref) health.State {
	if id, ok := ix.Row(ref); ok {
		st, _, _ := ix.RouteStateOf(id, vw)
		return st
	}
	if k := slices.IndexFunc(ix.ListenerSets, func(s explore.ListenerSetRow) bool { return s.Ref == ref }); k >= 0 {
		return ix.ListenerSetStateOf(explore.RowID(k), vw) // #nosec G115 -- a row count fits in int32
	}
	return ix.StateOf(ref, vw)
}

// Policies list parameters; single ones are allowed once.
var (
	policiesParams = []string{"kind", "feature", "namespace", "hasProblems", "limit", "cursor"}
	policiesSingle = []string{"hasProblems", "limit", "cursor"}
	featureFormat  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,62}$`)
)

// sortPolicies keeps policies cursors apart from every other list's.
const sortPolicies = "policies"

// policiesQuery is a parsed policies list query.
type policiesQuery struct {
	kinds      []objects.GroupKind
	features   []string
	namespaces []string
	// problems filters on has problems when set.
	problems *bool
	limit    int
	cursor   string
}

// parsePoliciesQuery parses r's query for GET …/policies. Unknown and repeated
// single-valued parameters and malformed values are a 400.
func parsePoliciesQuery(r *http.Request) (policiesQuery, error) {
	q := policiesQuery{limit: explore.DefaultLimit}
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return q, badParam("malformed query: %v", err)
	}
	for _, name := range slices.Sorted(maps.Keys(vals)) {
		if !slices.Contains(policiesParams, name) {
			return q, badParam("unknown parameter %q", name)
		}
		if len(vals[name]) > 1 && slices.Contains(policiesSingle, name) {
			return q, badParam("parameter %q given more than once", name)
		}
	}
	for _, v := range vals["kind"] {
		s, err := segments(v, 2, "kind")
		if err != nil {
			return q, err
		}
		q.kinds = append(q.kinds, objects.GroupKind{Group: s[0], Kind: s[1]})
	}
	for _, v := range vals["feature"] {
		if !featureFormat.MatchString(v) {
			return q, badParam("feature %q: want a spec key", v)
		}
		q.features = append(q.features, v)
	}
	for _, v := range vals["namespace"] {
		if len(validation.IsDNS1123Label(v)) > 0 {
			return q, badParam("namespace %q: want a namespace name", v)
		}
		q.namespaces = append(q.namespaces, v)
	}
	if vals.Has("hasProblems") {
		b, err := strconv.ParseBool(vals.Get("hasProblems"))
		if err != nil {
			return q, badParam("hasProblems %q: want true or false", vals.Get("hasProblems"))
		}
		q.problems = &b
	}
	if v := vals.Get("limit"); vals.Has("limit") {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > explore.MaxLimit {
			return q, badParam("limit %q: want 1–%d", v, explore.MaxLimit)
		}
		q.limit = n
	}
	q.cursor = vals.Get("cursor")
	return q, nil
}

// policyFilter is which of q's filters a policy passes.
type policyFilter struct{ kind, feature, namespace, problems bool }

// all reports whether every filter but skip passes.
func (f policyFilter) all(skip string) bool {
	return (f.kind || skip == "kind") && (f.feature || skip == "feature") && (f.namespace || skip == "namespace") && (f.problems || skip == "problems")
}

// filter is which of q's filters e passes, given whether it has problems.
func (q *policiesQuery) filter(e *policyEntry, problems bool) policyFilter {
	gk := objects.GroupKind{Group: e.p.Ref.Group, Kind: e.p.Ref.Kind}
	return policyFilter{
		kind: len(q.kinds) == 0 || slices.Contains(q.kinds, gk),
		feature: len(q.features) == 0 || slices.ContainsFunc(e.p.Features, func(f policy.Feature) bool {
			return slices.Contains(q.features, f.Key)
		}),
		namespace: len(q.namespaces) == 0 || slices.Contains(q.namespaces, e.p.Ref.Namespace),
		problems:  q.problems == nil || *q.problems == problems,
	}
}

// policies is GET …/policies (Phase 8 design §6.2): the install's policies
// the caller may view as members, with totals and facets over them only.
func (h *handler) policies(w http.ResponseWriter, r *http.Request) {
	p, i := h.find(w, r)
	if i == nil {
		return
	}
	q, err := parsePoliciesQuery(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	v := h.view(r)
	all := installPolicies(i)
	v.prefetch(func(add func(objects.Ref)) {
		for _, u := range all {
			add(objects.RefOf(u))
		}
	})
	pr := h.policyReader(p, i, v)
	kindsN, featuresN, nsN := map[objects.GroupKind]int{}, map[string]int{}, map[string]int{}
	facets := PolicyFacetsDTO{Kinds: []PolicyKindFacetDTO{}, Features: []PolicyFeatureFacetDTO{}, Namespaces: []PolicyNamespaceFacetDTO{}}
	var entries []policyEntry
	var refs []objects.Ref
	problems := map[objects.Ref]bool{}
	for _, u := range all {
		if !v.can(objects.RefOf(u)) {
			continue
		}
		e := pr.read(u)
		has := e.problems()
		f := q.filter(&e, has)
		if f.all("kind") {
			kindsN[objects.GroupKind{Group: e.p.Ref.Group, Kind: e.p.Ref.Kind}]++
		}
		if f.all("feature") {
			for _, ft := range e.p.Features {
				featuresN[ft.Key]++
			}
		}
		if f.all("namespace") {
			nsN[e.p.Ref.Namespace]++
		}
		if f.all("problems") && has {
			facets.HasProblems++
		}
		if f.all("") {
			entries, refs, problems[e.p.Ref] = append(entries, e), append(refs, e.p.Ref), has
		}
	}
	for _, gk := range slices.SortedFunc(maps.Keys(kindsN), func(a, b objects.GroupKind) int {
		return compareAffected(objects.Ref{Group: a.Group, Kind: a.Kind}, objects.Ref{Group: b.Group, Kind: b.Kind})
	}) {
		facets.Kinds = append(facets.Kinds, PolicyKindFacetDTO{Group: gk.Group, Kind: gk.Kind, Policies: kindsN[gk]})
	}
	for _, k := range slices.Sorted(maps.Keys(featuresN)) {
		facets.Features = append(facets.Features, PolicyFeatureFacetDTO{Key: k, Policies: featuresN[k]})
	}
	for _, ns := range slices.Sorted(maps.Keys(nsN)) {
		facets.Namespaces = append(facets.Namespaces, PolicyNamespaceFacetDTO{Namespace: ns, Policies: nsN[ns]})
	}
	pg, err := pageRefs(refs, q.cursor, q.limit, sortPolicies)
	if err != nil {
		fail(w, err)
		return
	}
	l := h.linker(v, p, i, nil, nil)
	items := make([]PolicyRow, 0, len(pg.refs))
	start := 0
	if len(pg.refs) > 0 {
		start = slices.Index(refs, pg.refs[0])
	}
	for k := range pg.refs {
		e := &entries[start+k]
		items = append(items, PolicyRow{Ref: l.linked(e.p.Ref), Targets: e.targets(nil), Accepted: e.accepted(), AttachedOn: e.attachedOn(),
			Features: e.features(false), Problems: PolicyProblemsDTO{Has: problems[e.p.Ref], Source: policy.SourceInferred}})
	}
	denied := v.deniedInInstall(len(all), func(id explore.RowID) objects.Ref { return objects.RefOf(all[id]) })
	writeJSON(w, PoliciesResponse{Page: Page[PolicyRow]{Install: i.Resolved.ID(), Items: items, Total: len(refs), Next: pg.next, Prev: pg.prev,
		DeniedInInstall: denied, CollectedAt: wireTime(p.ConfirmedAt)}, Facets: facets})
}

// policyDetail is GET …/policies/{group}/{kind}/{ns}/{name} (design §6.3), authorised by name
// before anything is read: a hidden, absent or other install's policy is the same 404.
func (h *handler) policyDetail(w http.ResponseWriter, r *http.Request) {
	p, i := h.find(w, r)
	if i == nil {
		return
	}
	group := r.PathValue("group")
	if group == "-" {
		group = ""
	}
	ref := objects.Ref{Group: group, Kind: r.PathValue("kind"), Namespace: r.PathValue("ns"), Name: r.PathValue("name")}
	noteSubject(r, ref)
	v := h.view(r)
	if !v.canNamed(ref) {
		writeErr(w, http.StatusNotFound, notFoundInScope)
		return
	}
	all := installPolicies(i)
	k, found := slices.BinarySearchFunc(all, ref, func(u *unstructured.Unstructured, ref objects.Ref) int { return compareAffected(objects.RefOf(u), ref) })
	if !found {
		writeErr(w, http.StatusNotFound, notFoundInScope)
		return
	}
	ix := i.Index
	if ix == nil {
		ix = &explore.Index{}
	}
	e := h.policyReader(p, i, v).read(all[k])
	l := h.linker(v, p, i, nil, nil)
	lh := policyHealth(l, ix, v.viewer(i))
	out := PolicyDetail{Install: i.Resolved.ID(), Ref: l.linkedNamed(ref), Generation: e.p.Generation, Interpreted: e.p.Interpreted,
		Targets: e.targets(lh), Ancestors: make([]PolicyAncestorDTO, 0, len(e.p.Ancestors)), Accepted: e.accepted(), AttachedOn: e.attachedOn(),
		Features: e.features(true), Annotations: make([]PolicyAnnotationDTO, 0, len(e.p.Annotations)),
		Problems: PolicyProblemsDTO{Has: e.problems(), Source: policy.SourceInferred}, CollectedAt: wireTime(p.ConfirmedAt)}
	if l.admitNamed(ref) != LinkNone {
		out.YAML = yamlPath(i.Resolved.ID(), ref)
	}
	for _, a := range e.p.Ancestors {
		d := PolicyAncestorDTO{Ref: LinkedRef{Ref: a.Ref.Ref(), Link: LinkNone}, SectionName: a.Ref.SectionName, Port: a.Ref.Port, Controller: a.Controller,
			Conditions: conditions(e.u, a.Conditions), StaleSource: policy.SourceInferred, Current: a.Current, NotCurrent: a.NotCurrent, Why: a.Why,
			CurrentSource: a.CurrentSource, Summary: a.Summary, SummarySource: a.SummarySource, Readings: make([]PolicyReadingDTO, 0, len(a.Readings)),
			Source: a.Source}
		// The summary ancestor names no object. One whose ownership the caller may not read
		// stays plain: its admission follows that ownership (D-107).
		if !a.Summary && a.NotCurrent != policy.NotCurrentUnreadable {
			d.Ref.Link, d.Health = lh(a.Ref.Ref())
			d.HealthSource = healthSource(d.Health)
		}
		for _, rd := range a.Readings {
			d.Readings = append(d.Readings, PolicyReadingDTO{Type: rd.Type, Warning: rd.Warning, Note: rd.Note, Source: rd.Source})
		}
		out.Ancestors = append(out.Ancestors, d)
	}
	for _, a := range e.p.Annotations {
		out.Annotations = append(out.Annotations, PolicyAnnotationDTO{Key: a.Key, Value: a.Value, Source: a.Source})
	}
	writeJSON(w, out)
}

// yamlPath is the install-scoped YAML path of ref; "-" stands for the empty group or namespace.
func yamlPath(installID string, ref objects.Ref) string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return url.PathEscape(s)
	}
	return "/api/v1/installs/" + url.PathEscape(installID) + "/resources/" + dash(ref.Group) + "/" + url.PathEscape(ref.Kind) + "/" + dash(ref.Namespace) + "/" + url.PathEscape(ref.Name)
}
