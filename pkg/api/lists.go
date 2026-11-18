package api

import (
	"errors"
	"net/http"
	"slices"

	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// list is one list request after its install was found and its query parsed.
type list struct {
	h  *handler
	r  *http.Request
	p  *engine.Published
	i  *engine.InstallModel
	ix *explore.Index
	q  explore.Query
	v  *view
	l  linker
	f  facts
	// vw is v's explore form for the install.
	vw explore.Viewer
	// paths reads the route rows' traffic paths; set by the routes list.
	paths *routePaths
}

// startList finds the install and parses the query, or writes 503, 404 or 400.
func (h *handler) startList(w http.ResponseWriter, r *http.Request, kind listKind) *list {
	p, i := h.find(w, r)
	if i == nil {
		return nil
	}
	q, err := parseQuery(r, kind)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return nil
	}
	ix := i.Index
	if ix == nil {
		ix = &explore.Index{}
	}
	v := h.view(r)
	l := h.linker(v, p, i, nil, nil)
	return &list{h: h, r: r, p: p, i: i, ix: ix, q: q, v: v, l: l, f: facts{ix: ix, v: v, l: l}, vw: v.viewerIn(i, q.Scope)}
}

// addInScope adds root ref to a prefetch round when the query's scope holds it.
func (ls *list) addInScope(add func(objects.Ref), ref objects.Ref) {
	if explore.InScope(ls.q.Scope, ref.Namespace) {
		add(ref)
	}
}

// recount is view.recount over the ids in the query's scope only.
func (ls *list) recount(ids []explore.RowID, ref func(explore.RowID) objects.Ref) int {
	if ls.q.Scope == nil {
		return ls.v.recount(ids, ref)
	}
	in := make([]explore.RowID, 0, len(ids))
	for _, id := range ids {
		if explore.InScope(ls.q.Scope, ref(id).Namespace) {
			in = append(in, id)
		}
	}
	return ls.v.recount(in, ref)
}

// deniedInScope is deniedInInstall over the n rows in the query's scope only.
func (ls *list) deniedInScope(n int, ref func(explore.RowID) objects.Ref) int {
	if ls.q.Scope == nil {
		return ls.v.deniedInInstall(n, ref)
	}
	var ids []explore.RowID
	for i := range n {
		if id := explore.RowID(i); explore.InScope(ls.q.Scope, ref(id).Namespace) { // #nosec G115 -- a row count fits in int32
			ids = append(ids, id)
		}
	}
	return ls.v.deniedInInstall(len(ids), func(k explore.RowID) objects.Ref { return ref(ids[k]) })
}

func (ls *list) routeRef(id explore.RowID) objects.Ref { return ls.ix.Routes[id].Ref }
func (ls *list) gwRef(id explore.RowID) objects.Ref    { return ls.ix.Gateways[id].Ref }
func (ls *list) lsRef(id explore.RowID) objects.Ref    { return ls.ix.ListenerSets[id].Ref }

// fail writes 400 for a query the index rejects; anything else is a bug.
func fail(w http.ResponseWriter, err error) {
	if errors.Is(err, explore.ErrBadQuery) || errors.Is(err, explore.ErrBadCursor) {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, "internal error")
}

// page wraps items with pg's totals and cursors.
func page[T any](ls *list, pg explore.Page, items []T, denied int) Page[T] {
	return Page[T]{Install: ls.i.Resolved.ID(), Items: items, Total: pg.Total, Next: pg.Next, Prev: pg.Prev,
		DeniedInInstall: denied, CollectedAt: wireTime(ls.p.ConfirmedAt)}
}

func (h *handler) routes(w http.ResponseWriter, r *http.Request) {
	ls := h.startList(w, r, listRoutes)
	if ls == nil {
		return
	}
	ls.v.borrow()
	defer ls.v.release()
	ls.v.prefetch(func(add func(objects.Ref)) {
		for i := range ls.ix.Routes {
			ls.addInScope(add, ls.ix.Routes[i].Ref)
		}
	})
	// The path filter, and a restricted viewer's health filter or sort, read every scoped route's path rows; else only the page's.
	filtered := len(ls.q.Path) > 0 || ls.vw.Perms != nil && (len(ls.q.Health) > 0 || ls.q.Sort.Key == "health")
	if filtered {
		ls.paths = h.routePaths(ls.p, ls.i, ls.ix, ls.v, func(ref objects.Ref) bool { return explore.InScope(ls.q.Scope, ref.Namespace) })
		ls.vw.Path = ls.paths.pathFunc()
	}
	pg, err := ls.ix.QueryRoutesAs(ls.q, ls.vw)
	if err != nil {
		fail(w, err)
		return
	}
	ls.prefetchRouteRows(pg.IDs)
	if !filtered {
		onPage := make(map[objects.Ref]bool, len(pg.IDs))
		for _, id := range pg.IDs {
			onPage[ls.ix.Routes[id].Ref] = true
		}
		ls.paths = h.routePaths(ls.p, ls.i, ls.ix, ls.v, func(ref objects.Ref) bool { return onPage[ref] })
		ls.vw.Path = ls.paths.pathFunc()
	}
	items := make([]RouteRow, 0, len(pg.IDs))
	for _, id := range pg.IDs {
		items = append(items, ls.routeRow(id))
	}
	out := page(ls, pg, items, ls.deniedInScope(len(ls.ix.Routes), ls.routeRef))
	// The dropped flag and state are withheld from every row (D-114).
	out.DelegationNotChecked = ls.vw.NoDelegation
	writeJSON(w, out)
}

// prefetchRouteRows resolves the buckets the page's route rows read: their
// parents, reported edges, problem backend and facts. A ListenerSet's Gateway
// is asked only for a ListenerSet not denied, as parentSummary asks it.
func (ls *list) prefetchRouteRows(ids []explore.RowID) {
	ix := ls.ix
	var sets []explore.DepSet
	ls.v.prefetch(func(add func(objects.Ref)) {
		for _, id := range ids {
			r := &ix.Routes[id]
			for _, p := range r.Parents {
				add(p.Target)
			}
			for _, p := range r.ParentRoutes {
				add(p)
			}
			if r.Backend != nil {
				add(*r.Backend)
			}
			for j := range r.Computed {
				sets = append(sets, r.Computed[j].Deps)
			}
			sets = append(sets, r.UnknownDeps...)
			if !ls.vw.NoDelegation {
				sets = append(sets, r.DroppedDeps...)
			}
		}
	})
	ls.v.prefetch(func(add func(objects.Ref)) {
		for _, id := range ids {
			for _, p := range ix.Routes[id].Parents {
				if p.Via != nil && !ls.v.denied(p.Target) {
					add(*p.Via)
				}
			}
		}
	})
	ls.v.prefetchSets(sets, ix.Ref)
}

// routeRow is route row id on the wire.
func (ls *list) routeRow(id explore.RowID) RouteRow {
	r := &ls.ix.Routes[id]
	st, flags, _ := ls.ix.RouteStateOf(id, ls.vw)
	out := RouteRow{Ref: ls.l.linked(r.Ref), Hostnames: r.Hostnames, Parents: make([]ParentSummary, 0, len(r.Parents)),
		ParentRoutes: make([]LinkedRef, 0, len(r.ParentRoutes)), Children: r.Children, Flags: flags.Names(), Health: st,
		Reason: routeReason(ls.ix, ls.v, ls.vw, ls.l, id)}
	out.Config, out.Path = routeSignals(ls.ix, ls.v, ls.vw, ls.l, ls.paths, id)
	if out.Hostnames == nil {
		out.Hostnames = []string{}
	}
	for _, p := range r.Parents {
		// A projecting viewer gets no entry for a parent it may not read: its existence and status stay hidden.
		if ls.v.projects() && !ls.v.can(p.Target) {
			continue
		}
		out.Parents = append(out.Parents, ls.parentSummary(id, p))
	}
	// Reported edges are the row's own status (rule 8); link() decides each.
	for _, p := range r.ParentRoutes {
		out.ParentRoutes = append(out.ParentRoutes, ls.l.linked(p))
	}
	if !ls.v.readsAll() || ls.q.Scope != nil {
		out.Children = ls.recount(ls.ix.ByParentRoute[r.Ref], ls.routeRef)
	}
	if len(ls.q.Backends) > 0 {
		out.Mirror = ls.mirrorOnly(id)
	}
	return out
}

// routeReason is route row id's reason for vw on the wire. A backend-problem
// names its backend and message only to a caller who may view the backend (D-013).
func routeReason(ix *explore.Index, v *view, vw explore.Viewer, l linker, id explore.RowID) *RouteReason {
	_, _, r := ix.RouteStateOf(id, vw)
	if r == nil {
		return nil
	}
	out := &RouteReason{RowReason: *r}
	if b := ix.ProblemBackend(id, vw); r.Kind == health.ReasonBackendProblem {
		if v.can(*b) {
			lr := l.linked(*b)
			out.Backend = &lr
		} else {
			out.Message = ""
		}
	}
	return out
}

// routeSignals are route row id's two signals for vw, whose path rp reads,
// on the wire. A path reason from a finding carries its shown message; a
// backend-problem names its backend as routeReason does.
func routeSignals(ix *explore.Index, v *view, vw explore.Viewer, l linker, rp *routePaths, id explore.RowID) (RouteSignal, PathSignal) {
	config, path, b := ix.RouteSignals(id, vw)
	out := PathSignal{State: path.State, Incomplete: path.Incomplete}
	if path.Reason != nil {
		out.Reason = &RouteReason{RowReason: *path.Reason}
		if k := path.Reason.Kind; k == health.ReasonBackendProblem || k == health.ReasonPathProblem {
			out.Reason.Message = rp.message(id)
		}
		if b != nil && v.can(*b) {
			lr := l.linked(*b)
			out.Reason.Backend = &lr
		}
	}
	return RouteSignal{State: config.State, Reason: config.Reason}, out
}

// parentSummary is one parent link on the wire. Via comes from the
// ListenerSet's spec, so it is sent only when the caller may view both the
// ListenerSet and its Gateway (rule 3); in secure mode it is else omitted.
// Computed is view A for row id's parentRef, omitted there when restricted.
func (ls *list) parentSummary(id explore.RowID, p explore.ParentLink) ParentSummary {
	s := ParentSummary{Ref: ls.l.linked(p.Target), SectionName: p.Ref.SectionName, Port: p.Ref.Port, Attachment: p.Attachment}
	if p.Via != nil {
		if ls.v.can(p.Target) && ls.v.can(*p.Via) {
			v := ls.l.linked(*p.Via)
			s.Via = &v
		} else {
			s.ViaRestricted = !ls.v.omits()
		}
	}
	switch {
	case p.Cond != nil:
		s.ConditionType, s.Reason, s.Message = p.Cond.Type, p.Cond.Reason, computed.ClipDisplay(p.Cond.Message, computed.MaxStatusBytes)
	case p.Attachment == explore.AttachmentPending:
		s.Reason = p.Pending
	}
	if f := ls.ix.Facts(id, p); f != nil {
		a := ls.f.attachment(f)
		for k := range p.Dups {
			if d := ls.ix.DupFacts(id, p, k); d != nil {
				a = mergeAttachment(a, ls.f.attachment(d))
			}
		}
		if !ls.f.omitted(a.Computed) {
			s.Computed = &a
		}
	}
	return s
}

// mirrorOnly reports whether every use of row id among the queried
// backends is a RequestMirror. It reads the row's own spec only (rule 2a).
func (ls *list) mirrorOnly(id explore.RowID) bool {
	matched := false
	for _, b := range ls.q.Backends {
		uses := ls.ix.ByBackend[b]
		j, ok := slices.BinarySearchFunc(uses, id, func(u explore.BackendUse, id explore.RowID) int { return int(u.Row - id) })
		if !ok {
			continue
		}
		if !uses[j].Mirror {
			return false
		}
		matched = true
	}
	return matched
}

func (h *handler) gateways(w http.ResponseWriter, r *http.Request) {
	ls := h.startList(w, r, listGateways)
	if ls == nil {
		return
	}
	ls.v.prefetch(func(add func(objects.Ref)) {
		for i := range ls.ix.Gateways {
			ls.addInScope(add, ls.ix.Gateways[i].Ref)
		}
	})
	pg, err := ls.ix.QueryGatewaysAs(ls.q, ls.vw)
	if err != nil {
		fail(w, err)
		return
	}
	ls.prefetchGatewayRows(pg.IDs)
	items := make([]GatewayListRow, 0, len(pg.IDs))
	for _, id := range pg.IDs {
		g := &ls.ix.Gateways[id]
		st := ls.ix.StateOf(g.Ref, ls.vw)
		row := GatewayListRow{Ref: ls.l.linked(g.Ref), Health: st, Reason: ls.ix.WorstOf(g.Ref, ls.vw).Reason(st), AttachedRoutes: g.AttachedRoutes,
			NotAccepted: ls.notAccepted(g), ListenerSets: ls.recount(g.ListenerSets, ls.lsRef), Disagreements: ls.ix.Disagreements(g.Ref, g.Disagreements, ls.vw),
			Addresses: []string{}}
		if v := g.View; v != nil {
			row.Class, row.Listeners = v.Class, len(v.Listeners)
			row.Addresses = append(row.Addresses, v.StatusAddresses...)
		}
		if n, ok := ls.memberCount(id, ls.ix.GatewayMemberCount, ls.ix.GatewayMemberCountAs, ls.ix.GatewayMemberCountIn); ok {
			row.Routes = &n
		}
		items = append(items, row)
	}
	writeJSON(w, page(ls, pg, items, ls.deniedInScope(len(ls.ix.Gateways), ls.gwRef)))
}

// memberCount is a routes column: rule 5's count, or in secure mode the
// visible members only (D-085); under a scope the members in it only.
func (ls *list) memberCount(id explore.RowID, all func(explore.RowID) (int, bool), as func(explore.RowID, func(objects.Ref) bool) (int, bool),
	in func(explore.RowID, func(objects.Ref) bool, []string) (int, bool)) (int, bool) {
	if ls.q.Scope != nil {
		return in(id, ls.v.visible(), ls.q.Scope)
	}
	if ls.v.secure {
		return as(id, ls.v.visible())
	}
	return all(id)
}

// prefetchGatewayRows resolves the buckets the page's Gateway rows read:
// their ListenerSets and not-accepted routes, then the ListenerSets that
// Under asks for the routes not denied (D-085).
func (ls *list) prefetchGatewayRows(ids []explore.RowID) {
	ix := ls.ix
	ls.v.prefetch(func(add func(objects.Ref)) {
		for _, id := range ids {
			g := &ix.Gateways[id]
			for _, id := range g.ListenerSets {
				ls.addInScope(add, ix.ListenerSets[id].Ref)
			}
			for _, id := range g.NotAccepted {
				ls.addInScope(add, ix.Routes[id].Ref)
			}
		}
	})
	ls.v.prefetch(func(add func(objects.Ref)) {
		for _, id := range ids {
			g := &ix.Gateways[id]
			for _, id := range g.NotAccepted {
				r := &ix.Routes[id]
				if !explore.InScope(ls.q.Scope, r.Ref.Namespace) || ls.v.denied(r.Ref) {
					continue
				}
				for _, p := range r.Parents {
					if p.Attachment == explore.AttachmentNotAccepted && p.Via != nil && *p.Via == g.Ref {
						add(p.Target)
					}
				}
			}
		}
	})
}

// notAccepted counts g's not-accepted routes the caller may view, through
// visible ListenerSets only (rule 3, rule 5).
func (ls *list) notAccepted(g *explore.GatewayRow) int {
	if ls.v.readsAll() && ls.q.Scope == nil {
		return len(g.NotAccepted)
	}
	n := 0
	for _, id := range g.NotAccepted {
		if ref := ls.ix.Routes[id].Ref; explore.InScope(ls.q.Scope, ref.Namespace) && ls.v.can(ref) && ls.ix.Under(id, g.Ref, explore.AttachmentNotAccepted, ls.v.can) {
			n++
		}
	}
	return n
}

func (h *handler) listenerSets(w http.ResponseWriter, r *http.Request) {
	ls := h.startList(w, r, listListenerSets)
	if ls == nil {
		return
	}
	ls.v.prefetch(func(add func(objects.Ref)) {
		for i := range ls.ix.ListenerSets {
			ls.addInScope(add, ls.ix.ListenerSets[i].Ref)
		}
	})
	pg, err := ls.ix.QueryListenerSetsAs(ls.q, ls.vw)
	if err != nil {
		fail(w, err)
		return
	}
	ls.v.prefetch(func(add func(objects.Ref)) {
		for _, id := range pg.IDs {
			add(ls.ix.ListenerSets[id].Parent)
		}
	})
	items := make([]ListenerSetRow, 0, len(pg.IDs))
	for _, id := range pg.IDs {
		s := &ls.ix.ListenerSets[id]
		st := ls.ix.ListenerSetStateOf(id, ls.vw)
		row := ListenerSetRow{Ref: ls.l.linked(s.Ref), Parent: ls.l.linked(s.Parent), Health: st, Reason: ls.ix.WorstOf(s.Ref, ls.vw).Reason(st), AcceptedReason: s.AcceptedReason,
			Disagreements: ls.ix.Disagreements(s.Ref, s.Disagreements, ls.vw)}
		if s.View != nil {
			row.Listeners = len(s.View.Listeners)
		}
		if n, ok := ls.memberCount(id, ls.ix.ListenerSetMemberCount, ls.ix.ListenerSetMemberCountAs, ls.ix.ListenerSetMemberCountIn); ok {
			row.Routes = &n
		}
		items = append(items, row)
	}
	writeJSON(w, page(ls, pg, items, ls.deniedInScope(len(ls.ix.ListenerSets), ls.lsRef)))
}
