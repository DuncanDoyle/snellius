package app

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
)

func oidcApp(t *testing.T, mode string, cfg oidc.Config) *App {
	t.Helper()
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil))
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}},
		Options{AuthMode: mode, OIDC: cfg, PollInterval: 10 * time.Millisecond, MetricsInterval: time.Hour, UI: ui, AuditOut: io.Discard}, extension.New())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func authStatus(t *testing.T, a *App, host string) (int, string) {
	t.Helper()
	req := httptest.NewRequest("GET", "/auth/status", nil)
	req.Host = host
	rec := httptest.NewRecorder()
	a.servedHandler().ServeHTTP(rec, req)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

func TestSecureModeNotReadyWithoutOIDC(t *testing.T) {
	a := oidcApp(t, ModeSecure, oidc.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)
	eventually(t, "an install in the model", func() bool { return len(a.Engine.Installs()) > 0 })
	ready, reason := a.readiness()()
	if ready || !strings.HasPrefix(reason, "sign-in not configured: ") {
		t.Fatalf("readiness = %v, %q; want not ready with a sign-in reason", ready, reason)
	}
	// Local mode ignores OIDC entirely.
	l := oidcApp(t, ModeLocal, oidc.Config{})
	l.Start(ctx)
	eventually(t, "an install in the model", func() bool { return len(l.Engine.Installs()) > 0 })
	eventually(t, "local mode ready", func() bool { ok, _ := l.readiness()(); return ok })
}

func TestAuthStatus(t *testing.T) {
	t.Run("local omits signIn", func(t *testing.T) {
		code, body := authStatus(t, oidcApp(t, ModeLocal, oidc.Config{}), "localhost:8080")
		if code != 200 || body != `{"mode":"local"}` {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("local keeps the Host guard", func(t *testing.T) {
		if code, _ := authStatus(t, oidcApp(t, ModeLocal, oidc.Config{}), "evil.example.com"); code != http.StatusMisdirectedRequest {
			t.Fatalf("code %d, want 421", code)
		}
	})
	t.Run("secure not configured", func(t *testing.T) {
		code, body := authStatus(t, oidcApp(t, ModeSecure, oidc.Config{IssuerURL: "http://x"}), "snellius.example.com")
		if code != 200 || body != `{"mode":"secure","signIn":"not-configured"}` {
			t.Fatalf("%d %s", code, body)
		}
	})
	t.Run("secure unavailable then ready", func(t *testing.T) {
		srv := httptest.NewTLSServer(nil)
		defer srv.Close()
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t"})
		})
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		a := oidcApp(t, ModeSecure, oidc.Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca})
		if _, body := authStatus(t, a, "s.example.com"); body != `{"mode":"secure","signIn":"unavailable"}` {
			t.Fatalf("before discovery: %s", body)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		a.Start(ctx)
		go a.oidc.Run(ctx)
		eventually(t, "sign-in ready", func() bool {
			_, b := authStatus(t, a, "s.example.com")
			return b == `{"mode":"secure","signIn":"ready"}`
		})
		eventually(t, "secure ready", func() bool { ok, _ := a.readiness()(); return ok })
	})
	t.Run("other methods are refused", func(t *testing.T) {
		rec := httptest.NewRecorder()
		oidcApp(t, ModeSecure, oidc.Config{}).servedHandler().ServeHTTP(rec, httptest.NewRequest("POST", "/auth/status", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code %d", rec.Code)
		}
	})
}

// Local mode warns once about OIDC flags it ignores, and only when one is set.
func TestWarnIgnoredOIDC(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	warnIgnoredOIDC(ModeLocal, oidc.Config{Scopes: oidc.DefaultScopes})
	warnIgnoredOIDC(ModeSecure, oidc.Config{ClientID: "c"})
	if buf.Len() != 0 {
		t.Fatalf("unexpected warning: %s", buf.String())
	}
	warnIgnoredOIDC(ModeLocal, oidc.Config{ClientID: "c"})
	if n := strings.Count(buf.String(), "ignored"); n != 1 {
		t.Fatalf("warnings = %d: %s", n, buf.String())
	}
}

// serve starts discovery in secure mode, so /readyz turns ready without extra wiring.
func TestServeRunsDiscovery(t *testing.T) {
	srv := httptest.NewTLSServer(nil)
	defer srv.Close()
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t"})
	})
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	a := oidcApp(t, ModeSecure, oidc.Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca})
	ctx, cancel := context.WithCancel(context.Background())
	ls, served := startServe(t, a, ctx)
	defer func() {
		cancel()
		if err := testutil.Recv(t, served, "serve to return"); err != nil {
			t.Error(err)
		}
	}()
	eventually(t, "/readyz 200", func() bool { code, _ := getBody(t, "http://"+ls.probe.Addr().String()+"/readyz"); return code == 200 })
}

// The sign-in routes exist only in secure mode; local mode answers 404.
func TestAuthRoutes(t *testing.T) {
	serve := func(a *App, path, host string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		a.servedHandler().ServeHTTP(rec, req)
		return rec
	}
	t.Run("local has none", func(t *testing.T) {
		a := oidcApp(t, ModeLocal, oidc.Config{})
		for _, p := range []string{"/auth/login", "/auth/callback"} {
			if rec := serve(a, p, "localhost:8080"); rec.Code != http.StatusNotFound {
				t.Errorf("local %s = %d, want 404", p, rec.Code)
			}
		}
	})
	t.Run("secure not configured", func(t *testing.T) {
		a := oidcApp(t, ModeSecure, oidc.Config{})
		for _, p := range []string{"/auth/login", "/auth/callback"} {
			if rec := serve(a, p, "s.example.com"); rec.Code != http.StatusServiceUnavailable {
				t.Errorf("%s = %d, want 503", p, rec.Code)
			}
		}
	})
	t.Run("secure redirects to the issuer", func(t *testing.T) {
		srv := httptest.NewTLSServer(nil)
		defer srv.Close()
		srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "authorization_endpoint": srv.URL + "/a", "token_endpoint": srv.URL + "/t"})
		})
		ca := filepath.Join(t.TempDir(), "ca.pem")
		if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
			t.Fatal(err)
		}
		a := oidcApp(t, ModeSecure, oidc.Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go a.oidc.Run(ctx)
		eventually(t, "discovery", func() bool { ok, _ := a.oidc.Ready(); return ok })
		rec := serve(a, "/auth/login?return=/x", "s.example.com")
		if rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), srv.URL+"/a?") {
			t.Fatalf("login = %d %q", rec.Code, rec.Header().Get("Location"))
		}
		c := rec.Result().Cookies()
		if len(c) != 1 || !c[0].Secure {
			t.Errorf("cookies = %+v; want one Secure login cookie by default", c)
		}
		// A callback without a transaction fails to /signin-failed.
		if rec := serve(a, "/auth/callback?state=x&code=y", "s.example.com"); rec.Header().Get("Location") != "/signin-failed?reason=invalid" {
			t.Errorf("callback = %d %q", rec.Code, rec.Header().Get("Location"))
		}
	})
	t.Run("unreadable client secret is not configured", func(t *testing.T) {
		a := oidcApp(t, ModeSecure, oidc.Config{IssuerURL: "https://idp.example.com", ClientID: "c", RedirectURL: "https://s.example.com/auth/callback",
			ClientSecretFile: filepath.Join(t.TempDir(), "missing")})
		if _, body := authStatus(t, a, "s.example.com"); body != `{"mode":"secure","signIn":"not-configured"}` {
			t.Fatalf("status = %s", body)
		}
	})
}
