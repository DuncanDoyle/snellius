package oidc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/audit"
	"golang.org/x/oauth2"
	clocktesting "k8s.io/utils/clock/testing"
)

const testClientID = "snellius"

// Token values the tests look for in every output.
const (
	testAccessToken  = "access-token-value"
	testRefreshToken = "refresh-token-value"
)

// testIdP is an httptest issuer: discovery, and a token endpoint that checks
// the PKCE verifier and returns an ID token built from claims.
type testIdP struct {
	srv *httptest.Server
	clk *clocktesting.FakeClock

	mu        sync.Mutex
	challenge string
	nonce     string
	claims    func(c map[string]any) // edits the default claims
	idToken   string                 // the last issued ID token
	form      url.Values             // the last token request
	basicAuth bool                   // the last token request used HTTP basic auth
}

func newTestIdP(t *testing.T, clk *clocktesting.FakeClock) *testIdP {
	t.Helper()
	p := &testIdP{clk: clk}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *testIdP) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		_ = json.NewEncoder(w).Encode(goodDoc(p.srv.URL))
	case "/token":
		_ = r.ParseForm()
		p.mu.Lock()
		defer p.mu.Unlock()
		p.form = r.PostForm
		_, _, p.basicAuth = r.BasicAuth()
		if r.PostForm.Get("code") != "good-code" || oauth2.S256ChallengeFromVerifier(r.PostForm.Get("code_verifier")) != p.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		now := p.clk.Now()
		c := map[string]any{"iss": p.srv.URL, "sub": "alice-sub", "aud": testClientID, "exp": now.Add(5 * time.Minute).Unix(),
			"iat": now.Unix(), "nonce": p.nonce}
		if p.claims != nil {
			p.claims(c)
		}
		p.idToken = jwt(c)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": testAccessToken, "token_type": "Bearer",
			"refresh_token": testRefreshToken, "expires_in": 300, "id_token": p.idToken})
	default:
		http.NotFound(w, r)
	}
}

// jwt encodes claims as an unsigned-looking compact JWT; signatures are TokenReview's job.
func jwt(claims map[string]any) string {
	enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
	return enc(map[string]string{"alg": "RS256", "typ": "JWT"}) + "." + enc(claims) + ".c2lnbmF0dXJl"
}

// fakeReviewer returns its identity or error and records the token it saw.
type fakeReviewer struct {
	mu    sync.Mutex
	id    Identity
	err   error
	token string
}

func (f *fakeReviewer) Review(_ context.Context, tok string) (Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.token = tok
	return f.id, f.err
}

type recordingCompleter struct{ results []Result }

func (c *recordingCompleter) Complete(w http.ResponseWriter, r *http.Request, res Result) {
	c.results = append(c.results, res)
	RedirectCompleter{}.Complete(w, r, res)
}

type recordingAuditor struct {
	mu   sync.Mutex
	recs []audit.Record
}

func (a *recordingAuditor) Emit(_ context.Context, rec audit.Record) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
}

// flowFixture wires a Flow to a test issuer, a fake reviewer and recorders.
type flowFixture struct {
	t        *testing.T
	clk      *clocktesting.FakeClock
	idp      *testIdP
	reviewer *fakeReviewer
	done     *recordingCompleter
	audit    *recordingAuditor
	logs     *bytes.Buffer
	flow     *Flow
}

func newFlowFixture(t *testing.T, secret string) *flowFixture {
	t.Helper()
	clk := clocktesting.NewFakeClock(time.Now())
	f := &flowFixture{t: t, clk: clk, idp: newTestIdP(t, clk), done: &recordingCompleter{}, audit: &recordingAuditor{}, logs: &bytes.Buffer{},
		reviewer: &fakeReviewer{id: Identity{Username: "oidc:alice", Groups: []string{"oidc:dev"}}}}
	prov := providerFor(t, f.idp.srv, clk)
	prov.cfg.ClientID = testClientID
	eps, err := prov.Discover(context.Background(), prov.HTTPClient())
	if err != nil {
		t.Fatal(err)
	}
	prov.eps, prov.lastErr = eps, nil
	cfg := prov.cfg
	if secret != "" {
		cfg.ClientSecretFile = filepath.Join(t.TempDir(), "secret")
		if err := os.WriteFile(cfg.ClientSecretFile, []byte(secret+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	flow, err := NewFlow(FlowOptions{Config: cfg, Provider: prov, Reviewer: f.reviewer, Completer: f.done, Audit: f.audit,
		Clock: clk, CookieSecure: true, Logger: slog.New(slog.NewTextHandler(f.logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	f.flow = flow
	return f
}

// login starts a sign-in and returns the login cookie and the authorization URL's parameters.
func (f *flowFixture) login(ret string) (*http.Cookie, url.Values) {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.flow.Login(rec, httptest.NewRequest("GET", "/auth/login?return="+url.QueryEscape(ret), nil))
	if rec.Code != http.StatusFound {
		f.t.Fatalf("login status %d", rec.Code)
	}
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		f.t.Fatal(err)
	}
	q := loc.Query()
	f.idp.mu.Lock()
	f.idp.challenge, f.idp.nonce = q.Get("code_challenge"), q.Get("nonce")
	f.idp.mu.Unlock()
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 {
		f.t.Fatalf("login cookies = %v", cookies)
	}
	return cookies[0], q
}

// callback calls /auth/callback with the cookie (when not nil) and query.
func (f *flowFixture) callback(c *http.Cookie, q url.Values) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest("GET", "/auth/callback?"+q.Encode(), nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.flow.Callback(rec, req)
	return rec
}

// signIn runs login then a callback with the given state.
func (f *flowFixture) signIn(ret string) *httptest.ResponseRecorder {
	c, q := f.login(ret)
	return f.callback(c, url.Values{"code": {"good-code"}, "state": {q.Get("state")}})
}

// assertFailed checks the redirect, the one audit record and that no session completed.
func (f *flowFixture) assertFailed(rec *httptest.ResponseRecorder, reason string) {
	f.t.Helper()
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/signin-failed?reason="+reason {
		f.t.Fatalf("callback = %d %q, want a redirect to /signin-failed?reason=%s", rec.Code, rec.Header().Get("Location"), reason)
	}
	if len(f.done.results) != 0 {
		f.t.Fatal("a failed sign-in completed")
	}
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	last := f.audit.recs[len(f.audit.recs)-1]
	if last.Level != audit.LevelSecurity || last.Action != "login" || last.Outcome != "failure" || last.Reason != reason {
		f.t.Fatalf("audit record = %+v", last)
	}
}

func TestLoginRedirect(t *testing.T) {
	f := newFlowFixture(t, "")
	rec := httptest.NewRecorder()
	f.flow.Login(rec, httptest.NewRequest("GET", "/auth/login?return=%2Froutes%3Fns%3Da", nil))
	loc, _ := url.Parse(rec.Header().Get("Location"))
	if rec.Code != http.StatusFound || loc.Scheme+"://"+loc.Host+loc.Path != f.idp.srv.URL+"/auth" {
		t.Fatalf("login = %d %s, want a redirect to the authorization endpoint", rec.Code, loc)
	}
	q := loc.Query()
	for k, want := range map[string]string{"response_type": "code", "client_id": testClientID, "redirect_uri": "https://s.example.com/auth/callback",
		"scope": "openid profile email groups offline_access", "code_challenge_method": "S256"} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
	for _, k := range []string{"state", "nonce", "code_challenge"} {
		if len(q.Get(k)) < 43 {
			t.Errorf("%s = %q, want at least 256 bits", k, q.Get(k))
		}
	}
	c := rec.Result().Cookies()[0]
	if c.Name != "snellius_login" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/auth/" || c.MaxAge != 600 {
		t.Errorf("cookie = %+v", c)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the login redirect may be cached")
	}
	post := httptest.NewRecorder()
	f.flow.Login(post, httptest.NewRequest("POST", "/auth/login", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /auth/login = %d", post.Code)
	}
}

func TestCallbackSuccess(t *testing.T) {
	f := newFlowFixture(t, "s3cret")
	rec := f.signIn("/routes?ns=a")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/routes?ns=a" {
		t.Fatalf("callback = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if len(f.done.results) != 1 {
		t.Fatalf("completed %d sign-ins", len(f.done.results))
	}
	res := f.done.results[0]
	if res.Identity.Username != "oidc:alice" || res.Issuer != f.idp.srv.URL || res.Subject != "alice-sub" || res.ReturnPath != "/routes?ns=a" ||
		res.IDToken != f.idp.idToken || res.AccessToken != testAccessToken || res.RefreshToken != testRefreshToken ||
		!res.Expiry.Equal(time.Unix(f.clk.Now().Add(5*time.Minute).Unix(), 0)) {
		t.Errorf("result = %+v", res)
	}
	if f.reviewer.token != f.idp.idToken {
		t.Error("TokenReview did not get the ID token")
	}
	if !f.idp.basicAuth || f.idp.form.Get("client_secret") != "" {
		t.Error("a confidential client must authenticate with HTTP basic auth")
	}
	form := f.idp.form
	if form.Get("grant_type") != "authorization_code" || form.Get("redirect_uri") != "https://s.example.com/auth/callback" {
		t.Errorf("token request = %v", form)
	}
	// The cookie is cleared; no other cookie is set.
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != "snellius_login" || cookies[0].MaxAge >= 0 || cookies[0].Path != "/auth/" {
		t.Errorf("callback cookies = %+v", cookies)
	}
	if len(f.audit.recs) != 0 {
		t.Errorf("audit records = %+v; success is the completer's", f.audit.recs)
	}
}

// A public client sends client_id in the form; a confidential one uses basic auth.
func TestCallbackClientAuth(t *testing.T) {
	f := newFlowFixture(t, "")
	f.signIn("/")
	if f.idp.form.Get("client_id") != testClientID || f.idp.form.Get("client_secret") != "" || f.idp.basicAuth {
		t.Errorf("public client form = %v", f.idp.form)
	}
	if len(f.done.results) != 1 {
		t.Fatal("public client sign-in failed")
	}
	if _, err := NewFlow(FlowOptions{Config: Config{ClientSecretFile: filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Error("an unreadable client secret file was accepted")
	}
}

func TestCallbackFailures(t *testing.T) {
	for name, c := range map[string]struct {
		run    func(f *flowFixture) *httptest.ResponseRecorder
		reason string
	}{
		"replayed callback": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			vals := url.Values{"code": {"good-code"}, "state": {q.Get("state")}}
			if rec := f.callback(ck, vals); rec.Header().Get("Location") != "/" {
				f.t.Fatalf("first callback = %q", rec.Header().Get("Location"))
			}
			f.done.results = nil
			return f.callback(ck, vals)
		}, "invalid"},
		"wrong state": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, _ := f.login("/")
			return f.callback(ck, url.Values{"code": {"good-code"}, "state": {"other"}})
		}, "invalid"},
		"wrong state consumes the transaction": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			f.callback(ck, url.Values{"code": {"good-code"}, "state": {"other"}})
			return f.callback(ck, url.Values{"code": {"good-code"}, "state": {q.Get("state")}})
		}, "invalid"},
		"no cookie": {func(f *flowFixture) *httptest.ResponseRecorder {
			_, q := f.login("/")
			return f.callback(nil, url.Values{"code": {"good-code"}, "state": {q.Get("state")}})
		}, "invalid"},
		"expired transaction": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			f.clk.Step(11 * time.Minute)
			return f.callback(ck, url.Values{"code": {"good-code"}, "state": {q.Get("state")}})
		}, "invalid"},
		"issuer error response": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			return f.callback(ck, url.Values{"error": {"access_denied"}, "state": {q.Get("state")}})
		}, "invalid"},
		"code exchange refused": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			return f.callback(ck, url.Values{"code": {"bad-code"}, "state": {q.Get("state")}})
		}, "invalid"},
		"wrong verifier": {func(f *flowFixture) *httptest.ResponseRecorder {
			ck, q := f.login("/")
			f.idp.challenge = "other-challenge"
			return f.callback(ck, url.Values{"code": {"good-code"}, "state": {q.Get("state")}})
		}, "invalid"},
		"wrong iss":               {claims(func(c map[string]any) { c["iss"] = "https://evil.example.com" }), "invalid"},
		"wrong aud":               {claims(func(c map[string]any) { c["aud"] = "other" }), "invalid"},
		"aud list without client": {claims(func(c map[string]any) { c["aud"] = []string{"a", "b"} }), "invalid"},
		"azp mismatch with several audiences": {claims(func(c map[string]any) {
			c["aud"], c["azp"] = []string{testClientID, "other"}, "other"
		}), "invalid"},
		"azp missing with several audiences": {claims(func(c map[string]any) { c["aud"] = []string{testClientID, "other"} }), "invalid"},
		"azp mismatch with one audience":     {claims(func(c map[string]any) { c["azp"] = "other" }), "invalid"},
		"expired token": {claims(func(c map[string]any) {
			c["exp"] = time.Now().Add(-3 * time.Minute).Unix()
		}), "invalid"},
		"missing exp":       {claims(func(c map[string]any) { delete(c, "exp") }), "invalid"},
		"iat in the future": {claims(func(c map[string]any) { c["iat"] = time.Now().Add(3 * time.Minute).Unix() }), "invalid"},
		"missing iat":       {claims(func(c map[string]any) { delete(c, "iat") }), "invalid"},
		"wrong nonce":       {claims(func(c map[string]any) { c["nonce"] = "other" }), "invalid"},
		"missing sub":       {claims(func(c map[string]any) { delete(c, "sub") }), "invalid"},
		"token review not authenticated": {func(f *flowFixture) *httptest.ResponseRecorder {
			f.reviewer.id, f.reviewer.err = Identity{}, ErrUnauthenticated
			return f.signIn("/")
		}, "unauthenticated"},
		"token review audience": {func(f *flowFixture) *httptest.ResponseRecorder {
			f.reviewer.id, f.reviewer.err = Identity{}, ErrAudience
			return f.signIn("/")
		}, "audience"},
		"token review unreachable": {func(f *flowFixture) *httptest.ResponseRecorder {
			f.reviewer.id, f.reviewer.err = Identity{}, context.DeadlineExceeded
			return f.signIn("/")
		}, "invalid"},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFlowFixture(t, "s3cret")
			rec := c.run(f)
			f.assertFailed(rec, c.reason)
			assertNoTokens(t, f, rec)
		})
	}
}

// claims runs a sign-in whose ID token has the default claims edited by edit.
func claims(edit func(map[string]any)) func(f *flowFixture) *httptest.ResponseRecorder {
	return func(f *flowFixture) *httptest.ResponseRecorder {
		f.idp.claims = edit
		rec := f.signIn("/")
		if f.reviewer.token != "" {
			f.t.Error("TokenReview ran on claims that failed")
		}
		return rec
	}
}

// Claims are checked against the injected clock, with two minutes of skew.
func TestCallbackClockSkew(t *testing.T) {
	f := newFlowFixture(t, "")
	f.idp.claims = func(c map[string]any) {
		c["exp"] = f.clk.Now().Add(-time.Minute).Unix()
		c["iat"] = f.clk.Now().Add(time.Minute).Unix()
	}
	if rec := f.signIn("/"); rec.Header().Get("Location") != "/" {
		t.Fatalf("within skew: %q", rec.Header().Get("Location"))
	}
}

func assertNoTokens(t *testing.T, f *flowFixture, rec *httptest.ResponseRecorder) {
	t.Helper()
	var hdr bytes.Buffer
	_ = rec.Header().Write(&hdr)
	f.audit.mu.Lock()
	recs, _ := json.Marshal(f.audit.recs)
	f.audit.mu.Unlock()
	for _, tok := range []string{f.idp.idToken, testAccessToken, testRefreshToken} {
		if tok == "" {
			continue
		}
		for where, s := range map[string]string{"headers": hdr.String(), "body": rec.Body.String(), "audit": string(recs), "log": f.logs.String()} {
			if strings.Contains(s, tok) {
				t.Errorf("a token reached the %s", where)
			}
		}
	}
}

func TestSafeReturnPath(t *testing.T) {
	for in, want := range map[string]string{
		"":                              "/",
		"/":                             "/",
		"/routes?ns=a#x":                "/routes?ns=a#x",
		"/a/b":                          "/a/b",
		"//evil":                        "/",
		"//evil.example.com":            "/",
		"https://evil":                  "/",
		"http:/evil":                    "/",
		"javascript:x":                  "/",
		`/\evil`:                        "/",
		"/\t/evil":                      "/",
		"/\n/evil":                      "/",
		"/a\x7f":                        "/",
		"relative":                      "/",
		"/" + strings.Repeat("a", 2048): "/",
	} {
		if got := safeReturnPath(in); got != want {
			t.Errorf("safeReturnPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// Open redirects through the full flow: login stores the safe path, the callback uses it.
func TestCallbackOpenRedirect(t *testing.T) {
	for _, ret := range []string{"//evil", "https://evil", `/\evil`} {
		t.Run(ret, func(t *testing.T) {
			f := newFlowFixture(t, "")
			if rec := f.signIn(ret); rec.Header().Get("Location") != "/" {
				t.Fatalf("return %q led to %q", ret, rec.Header().Get("Location"))
			}
		})
	}
}

// Before discovery succeeds, the routes answer 503 and start nothing.
func TestLoginBeforeDiscovery(t *testing.T) {
	clk := clocktesting.NewFakeClock(time.Now())
	idp := newTestIdP(t, clk)
	prov := providerFor(t, idp.srv, clk)
	flow, err := NewFlow(FlowOptions{Config: prov.cfg, Provider: prov, Reviewer: &fakeReviewer{}, Completer: RedirectCompleter{},
		Audit: &recordingAuditor{}, Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	flow.Login(rec, httptest.NewRequest("GET", "/auth/login", nil))
	if rec.Code != http.StatusServiceUnavailable || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("login before discovery = %d, cookies %v", rec.Code, rec.Result().Cookies())
	}
}
