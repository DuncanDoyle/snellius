package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/ctrlmetrics"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Handler tests of the two route signals (design §8, D-087) on the rule world.

const (
	// unattachedRoute has readable refs and no status; freeRoute names no parent and no backend.
	unattachedRoute = "sig-unattached"
	freeRoute       = "sig-free"
	// rulelessRoute is accepted on gw0 and writes no rule; lostRoute is accepted
	// on a listener gw0 does not have, as when the Gateway lost it.
	rulelessRoute = "sig-ruleless"
	lostRoute     = "sig-lost"
	// mirrorRoute sends to the readable Service shared and mirrors to the hidden hidden-svc.
	mirrorRoute = "sig-mirror"
)

// signalNeighbours adds toHidden and the sig- routes.
func signalNeighbours(s ruleSnap) {
	neighbours(s)
	lost := gw0()[0]
	lost.SectionName = "gone"
	mirror := serviceRoute(ns0, mirrorRoute, gw0(), ns0, "shared")
	mirror.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["filters"] = []any{map[string]any{"type": "RequestMirror",
		"requestMirror": map[string]any{"backendRef": map[string]any{"name": "hidden-svc", "namespace": hiddenNS, "port": int64(80)}}}}
	s.add(routeTo(ns0, unattachedRoute, gw0(), ns0, "be-0000", false), testutil.HTTPRoute(ns0, freeRoute, nil),
		testutil.HTTPRoute(ns0, rulelessRoute, gw0(), testutil.AcceptedBy(gw0()[0], testutil.Controller)),
		routeTo(ns0, lostRoute, []objects.ParentRef{lost}, ns0, "be-0000", true), mirror)
}

var (
	gw0Param   = objects.GatewayAPIGroup + "/Gateway/" + ns0 + "/" + testutil.ScaleGateway(0)
	lsAllParam = objects.GatewayAPIGroup + "/ListenerSet/" + ns0 + "/" + lsAllName
)

// signals are a route's health, reason and two signals as sent, every reason member included.
type signals struct {
	Health string          `json:"health"`
	Reason json.RawMessage `json:"reason"`
	Config *struct {
		State  string        `json:"state"`
		Reason *signalReason `json:"reason"`
	} `json:"config"`
	Path *struct {
		State      string        `json:"state"`
		Reason     *signalReason `json:"reason"`
		Incomplete bool          `json:"incomplete"`
	} `json:"path"`
}

// signalReason is a signal's reason as sent.
type signalReason struct {
	Kind    string         `json:"kind"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	More    int            `json:"more"`
	Backend *api.LinkedRef `json:"backend"`
}

// routeSurfaces are the route list, the Gateway and ListenerSet route tables and the route details of names.
func routeSurfaces(names ...string) []string {
	out := []string{"/routes?limit=200", "/routes?limit=200&parent=" + gw0Param, "/routes?limit=200&parent=" + lsAllParam}
	for _, n := range names {
		out = append(out, "/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+ns0+"/"+n)
	}
	return out
}

// signalsOf are the signals of every ns0 route on every surface for user, keyed by surface and route.
func signalsOf(t *testing.T, srv *httptest.Server, user string, names ...string) map[[2]string]signals {
	t.Helper()
	out := map[[2]string]signals{}
	for _, p := range routeSurfaces(names...) {
		code, b := fetch(t, srv, user, ruleBase+p)
		if code != http.StatusOK {
			t.Fatalf("GET %s as %s: %d", p, user, code)
		}
		var body struct {
			Ref   api.LinkedRef `json:"ref"`
			Items []struct {
				Ref api.LinkedRef `json:"ref"`
				signals
			} `json:"items"`
			signals
		}
		if err := json.Unmarshal([]byte(b), &body); err != nil {
			t.Fatal(err)
		}
		if body.Ref.Name != "" {
			out[[2]string{p, body.Ref.Name}] = body.signals
		}
		for _, it := range body.Items {
			if it.Ref.Namespace == ns0 {
				out[[2]string{p, it.Ref.Name}] = it.signals
			}
		}
	}
	return out
}

// want is a route's expected config state and code, path state, reason and incompleteness, and health.
type want struct {
	config, configCode, path, pathReason string
	incomplete                           bool
	health                               string
}

func checkWant(t *testing.T, got map[[2]string]signals, user string, wants map[string]want) {
	t.Helper()
	seen := map[string]int{}
	for k, s := range got {
		w, ok := wants[k[1]]
		if !ok {
			continue
		}
		seen[k[1]]++
		if s.Config == nil || s.Path == nil {
			t.Errorf("%s %s %s: no config or path", user, k[0], k[1])
			continue
		}
		code, reason := "", ""
		if s.Config.Reason != nil {
			code = s.Config.Reason.Code
		}
		if s.Path.Reason != nil {
			reason = s.Path.Reason.Kind
		}
		g := want{s.Config.State, code, s.Path.State, reason, s.Path.Incomplete, s.Health}
		if g != w {
			t.Errorf("%s %s %s: %+v, want %+v", user, k[0], k[1], g, w)
		}
	}
	for n := range wants {
		if seen[n] == 0 {
			t.Errorf("%s: %s on no surface", user, n)
		}
	}
}

// The incompleteness matrix on every route surface (design §8.1, brief): an
// unreadable ref, absent or present, makes the path unknown and incomplete;
// a readable ref to an absent Service is the ordinary 013, in config and on
// its path row, and complete; an unattached route keeps its pending path and
// one without path rows is not attached. The full viewer is never incomplete.
func TestRouteSignalsOnRouteSurfaces(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, signalNeighbours, ruleChange{name: "base"}))
	// Details open for the routes below; names-absent's row is admitted by a hidden finding, freeRoute's for its YAML only.
	names := []string{"svc-consumer", "absent-consumer", namesHidden, toHidden, unattachedRoute}
	incomplete := want{"healthy", "", "unknown", "path-incomplete", true, "unknown"}
	absent := want{"error", "KGWUI-013", "error", "path-problem", false, "error"}
	unattached := want{"warning", "KGWUI-003", "unknown", "path-problem", false, "warning"}
	notAttached := want{"healthy", "", "unknown", "not-attached", false, "healthy"}
	checkWant(t, signalsOf(t, srv, restrictedUser, names...), restrictedUser, map[string]want{
		"svc-consumer":    {"healthy", "", "healthy", "", false, "healthy"},
		"absent-consumer": absent,
		namesHidden:       incomplete,
		namesAbsent:       incomplete,
		toHidden:          incomplete,
		unattachedRoute:   unattached,
		freeRoute:         notAttached,
	})
	checkWant(t, signalsOf(t, srv, fullUser, names...), fullUser, map[string]want{
		"svc-consumer":    {"healthy", "", "healthy", "", false, "healthy"},
		"absent-consumer": absent,
		// hidden-gw's proxy is not ready (KGWUI-006): an error on names-hidden's path rows through it.
		namesHidden:     {"healthy", "", "error", "path-problem", false, "healthy"},
		toHidden:        {"healthy", "", "healthy", "", false, "healthy"},
		unattachedRoute: unattached,
		freeRoute:       notAttached,
	})
}

// A hidden fault reaches the full viewer's path with its reason and the
// restricted viewer's not at all: the path stays incomplete (design §8.3).
func TestRouteSignalsHiddenFault(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, signalNeighbours, change("faulty")))
	checkWant(t, signalsOf(t, srv, fullUser, toHidden), fullUser, map[string]want{toHidden: {"healthy", "", "error", "backend-problem", false, "warning"}})
	checkWant(t, signalsOf(t, srv, restrictedUser, toHidden), restrictedUser,
		map[string]want{toHidden: {"healthy", "", "unknown", "path-incomplete", true, "unknown"}})
}

// namedServices is ruleChecker with the restricted viewer's Service list in
// ns-001 denied: Services there are readable by name only (D-100 item 1).
type namedServices struct{ ruleChecker }

func (c namedServices) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username == restrictedUser && a.Verb == "list" && a.Resource == "services" && a.Namespace == testutil.ScaleNamespace(1) {
		return false, nil
	}
	return c.ruleChecker.Check(ctx, id, a)
}

// A route's ref to an absent Service the viewer may get by name but not list
// keeps its ordinary KGWUI-013 in config, and the path is complete.
func TestNamedReadableAbsentTargetKeepsFinding(t *testing.T) {
	ns1 := testutil.ScaleNamespace(1)
	gw1 := []objects.ParentRef{testutil.GwRef(ns1, testutil.ScaleGateway(1))}
	src := worldPlus(t, func(s ruleSnap) { s.add(failRefs(serviceRoute(ns1, "named-absent", gw1, ns1, "nowhere"))) }, ruleChange{name: "base"})
	srv := ruleServeWith(t, src, namedServices{})
	r := rowOf(t, decode[api.Page[api.RouteRow]](t, srv, restrictedUser, ruleBase+"/routes?limit=200&namespace="+ns1).Items, routeRowRef, ns1, "named-absent")
	if r.Config.State != "error" || r.Config.Reason == nil || r.Config.Reason.Code != "KGWUI-013" || r.Path.Incomplete {
		t.Errorf("config %+v, path %+v", r.Config, r.Path)
	}
}

// worstRank orders path row states: error, warning, unknown, healthy.
var worstRank = map[string]int{"error": 0, "warning": 1, "unknown": 2, "healthy": 3}

// The route path is the worst of the route's /paths rows for every viewer
// (design §8.1): never healthy while a readable row shows a problem. In this
// world every written target is list-readable where it is readable at all,
// so the route's rows and /paths admit the same rows.
func TestRoutePathMatchesPaths(t *testing.T) {
	for _, c := range []ruleChange{{name: "base"}, change("faulty"), change("rejecting")} {
		srv := ruleServe(t, worldPlus(t, signalNeighbours, c))
		for _, user := range []string{fullUser, restrictedUser} {
			checked := 0
			for _, r := range decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200&namespace="+ns0).Items {
				ps := decode[api.PathsResponse](t, srv, user, ruleBase+"/paths?limit=200&route="+r.Ref.Group+"/"+r.Ref.Kind+"/"+r.Ref.Namespace+"/"+r.Ref.Name)
				// Mirror rows are no path rows (D-111).
				worst, rows := "healthy", 0
				for _, row := range ps.Rows {
					if row.Mirror {
						continue
					}
					rows++
					if worstRank[string(row.Problem.State)] < worstRank[worst] {
						worst = string(row.Problem.State)
					}
				}
				var want string
				switch {
				case worst == "error" || worst == "warning":
					want = worst
				case r.Path.Incomplete || worst == "unknown":
					want = "unknown"
				// Without rows: not attached or no path, unless the route writes no rule.
				case rows == 0 && r.Ref.Name != rulelessRoute:
					want = "unknown"
				default:
					want = "healthy"
				}
				if string(r.Path.State) != want {
					t.Errorf("%s %s %s: path %s, its %d /paths rows' worst %s", c.name, user, r.Ref.Name, r.Path.State, rows, worst)
				}
				if worst != "healthy" {
					checked++
				}
			}
			if checked == 0 {
				t.Errorf("%s %s: no route with a path problem; the case is vacuous", c.name, user)
			}
		}
	}
}

// twoPortSvc serves port 80 with ready endpoints and port 81 with none.
const twoPortSvc = "two-port"

// portWorld adds twoPortSvc and a route to each of its ports.
func portWorld(s ruleSnap) {
	s.Services = append(s.Services, cache.ServiceFact{Namespace: ns0, Name: twoPortSvc, Type: "ClusterIP",
		Ports: []cache.ServicePort{{Name: "a", Port: 80, Target: 8080, Protocol: "TCP"}, {Name: "b", Port: 81, Target: 8081, Protocol: "TCP"}}})
	s.EndpointSlices = append(s.EndpointSlices, cache.SliceFact{Namespace: ns0, Service: twoPortSvc,
		Ports: []cache.SlicePort{{Name: "a", Port: 8080}, {Name: "b", Port: 8081}}, Ready: []int{2, 0}, Total: 2, AddressType: "IPv4"})
	for _, p := range []int64{80, 81} {
		u := serviceRoute(ns0, fmt.Sprintf("port-%d", p), gw0(), ns0, twoPortSvc)
		u.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["backendRefs"].([]any)[0].(map[string]any)["port"] = p
		s.add(u)
	}
}

// A Service port without ready endpoints colours only the routes that use
// that port: the path follows the row's port, as /paths does.
func TestRoutePathPortAttribution(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, portWorld, ruleChange{name: "base"}))
	for _, user := range []string{fullUser, restrictedUser} {
		got := signalsOf(t, srv, user)
		ok, bad := got[[2]string{"/routes?limit=200", "port-80"}], got[[2]string{"/routes?limit=200", "port-81"}]
		if ok.Path == nil || bad.Path == nil || ok.Path.State != "healthy" || bad.Path.State == "healthy" {
			t.Errorf("%s: port 80 path %+v, port 81 path %+v", user, ok.Path, bad.Path)
		}
	}
}

// config= and path= filter the route list and the Gateway route table by the
// viewer's signals: values within one OR, the two AND; a bad value is a 400.
func TestRouteSignalFilters(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, signalNeighbours, ruleChange{name: "base"}))
	names := func(user, q string) []string {
		var out []string
		for _, r := range decode[api.Page[api.RouteRow]](t, srv, user, ruleBase+"/routes?limit=200&namespace="+ns0+q).Items {
			out = append(out, r.Ref.Name)
		}
		return out
	}
	for _, c := range []struct {
		user, q   string
		has, lack []string
	}{
		{restrictedUser, "&path=unknown", []string{namesHidden, namesAbsent, toHidden}, []string{"svc-consumer", "absent-consumer"}},
		{restrictedUser, "&path=unknown&path=healthy", []string{namesHidden, "svc-consumer"}, nil},
		{restrictedUser, "&config=error", []string{"absent-consumer"}, []string{namesHidden, namesAbsent}},
		{restrictedUser, "&config=healthy&path=unknown", []string{namesHidden, namesAbsent}, []string{"absent-consumer", "svc-consumer"}},
		{fullUser, "&config=warning&path=unknown", []string{namesAbsent}, []string{"absent-consumer", namesHidden}},
		{restrictedUser, "&parent=" + gw0Param + "&path=unknown", []string{namesHidden, toHidden}, []string{"svc-consumer"}},
		{fullUser, "&path=unknown", nil, []string{namesHidden, toHidden}},
	} {
		got := names(c.user, c.q)
		for _, n := range c.has {
			if !slices.Contains(got, n) {
				t.Errorf("%s %s: %v lacks %s", c.user, c.q, got, n)
			}
		}
		for _, n := range c.lack {
			if slices.Contains(got, n) {
				t.Errorf("%s %s: %v lists %s", c.user, c.q, got, n)
			}
		}
	}
	for _, q := range []string{"&path=bad", "&config=bad"} {
		if code, _ := fetch(t, srv, fullUser, ruleBase+"/routes?namespace="+ns0+q); code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", q, code)
		}
	}
}

// The rule case per signal field (D-085): under each hidden change the
// restricted viewer's field, reason members included, is byte-equal on every
// route surface and the full viewer's moves. Turning the row-state step off
// moves config and health. No step admits a path row with a hidden
// dependency (design §7.4 rule 4), so path has none to turn off.
func TestRuleRouteSignals(t *testing.T) {
	for _, c := range []struct {
		field, change, route string
		step                 bool
	}{
		{"config", "computedTrigger", "child", true},
		{"path", "faulty", toHidden, false},
		{"health", "rejecting", namesHidden, true},
		{"health", "faulty", toHidden, true},
	} {
		field := func(s signals) string {
			b, _ := json.Marshal(map[string]any{"config": s.Config, "path": s.Path, "health": s.Health}[c.field])
			return string(b)
		}
		moved := func(user string, opts ...api.HandlerOption) bool {
			before := signalsOf(t, ruleServe(t, worldPlus(t, signalNeighbours, ruleChange{name: "base"}), opts...), user, c.route)
			after := signalsOf(t, ruleServe(t, worldPlus(t, signalNeighbours, change(c.change)), opts...), user, c.route)
			n := 0
			for k, b := range before {
				if k[1] == c.route {
					n++
				}
				if a, ok := after[k]; !ok || field(a) != field(b) {
					return true
				}
			}
			if n < 2 {
				t.Fatalf("%s: %s on %d surfaces", c.field, c.route, n)
			}
			return false
		}
		if moved(restrictedUser) {
			t.Errorf("%s under %s changes for the restricted viewer", c.field, c.change)
		}
		if !moved(fullUser) {
			t.Errorf("%s under %s does not reach the full viewer", c.field, c.change)
		}
		if c.step && !moved(restrictedUser, api.WithProjectionStepOff("rowState")) {
			t.Errorf("%s under %s holds without the row-state projection", c.field, c.change)
		}
	}
}

// path.incomplete is decided by permissions alone: for the restricted viewer
// it is byte-equal under every hidden change on every route surface, and the
// full viewer's is always false.
func TestRulePathIncomplete(t *testing.T) {
	incomplete := func(user string, c ruleChange) map[[2]string]bool {
		out := map[[2]string]bool{}
		for k, s := range signalsOf(t, ruleServe(t, worldPlus(t, signalNeighbours, c)), user, namesHidden, toHidden, "svc-consumer") {
			if s.Path == nil {
				t.Fatalf("%v: no path", k)
			}
			out[k] = s.Path.Incomplete
		}
		return out
	}
	base := incomplete(restrictedUser, ruleChange{name: "base"})
	if !base[[2]string{"/routes?limit=200", namesHidden}] || base[[2]string{"/routes?limit=200", "svc-consumer"}] {
		t.Fatalf("restricted base incompleteness %v", base)
	}
	for _, c := range ruleChanges {
		after := incomplete(restrictedUser, c)
		for k, b := range base {
			// Deleting hidden-gw takes child out of the install (D-107).
			if a, ok := after[k]; (!ok || a != b) && (c.name != "deleted" || k[1] != "child") {
				t.Errorf("%s: %v incomplete %v, was %v", c.name, k, a, b)
			}
		}
		for k, v := range incomplete(fullUser, c) {
			if v {
				t.Errorf("%s: full %v incomplete", c.name, k)
			}
		}
	}
}

// An accepted route without path rows: one whose Gateway lost the listener
// has no path, for every viewer and without a permission gap; one writing no
// rule is healthy. Health stays today's (design §8.3); the full viewer also
// gets the derived KGWUI-012 on the lost route's config.
func TestRoutePathAcceptedWithoutRows(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, signalNeighbours, ruleChange{name: "base"}))
	for user, lost := range map[string]want{
		fullUser:       {"warning", "KGWUI-012", "unknown", "no-path", false, "warning"},
		restrictedUser: {"healthy", "", "unknown", "no-path", false, "healthy"},
	} {
		checkWant(t, signalsOf(t, srv, user, lostRoute, rulelessRoute), user, map[string]want{
			lostRoute:     lost,
			rulelessRoute: {"healthy", "", "healthy", "", false, "healthy"},
		})
	}
}

// Mirror rows never decide the path or its completeness (D-111): a hidden
// mirror target beside a readable primary leaves the path healthy and
// complete, and the full viewer's faulty mirror target does not reach it.
func TestRoutePathIgnoresMirror(t *testing.T) {
	healthy := map[string]want{mirrorRoute: {"healthy", "", "healthy", "", false, "healthy"}}
	srv := ruleServe(t, worldPlus(t, signalNeighbours, change("faulty")))
	checkWant(t, signalsOf(t, srv, restrictedUser, mirrorRoute), restrictedUser, healthy)
	got := signalsOf(t, srv, fullUser, mirrorRoute)
	for k, s := range got {
		if k[1] == mirrorRoute && (s.Path == nil || s.Path.State != "healthy") {
			t.Errorf("full %s: path %+v", k[0], s.Path)
		}
	}
}

// namedRoutes is ruleChecker with HTTPRoute list in ns-002 denied for namedRouteUser, who gets them by name.
type namedRoutes struct{ ruleChecker }

const namedRouteUser = "named-route"

func (c namedRoutes) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	if id.Username == namedRouteUser && a.Verb == "list" && a.Resource == "httproutes" && a.Namespace == testutil.ScaleNamespace(2) {
		return false, nil
	}
	return c.ruleChecker.Check(ctx, id, a)
}

// A route read by name only opens its detail with its ordinary path and
// health: its own rows admit it by name, as the detail does.
func TestNamedOnlyRouteDetailPath(t *testing.T) {
	ns2 := testutil.ScaleNamespace(2)
	src := worldPlus(t, func(s ruleSnap) {
		s.Services = append(s.Services, service(ns2, "named-svc"))
		s.EndpointSlices = append(s.EndpointSlices, slice(ns2, "named-svc", 2))
		s.add(serviceRoute(ns2, "named-route", []objects.ParentRef{testutil.GwRef(ns2, testutil.ScaleGateway(2))}, ns2, "named-svc"))
	}, ruleChange{name: "base"})
	srv := ruleServeWith(t, src, namedRoutes{})
	d := decode[api.RouteDetail](t, srv, namedRouteUser, ruleBase+"/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+ns2+"/named-route")
	full := decode[api.RouteDetail](t, srv, fullUser, ruleBase+"/routes/"+objects.GatewayAPIGroup+"/HTTPRoute/"+ns2+"/named-route")
	if d.Path.State != full.Path.State || d.Path.Incomplete || d.Health != full.Health {
		t.Errorf("named-only: path %+v health %s; full: path %+v health %s", d.Path, d.Health, full.Path, full.Health)
	}
}

// The findings projection decides the path's finding signals on an admitted
// row: gw0's derived KGWUI-001 reaches the restricted viewer's path only with
// that step off (D-106).
func TestPathFindingsProjection(t *testing.T) {
	extra := func(s ruleSnap) { s.add(routeTo(ns0, "to-be0", gw0(), ns0, "be-0000", true)) }
	rejects := ruleChange{name: "gw0Rejects", rejects: []ctrlmetrics.GatewayKey{{Namespace: ns0, Name: testutil.ScaleGateway(0)}}}
	path := func(opts ...api.HandlerOption) string {
		srv := ruleServe(t, worldPlus(t, extra, rejects), opts...)
		return string(rowOf(t, decode[api.Page[api.RouteRow]](t, srv, restrictedUser, ruleBase+"/routes?limit=200&namespace="+ns0).Items,
			routeRowRef, ns0, "to-be0").Path.State)
	}
	if on, off := path(), path(api.WithProjectionStepOff("findings")); on != "healthy" || off != "error" {
		t.Errorf("restricted path %s with the projection, %s without; want healthy, error", on, off)
	}
}
