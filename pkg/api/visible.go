package api

import (
	"net/http"
	"slices"
	"sync"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/paths"
)

// view is what the caller of one request may see (design §7.4). Every
// visibility decision of a request goes through its view, which asks the
// Authorizer about each ref at most once per kind of question. Under
// AllowAll it never asks and every count is the precomputed one (rule 10).
// A view is used by one goroutine.
//
// Population answers (can) and named answers (canNamed) never share storage
// (D-100): a named allow must never answer a population question.
type view struct {
	a Authorizer
	r *http.Request
	// all is AllowAll or, once asked, the full read; read it through readsAll.
	all bool
	// lazy is set until readsAll asks the full read (D-100): canNamed never does.
	lazy bool
	seen map[objects.Ref]bool
	// seenNamed memoises canNamed.
	seenNamed map[objects.Ref]bool
	// pooled is set when seen and seenNamed came from seenPool and namedPool.
	pooled bool
	// deps memoises allowDeps per index of the request's dependency table: 1 visible, -1 hidden.
	deps []int8
	// table and tableDeps are allowTable's topology and its memo, as deps.
	table     *paths.Topology
	tableDeps []int8
	// lists are the list answers prefetchLists resolved for this request (D-100 item 6).
	lists map[ListQuery]bool
	// listFinal is set when a list denial is a member's final answer: no named-get scan.
	listFinal bool
	// secure is set in secure mode: without the full read, findings and check
	// results are projected (D-106) instead of shown as D-013 restricted rows.
	secure bool
	// off are the projection steps turned off (rule tests only).
	off projStep
	// projections memoises each install's projected report and diagnosis.
	projections map[*engine.InstallModel]*projection
	// listed memoises canList's cluster-wide answers.
	listed map[ListQuery]bool
}

// projection is an install's report and diagnosis as a viewer without the full read sees them.
type projection struct {
	rep  health.Report
	diag *diagnose.Diagnosis
}

// projStep is one step of the secure-mode projection. The rule tests turn
// each off alone to show that it matters.
type projStep uint8

const (
	// stepFindings projects findings, check results and derived facts (D-106).
	stepFindings projStep = 1 << iota
	// stepRowState decides row states and flags from the shown findings.
	stepRowState
	// stepCounts leaves hidden rows, counts and placeholders out (omits).
	stepCounts
	// stepDelegation withholds delegation analysis (D-114).
	stepDelegation
)

// view returns the view of the caller of r. Under a full read, asked the
// first time a population needs it, it is the allow-all view.
func (h *handler) view(r *http.Request) *view {
	v := newView(r, h.authz, h.secure && !h.bypass)
	v.off = h.off
	return v
}

// newView is the view of r's caller through a; secure turns the projection on.
func newView(r *http.Request, a Authorizer, secure bool) *view {
	v := &view{a: a, r: r, all: allowsAll(a), secure: secure}
	if !v.all {
		v.lazy = true
		v.seen, v.seenNamed = map[objects.Ref]bool{}, map[objects.Ref]bool{}
		if _, ok := a.(BatchAuthorizer); ok {
			v.lists = map[ListQuery]bool{}
			ls, ok := a.(ListScanner)
			v.listFinal = ok && !ls.NamedGetScan()
		}
	}
	return v
}

// listQueryOf is the list question that admits ref as a population member.
func listQueryOf(ref objects.Ref) ListQuery {
	return ListQuery{Kind: objects.GroupKind{Group: ref.Group, Kind: ref.Kind}, Namespace: ref.Namespace}
}

// prefetchLists resolves the questions of qs not yet in v's table, in
// parallel, before v renders. An error is recorded for the request, which
// then answers 503; v itself stays on its own goroutine.
func (v *view) prefetchLists(qs []ListQuery) {
	ba, ok := v.a.(BatchAuthorizer)
	if !ok || v.readsAll() || authzFailed(v.r) {
		return
	}
	todo := make([]ListQuery, 0, len(qs))
	for _, q := range qs {
		if _, done := v.lists[q]; !done {
			todo = append(todo, q)
		}
	}
	if len(todo) == 0 {
		return
	}
	res, err := ba.ResolveLists(v.r, todo)
	if err != nil {
		ReportAuthzError(v.r, err)
		return
	}
	for i, q := range todo {
		v.lists[q] = res[i]
	}
}

// batching reports whether v resolves list questions in parallel before it
// renders: a BatchAuthorizer, no full read and no error so far.
func (v *view) batching() bool {
	_, ok := v.a.(BatchAuthorizer)
	return ok && !v.readsAll() && !authzFailed(v.r)
}

// prefetch resolves, in one parallel round, the buckets of the refs refs
// adds. List handlers run it on their roots first, then on what the roots
// they admit need (plan-7.3b #5); a ref no round covers is decided on its own.
func (v *view) prefetch(refs func(add func(objects.Ref))) {
	if !v.batching() {
		return
	}
	seen := map[ListQuery]bool{}
	var qs []ListQuery
	refs(func(ref objects.Ref) {
		if q := listQueryOf(ref); !seen[q] {
			seen[q] = true
			qs = append(qs, q)
		}
	})
	v.prefetchLists(qs)
}

// prefetchSets resolves dependency sets in rounds, in the order allowDeps and
// allowTable ask them: each round asks every undecided set's next unresolved
// bucket, and a set with a denied bucket is decided. So no set asks a bucket
// its own check would not. ref maps a set's index to its ref.
func (v *view) prefetchSets(sets []explore.DepSet, ref func(int32) objects.Ref) {
	// Rows share interned dependency sets; each distinct set is resolved once.
	seen := map[*int32]bool{}
	todo := make([]explore.DepSet, 0, len(sets))
	for _, d := range sets {
		if len(d) > 0 && !seen[&d[0]] {
			seen[&d[0]] = true
			todo = append(todo, d)
		}
	}
	denied := func(k int32) bool { return v.denied(ref(k)) }
	unknown := func(k int32) bool {
		_, ok := v.lists[listQueryOf(ref(k))]
		return !ok
	}
	for len(todo) > 0 && v.batching() {
		next := todo[:0]
		v.prefetch(func(add func(objects.Ref)) {
			for _, d := range todo {
				if slices.ContainsFunc(d, denied) {
					continue
				}
				if i := slices.IndexFunc(d, unknown); i >= 0 {
					add(ref(d[i]))
					next = append(next, d)
				}
			}
		})
		todo = next
	}
}

// denied reports whether a prefetch round denied ref's bucket, so a hidden
// root never adds a bucket. With the named-get scan ref may still be admitted.
func (v *view) denied(ref objects.Ref) bool {
	allowed, ok := v.lists[listQueryOf(ref)]
	return ok && !allowed
}

// member decides ref as a population member: from the prefetched list
// answer when it is final, else through the Authorizer.
func (v *view) member(ref objects.Ref) bool {
	if allowed, ok := v.lists[listQueryOf(ref)]; ok && (allowed || v.listFinal) {
		return allowed
	}
	return decide(v.r, v.a, ref)
}

// readsAll reports whether v is the allow-all view, asking the request's
// full read the first time (D-100). Every population question starts here.
func (v *view) readsAll() bool {
	if v.lazy {
		v.lazy = false
		v.all = fullRead(v.r, v.a)
	}
	return v.all
}

// seenPool recycles the memos of route list requests, which ask about
// every row: a memo grown afresh per request was most of their garbage.
// Tests swap it to control reuse.
var seenPool interface {
	Get() any
	Put(any)
} = &sync.Pool{New: func() any { return map[objects.Ref]bool{} }}

// namedPool recycles the named memos as seenPool does the population ones;
// the two never trade maps. Tests swap it to control reuse.
var namedPool interface {
	Get() any
	Put(any)
} = &sync.Pool{New: func() any { return map[objects.Ref]bool{} }}

// borrow swaps v's memos for ones from seenPool and namedPool; release returns them.
func (v *view) borrow() {
	if v.readsAll() || v.pooled {
		return
	}
	v.seen, v.seenNamed, v.pooled = seenPool.Get().(map[objects.Ref]bool), namedPool.Get().(map[objects.Ref]bool), true
}

// release clears the borrowed memos and returns each to its pool. v asks the
// Authorizer afresh after it.
func (v *view) release() {
	if !v.pooled {
		return
	}
	clear(v.seen)
	clear(v.seenNamed)
	seenPool.Put(v.seen)
	namedPool.Put(v.seenNamed)
	v.seen, v.seenNamed, v.pooled = map[objects.Ref]bool{}, map[objects.Ref]bool{}, false
}

// can reports whether the caller may view ref as a member of a population:
// a list row, count, facet or aggregate (D-100).
func (v *view) can(ref objects.Ref) bool {
	if v.readsAll() {
		return true
	}
	ok, asked := v.seen[ref]
	if !asked {
		ok = v.member(ref)
		v.seen[ref] = ok
	}
	return ok
}

// canNamed reports whether the caller may view ref by name: a detail page's
// subject, or a ref written in an object the caller may read (D-100).
// Without a NamedAuthorizer both questions have one answer, so it is can.
// It uses a full read already asked but never asks one.
func (v *view) canNamed(ref objects.Ref) bool {
	if v.all {
		return true
	}
	if _, ok := v.a.(NamedAuthorizer); !ok {
		return v.can(ref)
	}
	ok, asked := v.seenNamed[ref]
	if !asked {
		ok = decideNamed(v.r, v.a, ref)
		v.seenNamed[ref] = ok
	}
	return ok
}

// canList reports whether the caller may list gk in namespace, "" meaning
// cluster-wide: true under a full read, false without a ListAuthorizer. A
// prefetched answer serves a namespace; cluster-wide is asked on its own.
func (v *view) canList(gk objects.GroupKind, namespace string) bool {
	if v.readsAll() {
		return true
	}
	q := ListQuery{Kind: gk, Namespace: namespace}
	if allowed, ok := v.lists[q]; ok && namespace != "" {
		return allowed
	}
	if allowed, ok := v.listed[q]; ok {
		return allowed
	}
	allowed := decideList(v.r, v.a, gk, namespace)
	if v.listed == nil {
		v.listed = map[ListQuery]bool{}
	}
	v.listed[q] = allowed
	return allowed
}

// visible is can in the form the explore queries take: nil under AllowAll,
// so they keep their precomputed values.
func (v *view) visible() func(objects.Ref) bool {
	if v.readsAll() {
		return nil
	}
	return v.can
}

// allowDeps reports whether the caller may view every dependency in d
// (design §7.4 rule 4). Answers are memoised per ref index; under AllowAll it
// is true without calls (rule 10). A view serves one index.
func (v *view) allowDeps(ix *explore.Index, d explore.DepSet) bool {
	if v.readsAll() {
		return true
	}
	if v.deps == nil {
		v.deps = make([]int8, len(ix.Refs))
	}
	for _, i := range d {
		switch v.deps[i] {
		case -1:
			return false
		case 0:
			if !v.can(ix.Ref(i)) {
				v.deps[i] = -1
				return false
			}
			v.deps[i] = 1
		}
	}
	return true
}

// allowTable reports whether the caller may view every dependency in d, an
// index set into t.Refs (design §13). Answers are memoised per topology; under
// AllowAll it is true without calls.
func (v *view) allowTable(t *paths.Topology, d explore.DepSet) bool {
	return v.allowTableNamed(t, d, nil)
}

// allowTableNamed is allowTable with named, when set, checked by canNamed: a
// ref written in a readable object. Its answer never enters the population memo.
func (v *view) allowTableNamed(t *paths.Topology, d explore.DepSet, named *objects.Ref) bool {
	if v.readsAll() {
		return true
	}
	if v.table != t {
		v.table, v.tableDeps = t, make([]int8, len(t.Refs))
	}
	// A final prefetched denial anywhere in d hides it before any check runs,
	// so a hidden route never asks about the refs it names.
	if v.listFinal {
		for _, i := range d {
			if v.tableDeps[i] == 0 && (named == nil || t.Refs[i] != *named) && v.denied(t.Refs[i]) {
				return false
			}
		}
	}
	for _, i := range d {
		if named != nil && t.Refs[i] == *named {
			if !v.canNamed(*named) {
				return false
			}
			continue
		}
		switch v.tableDeps[i] {
		case -1:
			return false
		case 0:
			if !v.can(t.Refs[i]) {
				v.tableDeps[i] = -1
				return false
			}
			v.tableDeps[i] = 1
		}
	}
	return true
}

// anyOf reports whether the caller may view at least one of refs.
func (v *view) anyOf(refs []objects.Ref) bool {
	for _, ref := range refs {
		if v.can(ref) {
			return true
		}
	}
	return false
}

// allOf reports whether the caller may view every one of refs.
func (v *view) allOf(refs []objects.Ref) bool {
	for _, ref := range refs {
		if !v.can(ref) {
			return false
		}
	}
	return true
}

// recount is the number of ids whose ref the caller may view (rule 5).
func (v *view) recount(ids []explore.RowID, ref func(explore.RowID) objects.Ref) int {
	if v.readsAll() {
		return len(ids)
	}
	n := 0
	for _, id := range ids {
		if v.can(ref(id)) {
			n++
		}
	}
	return n
}

// deniedInInstall is the number of the n rows of a listed kind that the
// caller may not view. It takes no filter, so no query changes it (rule 2).
// Secure mode sends no hidden count: it is 0 there.
func (v *view) deniedInInstall(n int, ref func(explore.RowID) objects.Ref) int {
	if v.omits() || v.readsAll() {
		return 0
	}
	d := 0
	for i := range n {
		if !v.can(ref(explore.RowID(i))) { // #nosec G115 -- a row count fits in int32
			d++
		}
	}
	return d
}

// Ref implements health.Readable: whether the caller may read ref as a population member.
func (v *view) Ref(ref objects.Ref) bool { return v.can(ref) }

// projects reports whether v projects findings and check results (D-106):
// secure mode without the full read.
func (v *view) projects() bool { return v.secure && v.off&stepFindings == 0 && !v.readsAll() }

// noDelegation reports whether v withholds delegation analysis: secure mode
// without the full read (D-114). Permissions alone decide it.
func (v *view) noDelegation() bool { return v.secure && v.off&stepDelegation == 0 && !v.readsAll() }

// shown reports whether the caller of v sees f: always under the full read
// and outside secure mode, where D-013's restricted rows stay; else when
// f.ShownTo the caller.
func (v *view) shown(f *health.Finding) bool {
	return !v.projects() || f.ShownTo(v)
}

// omits reports whether v leaves out what D-013 shows as a placeholder:
// in secure mode, a hidden object or fact is absent (brief row 16).
func (v *view) omits() bool { return v.secure && v.off&stepCounts == 0 }

// viewer is the explore form of v for install i: its population check and,
// when v projects, the findings it shows and its install-wide unknown.
func (v *view) viewer(i *engine.InstallModel) explore.Viewer {
	return v.viewerIn(i, nil)
}

// viewerIn is viewer for a list under scope. A scoped list projects the
// checks alone, so no finding outside the scope is asked about. Route paths
// are decided incomplete from v's permissions where states are (design §8.1).
func (v *view) viewerIn(i *engine.InstallModel, scope []string) explore.Viewer {
	vw := explore.Viewer{Visible: v.visible(), NoDelegation: v.noDelegation()}
	switch {
	case !v.secure || v.off&stepRowState != 0 || v.readsAll():
	case scope == nil:
		vw.Shown, vw.Unknown, vw.Perms = v.shown, requiredNotOK(v.report(i).Checks), incompletePermissions{v}
	default:
		vw.Shown, vw.Unknown, vw.Perms = v.shown, requiredNotOK(health.Project(health.Report{Checks: i.Health.Checks}, v).Checks), incompletePermissions{v}
	}
	return vw
}

// endpointSlices is the kind whose list permission endpoint counts need (design §7.6).
var endpointSlices = objects.GroupKind{Group: "discovery.k8s.io", Kind: "EndpointSlice"}

// projectInput is what the caller of v's path projection of install i in p
// reads: for a viewer without the full read, endpoint summaries carry the
// confirmation time, never the time of the last recompute (D-109).
func (v *view) projectInput(p *engine.Published, i *engine.InstallModel) paths.ProjectInput {
	in := projectInput(p, i)
	if v.projects() {
		in.ReadAt = p.ConfirmedAt
	}
	return in
}

// visibleRows picks the rows of t that rows picks (all when nil) and the
// caller of v may see, as pathsPage admits them.
func (v *view) visibleRows(t *paths.Topology, rows func(paths.Row) bool) func(paths.Row) bool {
	return func(r paths.Row) bool { return (rows == nil || rows(r)) && v.allowTable(t, t.Deps(r)) }
}

// scopedFilter is what the caller of v may see of install i's path projection,
// projecting only the rows of t that rows picks (all when nil): finding signals
// of shown findings only, and endpoint counts only where it may list
// EndpointSlices. Nil when v does not project. Findings and the picked rows'
// EndpointSlice checks are prefetched for those rows only.
func (v *view) scopedFilter(i *engine.InstallModel, t *paths.Topology, rows func(paths.Row) bool) *paths.ProjectionFilter {
	if !v.projects() {
		return nil
	}
	var want func(objects.Ref) bool
	var eps []objects.Ref
	if rows != nil {
		deps, nss := map[objects.Ref]bool{}, map[string]bool{}
		for _, r := range t.Rows {
			if rows(r) {
				for _, k := range t.Deps(r) {
					deps[t.Refs[k]] = true
				}
				if ns, ok := t.EndpointsNamespace(r); ok && !nss[ns] {
					nss[ns] = true
					// listQueryOf maps this ref to canList's EndpointSlice question.
					eps = append(eps, objects.Ref{Group: endpointSlices.Group, Kind: endpointSlices.Kind, Namespace: ns})
				}
			}
		}
		want = func(ref objects.Ref) bool { return deps[ref] }
	}
	v.prefetchFindings(i.Health.Findings, want, eps...)
	var byKey map[titleKey][]*health.Finding
	shown := map[titleKey]bool{}
	signal := func(s paths.Signal) bool {
		if s.Kind != paths.SignalFinding {
			return true
		}
		k := titleKey{s.Code, s.Ref}
		if ok, done := shown[k]; done {
			return ok
		}
		if byKey == nil {
			byKey = map[titleKey][]*health.Finding{}
			for j := range i.Health.Findings {
				if f := &i.Health.Findings[j]; f.Resource != nil {
					byKey[titleKey{f.Code, *f.Resource}] = append(byKey[titleKey{f.Code, *f.Resource}], f)
				}
			}
		}
		shown[k] = slices.ContainsFunc(byKey[k], v.shown)
		return shown[k]
	}
	return &paths.ProjectionFilter{Signal: signal, Endpoints: func(ns string) bool { return v.canList(endpointSlices, ns) }, Rows: rows}
}

// requiredNotOK reports whether a required check of cs is not OK.
func requiredNotOK(cs []health.CheckResult) bool {
	return slices.ContainsFunc(cs, func(c health.CheckResult) bool { return c.Required && !c.OK })
}

// report is install i's report as the caller of v sees it: the model's under
// the full read and outside secure mode, else health.Project's, grouped by
// v.diagnosis. It is memoised per request.
func (v *view) report(i *engine.InstallModel) health.Report {
	if !v.projects() {
		return i.Health
	}
	return v.projection(i).rep
}

// diagnosis is install i's diagnosis as the caller of v sees it: the model's
// under the full read and outside secure mode, else one built over v.report
// that only names causes and affected objects the caller may read.
func (v *view) diagnosis(i *engine.InstallModel) *diagnose.Diagnosis {
	if !v.projects() {
		return diagnosis(i)
	}
	return v.projection(i).diag
}

// projection builds and memoises i's projected report and diagnosis.
func (v *view) projection(i *engine.InstallModel) *projection {
	if p := v.projections[i]; p != nil {
		return p
	}
	p := v.project(i, i.Health)
	if v.projections == nil {
		v.projections = map[*engine.InstallModel]*projection{}
	}
	v.projections[i] = p
	return p
}

// project is rep, a report of install i, projected for the caller of v and grouped.
func (v *view) project(i *engine.InstallModel, rep health.Report) *projection {
	v.prefetchFindings(rep.Findings, nil)
	rep = health.Project(rep, v)
	d := diagnose.Build(&rep, diagnose.Input{Install: i.Resolved, Owned: i.Owned, Views: decoderOf(i), Readable: v.can}, diagnose.Families())
	return &projection{rep: rep, diag: d}
}

// findingsIn is install i's findings and diagnosis as the caller of v sees
// them, under scope. A projecting caller projects only the findings on
// resources in scope, so nothing is asked about the others (D-100 item 8).
// With groups every caller gets a diagnosis of the in-scope findings, so group
// counts come from rows in scope only; a full list keeps the install's order.
func (v *view) findingsIn(i *engine.InstallModel, scope []string, groups bool) ([]health.Finding, *diagnose.Diagnosis) {
	if scope == nil || !v.projects() && !groups {
		return v.report(i).Findings, v.diagnosis(i)
	}
	rep := i.Health
	rep.Findings = slices.DeleteFunc(slices.Clone(rep.Findings), func(f health.Finding) bool { return !findingInScope(&f, scope) })
	if v.projects() {
		p := v.project(i, rep)
		return p.rep.Findings, p.diag
	}
	d := diagnose.Build(&rep, diagnose.Input{Install: i.Resolved, Owned: i.Owned, Views: decoderOf(i)}, diagnose.Families())
	return rep.Findings, d
}

// decoderOf decodes through install i's index views when it has an index.
func decoderOf(i *engine.InstallModel) objects.Decoder {
	if i.Index != nil {
		return indexViews{ix: i.Index}
	}
	return objects.Direct{}
}

// findingInScope reports whether f's resource is a namespaced object in
// scope; a finding without one is outside every scope.
func findingInScope(f *health.Finding, scope []string) bool {
	return scope == nil || f.Resource != nil && f.Resource.Namespace != "" && explore.InScope(scope, f.Resource.Namespace)
}

// prefetchFindings resolves, in two parallel rounds, what ShownTo asks of the
// findings of fs on resources want admits (all when nil):
// the resources of own-object and collection findings, then the objects named
// by those whose resource was not denied. Derived findings ask nothing.
// The buckets of extra join the first round.
func (v *view) prefetchFindings(fs []health.Finding, want func(objects.Ref) bool, extra ...objects.Ref) {
	if !v.batching() {
		return
	}
	v.prefetch(func(add func(objects.Ref)) {
		for _, r := range extra {
			add(r)
		}
		for k := range fs {
			if f := &fs[k]; f.Resource != nil && (want == nil || want(*f.Resource)) && (f.Class() == health.ClassOwn || f.Basis != nil && f.Basis.Collection) {
				add(*f.Resource)
			}
		}
	})
	v.prefetch(func(add func(objects.Ref)) {
		for k := range fs {
			if f := &fs[k]; f.Class() == health.ClassOwn && f.Basis != nil && f.Resource != nil && (want == nil || want(*f.Resource)) && !v.denied(*f.Resource) {
				for _, r := range f.Basis.Refs {
					add(r)
				}
			}
		}
	})
}

// namedSubject reads subject by name and every other ref as a population
// member: a finding detail's resource and its basis (D-100).
type namedSubject struct {
	v       *view
	subject objects.Ref
}

// Ref implements health.Readable.
func (n namedSubject) Ref(r objects.Ref) bool {
	if r == n.subject {
		return n.v.canNamed(r)
	}
	return n.v.can(r)
}
