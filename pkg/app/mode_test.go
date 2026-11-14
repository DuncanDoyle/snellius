package app

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

func TestCheckListenLocalRefusesNonLoopback(t *testing.T) {
	for _, addr := range []string{":8080", "0.0.0.0:8080", "10.0.0.5:8080", "[::]:8080"} {
		if err := checkListen(ModeLocal, addr, "", ""); err == nil {
			t.Errorf("checkListen(local, %q) = nil, want error", addr)
		}
	}
	for _, addr := range []string{"127.0.0.1:8080", "[::1]:8080", "localhost:8080", "127.0.0.2:8080"} {
		if err := checkListen(ModeLocal, addr, "", ""); err != nil {
			t.Errorf("checkListen(local, %q) = %v, want nil", addr, err)
		}
	}
}

func TestCheckListenSecureAllowsAnyAddress(t *testing.T) {
	if err := checkListen(ModeSecure, ":8080", "10.0.0.5", "10.0.0.9"); err != nil {
		t.Errorf("secure mode: %v", err)
	}
}

func TestCheckListenRefusesHostNetwork(t *testing.T) {
	if err := checkListen(ModeLocal, "127.0.0.1:8080", "10.0.0.5", "10.0.0.5"); err == nil {
		t.Error("equal pod and host IP: want error")
	}
	if err := checkListen(ModeLocal, "127.0.0.1:8080", "10.0.0.5", "10.0.0.9"); err != nil {
		t.Errorf("different IPs: %v", err)
	}
	if err := checkListen(ModeLocal, "127.0.0.1:8080", "", ""); err != nil {
		t.Errorf("unset IPs: %v", err)
	}
}

func TestValidateMode(t *testing.T) {
	for _, m := range []string{"", ModeLocal, ModeSecure} {
		if err := validateMode(m); err != nil {
			t.Errorf("validateMode(%q) = %v", m, err)
		}
	}
	if err := validateMode("bogus"); err == nil {
		t.Error("validateMode(bogus) = nil, want error")
	}
}

// Secure mode without a registry authoriser wires DenyAll; local mode keeps AllowAll.
func TestSecureModeWiresDenyAll(t *testing.T) {
	dyn, kube := testutil.Clients(t)
	for mode, wantAllow := range map[string]bool{ModeSecure: false, ModeLocal: true} {
		a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}},
			Options{AuthMode: mode, PollInterval: 10 * time.Millisecond, MetricsInterval: time.Hour, UI: ui, AuditOut: io.Discard}, extension.New())
		if err != nil {
			t.Fatal(err)
		}
		if got := a.authz.Authorize(httptest.NewRequest("GET", "/", nil), objects.Ref{}); got != wantAllow {
			t.Errorf("%s: authoriser allows %v, want %v", mode, got, wantAllow)
		}
	}
}

// Secure mode replaces either concrete AllowAll with DenyAll and keeps a custom authoriser.
func TestSecureModeReplacesAllowAllForms(t *testing.T) {
	dyn, kube := testutil.Clients(t)
	custom := api.AuthorizerFunc(func(*http.Request, objects.Ref) bool { return true })
	for name, c := range map[string]struct {
		authz api.Authorizer
		want  string
	}{
		"value":   {api.AllowAll{}, "api.DenyAll"},
		"pointer": {&api.AllowAll{}, "api.DenyAll"},
		"custom":  {custom, "api.AuthorizerFunc"},
	} {
		a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}},
			Options{AuthMode: ModeSecure, PollInterval: 10 * time.Millisecond, MetricsInterval: time.Hour, UI: ui, AuditOut: io.Discard},
			extension.New(extension.WithAuthorizer(c.authz)))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%T", a.authz); got != c.want {
			t.Errorf("%s: secure-mode authoriser is %s, want %s", name, got, c.want)
		}
	}
}

// The served handler keeps a proxy's request ID only from a configured CIDR.
func TestServedHandlerTrustsProxyCIDRs(t *testing.T) {
	a := oidcApp(t, ModeSecure, oidc.Config{})
	a.opts.TrustedProxyCIDRs = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}
	for peer, keep := range map[string]bool{"10.0.0.7:1234": true, "192.0.2.1:1234": false} {
		req := httptest.NewRequest("GET", "/auth/status", nil)
		req.RemoteAddr = peer
		req.Header.Set("X-Request-Id", "proxy-id")
		rec := httptest.NewRecorder()
		a.servedHandler().ServeHTTP(rec, req)
		if got := rec.Header().Get("X-Request-Id") == "proxy-id"; got != keep {
			t.Errorf("peer %s: kept = %v, want %v", peer, got, keep)
		}
	}
}
