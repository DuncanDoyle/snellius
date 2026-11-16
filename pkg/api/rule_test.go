package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/kinds"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/scopeverify"
)

// The D-085 rule harness (design §13): a change to objects a restricted viewer
// may not read never changes what that viewer is served, apart from the install
// context changes TestRuleInstallContextMayChange names.

// ruleTier has twelve namespaces with a Gateway and a Backend each.
var ruleTier = func() scale.Tier {
	t := scale.Typical
	t.Name, t.Gateways, t.HTTPRoutes, t.ListenerSets, t.Namespaces, t.Backends = "rule", 12, 72, 2, 12, 12
	return t
}()

const (
	ruleInstall = "kgw.kgateway"
	ruleBase    = "/api/v1/installs/" + ruleInstall
	// The two viewers: full may list everything, restricted the first ten scale namespaces.
	fullUser       = "full"
	restrictedUser = "restricted"
)

var (
	// hiddenNS is where every rule change happens; the controller's kgw is hidden too.
	hiddenNS = testutil.ScaleNamespace(11)
	// ruleCommit is the commit clock.
	ruleCommit = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	// ruleSteps are the publication times: a world is computed at each, so
	// objects present from the first are past grace and a stuck count matures.
	ruleSteps = []time.Duration{-10 * time.Minute, -6 * time.Minute, 0}
)

// ruleSnap is a snapshot a change edits.
type ruleSnap struct {
	t *testing.T
	*cache.Snapshot
}

// add adds u under its served kind.
func (s ruleSnap) add(us ...*unstructured.Unstructured) {
	s.t.Helper()
	for _, u := range us {
		gk := u.GroupVersionKind().GroupKind()
		i := slices.IndexFunc(testutil.ServedUpstream(), func(k kinds.Served) bool { return k.Group == gk.Group && k.Kind == gk.Kind })
		if i < 0 {
			s.t.Fatalf("unserved kind %s", gk)
		}
		gvr := testutil.ServedUpstream()[i].GVR()
		s.Objects[gvr] = append(s.Objects[gvr], u)
	}
}

// get returns the stored kind ns/name; the test fails when it is absent.
func (s ruleSnap) get(kind, ns, name string) *unstructured.Unstructured {
	s.t.Helper()
	for _, objs := range s.Objects {
		for _, u := range objs {
			if u.GetKind() == kind && u.GetNamespace() == ns && u.GetName() == name {
				return u
			}
		}
	}
	s.t.Fatalf("no %s %s/%s", kind, ns, name)
	return nil
}

// remove drops kind ns/name.
func (s ruleSnap) remove(kind, ns, name string) {
	s.t.Helper()
	u := s.get(kind, ns, name)
	for gvr, objs := range s.Objects {
		if i := slices.Index(objs, u); i >= 0 {
			s.Objects[gvr] = slices.Delete(objs, i, i+1)
		}
	}
}

// removeDeployment drops the Deployment ns/name.
func (s ruleSnap) removeDeployment(ns, name string) {
	s.Deployments = slices.DeleteFunc(s.Deployments, func(d *appsv1.Deployment) bool { return d.Namespace == ns && d.Name == name })
}

// deployment returns the Deployment ns/name.
func (s ruleSnap) deployment(ns, name string) *appsv1.Deployment {
	s.t.Helper()
	for _, d := range s.Deployments {
		if d.Namespace == ns && d.Name == name {
			return d
		}
	}
	s.t.Fatalf("no Deployment %s/%s", ns, name)
	return nil
}

// routeTo is an HTTPRoute ns/name on parents, accepted by each, sending to the
// Backend beNS/be ("" sends nowhere). status false leaves it without status.
func routeTo(ns, name string, parents []objects.ParentRef, beNS, be string, status bool) *unstructured.Unstructured {
	var st []testutil.RouteParent
	if status {
		for _, p := range parents {
			st = append(st, testutil.AcceptedBy(p, testutil.Controller))
		}
	}
	u := testutil.HTTPRoute(ns, name, parents, st...)
	if be != "" {
		ref := map[string]any{"group": kinds.KgatewayGroup, "kind": "Backend", "name": be}
		if beNS != ns {
			ref["namespace"] = beNS
		}
		u.Object["spec"].(map[string]any)["rules"] = []any{map[string]any{"backendRefs": []any{ref}}}
	}
	if !status {
		delete(u.Object, "status")
	}
	return u
}

// hiddenGW is the parentRef to a Gateway of the hidden namespace.
func hiddenGW(name string) []objects.ParentRef {
	return []objects.ParentRef{testutil.GwRef(hiddenNS, name)}
}

// ruleSnapshot is the base world: ruleTier, plus a hidden Gateway with its
// proxy, a hidden Backend and a hidden route between them, all healthy.
func ruleSnapshot(t *testing.T) ruleSnap {
	s := ruleSnap{t, scale.Snapshot(ruleTier, 1)}
	s.add(testutil.Gateway(hiddenNS, "hidden-gw", scale.Class, nil), testutil.Backend(hiddenNS, "hidden-be", nil),
		routeTo(hiddenNS, "hidden-route", hiddenGW("hidden-gw"), hiddenNS, "hidden-be", true))
	s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, "hidden-gw", 1, 1))
	return s
}

// ruleChange is one change to the world. apply runs before the first
// publication, late before the last; rejects are the Gateways the controller
// metrics report rejecting.
type ruleChange struct {
	name    string
	apply   func(s ruleSnap)
	late    func(s ruleSnap)
	rejects []ctrlmetrics.GatewayKey
}

// stuck adds n hidden routes on hidden-gw whose status is for an older generation.
func stuck(s ruleSnap, n int) {
	for i := range n {
		u := routeTo(hiddenNS, fmt.Sprintf("hidden-stuck-%02d", i), hiddenGW("hidden-gw"), hiddenNS, "hidden-be", true)
		u.SetGeneration(2)
		s.add(u)
	}
}

// failRefs makes every status.parents entry of route u report ResolvedRefs=False.
func failRefs(u *unstructured.Unstructured) {
	setParents(u, "ResolvedRefs", "BackendNotFound")
}

// setParents sets condition typ of every status.parents entry of route u to False with reason.
func setParents(u *unstructured.Unstructured, typ, reason string) {
	ps, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	for _, p := range ps {
		for _, c := range p.(map[string]any)["conditions"].([]any) {
			if c := c.(map[string]any); c["type"] == typ {
				c["status"], c["reason"] = "False", reason
			}
		}
	}
	_ = unstructured.SetNestedSlice(u.Object, ps, "status", "parents")
}

// failFirstParent makes the first status.parents entry of route u not Accepted.
func failFirstParent(u *unstructured.Unstructured) {
	ps, _, _ := unstructured.NestedSlice(u.Object, "status", "parents")
	c := ps[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)
	c["status"], c["reason"] = "False", "NotAllowedByListeners"
	_ = unstructured.SetNestedSlice(u.Object, ps, "status", "parents")
}

// setCondition sets u's top-level condition typ to status.
func setCondition(u *unstructured.Unstructured, typ string, status metav1.ConditionStatus) {
	cs, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for i, c := range cs {
		if m := c.(map[string]any); m["type"] == typ {
			m["status"], m["reason"] = string(status), "Invalid"
			cs[i] = m
		}
	}
	_ = unstructured.SetNestedSlice(u.Object, cs, "status", "conditions")
}

// ruleChanges are the hidden changes no restricted response may show (design §13).
var ruleChanges = []ruleChange{
	{name: "created", apply: func(s ruleSnap) {
		s.add(testutil.Gateway(hiddenNS, "hidden-new-gw", scale.Class, nil), testutil.Backend(hiddenNS, "hidden-new-be", nil),
			routeTo(hiddenNS, "hidden-new-route", hiddenGW("hidden-new-gw"), hiddenNS, "hidden-new-be", true),
			// A hidden route naming a visible Backend.
			routeTo(hiddenNS, "hidden-consumer", hiddenGW("hidden-gw"), testutil.ScaleNamespace(0), "be-0000", true))
		s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, "hidden-new-gw", 1, 1))
	}},
	{name: "deleted", apply: func(s ruleSnap) {
		s.remove("HTTPRoute", hiddenNS, "hidden-route")
		s.remove("Backend", hiddenNS, "hidden-be")
		s.remove("Gateway", hiddenNS, "hidden-gw")
		s.removeDeployment(hiddenNS, "hidden-gw")
	}},
	{name: "faulty", apply: func(s ruleSnap) {
		failFirstParent(s.get("HTTPRoute", hiddenNS, "hidden-route"))
		setCondition(s.get("Backend", hiddenNS, "hidden-be"), "Accepted", metav1.ConditionFalse)
		setCondition(s.get("Gateway", hiddenNS, "hidden-gw"), "Programmed", metav1.ConditionFalse)
	}},
	{name: "pending", late: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-pending", hiddenGW("hidden-gw"), hiddenNS, "hidden-be", false))
	}},
	{name: "stuckThreshold", apply: func(s ruleSnap) { stuck(s, 12) }},
	{name: "threshold", apply: func(s ruleSnap) {
		stuck(s, 12)
		for i := range 3 {
			name := fmt.Sprintf("hidden-down-%d", i)
			s.add(testutil.Gateway(hiddenNS, name, scale.Class, nil))
			s.Deployments = append(s.Deployments, testutil.ProxyDeployment(hiddenNS, name, 2, 0))
		}
		s.deployment(hiddenNS, "hidden-gw").Status.ReadyReplicas = 0
	}},
	{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: hiddenNS, Name: "hidden-gw"}}},
	{name: "gatewayAdded", apply: func(s ruleSnap) {
		s.add(testutil.Gateway(hiddenNS, "hidden-added", scale.Class, nil))
	}},
	{name: "controllerRestart", apply: func(s ruleSnap) {
		restart := metav1.NewTime(testutil.Now.Add(-time.Minute))
		p := s.Pods[0]
		p.Status.ContainerStatuses = []corev1.ContainerStatus{{Name: p.Spec.Containers[0].Name, Ready: true, RestartCount: 1,
			State:                corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: restart}},
			LastTerminationState: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{ExitCode: 2, Reason: "Error", FinishedAt: restart}}}}
	}},
	// The extension check fails on a hidden Backend once it is marked.
	{name: "extension", apply: func(s ruleSnap) {
		s.get("Backend", hiddenNS, "hidden-be").SetLabels(map[string]string{extLabel: "fail"})
	}},
}

// extLabel marks the objects extCheck reports.
const extLabel = "example.com/ext"

// extCheck is an extension check: an error finding on each Backend marked
// with extLabel, and a failed result naming it. Every world runs it.
type extCheck struct{}

func (extCheck) Name() string { return "extension check" }

func (extCheck) Inputs() health.InputSet { return health.InputSet{Clock: true} }

func (c extCheck) Run(ctx health.RunContext) ([]health.Finding, health.CheckResult) {
	res := health.CheckResult{Name: c.Name(), OK: true, Required: true, CollectedAt: ctx.Input.Now}
	var out []health.Finding
	for _, objs := range ctx.Input.Snapshot.Objects {
		for _, u := range objs {
			if u.GetLabels()[extLabel] != "" {
				r := objects.RefOf(u)
				out = append(out, health.Finding{Code: "EXT-001", Slug: "ext", Severity: health.SeverityError, Install: ctx.Input.Install.ID(), Resource: &r,
					Message: "extension says " + r.Name})
				res.OK, res.Error = false, "extension failed on "+r.Namespace+"/"+r.Name
			}
		}
	}
	return out, res
}

// holder serves the snapshot a world's current publication reads.
type holder struct {
	mu sync.Mutex
	s  *cache.Snapshot
}

func (h *holder) Snapshot() *cache.Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.s
}

// sortObjects orders each kind by "namespace/name", as the cache does.
func sortObjects(s *cache.Snapshot) {
	for _, objs := range s.Objects {
		slices.SortFunc(objs, func(x, y *unstructured.Unstructured) int {
			return strings.Compare(x.GetNamespace()+"/"+x.GetName(), y.GetNamespace()+"/"+y.GetName())
		})
	}
}

// ruleWorld computes the base world with c applied, through a fresh engine
// publishing at each of ruleSteps, and returns the engine.
func ruleWorld(t *testing.T, c ruleChange) *engine.Engine {
	t.Helper()
	return worldWith(t, ruleSnapshot(t), c)
}

// worldWith computes s with c applied, as ruleWorld does.
func worldWith(t *testing.T, s ruleSnap, c ruleChange) *engine.Engine {
	t.Helper()
	if c.apply != nil {
		c.apply(s)
	}
	var now time.Time
	clock := func() time.Time { return now }
	h := &holder{}
	e := engine.New(engine.Config{Snapshots: h, Served: testutil.ServedUpstream(), Detectors: []install.Detector{install.UpstreamDetector{}},
		Scope: install.NamespaceSelectorScope{}, Verifier: scopeverify.NewManagedCountVerifier(time.Hour), Checks: append(health.Upstream(), extCheck{}),
		Now: clock, Collected: api.RuleCollected(ruleInstall, clock, c.rejects...)})
	for k, at := range ruleSteps {
		now = testutil.Now.Add(at)
		if k == len(ruleSteps)-1 && c.late != nil {
			c.late(s)
		}
		sortObjects(s.Snapshot)
		s.TakenAt = now
		h.mu.Lock()
		h.s = s.Snapshot
		h.mu.Unlock()
		if m := e.Compute(); len(m.Installs) != 1 {
			t.Fatalf("%s: %d installs, want 1", c.name, len(m.Installs))
		}
	}
	return e
}

// ruleKinds maps every upstream kind and the core kinds findings name.
type ruleKinds struct{}

func (ruleKinds) Lookup(group, kind string) (sar.Kind, bool) {
	switch {
	case group == "apps" && kind == "Deployment":
		return sar.Kind{Group: "apps", Resource: "deployments", Namespaced: true}, true
	case group == "" && kind == "Pod":
		return sar.Kind{Resource: "pods", Namespaced: true}, true
	case group == "" && kind == "Service":
		return sar.Kind{Resource: "services", Namespaced: true}, true
	}
	for _, s := range testutil.ServedUpstream() {
		if s.Group == group && s.Kind == kind {
			return sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced}, true
		}
	}
	return sar.Kind{}, false
}

func (ruleKinds) All() ([]sar.Kind, string) {
	var out []sar.Kind
	for _, s := range testutil.ServedUpstream() {
		out = append(out, sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced})
	}
	return out, "rule"
}

type ruleUserKey struct{}

func ruleIdentity(ctx context.Context) (oidc.Identity, bool) {
	u, ok := ctx.Value(ruleUserKey{}).(string)
	return oidc.Identity{Username: u}, ok && u != ""
}

// ruleChecker allows the full viewer everything and the restricted one every
// list and get in the first ten scale namespaces.
type ruleChecker struct{}

func (ruleChecker) Check(_ context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username == fullUser {
		return true, nil
	}
	for i := range 10 {
		if a.Namespace == testutil.ScaleNamespace(i) {
			return true, nil
		}
	}
	return false, nil
}

// ruleServe serves src in complete secure mode through a real sar.Authorizer,
// the user taken from X-User.
func ruleServe(t *testing.T, src api.ModelSource, opts ...api.HandlerOption) *httptest.Server {
	t.Helper()
	const unlimited = 1 << 30
	az := sar.New(sar.Options{Checker: ruleChecker{}, Kinds: ruleKinds{}, Clock: clocktesting.NewFakeClock(ruleCommit), IdentityFrom: ruleIdentity,
		MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	opts = append([]api.HandlerOption{api.WithSecureMode(true), api.WithCommitClock(func() time.Time { return ruleCommit })}, opts...)
	h := api.NewHandler(src, fstest.MapFS{}, az, opts...)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ruleUserKey{}, r.Header.Get("X-User"))))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// fetch returns the status and body of GET path as user.
func fetch(t *testing.T, srv *httptest.Server, user, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-User", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

// decode is GET path as user decoded into T; it must answer 200.
func decode[T any](t *testing.T, srv *httptest.Server, user, path string) T {
	t.Helper()
	code, b := fetch(t, srv, user, path)
	if code != http.StatusOK {
		t.Fatalf("GET %s as %s: %d %s", path, user, code, b)
	}
	var v T
	if err := json.Unmarshal([]byte(b), &v); err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return v
}

// ruleSurface is one response the rule covers; its path may depend on the worlds compared.
type ruleSurface struct {
	name string
	path func(t *testing.T, before, after *httptest.Server) string
}

func fixed(p string) func(*testing.T, *httptest.Server, *httptest.Server) string {
	return func(*testing.T, *httptest.Server, *httptest.Server) string { return ruleBase + p }
}

// hiddenFindingDetail is the detail path of a finding on a hidden object that
// the full viewer sees in one world only; else of one in both, else of none.
func hiddenFindingDetail(t *testing.T, before, after *httptest.Server) string {
	t.Helper()
	hidden := func(srv *httptest.Server) []string {
		var out []string
		for _, f := range decode[api.Page[api.FindingRow]](t, srv, fullUser, ruleBase+"/findings?limit=200").Items {
			if f.Resource == nil || !readable(f.Resource.Ref) {
				out = append(out, f.ID)
			}
		}
		return out
	}
	b, a := hidden(before), hidden(after)
	for _, id := range append(slices.Clone(a), b...) {
		if !slices.Contains(a, id) || !slices.Contains(b, id) {
			return ruleBase + "/findings/" + url.PathEscape(id)
		}
	}
	if len(a) > 0 {
		return ruleBase + "/findings/" + url.PathEscape(a[0])
	}
	return ruleBase + "/findings/f1-none"
}

// readable reports whether the restricted viewer may read ref.
func readable(ref objects.Ref) bool {
	for i := range 10 {
		if ref.Namespace == testutil.ScaleNamespace(i) {
			return true
		}
	}
	return false
}

var ruleSurfaces = []ruleSurface{
	{name: "/health", path: fixed("/health")},
	{name: "/findings", path: fixed("/findings?limit=200")},
	{name: "/findings?q", path: fixed("/findings?limit=200&q=hidden")},
	{name: "/findings?view=groups", path: fixed("/findings?view=groups&limit=200")},
	{name: "/diagnostics", path: fixed("/diagnostics")},
	{name: "finding detail", path: hiddenFindingDetail},
}

// ruleMasks are, per surface and change, the JSON paths (dot-separated) the
// change may alter because a later task projects them; each names its reason.
var ruleMasks = map[[2]string][]string{
	// The controller summary of a hidden controller is a D-013 placeholder until Task 33 removes it.
	{"/diagnostics", "controllerRestart"}: {"processes.controller"},
}

// ruleNotApplicable are the pairs a change cannot reach for the full viewer either.
var ruleNotApplicable = map[[2]string]string{
	{"/findings?q", "controllerRestart"}: "the restart finding is on the controller Deployment, whose name has no \"hidden\"",
}

// ruleHeldBefore are the pairs the 7.3 code already holds without the projection.
var ruleHeldBefore = map[[2]string]string{}

// mask is body without the fields paths name; a body that is not JSON is unchanged.
func mask(t *testing.T, body string, paths []string) string {
	t.Helper()
	if len(paths) == 0 {
		return body
	}
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return body
	}
	for _, p := range paths {
		drop(v, strings.Split(p, "."))
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// drop deletes the field path names in v; "*" walks every array element.
func drop(v any, path []string) {
	switch x := v.(type) {
	case map[string]any:
		if len(path) == 1 {
			delete(x, path[0])
			return
		}
		drop(x[path[0]], path[1:])
	case []any:
		if path[0] == "*" {
			for _, e := range x {
				drop(e, path[1:])
			}
		}
	}
}

// ruleResponse is one surface's status and body.
type ruleResponse struct {
	code int
	body string
}

// rulePair is a change's before and after servers.
type rulePair struct {
	change        ruleChange
	before, after *httptest.Server
}

// rulePairs serves the base world and each change, with opts.
func rulePairs(t *testing.T, changes []ruleChange, opts ...api.HandlerOption) []rulePair {
	t.Helper()
	before := ruleServe(t, ruleWorld(t, ruleChange{name: "base"}), opts...)
	var out []rulePair
	for _, c := range changes {
		out = append(out, rulePair{change: c, before: before, after: ruleServe(t, ruleWorld(t, c), opts...)})
	}
	return out
}

// differences returns, for user, the surfaces whose masked response differs
// between each pair's worlds, keyed by surface and change.
func differences(t *testing.T, pairs []rulePair, user string, masks map[[2]string][]string) map[[2]string]string {
	t.Helper()
	out := map[[2]string]string{}
	for _, p := range pairs {
		for _, s := range ruleSurfaces {
			key := [2]string{s.name, p.change.name}
			path := s.path(t, p.before, p.after)
			var rs [2]ruleResponse
			for k, srv := range []*httptest.Server{p.before, p.after} {
				code, body := fetch(t, srv, user, path)
				rs[k] = ruleResponse{code, mask(t, body, masks[key])}
			}
			if rs[0] != rs[1] {
				out[key] = fmt.Sprintf("%s: %d %.300s\n  vs %d %.300s", path, rs[0].code, rs[0].body, rs[1].code, rs[1].body)
			}
		}
	}
	return out
}

// TestRuleUnchangedForRestricted: every hidden change leaves every surface
// byte-equal for the restricted viewer (D-085).
func TestRuleUnchangedForRestricted(t *testing.T) {
	for key, diff := range differences(t, rulePairs(t, ruleChanges), restrictedUser, ruleMasks) {
		t.Errorf("%s changed for the restricted viewer under %s:\n%s", key[0], key[1], diff)
	}
}

// TestRuleChangesReachFullReader: each change reaches each surface for the full
// viewer, so the rule tests are not vacuous; ruleNotApplicable lists the exceptions.
func TestRuleChangesReachFullReader(t *testing.T) {
	diffs := differences(t, rulePairs(t, ruleChanges), fullUser, nil)
	for _, c := range ruleChanges {
		for _, s := range ruleSurfaces {
			key := [2]string{s.name, c.name}
			_, differs := diffs[key]
			reason, listed := ruleNotApplicable[key]
			switch {
			case !differs && !listed:
				t.Errorf("%s does not reach %s for the full viewer; list it in ruleNotApplicable with a reason", c.name, s.name)
			case differs && listed:
				t.Errorf("%s reaches %s for the full viewer, but ruleNotApplicable says %q", c.name, s.name, reason)
			}
		}
	}
}

// TestRuleBypassBreaksInvariant: without the projection, every applicable pair
// changes for the restricted viewer, so a regression in any step fails the rule.
func TestRuleBypassBreaksInvariant(t *testing.T) {
	diffs := differences(t, rulePairs(t, ruleChanges, api.WithProjectionBypass()), restrictedUser, ruleMasks)
	for _, c := range ruleChanges {
		for _, s := range ruleSurfaces {
			key := [2]string{s.name, c.name}
			if _, na := ruleNotApplicable[key]; na {
				continue
			}
			_, differs := diffs[key]
			reason, held := ruleHeldBefore[key]
			switch {
			case !differs && !held:
				t.Errorf("%s under %s holds without the projection; list it in ruleHeldBefore with a reason", s.name, c.name)
			case differs && held:
				t.Errorf("%s under %s fails without the projection, but ruleHeldBefore says %q", s.name, c.name, reason)
			}
		}
	}
}

// checksOf is the "What was checked" list of /health and /diagnostics for user.
func checksOf(t *testing.T, srv *httptest.Server, user string) [2][]health.CheckResult {
	t.Helper()
	return [2][]health.CheckResult{
		decode[api.HealthResponse](t, srv, user, ruleBase+"/health").Checks,
		decode[api.Diagnostics](t, srv, user, ruleBase+"/diagnostics").Checks,
	}
}

// collectionChecks are the checks whose collection failures every viewer sees (D-106 item 3).
var collectionChecks = map[string]bool{"cluster access": true, "served kinds": true, "backend endpoints": true}

// TestRuleCheckedListUnchanged: the restricted viewer's "What was checked" is
// the same across every change, and every result but a collection failure is "Not checked".
func TestRuleCheckedListUnchanged(t *testing.T) {
	pairs := rulePairs(t, ruleChanges)
	want := checksOf(t, pairs[0].before, restrictedUser)
	for _, cs := range want {
		for _, c := range cs {
			if collectionChecks[c.Name] {
				continue
			}
			if !c.NotAssessed || !c.OK || c.Error != "" || c.Detail != "" || c.Hold != nil || c.Pending {
				t.Errorf("check %+v: want Not checked", c)
			}
		}
	}
	for _, p := range pairs {
		got := checksOf(t, p.after, restrictedUser)
		for k := range got {
			a, _ := json.Marshal(want[k])
			b, _ := json.Marshal(got[k])
			if string(a) != string(b) {
				t.Errorf("%s: checks changed for the restricted viewer:\n%s\n  vs %s", p.change.name, a, b)
			}
		}
	}
}

// installContext are the hidden changes that may change a restricted viewer's
// output (D-106 item 1, D-107), each with the fields it may change.
var installContext = []struct {
	change ruleChange
	masks  map[string][]string
}{
	{
		// The controller's discovery selector narrows the install to labelled namespaces.
		change: ruleChange{name: "controllerSettings", apply: func(s ruleSnap) {
			d := s.deployment("kgw", "kgateway")
			d.Spec.Template.Spec.Containers[0].Env = append(d.Spec.Template.Spec.Containers[0].Env,
				testutil.Env(install.DiscoveryNamespaceSelectorsEnv, `[{"matchLabels":{"team":"a"}}]`))
		}},
		masks: membershipMasks,
	},
	{
		// A hidden GatewayClass starts naming kgateway's controller, so a readable Gateway joins the install.
		change: ruleChange{name: "classController", apply: func(s ruleSnap) {
			s.get("GatewayClass", "", "other").Object["spec"].(map[string]any)["controllerName"] = testutil.Controller
		}},
		masks: membershipMasks,
	},
}

// membershipMasks are the membership-driven fields of each surface: rows,
// counts, findings and groups. Checks, times and the install ID stay compared.
var membershipMasks = map[string][]string{
	"/health":               {"findings", "counts", "state"},
	"/findings":             {"items", "total", "next", "prev"},
	"/findings?q":           {"items", "total", "next", "prev"},
	"/findings?view=groups": {"items", "total", "next", "prev"},
	"/diagnostics":          {"verdict", "groups", "groupTotal", "dataPlane", "processes"},
}

// classSnapshot is the base world with a GatewayClass of another controller
// and a readable Gateway of that class.
func classSnapshot(t *testing.T) ruleSnap {
	s := ruleSnapshot(t)
	s.add(testutil.GatewayClass("other", "example.com/other", true), testutil.Gateway(testutil.ScaleNamespace(0), "other-gw", "other", nil))
	return s
}

// TestRuleInstallContextMayChange: install-context changes may change only the
// fields their mask names; every other field stays byte-equal.
func TestRuleInstallContextMayChange(t *testing.T) {
	before := ruleServe(t, worldWith(t, classSnapshot(t), ruleChange{name: "base"}))
	for _, ic := range installContext {
		after := ruleServe(t, worldWith(t, classSnapshot(t), ic.change))
		masks := map[[2]string][]string{}
		for s, m := range ic.masks {
			masks[[2]string{s, ic.change.name}] = m
		}
		pair := []rulePair{{change: ic.change, before: before, after: after}}
		for key, diff := range differences(t, pair, restrictedUser, masks) {
			if key[0] == "finding detail" {
				continue // a membership change may move a finding in or out
			}
			t.Errorf("%s changed outside its mask under %s:\n%s", key[0], key[1], diff)
		}
	}
}

// TestRuleCollectionErrorTextHidden: a collection failure reaches a restricted
// viewer with its fixed text, never the API server's, which names a namespace and a ServiceAccount.
func TestRuleCollectionErrorTextHidden(t *testing.T) {
	const raw = `pods is forbidden: User "system:serviceaccount:secret-ops:snellius-reader" cannot list resource "pods" in the namespace "secret-ops"`
	src := ruleWorld(t, ruleChange{name: "denied", apply: func(s ruleSnap) {
		s.Access[cache.PodsGVR] = cache.Access{Err: raw, Denied: true}
	}})
	srv := ruleServe(t, src)
	if _, full := fetch(t, srv, fullUser, ruleBase+"/health"); !strings.Contains(full, "snellius-reader") {
		t.Fatal("the full viewer lacks the API server's text; the fixture does not reach it")
	}
	for _, p := range []string{"/health", "/diagnostics", "/findings?limit=200"} {
		_, body := fetch(t, srv, restrictedUser, ruleBase+p)
		for _, leak := range []string{"secret-ops", "snellius-reader", "serviceaccount"} {
			if strings.Contains(body, leak) {
				t.Errorf("%s: the restricted viewer sees %q:\n%.400s", p, leak, body)
			}
		}
	}
}
