package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/health"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Handler tests of the lists and details in secure mode (Task 33): visible
// rows, counts and ancestors, states from shown findings, and no placeholders.

// worldPlus computes the base world with extra, then c, applied.
func worldPlus(t *testing.T, extra func(ruleSnap), c ruleChange) *engine.Engine {
	t.Helper()
	s := ruleSnapshot(t)
	if extra != nil {
		extra(s)
	}
	return worldWith(t, s, c)
}

// change is ruleChanges' change named name.
func change(name string) ruleChange {
	return ruleChanges[slices.IndexFunc(ruleChanges, func(c ruleChange) bool { return c.name == name })]
}

// localServe serves src outside secure mode through the registry authoriser nsAuthz.
func localServe(t *testing.T, src api.ModelSource) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(api.NewHandler(src, fstest.MapFS{}, nsAuthz{}))
	t.Cleanup(srv.Close)
	return srv
}

// toHidden is a visible route sending to the hidden Backend; lsAll is the base world's ls-all.
const (
	lsAll    = lsAllName
	toHidden = "to-hidden-be"
)

// neighbours adds toHidden.
func neighbours(s ruleSnap) {
	s.add(routeTo(ns0, toHidden, gw0(), hiddenNS, "hidden-be", true))
}

// hiddenMembers attaches a hidden route to gw0 and one to lsAll.
var hiddenMembers = ruleChange{name: "hiddenMembers", apply: func(s ruleSnap) {
	s.add(routeTo(hiddenNS, "hidden-attached", gw0(), hiddenNS, "hidden-be", true),
		routeTo(hiddenNS, "hidden-on-ls", []objects.ParentRef{lsAllRef}, hiddenNS, "hidden-be", true))
}}

// rowOf is the row of items whose ref is ns/name; the test fails without one.
func rowOf[T any](t *testing.T, items []T, ref func(T) api.LinkedRef, ns, name string) T {
	t.Helper()
	i := slices.IndexFunc(items, func(r T) bool { lr := ref(r); return lr.Namespace == ns && lr.Name == name })
	if i < 0 {
		t.Fatalf("no row %s/%s", ns, name)
	}
	return items[i]
}

func gwRef(r api.GatewayListRow) api.LinkedRef     { return r.Ref }
func lsRowRef(r api.ListenerSetRow) api.LinkedRef  { return r.Ref }
func routeRowRef(r api.RouteRow) api.LinkedRef     { return r.Ref }
func procRef(r api.GatewayProcesses) api.LinkedRef { return r.Gateway }

// counts are the routes columns and per-listener counts of gw0 and lsAll for user.
func counts(t *testing.T, srv *httptest.Server, user string) []int {
	t.Helper()
	gw := rowOf(t, decode[api.Page[api.GatewayListRow]](t, srv, user, ruleBase+"/gateways?limit=200").Items, gwRef, ns0, testutil.ScaleGateway(0))
	ls := rowOf(t, decode[api.Page[api.ListenerSetRow]](t, srv, user, ruleBase+"/listenersets?limit=200").Items, lsRowRef, ns0, lsAll)
	if gw.Routes == nil || ls.Routes == nil {
		t.Fatalf("%s: no routes column: %v %v", user, gw.Routes, ls.Routes)
	}
	out := []int{*gw.Routes, *ls.Routes}
	listeners := decode[api.GatewayDetail](t, srv, user, ruleBase+"/gateways/"+ns0+"/"+testutil.ScaleGateway(0)).Listeners
	listeners = append(listeners, decode[api.ListenerSetDetail](t, srv, user, ruleBase+"/listenersets/"+objects.GatewayAPIGroup+"/ListenerSet/"+ns0+"/"+lsAll).Listeners...)
	for _, l := range listeners {
		if l.ComputedRoutes != nil {
			out = append(out, *l.ComputedRoutes)
		}
	}
	return out
}

// Gateway and ListenerSet routes and per-listener computed counts count only
// the routes the viewer may read; a full reader's counts include hidden ones.
func TestMemberCountsVisibleOnly(t *testing.T) {
	before := ruleServe(t, worldPlus(t, neighbours, ruleChange{name: "base"}))
	after := ruleServe(t, worldPlus(t, neighbours, hiddenMembers))
	if b, a := counts(t, before, restrictedUser), counts(t, after, restrictedUser); !slices.Equal(a, b) {
		t.Errorf("restricted counts %v, want %v", a, b)
	}
	b, a := counts(t, before, fullUser), counts(t, after, fullUser)
	if len(a) != len(b) || a[0] != b[0]+1 || a[1] != b[1]+1 {
		t.Errorf("full counts %v, want the Gateway's and ListenerSet's one above %v", a, b)
	}
}

// A path through a hidden ancestor route or to a hidden root is left out;
// the full viewer sees it.
func TestAncestorHiddenOmitted(t *testing.T) {
	visParent := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: ns0, Name: "vis-parent"}
	hidParent := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute", Namespace: hiddenNS, Name: "hidden-parent"}
	srv := ruleServe(t, worldPlus(t, func(s ruleSnap) {
		s.remove("HTTPRoute", ns0, "child")
		s.add(delegating(ns0, "vis-parent", gw0(), ns0, "child"), child(ns0, "child", hidParent, visParent))
	}, ruleChange{name: "base"}))
	for route, wantFull := range map[string]int{"child": 2, namesHidden: 2} {
		path := ruleBase + "/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + route
		if full := decode[api.RouteDetail](t, srv, fullUser, path).Ancestors; len(full) != wantFull {
			t.Errorf("%s: full viewer %d paths, want %d", route, len(full), wantFull)
		}
		d := decode[api.RouteDetail](t, srv, restrictedUser, path)
		if len(d.Ancestors) != 1 || d.AncestorsTruncated {
			t.Errorf("%s: restricted %d paths (truncated %v), want the visible one", route, len(d.Ancestors), d.AncestorsTruncated)
		}
		for _, p := range d.Ancestors {
			for _, s := range p.Steps {
				if s.Restricted || s.Ref == nil || s.Ref.Link == api.LinkNone {
					t.Errorf("%s: restricted step %+v", route, s)
				}
			}
		}
	}
}

// placeholders are what D-013 shows for hidden objects and facts; secure mode sends none.
var placeholders = []string{`"deniedInInstall"`, `"viaRestricted"`, `"restricted":true`, `"evidenceRestricted"`, `"state":"restricted"`}

// placeholderPaths are the bodies that carry each placeholder outside secure mode.
func placeholderPaths() []string {
	route := func(name string) string {
		return "/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + name
	}
	return []string{"/routes?limit=200", "/gateways?limit=200", "/listenersets?limit=200", "/processes?limit=200", "/diagnostics",
		route("child"), route(namesHidden), route(delegToHidden), route(toHidden), route("ls-route"), route(delegToHidden) + "/delegation",
		"/gateways/" + ns0 + "/" + testutil.ScaleGateway(0), "/listenersets/" + objects.GatewayAPIGroup + "/ListenerSet/" + ns0 + "/" + lsAll}
}

// In secure mode no list or detail carries a placeholder for a restricted
// viewer; outside it, a registry authoriser keeps every one of them.
func TestNoPlaceholdersInSecureMode(t *testing.T) {
	src := worldPlus(t, func(s ruleSnap) {
		neighbours(s)
		ls := objects.ParentRef{Group: objects.GatewayAPIGroup, Kind: "ListenerSet", Namespace: hiddenNS, Name: "hidden-ls"}
		s.add(testutil.ListenerSet(hiddenNS, "hidden-ls", testutil.GwRef(hiddenNS, "hidden-gw"), nil), routeTo(ns0, "ls-route", []objects.ParentRef{ls}, ns0, "be-0000", true))
	}, hiddenMembers)
	secure, local := ruleServe(t, src), localServe(t, src)
	seen := map[string]bool{}
	for _, p := range placeholderPaths() {
		_, sb := fetch(t, secure, restrictedUser, ruleBase+p)
		_, lb := fetch(t, local, "", ruleBase+p)
		for _, ph := range placeholders {
			if strings.Contains(sb, ph) {
				t.Errorf("secure %s carries %s", p, ph)
			}
			seen[ph] = seen[ph] || strings.Contains(lb, ph)
		}
	}
	for _, ph := range placeholders {
		if ph != `"evidenceRestricted"` && !seen[ph] {
			t.Errorf("no local body carries %s; the fixture does not reach it", ph)
		}
	}
	// The data plane block is absent for the restricted viewer, whatever rejects.
	rej := ruleServe(t, worldPlus(t, nil, ruleChange{name: "rejecting", rejects: []ctrlmetrics.GatewayKey{{Namespace: ns0, Name: testutil.ScaleGateway(0)}}}))
	gw := "/gateways/" + ns0 + "/" + testutil.ScaleGateway(0)
	if d := decode[api.GatewayDetail](t, rej, fullUser, ruleBase+gw).DataPlane; d == nil || !d.Rejecting {
		t.Fatalf("full data plane %+v, want rejecting", d)
	}
	if d := decode[api.GatewayDetail](t, rej, restrictedUser, ruleBase+gw); d.DataPlane != nil {
		t.Errorf("restricted data plane %+v, want none", d.DataPlane)
	}
}

// A hidden Gateway's KGWUI-001 and a hidden Backend's rejection leave a
// visible route's health, reason and flags unchanged for the restricted
// viewer; the full viewer sees them lifted.
func TestRowStateFromShownFindings(t *testing.T) {
	base := ruleServe(t, worldPlus(t, neighbours, ruleChange{name: "base"}))
	for name, c := range map[string]ruleChange{namesHidden: change("rejecting"), toHidden: change("faulty")} {
		after := ruleServe(t, worldPlus(t, neighbours, c))
		row := func(srv *httptest.Server, user string) api.RouteRow {
			return rowOf(t, decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200").Items, routeRowRef, ns0, name)
		}
		b, a := row(base, restrictedUser), row(after, restrictedUser)
		if bj, aj := mustJSON(t, b), mustJSON(t, a); bj != aj {
			t.Errorf("%s under %s: restricted row\n%s\n  vs %s", name, c.name, bj, aj)
		}
		f := row(after, fullUser)
		flag := map[string]string{namesHidden: "gateway-rejecting", toHidden: "backend-problem"}[name]
		if f.Health != health.StateWarning || !slices.Contains(f.Flags, flag) || f.Reason == nil {
			t.Errorf("%s under %s: full row %s %v %+v, want warning with %s", name, c.name, f.Health, f.Flags, f.Reason, flag)
		}
		detail := ruleBase + "/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + name
		if bd, ad := decode[api.RouteDetail](t, base, restrictedUser, detail), decode[api.RouteDetail](t, after, restrictedUser, detail); bd.Health != ad.Health || mustJSON(t, bd.Reason) != mustJSON(t, ad.Reason) {
			t.Errorf("%s under %s: restricted detail %s %+v, want %s %+v", name, c.name, ad.Health, ad.Reason, bd.Health, bd.Reason)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The health and state filters use the viewer's states: a route lifted by a
// hidden Gateway's KGWUI-001 is warning for the full viewer only, and unknown
// for the restricted one, whose path names that Gateway (design §8.3).
func TestHealthFilterUsesViewerState(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, nil, change("rejecting")))
	names := func(user, q string) []string {
		var out []string
		for _, r := range decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200&namespace="+ns0+q).Items {
			out = append(out, r.Ref.Name)
		}
		return out
	}
	for _, q := range []string{"&health=warning", "&state=gateway-rejecting"} {
		if !slices.Contains(names(fullUser, q), namesHidden) {
			t.Errorf("full %s: %v lacks %s", q, names(fullUser, q), namesHidden)
		}
		if slices.Contains(names(restrictedUser, q), namesHidden) {
			t.Errorf("restricted %s lists %s", q, namesHidden)
		}
	}
	if !slices.Contains(names(restrictedUser, "&health=unknown"), namesHidden) || slices.Contains(names(restrictedUser, "&health=healthy"), namesHidden) {
		t.Errorf("restricted unknown: %v lacks %s", names(restrictedUser, "&health=unknown"), namesHidden)
	}
	// The health sort puts it among the unknown rows for the restricted viewer: after errors, before healthy rows.
	sorted := names(restrictedUser, "&sort=health.desc")
	if i := slices.Index(sorted, namesHidden); i < 0 || i < slices.Index(sorted, "absent-consumer") || i > slices.Index(sorted, "svc-consumer") {
		t.Errorf("restricted health sort %v", sorted)
	}
}

// The controller's hidden Deployment gives a restricted viewer no controller
// counts and a not-assessed state; the full viewer and local mode keep theirs.
func TestControllerSummaryHiddenDeployment(t *testing.T) {
	src := worldPlus(t, nil, ruleChange{name: "base"})
	srv := ruleServe(t, src)
	p := decode[api.ProcessesResponse](t, srv, restrictedUser, ruleBase+"/processes?limit=200")
	if c := p.Controller; !c.NotAssessed || c.Observed || c.Ready != nil || c.Desired != nil || c.Restricted || len(c.Processes) != 0 {
		t.Errorf("restricted controller set %+v, want not assessed with no counts", c)
	}
	d := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics").Processes
	if c := d.Controller; !c.NotAssessed || c.Ready != nil || c.Desired != nil || c.Restricted || c.State != health.StateUnknown {
		t.Errorf("restricted controller line %+v, want not assessed with no counts", c)
	}
	if !d.Proxies.NotAssessed {
		t.Errorf("restricted proxy line %+v, want not assessed", d.Proxies)
	}
	for _, g := range p.Proxies.Items {
		if !g.NotAssessed || g.Health != health.StateUnknown {
			t.Errorf("restricted proxies of %s: %s, not assessed %v", g.Gateway.Name, g.Health, g.NotAssessed)
		}
	}
	// kindLimitedNS's Deployments are hidden: its Gateway's proxy set has no counts.
	if g := rowOf(t, p.Proxies.Items, procRef, kindLimitedNS, testutil.ScaleGateway(9)); !g.Set.NotAssessed || g.Set.Desired != nil || g.Set.Ready != nil {
		t.Errorf("proxies of the kind-limited Gateway %+v, want no counts", g.Set)
	}
	full := decode[api.ProcessesResponse](t, srv, fullUser, ruleBase+"/processes?limit=200")
	if c := full.Controller; c.NotAssessed || !c.Observed || c.Ready == nil {
		t.Errorf("full controller set %+v", c)
	}
	if c := decode[api.ProcessesResponse](t, localServe(t, src), "", ruleBase+"/processes?limit=200").Controller; !c.Restricted || c.Ready == nil {
		t.Errorf("local restricted controller set %+v, want the D-013 counts", c)
	}
}

// computedPaths are the route, Gateway, ListenerSet and delegation bodies a
// restricted fact may appear in.
func computedPaths() []string {
	route := func(name string) string {
		return "/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + name
	}
	return []string{"/routes?limit=200", route("child"), route(namesHidden), route(delegToHidden), route(toHidden), route(delegToHidden) + "/delegation",
		"/gateways/" + ns0 + "/" + testutil.ScaleGateway(0), "/listenersets/" + objects.GatewayAPIGroup + "/ListenerSet/" + ns0 + "/" + lsAll}
}

// No computed fact reads "restricted" in secure mode; the local registry
// authoriser keeps the state.
func TestNoRestrictedComputedState(t *testing.T) {
	src := worldPlus(t, neighbours, hiddenMembers)
	secure, local := ruleServe(t, src), localServe(t, src)
	seen := false
	for _, p := range computedPaths() {
		if _, b := fetch(t, secure, restrictedUser, ruleBase+p); strings.Contains(b, `"state":"restricted"`) {
			t.Errorf("secure %s has a restricted fact", p)
		}
		_, b := fetch(t, local, "", ruleBase+p)
		seen = seen || strings.Contains(b, `"state":"restricted"`)
	}
	if !seen {
		t.Error("no local body has a restricted fact; the fixture does not reach one")
	}
}

// listenerBodies are gw0's listeners and the attachments of a visible member route.
func listenerBodies(t *testing.T, srv *httptest.Server, user string) [2]string {
	t.Helper()
	gw := decode[api.GatewayDetail](t, srv, user, ruleBase+"/gateways/"+ns0+"/"+testutil.ScaleGateway(0))
	r := decode[api.RouteDetail](t, srv, user, ruleBase+"/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+ns0+"/"+namesHidden)
	return [2]string{mustJSON(t, gw.Listeners), mustJSON(t, r.Attachments)}
}

// The listener-count check compares the whole population: a hidden route on a
// visible All listener leaves the restricted viewer's listener and member
// bodies byte-equal; a viewer who may list routes everywhere sees the check.
func TestListenerCheckNeedsPopulation(t *testing.T) {
	before := ruleServe(t, worldPlus(t, nil, ruleChange{name: "base"}))
	after := ruleServe(t, worldPlus(t, nil, change("listenerCount")))
	if b, a := listenerBodies(t, before, restrictedUser), listenerBodies(t, after, restrictedUser); b != a {
		t.Errorf("restricted bodies changed:\n%s\n  vs %s", b, a)
	}
	gw := ruleBase + "/gateways/" + ns0 + "/" + testutil.ScaleGateway(0)
	for _, l := range decode[api.GatewayDetail](t, after, restrictedUser, gw).Listeners {
		if l.Computed != nil || l.IsolationNote {
			t.Errorf("restricted listener %s: computed %+v, isolation note %v", l.Name, l.Computed, l.IsolationNote)
		}
	}
	// The route reader may list routes everywhere: All listeners show the check as for the full viewer.
	// A Selector listener also needs the Namespaces, which it may not list. Counts are visible members'.
	checks := func(user string) (all, selector []api.ListenerDTO) {
		for _, l := range decode[api.GatewayDetail](t, after, user, gw).Listeners {
			l.ComputedRoutes = nil
			if l.AllowedRoutes.Namespaces.From == "Selector" {
				selector = append(selector, l)
			} else {
				all = append(all, l)
			}
		}
		return all, selector
	}
	fullAll, fullSel := checks(fullUser)
	readerAll, readerSel := checks(routeReader)
	if len(fullAll) == 0 || len(fullSel) == 0 || mustJSON(t, fullAll) != mustJSON(t, readerAll) {
		t.Errorf("route reader's All listeners\n%s\n  want the full viewer's %s", mustJSON(t, readerAll), mustJSON(t, fullAll))
	}
	for _, l := range readerSel {
		if l.Computed != nil || l.IsolationNote {
			t.Errorf("route reader's Selector listener %s has its check: %+v", l.Name, l.Computed)
		}
	}
}

// A readable route admitted only through a finding whose basis is hidden has
// no link, detail or YAML for the restricted viewer, before and after a hidden
// change raises the finding; the full viewer gets the link and YAML once it does.
func TestAdmissionIgnoresHiddenFindings(t *testing.T) {
	// Before, the hidden Gateway the route names belongs to another controller; then it goes.
	other := func(s ruleSnap) {
		s.add(testutil.GatewayClass("other", "example.com/other", true), testutil.Gateway(hiddenNS, "absent-gw", "other", nil))
	}
	gone := ruleChange{name: "gone", apply: func(s ruleSnap) { s.remove("Gateway", hiddenNS, "absent-gw") }}
	yaml := ruleBase + "/resources/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + namesAbsent
	detail := ruleBase + "/routes/" + objects.GatewayAPIGroup + "/HTTPRoute/" + ns0 + "/" + namesAbsent
	for _, w := range []struct {
		name    string
		c       ruleChange
		finding bool
	}{{"another controller's Gateway", ruleChange{name: "base"}, false}, {"Gateway gone", gone, true}} {
		srv := ruleServe(t, worldPlus(t, other, w.c))
		if has := len(on(findingsOf(t, srv, fullUser), ns0, namesAbsent)) > 0; has != w.finding {
			t.Fatalf("%s: full viewer has a finding %v", w.name, has)
		}
		for _, r := range decode[api.Page[api.RouteRow]](t, srv, restrictedUser, ruleBase+"/routes?limit=200").Items {
			if r.Ref.Name == namesAbsent && r.Ref.Link != api.LinkNone {
				t.Errorf("%s: restricted link %s", w.name, r.Ref.Link)
			}
		}
		for _, p := range []string{yaml, detail} {
			if code, _ := fetch(t, srv, restrictedUser, p); code != http.StatusNotFound {
				t.Errorf("%s: restricted %s answers %d", w.name, p, code)
			}
		}
		code, _ := fetch(t, srv, fullUser, yaml)
		if want := map[bool]int{false: http.StatusNotFound, true: http.StatusOK}[w.finding]; code != want {
			t.Errorf("%s: full YAML %d, want %d", w.name, code, want)
		}
		if w.finding {
			if l := rowOf(t, decode[api.Page[api.RouteRow]](t, srv, fullUser, ruleBase+"/routes?limit=200").Items, routeRowRef, ns0, namesAbsent).Ref.Link; l == api.LinkNone {
				t.Errorf("%s: full link none", w.name)
			}
		}
	}
}

// selGW is a visible Gateway whose only listener selects namespaces by label.
const selGW = "sel-gw"

// selectorGateway adds selGW to ns0, selecting namespaces labelled sel=on, and
// denies Snellius the Namespace watch, so a route that would pass makes its count unknown.
func selectorGateway(s ruleSnap) {
	g := testutil.Gateway(ns0, selGW, "kgateway", nil)
	g.Object["spec"].(map[string]any)["listeners"].([]any)[0].(map[string]any)["allowedRoutes"] = map[string]any{
		"namespaces": map[string]any{"from": "Selector", "selector": map[string]any{"matchLabels": map[string]any{"sel": "on"}}}}
	s.add(g)
	s.Access[cache.NamespacesGVR] = cache.Access{Err: "namespaces is forbidden", Denied: true}
}

// A hidden route that would make a visible listener's count unknown leaves the
// restricted viewer's routes column, sort and computed count as they were.
func TestListenerUnknownFromVisibleOnly(t *testing.T) {
	hidden := ruleChange{name: "hiddenOnSelector", apply: func(s ruleSnap) {
		s.add(routeTo(hiddenNS, "hidden-on-sel", []objects.ParentRef{testutil.GwRef(ns0, selGW)}, hiddenNS, "hidden-be", true))
	}}
	before := ruleServe(t, worldPlus(t, selectorGateway, ruleChange{name: "base"}))
	after := ruleServe(t, worldPlus(t, selectorGateway, hidden))
	row := func(srv *httptest.Server, user string) api.GatewayListRow {
		return rowOf(t, decode[api.Page[api.GatewayListRow]](t, srv, user, ruleBase+"/gateways?limit=200&sort=routes").Items, gwRef, ns0, selGW)
	}
	if r := row(after, fullUser); r.Routes != nil {
		t.Fatalf("full routes column %d, want unknown: the fixture does not reach the unknown count", *r.Routes)
	}
	same(t, before, after, restrictedUser, "/gateways?limit=200&sort=routes", "/gateways/"+ns0+"/"+selGW)
	if r := row(after, restrictedUser); r.Routes == nil || *r.Routes != 0 {
		t.Errorf("restricted routes column %v, want 0", r.Routes)
	}
}

// probing gives gw0 a proxy Pod on another version with readiness probe
// failures and an Event whose earlier count was not observed.
func probing(s ruleSnap) {
	p := testutil.ProxyPod(ns0, testutil.ScaleGateway(0), 0, "abc", 0)
	p.Spec.Containers[0].Image += "-skewed"
	p.Spec.Containers[0].ReadinessProbe = &corev1.Probe{}
	s.ProxyPods = append(s.ProxyPods, p)
	s.deployment(ns0, testutil.ScaleGateway(0)).Spec.Selector = &metav1.LabelSelector{MatchLabels: p.Labels}
	s.Access[cache.ProxyPodsGVR], s.Access[cache.EventsGVR] = cache.Access{OK: true}, cache.Access{OK: true}
	s.EventDeltas = map[types.UID][]cache.Observation{p.UID: {{At: testutil.Now.Add(-time.Minute), Reason: "Unhealthy", Delta: 3}}}
	s.EventHistoryUnknown = map[types.UID]bool{p.UID: true}
}

// Probe facts come from Events under a global cap: without cluster-wide Events
// list a projecting viewer gets none. Version skew is derived, so a "Not
// checked" row has none either.
func TestProbeFactsNeedEventsList(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, probing, ruleChange{name: "base"}))
	proc := func(user string) api.GatewayProcesses {
		return rowOf(t, decode[api.ProcessesResponse](t, srv, user, ruleBase+"/processes?limit=200").Proxies.Items, procRef, ns0, testutil.ScaleGateway(0))
	}
	probed := func(g api.GatewayProcesses) bool {
		return slices.ContainsFunc(g.Set.Processes, func(p api.ProcessDTO) bool { return p.Probe != nil })
	}
	if full := proc(fullUser); !probed(full) {
		t.Fatalf("full viewer: no probe facts on %+v; the fixture does not reach them", full.Set.Processes)
	}
	r := proc(restrictedUser)
	if probed(r) || len(r.Set.Processes) == 0 {
		t.Errorf("restricted viewer: probe facts %+v", r.Set.Processes)
	}
	// The full read's proof leaves Events out, so it alone does not grant probe facts.
	if f := proc(fullNoInputs); probed(f) || !f.Skew {
		t.Errorf("full read without Events: probe facts %+v, skew %v (want none, and skew as under the full read)", f.Set.Processes, f.Skew)
	}
	if r.Skew {
		t.Error("restricted viewer: skew on a Not checked row")
	}
	if d := decode[api.Diagnostics](t, srv, restrictedUser, ruleBase+"/diagnostics").Processes.Proxies; d.Skew != 0 {
		t.Errorf("restricted proxy line skew %d", d.Skew)
	}
}
