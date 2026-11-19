package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8stesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
)

// signInIdP is a TLS issuer whose token endpoint returns an ID token for the last login's nonce.
type signInIdP struct {
	srv   *httptest.Server
	mu    sync.Mutex
	nonce string
}

func newSignInIdP(t *testing.T) (*signInIdP, string) {
	t.Helper()
	p := &signInIdP{}
	p.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/a",
				"token_endpoint": p.srv.URL + "/t", "end_session_endpoint": p.srv.URL + "/logout"})
		case "/t":
			p.mu.Lock()
			nonce := p.nonce
			p.mu.Unlock()
			enc := func(v any) string { b, _ := json.Marshal(v); return base64.RawURLEncoding.EncodeToString(b) }
			now := time.Now()
			idTok := enc(map[string]string{"alg": "RS256"}) + "." + enc(map[string]any{"iss": p.srv.URL, "sub": "alice-sub", "aud": "c",
				"exp": now.Add(time.Hour).Unix(), "iat": now.Unix(), "nonce": nonce}) + ".c2ln"
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "a", "token_type": "Bearer", "refresh_token": "r", "id_token": idTok})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p, ca
}

// sessionApp is a secure-mode app whose cluster authenticates every token as oidc:alice.
func sessionApp(t *testing.T, cfg oidc.Config) *App {
	t.Helper()
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil))
	kube.PrependReactor("create", "tokenreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: true,
			User: authnv1.UserInfo{Username: "oidc:alice", Groups: []string{"oidc:dev"}}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}},
		Options{AuthMode: ModeSecure, OIDC: cfg, PollInterval: 10 * time.Millisecond, MetricsInterval: time.Hour, UI: ui, AuditOut: io.Discard},
		extension.New())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// call serves one request through the served handler.
func call(a *App, method, target string, cookies []*http.Cookie, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.Host = "s.example.com"
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	a.servedHandler().ServeHTTP(rec, req)
	return rec
}

// A sign-in through the served routes starts a session that /auth/me, the API gate and logout see.
func TestSecureSessionFlow(t *testing.T) {
	idp, ca := newSignInIdP(t)
	a := sessionApp(t, oidc.Config{IssuerURL: idp.srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.oidc.Run(ctx)
	eventually(t, "discovery", func() bool { ok, _ := a.oidc.Ready(); return ok })

	if rec := call(a, "GET", "/api/v1/meta", nil, nil); rec.Code != 401 ||
		strings.TrimSpace(rec.Body.String()) != `{"error":"sign-in-required","login":"/auth/login?return=%2F"}` {
		t.Fatalf("API without a session = %d %s", rec.Code, rec.Body.String())
	}
	login := call(a, "GET", "/auth/login?return=/routes", nil, nil)
	loc, _ := url.Parse(login.Header().Get("Location"))
	idp.mu.Lock()
	idp.nonce = loc.Query().Get("nonce")
	idp.mu.Unlock()
	cb := call(a, "GET", "/auth/callback?code=x&state="+url.QueryEscape(loc.Query().Get("state")), login.Result().Cookies(), nil)
	if cb.Code != http.StatusFound || cb.Header().Get("Location") != "/routes#signed-in" {
		t.Fatalf("callback = %d %q", cb.Code, cb.Header().Get("Location"))
	}
	var sess []*http.Cookie
	for _, c := range cb.Result().Cookies() {
		if c.Name == "snellius_session" {
			sess = append(sess, c)
		}
	}
	if len(sess) != 1 || !sess[0].Secure {
		t.Fatalf("session cookies = %+v", cb.Result().Cookies())
	}
	if rec := call(a, "GET", "/auth/me", sess, nil); rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"username":"oidc:alice","groups":["oidc:dev"]}` {
		t.Fatalf("me = %d %s", rec.Code, rec.Body.String())
	}
	// With a session the API serves.
	if rec := call(a, "GET", "/api/v1/meta", sess, nil); rec.Code != http.StatusOK {
		t.Fatalf("API with a session = %d %s", rec.Code, rec.Body.String())
	}
	if m := metricsText(a); !strings.Contains(m, "snellius_sessions_active 1") {
		t.Fatalf("metrics:\n%s", m)
	}
	out := call(a, "POST", "/auth/logout", sess, map[string]string{"Origin": "https://s.example.com"})
	want := idp.srv.URL + "/logout?client_id=c&post_logout_redirect_uri=" + url.QueryEscape("https://s.example.com/")
	var next struct{ Next string }
	if err := json.Unmarshal(out.Body.Bytes(), &next); err != nil || out.Code != 200 || next.Next != want {
		t.Fatalf("logout = %d %s; want next %s", out.Code, out.Body.String(), want)
	}
	if rec := call(a, "GET", "/auth/me", sess, nil); rec.Code != 401 {
		t.Fatalf("me after logout = %d", rec.Code)
	}
	if m := metricsText(a); !strings.Contains(m, "snellius_sessions_active 0") {
		t.Fatalf("metrics:\n%s", m)
	}
}

// The signed-in fragment goes on the validated return path, so a foreign return still stays on this origin.
func TestSignedInFragmentKeepsReturnLocal(t *testing.T) {
	idp, ca := newSignInIdP(t)
	a := sessionApp(t, oidc.Config{IssuerURL: idp.srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.oidc.Run(ctx)
	eventually(t, "discovery", func() bool { ok, _ := a.oidc.Ready(); return ok })
	for _, ret := range []string{"//evil.example", "https://evil.example/", "/%5Cevil.example", "/routes#//evil.example"} {
		login := call(a, "GET", "/auth/login?return="+url.QueryEscape(ret), nil, nil)
		loc, _ := url.Parse(login.Header().Get("Location"))
		idp.mu.Lock()
		idp.nonce = loc.Query().Get("nonce")
		idp.mu.Unlock()
		cb := call(a, "GET", "/auth/callback?code=x&state="+url.QueryEscape(loc.Query().Get("state")), login.Result().Cookies(), nil)
		got, err := url.Parse(cb.Header().Get("Location"))
		if cb.Code != http.StatusFound || err != nil || got.Host != "" || got.Scheme != "" || got.Fragment != "signed-in" {
			t.Errorf("return %q: callback = %d %q", ret, cb.Code, cb.Header().Get("Location"))
		}
	}
}

// Session routes exist only in secure mode; without OIDC, logout still refuses a foreign origin.
func TestSessionRoutesByMode(t *testing.T) {
	l := oidcApp(t, ModeLocal, oidc.Config{})
	for _, p := range []string{"/auth/me", "/auth/logout"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "localhost:8080"
		rec := httptest.NewRecorder()
		l.servedHandler().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("local %s = %d, want 404", p, rec.Code)
		}
	}
	s := oidcApp(t, ModeSecure, oidc.Config{})
	if rec := call(s, "GET", "/auth/me", nil, nil); rec.Code != 401 {
		t.Errorf("secure me = %d, want 401", rec.Code)
	}
	if rec := call(s, "POST", "/auth/logout", nil, map[string]string{"Origin": "https://s.example.com"}); rec.Code != http.StatusForbidden {
		t.Errorf("unconfigured logout = %d, want 403", rec.Code)
	}
}

func metricsText(a *App) string {
	rec := httptest.NewRecorder()
	a.Metrics.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}
