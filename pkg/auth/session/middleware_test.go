package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/platform"
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
	if rec := call("/api/v1/installs", "", c); rec.Code != 401 || strings.TrimSpace(rec.Body.String()) != endedBody {
		t.Fatalf("after ended = %d %s", rec.Code, rec.Body.String())
	}
}

// An end-session endpoint's own id_token_hint, single or repeated, never reaches the browser.
func TestLogoutDropsIDTokenHint(t *testing.T) {
	for _, ep := range []string{"https://idp.example.com/logout?id_token_hint=abc&realm=x",
		"https://idp.example.com/logout?id_token_hint=a&id_token_hint=b&realm=x"} {
		f := newFixture(t, nil)
		f.endSession = ep
		rec := f.logout(nil, "https://snellius.example.com")
		var body struct{ Next string }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(body.Next)
		if q := u.Query(); q.Has("id_token_hint") || q.Get("realm") != "x" || q.Get("client_id") != "snellius" {
			t.Fatalf("%s: next = %s", ep, body.Next)
		}
	}
}

// wantEndedEveryRequest sends 20 concurrent /api/ requests with c, then 5 sequential ones and
// an /auth/me: each answers session-ended and none sets a cookie.
func (f *fixture) wantEndedEveryRequest(c *http.Cookie) {
	f.t.Helper()
	h := f.store.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	api := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/api/v1/installs", nil)
		req.AddCookie(c)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	recs := make([]*httptest.ResponseRecorder, 20, 26)
	var wg sync.WaitGroup
	for i := range recs {
		wg.Add(1)
		go func() { defer wg.Done(); recs[i] = api() }()
	}
	wg.Wait()
	for range 5 {
		recs = append(recs, api())
	}
	recs = append(recs, f.me(c))
	for i, rec := range recs {
		if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusUnauthorized || got != endedBody {
			f.t.Fatalf("request %d = %d %s; want session-ended", i, rec.Code, got)
		}
		if cs := rec.Result().Cookies(); len(cs) != 0 {
			f.t.Fatalf("request %d sets cookies %v", i, cs)
		}
	}
}

// signInWith completes a sign-in from a browser that sends old, and returns the new cookie.
func (f *fixture) signInWith(old *http.Cookie) *http.Cookie {
	f.t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/callback", nil)
	req.AddCookie(old)
	f.store.Complete(rec, req, f.result("oidc:dev"))
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	f.t.Fatal("no session cookie")
	return nil
}

// After an idle expiry, every request with the cookie hears session-ended, not only the first.
func TestEndedSessionAnswersEveryRequest(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(31 * time.Minute)
	f.wantEndedEveryRequest(c)
	if got := f.audit.actions(); strings.Join(got, ",") != "login/success,session-ended/failure/idle" {
		t.Fatalf("audit = %v", got)
	}
}

// A sign-in from the browser deletes the old cookie's end notice.
func TestEndedNoticeClearedBySignIn(t *testing.T) {
	f := newFixture(t, nil)
	old := f.signIn(f.result("oidc:dev"))
	f.clk.Step(31 * time.Minute)
	f.wantMe(old, 401, endedBody)
	c := f.signInWith(old)
	f.wantMe(c, 200, okBody)
	f.wantMe(old, 401, signInBody)
}

// Logout with an ended session's cookie deletes its end notice.
func TestLogoutClearsEndedNotice(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(31 * time.Minute)
	f.wantMe(c, 401, endedBody)
	if rec := f.logout(c, "https://snellius.example.com"); rec.Code != 200 {
		t.Fatalf("logout = %d", rec.Code)
	}
	f.wantMe(c, 401, signInBody)
}

// noModel is a ModelSource without a publication; the in-flight test only calls an extension.
type noModel struct{}

func (noModel) Current() *engine.Published { return nil }
func (noModel) Platform() platform.Info    { return platform.Kubernetes() }

// barrier holds each request until release closes; entered records arrivals.
type barrier struct {
	entered chan struct{}
	release chan struct{}
}

func (b barrier) wait() {
	b.entered <- struct{}{}
	<-b.release
}

// blockedFullRead is an authoriser whose full-read SAR blocks on b, then fails.
type blockedFullRead struct {
	api.AllowAll
	b barrier
}

func (a blockedFullRead) FullRead(*http.Request) (bool, error) {
	a.b.wait()
	return false, errors.New("SAR failed")
}

// Requests already past Middleware when their session ends answer 401, never
// their buffered body or a 503, whether the end is idle expiry, a failed
// refresh or a logout, and whether they wait in a handler or a full-read SAR.
func TestSessionEndsInFlight(t *testing.T) {
	const n = 4
	ends := map[string]func(f *fixture, c *http.Cookie){
		"none": func(*fixture, *http.Cookie) {},
		"idle": func(f *fixture, _ *http.Cookie) { f.clk.Step(31 * time.Minute) },
		"refresh": func(f *fixture, c *http.Cookie) {
			f.refresh.mu.Lock()
			f.refresh.err = errors.New("refused")
			f.refresh.mu.Unlock()
			f.clk.Step(16 * time.Minute)
			f.wantMe(c, 401, endedBody)
		},
		"logout": func(f *fixture, c *http.Cookie) {
			if rec := f.logout(c, "https://snellius.example.com"); rec.Code != 200 {
				f.t.Fatalf("logout = %d", rec.Code)
			}
		},
	}
	for name, end := range ends {
		for _, inFullRead := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fullRead=%v", name, inFullRead), func(t *testing.T) {
				f := newFixture(t, nil)
				c := f.signIn(f.result("oidc:dev"))
				b := barrier{entered: make(chan struct{}, n), release: make(chan struct{})}
				var authz api.Authorizer = api.AllowAll{}
				if inFullRead {
					authz = blockedFullRead{b: b}
				}
				// With a full read the handler waits in it, as its first population question.
				ext := api.ExtMount{Name: "slow", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if inFullRead {
						api.Visible(r, authz, []objects.Ref{{Kind: "HTTPRoute", Namespace: "shop", Name: "a"}}, func(ref objects.Ref) objects.Ref { return ref })
					} else {
						b.wait()
					}
					api.WriteJSON(w, "buffered")
				})}
				h := f.store.Middleware(api.NewHandler(noModel{}, fstest.MapFS{}, authz, api.WithSecureMode(true), api.WithExtMounts(ext),
					api.WithCommitCheck(f.store.StillSignedIn)))
				recs := make([]*httptest.ResponseRecorder, n)
				var wg sync.WaitGroup
				for i := range recs {
					recs[i] = httptest.NewRecorder()
					req := httptest.NewRequest("GET", "/api/ext/slow/data", nil)
					req.AddCookie(c)
					wg.Go(func() { h.ServeHTTP(recs[i], req) })
				}
				for range n {
					testutil.Recv(t, b.entered, "a request to pass the middleware")
				}
				end(f, c)
				close(b.release)
				wg.Wait()
				want := map[string]string{"none": `"buffered"`, "idle": endedBody, "refresh": endedBody, "logout": signInBody}[name]
				code := http.StatusUnauthorized
				switch {
				case name == "none" && inFullRead:
					code, want = http.StatusServiceUnavailable, `{"error":"authz-unavailable"}`
				case name == "none":
					code = http.StatusOK
				}
				for i, rec := range recs {
					if got := strings.TrimSpace(rec.Body.String()); rec.Code != code || got != want {
						t.Errorf("request %d = %d %s; want %d %s", i, rec.Code, got, code, want)
					}
				}
			})
		}
	}
}

// commitRun sends one /api/ request with c through Middleware and the commit
// wrapper. Its handler shows "admin" only to the oidc:admin group; the first
// build calls mid before it writes. It answers the response and the build count.
func (f *fixture) commitRun(c *http.Cookie, mid func(*http.Request), opts ...api.HandlerOption) (*httptest.ResponseRecorder, int) {
	f.t.Helper()
	builds := 0
	ext := api.ExtMount{Name: "probe", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if builds++; builds == 1 {
			mid(r)
		}
		id, _ := IdentityFrom(r.Context())
		data := "public"
		if slices.Contains(id.Groups, "oidc:admin") {
			data = "admin"
		}
		api.WriteJSON(w, data)
	})}
	opts = append([]api.HandlerOption{api.WithSecureMode(true), api.WithExtMounts(ext), api.WithCommitCheck(f.store.StillSignedIn)}, opts...)
	h := f.store.Middleware(api.NewHandler(noModel{}, fstest.MapFS{}, api.AllowAll{}, opts...))
	req := httptest.NewRequest("GET", "/api/ext/probe/data", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, builds
}

// refreshDone waits until the refresh the request started has replaced the identity.
func (f *fixture) refreshDone() {
	f.t.Helper()
	waitFor(f.t, "the refresh to finish", func() bool {
		f.store.mu.Lock()
		defer f.store.mu.Unlock()
		for _, sess := range f.store.sessions {
			if sess.refreshing != nil || !sess.reviewed.Equal(f.clk.Now()) {
				return false
			}
		}
		return f.refresh.count() == 1
	})
}

// A refresh that drops a group while a request runs: the response is rebuilt
// with the current identity, never served from the dropped group.
func TestCommitRebuildsAfterRefreshDropsGroup(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.gate = make(chan struct{})
	f.refresh.result = func() oidc.Result { return f.result("oidc:dev") }
	c := f.signIn(f.result("oidc:dev", "oidc:admin"))
	// Within refreshAhead of staleness, the request starts a background refresh.
	f.clk.Step(14 * time.Minute)
	rec, builds := f.commitRun(c, func(*http.Request) {
		close(f.refresh.gate)
		f.refreshDone()
	})
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `"public"` || builds != 2 {
		t.Fatalf("got %d %s after %d builds; want 200 \"public\" after 2", rec.Code, got, builds)
	}
}

// A check that failed during the build answers 503 and its audit record even
// when a refresh replaced the identity meanwhile: no rebuild hides the error.
func TestCommitFailedCheckBeforeRebuild(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.gate = make(chan struct{})
	f.refresh.result = func() oidc.Result { return f.result("oidc:dev") }
	c := f.signIn(f.result("oidc:dev", "oidc:admin"))
	f.clk.Step(14 * time.Minute)
	var reasons []string
	rec, builds := f.commitRun(c, func(r *http.Request) {
		api.ReportAuthzError(r, errors.New("SAR failed"))
		close(f.refresh.gate)
		f.refreshDone()
	}, api.WithAuthzAudit(func(_ *http.Request, why string) { reasons = append(reasons, why) }))
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusServiceUnavailable || got != `{"error":"authz-unavailable"}` || builds != 1 {
		t.Fatalf("got %d %s after %d builds; want 503 authz-unavailable after 1", rec.Code, got, builds)
	}
	if !slices.Equal(reasons, []string{api.AuditCheckFailed}) {
		t.Fatalf("audit %v, want [%s]", reasons, api.AuditCheckFailed)
	}
}

// A refresh that returns the same identity does not rebuild the response.
func TestCommitKeepsUnchangedRefresh(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.gate = make(chan struct{})
	f.refresh.result = func() oidc.Result { return f.result("oidc:admin", "oidc:dev") }
	c := f.signIn(f.result("oidc:dev", "oidc:admin"))
	f.clk.Step(14 * time.Minute)
	rec, builds := f.commitRun(c, func(*http.Request) {
		close(f.refresh.gate)
		f.refreshDone()
	})
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusOK || got != `"admin"` || builds != 1 {
		t.Fatalf("got %d %s after %d builds; want 200 \"admin\" after 1", rec.Code, got, builds)
	}
}

// A request that commits after its identity went stale, with a refresh
// possible, answers 503 authz-unavailable; the session lives on and the next
// request waits for the refresh.
func TestCommitStaleIdentity(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev", "oidc:admin"))
	rec, builds := f.commitRun(c, func(*http.Request) { f.clk.Step(16 * time.Minute) })
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != http.StatusServiceUnavailable || got != `{"error":"authz-unavailable"}` || builds != 1 {
		t.Fatalf("got %d %s after %d builds; want 503 authz-unavailable after 1", rec.Code, got, builds)
	}
	if n := f.refresh.count(); n != 0 {
		t.Fatalf("the commit check refreshed %d times", n)
	}
	f.wantMe(c, 200, `{"username":"oidc:alice","groups":["oidc:ops"]}`)
}
