package app

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/audit"
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
// guard in local mode, the API alone in secure mode, all behind request IDs.
func (a *App) servedHandler() http.Handler {
	if a.opts.AuthMode == ModeSecure {
		return audit.RequestID(a.Handler)
	}
	return audit.RequestID(api.HostGuard(a.opts.AllowedHosts, a.Handler))
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
