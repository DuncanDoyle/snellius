package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Only allow-listed filters reach a record: free text, paging and unknown names stay out.
func TestAuditFiltersAllowListed(t *testing.T) {
	long := strings.Repeat("n", maxFilterValue+10)
	r := httptest.NewRequest("GET", "/api/v1/installs/x/routes?namespace=a&namespace=b&health=error&q=secret&cursor=c1&limit=5&token=t1&parent="+long, nil)
	got := filtersOf(r)
	want := map[string]string{"namespace": "a,b", "health": "error", "parent": long[:maxFilterValue]}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filters %v, want %v", got, want)
	}
	if f := filtersOf(httptest.NewRequest("GET", "/api/v1/installs?q=x", nil)); f != nil {
		t.Fatalf("filters %v, want none", f)
	}
}

// Disclosure lists each object reference in a body once, sorted, with the read's subject.
func TestReturnedRefs(t *testing.T) {
	body := `{"items":[{"ref":{"group":"g","kind":"K","namespace":"b","name":"x"},"link":"detail"},` +
		`{"group":"g","kind":"K","namespace":"a","name":"y"},{"group":"g","kind":"K","namespace":"b","name":"x"}],"kind":"NotARef"}`
	subject := objects.Ref{Kind: "Service", Namespace: "a", Name: "s"}
	got := returnedRefs([]byte(body), &subject)
	want := []objects.Ref{{Kind: "Service", Namespace: "a", Name: "s"}, {Group: "g", Kind: "K", Namespace: "a", Name: "y"}, {Group: "g", Kind: "K", Namespace: "b", Name: "x"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("refs %v, want %v", got, want)
	}
}

// The audited request carries the route template and kind, extension GETs are
// lists, and the body is kept only for disclosure.
func TestAuditingEvent(t *testing.T) {
	mux := http.NewServeMux()
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		noteSubject(r, objects.Ref{Kind: "K", Namespace: "n", Name: r.PathValue("name")})
		_, _ = w.Write([]byte(`{"group":"","kind":"K","namespace":"n","name":"x"}`))
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
		h := auditing(noteBuilds(mux), func(ev AuditEvent) { got = ev }, c.disclosure)
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
		auditing(noteBuilds(mux), func(ev AuditEvent) { got = ev }, false).ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest("GET", "/api/v1/installs/i/gateways/n/x", nil))
		if got.Denied != c.want {
			t.Errorf("refused %v, status %d: denied %v, want %v", c.refused, c.code, got.Denied, c.want)
		}
	}
}
