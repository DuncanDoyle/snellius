package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"testing/fstest"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// A record carries only the filters its endpoint takes, and only values in
// each filter's grammar: free text, paging, unknown names and stray values stay out.
func TestAuditFiltersPerEndpoint(t *testing.T) {
	for _, c := range []struct {
		route, query string
		want         map[string]string
	}{
		{"GET /api/v1/installs/{id}/routes", "namespace=a&namespace=b&namespace=Tok_Secret&health=error&health=tok-secret&q=secret&cursor=c1&limit=5" +
			"&token=t1&parent=core/Service/shop/web&backend=g/K/ns/Tok.Secret&state=pending&state=tok&listener=_unknown&attachment=accepted" +
			"&kind=gateway.networking.k8s.io/HTTPRoute&sort=name.desc&namespaces=b,a&path=error&config=bogus",
			map[string]string{"namespace": "a,b", "health": "error", "parent": "core/Service/shop/web", "state": "pending", "listener": "_unknown",
				"attachment": "accepted", "kind": "gateway.networking.k8s.io/HTTPRoute", "sort": "name.desc", "namespaces": "a,b", "path": "error"}},
		{"GET /api/v1/installs", "path=tok-secret&namespace=infra", nil},
		{"GET /api/v1/installs/{id}/overview", "namespace=infra", nil},
		{"GET /api/v1/installs/{id}/findings", "health=error&namespace=infra&path=error&kind=core/Service", map[string]string{"namespace": "infra", "kind": "core/Service"}},
		{"GET /api/v1/installs/{id}/paths", "kind=HTTPRoute&state=warning&path=error&backend=core/Service/a/b", map[string]string{"kind": "HTTPRoute", "state": "warning", "backend": "core/Service/a/b"}},
		{"GET /api/v1/installs/{id}/backends", "kind=core/Service&namespaces=a,Tok", nil},
		{"/api/ext/acme/list", "namespace=infra", nil},
	} {
		r := httptest.NewRequest("GET", "/api/x?"+c.query, nil)
		r.Pattern = c.route
		if got := filtersOf(r); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: filters %v, want %v", c.route, got, c.want)
		}
	}
}

// Requests that never reached a route, or reached none, record "unmatched", never their path.
func TestAuditUnmatchedRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/installs/{id}/gateways/{ns}/{name}", func(w http.ResponseWriter, _ *http.Request) {})
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNotFound) })
	refused := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	for _, c := range []struct {
		next       http.Handler
		path, want string
	}{
		{noteBuilds(mux), "/api/v1/tok-secret", auditUnmatched},
		{refused, "/api/v1/tok-secret", auditUnmatched},
		{refused, "/api/v1/installs/i/gateways/n/x", "GET /api/v1/installs/{id}/gateways/{ns}/{name}"},
		{noteBuilds(mux), "/api/v1/installs/i/gateways/n/x", "GET /api/v1/installs/{id}/gateways/{ns}/{name}"},
	} {
		var got AuditEvent
		auditing(c.next, mux, func(ev AuditEvent) { got = ev }, false).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", c.path, nil))
		if got.Route != c.want {
			t.Errorf("%s: route %q, want %q", c.path, got.Route, c.want)
		}
	}
}

// refsDTO mixes typed references with raw data shaped like one.
type refsDTO struct {
	Ref      LinkedRef      `json:"ref"`
	Items    []LinkedRef    `json:"items"`
	Missing  *LinkedRef     `json:"missing,omitempty"`
	Hidden   objects.Ref    `json:"-"`
	Raw      map[string]any `json:"raw"`
	Anything any            `json:"anything"`
}

// Disclosure lists the typed references a response holds, once each and
// sorted, with the read's subject; raw maps shaped like a reference are not one.
func TestReturnedRefs(t *testing.T) {
	x := objects.Ref{Group: "g", Kind: "K", Namespace: "b", Name: "x"}
	y := objects.Ref{Group: "g", Kind: "K", Namespace: "a", Name: "y"}
	raw := map[string]any{"group": "g", "kind": "Secret", "name": "tok-secret"}
	v := refsDTO{Ref: LinkedRef{Ref: x}, Items: []LinkedRef{{Ref: y}, {Ref: x}}, Hidden: objects.Ref{Kind: "K", Name: "hidden"},
		Raw: map[string]any{"metadata": map[string]any{"annotations": raw}}, Anything: raw}
	subject := objects.Ref{Kind: "Service", Namespace: "a", Name: "s"}
	got := returnedRefs(typedRefs(v), &subject)
	want := []objects.Ref{{Kind: "Service", Namespace: "a", Name: "s"}, y, x}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}
	many := make([]LinkedRef, 3*maxReturned)
	for i := range many {
		many[i].Ref = objects.Ref{Kind: "K", Namespace: "n", Name: strconv.Itoa(i)}
	}
	if n := len(typedRefs(many)); n != maxReturned {
		t.Fatalf("%d refs collected, want the cap %d", n, maxReturned)
	}
}

// A finding detail read carries the finding's resource as its subject.
func TestAuditFindingDetailSubject(t *testing.T) {
	e, srv := findingsWorld(t, nil)
	rows := get[Page[FindingRow]](t, srv, findingsBase, http.StatusOK)
	var row *FindingRow
	for i := range rows.Items {
		if rows.Items[i].Resource != nil {
			row = &rows.Items[i]
			break
		}
	}
	if row == nil {
		t.Fatal("no finding with a resource")
	}
	var got AuditEvent
	h := NewHandler(e, fstest.MapFS{}, nil, WithRequestAudit(func(ev AuditEvent) { got = ev }, true))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", findingsBase+"/"+row.ID, nil))
	if rec.Code != http.StatusOK || got.Kind != AuditRead || got.Subject == nil || *got.Subject != row.Resource.Ref {
		t.Fatalf("status %d, event %+v, want subject %v", rec.Code, got, row.Resource.Ref)
	}
	if !slices.Contains(got.Returned, row.Resource.Ref) {
		t.Fatalf("returned %v lacks %v", got.Returned, row.Resource.Ref)
	}
}

// The audited request carries the route template and kind, extension GETs are
// lists, and references are collected only for disclosure.
func TestAuditingEvent(t *testing.T) {
	mux := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noteSubject(r, objects.Ref{Kind: "K", Namespace: "n", Name: r.PathValue("name")})
		writeJSON(w, LinkedRef{Ref: objects.Ref{Kind: "K", Namespace: "n", Name: "x"}})
	})
	mux.Handle("GET /api/v1/installs/{id}/gateways/{ns}/{name}", ok)
	mux.Handle("/api/ext/acme/hi", ok)
	for _, c := range []struct {
		method, path, route string
		kind                AuditKind
		disclosure          bool
	}{
		{"GET", "/api/v1/installs/i1/gateways/n/x", "GET /api/v1/installs/{id}/gateways/{ns}/{name}", AuditRead, true},
		{"GET", "/api/ext/acme/hi", "/api/ext/acme/hi", AuditList, false},
		{"POST", "/api/ext/acme/hi", "/api/ext/acme/hi", AuditOther, false},
	} {
		var got AuditEvent
		h := auditing(noteBuilds(mux), mux, func(ev AuditEvent) { got = ev }, c.disclosure)
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
		if got.Request.Pattern != c.route || got.Kind != c.kind || got.Status != http.StatusOK {
			t.Errorf("%s %s: route %q kind %v status %d", c.method, c.path, got.Request.Pattern, got.Kind, got.Status)
		}
		if c.kind == AuditRead && (got.Install != "i1" || got.Subject == nil || got.Subject.Name != "x" || got.Denied) {
			t.Errorf("read event %+v", got)
		}
		if (len(got.Returned) > 0) != c.disclosure {
			t.Errorf("%s %s: returned %v with disclosure %v", c.method, c.path, got.Returned, c.disclosure)
		}
	}
}

// A refused subject is a denial only when the read answered 404; another ref refused is none.
func TestAuditDeniedSubject(t *testing.T) {
	subject := objects.Ref{Kind: "K", Namespace: "n", Name: "x"}
	for _, c := range []struct {
		refused objects.Ref
		code    int
		want    bool
	}{
		{subject, http.StatusNotFound, true},
		{subject, http.StatusOK, false},
		{objects.Ref{Kind: "K", Namespace: "n", Name: "other"}, http.StatusNotFound, false},
	} {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /api/v1/installs/{id}/gateways/{ns}/{name}", func(w http.ResponseWriter, r *http.Request) {
			noteSubject(r, subject)
			v := newView(r, AuthorizerFunc(func(*http.Request, objects.Ref) bool { return false }), true)
			v.canNamed(c.refused)
			w.WriteHeader(c.code)
		})
		var got AuditEvent
		auditing(noteBuilds(mux), mux, func(ev AuditEvent) { got = ev }, false).ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest("GET", "/api/v1/installs/i/gateways/n/x", nil))
		if got.Denied != c.want {
			t.Errorf("refused %v, status %d: denied %v, want %v", c.refused, c.code, got.Denied, c.want)
		}
	}
}
