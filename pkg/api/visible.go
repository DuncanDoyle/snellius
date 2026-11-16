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

// view returns the view of the caller of r. Under a full read, asked the
// first time a population needs it, it is the allow-all view.
func (h *handler) view(r *http.Request) *view {
	v := &view{a: h.authz, r: r, all: allowsAll(h.authz), secure: h.secure && !h.bypass}
	if !v.all {
		v.lazy = true
		v.seen, v.seenNamed = map[objects.Ref]bool{}, map[objects.Ref]bool{}
		if _, ok := h.authz.(BatchAuthorizer); ok {
			v.lists = map[ListQuery]bool{}
			ls, ok := h.authz.(ListScanner)
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
	if v.secure || v.readsAll() {
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
func (v *view) projects() bool { return v.secure && !v.readsAll() }

// shown reports whether the caller of v sees f: always under the full read
// and outside secure mode, where D-013's restricted rows stay; else when
// f.ShownTo the caller.
func (v *view) shown(f *health.Finding) bool {
	return !v.projects() || f.ShownTo(v)
}

// omits reports whether v leaves out what D-013 shows as a placeholder:
// in secure mode, a hidden object or fact is absent (brief row 16).
func (v *view) omits() bool { return v.secure }

// viewer is the explore form of v for install i: its population check and,
// when v projects, the findings it shows and its install-wide unknown.
func (v *view) viewer(i *engine.InstallModel) explore.Viewer {
	vw := explore.Viewer{Visible: v.visible()}
	if v.projects() {
		vw.Shown, vw.Unknown = v.shown, requiredNotOK(v.report(i).Checks)
	}
	return vw
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
	v.prefetchFindings(i.Health.Findings)
	rep := health.Project(i.Health, v)
	var dec objects.Decoder = objects.Direct{}
	if i.Index != nil {
		dec = indexViews{ix: i.Index}
	}
	d := diagnose.Build(&rep, diagnose.Input{Install: i.Resolved, Owned: i.Owned, Views: dec, Readable: v.can}, diagnose.Families())
	p := &projection{rep: rep, diag: d}
	if v.projections == nil {
		v.projections = map[*engine.InstallModel]*projection{}
	}
	v.projections[i] = p
	return p
}

// prefetchFindings resolves, in two parallel rounds, what ShownTo asks of fs:
// the resources of own-object and collection findings, then the objects named
// by those whose resource was not denied. Derived findings ask nothing.
func (v *view) prefetchFindings(fs []health.Finding) {
	if !v.batching() {
		return
	}
	v.prefetch(func(add func(objects.Ref)) {
		for k := range fs {
			if f := &fs[k]; f.Resource != nil && (f.Class == health.ClassOwn || f.Basis.Collection) {
				add(*f.Resource)
			}
		}
	})
	v.prefetch(func(add func(objects.Ref)) {
		for k := range fs {
			if f := &fs[k]; f.Class == health.ClassOwn && f.Resource != nil && !v.denied(*f.Resource) {
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
