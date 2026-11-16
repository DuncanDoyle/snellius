package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/app"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/overview"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

// Installs, /overview, /meta and the extension Viewer for viewers without the
// full read (design §7.2, §7.4, D-102, D-103).

// recorder denies every SAR and records it.
type recorder struct {
	mu    sync.Mutex
	calls []sar.Attributes
}

func (c *recorder) Check(_ context.Context, _ oidc.Identity, a sar.Attributes) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, a)
	return false, nil
}

// take returns the recorded SARs and forgets them.
func (c *recorder) take() []sar.Attributes {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.calls
	c.calls = nil
	return out
}

// tierWorld is tier's world with a second, empty install kgw2.kgateway2, computed once.
func tierWorld(t *testing.T, tier scale.Tier) *engine.Engine {
	t.Helper()
	s := scale.Snapshot(tier, 1)
	s.Deployments = append(s.Deployments, testutil.ControllerDeployment("kgw2", "kgateway2", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.7", 1, 1,
		testutil.Env(install.DiscoveryNamespaceSelectorsEnv, `[{"matchLabels":{"team":"none"}}]`)))
	sortObjects(s)
	e := engine.New(engine.Config{Snapshots: &holder{s: s}, Served: testutil.ServedUpstream(), Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: health.Upstream(),
		Now: func() time.Time { return testutil.Now }})
	if m := e.Compute(); len(m.Installs) != 2 {
		t.Fatalf("%d installs, want 2", len(m.Installs))
	}
	return e
}

// sarBucket is a SAR as "verb resource namespace".
func sarBucket(verb, resource, ns string) string { return verb + " " + resource + " " + ns }

// basisBuckets are the list buckets the projection of m's reports may ask:
// the resource and basis of every own-object and collection finding, and each install's controller Deployment.
func basisBuckets(m *engine.Model) map[string]bool {
	out := map[string]bool{}
	add := func(r objects.Ref) {
		if k, ok := (ruleKinds{}).Lookup(r.Group, r.Kind); ok {
			ns := r.Namespace
			if !k.Namespaced {
				ns = ""
			}
			out[sarBucket("list", k.Resource, ns)] = true
		}
	}
	for _, i := range m.Installs {
		if d := i.Resolved.Deployment; d != nil {
			add(objects.Ref{Group: "apps", Kind: "Deployment", Namespace: d.Namespace, Name: d.Name})
		}
		for _, f := range i.Health.Findings {
			if f.Resource != nil && (f.Class == health.ClassOwn || f.Basis.Collection) {
				add(*f.Resource)
				for _, r := range f.Basis.Refs {
					add(r)
				}
			}
		}
	}
	return out
}

// TestDeniedAllSeesEveryInstall: a viewer allowed nothing still sees every
// install by ID with its details hidden (D-103), and each install's pages
// answer 200 with no rows; no SAR is spent on reachability.
func TestDeniedAllSeesEveryInstall(t *testing.T) {
	t.Run("extreme installs", func(t *testing.T) {
		e := tierWorld(t, scale.Extreme)
		rec := &recorder{}
		srv := ruleServeWith(t, e, rec)
		got := decode[[]api.InstallSummary](t, srv, nobodyUser, "/api/v1/installs")
		var ids []string
		for _, i := range got {
			ids = append(ids, i.ID)
			if !i.DetailsHidden || i.Version != "" || i.Edition != "" || i.DisplayName != "" || len(i.ControllerNames) != 0 || i.Scope != nil {
				t.Errorf("install %s: details shown to a viewer allowed nothing: %+v", i.ID, i)
			}
		}
		if !slices.Equal(ids, []string{"kgw.kgateway", "kgw2.kgateway2"}) {
			t.Errorf("installs %v, want both", ids)
		}
		allowed := basisBuckets(e.Current().Model)
		sars := rec.take()
		if !slices.ContainsFunc(sars, func(a sar.Attributes) bool { return a.Resource == "deployments" && a.Namespace == "kgw2" }) {
			t.Errorf("no SAR for kgw2's controller Deployment among %d; the header sources are not checked", len(sars))
		}
		for _, a := range sars {
			// The full read lists every served kind cluster-wide; the header lists GatewayClasses and Namespaces cluster-wide.
			if a.Verb != "list" || a.Namespace != "" && !allowed[sarBucket(a.Verb, a.Resource, a.Namespace)] {
				t.Errorf("SAR %+v is neither the full read, a header source nor a finding's basis", a)
			}
		}
	})
	t.Run("large pages", func(t *testing.T) {
		e := tierWorld(t, scale.Large)
		rec := &recorder{}
		srv := ruleServeWith(t, e, rec)
		for _, id := range []string{"kgw.kgateway", "kgw2.kgateway2"} {
			base := "/api/v1/installs/" + id
			ov := decode[api.OverviewResponse](t, srv, nobodyUser, base+"/overview")
			if len(ov.Gateways) != 0 || slices.ContainsFunc(ov.Counts, func(c overview.Count) bool { return c.Count != 0 }) {
				t.Errorf("%s overview has rows: %d gateways, counts %+v", id, len(ov.Gateways), ov.Counts)
			}
			if p := decode[api.Page[api.RouteRow]](t, srv, nobodyUser, base+"/routes"); len(p.Items) != 0 || p.Total != 0 {
				t.Errorf("%s routes: %d rows, total %d", id, len(p.Items), p.Total)
			}
			if h := decode[api.HealthResponse](t, srv, nobodyUser, base+"/health"); len(h.Findings) != 0 {
				t.Errorf("%s health: %d findings", id, len(h.Findings))
			}
			if p := decode[api.Page[api.FindingRow]](t, srv, nobodyUser, base+"/findings"); len(p.Items) != 0 || p.Total != 0 {
				t.Errorf("%s findings: %d rows, total %d", id, len(p.Items), p.Total)
			}
		}
		for _, a := range rec.take() {
			if a.Verb != "list" || a.Resource == "pods" || a.Resource == "deployments" && a.Namespace != "kgw" && a.Namespace != "kgw2" {
				t.Errorf("SAR %+v: no page asks by name or about a workload outside the header", a)
			}
		}
	})
}

// TestUnknownInstallStill404: an unknown ID answers 404 and a known one 200,
// for every viewer, so the answer never depends on what the viewer can read.
func TestUnknownInstallStill404(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	for _, user := range []string{fullUser, restrictedUser, nobodyUser} {
		for _, p := range []string{"/overview", "/health", "/findings", "/routes", "/diagnostics"} {
			if code, body := fetch(t, srv, user, "/api/v1/installs/nope.nope"+p); code != http.StatusNotFound {
				t.Errorf("%s unknown %s: %d %s, want 404", user, p, code, body)
			}
			if code, body := fetch(t, srv, user, ruleBase+p); code != http.StatusOK {
				t.Errorf("%s %s: %d %.200s, want 200", user, p, code, body)
			}
		}
	}
}

// headers are the install header of /installs and of /overview, as user sees them.
func headers(t *testing.T, srv *httptest.Server, user string) []overview.Header {
	t.Helper()
	var out []overview.Header
	for _, i := range decode[[]api.InstallSummary](t, srv, user, "/api/v1/installs") {
		out = append(out, overview.Header{ID: i.ID, DisplayName: i.DisplayName, Namespace: i.Namespace, Name: i.Name, Version: i.Version,
			Edition: i.Edition, ControllerNames: i.ControllerNames, Readiness: i.Readiness, Scope: i.Scope, DetailsHidden: i.DetailsHidden})
	}
	return append(out, decode[api.OverviewResponse](t, srv, user, ruleBase+"/overview").Header)
}

// TestInstallMetadataPerSource: each header field shows only when the viewer
// may read its source: the controller Deployment, GatewayClasses cluster-wide,
// and Namespaces cluster-wide (scope also needs the Deployment it is read from).
func TestInstallMetadataPerSource(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	full := headers(t, srv, fullUser)[0]
	if full.Version == "" || full.Edition == "" || full.DisplayName == "" || len(full.ControllerNames) == 0 || full.Scope == nil || full.DetailsHidden {
		t.Fatalf("the full viewer lacks details: %+v", full)
	}
	for _, src := range []string{"", "d", "c", "n", "dc", "dn", "cn", "dcn"} {
		dep, classes, ns := strings.Contains(src, "d"), strings.Contains(src, "c"), strings.Contains(src, "n")
		want := overview.Header{ID: full.ID, ControllerNames: []string{}, Readiness: overview.Readiness{State: overview.ReadinessUnknown}}
		if dep {
			want.DisplayName, want.Namespace, want.Name, want.Version, want.Edition, want.Readiness = full.DisplayName, full.Namespace, full.Name, full.Version, full.Edition, full.Readiness
		}
		if classes {
			want.ControllerNames = full.ControllerNames
		}
		if dep && ns {
			want.Scope = full.Scope
		}
		want.DetailsHidden = src != "dcn"
		for k, got := range headers(t, srv, metaUser+src) {
			if mustJSON(t, got) != mustJSON(t, want) {
				t.Errorf("sources %q, header %d:\n got %s\nwant %s", src, k, mustJSON(t, got), mustJSON(t, want))
			}
		}
	}
}

// TestInstallHealthOverShownFindings: /installs health and group rows (the
// header tint's input) follow the findings the viewer is shown.
func TestInstallHealthOverShownFindings(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, change("faulty")))
	summary := func(user string) api.InstallSummary {
		return decode[[]api.InstallSummary](t, srv, user, "/api/v1/installs")[0]
	}
	for _, user := range []string{fullUser, restrictedUser} {
		got, h := summary(user), decode[api.HealthResponse](t, srv, user, ruleBase+"/health")
		if got.Health.State != h.State || !maps.Equal(got.Health.Counts, h.Counts) {
			t.Errorf("%s: /installs health %+v, /health %s %v", user, got.Health, h.State, h.Counts)
		}
		if d := decode[api.Diagnostics](t, srv, user, ruleBase+"/diagnostics"); got.GroupRows != min(d.GroupTotal, len(d.Groups)) && got.GroupRows != d.GroupTotal {
			t.Errorf("%s: groupRows %d, diagnostics lists %d of %d", user, got.GroupRows, len(d.Groups), d.GroupTotal)
		}
	}
	if f, r := summary(fullUser).Health, summary(restrictedUser).Health; maps.Equal(f.Counts, r.Counts) {
		t.Errorf("the hidden faults reach neither viewer's counts (%v); the fixture does not test the projection", f.Counts)
	}
}

// TestRestrictedSeesOnlyInstallID: with the controller Deployment hidden, the
// restricted viewer's responses carry the install ID and no other field from
// the controller Deployment (D-102); the full viewer sees them all.
func TestRestrictedSeesOnlyInstallID(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	full := headers(t, srv, fullUser)
	for _, h := range full {
		if h.Version != "2.4.7" || h.Edition != "kgateway" || h.DisplayName != "kgw/kgateway" || h.Readiness.State != overview.ReadinessReady ||
			len(h.ControllerNames) == 0 || h.Scope == nil {
			t.Fatalf("the full viewer lacks a detail: %+v", h)
		}
	}
	for _, h := range headers(t, srv, restrictedUser) {
		want := overview.Header{ID: ruleInstall, ControllerNames: []string{}, Readiness: overview.Readiness{State: overview.ReadinessUnknown}, DetailsHidden: true}
		if mustJSON(t, h) != mustJSON(t, want) {
			t.Errorf("restricted header %s, want only the ID", mustJSON(t, h))
		}
	}
	for _, s := range ruleSurfaces {
		path := s.path(t, srv, srv)
		_, body := fetch(t, srv, restrictedUser, path)
		for _, leak := range []string{`"2.4.7"`, "v2.4.7", "kgw/kgateway", `"edition":"kgateway"`} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the restricted viewer sees %s:\n%.300s", s.name, leak, body)
			}
		}
	}
}

// metaOf is /api/v1/meta as user, decoded generically.
func metaOf(t *testing.T, srv *httptest.Server, user string) map[string]any {
	t.Helper()
	return decode[map[string]any](t, srv, user, "/api/v1/meta")
}

// TestMetaLimitedView: limitedView is true in secure mode without the full
// read, and absent for a full reader and in local mode.
func TestMetaLimitedView(t *testing.T) {
	e := ruleWorld(t, ruleChange{name: "base"})
	srv := ruleServe(t, e)
	if v, ok := metaOf(t, srv, fullUser)["limitedView"]; ok {
		t.Errorf("full reader: limitedView %v, want absent", v)
	}
	for _, user := range []string{restrictedUser, routeReader, nobodyUser} {
		if v := metaOf(t, srv, user)["limitedView"]; v != true {
			t.Errorf("%s: limitedView %v, want true", user, v)
		}
	}
	if v, ok := metaOf(t, localServe(t, e), restrictedUser)["limitedView"]; ok {
		t.Errorf("local mode: limitedView %v, want absent", v)
	}
	// Hidden objects never decide it.
	if v := metaOf(t, ruleServe(t, ruleWorld(t, change("created"))), restrictedUser)["limitedView"]; v != true {
		t.Errorf("after a hidden change: limitedView %v", v)
	}
}

// capNames are the names of meta's capability states.
func capNames(m map[string]any) []string {
	var out []string
	states, _ := m["capabilityStates"].([]any)
	for _, s := range states {
		out = append(out, fmt.Sprint(s.(map[string]any)["name"]))
	}
	return out
}

// TestMetaControllerLogsDropped: the controller log state comes from the
// controller Pods' log content, so a viewer without the full read gets none, unless it is off.
func TestMetaControllerLogsDropped(t *testing.T) {
	src := ruleWorld(t, change("controller"))
	srv := ruleServe(t, src)
	full, restricted := metaOf(t, srv, fullUser), metaOf(t, srv, restrictedUser)
	if !slices.Contains(capNames(full), "controllerLogs") || !slices.Contains(full["capabilities"].([]any), any("controllerLogs")) {
		t.Fatalf("the full reader lacks controllerLogs: %v", full["capabilityStates"])
	}
	if slices.Contains(capNames(restricted), "controllerLogs") || slices.Contains(restricted["capabilities"].([]any), any("controllerLogs")) {
		t.Errorf("the restricted viewer sees controllerLogs: %v", restricted["capabilityStates"])
	}
	off := ruleServe(t, src, api.WithCoreCapabilities(func() []api.Capability {
		m := src.Current().Model
		return app.CoreCapabilities(m.Snapshot, app.Diagnostics{}, m.Collected)
	}))
	if !slices.Contains(capNames(metaOf(t, off, restrictedUser)), "controllerLogs") {
		t.Error("controllerLogs off is configuration, so every viewer sees it")
	}
}

// TestMetaUnchangedFields: apart from limitedView and controllerLogs, /meta is
// byte-equal for the two viewers: it describes Snellius, not cluster objects.
func TestMetaUnchangedFields(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, change("controller")))
	strip := func(m map[string]any) string {
		delete(m, "limitedView")
		m["capabilities"] = slices.DeleteFunc(m["capabilities"].([]any), func(c any) bool { return c == "controllerLogs" })
		if states, ok := m["capabilityStates"].([]any); ok {
			m["capabilityStates"] = slices.DeleteFunc(states, func(s any) bool { return s.(map[string]any)["name"] == "controllerLogs" })
		}
		return mustJSON(t, m)
	}
	full, restricted := metaOf(t, srv, fullUser), metaOf(t, srv, restrictedUser)
	if len(capNames(full)) < 4 {
		t.Fatalf("meta lists %v; the core capabilities are not wired", capNames(full))
	}
	if f, r := strip(full), strip(restricted); f != r {
		t.Errorf("meta differs between the viewers:\n%s\n  vs %s", f, r)
	}
}

// TestViewerOfMatchesView: the Viewer an extension gets through Deps.View
// answers as the core handlers' view does, for every viewer.
func TestViewerOfMatchesView(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}))
	for _, user := range []string{fullUser, restrictedUser, routeReader, nobodyUser} {
		got := decode[viewerAnswers](t, srv, user, "/api/ext/rule/viewer")
		var gws []string
		for _, r := range decode[api.Page[api.GatewayListRow]](t, srv, user, ruleBase+"/gateways?limit=200").Items {
			gws = append(gws, r.Ref.Namespace+"/"+r.Ref.Name)
		}
		slices.Sort(gws)
		slices.Sort(got.Gateways)
		if !slices.Equal(got.Gateways, gws) {
			t.Errorf("%s: Can admits Gateways %v, /gateways lists %v", user, got.Gateways, gws)
		}
		ids := findingIDs(t, srv, user)
		slices.Sort(ids)
		slices.Sort(got.Shown)
		if !slices.Equal(got.Shown, ids) {
			t.Errorf("%s: Shown admits %d findings, /findings lists %d", user, len(got.Shown), len(ids))
		}
		if !got.Secure || got.ReadsAll != (user == fullUser) {
			t.Errorf("%s: secure %v, reads all %v", user, got.Secure, got.ReadsAll)
		}
	}
	r := decode[viewerAnswers](t, srv, restrictedUser, "/api/ext/rule/viewer")
	if want := map[string]bool{"HTTPRoute/" + ns0: true, "HTTPRoute/" + hiddenNS: false, "HTTPRoute/": false}; !maps.Equal(r.Lists, want) || !r.Named {
		t.Errorf("restricted lists %v (want %v), named %v (want true)", r.Lists, want, r.Named)
	}
	// Outside secure mode the Viewer is not secure and, under AllowAll, reads everything.
	e := ruleWorld(t, ruleChange{name: "base"})
	mounts, err := extension.NewCore(extension.WithHandlers("rule", ruleExt)).Mounts(extension.Deps{Current: e.Current})
	if err != nil {
		t.Fatal(err)
	}
	local := httptest.NewServer(api.NewHandler(e, fstest.MapFS{}, nil, api.WithExtMounts(mounts...)))
	t.Cleanup(local.Close)
	if l := decode[viewerAnswers](t, local, "", "/api/ext/rule/viewer"); l.Secure || !l.ReadsAll {
		t.Errorf("local: secure %v, reads all %v", l.Secure, l.ReadsAll)
	}
}

// TestVisibleToNoHiddenCount: an extension serving objects through Deps.View
// and VisibleTo answers a restricted viewer byte-equal before and after a
// hidden object is created, with no hidden count; the full viewer sees the change.
func TestVisibleToNoHiddenCount(t *testing.T) {
	before, after := ruleServe(t, ruleWorld(t, ruleChange{name: "base"})), ruleServe(t, ruleWorld(t, change("created")))
	const path = "/api/ext/rule/backends"
	if _, b := fetch(t, before, fullUser, path); b == func() string { _, a := fetch(t, after, fullUser, path); return a }() {
		t.Fatal("the created Backend does not reach the full viewer")
	}
	_, b := fetch(t, before, restrictedUser, path)
	_, a := fetch(t, after, restrictedUser, path)
	if a != b {
		t.Errorf("the restricted viewer's extension response changed:\n%.300s\n  vs %.300s", b, a)
	}
	var rows []extBackend
	if err := json.Unmarshal([]byte(b), &rows); err != nil || len(rows) == 0 {
		t.Fatalf("rows %v, %v", rows, err)
	}
	for _, r := range rows {
		if !readable(r.Ref) {
			t.Errorf("hidden Backend %v served", r.Ref)
		}
	}
}

// TestRuleNothingReadable: for a viewer who can read nothing, /installs and
// every install page are byte-equal before and after a hidden object is created in the install.
func TestRuleNothingReadable(t *testing.T) {
	pairs := rulePairs(t, []ruleChange{change("created")})
	for key, diff := range differences(t, pairs, nobodyUser, nil) {
		t.Errorf("%s changed for a viewer who reads nothing under %s:\n%s", key[0], key[1], diff)
	}
	if got := decode[[]api.InstallSummary](t, pairs[0].after, nobodyUser, "/api/v1/installs"); len(got) != 1 || got[0].ID != ruleInstall {
		t.Errorf("installs %+v, want the rule install", got)
	}
}

// TestOverviewDataPlaneShownOnly: the overview's Data plane column reads only
// the 001 and 024 findings and xDS check the viewer is shown; 001 is derived,
// so a restricted viewer sees "unknown", never "ok" or "rejects".
func TestOverviewDataPlaneShownOnly(t *testing.T) {
	srv := ruleServe(t, ruleWorld(t, ruleChange{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: ns0, Name: testutil.ScaleGateway(0)}}}))
	column := func(user string) map[string]string {
		out := map[string]string{}
		for _, g := range decode[api.OverviewResponse](t, srv, user, ruleBase+"/overview").Gateways {
			out[g.Ref.Namespace+"/"+g.Ref.Name] = g.DataPlane
		}
		return out
	}
	gw0 := ns0 + "/" + testutil.ScaleGateway(0)
	if full := column(fullUser); full[gw0] != api.DataPlaneRejects || !slices.Contains(slices.Collect(maps.Values(full)), api.DataPlaneOK) {
		t.Fatalf("full data plane %v, want gw0 rejecting and the rest ok", full)
	}
	for ref, dp := range column(restrictedUser) {
		if dp != api.DataPlaneUnknown {
			t.Errorf("restricted %s: data plane %q, want unknown", ref, dp)
		}
	}
}
