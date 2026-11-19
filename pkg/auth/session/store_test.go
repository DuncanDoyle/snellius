package session

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

const (
	testIssuer   = "https://idp.example.com"
	testRedirect = "https://snellius.example.com/auth/callback"
	testRefresh  = "refresh-token-value"
)

type recordingAuditor struct {
	mu   sync.Mutex
	recs []audit.Record
}

func (a *recordingAuditor) Emit(_ context.Context, rec audit.Record) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recs = append(a.recs, rec)
}

// actions lists the records as action/outcome/reason.
func (a *recordingAuditor) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, r := range a.recs {
		out = append(out, strings.TrimRight(r.Action+"/"+r.Outcome+"/"+r.Reason, "/"))
	}
	return out
}

// fakeRefresher answers with result or err; with gate set, each call waits for it.
type fakeRefresher struct {
	mu     sync.Mutex
	calls  int
	result func() oidc.Result
	err    error
	gate   chan struct{}
	called chan struct{}
}

func (f *fakeRefresher) Refresh(_ context.Context, rt string) (oidc.Result, error) {
	f.mu.Lock()
	f.calls++
	gate, called := f.gate, f.called
	f.mu.Unlock()
	if called != nil {
		called <- struct{}{}
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return oidc.Result{}, f.err
	}
	if rt != testRefresh {
		return oidc.Result{}, errors.New("unexpected refresh token")
	}
	return f.result(), nil
}

func (f *fakeRefresher) count() int { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }

type fixture struct {
	t          *testing.T
	clk        *clocktesting.FakeClock
	refresh    *fakeRefresher
	audit      *recordingAuditor
	metrics    *obs.Registry
	store      *Store
	endSession string
	logs       *syncBuf
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newFixture(t *testing.T, mod func(*Options)) *fixture {
	t.Helper()
	f := &fixture{t: t, clk: clocktesting.NewFakeClock(time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)), audit: &recordingAuditor{}, metrics: obs.NewRegistry()}
	f.refresh = &fakeRefresher{result: func() oidc.Result { return f.result("oidc:ops") }}
	f.logs = &syncBuf{}
	o := Options{Logger: slog.New(slog.NewTextHandler(f.logs, nil)), Clock: f.clk, Refresher: f.refresh, Audit: f.audit, Metrics: f.metrics, CookieSecure: true, ClientID: "snellius",
		RedirectURL: testRedirect, EndSession: func() string { return f.endSession }}
	if mod != nil {
		mod(&o)
	}
	f.store = New(o)
	return f
}

// result is a sign-in for alice whose ID token lives an hour.
func (f *fixture) result(groups ...string) oidc.Result {
	return oidc.Result{Identity: oidc.Identity{Username: "oidc:alice", Groups: groups}, Issuer: testIssuer, Subject: "alice-sub",
		IDToken: "id-token-value", Expiry: f.clk.Now().Add(time.Hour), AccessToken: "access-token-value", RefreshToken: testRefresh, ReturnPath: "/routes"}
}

// signIn completes a sign-in and returns the session cookie.
func (f *fixture) signIn(res oidc.Result) *http.Cookie {
	f.t.Helper()
	rec := httptest.NewRecorder()
	f.store.Complete(rec, httptest.NewRequest("GET", "/auth/callback", nil), res)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != res.ReturnPath+"#signed-in" {
		f.t.Fatalf("complete = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	f.t.Fatal("no session cookie")
	return nil
}

// me calls GET /auth/me with the cookie, when not nil.
func (f *fixture) me(c *http.Cookie) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest("GET", "/auth/me", nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.store.Me(rec, req)
	return rec
}

func (f *fixture) wantMe(c *http.Cookie, code int, body string) {
	f.t.Helper()
	rec := f.me(c)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != code || got != body {
		f.t.Fatalf("me = %d %s; want %d %s", rec.Code, got, code, body)
	}
}

const (
	okBody     = `{"username":"oidc:alice","groups":["oidc:dev"]}`
	endedBody  = `{"error":"session-ended","login":"/auth/login?return=%2F"}`
	signInBody = `{"error":"sign-in-required","login":"/auth/login?return=%2F"}`
)

func (f *fixture) gauge() string {
	rec := httptest.NewRecorder()
	f.metrics.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if strings.HasPrefix(l, "snellius_sessions_active ") {
			return strings.TrimPrefix(l, "snellius_sessions_active ")
		}
	}
	return ""
}

func TestCookieAndLoginAudit(t *testing.T) {
	for _, secure := range []bool{true, false} {
		f := newFixture(t, func(o *Options) { o.CookieSecure = secure })
		c := f.signIn(f.result("oidc:dev"))
		if !c.HttpOnly || c.Secure != secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || len(c.Value) != 43 {
			t.Fatalf("cookie = %+v", c)
		}
		f.wantMe(c, 200, okBody)
		recs := f.audit.recs
		if len(recs) != 1 || recs[0].Action != "login" || recs[0].Outcome != "success" || recs[0].Level != audit.LevelSecurity {
			t.Fatalf("audit = %+v", recs)
		}
		r := recs[0]
		if r.Session == "" || r.Session == c.Value || strings.Contains(c.Value, r.Session) || r.User.Subject != "alice-sub" ||
			r.User.Issuer != testIssuer || r.Kube.Username != "oidc:alice" {
			t.Fatalf("login record = %+v", r)
		}
	}
}

// The redirect after sign-in carries the signed-in fragment in place of any other, on the same path.
func TestCompleteMarksFreshSignIn(t *testing.T) {
	f := newFixture(t, nil)
	for in, want := range map[string]string{"/": "/#signed-in", "/routes?q=a": "/routes?q=a#signed-in", "/i/x/#old": "/i/x/#signed-in"} {
		res := f.result()
		res.ReturnPath = in
		rec := httptest.NewRecorder()
		f.store.Complete(rec, httptest.NewRequest("GET", "/auth/callback", nil), res)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("Complete(%q) Location = %q, want %q", in, got, want)
		}
	}
}

// Signing in again replaces the browser's previous session.
func TestSignInReplacesSession(t *testing.T) {
	f := newFixture(t, nil)
	old := f.signIn(f.result("oidc:dev"))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/auth/callback", nil)
	req.AddCookie(old)
	f.store.Complete(rec, req, f.result("oidc:dev"))
	f.wantMe(old, 401, signInBody)
	if g := f.gauge(); g != "1" {
		t.Fatalf("gauge = %s", g)
	}
}

func TestMe(t *testing.T) {
	f := newFixture(t, nil)
	f.wantMe(nil, 401, signInBody)
	f.wantMe(&http.Cookie{Name: CookieName, Value: "forged"}, 401, signInBody)
	c := f.signIn(f.result("oidc:dev"))
	f.wantMe(c, 200, okBody)
	rec := f.me(c)
	if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("headers = %v", rec.Header())
	}
	// No groups is an empty list, not null.
	c = f.signIn(f.result())
	f.wantMe(c, 200, `{"username":"oidc:alice","groups":[]}`)
	post := httptest.NewRecorder()
	f.store.Me(post, httptest.NewRequest("POST", "/auth/me", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", post.Code)
	}
}

func TestIdleExpiry(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.result = func() oidc.Result { return f.result("oidc:dev") }
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(29 * time.Minute)
	f.wantMe(c, 200, okBody)
	f.clk.Step(30 * time.Minute)
	f.wantMe(c, 401, endedBody)
	f.wantMe(c, 401, endedBody)
	if got := f.audit.actions(); strings.Join(got, ",") != "login/success,session-ended/failure/idle" {
		t.Fatalf("audit = %v", got)
	}
}

func TestAbsoluteExpiry(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Absolute = time.Hour })
	f.refresh.result = func() oidc.Result { return f.result("oidc:dev") }
	c := f.signIn(f.result("oidc:dev"))
	for range 5 {
		f.clk.Step(10 * time.Minute)
		f.wantMe(c, 200, okBody)
	}
	f.clk.Step(10 * time.Minute)
	f.wantEndedEveryRequest(c)
	if got := f.audit.actions(); got[len(got)-1] != "session-ended/failure/absolute" {
		t.Fatalf("audit = %v", got)
	}
}

// The sweeper ends idle sessions every minute and stops when its context ends.
func TestSweeper(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	f.signIn(f.result("oidc:dev"))
	if g := f.gauge(); g != "2" {
		t.Fatalf("gauge = %s", g)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.store.Run(ctx); close(done) }()
	waitFor(t, "the sweeper's ticker", func() bool { return f.clk.HasWaiters() })
	f.clk.Step(31 * time.Minute)
	waitFor(t, "sessions swept", func() bool { return f.gauge() == "0" })
	f.wantMe(c, 401, endedBody)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sweeper did not stop")
	}
}

func TestBoundEvictsOldestIdle(t *testing.T) {
	f := newFixture(t, func(o *Options) { o.Max = 3 })
	var cs []*http.Cookie
	for range 3 {
		cs = append(cs, f.signIn(f.result("oidc:dev")))
		f.clk.Step(time.Second)
	}
	// The first session is the oldest but was used last, so the second is the oldest idle.
	f.wantMe(cs[0], 200, okBody)
	f.clk.Step(time.Second)
	cs = append(cs, f.signIn(f.result("oidc:dev")))
	if g := f.gauge(); g != "3" {
		t.Fatalf("gauge = %s", g)
	}
	f.wantMe(cs[1], 401, endedBody)
	for _, i := range []int{0, 2, 3} {
		f.wantMe(cs[i], 200, okBody)
	}
	got := f.audit.actions()
	if got[len(got)-1] != "login/success" || got[len(got)-2] != "session-evicted/failure/bound" {
		t.Fatalf("audit = %v", got)
	}
}

func TestSessionsGauge(t *testing.T) {
	f := newFixture(t, nil)
	if g := f.gauge(); g != "0" {
		t.Fatalf("initial gauge = %q", g)
	}
	a := f.signIn(f.result("oidc:dev"))
	b := f.signIn(f.result("oidc:dev"))
	if g := f.gauge(); g != "2" {
		t.Fatalf("gauge after two sign-ins = %s", g)
	}
	if rec := f.logout(a, "https://snellius.example.com"); rec.Code != 200 {
		t.Fatalf("logout = %d", rec.Code)
	}
	if g := f.gauge(); g != "1" {
		t.Fatalf("gauge after logout = %s", g)
	}
	f.clk.Step(31 * time.Minute)
	f.wantMe(b, 401, endedBody)
	if g := f.gauge(); g != "0" {
		t.Fatalf("gauge after expiry = %s", g)
	}
}

// No audit record or log line carries a token or the cookie value.
func TestAuditCarriesNoSecrets(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	f.logout(c, "https://snellius.example.com")
	b, _ := json.Marshal(f.audit.recs)
	for _, secret := range []string{c.Value, "id-token-value", "access-token-value", testRefresh} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("audit carries %q: %s", secret, b)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// An end notice lives for the absolute timeout after the end; then the cookie hears sign-in-required and is cleared.
func TestEndedNoticeExpires(t *testing.T) {
	f := newFixture(t, nil)
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(31 * time.Minute)
	f.wantMe(c, 401, endedBody)
	f.clk.Step(DefaultAbsolute - time.Second)
	f.wantMe(c, 401, endedBody)
	f.clk.Step(time.Second)
	rec := f.me(c)
	if got := strings.TrimSpace(rec.Body.String()); rec.Code != 401 || got != signInBody {
		t.Fatalf("expired notice = %d %s", rec.Code, got)
	}
	var cleared bool
	for _, ck := range rec.Result().Cookies() {
		cleared = cleared || ck.Name == CookieName && ck.MaxAge < 0
	}
	if !cleared {
		t.Fatal("sign-in-required did not clear the cookie")
	}
}

// At most Max end notices are held: notice Max+1 forgets the oldest, whose cookie then hears sign-in-required.
func TestEndedNoticeBound(t *testing.T) {
	f := newFixture(t, nil)
	cs := make([]*http.Cookie, DefaultMax)
	for i := range cs {
		cs[i] = f.signIn(f.result("oidc:dev"))
	}
	f.clk.Step(31 * time.Minute)
	// Each request ends one session; distinct times make the first notice the oldest.
	for _, c := range cs {
		f.clk.Step(time.Millisecond)
		f.wantMe(c, 401, endedBody)
	}
	last := f.signIn(f.result("oidc:dev"))
	f.clk.Step(31 * time.Minute)
	f.wantMe(last, 401, endedBody)
	f.wantMe(cs[0], 401, signInBody)
	f.wantMe(cs[1], 401, endedBody)
	f.wantMe(cs[DefaultMax-1], 401, endedBody)
	f.wantMe(last, 401, endedBody)
}
