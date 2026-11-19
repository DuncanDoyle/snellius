package app

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// Auth modes. Local serves only through loopback; secure expects an identity gate.
const (
	ModeLocal  = "local"
	ModeSecure = "secure"
)

// validateMode rejects an unknown auth mode; empty means local.
func validateMode(mode string) error {
	switch mode {
	case "", ModeLocal, ModeSecure:
		return nil
	}
	return fmt.Errorf("unknown --auth-mode %q: want %q or %q", mode, ModeLocal, ModeSecure)
}

// checkListen refuses, in local mode, a non-loopback listen address and a pod
// that shares the node network (equal pod and host IP). Secure mode allows any.
func checkListen(mode, addr, podIP, hostIP string) error {
	if mode == ModeSecure {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", addr, err)
	}
	if host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("auth mode local listens only on loopback, not %q; use --auth-mode secure to serve on the network", addr)
		}
	}
	if podIP != "" && podIP == hostIP {
		return fmt.Errorf("auth mode local refuses host networking (pod IP %s equals host IP)", podIP)
	}
	return nil
}

// servedHandler builds the handler that is served: the API behind the Host
// guard in local mode, behind the session check in secure mode (applied inside
// the API's request audit, so refusals are audited), and /auth/status and the
// sign-in routes outside the API, all behind request IDs.
func (a *App) servedHandler() http.Handler {
	h := a.withAuthStatus(a.withSignIn(a.Handler))
	if a.opts.AuthMode == ModeSecure {
		return audit.RequestIDTrusting(a.opts.TrustedProxyCIDRs, h)
	}
	return audit.RequestIDTrusting(a.opts.TrustedProxyCIDRs, api.HostGuard(a.opts.AllowedHosts, h))
}

// withSignIn serves /auth/login, /auth/callback, /auth/me and /auth/logout ahead
// of next in secure mode; login and callback are 503 while sign-in is not
// configured. Local mode has no such routes: 404.
func (a *App) withSignIn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/login", "/auth/callback", "/auth/me", "/auth/logout":
		default:
			next.ServeHTTP(w, r)
			return
		}
		switch {
		case a.mode() != ModeSecure || a.sessions == nil:
			http.NotFound(w, r)
		case r.URL.Path == "/auth/me":
			a.sessions.Me(w, r)
		case r.URL.Path == "/auth/logout":
			a.sessions.Logout(w, r)
		case a.flow == nil:
			w.Header().Set("Cache-Control", "no-store")
			http.Error(w, "sign-in is not configured", http.StatusServiceUnavailable)
		case r.URL.Path == "/auth/login":
			a.flow.Login(w, r)
		default:
			a.flow.Callback(w, r)
		}
	})
}

// withAuthStatus serves GET /auth/status ahead of next, outside the secure gate.
func (a *App) withAuthStatus(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/status" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		st := struct {
			Mode   string `json:"mode"`
			SignIn string `json:"signIn,omitempty"`
		}{Mode: a.mode()}
		if st.Mode == ModeSecure {
			st.SignIn = a.signInState()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(st)
	})
}

// Sign-in states reported by /auth/status.
const (
	signInReady         = "ready"
	signInNotConfigured = "not-configured"
	signInUnavailable   = "unavailable"
)

// signInState is the secure-mode sign-in state, without reasons.
func (a *App) signInState() string {
	switch ready, _ := a.oidcReady(); {
	case a.oidcErr != nil:
		return signInNotConfigured
	case ready:
		return signInReady
	}
	return signInUnavailable
}

// oidcReady reports whether sign-in works; the reason is for the log only.
func (a *App) oidcReady() (bool, string) {
	if a.oidcErr != nil {
		return false, "sign-in not configured: " + a.oidcErr.Error()
	}
	if a.oidc == nil {
		return false, "sign-in not configured: no OIDC provider"
	}
	if ok, err := a.oidc.Ready(); !ok {
		return false, "sign-in unavailable: " + err.Error()
	}
	return true, ""
}

// readiness is the probe's readiness: the model, and in secure mode sign-in too.
func (a *App) readiness() api.Ready {
	model := api.ModelReady(a.Engine)
	if a.mode() != ModeSecure {
		return model
	}
	return func() (bool, string) {
		if ok, why := model(); !ok {
			return false, why
		}
		return a.oidcReady()
	}
}

// warnIgnoredOIDC logs once when local mode is given OIDC settings it ignores.
func warnIgnoredOIDC(mode string, c oidc.Config) {
	if mode == ModeLocal && c.Set() {
		slog.Warn("OIDC flags are ignored in local mode; use --auth-mode secure to sign in")
	}
}

// mode returns the effective auth mode; empty means local.
func (a *App) mode() string {
	if a.opts.AuthMode == "" {
		return ModeLocal
	}
	return a.opts.AuthMode
}

// podHostIPs returns the downward-API POD_IP and HOST_IP, empty when unset.
func podHostIPs() (podIP, hostIP string) {
	return os.Getenv("POD_IP"), os.Getenv("HOST_IP")
}

// noAuthorizer reports whether a is unset or either form of api.AllowAll,
// the registry default that secure mode never serves with.
func noAuthorizer(a api.Authorizer) bool {
	switch a.(type) {
	case nil, api.AllowAll, *api.AllowAll:
		return true
	}
	return false
}

// drainTimeout bounds how long serve waits for in-flight requests at shutdown.
var drainTimeout = 5 * time.Second
