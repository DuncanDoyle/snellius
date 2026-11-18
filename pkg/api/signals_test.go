package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// Handler tests of the two route signals (design §8, D-087) on the rule world.

const (
	// unattachedRoute has readable refs and no status; freeRoute names no parent and no backend.
	unattachedRoute = "sig-unattached"
	freeRoute       = "sig-free"
)

// signalNeighbours adds toHidden, unattachedRoute and freeRoute.
func signalNeighbours(s ruleSnap) {
	neighbours(s)
	s.add(routeTo(ns0, unattachedRoute, gw0(), ns0, "be-0000", false), testutil.HTTPRoute(ns0, freeRoute, nil))
}

var (
	gw0Param   = objects.GatewayAPIGroup + "/Gateway/" + ns0 + "/" + testutil.ScaleGateway(0)
	lsAllParam = objects.GatewayAPIGroup + "/ListenerSet/" + ns0 + "/" + lsAllName
)

// signals are a route's health, reason and two signals as sent.
type signals struct {
	Health string          `json:"health"`
	Reason json.RawMessage `json:"reason"`
	Config *struct {
		State  string `json:"state"`
		Reason *struct {
			Kind string `json:"kind"`
			Code string `json:"code"`
		} `json:"reason"`
	} `json:"config"`
	Path *struct {
		State  string `json:"state"`
		Reason *struct {
			Kind string `json:"kind"`
		} `json:"reason"`
		Incomplete bool `json:"incomplete"`
	} `json:"path"`
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
// a readable ref to an absent Service is the ordinary 013 and complete; an
// unattached route and one without path rows keep their ordinary path. The
// full viewer is never incomplete.
func TestRouteSignalsOnRouteSurfaces(t *testing.T) {
	srv := ruleServe(t, worldPlus(t, signalNeighbours, ruleChange{name: "base"}))
	// Details open for the routes below; names-absent's row is admitted by a hidden finding, freeRoute's for its YAML only.
	names := []string{"svc-consumer", "absent-consumer", namesHidden, toHidden, unattachedRoute}
	incomplete := want{"healthy", "", "unknown", "path-incomplete", true, "unknown"}
	checkWant(t, signalsOf(t, srv, restrictedUser, names...), restrictedUser, map[string]want{
		"svc-consumer":    {"healthy", "", "healthy", "", false, "healthy"},
		"absent-consumer": {"error", "KGWUI-013", "healthy", "", false, "error"},
		namesHidden:       incomplete,
		namesAbsent:       incomplete,
		toHidden:          incomplete,
		unattachedRoute:   {"warning", "KGWUI-003", "healthy", "", false, "warning"},
		freeRoute:         {"healthy", "", "healthy", "", false, "healthy"},
	})
	checkWant(t, signalsOf(t, srv, fullUser, names...), fullUser, map[string]want{
		"svc-consumer":    {"healthy", "", "healthy", "", false, "healthy"},
		"absent-consumer": {"error", "KGWUI-013", "healthy", "", false, "error"},
		namesHidden:       {"healthy", "", "healthy", "", false, "healthy"},
		toHidden:          {"healthy", "", "healthy", "", false, "healthy"},
		unattachedRoute:   {"warning", "KGWUI-003", "healthy", "", false, "warning"},
		freeRoute:         {"healthy", "", "healthy", "", false, "healthy"},
	})
}

// A hidden fault reaches the full viewer's path as its reason kind and the
// restricted viewer's not at all: the path stays incomplete (design §8.3).
func TestRouteSignalsHiddenFault(t *testing.T) {
	for _, c := range []struct {
		change, route, reason string
	}{{"rejecting", namesHidden, "gateway-rejecting"}, {"faulty", toHidden, "backend-problem"}} {
		srv := ruleServe(t, worldPlus(t, signalNeighbours, change(c.change)))
		checkWant(t, signalsOf(t, srv, fullUser, c.route), fullUser, map[string]want{c.route: {"healthy", "", "error", c.reason, false, "warning"}})
		checkWant(t, signalsOf(t, srv, restrictedUser, c.route), restrictedUser,
			map[string]want{c.route: {"healthy", "", "unknown", "path-incomplete", true, "unknown"}})
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
		{fullUser, "&config=warning&path=healthy", []string{namesAbsent}, []string{"absent-consumer", namesHidden}},
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
// restricted viewer's field is byte-equal on every route surface, the full
// viewer's moves, and turning the row-state projection off moves it.
func TestRuleRouteSignals(t *testing.T) {
	for _, c := range []struct {
		field, change, route string
	}{
		{"config", "computedTrigger", "child"},
		{"path", "rejecting", namesHidden},
		{"path", "faulty", toHidden},
		{"health", "rejecting", namesHidden},
		{"health", "faulty", toHidden},
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
		if !moved(restrictedUser, api.WithProjectionStepOff("rowState")) {
			t.Errorf("%s under %s holds without the row-state projection", c.field, c.change)
		}
	}
}
