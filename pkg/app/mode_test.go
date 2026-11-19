package app

import (
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
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
