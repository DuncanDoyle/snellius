package explore

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Signal is one of a route's two signals (design §8.1, D-087): its state and
// why it is not healthy. Incomplete is set on a path the viewer's
// permissions leave unassessable.
type Signal struct {
	State      health.State
	Reason     *health.RowReason
	Incomplete bool
}

// PathRows is a route's traffic path read from its path rows (design §8.1):
// the worst state over the rows the viewer may read, the reason that state
// gives and the backend the reason names. Rows counts those rows.
type PathRows struct {
	State   health.State
	Reason  *health.RowReason
	Backend *objects.Ref
	Rows    int
}

// RouteSignals are route row id's configuration and traffic path for vw
// (design §8.1). Configuration is the worst shown finding on the route
// itself. The path is vw.Path's rows, or without it the index's rejecting
// Gateways and backend faults; see path for the order. backend is the
// backend a backend-problem path reason names, else nil.
func (ix *Index) RouteSignals(id RowID, vw Viewer) (config, path Signal, backend *objects.Ref) {
	r := &ix.Routes[id]
	pr := ix.indexPath(r, vw)
	if vw.Path != nil {
		pr = vw.Path(id)
	}
	return ix.signals(r, vw, pr)
}

// ObjectSignals are RouteSignals for route u without a row, which has no
// path rows: only incompleteness and the install-wide unknown move its path.
func (ix *Index) ObjectSignals(u *unstructured.Unstructured, vw Viewer) (config, path Signal) {
	config, path, _ = ix.signals(&RouteRow{Ref: objects.RefOf(u), Obj: u}, vw, PathRows{State: health.StateHealthy})
	return config, path
}

// signals composes route r's configuration and its path from pr. A readable
// error or warning decides the path, incomplete or not; then an incomplete
// path is unknown, then an unknown row, the install-wide unknown; a route
// without rows is not attached when no parent accepted it, else it has no
// path, unless it writes no rule (design §8.1).
func (ix *Index) signals(r *RouteRow, vw Viewer, pr PathRows) (config, path Signal, backend *objects.Ref) {
	unknown := ix.unknown
	if vw.Shown != nil {
		unknown = vw.Unknown
	}
	config = ix.config(r.Ref, vw)
	path.Incomplete = ix.incomplete(r, vw)
	switch {
	case pr.State == health.StateError || pr.State == health.StateWarning:
		path.State, path.Reason, backend = pr.State, pr.Reason, pr.Backend
	case path.Incomplete:
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonPathIncomplete}
	case pr.State == health.StateUnknown:
		path.State, path.Reason = health.StateUnknown, pr.Reason
	case unknown:
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonUnknown}
	case pr.Rows == 0 && r.Flags&FlagAccepted == 0:
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonNotAttached}
	case pr.Rows == 0 && hasRules(r):
		// Accepted, yet no path row: say so, never healthy and never a permission gap.
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonNoPath}
	default:
		path.State = health.StateHealthy
	}
	return config, path, backend
}

// hasRules reports whether route r writes any rule; a route without one has no path rows by design.
func hasRules(r *RouteRow) bool {
	if r.View != nil {
		return len(r.View.Rules) > 0
	}
	return r.Obj != nil && len(objects.DecodeRouteRules(r.Obj)) > 0
}

// config is route ref's configuration signal for vw: its own shown findings.
func (ix *Index) config(ref objects.Ref, vw Viewer) Signal {
	unknown := ix.unknown
	if vw.Shown != nil {
		unknown = vw.Unknown
	}
	w := ix.WorstOf(ref, vw)
	st := w.State(unknown)
	return Signal{State: st, Reason: w.Reason(st)}
}

// incomplete reports whether vw cannot assess route r's whole path: its
// permissions leave a ref unreadable, or it gets no delegation analysis
// (D-114) and r names a parent route. Both are permissions alone.
func (ix *Index) incomplete(r *RouteRow, vw Viewer) bool {
	return vw.Perms != nil && (ix.routeIncomplete(r, vw.Perms) || vw.NoDelegation && len(ix.routeData(r).routeParents) > 0)
}

// indexPath is route r's path from the index alone, for a viewer without
// path rows: the first most severe shown fault of a rejecting parent
// Gateway or a non-mirror backend; one row when any parent accepted it.
func (ix *Index) indexPath(r *RouteRow, vw Viewer) PathRows {
	out := PathRows{State: health.StateHealthy}
	if r.Flags&FlagAccepted != 0 {
		out.Rows = 1
	}
	var fault *health.Finding
	kind := ""
	for _, g := range r.rejectedBy {
		if f := ix.shownFinding(g, health.CodeXDSRejected, vw); f != nil && worse(f, fault) {
			fault, kind, out.Backend = f, health.ReasonGatewayRejecting, nil
		}
	}
	for k := range r.problemBackends {
		bw := ix.WorstOf(r.problemBackends[k], vw)
		f := bw.Error
		if f == nil {
			f = bw.Warning
		}
		if f != nil && worse(f, fault) {
			fault, kind, out.Backend = f, health.ReasonBackendProblem, &r.problemBackends[k]
		}
	}
	if fault == nil {
		return out
	}
	out.State, out.Reason = health.StateWarning, &health.RowReason{Kind: kind}
	if fault.Severity == health.SeverityError {
		out.State = health.StateError
	}
	if kind == health.ReasonBackendProblem {
		out.Reason.Code, out.Reason.Message = fault.Code, fault.Message
	}
	return out
}

// worse reports whether f is more severe than cur, an error over a warning or anything over nil.
func worse(f, cur *health.Finding) bool {
	return cur == nil || f.Severity == health.SeverityError && cur.Severity != health.SeverityError
}

// summary is the legacy health of route row id for vw (design §8.3): st,
// raised from healthy to unknown when vw cannot assess the whole path.
func (ix *Index) summary(id RowID, vw Viewer, st health.State, r *health.RowReason) (health.State, *health.RowReason) {
	if st != health.StateHealthy || !ix.incomplete(&ix.Routes[id], vw) {
		return st, r
	}
	return health.StateUnknown, &health.RowReason{Kind: health.ReasonPathIncomplete}
}
