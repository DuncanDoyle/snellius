package oidc

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"
)

func TestValidateConfig(t *testing.T) {
	ok := Config{IssuerURL: "https://idp.example.com", ClientID: "snellius", RedirectURL: "https://snellius.example.com/auth/callback"}
	with := func(f func(*Config)) Config { c := ok; f(&c); return c }
	for name, c := range map[string]struct {
		cfg  Config
		want string
	}{
		"valid":                      {ok, ""},
		"valid loopback redirect":    {with(func(c *Config) { c.RedirectURL = "http://localhost:8080/auth/callback" }), ""},
		"valid 127.0.0.1 redirect":   {with(func(c *Config) { c.RedirectURL = "http://127.0.0.1:8080/auth/callback" }), ""},
		"valid scopes":               {with(func(c *Config) { c.Scopes = []string{"openid", "email"} }), ""},
		"missing issuer":             {with(func(c *Config) { c.IssuerURL = "" }), "issuer"},
		"http issuer":                {with(func(c *Config) { c.IssuerURL = "http://idp.example.com" }), "issuer"},
		"http loopback issuer":       {with(func(c *Config) { c.IssuerURL = "http://localhost:5556" }), "issuer"},
		"relative issuer":            {with(func(c *Config) { c.IssuerURL = "idp.example.com" }), "issuer"},
		"issuer with fragment":       {with(func(c *Config) { c.IssuerURL = "https://idp.example.com#x" }), "issuer"},
		"missing client id":          {with(func(c *Config) { c.ClientID = "" }), "client ID"},
		"missing redirect":           {with(func(c *Config) { c.RedirectURL = "" }), "redirect"},
		"http redirect":              {with(func(c *Config) { c.RedirectURL = "http://snellius.example.com/cb" }), "redirect"},
		"http lookalike redirect":    {with(func(c *Config) { c.RedirectURL = "http://localhost.example.com/cb" }), "redirect"},
		"scopes without openid":      {with(func(c *Config) { c.Scopes = []string{"email"} }), "openid"},
		"all missing names each":     {Config{}, "issuer"},
		"all missing names client":   {Config{}, "client ID"},
		"all missing names redirect": {Config{}, "redirect"},
	} {
		t.Run(name, func(t *testing.T) {
			err := c.cfg.Validate()
			switch {
			case c.want == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
				t.Fatalf("Validate() = %v, want error containing %q", err, c.want)
			}
		})
	}
}

// idp serves one discovery document and counts the requests.
func idp(t *testing.T, doc func(issuer string) map[string]any, fail int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/.well-known/openid-configuration" {
			http.NotFound(w, r)
			return
		}
		if calls.Add(1) <= fail {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(doc(srv.URL))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func goodDoc(issuer string) map[string]any {
	return map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/auth", "token_endpoint": issuer + "/token",
		"end_session_endpoint": issuer + "/logout"}
}

func provider(t *testing.T, srv *httptest.Server) *Provider {
	t.Helper()
	p, err := NewProvider(Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback"}, clocktesting.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDiscovery(t *testing.T) {
	srv, _ := idp(t, goodDoc, 0)
	ep, err := provider(t, srv).Discover(context.Background(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if ep.AuthorizationEndpoint != srv.URL+"/auth" || ep.TokenEndpoint != srv.URL+"/token" || ep.EndSessionEndpoint != srv.URL+"/logout" {
		t.Errorf("endpoints = %+v", ep)
	}
}

func TestDiscoveryIssuerMismatch(t *testing.T) {
	for name, issuer := range map[string]func(string) string{
		"other host":     func(string) string { return "https://evil.example.com" },
		"trailing slash": func(s string) string { return s + "/" },
	} {
		t.Run(name, func(t *testing.T) {
			srv, _ := idp(t, func(s string) map[string]any { d := goodDoc(s); d["issuer"] = issuer(s); return d }, 0)
			if _, err := provider(t, srv).Discover(context.Background(), srv.Client()); err == nil || !strings.Contains(err.Error(), "issuer") {
				t.Fatalf("Discover() = %v, want an issuer mismatch error", err)
			}
		})
	}
}

func TestDiscoveryRejectsHTTPEndpoints(t *testing.T) {
	for _, field := range []string{"authorization_endpoint", "token_endpoint"} {
		t.Run(field, func(t *testing.T) {
			srv, _ := idp(t, func(s string) map[string]any { d := goodDoc(s); d[field] = "http://idp.example.com/x"; return d }, 0)
			if _, err := provider(t, srv).Discover(context.Background(), srv.Client()); err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("Discover() = %v, want an error naming %s", err, field)
			}
		})
	}
	t.Run("missing token endpoint", func(t *testing.T) {
		srv, _ := idp(t, func(s string) map[string]any { d := goodDoc(s); delete(d, "token_endpoint"); return d }, 0)
		if _, err := provider(t, srv).Discover(context.Background(), srv.Client()); err == nil {
			t.Fatal("Discover() = nil, want error")
		}
	})
	t.Run("loopback http is allowed", func(t *testing.T) {
		srv, _ := idp(t, func(s string) map[string]any {
			d := goodDoc(s)
			d["authorization_endpoint"], d["token_endpoint"] = "http://127.0.0.1:5556/auth", "http://localhost:5556/token"
			return d
		}, 0)
		if _, err := provider(t, srv).Discover(context.Background(), srv.Client()); err != nil {
			t.Fatal(err)
		}
	})
}

func TestDiscoveryRetries(t *testing.T) {
	srv, calls := idp(t, goodDoc, 2)
	clk := clocktesting.NewFakeClock(time.Now())
	p, err := NewProvider(Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback"}, clk)
	if err != nil {
		t.Fatal(err)
	}
	p.client = srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	waitFor(t, "a retry timer", clk.HasWaiters)
	if ready, err := p.Ready(); ready || err == nil {
		t.Fatalf("after the first failure Ready() = %v, %v; want false and the last error", ready, err)
	}
	clk.Step(time.Second)
	waitFor(t, "a second retry timer", func() bool { return calls.Load() == 2 && clk.HasWaiters() })
	clk.Step(2 * time.Second)
	waitFor(t, "discovery to succeed", func() bool { ready, _ := p.Ready(); return ready })
	if n := calls.Load(); n != 3 {
		t.Errorf("discovery calls = %d, want 3", n)
	}
	if p.Endpoints() == nil {
		t.Error("Endpoints() = nil after success")
	}
	cancel()
	<-done
}

func TestRunStopsOnCancel(t *testing.T) {
	srv, _ := idp(t, goodDoc, 1<<30)
	clk := clocktesting.NewFakeClock(time.Now())
	p, _ := NewProvider(Config{IssuerURL: srv.URL}, clk)
	p.client = srv.Client()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	waitFor(t, "a retry timer", clk.HasWaiters)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestBackoffDoublesToACap(t *testing.T) {
	d := time.Second
	var got []time.Duration
	for range 9 {
		got = append(got, d)
		d = nextBackoff(d)
	}
	want := []time.Duration{1, 2, 4, 8, 16, 32, 60, 60, 60}
	for i := range want {
		if got[i] != want[i]*time.Second {
			t.Fatalf("backoff = %v, want %v seconds", got, want)
		}
	}
}

func TestCAFile(t *testing.T) {
	srv, _ := idp(t, goodDoc, 0)
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := write("ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	cfg := Config{IssuerURL: srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/cb"}

	// Without the CA the private issuer is not trusted.
	p, err := NewProvider(cfg, clocktesting.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Discover(context.Background(), p.HTTPClient()); err == nil {
		t.Fatal("Discover() trusted a private CA that was not configured")
	}

	cfg.CAFile = good
	p, err = NewProvider(cfg, clocktesting.NewFakeClock(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Discover(context.Background(), p.HTTPClient()); err != nil {
		t.Fatalf("Discover() with the CA = %v", err)
	}

	for name, path := range map[string]string{"missing": filepath.Join(dir, "none.pem"), "not pem": write("bad.pem", []byte("nope"))} {
		cfg.CAFile = path
		if _, err := NewProvider(cfg, clocktesting.NewFakeClock(time.Now())); err == nil {
			t.Errorf("%s CA file: NewProvider() = nil, want error", name)
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
		time.Sleep(5 * time.Millisecond)
	}
}
