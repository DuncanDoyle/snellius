// Package cli parses the flags the snellius binaries share, so the default,
// core-only and downstream binaries cannot drift apart.
package cli

import (
	"flag"
	"strings"
	"time"

	"github.com/DuncanDoyle/snellius/internal/webui"
	"github.com/DuncanDoyle/snellius/pkg/app"
)

// Parse registers the flags on fs and parses args. It returns the kubeconfig
// path and the app options, with the embedded SPA as the UI. A build from the
// module cache embeds only a placeholder, so a downstream build sets o.UI to its own.
func Parse(fs *flag.FlagSet, args []string) (kubeconfig string, o app.Options, err error) {
	o.UI = webui.FS()
	fs.StringVar(&kubeconfig, "kubeconfig", "", "path to a kubeconfig; empty: in-cluster config, else default kubeconfig loading rules")
	fs.StringVar(&o.Pinned, "install", "", "pin the UI to one controller install (install ID or namespace/name)")
	fs.StringVar(&o.Listen, "listen", "", "listen address (default :8080 in-cluster, 127.0.0.1:8080 locally)")
	fs.StringVar(&o.AuthMode, "auth-mode", app.ModeLocal, `"local": loopback only, Host-guarded; "secure": serve on the network behind an identity gate`)
	allowedHosts := fs.String("allowed-hosts", "", "comma-separated extra Host names local mode serves (any port)")
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
	for _, h := range strings.Split(*allowedHosts, ",") {
		if h = strings.TrimSpace(h); h != "" {
			o.AllowedHosts = append(o.AllowedHosts, h)
		}
	}
	o.Diagnostics.NoEvents, o.Diagnostics.NoCRDs, o.Diagnostics.NoProxyStats, o.Diagnostics.NoEndpoints = !events, !crds, !proxyStats, !endpoints
	return kubeconfig, o, nil
}
