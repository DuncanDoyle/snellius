// Package cli parses the flags the snellius binaries share, so the default,
// core-only and downstream binaries cannot drift apart.
package cli

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/internal/webui"
	"github.com/DuncanDoyle/snellius/pkg/app"
	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
)

// Parse registers the flags on fs and parses args. It returns the kubeconfig
// path and the app options, with the embedded SPA as the UI. A build from the
// module cache embeds only a placeholder, so a downstream build sets o.UI to its own.
func Parse(fs *flag.FlagSet, args []string) (kubeconfig string, o app.Options, err error) {
	o.UI = webui.FS()
	fs.StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig; empty: in-cluster config, else default kubeconfig loading rules")
	fs.StringVar(&o.Pinned, "install", "", "pin the UI to one controller install (install ID or namespace/name)")
	fs.StringVar(&o.Listen, "listen", "", "listen address (default 127.0.0.1:8080; :8080 in-cluster in secure mode)")
	fs.StringVar(&o.AuthMode, "auth-mode", app.ModeLocal, `"local": loopback only, Host-guarded; "secure": serve on the network behind an identity gate`)
	fs.StringVar(&o.OIDC.IssuerURL, "oidc-issuer-url", "", "secure mode: OIDC issuer URL (https)")
	fs.StringVar(&o.OIDC.ClientID, "oidc-client-id", "", "secure mode: OIDC client ID")
	fs.StringVar(&o.OIDC.ClientSecretFile, "oidc-client-secret-file", "", "secure mode: file holding the OIDC client secret; empty: public client")
	fs.StringVar(&o.OIDC.RedirectURL, "oidc-redirect-url", "", "secure mode: OIDC redirect URL, https://<host>/auth/callback; also register https://<host>/ as the post-logout redirect URI")
	fs.StringVar(&o.OIDC.CAFile, "oidc-ca-file", "", "secure mode: PEM bundle of extra CAs trusted for the OIDC issuer")
	cookieSecure := fs.Bool("cookie-secure", true, "secure mode: mark sign-in cookies Secure; false only for plain-HTTP testing")
	fs.DurationVar(&o.SessionIdle, "session-idle", 30*time.Minute, "secure mode: a session ends after this long without a request")
	fs.DurationVar(&o.SessionAbsolute, "session-absolute", 8*time.Hour, "secure mode: a session ends this long after sign-in")
	fs.DurationVar(&o.AuthzCacheTTL, "authz-cache-ttl", sar.DefaultAllowTTL, "secure mode: how long an allowed permission check is reused, 30s to 15m; a revoked permission can stay visible this long")
	fs.BoolVar(&o.AuthzNamedGetScan, "authz-named-get-scan", false, "secure mode: also show in lists the objects a viewer may only get by name (resourceNames grants); costs one check per hidden object")
	fs.IntVar(&o.AuthzMaxInFlight, "authz-max-in-flight", sar.DefaultMaxInFlight, "secure mode: most permission checks sent to the API server at once, across all viewers")
	fs.IntVar(&o.AuthzRate, "authz-rate", sar.DefaultRate, "secure mode: most permission checks sent to the API server per second, across all viewers, 1 to 10000")
	fs.IntVar(&o.AuthzBurst, "authz-burst", sar.DefaultBurst, "secure mode: permission checks sent above --authz-rate before it applies; the default covers one cold page")
	proxies := fs.String("trusted-proxy-cidrs", "", "comma-separated CIDRs of proxies whose X-Request-Id and X-Forwarded-For are trusted")
	scopes := fs.String("oidc-scopes", strings.Join(oidc.DefaultScopes, ","), "secure mode: comma-separated OIDC scopes")
	allowedHosts := fs.String("allowed-hosts", "", "comma-separated extra Host names local mode serves (any port)")
	fs.StringVar(&o.AuditLevels, "audit-levels", audit.DefaultLevels, "comma-separated audit levels to write: security, agent, reads, summaries, disclosure, requests")
	fs.StringVar(&o.ProbeListen, "probe-listen", "", "health-probe address (default :8081 in-cluster, 127.0.0.1:8081 locally)")
	fs.DurationVar(&o.PollInterval, "poll-interval", 30*time.Second, "maximum time between model recomputes (safety timer)")
	fs.DurationVar(&o.MetricsInterval, "metrics-interval", 10*time.Second, "controller metrics scrape interval")
	fs.IntVar(&o.DelegationEdgeCap, "delegation-edge-cap", 0, "selected delegation edges computed per install; 0: the default (10000). A higher cap needs more memory")
	var events, crds, proxyStats, endpoints bool
	fs.BoolVar(&events, "diagnostics-events", true, "watch Warning Events on controller and proxy Pods")
	fs.BoolVar(&crds, "diagnostics-crds", true, "watch Gateway API CRD metadata")
	fs.BoolVar(&proxyStats, "diagnostics-proxy-stats", true, "read proxy Envoy stats")
	fs.BoolVar(&endpoints, "diagnostics-endpoints", true, "watch Services and EndpointSlices")
	fs.BoolVar(&o.ClassOnlyInstalls, "class-only-installs", false, "also show an install per GatewayClass controllerName that no detector recognises")
	fs.BoolVar(&o.Diagnostics.ControllerLogs, "diagnostics-controller-logs", false, "read controller logs for derived signals")
	if err := fs.Parse(args); err != nil {
		return "", app.Options{}, err
	}
	if _, err := audit.ParseLevels(o.AuditLevels); err != nil {
		return "", app.Options{}, err
	}
	if o.SessionIdle <= 0 || o.SessionAbsolute <= 0 {
		return "", app.Options{}, fmt.Errorf("--session-idle (%v) and --session-absolute (%v) must be positive", o.SessionIdle, o.SessionAbsolute)
	}
	if o.AuthzCacheTTL < MinAuthzCacheTTL || o.AuthzCacheTTL > MaxAuthzCacheTTL {
		return "", app.Options{}, fmt.Errorf("--authz-cache-ttl (%v) must be between %v and %v", o.AuthzCacheTTL, MinAuthzCacheTTL, MaxAuthzCacheTTL)
	}
	if o.AuthzMaxInFlight < 1 || o.AuthzRate < 1 || o.AuthzBurst < 1 {
		return "", app.Options{}, fmt.Errorf("--authz-max-in-flight (%d), --authz-rate (%d) and --authz-burst (%d) must be at least 1", o.AuthzMaxInFlight, o.AuthzRate, o.AuthzBurst)
	}
	if o.AuthzRate > sar.MaxRate {
		return "", app.Options{}, fmt.Errorf("--authz-rate (%d) must be at most %d", o.AuthzRate, sar.MaxRate)
	}
	if o.TrustedProxyCIDRs, err = audit.ParseCIDRs(*proxies); err != nil {
		return "", app.Options{}, err
	}
	o.OIDC.Scopes = splitList(*scopes)
	o.CookieInsecure = !*cookieSecure
	for _, h := range strings.Split(*allowedHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			o.AllowedHosts = append(o.AllowedHosts, h)
		}
	}
	o.Diagnostics.NoEvents, o.Diagnostics.NoCRDs, o.Diagnostics.NoProxyStats, o.Diagnostics.NoEndpoints = !events, !crds, !proxyStats, !endpoints
	return kubeconfig, o, nil
}

// The --authz-cache-ttl range (design §6.2).
const (
	MinAuthzCacheTTL = 30 * time.Second
	MaxAuthzCacheTTL = 15 * time.Minute
)

// splitList splits a comma list, trimming blanks.
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
