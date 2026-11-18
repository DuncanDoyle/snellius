package api

import (
	"cmp"

	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/paths"
)

// routePaths reads routes' traffic paths from the install's path rows
// (design §8.1) exactly as /paths shows them to the caller: the memoised
// topology, its projection for the caller over the covered routes' rows,
// and only the rows the caller may see. One request uses it.
type routePaths struct {
	v       *view
	i       *engine.InstallModel
	ix      *explore.Index
	t       *paths.Topology
	proj    *paths.Projection
	visible func(paths.Row) bool
	// deciding is each answered route's deciding signal, for its message.
	deciding map[explore.RowID]paths.Signal
	titles   map[titleKey]string
	done     map[explore.RowID]explore.PathRows
}

// routePaths is the path reader of install i for the caller of v over the
// routes covers admits; covers runs on route refs before any check. Nil
// without computed views, which derive no path rows: the index decides then.
func (h *handler) routePaths(p *engine.Published, i *engine.InstallModel, ix *explore.Index, v *view, covers func(objects.Ref) bool) *routePaths {
	if i.Computed == nil {
		return nil
	}
	t, proj, _ := h.pathModel(p, i, v)
	pick := func(r paths.Row) bool { return t.ChainHolds(r, covers) }
	prefetchRows(v, t, pick)
	// The target a route writes is read by name, as the route detail's trace reads it (D-100 item 1).
	visible := func(r paths.Row) bool { return pick(r) && v.allowTableNamed(t, t.Deps(r), writtenTarget(t, r)) }
	proj = proj.For(t, v.projectInput(p, i), v.scopedFilter(i, t, visible))
	return &routePaths{v: v, i: i, ix: ix, t: t, proj: proj, visible: visible,
		deciding: map[explore.RowID]paths.Signal{}, done: map[explore.RowID]explore.PathRows{}}
}

// pathFunc is rp's path for an explore.Viewer; nil for a nil rp.
func (rp *routePaths) pathFunc() func(explore.RowID) explore.PathRows {
	if rp == nil {
		return nil
	}
	return rp.path
}

// path is route row id's path rows: the worst state over the rows the caller
// may see and the first signal at that state as the reason.
func (rp *routePaths) path(id explore.RowID) explore.PathRows {
	if out, ok := rp.done[id]; ok {
		return out
	}
	out := rp.read(id)
	rp.done[id] = out
	return out
}

// read is path without the memo.
func (rp *routePaths) read(id explore.RowID) explore.PathRows {
	t := rp.t
	out := explore.PathRows{State: health.StateHealthy}
	var dec paths.Signal
	var decRow paths.Row
	found := false
	for _, k := range t.RowsOf(rp.ix.Routes[id].Ref) {
		r := t.Rows[k]
		if !rp.visible(r) {
			continue
		}
		out.Rows++
		pr := rp.proj.Problems[k]
		if problemRank(pr.State) >= problemRank(out.State) {
			continue
		}
		out.State, found = pr.State, false
		for _, s := range pr.Signals {
			if s.State == pr.State {
				dec, decRow, found = s, r, true
				break
			}
		}
	}
	if out.State == health.StateHealthy || !found {
		return out
	}
	rp.deciding[id] = dec
	// A signal without a code, such as waiting for status, is named by its kind.
	code := cmp.Or(dec.Code, dec.Kind)
	switch target := writtenTarget(t, decRow); {
	case dec.Kind == paths.SignalFinding && dec.Code == health.CodeXDSRejected:
		out.Reason = &health.RowReason{Kind: health.ReasonGatewayRejecting}
	case target != nil && dec.Ref == *target:
		out.Reason, out.Backend = &health.RowReason{Kind: health.ReasonBackendProblem, Code: code}, target
	default:
		out.Reason = &health.RowReason{Kind: health.ReasonPathProblem, Code: code}
	}
	return out
}

// message is the shown finding message of route row id's deciding signal, or
// "". Titles are read once for every route answered so far.
func (rp *routePaths) message(id explore.RowID) string {
	if rp == nil {
		return ""
	}
	s, ok := rp.deciding[id]
	if !ok || s.Kind != paths.SignalFinding {
		return ""
	}
	if _, ok := rp.titles[titleKey{s.Code, s.Ref}]; !ok {
		sets := make([][]paths.Signal, 0, len(rp.deciding))
		for _, d := range rp.deciding {
			sets = append(sets, []paths.Signal{d})
		}
		rp.titles = signalTitles(rp.v, rp.i, sets...)
	}
	return rp.titles[titleKey{s.Code, s.Ref}]
}
