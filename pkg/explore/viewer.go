package explore

import (
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Viewer is one caller's view of an index (design §7.4, D-106). Visible
// admits population members; nil admits every one. Shown says which findings
// the caller sees; nil means the precomputed states, flags and reasons, and
// then Unknown is ignored. Unknown is the caller's install-wide unknown.
// Paths makes the routes on a delegation path restrict its facts (secure mode).
type Viewer struct {
	Visible func(objects.Ref) bool
	Shown   func(*health.Finding) bool
	Unknown bool
	Paths   bool
}

// WorstOf is ref's Worst over the findings vw shows.
func (ix *Index) WorstOf(ref objects.Ref, vw Viewer) Worst {
	if vw.Shown == nil {
		return ix.Findings[ref]
	}
	var w Worst
	for _, k := range ix.findingsOf[ref] {
		if f := &ix.findings[k]; vw.Shown(f) {
			w = w.Add(f)
		}
	}
	return w
}

// StateOf is the row state of ref from the findings vw shows (design §6.3).
// A ListenerSet row's lift by a rejecting parent is ListenerSetStateOf's.
func (ix *Index) StateOf(ref objects.Ref, vw Viewer) health.State {
	if vw.Shown == nil {
		return ix.Findings[ref].State(ix.unknown)
	}
	return ix.WorstOf(ref, vw).State(vw.Unknown)
}

// ListenerSetStateOf is ListenerSet row id's state for vw: StateOf, lifted
// when the row is programmed and its parent has a shown KGWUI-001.
func (ix *Index) ListenerSetStateOf(id RowID, vw Viewer) health.State {
	l := &ix.ListenerSets[id]
	if vw.Shown == nil {
		return l.State
	}
	return lifted(ix.StateOf(l.Ref, vw), l.programmed && ix.shownProblem(l.Parent, health.CodeXDSRejected, vw))
}

// shownProblem reports whether vw shows an error or warning finding with code on ref.
func (ix *Index) shownProblem(ref objects.Ref, code string, vw Viewer) bool {
	for _, k := range ix.findingsOf[ref] {
		f := &ix.findings[k]
		if f.Code == code && (f.Severity == health.SeverityError || f.Severity == health.SeverityWarning) && vw.Shown(f) {
			return true
		}
	}
	return false
}

// FindingShown reports whether shown admits any finding on ref.
func (ix *Index) FindingShown(ref objects.Ref, shown func(*health.Finding) bool) bool {
	for _, k := range ix.findingsOf[ref] {
		if shown(&ix.findings[k]) {
			return true
		}
	}
	return false
}

// Disagreements is n, a row's count of disagreeing checks, when vw shows a
// KGWUI-012 on ref; else 0, as the count comes from a derived check.
func (ix *Index) Disagreements(ref objects.Ref, n int, vw Viewer) int {
	if vw.Shown == nil || ix.shownProblem(ref, health.CodeComputedDisagrees, vw) {
		return n
	}
	return 0
}

// ProblemBackend is the backend that sets route row id's FlagBackendProblem
// for vw: the first non-mirror one with a shown error or warning (D-082).
func (ix *Index) ProblemBackend(id RowID, vw Viewer) *objects.Ref {
	r := &ix.Routes[id]
	if vw.Shown == nil {
		return r.Backend
	}
	for k := range r.problemBackends {
		if w := ix.WorstOf(r.problemBackends[k], vw); w.Error != nil || w.Warning != nil {
			return &r.problemBackends[k]
		}
	}
	return nil
}

// linkFlags are the row flags its parent links set.
const linkFlags = FlagAccepted | FlagNotAccepted | FlagRefsUnresolved | FlagNotProgrammed | FlagPending | FlagDeclared

// RouteStateOf is route row id's state, flags and reason for vw: the worst
// shown finding on the row; gateway-rejecting only from a shown KGWUI-001 on a
// parent Gateway; backend-problem only from a shown finding on a non-mirror
// backend; disagrees only from a shown KGWUI-012; status flags only from
// links to parents vw.Visible admits. Flags pass RowFlags first.
func (ix *Index) RouteStateOf(id RowID, vw Viewer) (health.State, Flag, *health.RowReason) {
	return ix.routeState(id, vw, true)
}

// routeState is RouteStateOf; without links its flags leave out the status
// flags and no parent is asked about. The state never depends on them.
func (ix *Index) routeState(id RowID, vw Viewer, links bool) (health.State, Flag, *health.RowReason) {
	r := &ix.Routes[id]
	f := ix.rowFlags(id, vw.Visible, vw.Paths)
	if vw.Shown == nil {
		return r.State, f, ix.RouteReason(id)
	}
	// Status flags come from the links to parents the viewer may read only:
	// a hidden parent's existence or status never shows on the row.
	f &^= linkFlags | FlagGatewayRejecting | FlagBackendProblem | FlagDisagrees
	for k := range r.Parents {
		if l := &r.Parents[k]; links && (vw.Visible == nil || vw.Visible(l.Target)) {
			f |= l.flags &^ FlagGatewayRejecting
		}
	}
	for _, g := range r.rejectedBy {
		if ix.shownProblem(g, health.CodeXDSRejected, vw) {
			f |= FlagGatewayRejecting
			break
		}
	}
	if r.Flags&FlagDisagrees != 0 && ix.shownProblem(r.Ref, health.CodeComputedDisagrees, vw) {
		f |= FlagDisagrees
	}
	b := ix.ProblemBackend(id, vw)
	if b != nil {
		f |= FlagBackendProblem
	}
	w := ix.WorstOf(r.Ref, vw)
	st := lifted(lifted(w.State(vw.Unknown), f&FlagGatewayRejecting != 0), b != nil)
	return st, f, ix.routeReason(w, st, f, b, vw)
}

// routeReason is RouteReason over w, st, flags f and problem backend b.
func (ix *Index) routeReason(w Worst, st health.State, f Flag, b *objects.Ref, vw Viewer) *health.RowReason {
	out := w.Reason(st)
	if out == nil || out.Kind != health.ReasonGatewayRejecting || f&FlagGatewayRejecting != 0 || b == nil {
		return out
	}
	bw := ix.WorstOf(*b, vw)
	d := bw.Error
	if d == nil {
		d = bw.Warning
	}
	return &health.RowReason{Kind: health.ReasonBackendProblem, Code: d.Code, Message: d.Message}
}

// GatewayMemberCountAs is GatewayMemberCount over the members visible
// admits: a visible route with visible dependencies; nil counts them all.
// With visible set, a listener's unknown count does not make it unknown:
// only routes the caller cannot read can do that, and they are not counted.
func (ix *Index) GatewayMemberCountAs(id RowID, visible func(objects.Ref) bool) (int, bool) {
	g := &ix.Gateways[id]
	if visible == nil || g.View == nil {
		return ix.GatewayMemberCount(id)
	}
	if !ix.computed(g.Ref, g.View.Listeners) {
		return 0, false
	}
	return ix.visibleMembers(g.Ref, g.View.Listeners, visible), true
}

// computed reports whether every listener of ref has a computed count, known or not.
func (ix *Index) computed(ref objects.Ref, ls []objects.Listener) bool {
	if ix.Listeners == nil {
		return false
	}
	for _, l := range ls {
		if _, ok := ix.Listeners[ListenerKey{ref, l.Name}]; !ok {
			return false
		}
	}
	return true
}

// ListenerSetMemberCountAs is GatewayMemberCountAs for ListenerSet row id.
func (ix *Index) ListenerSetMemberCountAs(id RowID, visible func(objects.Ref) bool) (int, bool) {
	l := &ix.ListenerSets[id]
	if visible == nil || l.View == nil {
		return ix.ListenerSetMemberCount(id)
	}
	if !ix.computed(l.Ref, l.View.Listeners) {
		return 0, false
	}
	return ix.visibleMembers(l.Ref, l.View.Listeners, visible), true
}

// ListenerCountAs is listener k's count over the members visible admits,
// whether or not its whole count is known; nil gives the computed count.
func (ix *Index) ListenerCountAs(k ListenerKey, visible func(objects.Ref) bool) int {
	if visible == nil {
		return ix.Listeners[k].Count
	}
	n := 0
	for _, id := range ix.listenerRows(k, visible, true) {
		if visible(ix.Routes[id].Ref) {
			n++
		}
	}
	return n
}

// GatewayMemberCountIn is GatewayMemberCountAs over the member routes in
// scope only (D-100 item 8): a member outside it is skipped before any check.
func (ix *Index) GatewayMemberCountIn(id RowID, visible func(objects.Ref) bool, scope []string) (int, bool) {
	g := &ix.Gateways[id]
	if g.View == nil {
		return 0, false
	}
	return ix.membersIn(g.Ref, g.View.Listeners, visible, scope)
}

// ListenerSetMemberCountIn is GatewayMemberCountIn for ListenerSet row id.
func (ix *Index) ListenerSetMemberCountIn(id RowID, visible func(objects.Ref) bool, scope []string) (int, bool) {
	l := &ix.ListenerSets[id]
	if l.View == nil {
		return 0, false
	}
	return ix.membersIn(l.Ref, l.View.Listeners, visible, scope)
}

// membersIn counts the distinct member routes of ref's listeners in scope
// whose dependencies and own ref visible admits; nil admits all, and then,
// as memberCount, a listener of unknown count gives no count.
func (ix *Index) membersIn(ref objects.Ref, ls []objects.Listener, visible func(objects.Ref) bool, scope []string) (int, bool) {
	if !ix.computed(ref, ls) {
		return 0, false
	}
	if visible == nil {
		if _, ok := ix.memberCount(ref, ls, nil); !ok {
			return 0, false
		}
	}
	seen := map[RowID]bool{}
	for _, l := range ls {
		k := ListenerKey{ref, l.Name}
		ms := ix.ByListener[k]
		if !ix.Listeners[k].Known {
			ms = ix.unknownMembers[k]
		}
		for _, m := range ms {
			r := ix.Routes[m.Row].Ref
			if seen[m.Row] || !InScope(scope, r.Namespace) {
				continue
			}
			if ix.Visible(m.Deps, visible) && (visible == nil || visible(r)) {
				seen[m.Row] = true
			}
		}
	}
	return len(seen), true
}

// visibleMembers counts the distinct visible member routes of ref's listeners.
func (ix *Index) visibleMembers(ref objects.Ref, ls []objects.Listener, visible func(objects.Ref) bool) int {
	seen := map[RowID]bool{}
	for _, l := range ls {
		for _, id := range ix.listenerRows(ListenerKey{ref, l.Name}, visible, true) {
			if !seen[id] && visible(ix.Routes[id].Ref) {
				seen[id] = true
			}
		}
	}
	return len(seen)
}
