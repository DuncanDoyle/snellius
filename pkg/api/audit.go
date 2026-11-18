package api

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
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

// auditUnmatched is the route of a request that reached no route of its own.
const auditUnmatched = "unmatched"

// AuditEvent is what the API knows about one answered /api/ request.
type AuditEvent struct {
	// Request is the request as its route saw it; Pattern is the route template, empty when no route ran.
	Request *http.Request
	// Route is the route template the path matches, or "unmatched"; never the path itself.
	Route string
	// Status is the answer's HTTP status.
	Status int
	Kind   AuditKind
	// Install is the install the path names, or empty.
	Install string
	// Subject is the object a read names: the path's object, or a finding's resource; nil for other routes.
	Subject *objects.Ref
	// Denied is set when the Authorizer refused Subject by name and the read answered 404.
	Denied bool
	// Filters are the filters a list's endpoint takes, each with its well-formed values joined by commas.
	Filters map[string]string
	// Returned are the typed object references in a successful response, with Subject; set only with disclosure.
	Returned []objects.Ref
}

// WithRequestAudit calls hear once for each /api/ request, after its answer is
// written. With disclosure, each successful answer reports the object
// references its response value holds; the body is never parsed.
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

// filterCheck accepts one filter value in its grammar and returns it as recorded.
type filterCheck func(v string) (string, bool)

var (
	kindFormat = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{0,62}$`)
	sortFormat = regexp.MustCompile(`^[a-z][A-Za-z]{0,31}(\.(asc|desc))?$`)
)

func oneOf(vs ...string) filterCheck {
	return func(v string) (string, bool) { return v, slices.Contains(vs, v) }
}

func validGroup(g string) bool { return g == coreGroup || len(validation.IsDNS1123Subdomain(g)) == 0 }

var (
	checkNamespace filterCheck = func(v string) (string, bool) { return v, len(validation.IsDNS1123Label(v)) == 0 }
	checkScope     filterCheck = func(v string) (string, bool) {
		names, err := parseScope(url.Values{scopeParam: {v}})
		return strings.Join(names, ","), err == nil
	}
	checkHealth             = oneOf(string(health.StateHealthy), string(health.StateWarning), string(health.StateError), string(health.StateUnknown))
	checkFlag   filterCheck = func(v string) (string, bool) { _, ok := explore.ParseFlag(v); return v, ok }
	checkAttach             = oneOf(string(explore.AttachmentAccepted), string(explore.AttachmentNotAccepted), string(explore.AttachmentPending),
		string(explore.AttachmentUnwatched), string(explore.AttachmentDeclared))
	checkKind      filterCheck = func(v string) (string, bool) { return v, kindFormat.MatchString(v) }
	checkGroupKind filterCheck = func(v string) (string, bool) {
		g, k, ok := strings.Cut(v, "/")
		return v, ok && validGroup(g) && kindFormat.MatchString(k)
	}
	checkRef filterCheck = func(v string) (string, bool) {
		s := strings.Split(v, "/")
		return v, len(s) == 4 && validGroup(s[0]) && kindFormat.MatchString(s[1]) &&
			(s[2] == "-" || len(validation.IsDNS1123Label(s[2])) == 0) && len(validation.IsDNS1123Subdomain(s[3])) == 0
	}
	checkListener filterCheck = func(v string) (string, bool) {
		return v, v == explore.ListenerUnknown || len(validation.IsDNS1123Subdomain(v)) == 0
	}
	checkSort filterCheck = func(v string) (string, bool) { return v, sortFormat.MatchString(v) }
)

// commonFilters are the filters every explore list takes.
var commonFilters = map[string]filterCheck{"namespace": checkNamespace, scopeParam: checkScope, "health": checkHealth, "sort": checkSort}

// with is base plus more.
func with(base, more map[string]filterCheck) map[string]filterCheck {
	out := maps.Clone(base)
	maps.Copy(out, more)
	return out
}

// auditFilters are the filters each list endpoint takes that a record may
// carry; free text (q), paging and limits are left out, and other endpoints carry none.
var auditFilters = map[string]map[string]filterCheck{
	"GET /api/v1/installs/{id}/routes": with(commonFilters, map[string]filterCheck{"kind": checkGroupKind, "parent": checkRef,
		"attachment": checkAttach, "listener": checkListener, "state": checkFlag, "backend": checkRef, "config": checkHealth, "path": checkHealth}),
	"GET /api/v1/installs/{id}/gateways":     commonFilters,
	"GET /api/v1/installs/{id}/listenersets": with(commonFilters, map[string]filterCheck{"parent": checkRef}),
	"GET /api/v1/installs/{id}/findings":     {"namespace": checkNamespace, scopeParam: checkScope, "kind": checkGroupKind, "sort": checkSort},
	"GET /api/v1/installs/{id}/processes":    {scopeParam: checkScope, "health": checkHealth},
	"GET /api/v1/installs/{id}/paths": {"namespace": checkNamespace, scopeParam: checkScope, "listener": checkListener, "backend": checkRef,
		"state": checkHealth, "kind": checkKind, "sort": checkSort},
	"GET /api/v1/installs/{id}/backends": {"namespace": checkNamespace, scopeParam: checkScope, "state": checkHealth, "kind": checkKind, "sort": checkSort},
}

// maxFilterValue bounds one filter's joined values in a record.
const maxFilterValue = 512

// filtersOf returns the filters r's endpoint takes, with the values that are
// well formed, or nil.
func filtersOf(r *http.Request) map[string]string {
	rules := auditFilters[r.Pattern]
	if rules == nil || r.URL.RawQuery == "" {
		return nil
	}
	vals, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil
	}
	var out map[string]string
	for name, check := range rules {
		var kept []string
		for _, v := range vals[name] {
			if c, ok := check(v); ok {
				kept = append(kept, c)
			}
		}
		if len(kept) == 0 {
			continue
		}
		v := strings.Join(kept, ",")
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
	// returned are the typed references of the response value; collected only with disclosure.
	returned []objects.Ref
}

// auditHolder keeps the note and request of a request's latest build.
type auditHolder struct {
	note     *auditNote
	req      *http.Request
	disclose bool
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

// noteWriter lets writeJSON report the response value of a disclosed build.
type noteWriter struct {
	http.ResponseWriter
	note *auditNote
}

// Unwrap lets http.ResponseController reach the connection.
func (w *noteWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// noteReturned records the typed references in v when w belongs to a disclosed build.
func noteReturned(w http.ResponseWriter, v any) {
	if nw, ok := w.(*noteWriter); ok {
		nw.note.returned = typedRefs(v)
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
		if h.disclose {
			w = &noteWriter{ResponseWriter: w, note: n}
		}
		next.ServeHTTP(w, nr)
		// The caller's request keeps the route template, as it would without this wrapper.
		r.Pattern = nr.Pattern
	})
}

// auditing calls hear after each /api/ request is answered. mux names the
// route of a request that no build reached.
func auditing(next http.Handler, mux *http.ServeMux, hear func(AuditEvent), disclosure bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		h := &auditHolder{disclose: disclosure}
		aw := &auditWriter{ResponseWriter: w}
		next.ServeHTTP(aw, r.WithContext(context.WithValue(r.Context(), holderKey{}, h)))
		hear(h.event(r, mux, aw))
	})
}

// event builds the audit event of the answered request; r is the request as it arrived.
func (h *auditHolder) event(r *http.Request, mux *http.ServeMux, aw *auditWriter) AuditEvent {
	status := aw.status
	if status == 0 {
		status = http.StatusOK
	}
	req := r
	if h.req != nil {
		req = h.req
	}
	ev := AuditEvent{Request: req, Route: routeOf(mux, req), Status: status, Kind: auditKind(req), Install: req.PathValue("id")}
	if n := h.note; n != nil && ev.Kind == AuditRead {
		ev.Subject = n.subject
		ev.Denied = n.denied && status == http.StatusNotFound
	}
	if ev.Kind == AuditList {
		ev.Filters = filtersOf(req)
	}
	if h.disclose && status < 300 {
		var refs []objects.Ref
		if h.note != nil {
			refs = h.note.returned
		}
		ev.Returned = returnedRefs(refs, ev.Subject)
	}
	return ev
}

// routeOf is r's route template, looked up in mux when no route ran, or "unmatched".
func routeOf(mux *http.ServeMux, r *http.Request) string {
	route := r.Pattern
	if route == "" && mux != nil {
		_, route = mux.Handler(r)
	}
	if route == "" || route == "/api/" {
		return auditUnmatched
	}
	return route
}

// withRoute is r with its Pattern set to its route template or "unmatched", for a hook that runs outside the mux.
func withRoute(mux *http.ServeMux, r *http.Request) *http.Request {
	if r.Pattern != "" && r.Pattern != "/api/" {
		return r
	}
	nr := r.WithContext(r.Context())
	nr.Pattern = routeOf(mux, r)
	return nr
}

// auditWriter records the status written through it.
type auditWriter struct {
	http.ResponseWriter
	status int
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
	return w.ResponseWriter.Write(p)
}

// Unwrap lets http.ResponseController reach the connection.
func (w *auditWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// maxReturned bounds the references one disclosure record lists.
const maxReturned = 1000

// returnedRefs lists refs and subject, sorted and once each, at most maxReturned.
func returnedRefs(refs []objects.Ref, subject *objects.Ref) []objects.Ref {
	out := slices.Clone(refs)
	if subject != nil {
		out = append(out, *subject)
	}
	slices.SortFunc(out, objects.Ref.Compare)
	out = slices.Compact(out)
	return out[:min(len(out), maxReturned)]
}

var refType = reflect.TypeFor[objects.Ref]()

// typedRefs returns the distinct objects.Ref values in v's JSON fields, up to
// maxReturned. Maps of untyped values are raw data, such as a stored object,
// and are never searched.
func typedRefs(v any) []objects.Ref {
	seen := map[objects.Ref]bool{}
	var out []objects.Ref
	var walk func(reflect.Value)
	walk = func(v reflect.Value) {
		if len(out) >= maxReturned {
			return
		}
		switch v.Kind() {
		case reflect.Pointer, reflect.Interface:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Struct:
			if v.Type() == refType {
				// Read field by field: a ref reached through an unexported embedding cannot be Interface()d.
				ref := objects.Ref{Group: v.FieldByName("Group").String(), Kind: v.FieldByName("Kind").String(),
					Namespace: v.FieldByName("Namespace").String(), Name: v.FieldByName("Name").String()}
				if ref.Kind != "" && ref.Name != "" && !seen[ref] {
					seen[ref] = true
					out = append(out, ref)
				}
				return
			}
			t := v.Type()
			for i := range t.NumField() {
				if f := t.Field(i); (f.IsExported() || f.Anonymous) && f.Tag.Get("json") != "-" {
					walk(v.Field(i))
				}
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Map:
			if v.Type().Elem().Kind() == reflect.Interface {
				return
			}
			for it := v.MapRange(); it.Next(); {
				walk(it.Value())
			}
		}
	}
	walk(reflect.ValueOf(v))
	return out
}
