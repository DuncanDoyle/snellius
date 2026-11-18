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

// RouteSignals are route row id's configuration and traffic path for vw
// (design §8.1). Configuration is the worst shown finding on the route
// itself. The path is the worst shown fault of a parent Gateway rejecting
// config or a non-mirror backend, the install-wide unknown, and unknown when
// vw.Perms leave it incomplete. backend is the backend that decides a
// backend-problem path reason, else nil.
func (ix *Index) RouteSignals(id RowID, vw Viewer) (config, path Signal, backend *objects.Ref) {
	return ix.signals(&ix.Routes[id], vw)
}

// ObjectSignals are RouteSignals for route u without a row: no Gateway or
// backend fault reaches it, so only the install-wide unknown and
// incompleteness move its path.
func (ix *Index) ObjectSignals(u *unstructured.Unstructured, vw Viewer) (config, path Signal) {
	config, path, _ = ix.signals(&RouteRow{Ref: objects.RefOf(u), Obj: u}, vw)
	return config, path
}

// signals is RouteSignals for route r.
func (ix *Index) signals(r *RouteRow, vw Viewer) (config, path Signal, backend *objects.Ref) {
	unknown := ix.unknown
	if vw.Shown != nil {
		unknown = vw.Unknown
	}
	w := ix.WorstOf(r.Ref, vw)
	config.State = w.State(unknown)
	config.Reason = w.Reason(config.State)
	// The first most severe fault decides, Gateways before backends as on the legacy reason.
	var fault *health.Finding
	kind := ""
	for _, g := range r.rejectedBy {
		if f := ix.shownFinding(g, health.CodeXDSRejected, vw); f != nil && worse(f, fault) {
			fault, kind, backend = f, health.ReasonGatewayRejecting, nil
		}
	}
	for k := range r.problemBackends {
		bw := ix.WorstOf(r.problemBackends[k], vw)
		f := bw.Error
		if f == nil {
			f = bw.Warning
		}
		if f != nil && worse(f, fault) {
			fault, kind, backend = f, health.ReasonBackendProblem, &r.problemBackends[k]
		}
	}
	path.Incomplete = vw.Perms != nil && ix.routeIncomplete(r, vw.Perms)
	// The row rule of Worst.State: error, then unknown, then warning.
	switch {
	case fault != nil && fault.Severity == health.SeverityError:
		path.State = health.StateError
	case unknown:
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonUnknown}
		fault, backend = nil, nil
	case fault != nil:
		path.State = health.StateWarning
	case path.Incomplete:
		path.State, path.Reason = health.StateUnknown, &health.RowReason{Kind: health.ReasonPathIncomplete}
	default:
		path.State = health.StateHealthy
	}
	if fault != nil {
		path.Reason = &health.RowReason{Kind: kind}
		if kind == health.ReasonBackendProblem {
			path.Reason.Code, path.Reason.Message = fault.Code, fault.Message
		}
	}
	return config, path, backend
}

// worse reports whether f is more severe than cur, an error over a warning or anything over nil.
func worse(f, cur *health.Finding) bool {
	return cur == nil || f.Severity == health.SeverityError && cur.Severity != health.SeverityError
}

// summary is the legacy health of route row id for vw (design §8.3): st,
// raised from healthy to unknown when vw.Perms leave the path incomplete.
func (ix *Index) summary(id RowID, vw Viewer, st health.State, r *health.RowReason) (health.State, *health.RowReason) {
	if st != health.StateHealthy || vw.Perms == nil || !ix.PathIncomplete(id, vw.Perms) {
		return st, r
	}
	return health.StateUnknown, &health.RowReason{Kind: health.ReasonPathIncomplete}
}
