package session

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// logout posts /auth/logout over plain HTTP, as behind TLS termination, with origin when not empty.
func (f *fixture) logout(c *http.Cookie, origin string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest("POST", "http://10.0.0.5:8080/auth/logout", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.store.Logout(rec, req)
	return rec
}

func TestLogoutOrigin(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	for _, origin := range []string{"", "http://snellius.example.com", "https://evil.example.com", "https://snellius.example.com:8443", "null"} {
		if rec := f.logout(c, origin); rec.Code != http.StatusForbidden {
			t.Errorf("Origin %q: %d, want 403", origin, rec.Code)
		}
	}
	f.wantMe(c, 200, okBody)
	rec := f.logout(c, "https://snellius.example.com")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"next":"/"}` {
		t.Fatalf("logout = %d %s", rec.Code, rec.Body.String())
	}
	var cleared bool
	for _, ck := range rec.Result().Cookies() {
		cleared = cleared || ck.Name == CookieName && ck.MaxAge < 0
	}
	if !cleared {
		t.Fatal("logout did not clear the cookie")
	}
	f.wantMe(c, 401, signInBody)
	get := httptest.NewRecorder()
	f.store.Logout(get, httptest.NewRequest("GET", "/auth/logout", nil))
	if get.Code != http.StatusMethodNotAllowed || get.Header().Get("Allow") != "POST" {
		t.Fatalf("GET = %d", get.Code)
	}
}

// RP-initiated logout names the client and the app root, never an ID token.
func TestLogoutEndSessionURL(t *testing.T) {
	f := newFixture(t, nil)
	f.endSession = "https://idp.example.com/logout?realm=x"
	c := f.signIn(f.result("oidc:dev"))
	rec := f.logout(c, "https://snellius.example.com")
	var body struct{ Next string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(body.Next)
	if err != nil || u.Scheme != "https" || u.Host != "idp.example.com" || u.Path != "/logout" {
		t.Fatalf("next = %q", body.Next)
	}
	q := u.Query()
	if q.Get("client_id") != "snellius" || q.Get("post_logout_redirect_uri") != "https://snellius.example.com/" || q.Get("realm") != "x" {
		t.Fatalf("query = %v", q)
	}
	if q.Has("id_token_hint") || strings.Contains(rec.Body.String(), "id-token-value") {
		t.Fatalf("logout leaks the ID token: %s", body.Next)
	}
	// Without a session there is nothing to audit, and the answer is the same.
	before := len(f.audit.actions())
	if rec := f.logout(nil, "https://snellius.example.com"); rec.Code != 200 || len(f.audit.actions()) != before {
		t.Fatalf("logout without a session = %d, audit %v", rec.Code, f.audit.actions())
	}
}

func TestMiddleware(t *testing.T) {
	f := newFixture(t, nil)
	var seen Info
	var seenOK bool
	h := f.store.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, seenOK = FromContext(r.Context())
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	call := func(path, referer string, c *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if referer != "" {
			req.Header.Set("Referer", referer)
		}
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	// Outside /api/ the middleware passes the request on without an identity.
	if rec := call("/routes", "", nil); rec.Code != http.StatusServiceUnavailable || seenOK {
		t.Fatalf("SPA = %d, identity %v", rec.Code, seenOK)
	}
	rec := call("/api/v1/installs", "https://snellius.example.com/routes?install=a", nil)
	want := `{"error":"sign-in-required","login":"/auth/login?return=%2Froutes%3Finstall%3Da"}`
	if rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != want || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("no session = %d %s", rec.Code, rec.Body.String())
	}
	c := f.signIn(f.result("oidc:dev"))
	// With a session the request reaches the next handler with the identity.
	if rec := call("/api/v1/installs", "", c); rec.Code != http.StatusServiceUnavailable || !seenOK {
		t.Fatalf("session = %d, identity %v", rec.Code, seenOK)
	}
	id, ok := IdentityFrom(withInfo(t.Context(), seen))
	if !ok || id.Username != "oidc:alice" || seen.Subject != "alice-sub" || seen.Issuer != testIssuer || seen.AuditID == "" || seen.AuditID == c.Value {
		t.Fatalf("info = %+v", seen)
	}
	f.clk.Step(31 * time.Minute)
	if rec := call("/api/v1/installs", "", c); rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != endedBody {
		t.Fatalf("ended = %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("/api/v1/installs", "", c); rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != signInBody {
		t.Fatalf("after ended = %d %s", rec.Code, rec.Body.String())
	}
}
