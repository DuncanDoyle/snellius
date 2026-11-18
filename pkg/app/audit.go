package app

import (
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/session"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// requestAuditor turns each answered /api/ request into the records of the
// enabled levels (design §9.2): a denial, a read, a list summary, the
// references disclosed and the request itself. Secure mode only.
func requestAuditor(l *audit.Logger) func(api.AuditEvent) {
	return func(ev api.AuditEvent) {
		ok := ev.Status >= 200 && ev.Status < 300
		if l.Enabled(audit.LevelRequests) {
			rec := eventRecord(ev, audit.LevelRequests, "request")
			rec.Outcome, rec.Reason = "success", strconv.Itoa(ev.Status)
			if ev.Status >= 400 {
				rec.Outcome = "failure"
			}
			l.Emit(ev.Request.Context(), rec)
		}
		switch {
		case ev.Denied && l.Enabled(audit.LevelSecurity):
			rec := eventRecord(ev, audit.LevelSecurity, "denial")
			rec.Outcome, rec.Reason, rec.Refs = "failure", "forbidden", []string{refString(*ev.Subject)}
			l.Collapse(ev.Request.Context(), collapseKey(rec), rec)
		case ok && ev.Kind == api.AuditRead && l.Enabled(audit.LevelReads):
			rec := eventRecord(ev, audit.LevelReads, "read")
			rec.Outcome = "success"
			if ev.Subject != nil {
				rec.Refs = []string{refString(*ev.Subject)}
			}
			l.Emit(ev.Request.Context(), rec)
		case ok && ev.Kind == api.AuditList && l.Enabled(audit.LevelSummaries):
			rec := eventRecord(ev, audit.LevelSummaries, "list")
			rec.Outcome, rec.Request.Filters = "success", ev.Filters
			l.Summarize(collapseKey(rec), rec)
		}
		if ok && len(ev.Returned) > 0 && l.Enabled(audit.LevelDisclosure) {
			rec := eventRecord(ev, audit.LevelDisclosure, "disclosure")
			rec.Outcome = "success"
			for _, ref := range ev.Returned {
				rec.Refs = append(rec.Refs, refString(ref))
			}
			l.Emit(ev.Request.Context(), rec)
		}
	}
}

// eventRecord is the record of ev at level with action: the request, the
// install, and the session's person, Kubernetes identity and audit ID.
func eventRecord(ev api.AuditEvent, level, action string) audit.Record {
	rec := audit.FromRequest(ev.Request)
	if rec.Request.Route == "" {
		rec.Request.Route = ev.Request.URL.Path
	}
	rec.Level, rec.Action, rec.Install = level, action, ev.Install
	if info, ok := session.FromContext(ev.Request.Context()); ok {
		rec.User = audit.Identity{Issuer: info.Issuer, Subject: info.Subject}
		rec.Kube = audit.Kube{Username: info.Identity.Username, Groups: info.Identity.Groups}
		rec.Session = info.AuditID
	}
	return rec
}

// collapseKey is a record's window key: session, method, route, install, refs and filters.
func collapseKey(rec audit.Record) string {
	parts := []string{rec.Session, rec.Request.Method, rec.Request.Route, rec.Install, strings.Join(rec.Refs, " ")}
	for _, k := range slices.Sorted(maps.Keys(rec.Request.Filters)) {
		parts = append(parts, k+"="+rec.Request.Filters[k])
	}
	return strings.Join(parts, "\x00")
}

// refString writes ref as a ref parameter does: "core" for the empty group, "-" for no namespace.
func refString(ref objects.Ref) string {
	g, ns := ref.Group, ref.Namespace
	if g == "" {
		g = "core"
	}
	if ns == "" {
		ns = "-"
	}
	return g + "/" + ref.Kind + "/" + ns + "/" + ref.Name
}

// serverName names this server instance in audit records: the host name, which is the pod's name in a cluster.
func serverName() string {
	name, _ := os.Hostname()
	return name
}
