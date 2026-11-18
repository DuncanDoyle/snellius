package health

// Row reason kinds (D-063).
const (
	// ReasonFinding: the row's own error or warning finding decides its state.
	ReasonFinding = "finding"
	// ReasonUnknown: a required check is not OK, which makes every row unknown.
	ReasonUnknown = "unknown"
	// ReasonGatewayRejecting: the row is warning only because its Gateway rejects config.
	ReasonGatewayRejecting = "gateway-rejecting"
	// ReasonBackendProblem: the route is warning only because a backend it sends to has a problem (D-082).
	ReasonBackendProblem = "backend-problem"
	// ReasonPathIncomplete: the viewer may not read everything the route's traffic path needs (design §8.1).
	ReasonPathIncomplete = "path-incomplete"
	// ReasonNotAttached: no parent accepted the route and it has no path rows (design §8.1).
	ReasonNotAttached = "not-attached"
	// ReasonNoPath: the route's status says accepted, yet the viewer has no path row of it,
	// such as when its Gateway lost the listener.
	ReasonNoPath = "no-path"
	// ReasonPathProblem: a signal on the route's path rows other than its Gateway's
	// rejection or a backend's, such as a listener condition; Code is the signal's code.
	ReasonPathProblem = "path-problem"
)

// RowReason says why a list or overview row is not healthy; healthy rows have
// none. Kind finding carries the deciding finding's code and message, never
// its detail or evidence, and More counts the object's other findings, all of
// which are of that severity or lower. Kind backend-problem carries the
// backend's deciding finding's code and message. The other kinds carry nothing else.
type RowReason struct {
	Kind    string `json:"kind"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
	More    int    `json:"more,omitempty"`
}
