// Package app wires the backend together. Both the upstream main and
// downstream mains use it; downstreams pass extension options.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/metadata"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/session"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/collect"
	"github.com/DuncanDoyle/snellius/pkg/ctrllogs"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/endpoints"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
	"github.com/DuncanDoyle/snellius/pkg/proxystats"
	"github.com/DuncanDoyle/snellius/pkg/setup"
)

// Options are the runtime settings.
type Options struct {
	// Listen is the HTTP address; empty means 127.0.0.1:8080, or :8080 in-cluster in secure mode.
	Listen string
	// AuthMode is "local" (default: loopback only, Host-guarded) or "secure".
	AuthMode string
	// OIDC configures sign-in in secure mode; local mode ignores it.
	OIDC oidc.Config
	// CookieInsecure drops the Secure attribute from sign-in cookies, for plain-HTTP testing only.
	CookieInsecure bool
	// SessionIdle and SessionAbsolute bound a secure-mode session; zero means 30 min and 8 h.
	SessionIdle, SessionAbsolute time.Duration
	// TrustedProxyCIDRs are the peers whose X-Request-Id and X-Forwarded-For are trusted.
	TrustedProxyCIDRs []netip.Prefix
	// AllowedHosts are extra Host names local mode serves, besides loopback.
	AllowedHosts []string
	// AuditLevels is the comma list of audit levels to write; empty means audit.DefaultLevels.
	AuditLevels string
	// AuditOut receives the audit JSON lines; nil means stdout.
	AuditOut io.Writer
	// ProbeListen is the health-probe address; empty means :8081 in-cluster and 127.0.0.1:8081 locally.
	ProbeListen string
	// Pinned restricts the UI to one install: its ID, or namespace/name (D-016).
	Pinned string
	// PollInterval is the maximum time between model recomputes: the safety
	// timer. Object, access, metrics and served-kind changes recompute sooner (D-019).
	PollInterval time.Duration
	// MetricsInterval is the controller metrics scrape interval.
	MetricsInterval time.Duration
	// DelegationEdgeCap caps the selected delegation edges per install (rules
	// §5.7); zero means computed.DefaultBounds.Edges. A higher cap needs more
	// memory (D-025).
	DelegationEdgeCap int
	// KindsInterval is how often served kinds are re-resolved; zero means 60 s.
	KindsInterval time.Duration
	// UI is the single-page application served at /.
	UI fs.FS
	// ConfirmEvery is the engine's confirm tick interval; zero means
	// engine.ConfirmEvery. Tests only.
	ConfirmEvery time.Duration
	// OnLoop receives the engine's loop transitions. Tests only.
	OnLoop func(engine.LoopEvent)
	// Clock drives the engine and stamps snapshots and collected values; nil
	// means the real clock. Tests only.
	Clock clock.WithTicker
	// Diagnostics toggles the optional inputs; the zero value keeps the defaults.
	Diagnostics Diagnostics
	// ClassOnlyInstalls adds an install per GatewayClass controllerName that no
	// detector claims, known from Gateway API status alone.
	ClassOnlyInstalls bool
}

// Diagnostics toggles the optional inputs (design §18.1): No* turns off a
// default-on input, ControllerLogs turns on a default-off one.
type Diagnostics struct {
	NoEvents, NoCRDs, NoProxyStats, NoEndpoints bool
	ControllerLogs                              bool
	// ControllerLogsNamespaces are the install namespaces whose controller logs are read.
	ControllerLogsNamespaces []string
}

// CoreCapabilities returns the core inputs' states for /api/v1/meta; a syncing
// or unstarted watch, and proxy stats before their first round, have none.
func CoreCapabilities(snap *cache.Snapshot, d Diagnostics, collected collect.Snapshot) []api.Capability {
	inputs := []struct {
		name string
		off  bool
		gvr  schema.GroupVersionResource
	}{
		{"processes", false, cache.ProxyPodsGVR},
		{"events", d.NoEvents, cache.EventsGVR},
		{"crds", d.NoCRDs, cache.CRDsGVR},
	}
	var out []api.Capability
	for _, in := range inputs {
		if in.off {
			out = append(out, api.Capability{Name: in.name, State: api.CapOff})
			continue
		}
		if snap == nil {
			continue
		}
		a, running := snap.Access[in.gvr]
		switch why := health.AccessAvailability(a); {
		case !running:
		case a.OK:
			out = append(out, api.Capability{Name: in.name, State: api.CapOn})
		case why == health.AvailDenied:
			out = append(out, api.Capability{Name: in.name, State: api.CapDenied, Reason: reasonDenied})
		case why == health.AvailFailing:
			out = append(out, api.Capability{Name: in.name, State: api.CapFailing, Reason: reasonFailing})
		}
	}
	if d.NoEndpoints {
		out = append(out, api.Capability{Name: "endpoints", State: api.CapOff})
	} else if c, ok := endpointsCapability(snap); ok {
		out = append(out, c)
	}
	// Per-Gateway failures stay in the value; only a stopped or stuck collector is failing here.
	switch v, ok := collected.Get(proxystats.Key); {
	case d.NoProxyStats:
		out = append(out, api.Capability{Name: "proxyStats", State: api.CapOff})
	case !ok:
	case v.State == collect.OK:
		out = append(out, api.Capability{Name: "proxyStats", State: api.CapOn})
	case v.State == collect.Off:
		out = append(out, api.Capability{Name: "proxyStats", State: api.CapOff})
	default:
		out = append(out, api.Capability{Name: "proxyStats", State: api.CapFailing, Reason: reasonStatsFailing})
	}
	if !d.ControllerLogs {
		return append(out, api.Capability{Name: "controllerLogs", State: api.CapOff})
	}
	st, ok := ctrllogs.Availability(collected)
	if !ok {
		return out
	}
	c := api.Capability{Name: "controllerLogs", State: api.CapFailing}
	switch st {
	case ctrllogs.StateOn:
		c.State = api.CapOn
	case ctrllogs.StateDenied:
		c.State, c.Reason = api.CapDenied, reasonDenied
	case ctrllogs.StateUnrecognised:
		c.Reason = reasonLogsUnrecognised
	case ctrllogs.StateNoParser:
		c.Reason = reasonLogsNoParser
	case ctrllogs.StateNotConnected:
		c.Reason = reasonLogsNotConnected
	default:
		c.Reason = reasonLogsFailing
	}
	return append(out, c)
}

// endpointsCapability merges the Service and EndpointSlice watches: on only when
// both run, denied when either is, else failing. ok is false while either is
// missing or syncing.
func endpointsCapability(snap *cache.Snapshot) (api.Capability, bool) {
	if snap == nil {
		return api.Capability{}, false
	}
	var failing, denied bool
	for _, gvr := range []schema.GroupVersionResource{cache.ServicesGVR, cache.EndpointSlicesGVR} {
		a, running := snap.Access[gvr]
		switch why := health.AccessAvailability(a); {
		case !running:
			return api.Capability{}, false
		case a.OK:
		case why == health.AvailDenied:
			denied = true
		case why == health.AvailFailing:
			failing = true
		default:
			return api.Capability{}, false
		}
	}
	switch {
	case denied:
		return api.Capability{Name: "endpoints", State: api.CapDenied, Reason: reasonDenied}, true
	case failing:
		return api.Capability{Name: "endpoints", State: api.CapFailing, Reason: reasonFailing}, true
	}
	return api.Capability{Name: "endpoints", State: api.CapOn}, true
}

// /api/v1/meta is not gated, so its reasons are fixed text; the raw error stays in check results.
const (
	reasonDenied  = "Access to this resource is denied"
	reasonFailing = "The watch is failing"
	// reasonStatsFailing: the collector stopped or is stuck (wording pass, Task 51).
	reasonStatsFailing = "Proxy stats are not being collected"
	// Controller log reasons (design §8.9).
	reasonLogsFailing      = "Controller logs cannot be read"
	reasonLogsUnrecognised = "The controller log format is not recognised"
	reasonLogsNoParser     = "No log format is known for this kgateway version"
	reasonLogsNotConnected = "No controller log stream is connected"
)

// Clients are the Kubernetes clients the app uses.
type Clients struct {
	Dynamic   dynamic.Interface
	Kube      kubernetes.Interface
	Discovery kinds.DiscoveryClient
	// Metadata reads CRD metadata; nil means no CRD watch.
	Metadata metadata.Interface
	// InCluster selects direct pod-IP metric scraping; otherwise pods/proxy is used.
	InCluster bool
	// Scraper overrides the metrics scraper InCluster selects; nil keeps it.
	Scraper ctrlmetrics.Scraper
}

// App is a built, not yet started, backend.
type App struct {
	Engine  *engine.Engine
	Handler http.Handler
	// Metrics is served on the probe listener.
	Metrics *obs.Registry
	// Audit writes the audit records.
	Audit *audit.Logger
	// probe replaces the probe handler; tests only.
	probe http.Handler
	cfg   engine.Config
	cache *cache.Cache
	kinds *kinds.Monitor
	opts  Options
	// authz is the authoriser the handler was built with.
	authz api.Authorizer
	// reg starts the extension collectors; packs runs the pack collectors; collected is their store.
	reg, packs *extension.Registry
	collectors []extension.Collector
	collected  *collect.Store
	deps       extension.Deps
	// oidc discovers the issuer in secure mode; nil when the configuration is invalid (oidcErr).
	// flow serves /auth/login and /auth/callback; nil when oidc is.
	oidc    *oidc.Provider
	oidcErr error
	flow    *oidc.Flow
	// sessions holds secure-mode sessions; nil in local mode.
	sessions *session.Store
	// discovered is closed after the first model with an install over a settled cache.
	discovered chan struct{}
}

// Build resolves the served kinds and constructs every component. Both
// intervals must be positive and the delegation edge cap not negative.
func Build(c Clients, o Options, reg *extension.Registry) (*App, error) {
	if o.PollInterval <= 0 || o.MetricsInterval <= 0 {
		return nil, fmt.Errorf("poll interval (%v) and metrics interval (%v) must be positive", o.PollInterval, o.MetricsInterval)
	}
	if o.KindsInterval <= 0 {
		o.KindsInterval = 60 * time.Second
	}
	sc, err := setup.Build(setup.Clients{Dynamic: c.Dynamic, Kube: c.Kube, Discovery: c.Discovery, Metadata: c.Metadata},
		setup.Options{Pinned: o.Pinned, MetricsInterval: o.MetricsInterval, DelegationEdgeCap: o.DelegationEdgeCap,
			NoEvents: o.Diagnostics.NoEvents, NoCRDs: o.Diagnostics.NoCRDs, NoEndpoints: o.Diagnostics.NoEndpoints, Clock: o.Clock,
			ClassOnlyInstalls: o.ClassOnlyInstalls}, reg)
	if err != nil {
		return nil, err
	}
	mon, collected := sc.Kinds, sc.Collected
	ca := cache.New(sc.Cache)
	discovered := make(chan struct{})
	var once sync.Once
	crds := crdRefresher{mon: mon}
	cfg := sc.Engine
	cfg.Snapshots = ca
	cfg.ConfirmEvery, cfg.OnLoop = o.ConfirmEvery, o.OnLoop
	cfg.OnCompute = func(m *engine.Model) {
		// An unsettled cache can hold a Deployment without its Pods; scraping
		// that install fails and is not retried for a full metrics interval.
		if m.Snapshot.Synced && len(m.Installs) > 0 {
			once.Do(func() { close(discovered) })
		}
		crds.observe(m.Snapshot.CRDs)
	}
	eng := engine.New(cfg)
	env := extension.CollectorEnv{Kube: c.Kube, InCluster: c.InCluster, Scraper: c.Scraper, Interval: o.MetricsInterval,
		MaxAge: setup.MetricsMaxAge(o.MetricsInterval), Installs: eng.Installs, Current: eng.Current, Discovered: discovered,
		Collected: collected, NoProxyStats: o.Diagnostics.NoProxyStats, ControllerLogs: o.Diagnostics.ControllerLogs}
	if o.Clock != nil {
		env.Clock = o.Clock
	}
	packCols := reg.PackCollectors(env)
	deps := extension.Deps{Current: eng.Current, Authorizer: reg.Authorizer, Capabilities: reg.CapabilityStates, Logger: slog.Default()}
	mounts, err := reg.Mounts(deps)
	if err != nil {
		return nil, err
	}
	core := func() []api.Capability {
		var snap *cache.Snapshot
		if p := eng.Current(); p != nil && p.Model != nil {
			snap = p.Model.Snapshot
		}
		return CoreCapabilities(snap, o.Diagnostics, collected.Snapshot())
	}
	// Each install's next steps, UI features and counted kinds come from its pack.
	commands := func(i install.Install) diagnose.Commands { return reg.ImplFor(i).Commands }
	consumers := func(i install.Install, o *install.Owned) []objects.Consumer {
		if f := reg.ImplFor(i).BackendConsumers; f != nil {
			return f(i, o)
		}
		return nil
	}
	// Backend detail reads endpoints on request only while the endpoints input is on.
	var scoped endpoints.ScopedReader
	if !o.Diagnostics.NoEndpoints {
		var clk clock.Clock = clock.RealClock{}
		if o.Clock != nil {
			clk = o.Clock
		}
		scoped = endpoints.Bounded(cache.NewScopedReader(c.Kube, clk), endpoints.DefaultBounds, clk)
	}
	authz := reg.Authorizer
	if o.AuthMode == ModeSecure && noAuthorizer(authz) {
		authz = api.DenyAll{}
	}
	hopts := []api.HandlerOption{api.WithExtMounts(mounts...), api.WithCoreCapabilities(core), api.WithCapabilitySources(reg.CapabilityStates),
		api.WithCommands(reg.Commands), api.WithInstallCommands(commands), api.WithInstallFeatures(reg.FeaturesFor),
		api.WithPacks(reg.Packs()...), api.WithInstallKinds(reg.KindAppliesTo), api.WithScopedReader(scoped), api.WithBackendConsumers(consumers)}
	if o.AuthMode == ModeSecure {
		hopts = append(hopts, api.WithSecureMode(false))
	}
	h := api.NewHandler(eng, o.UI, authz, hopts...)
	levels, err := audit.ParseLevels(o.AuditLevels)
	if err != nil {
		return nil, err
	}
	auditOut := o.AuditOut
	if auditOut == nil {
		auditOut = os.Stdout
	}
	metrics := obs.NewRegistry()
	mode := o.AuthMode
	if mode == "" {
		mode = ModeLocal
	}
	metrics.Gauge("snellius_auth_mode", "Authentication mode of this process; 1 for the active mode.", "mode").Set(1, mode)
	auditLog := audit.New(auditOut, levels, metrics)
	var prov *oidc.Provider
	var flow *oidc.Flow
	var oidcErr error
	var sessions *session.Store
	if o.AuthMode == ModeSecure {
		sessions, prov, flow, oidcErr = buildSignIn(c.Kube, o, auditLog, metrics)
	}
	return &App{Engine: eng, oidc: prov, flow: flow, oidcErr: oidcErr, sessions: sessions, Handler: h, Metrics: metrics, Audit: auditLog, cfg: cfg, cache: ca, kinds: mon, opts: o, authz: authz, discovered: discovered, reg: reg,
		packs: extension.NewCore(extension.WithCollectors(packCols...)), collectors: packCols, collected: collected, deps: deps}, nil
}

// Start runs the cache, the pack and extension collectors, the model loop and
// the served-kinds monitor until ctx is done. PollInterval is the loop's
// safety timer. The controller metrics collector starts as soon as the first
// install is discovered over a settled cache, so the first metrics do not wait for a tick.
func (a *App) Start(ctx context.Context) {
	a.cache.Start(ctx)
	a.packs.RunCollectors(ctx, a.deps, a.collected)
	a.reg.RunCollectors(ctx, a.deps, a.collected)
	go a.Engine.Run(ctx, a.opts.PollInterval)
	go a.kinds.Run(ctx, a.opts.KindsInterval)
	if a.sessions != nil {
		go a.sessions.Run(ctx)
	}
}

// Run is RunWith over extension.New(opts...): the core plus the kgateway pack.
func Run(ctx context.Context, kubeconfig string, o Options, opts ...extension.Option) error {
	return RunWith(ctx, kubeconfig, o, extension.New(opts...))
}

// RunWith builds clients from kubeconfig (or the in-cluster config when empty
// and running in a pod), then serves reg until ctx is done.
func RunWith(ctx context.Context, kubeconfig string, o Options, reg *extension.Registry) error {
	ApplyMemoryLimit()
	cfg, inCluster, err := restConfig(kubeconfig)
	if err != nil {
		return err
	}
	if err := validateMode(o.AuthMode); err != nil {
		return err
	}
	if o.AuthMode == "" {
		o.AuthMode = ModeLocal
	}
	if o.Listen == "" {
		o.Listen = "127.0.0.1:8080"
		if inCluster && o.AuthMode == ModeSecure {
			o.Listen = ":8080"
		}
	}
	if o.ProbeListen == "" {
		o.ProbeListen = "127.0.0.1:8081"
		if inCluster {
			o.ProbeListen = ":8081"
		}
	}
	podIP, hostIP := podHostIPs()
	if err := checkListen(o.AuthMode, o.Listen, podIP, hostIP); err != nil {
		return err
	}
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return err
	}
	md, err := metadata.NewForConfig(cfg)
	if err != nil {
		return err
	}
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Metadata: md, InCluster: inCluster}, o, reg)
	if err != nil {
		return err
	}
	return serve(ctx, a, o.Listen, o.ProbeListen, func(main, probe net.Listener) {
		slog.Info("snellius listening", "addr", main.Addr().String(), "probeAddr", probe.Addr().String(), "inCluster", inCluster)
	})
}

// serve listens on addr (the UI and API) and probeAddr (probes and metrics),
// starts a and serves both until ctx is done, then drains in-flight requests
// for up to drainTimeout, then closes what is left. Either listener failing to
// bind or either server failing stops both. Everything it started is cancelled
// when it returns; onListen, if set, receives the bound listeners.
func serve(ctx context.Context, a *App, addr, probeAddr string, onListen func(main, probe net.Listener)) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	probeLn, err := net.Listen("tcp", probeAddr)
	if err != nil {
		_ = ln.Close()
		return fmt.Errorf("listen probe: %w", err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	a.Start(ctx)
	// The writer outlives the servers so records from draining requests are written.
	auditCtx, stopAudit := context.WithCancel(context.Background())
	auditDone := make(chan struct{})
	go func() { a.Audit.Run(auditCtx); close(auditDone) }()
	defer func() { stopAudit(); <-auditDone }()
	a.Audit.Emit(ctx, audit.Record{Level: audit.LevelSecurity, Action: "startup", Outcome: "success", Mode: a.mode(), Listen: addr})
	warnIgnoredOIDC(a.mode(), a.opts.OIDC)
	if a.oidc != nil {
		go a.oidc.Run(ctx)
	}
	if a.mode() == ModeLocal {
		slog.Warn("local mode: anyone who can port-forward to Snellius sees everything it reads", "addr", ln.Addr().String())
	}
	probe := a.probe
	if probe == nil {
		probe = api.ProbeHandler(a.readiness(), a.Metrics)
	}
	srvs := []*http.Server{
		{Handler: a.servedHandler(), ReadHeaderTimeout: 10 * time.Second},
		{Handler: probe, ReadHeaderTimeout: 10 * time.Second},
	}
	drained := make(chan struct{})
	go func() { //nolint:gosec // shutdown must outlive the cancelled serve context
		defer close(drained)
		<-ctx.Done()
		sctx, scancel := context.WithTimeout(context.Background(), drainTimeout)
		defer scancel()
		var wg sync.WaitGroup
		for _, s := range srvs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Past the drain timeout, close the connections still open.
				if err := s.Shutdown(sctx); err != nil {
					slog.Warn("drain timed out; closing open connections", "err", err)
					_ = s.Close()
				}
			}()
		}
		wg.Wait()
	}()
	if onListen != nil {
		onListen(ln, probeLn)
	}
	errs := make(chan error, len(srvs))
	for i, l := range []net.Listener{ln, probeLn} {
		go func() { errs <- srvs[i].Serve(l) }()
	}
	var failure error
	for range srvs {
		if err := <-errs; !errors.Is(err, http.ErrServerClosed) && failure == nil {
			failure = err
		}
		cancel()
	}
	<-drained
	if failure != nil {
		return fmt.Errorf("serve: %w", failure)
	}
	return nil
}

// restConfig prefers the in-cluster config when kubeconfig is empty, then
// falls back to the default loading rules.
func restConfig(kubeconfig string) (*rest.Config, bool, error) {
	if kubeconfig == "" {
		if c, err := rest.InClusterConfig(); err == nil {
			return c, true, nil
		}
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	c, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
	return c, false, err
}

// crdRefresher re-lists the Catalog when the Gateway API CRD set changes, so a
// CRD installed while the UI runs is served from the next compute, not the next
// interval. Other groups wait for the interval. observe runs on the engine loop.
type crdRefresher struct {
	mon        *kinds.Monitor
	seen       bool
	key        string
	refreshing atomic.Bool
}

func (r *crdRefresher) observe(crds []cache.CRDMeta) {
	names := make([]string, 0, len(crds))
	for _, c := range crds {
		names = append(names, c.Name)
	}
	key := strings.Join(names, ",")
	if !r.seen {
		r.seen, r.key = true, key
		return
	}
	// A refresh in flight may predate this change; the key stays old so the next compute retries.
	if key == r.key || !r.refreshing.CompareAndSwap(false, true) {
		return
	}
	r.key = key
	go func() {
		defer r.refreshing.Store(false)
		if err := r.mon.Refresh(); err != nil {
			slog.Warn("re-listing served kinds after a CRD change failed", "err", err)
		}
	}()
}

// buildSignIn builds the session store, the OIDC provider and the sign-in flow.
// The store always exists in secure mode; the error means sign-in is not configured.
func buildSignIn(kube kubernetes.Interface, o Options, auditLog *audit.Logger, metrics *obs.Registry) (*session.Store, *oidc.Provider, *oidc.Flow, error) {
	clk := clock.WithTicker(clock.RealClock{})
	if o.Clock != nil {
		clk = o.Clock
	}
	// The store and the flow refer to each other: the flow completes into the store, the store refreshes through the flow.
	var prov *oidc.Provider
	var flow *oidc.Flow
	store := session.New(session.Options{Idle: o.SessionIdle, Absolute: o.SessionAbsolute, Clock: clk, Audit: auditLog, Metrics: metrics,
		CookieSecure: !o.CookieInsecure, ClientID: o.OIDC.ClientID, RedirectURL: o.OIDC.RedirectURL,
		Refresher: refresherFunc(func(ctx context.Context, rt string) (oidc.Result, error) {
			if flow == nil {
				return oidc.Result{}, errors.New("sign-in is not configured")
			}
			return flow.Refresh(ctx, rt)
		}),
		EndSession: func() string {
			if prov == nil {
				return ""
			}
			if eps := prov.Endpoints(); eps != nil {
				return eps.EndSessionEndpoint
			}
			return ""
		}})
	if err := o.OIDC.Validate(); err != nil {
		return store, nil, nil, err
	}
	p, err := oidc.NewProvider(o.OIDC, clk)
	if err != nil {
		return store, nil, nil, err
	}
	f, err := oidc.NewFlow(oidc.FlowOptions{Config: o.OIDC, Provider: p, Reviewer: oidc.NewKubeReviewer(kube),
		Completer: store, Audit: auditLog, Clock: clk, CookieSecure: !o.CookieInsecure})
	if err != nil {
		return store, nil, nil, err
	}
	prov, flow = p, f
	return store, prov, flow, nil
}

// refresherFunc adapts a function to session.Refresher.
type refresherFunc func(ctx context.Context, refreshToken string) (oidc.Result, error)

// Refresh implements session.Refresher.
func (f refresherFunc) Refresh(ctx context.Context, refreshToken string) (oidc.Result, error) {
	return f(ctx, refreshToken)
}
