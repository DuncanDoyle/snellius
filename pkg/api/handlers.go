package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"slices"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/endpoints"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/overview"
	"github.com/DuncanDoyle/snellius/pkg/paths"
	"github.com/DuncanDoyle/snellius/pkg/platform"
	"github.com/DuncanDoyle/snellius/pkg/version"
)

// ModelSource provides the current publication and the platform it is computed for.
type ModelSource interface {
	Current() *engine.Published
	Platform() platform.Info
}

// NewHandler returns the HTTP handler for the API, the probes and the SPA in ui.
// Every response with resources, findings or counts goes through authz; a nil
// authz means AllowAll (D-013). Options add extension endpoints. In complete
// secure mode every /api/ response goes through the commit wrapper (commit.go).
func NewHandler(src ModelSource, ui fs.FS, authz Authorizer, opts ...HandlerOption) http.Handler {
	var cfg handlerConfig
	for _, o := range opts {
		o(&cfg)
	}
	if authz == nil && cfg.secure {
		authz = DenyAll{}
	} else if authz == nil {
		authz = AllowAll{}
	}
	if cfg.commands == nil {
		cfg.commands = diagnose.Kubectl{}
	}
	h := &handler{src: src, authz: authz, capSources: cfg.capSources, coreSource: cfg.coreSource, commands: cfg.commands, commandsFor: cfg.commandsFor,
		featuresFor: cfg.featuresFor, packs: cfg.packs, kindFor: cfg.kindFor, pathMemo: cfg.pathMemo, scoped: cfg.scoped, consumersFor: cfg.consumersFor, secure: cfg.secure, bypass: cfg.bypass, off: cfg.off}
	if h.pathMemo == nil {
		h.pathMemo = paths.NewMemo(nil)
	}
	if h.scoped == nil {
		h.scoped = endpoints.NoReader
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", healthz)
	mux.HandleFunc("GET /readyz", readyzHandler(ModelReady(src)))
	mux.HandleFunc("GET /api/v1/meta", h.meta)
	mux.HandleFunc("GET /api/v1/installs", h.installs)
	mux.HandleFunc("GET /api/v1/installs/{id}/overview", h.overview)
	mux.HandleFunc("GET /api/v1/installs/{id}/health", h.health)
	mux.HandleFunc("GET /api/v1/installs/{id}/findings", h.findings)
	mux.HandleFunc("GET /api/v1/installs/{id}/findings/{findingId}", h.findingDetail)
	mux.HandleFunc("GET /api/v1/installs/{id}/diagnostics", h.diagnostics)
	mux.HandleFunc("GET /api/v1/installs/{id}/processes", h.processes)
	mux.HandleFunc("GET /api/v1/installs/{id}/routes", h.routes)
	mux.HandleFunc("GET /api/v1/installs/{id}/gateways", h.gateways)
	mux.HandleFunc("GET /api/v1/installs/{id}/listenersets", h.listenerSets)
	mux.HandleFunc("GET /api/v1/installs/{id}/namespaces", h.namespaces)
	mux.HandleFunc("GET /api/v1/installs/{id}/gateways/{ns}/{name}", h.gatewayDetail)
	mux.HandleFunc("GET /api/v1/installs/{id}/listenersets/{group}/{kind}/{ns}/{name}", h.listenerSetDetail)
	mux.HandleFunc("GET /api/v1/installs/{id}/routes/{group}/{kind}/{ns}/{name}", h.routeDetail)
	mux.HandleFunc("GET /api/v1/installs/{id}/routes/{group}/{kind}/{ns}/{name}/delegation", h.routeDelegation)
	mux.HandleFunc("GET /api/v1/installs/{id}/resources/{group}/{kind}/{rns}/{rname}", h.resource)
	mux.HandleFunc("GET /api/v1/installs/{id}/refs", h.refs)
	mux.HandleFunc("GET /api/v1/installs/{id}/paths", h.pathList)
	mux.HandleFunc("GET /api/v1/installs/{id}/backends", h.backendList)
	mux.HandleFunc("GET /api/v1/installs/{id}/backends/{group}/{kind}/{ns}/{name}", h.backendDetail)
	mountExts(mux, cfg.ext)
	// Unknown /api/ paths, extension ones included, get a JSON 404, never the SPA.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, _ *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
	mux.Handle("/", spa(ui, authMode(cfg.secure)))
	var next http.Handler = mux
	if cfg.bypass {
		// Extension Viewers see the bypass through the request.
		next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), bypassKey{}, true)))
		})
	}
	switch {
	case cfg.secure && !cfg.complete:
		return secureGate(next)
	case cfg.secure:
		admit, _ := authz.(Admitter)
		return commit(next, admit, cfg.commitCheck, cfg.authzAudit, cfg.capHit, cfg.commitClock)
	}
	return next
}

// secureGate answers every /api/ path with 503 and passes the rest (SPA, probes) on.
func secureGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeErr(w, http.StatusServiceUnavailable, "secure-mode-incomplete")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type handler struct {
	src        ModelSource
	authz      Authorizer
	capSources []func() []Capability
	coreSource func() []Capability
	commands   diagnose.Commands
	// commandsFor, if set, gives an install's own commands.
	commandsFor func(install.Install) diagnose.Commands
	// featuresFor, if set, gives an install's pack features.
	featuresFor func(install.Install) []string
	packs       []string
	kindFor     func(install.Install, objects.GroupKind) bool
	// pathMemo holds the path topologies and projections; one per handler.
	pathMemo *paths.Memo
	// scoped reads one Service's endpoints for backend detail; never nil.
	// Authorise the backend before every call, memo hits included.
	scoped endpoints.ScopedReader
	// consumersFor, if set, gives an install's non-route backend consumers.
	consumersFor func(install.Install, *install.Owned) []objects.Consumer
	// secure is true when the handler was built with WithSecureMode.
	secure bool
	// bypass turns the secure-mode projection off, and off some of its steps (rule tests only).
	bypass bool
	off    projStep
}

// WithScopedReader reads one Service's endpoints for backend detail (design
// §9.3). Without it, or with nil, those endpoints are unavailable.
func WithScopedReader(r endpoints.ScopedReader) HandlerOption {
	return func(c *handlerConfig) { c.scoped = r }
}

// commandsOf is i's next-step commands: its own, else the handler's.
func (h *handler) commandsOf(i install.Install) diagnose.Commands {
	if h.commandsFor != nil {
		if c := h.commandsFor(i); c != nil {
			return c
		}
	}
	return h.commands
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

// current returns the published model, or nil before the first publication.
func (h *handler) current() *engine.Model {
	if p := h.src.Current(); p != nil {
		return p.Model
	}
	return nil
}

// load returns the current publication, or writes 503 if its model is not
// ready. A request calls it once and serves everything from that one value.
func (h *handler) load(w http.ResponseWriter) *engine.Published {
	p := h.src.Current()
	if p == nil || !synced(p.Model) {
		writeErr(w, http.StatusServiceUnavailable, "connecting to cluster: cache not synced")
		return nil
	}
	return p
}

// synced reports whether m exists and was computed over a synced cache.
func synced(m *engine.Model) bool { return m != nil && m.Snapshot != nil && m.Snapshot.Synced }

func (h *handler) meta(w http.ResponseWriter, r *http.Request) {
	m, plat := h.current(), h.src.Platform()
	resp := MetaResponse{Version: version.Version, Platform: plat.Name, Capabilities: append([]string{}, plat.Capabilities...), Kinds: []kinds.Served{},
		Packs: append([]string{}, h.packs...), AuthMode: authMode(h.secure)}
	if m != nil {
		resp.Pinned, resp.Synced = m.Pinned, synced(m)
		for _, k := range m.Served {
			if !isAddedAfterV1(k) {
				resp.Kinds = append(resp.Kinds, k)
			}
		}
	}
	// Deployments are linkable only where the platform runs workloads.
	if plat.Has(platform.Workloads) {
		resp.Kinds = append(resp.Kinds, deploymentKind)
	}
	if m != nil {
		for _, k := range m.Served {
			if isAddedAfterV1(k) {
				resp.Kinds = append(resp.Kinds, k)
			}
		}
		resp.Views = explore.DetailKinds(m.Served)
	}
	resp.Filters = addedFilters
	addCapabilities(&resp, plat.Capabilities, h.coreSource, h.capSources)
	if h.secure {
		v := h.view(r)
		resp.LimitedView = !v.readsAll()
		if v.projects() {
			dropControllerLogs(&resp)
		}
	}
	writeJSON(w, resp)
}

// dropControllerLogs removes the controller log state unless it is off: its
// other states come from the controller Pods' log content (design §7.2).
func dropControllerLogs(resp *MetaResponse) {
	const name = "controllerLogs"
	if j := slices.IndexFunc(resp.CapabilityStates, func(c Capability) bool { return c.Name == name }); j < 0 || resp.CapabilityStates[j].State == CapOff {
		return
	}
	resp.CapabilityStates = slices.DeleteFunc(resp.CapabilityStates, func(c Capability) bool { return c.Name == name })
	resp.Capabilities = slices.DeleteFunc(resp.Capabilities, func(c string) bool { return c == name })
}

// visibleHealth returns the install's report as the caller of v may see it:
// v.report, with D-013's restricted rows outside secure mode. There a finding
// on a denied resource keeps its code, severity and ref but is restricted:
// message, detail, since and evidence are emptied. State and counts still
// cover every finding, so partial visibility never makes an install look
// healthier, and the findings keep the order health returned.
func visibleHealth(v *view, i *engine.InstallModel) health.Report {
	if v.readsAll() || v.secure {
		return v.report(i)
	}
	rep := i.Health
	rep.Findings = make([]health.Finding, 0, len(i.Health.Findings))
	for _, f := range i.Health.Findings {
		if f.Resource != nil && !v.can(*f.Resource) {
			f.Message, f.Detail, f.Since, f.Facts, f.Restricted = "", "", nil, nil, true
		}
		rep.Findings = append(rep.Findings, f)
	}
	return rep
}

// visibleOverview rebuilds the overview over the owned objects the caller of v
// may see: denied objects leave the count tiles and the gateway rows.
func (h *handler) visibleOverview(v *view, m *engine.Model, i *engine.InstallModel, rep health.Report) overview.Overview {
	if v.readsAll() {
		return i.Overview
	}
	can := func(u *unstructured.Unstructured) bool { return v.can(objects.RefOf(u)) }
	keep := func(us []*unstructured.Unstructured) []*unstructured.Unstructured {
		out := []*unstructured.Unstructured{}
		for _, u := range us {
			if can(u) {
				out = append(out, u)
			}
		}
		return out
	}
	keepObjs := func(os []install.OwnedObject) []install.OwnedObject {
		out := []install.OwnedObject{}
		for _, o := range os {
			if can(o.Obj) {
				out = append(out, o)
			}
		}
		return out
	}
	o := i.Owned
	unattached := []install.UnattachedRoute{}
	for _, u := range o.Unattached {
		if can(u.Route.Obj) {
			unattached = append(unattached, u)
		}
	}
	owned := &install.Owned{GatewayClasses: keep(o.GatewayClasses), Gateways: keep(o.Gateways), ListenerSets: keep(o.ListenerSets),
		Routes: keepObjs(o.Routes), Policies: keepObjs(o.Policies), Listed: keepObjs(o.Listed), Unattached: unattached, Backends: v.backends(i)}
	return overview.Build(i.Resolved, owned, m.Snapshot, m.Served, rep, h.src.Platform())
}

// backends are install i's Backends the caller of v may see. In secure mode
// without the full read, only those a readable owned route names: a Backend
// joins an install through its consumer routes, which are not install context (D-107).
func (v *view) backends(i *engine.InstallModel) []*unstructured.Unstructured {
	out := []*unstructured.Unstructured{}
	if i.Owned == nil {
		return out
	}
	for _, b := range i.Owned.Backends {
		if v.can(objects.RefOf(b)) {
			out = append(out, b)
		}
	}
	if !v.omits() || v.readsAll() {
		return out
	}
	var dec objects.Decoder = objects.Direct{}
	if i.Index != nil {
		dec = indexViews{ix: i.Index}
	}
	named := map[objects.Ref]bool{}
	for _, r := range i.Owned.Routes {
		if v.can(objects.RefOf(r.Obj)) {
			for _, t := range dec.Route(r.Obj).Targets {
				named[t.Ref] = true
			}
		}
	}
	return slices.DeleteFunc(out, func(b *unstructured.Unstructured) bool { return !named[objects.RefOf(b)] })
}

func (h *handler) installs(w http.ResponseWriter, r *http.Request) {
	p := h.load(w)
	if p == nil {
		return
	}
	m := p.Model
	// A zero confirmation time is omitted, so clients fall back to computedAt.
	var confirmed *time.Time
	if !p.ConfirmedAt.IsZero() {
		at := wireTime(p.ConfirmedAt)
		confirmed = &at
	}
	out, v := []InstallSummary{}, h.view(r)
	computed := wireTime(m.ComputedAt)
	if v.projects() && confirmed != nil {
		// When the model was computed is when the cluster last changed, hidden objects included (D-085).
		computed = *confirmed
	}
	for k := range m.Installs {
		i := &m.Installs[k]
		hd, rep := v.installHeader(i), visibleHealth(v, i)
		out = append(out, InstallSummary{ID: hd.ID, DisplayName: hd.DisplayName, Namespace: hd.Namespace, Name: hd.Name, Version: hd.Version,
			Edition: hd.Edition, ControllerNames: hd.ControllerNames, Readiness: hd.Readiness, Scope: hd.Scope, DetailsHidden: hd.DetailsHidden,
			Health: HealthSummary{State: rep.State, Counts: rep.Counts}, GroupRows: min(groupTotal(v.diagnosis(i)), maxGroups),
			ComputedAt: computed, ConfirmedAt: confirmed, Features: h.features(i)})
	}
	writeJSON(w, out)
}

// Kinds the install header reads besides the controller Deployment (design §7.4).
var (
	gatewayClassKind = objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "GatewayClass"}
	namespaceKind    = objects.GroupKind{Kind: "Namespace"}
)

// installHeader is i's header as the caller of v may see it (design §7.4,
// D-102): the model's unless v projects; else the ID, and each other field
// only when the caller may read its source. Each check is a permission, so
// hidden objects never change the answer.
func (v *view) installHeader(i *engine.InstallModel) overview.Header {
	hd := i.Overview.Header
	if !v.projects() {
		return hd
	}
	classes := v.canList(gatewayClassKind, "")
	// A class-only install has no Deployment: its name comes from its classes.
	ctrl := classes
	if d := i.Resolved.Deployment; d != nil {
		ctrl = v.can(objects.Ref{Group: "apps", Kind: "Deployment", Namespace: d.Namespace, Name: d.Name})
	}
	out := overview.Header{ID: hd.ID, ControllerNames: []string{}, Readiness: overview.Readiness{State: overview.ReadinessUnknown}}
	if ctrl {
		out.DisplayName, out.Namespace, out.Name, out.Version, out.Edition, out.Readiness = hd.DisplayName, hd.Namespace, hd.Name, hd.Version, hd.Edition, hd.Readiness
	}
	if classes {
		out.ControllerNames = hd.ControllerNames
	}
	// The scope's settings come from the Deployment, its counts from the Namespaces.
	if hd.Scope != nil && ctrl && v.canList(namespaceKind, "") {
		out.Scope = hd.Scope
	}
	out.DetailsHidden = !ctrl || !classes || hd.Scope != nil && out.Scope == nil
	return out
}

// find loads the publication and the install named by the request, or writes 503 or 404.
func (h *handler) find(w http.ResponseWriter, r *http.Request) (*engine.Published, *engine.InstallModel) {
	p := h.load(w)
	if p == nil {
		return nil, nil
	}
	id := r.PathValue("id")
	for i := range p.Model.Installs {
		if p.Model.Installs[i].Resolved.ID() == id {
			return p, &p.Model.Installs[i]
		}
	}
	writeErr(w, http.StatusNotFound, "install "+id+" not found")
	return nil, nil
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	if p, i := h.find(w, r); i != nil {
		v := h.view(r)
		ov := h.visibleOverview(v, p.Model, i, visibleHealth(v, i))
		ov.Header, ov.Counts = v.installHeader(i), h.applicableCounts(i, ov.Counts)
		rows, l := make([]OverviewGatewayRow, 0, len(ov.Gateways)), h.linker(v, p, i, nil, nil)
		// Over the findings and checks the caller is shown: a Not checked result proves nothing.
		rejecting := gatewaysWith(i, health.CodeXDSRejected, "", v.shown)
		withheld := gatewaysWith(i, health.CodeConfigWithheld, health.SeverityError, v.shown)
		checked := slices.ContainsFunc(v.report(i).Checks, func(c health.CheckResult) bool { return c.Name == xdsCheckName && c.OK && !c.NotAssessed })
		for _, g := range ov.Gateways {
			// A reject, then withheld config, shows even while the check is not OK; otherwise only an OK check proves "ok".
			dp := DataPlaneUnknown
			switch {
			case rejecting[g.Ref]:
				dp = DataPlaneRejects
			case withheld[g.Ref]:
				dp = DataPlaneWithheld
			case checked:
				dp = DataPlaneOK
			}
			rows = append(rows, OverviewGatewayRow{GatewayRow: g, Link: l.link(g.Ref), DataPlane: dp})
		}
		writeJSON(w, OverviewResponse{Overview: ov, Gateways: rows, CollectedAt: wireTime(p.ConfirmedAt)})
	}
}

// wireTime is t as the API emits it: UTC. Internal clocks keep their zone and
// monotonic reading, which grace and staleness durations rely on.
func wireTime(t time.Time) time.Time { return t.UTC() }

// wireHealth is rep with every time in wire form and collectedAt set to the
// confirmation time (design §3.1). It copies the slices, since rep may share
// them with the model; the stored report is unchanged.
func wireHealth(rep health.Report, confirmedAt time.Time) health.Report {
	rep.CollectedAt = wireTime(confirmedAt)
	rep.Findings = slices.Clone(rep.Findings)
	for i, f := range rep.Findings {
		if f.Since != nil {
			at := wireTime(*f.Since)
			rep.Findings[i].Since = &at
		}
	}
	if rep.Checks != nil {
		rep.Checks = wireChecks(rep.Checks)
	}
	return rep
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if p, i := h.find(w, r); i != nil {
		v := h.view(r)
		writeJSON(w, healthResponse(v, h.linker(v, p, i, nil, nil), i, p.ConfirmedAt))
	}
}

// healthResponse is the caller's report in wire form: each finding's resource
// carries its link. A visible finding whose evidence the caller may not view
// all of has its detail replaced and no typed evidence, and a since it has is
// replaced by the resource's own (design §7.4 rule 7, Phase 3 §14 rule 12).
// Typed evidence is in wire form (§14 rule 14). State, counts and severities stay.
func healthResponse(v *view, l linker, i *engine.InstallModel, confirmedAt time.Time) HealthResponse {
	rep := wireHealth(visibleHealth(v, i), confirmedAt)
	v.stampChecks(rep.Checks, confirmedAt)
	out := HealthResponse{Report: rep, Findings: make([]FindingDTO, 0, len(rep.Findings))}
	out.Report.Findings = nil // shadowed by out.Findings
	for k, f := range rep.Findings {
		d := FindingDTO{Finding: f}
		if f.Resource != nil {
			lr := l.linked(*f.Resource)
			d.Resource = &lr
		}
		if !f.Restricted && !v.allOf(f.Evidence) {
			d.Detail, d.Facts, d.EvidenceRestricted = restrictedDetail(&rep.Findings[k]), nil, true
			if f.Since != nil {
				d.Since = nil
				if f.ResourceSince != nil {
					at := wireTime(*f.ResourceSince)
					d.Since = &at
				}
			}
		}
		if d.Facts != nil {
			d.Facts = wireEvidence(v, l, d.Facts)
		}
		out.Findings = append(out.Findings, d)
	}
	return out
}

// resource serves one object the install admits (design §7.1) when the
// caller may view it and it exists: admit is not none. Only
// metadata.managedFields is stripped (D-013). Secrets are never watched, so
// they are never reachable. "-" stands for the empty group or namespace.
func (h *handler) resource(w http.ResponseWriter, r *http.Request) {
	p, i := h.find(w, r)
	if i == nil {
		return
	}
	dash := func(s string) string {
		if s == "-" {
			return ""
		}
		return s
	}
	want := objects.Ref{Group: dash(r.PathValue("group")), Kind: r.PathValue("kind"), Namespace: dash(r.PathValue("rns")), Name: r.PathValue("rname")}
	if linkerFor(h.view(r), p.Model, i).admitNamed(want) != LinkNone {
		if obj := object(p.Model, want); obj != nil {
			// The body is the raw object, so the collection time travels in a header (D-017 §6).
			w.Header().Set("Collected-At", wireTime(p.ConfirmedAt).Format(time.RFC3339Nano))
			writeJSON(w, obj)
			return
		}
	}
	// Denied, absent and unadmitted objects get the same answer.
	writeErr(w, http.StatusNotFound, notFoundInScope)
}

// object returns the stored object ref names as a JSON object, or nil.
func object(m *engine.Model, ref objects.Ref) map[string]any {
	switch u, d := stored(m, ref); {
	case u != nil:
		return clean(u.Object)
	case d != nil:
		return deploymentObject(d)
	}
	return nil
}

// stored returns the object ref names in the snapshot: an object of a served
// kind, or a Deployment. Both are nil when it is absent or never served.
func stored(m *engine.Model, ref objects.Ref) (*unstructured.Unstructured, *appsv1.Deployment) {
	if ref.Group == deploymentKind.Group && ref.Kind == deploymentKind.Kind {
		for _, d := range m.Snapshot.Deployments {
			if d.Namespace == ref.Namespace && d.Name == ref.Name {
				return nil, d
			}
		}
		return nil, nil
	}
	for _, k := range m.Served {
		if k.Group != ref.Group || k.Kind != ref.Kind {
			continue
		}
		// The cache sorts each kind by "namespace/name".
		objs, key := m.Snapshot.Objects[k.GVR()], ref.Namespace+"/"+ref.Name
		if j, ok := slices.BinarySearchFunc(objs, key, func(u *unstructured.Unstructured, key string) int {
			return strings.Compare(u.GetNamespace()+"/"+u.GetName(), key)
		}); ok {
			return objs[j], nil
		}
	}
	return nil, nil
}

// clean returns a copy of obj without metadata.managedFields.
func clean(obj map[string]any) map[string]any {
	c := (&unstructured.Unstructured{Object: obj}).DeepCopy()
	unstructured.RemoveNestedField(c.Object, "metadata", "managedFields")
	return c.Object
}

// deploymentObject returns d as served by the resource endpoint. Typed
// informers drop apiVersion and kind, so they are set here.
func deploymentObject(d *appsv1.Deployment) map[string]any {
	obj, err := runtime.DefaultUnstructuredConverter.ToUnstructured(d.DeepCopy())
	if err != nil {
		return nil
	}
	obj["apiVersion"], obj["kind"] = "apps/v1", "Deployment"
	return clean(obj)
}

// authMode is the /meta authMode for a handler built with or without WithSecureMode.
func authMode(secure bool) string {
	if secure {
		return "secure"
	}
	return "local"
}

// spa serves static files and falls back to index.html for client-side
// routes; directories are never listed. The fallback stamps the auth mode on <html>,
// so the UI can show the mode on first paint without waiting for /meta.
func spa(ui fs.FS, mode string) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if st, err := fs.Stat(ui, p); err == nil && !st.IsDir() {
				files.ServeHTTP(w, r)
				return
			}
		}
		b, err := fs.ReadFile(ui, "index.html")
		if err != nil {
			http.Error(w, "UI not built", http.StatusNotFound)
			return
		}
		b = bytes.Replace(b, []byte("<html "), []byte(`<html data-auth-mode="`+mode+`" `), 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(b)
	})
}
