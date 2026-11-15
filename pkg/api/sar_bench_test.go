//go:build bench

package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// benchKinds mirrors pkg/app's sarKinds: Lookup maps the served kinds and the
// core kinds; All lists only the kinds the per-object path authorises.
type benchKinds struct{}

var coreKinds = map[[2]string]sar.Kind{
	{"", "Namespace"}:                     {Resource: "namespaces"},
	{"", "Pod"}:                           {Resource: "pods", Namespaced: true},
	{"apps", "Deployment"}:                {Group: "apps", Resource: "deployments", Namespaced: true},
	{"", "Service"}:                       {Resource: "services", Namespaced: true},
	{"discovery.k8s.io", "EndpointSlice"}: {Group: "discovery.k8s.io", Resource: "endpointslices", Namespaced: true},
	{"", "Event"}:                         {Resource: "events", Namespaced: true},
	{"apiextensions.k8s.io", "CustomResourceDefinition"}: {Group: "apiextensions.k8s.io", Resource: "customresourcedefinitions"},
}

func (benchKinds) Lookup(group, kind string) (sar.Kind, bool) {
	if k, ok := coreKinds[[2]string{group, kind}]; ok {
		return k, true
	}
	for _, s := range testutil.ServedUpstream() {
		if s.Group == group && s.Kind == kind {
			return sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced}, true
		}
	}
	return sar.Kind{}, false
}

func (benchKinds) All() ([]sar.Kind, string) {
	var out []sar.Kind
	for _, s := range testutil.ServedUpstream() {
		out = append(out, sar.Kind{Group: s.Group, Resource: s.Resource, Namespaced: s.Namespaced})
	}
	for _, gk := range [][2]string{{"", "Namespace"}, {"", "Pod"}, {"apps", "Deployment"}, {"", "Service"}} {
		out = append(out, coreKinds[gk])
	}
	return out, "bench"
}

type userKey struct{}

func identityFrom(ctx context.Context) (oidc.Identity, bool) {
	u, ok := ctx.Value(userKey{}).(string)
	return oidc.Identity{Username: u}, ok && u != ""
}

// fakeChecker answers SARs by allow after lat, counting list and get.
type fakeChecker struct {
	lat         time.Duration
	allow       func(sar.Attributes) bool
	lists, gets atomic.Int64
}

func (c *fakeChecker) Check(_ context.Context, _ oidc.Identity, a sar.Attributes) (bool, error) {
	if a.Verb == "list" {
		c.lists.Add(1)
	} else {
		c.gets.Add(1)
	}
	if c.lat > 0 {
		time.Sleep(c.lat)
	}
	return c.allow(a), nil
}

func (c *fakeChecker) total() int64 { return c.lists.Load() + c.gets.Load() }

// granted is one namespace in ten: ns-000, ns-010, … ns-090.
func granted(ns string) bool { return strings.HasSuffix(ns, "0") }

// The three viewers: list denied everywhere with get on the granted
// namespaces' objects; list on the granted namespaces only; list everywhere.
var (
	deniedList = func(a sar.Attributes) bool {
		if a.Verb == "list" {
			return false
		}
		return a.Namespace != "" && granted(a.Namespace) || a.Resource == "namespaces" && granted(a.Name)
	}
	nsGrants = func(a sar.Attributes) bool { return a.Verb == "list" && a.Namespace != "" && granted(a.Namespace) }
	fullRead = func(sar.Attributes) bool { return true }
)

// canView is what both restricted viewers may see.
func canView(r objects.Ref) bool {
	return r.Namespace != "" && granted(r.Namespace) || r.Kind == "Namespace" && granted(r.Name)
}

func newAuthz(c *fakeChecker, clk *clocktesting.FakeClock) *sar.Authorizer {
	// The fake clock never refills the global rate, so the limits are lifted.
	return sar.New(sar.Options{Checker: c, Kinds: benchKinds{}, Clock: clk, IdentityFrom: identityFrom, MaxInFlight: 1 << 30, Rate: 1 << 30, Burst: 1 << 30})
}

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// result is one GET's status, body size, SARs and duration.
type result struct {
	code, body int
	sars       int64
	d          time.Duration
}

func get(h http.Handler, c *fakeChecker, user, path string) result {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req = req.WithContext(context.WithValue(req.Context(), userKey{}, user))
	w := httptest.NewRecorder()
	n := c.total()
	start := time.Now()
	h.ServeHTTP(w, req)
	return result{code: w.Code, body: w.Body.Len(), sars: c.total() - n, d: time.Since(start)}
}

func pct(d []time.Duration, q int) time.Duration {
	slices.Sort(d)
	i := (len(d)*q + 99) / 100
	return d[max(i, 1)-1].Round(10 * time.Microsecond)
}

func heapNow() uint64 {
	runtime.GC()
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.HeapAlloc
}

type page struct{ name, path string }

// TestBudgetSARDeniedList reports the SAR cost of the per-object path at
// Large (design §6.4 bench). Report-only: it logs and never fails on a number.
func TestBudgetSARDeniedList(t *testing.T) {
	build, m := api.LargeHandlers(t)
	ix := m.Installs[0].Index
	base := "/api/v1/installs/" + m.Installs[0].Resolved.ID()
	deepest, depth := api.DeepestChild(ix, canView)
	var svc objects.Ref
	for r, uses := range ix.ByBackend {
		if r.Kind == "Service" && canView(r) && (svc.Name == "" || len(uses) > len(ix.ByBackend[svc]) ||
			len(uses) == len(ix.ByBackend[svc]) && r.String() < svc.String()) {
			svc = r
		}
	}
	if depth == 0 || svc.Name == "" {
		t.Fatalf("no visible deepest child (%s, depth %d) or Service (%s)", deepest, depth, svc)
	}
	t.Logf("deepest visible child %s at depth %d; backend detail %s with %d uses", deepest, depth, svc, len(ix.ByBackend[svc]))
	pages := []page{
		{"/routes", base + "/routes"},
		{"/gateways", base + "/gateways"},
		{"/backends", base + "/backends"},
		{"/paths", base + "/paths"},
		{"route detail", base + "/routes/" + deepest.Group + "/" + deepest.Kind + "/" + deepest.Namespace + "/" + deepest.Name},
		{"backend detail", base + "/backends/-/Service/" + svc.Namespace + "/" + svc.Name},
	}
	secure := func(az api.Authorizer) http.Handler { return build(az, api.WithSecureMode(true)) }

	// Buffered response per page: the full-read viewer sees the most.
	fc := &fakeChecker{allow: fullRead}
	fh := secure(newAuthz(fc, clocktesting.NewFakeClock(t0)))
	for _, p := range pages {
		r := get(fh, fc, "operator", p.path)
		msg := fmt.Sprintf("full-read %-15s %d, %d B buffered, %d SARs", p.name, r.code, r.body, r.sars)
		if p.name[0] == '/' {
			l := get(fh, fc, "operator", p.path+"?limit=200")
			msg += fmt.Sprintf("; ?limit=200 %d, %d B buffered", l.code, l.body)
		}
		t.Log(msg)
	}

	t.Run("denied-list", func(t *testing.T) {
		for _, p := range pages {
			c := &fakeChecker{allow: deniedList}
			h := secure(newAuthz(c, clocktesting.NewFakeClock(t0)))
			cold := get(h, c, "u", p.path)
			warm := get(h, c, "u", p.path)
			polls, sars, last := 2, cold.sars+warm.sars, warm
			for ; last.code != http.StatusOK && polls < 12; polls++ {
				last = get(h, c, "u", p.path)
				sars += last.sars
			}
			// The same viewer outside the commit wrapper: no per-request cap.
			uc := &fakeChecker{allow: deniedList}
			uaz := newAuthz(uc, clocktesting.NewFakeClock(t0))
			req := httptest.NewRequest(http.MethodGet, p.path, nil)
			if _, err := uaz.FullRead(req.WithContext(context.WithValue(req.Context(), userKey{}, "u"))); err != nil {
				t.Fatal(err)
			}
			full := uc.total()
			un := get(build(uaz), uc, "u", p.path)
			t.Logf("denied-list %-15s cold %d SARs → %d (cap hit %v); warm %d SARs → %d; %d polls to %d, %d SARs; uncapped demand %d SARs (%d full-read, %d list, %d get) → %d, %d B",
				p.name, cold.sars, cold.code, cold.code == http.StatusServiceUnavailable, warm.sars, warm.code, polls, last.code, sars,
				un.sars+full, full, uc.lists.Load()-full, uc.gets.Load(), un.code, un.body)
		}
	})

	t.Run("latency", func(t *testing.T) {
		for _, lat := range []time.Duration{0, 2 * time.Millisecond} {
			n := 50
			if lat > 0 {
				n = 12
			}
			for _, p := range pages {
				c := &fakeChecker{lat: lat, allow: deniedList}
				h := secure(newAuthz(c, clocktesting.NewFakeClock(t0)))
				var cold, warm []time.Duration
				var code int
				for i := range n {
					r := get(h, c, fmt.Sprintf("cold-%d", i), p.path)
					cold, code = append(cold, r.d), r.code
				}
				var w result
				for i := 0; i < 12 && (i == 0 || w.code != http.StatusOK); i++ {
					w = get(h, c, "warm", p.path)
				}
				for range 200 {
					warm = append(warm, get(h, c, "warm", p.path).d)
				}
				t.Logf("latency %v %-15s cold (%d, → %d) p50 %v p95 %v; warm (200, → %d) p50 %v p95 %v",
					lat, p.name, n, code, pct(cold, 50), pct(cold, 95), w.code, pct(warm, 50), pct(warm, 95))
			}
		}
	})

	t.Run("namespace-grants", func(t *testing.T) {
		// One user polls one page every 5 s for 2 minutes; the second minute is steady state.
		for _, p := range pages {
			c := &fakeChecker{allow: nsGrants}
			clk := clocktesting.NewFakeClock(t0)
			h := secure(newAuthz(c, clk))
			var first result
			var perMin int64
			ok := 0
			for i := range 24 {
				r := get(h, c, "u", p.path)
				if i == 0 {
					first = r
				}
				if i >= 12 {
					perMin += r.sars
					if r.code == http.StatusOK {
						ok++
					}
				}
				clk.Step(5 * time.Second)
			}
			t.Logf("namespace-grants %-15s first poll %d SARs → %d; steady state %d SARs per minute (target 50), %d of 12 polls 200",
				p.name, first.sars, first.code, perMin, ok)
		}
	})

	t.Run("memory", func(t *testing.T) {
		const entries = 10_000
		c := &fakeChecker{allow: deniedList}
		az := newAuthz(c, clocktesting.NewFakeClock(t0))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), userKey{}, "mem"))
		before := heapNow()
		// 100 namespaces: one list and 99 named gets each.
		for ns := range 100 {
			for j := range 99 {
				ref := objects.Ref{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: fmt.Sprintf("ns-%03d", ns), Name: fmt.Sprintf("r-%05d", ns*99+j)}
				if _, err := az.AuthorizeErr(req, ref); err != nil {
					t.Fatal(err)
				}
			}
		}
		perDecision := float64(heapNow()-before) / entries
		runtime.KeepAlive(az)
		if c.total() != entries {
			t.Fatalf("%d SARs, want %d", c.total(), entries)
		}
		// Identity overhead: 2,000 identities with one decision each.
		ic := &fakeChecker{allow: nsGrants}
		iaz := newAuthz(ic, clocktesting.NewFakeClock(t0))
		before = heapNow()
		for i := range 2000 {
			r := req.WithContext(context.WithValue(req.Context(), userKey{}, fmt.Sprintf("user-%04d", i)))
			if _, err := iaz.CanList(r, objects.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, "ns-000"); err != nil {
				t.Fatal(err)
			}
		}
		perIdentity := float64(heapNow()-before)/2000 - perDecision
		runtime.KeepAlive(iaz)
		const mib = 1 << 20
		t.Logf("memory: %.0f B per cached decision (one identity, %d entries, %.2f MiB); %.0f B per identity besides its decisions",
			perDecision, entries, perDecision*entries/mib, perIdentity)
		t.Logf("memory: projection at 2,000 identities × 10,000 decisions %.0f MiB; at 2,000 × 2,000 decisions %.0f MiB",
			2000*(entries*perDecision+perIdentity)/mib, 2000*(2000*perDecision+perIdentity)/mib)
	})
}
