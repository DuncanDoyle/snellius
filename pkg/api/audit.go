package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// AuditKind is how the audit stream treats an /api/ route (design §9.2).
type AuditKind uint8

const (
	// AuditOther is a route that is neither a read nor a list; only the
	// requests and disclosure levels record it.
	AuditOther AuditKind = iota
	// AuditRead is a detail or YAML read.
	AuditRead
	// AuditList is list or overview access, recorded as a summary.
	AuditList
)

// AuditEvent is what the API knows about one answered /api/ request.
type AuditEvent struct {
	// Request is the request as its route saw it; Pattern is the route template, empty when no route ran.
	Request *http.Request
	// Status is the answer's HTTP status.
	Status int
	Kind   AuditKind
	// Install is the install the path names, or empty.
	Install string
	// Subject is the object a read's path names; nil for other routes.
	Subject *objects.Ref
	// Denied is set when the Authorizer refused Subject by name and the read answered 404.
	Denied bool
	// Filters are a list's allow-listed filter names, each with its values joined by commas.
	Filters map[string]string
	// Returned are the object references in a successful response; set only with disclosure.
	Returned []objects.Ref
}

// WithRequestAudit calls hear once for each /api/ request, after its answer is
// written. With disclosure, each successful answer is scanned for the object
// references it returned; without it, the body is never looked at.
func WithRequestAudit(hear func(AuditEvent), disclosure bool) HandlerOption {
	return func(c *handlerConfig) { c.audit, c.disclosure = hear, disclosure }
}

// auditRoutes classifies the core routes; others are AuditOther, extension routes aside.
var auditRoutes = map[string]AuditKind{
	"GET /api/v1/installs":                                                   AuditList,
	"GET /api/v1/installs/{id}/overview":                                     AuditList,
	"GET /api/v1/installs/{id}/health":                                       AuditList,
	"GET /api/v1/installs/{id}/findings":                                     AuditList,
	"GET /api/v1/installs/{id}/diagnostics":                                  AuditList,
	"GET /api/v1/installs/{id}/processes":                                    AuditList,
	"GET /api/v1/installs/{id}/routes":                                       AuditList,
	"GET /api/v1/installs/{id}/gateways":                                     AuditList,
	"GET /api/v1/installs/{id}/listenersets":                                 AuditList,
	"GET /api/v1/installs/{id}/namespaces":                                   AuditList,
	"GET /api/v1/installs/{id}/paths":                                        AuditList,
	"GET /api/v1/installs/{id}/backends":                                     AuditList,
	"GET /api/v1/installs/{id}/findings/{findingId}":                         AuditRead,
	"GET /api/v1/installs/{id}/gateways/{ns}/{name}":                         AuditRead,
	"GET /api/v1/installs/{id}/listenersets/{group}/{kind}/{ns}/{name}":      AuditRead,
	"GET /api/v1/installs/{id}/routes/{group}/{kind}/{ns}/{name}":            AuditRead,
	"GET /api/v1/installs/{id}/routes/{group}/{kind}/{ns}/{name}/delegation": AuditRead,
	"GET /api/v1/installs/{id}/resources/{group}/{kind}/{rns}/{rname}":       AuditRead,
	"GET /api/v1/installs/{id}/backends/{group}/{kind}/{ns}/{name}":          AuditRead,
}

// auditKind classifies r's route. An extension GET is a list: its shape is unknown.
func auditKind(r *http.Request) AuditKind {
	if k, ok := auditRoutes[r.Pattern]; ok {
		return k
	}
	_, path, found := strings.Cut(r.Pattern, " ")
	if !found {
		path = r.Pattern
	}
	if strings.HasPrefix(path, "/api/ext/") && (r.Method == http.MethodGet || r.Method == http.MethodHead) {
		return AuditList
	}
	return AuditOther
}

// auditFilters are the list parameters an audit record may carry, with their
// values; free text (q), paging and limits are left out.
var auditFilters = []string{"namespace", scopeParam, "health", "kind", "parent", "attachment", "listener", "state", "backend", "config", "path", "sort"}

// maxFilterValue bounds one filter's joined values in a record.
const maxFilterValue = 512

// filtersOf returns the allow-listed filters of r's query, or nil.
func filtersOf(r *http.Request) map[string]string {
	if r.URL.RawQuery == "" {
		return nil
	}
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil
	}
	var out map[string]string
	for _, name := range auditFilters {
		vs, ok := vals[name]
		if !ok {
			continue
		}
		v := strings.Join(vs, ",")
		if len(v) > maxFilterValue {
			v = strings.ToValidUTF8(v[:maxFilterValue], "")
		}
		if out == nil {
			out = map[string]string{}
		}
		out[name] = v
	}
	return out
}

// auditNote is what one build of a request learnt for its audit event.
type auditNote struct {
	subject *objects.Ref
	denied  bool
}

// auditHolder keeps the note and request of a request's latest build.
type auditHolder struct {
	note *auditNote
	req  *http.Request
}

type (
	holderKey struct{}
	noteKey   struct{}
)

// noteOf returns the audit note of r's build, or nil when nothing listens.
func noteOf(r *http.Request) *auditNote {
	if r == nil {
		return nil
	}
	n, _ := r.Context().Value(noteKey{}).(*auditNote)
	return n
}

// noteSubject records ref as the object r's read names.
func noteSubject(r *http.Request, ref objects.Ref) {
	if n := noteOf(r); n != nil {
		n.subject = &ref
	}
}

// deniedNamed records that the Authorizer refused ref by name.
func (n *auditNote) deniedNamed(ref objects.Ref) {
	if n != nil && n.subject != nil && *n.subject == ref {
		n.denied = true
	}
}

// noteBuilds gives each build of an audited request a fresh note.
func noteBuilds(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, _ := r.Context().Value(holderKey{}).(*auditHolder)
		if h == nil {
			next.ServeHTTP(w, r)
			return
		}
		n := &auditNote{}
		nr := r.WithContext(context.WithValue(r.Context(), noteKey{}, n))
		h.note, h.req = n, nr
		next.ServeHTTP(w, nr)
		// The caller's request keeps the route template, as it would without this wrapper.
		r.Pattern = nr.Pattern
	})
}

// auditing calls hear after each /api/ request is answered; disclosure keeps
// a copy of the body to scan.
func auditing(next http.Handler, hear func(AuditEvent), disclosure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		h := &auditHolder{}
		aw := &auditWriter{ResponseWriter: w, keep: disclosure}
		next.ServeHTTP(aw, r.WithContext(context.WithValue(r.Context(), holderKey{}, h)))
		hear(h.event(r, aw))
	})
}

// event builds the audit event of the answered request; r is the request as it arrived.
func (h *auditHolder) event(r *http.Request, aw *auditWriter) AuditEvent {
	status := aw.status
	if status == 0 {
		status = http.StatusOK
	}
	req := r
	if h.req != nil {
		req = h.req
	}
	ev := AuditEvent{Request: req, Status: status, Kind: auditKind(req), Install: req.PathValue("id")}
	if n := h.note; n != nil && ev.Kind == AuditRead {
		ev.Subject = n.subject
		ev.Denied = n.denied && status == http.StatusNotFound
	}
	if ev.Kind == AuditList {
		ev.Filters = filtersOf(req)
	}
	if aw.keep && status < 300 {
		ev.Returned = returnedRefs(aw.body.Bytes(), ev.Subject)
	}
	return ev
}

// auditWriter records the status written through it and, when keep is set, the body.
type auditWriter struct {
	http.ResponseWriter
	status int
	keep   bool
	body   bytes.Buffer
}

func (w *auditWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *auditWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.keep {
		w.body.Write(p)
	}
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// maxReturned bounds the references one disclosure record lists.
const maxReturned = 1000

// returnedRefs lists, sorted and once each, the object references in a JSON
// body: every object with string group, kind and name, and subject.
func returnedRefs(body []byte, subject *objects.Ref) []objects.Ref {
	seen := map[objects.Ref]bool{}
	if subject != nil {
		seen[*subject] = true
	}
	var doc any
	if json.Unmarshal(body, &doc) == nil {
		walkRefs(doc, seen)
	}
	out := make([]objects.Ref, 0, len(seen))
	for ref := range seen {
		out = append(out, ref)
	}
	slices.SortFunc(out, objects.Ref.Compare)
	if len(out) > maxReturned {
		out = out[:maxReturned]
	}
	return out
}

func walkRefs(v any, seen map[objects.Ref]bool) {
	switch x := v.(type) {
	case map[string]any:
		g, okG := x["group"].(string)
		k, okK := x["kind"].(string)
		n, okN := x["name"].(string)
		if okG && okK && okN && k != "" && n != "" {
			ns, _ := x["namespace"].(string)
			seen[objects.Ref{Group: g, Kind: k, Namespace: ns, Name: n}] = true
		}
		for _, c := range x {
			walkRefs(c, seen)
		}
	case []any:
		for _, c := range x {
			walkRefs(c, seen)
		}
	}
}
