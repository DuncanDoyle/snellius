//go:build bench

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/internal/testutil/scale"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/auth/session"
	"github.com/DuncanDoyle/snellius/pkg/cache"
	"github.com/DuncanDoyle/snellius/pkg/explore"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
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

// fakeChecker answers SARs by allow after lat, counting list and get and the peak in flight.
type fakeChecker struct {
	lat            time.Duration
	allow          func(sar.Attributes) bool
	lists, gets    atomic.Int64
	inFlight, peak atomic.Int64
	// sent and answered, when set, are called as each SAR starts and as it returns.
	sent, answered func(ctx context.Context, user string, a sar.Attributes)
}

func (c *fakeChecker) Check(ctx context.Context, id oidc.Identity, a sar.Attributes) (bool, error) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for p := c.peak.Load(); n > p && !c.peak.CompareAndSwap(p, n); p = c.peak.Load() {
	}
	if c.sent != nil {
		c.sent(ctx, id.Username, a)
	}
	if a.Verb == "list" {
		c.lists.Add(1)
	} else {
		c.gets.Add(1)
	}
	if c.lat > 0 {
		time.Sleep(c.lat)
	}
	if c.answered != nil {
		c.answered(ctx, id.Username, a)
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

type viewer struct {
	name  string
	allow func(sar.Attributes) bool
}

var viewers = []viewer{{"denied-list", deniedList}, {"namespace-grants", nsGrants}}

// canView is what both restricted viewers may see.
func canView(r objects.Ref) bool {
	return r.Namespace != "" && granted(r.Namespace) || r.Kind == "Namespace" && granted(r.Name)
}

// lifted lifts the global limits, which a frozen fake clock never refills; scan turns on the named-get scan.
func lifted(o *sar.Options) { o.MaxInFlight, o.Rate, o.Burst = 1<<30, 1<<30, 1<<30 }
func scan(o *sar.Options)   { o.NamedGetScan = true }

// newAuthz is a real Authorizer over c with the D-100 defaults, then opts.
func newAuthz(c *fakeChecker, clk clock.WithTicker, reg *obs.Registry, opts ...func(*sar.Options)) *sar.Authorizer {
	o := sar.Options{Checker: c, Kinds: benchKinds{}, Clock: clk, IdentityFrom: identityFrom, Metrics: reg}
	for _, f := range opts {
		f(&o)
	}
	return sar.New(o)
}

var t0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// fastClock runs scale times faster than wall time from t0, so limiter minutes pass in seconds.
type fastClock struct {
	start time.Time
	scale time.Duration
}

func (f fastClock) Now() time.Time                         { return t0.Add(time.Since(f.start) * f.scale) }
func (f fastClock) Since(t time.Time) time.Duration        { return f.Now().Sub(t) }
func (f fastClock) After(d time.Duration) <-chan time.Time { return time.After(d / f.scale) }
func (f fastClock) NewTimer(d time.Duration) clock.Timer {
	return clock.RealClock{}.NewTimer(d / f.scale)
}
func (f fastClock) Sleep(d time.Duration)                 { time.Sleep(d / f.scale) }
func (f fastClock) Tick(d time.Duration) <-chan time.Time { return clock.RealClock{}.Tick(d / f.scale) }
func (f fastClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.RealClock{}.NewTicker(d / f.scale)
}

// result is one GET's status, body size, SARs and duration.
type result struct {
	code, body int
	sars       int64
	d          time.Duration
}

func request(user, path string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	return req.WithContext(context.WithValue(req.Context(), userKey{}, user))
}

func get(h http.Handler, c *fakeChecker, user, path string) result {
	w := httptest.NewRecorder()
	n := c.total()
	start := time.Now()
	h.ServeHTTP(w, request(user, path))
	return result{code: w.Code, body: w.Body.Len(), sars: c.total() - n, d: time.Since(start)}
}

func pct(d []time.Duration, q int) time.Duration {
	d = slices.Clone(d)
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

// metric reads one unlabelled series from reg's exposition.
func metric(reg *obs.Registry, name string) float64 {
	w := httptest.NewRecorder()
	reg.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.Lines(w.Body.String()) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+" "); ok {
			f, _ := strconv.ParseFloat(v, 64)
			return f
		}
	}
	return 0
}

type page struct {
	name, path string
	// limit is the D-100 item 7 cold target; 0 means report only.
	limit int64
}

// mark is the target verdict of n cold SARs on p.
func (p page) mark(n int64) string {
	switch {
	case p.limit == 0:
		return "report"
	case n <= p.limit:
		return fmt.Sprintf("✅ ≤ %d", p.limit)
	}
	return fmt.Sprintf("❌ > %d", p.limit)
}

// TestBudgetSARDeniedList reports the SAR cost of the permission checks at
// Large against D-100 item 7. Report-only but for the load subtest, which
// checks many viewers' cold pages against D-116.
func TestBudgetSARDeniedList(t *testing.T) {
	build, m := api.LargeHandlers(t)
	base := "/api/v1/installs/" + m.Installs[0].Resolved.ID()
	route, svc := detailPages(t, m.Installs[0].Index, base)
	pages := []page{
		{"/routes", base + "/routes", 400},
		{"/gateways", base + "/gateways", 400},
		{"/listenersets", base + "/listenersets", 400},
		{"/backends", base + "/backends", 550},
		{"/paths", base + "/paths", 400},
		{"/findings", base + "/findings", 400},
		{"/diagnostics", base + "/diagnostics", 0},
		{"/installs", "/api/v1/installs", 0},
		{"/meta", "/api/v1/meta", 0},
		{"route detail", route, 0},
		{"backend detail", svc, 0},
	}
	secure := secureWith(build)
	fake := func(v viewer, opts ...func(*sar.Options)) (*fakeChecker, *clocktesting.FakeClock, http.Handler) {
		c, clk := &fakeChecker{allow: v.allow}, clocktesting.NewFakeClock(t0)
		return c, clk, secure(newAuthz(c, clk, nil, append([]func(*sar.Options){lifted}, opts...)...), clk.Now)
	}

	// Buffered response per page: the full-read viewer sees the most.
	fc, _, fh := fake(viewer{"full-read", fullRead})
	for _, p := range pages {
		r := get(fh, fc, "operator", p.path)
		msg := fmt.Sprintf("full-read %-15s %d, %d B buffered, %d SARs", p.name, r.code, r.body, r.sars)
		if p.limit > 0 {
			l := get(fh, fc, "operator", p.path+"?limit=200")
			msg += fmt.Sprintf("; ?limit=200 %d, %d B buffered", l.code, l.body)
		}
		t.Log(msg)
	}

	t.Run("cold-warm", func(t *testing.T) {
		for _, v := range viewers {
			for _, p := range pages {
				c, _, h := fake(v)
				cold := get(h, c, "u", p.path)
				warm := get(h, c, "u", p.path)
				t.Logf("%s %-15s cold %d SARs → %d (%s); warm %d SARs → %d",
					v.name, p.name, cold.sars, cold.code, p.mark(cold.sars), warm.sars, warm.code)
			}
		}
	})

	t.Run("scoped", func(t *testing.T) {
		// One allowed namespace (D-100 item 8): every list's cold SARs against its unscoped count and target.
		lists := append(slices.Clone(pages[:6]), page{"/processes", base + "/processes", 400})
		for _, v := range viewers {
			for _, p := range lists {
				c, _, h := fake(v)
				all := get(h, c, "u", p.path)
				c, _, h = fake(v)
				scoped := get(h, c, "u", p.path+"?namespaces=ns-000")
				verdict := "below unscoped"
				if scoped.sars >= all.sars {
					verdict = "⚠ NOT below unscoped"
				}
				t.Logf("scoped %s %-13s cold unscoped %d SARs → %d (%s); namespaces=ns-000 %d SARs → %d (%s, %s)",
					v.name, p.name, all.sars, all.code, p.mark(all.sars), scoped.sars, scoped.code, p.mark(scoped.sars), verdict)
			}
		}
	})

	t.Run("named-get-scan", func(t *testing.T) {
		for _, v := range viewers {
			for _, p := range pages {
				c, _, h := fake(v, scan)
				cold := get(h, c, "u", p.path)
				warm := get(h, c, "u", p.path)
				t.Logf("scan %s %-15s cold %d SARs → %d (cap %d hit %v); warm %d SARs → %d",
					v.name, p.name, cold.sars, cold.code, api.CheckLimit, cold.sars >= api.CheckLimit, warm.sars, warm.code)
			}
		}
	})

	t.Run("polling", func(t *testing.T) {
		// One user polls every 5 s for 6 minutes: minutes 1–5 hold the allow refreshes and one deny refresh.
		for _, v := range viewers {
			for _, p := range pages {
				c, clk, h := fake(v)
				var first result
				var perMin [6]int64
				ok := 0
				for i := range 72 {
					r := get(h, c, "u", p.path)
					if i == 0 {
						first = r
					}
					perMin[i/12] += r.sars
					if r.code == http.StatusOK {
						ok++
					}
					clk.Step(5 * time.Second)
				}
				var sum, peak int64
				for _, n := range perMin[1:] {
					sum, peak = sum+n, max(peak, n)
				}
				avg := float64(sum) / 5
				verdict := "report"
				if p.name == "/routes" {
					verdict = map[bool]string{true: "✅ ≤ 150", false: "❌ > 150"}[avg <= 150]
				}
				t.Logf("polling %s %-15s first %d SARs → %d; minutes 1–5 average %.0f SARs/min (%s), peak minute %d, per minute %v; %d of 72 polls 200",
					v.name, p.name, first.sars, first.code, avg, verdict, peak, perMin, ok)
			}
		}
	})

	t.Run("latency", func(t *testing.T) {
		// Limits lifted: the cost of the checks alone.
		for _, lat := range []time.Duration{0, 2 * time.Millisecond} {
			n := 30
			if lat > 0 {
				n = 12
			}
			for _, v := range viewers {
				for _, p := range pages {
					c, clk := &fakeChecker{lat: lat, allow: v.allow}, clocktesting.NewFakeClock(t0)
					h := secure(newAuthz(c, clk, nil, lifted), clk.Now)
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
					for range 100 {
						warm = append(warm, get(h, c, "warm", p.path).d)
					}
					t.Logf("latency %v %s %-15s cold (%d, → %d) p50 %v p95 %v; warm (100, → %d) p50 %v p95 %v",
						lat, v.name, p.name, n, code, pct(cold, 50), pct(cold, 95), w.code, pct(warm, 50), pct(warm, 95))
				}
			}
		}
	})

	t.Run("limits-on", func(t *testing.T) {
		// Real clock and the D-100 limits (32 in flight, 100/s, burst 500), 2 ms per SAR.
		// Peak in flight is one cold request's share of the shared 4,096-flight cap.
		for _, v := range viewers {
			for _, p := range pages {
				var full []time.Duration
				var code int
				var sars, peak int64
				for i := range 3 {
					c := &fakeChecker{lat: 2 * time.Millisecond, allow: v.allow}
					h := secure(newAuthz(c, clock.RealClock{}, nil), time.Now)
					r := get(h, c, fmt.Sprintf("cold-%d", i), p.path)
					full, code, sars, peak = append(full, r.d), r.code, r.sars, max(peak, c.peak.Load())
				}
				// Spend the whole burst first, as other viewers' cold pages would.
				c := &fakeChecker{lat: 2 * time.Millisecond, allow: v.allow}
				az := newAuthz(c, clock.RealClock{}, nil)
				var wg sync.WaitGroup
				for g := range sar.DefaultMaxInFlight {
					wg.Go(func() {
						for i := g; i < sar.DefaultBurst; i += sar.DefaultMaxInFlight {
							gk := objects.GroupKind{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}
							_, _ = az.CanList(request("drain", "/"), gk, fmt.Sprintf("drain-%03d", i))
						}
					})
				}
				wg.Wait()
				d := get(secure(az, time.Now), c, "cold", p.path)
				t.Logf("limits-on %s %-15s %d SARs, peak %d in flight; cold with the burst full (3) → %d p50 %v max %v; cold after the burst is spent → %d in %v",
					v.name, p.name, sars, peak, code, pct(full, 50), pct(full, 100), d.code, d.d.Round(time.Millisecond))
			}
		}
	})

	t.Run("load", func(t *testing.T) {
		for _, p := range []page{pages[0], pages[4], pages[3]} {
			t.Run(strings.TrimPrefix(p.name, "/"), func(t *testing.T) { loadTest(t, secure, p) })
		}
	})

	t.Run("memory", func(t *testing.T) {
		// Namespace-grants viewers' cold /routes fill the cache past the 8 MiB budget.
		reg := obs.NewRegistry()
		c := &fakeChecker{allow: nsGrants}
		clk := clocktesting.NewFakeClock(t0)
		az := newAuthz(c, clk, reg, lifted)
		h := secure(az, clk.Now)
		get(h, c, "warmup", pages[0].path)
		n0, e0 := c.total(), metric(reg, "snellius_authz_cache_evictions_total")
		before := heapNow()
		users, evictedAt := 0, 0
		for ; evictedAt == 0 || users < 2*evictedAt; users++ {
			if r := get(h, c, fmt.Sprintf("mem-%04d", users), pages[0].path); r.code != http.StatusOK {
				t.Fatalf("mem-%04d: %d", users, r.code)
			}
			if evictedAt == 0 && metric(reg, "snellius_authz_cache_evictions_total") > e0 {
				evictedAt = users + 1
			}
		}
		retained := float64(heapNow()) - float64(before)
		// The handler holds the Large model, so it must outlive the measurement too.
		runtime.KeepAlive(h)
		runtime.KeepAlive(az)
		accounted, evicted := metric(reg, "snellius_authz_cache_bytes"), metric(reg, "snellius_authz_cache_evictions_total")
		// Held entries: one decision per SAR and one full read per viewer, less the evicted.
		sars := c.total() - n0
		held := float64(n0+sars+int64(users)+1) - evicted
		const mib = 1 << 20
		t.Logf("memory: %d viewers' cold /routes (eviction from viewer %d), %d SARs, %.0f evicted, %.0f entries held, %.0f identities",
			users, evictedAt, sars, evicted, held, metric(reg, "snellius_authz_identities"))
		t.Logf("memory: accounted %.0f B (%.2f MiB of the 8 MiB budget), retained heap %.0f B (%.2f MiB, %.2f× accounted); %.0f B retained and %.0f B accounted per entry",
			accounted, accounted/mib, retained, retained/mib, retained/accounted, retained/held, accounted/held)
	})
}

// pollKey carries a poll's state through its request and the SARs it sends.
type pollKey struct{}

// pollState is one poll's SARs sent and, per check key, when its first counted
// miss was spent; denied keys are its deny refreshes spent, refreshed those answered.
type pollState struct {
	sars      atomic.Int64
	mu        sync.Mutex
	pending   map[any]time.Time
	denied    map[any]bool
	refreshed int
}

// viewerLoad is one load viewer's polls: its first 200, then the scored polls
// and those answered 200, and its deny refreshes answered in time. A cut poll
// ended after the deadline and scores as a failure, whatever it answered.
type viewerLoad struct {
	ok, busy       atomic.Bool
	first, last    time.Duration
	after, afterOK int
	cut, refreshes int
}

// record scores a poll that started at start and ended at end (both since the
// storm), answered code and completed refreshes deny refreshes; late marks one
// that ended after the deadline.
func (v *viewerLoad) record(start, end time.Duration, code, refreshes int, late bool) {
	ok := code == http.StatusOK && !late
	switch {
	case v.ok.Load():
		v.after, v.last = v.after+1, start
		if !late {
			v.refreshes += refreshes
		}
		if ok {
			v.afterOK++
		}
	case ok:
		v.first = end
		v.ok.Store(true)
	}
	if late {
		v.cut++
	}
}

// A poll that ends after the deadline scores as a failure, a 200 too, and its deny refreshes give no coverage.
func TestLoadScoresLatePollsAsFailures(t *testing.T) {
	v := &viewerLoad{}
	v.record(0, time.Second, http.StatusOK, 0, false)
	v.record(5*time.Second, 6*time.Second, http.StatusOK, 1, false)
	v.record(10*time.Second, 99*time.Second, http.StatusOK, 1, true)
	if v.after != 2 || v.afterOK != 1 || v.cut != 1 || v.refreshes != 1 {
		t.Errorf("after %d, 200 %d, cut %d, deny refreshes %d; want 2, 1, 1, 1", v.after, v.afterOK, v.cut, v.refreshes)
	}
	late := &viewerLoad{}
	late.record(0, 99*time.Second, http.StatusOK, 0, true)
	if late.ok.Load() || late.cut != 1 {
		t.Errorf("a late first 200 counts as the first 200 (cut %d)", late.cut)
	}
}

// answerKey is one viewer's check, as the authoriser keys it.
type answerKey struct {
	user string
	key  any
}

// missTrace names why each counted check missed, per build and before or after
// its viewer's first 200: first ask, allow-expired, deny-refresh, or evicted
// (answered before, absent now). It also keeps each check's longest wait from miss to SAR.
type missTrace struct {
	mu       sync.Mutex
	answered map[answerKey]bool
	byBuild  map[int]map[string]int
	byPhase  [2]map[string]int
	// wait is the longest from a miss to its SAR; fullWait that of a cluster-wide list.
	wait, fullWait time.Duration
}

func newMissTrace() *missTrace {
	return &missTrace{answered: map[answerKey]bool{}, byBuild: map[int]map[string]int{}, byPhase: [2]map[string]int{{}, {}}}
}

func (m *missTrace) missed(user string, build int, reason string, key any, after bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cause := map[string]string{api.MissAllowExpired: "allow-expired", api.MissDenyExpired: "deny-refresh", "": "unnamed"}[reason]
	if reason == api.MissAbsent {
		cause = "first"
		if m.answered[answerKey{user, key}] {
			cause = "evicted"
		}
	}
	if m.byBuild[build] == nil {
		m.byBuild[build] = map[string]int{}
	}
	m.byBuild[build][cause]++
	phase := 0
	if after {
		phase = 1
	}
	m.byPhase[phase][cause]++
}

func (m *missTrace) answer(user string, a sar.Attributes) {
	m.mu.Lock()
	m.answered[answerKey{user, a}] = true
	m.mu.Unlock()
}

func (m *missTrace) waited(a sar.Attributes, d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.wait = max(m.wait, d)
	if a.Verb == "list" && a.Namespace == "" && a.Name == "" {
		m.fullWait = max(m.fullWait, d)
	}
}

// loadTest runs 50 namespace-grants viewers polling p every 5 s at once with
// the D-100 limits, on a clock 10× wall time, until each is past its deny
// refresh, under a hard deadline. Every viewer gets a first 200 within all
// viewers' cold SARs at the default rate plus a minute, then scores at least
// minScored polls through an answered deny refresh, of which at least 95 % answer 200;
// a poll that ends after the deadline scores as a failure. One viewer's cold SARs keep
// p's D-100 target; no poll passes the check cap; a viewer warmed before the
// storm answers quickly while cold requests queue; the SARs keep the in-flight,
// rate and burst limits (D-116). It logs why each counted check missed.
func loadTest(t *testing.T, secure func(api.Authorizer, func() time.Time, ...api.HandlerOption) http.Handler, p page) {
	const viewers, scale, lat, every = 50, 10, 2 * time.Millisecond, 5 * time.Second
	// minScored is the fewest polls that make the 95 % share meaningful: one failure in 20 still passes.
	// Polls slow down when refreshes near the default rate (D-118), so coverage is a deny TTL of time, not of polls.
	const minScored = 20
	// Four admission places (32 in flight ÷ 8 per request): more cold viewers in a poll means some queue.
	const places = 4
	const warmUser = "warm"
	// One viewer's cold SARs without limits; every viewer asks the same.
	cc, cclk := &fakeChecker{allow: nsGrants}, clocktesting.NewFakeClock(t0)
	cold := get(secure(newAuthz(cc, cclk, nil, lifted), cclk.Now), cc, "u", p.path)
	if cold.code != http.StatusOK {
		t.Fatalf("one cold viewer: %d", cold.code)
	}
	if cold.sars > p.limit {
		t.Errorf("load %s: one cold viewer asked %d SARs, target ≤ %d", p.name, cold.sars, p.limit)
	}
	bound := time.Duration(float64(viewers*cold.sars)/sar.DefaultRate*float64(time.Second)) + time.Minute
	// Polls start until the last first 200 allowed plus two deny TTLs and a minute; a poll gets one more minute to finish.
	// Two TTLs give each viewer about 70 scored polls at saturation, so 95 % no longer turns on one slow poll.
	window, grace := bound+2*sar.DenyTTL+time.Minute, time.Minute

	clk := fastClock{start: time.Now(), scale: scale}
	load := map[string]*viewerLoad{}
	for u := range viewers {
		load[fmt.Sprintf("viewer-%02d", u)] = &viewerLoad{}
	}
	var mu sync.Mutex
	var sends []time.Time
	var reasons []string
	var capHits atomic.Int64
	trace := newMissTrace()
	after := func(user string) bool { v := load[user]; return v == nil || v.ok.Load() }
	c := &fakeChecker{lat: lat / scale, allow: nsGrants}
	c.sent = func(ctx context.Context, user string, a sar.Attributes) {
		now := clk.Now()
		if ps, ok := ctx.Value(pollKey{}).(*pollState); ok {
			ps.sars.Add(1)
			ps.mu.Lock()
			at, ok := ps.pending[a]
			delete(ps.pending, a)
			ps.mu.Unlock()
			if ok {
				trace.waited(a, now.Sub(at))
			}
		}
		mu.Lock()
		sends = append(sends, now)
		mu.Unlock()
	}
	c.answered = func(ctx context.Context, user string, a sar.Attributes) {
		trace.answer(user, a)
		if ps, ok := ctx.Value(pollKey{}).(*pollState); ok {
			ps.mu.Lock()
			if ps.denied[a] {
				delete(ps.denied, a)
				ps.refreshed++
			}
			ps.mu.Unlock()
		}
	}
	audit := func(r *http.Request, reason string) {
		if r.Context().Err() != nil {
			reason = "cut " + reason
		}
		mu.Lock()
		reasons = append(reasons, reason)
		mu.Unlock()
	}
	missed := func(r *http.Request, build int, reason string, key any) {
		user, _ := r.Context().Value(userKey{}).(string)
		if ps, ok := r.Context().Value(pollKey{}).(*pollState); ok {
			ps.mu.Lock()
			if _, seen := ps.pending[key]; !seen {
				ps.pending[key] = clk.Now()
			}
			if reason == api.MissDenyExpired {
				ps.denied[key] = true
			}
			ps.mu.Unlock()
		}
		trace.missed(user, build, reason, key, after(user))
	}
	reg := obs.NewRegistry()
	h := secure(newAuthz(c, clk, reg), clk.Now, api.WithAuthzAudit(audit), api.WithCheckTrace(missed),
		api.WithCheckCapHit(func(*http.Request) { capHits.Add(1) }))
	// poll also returns its deny refreshes whose SARs were answered.
	poll := func(ctx context.Context, user string) (code int, sars int64, took, wall time.Duration, refreshed int) {
		ps := &pollState{pending: map[any]time.Time{}, denied: map[any]bool{}}
		rctx := context.WithValue(context.WithValue(ctx, userKey{}, user), pollKey{}, ps)
		w := httptest.NewRecorder()
		start, wstart := clk.Now(), time.Now()
		h.ServeHTTP(w, httptest.NewRequestWithContext(rctx, http.MethodGet, p.path, nil))
		ps.mu.Lock()
		defer ps.mu.Unlock()
		return w.Code, ps.sars.Load(), clk.Since(start), time.Since(wstart), ps.refreshed
	}

	// The warm viewer's answers are all cached before the storm; its allows live 60 s from here.
	warmAt := clk.Now()
	if code, _, _, _, _ := poll(context.Background(), warmUser); code != http.StatusOK {
		t.Fatalf("load %s: warm-up %d", p.name, code)
	}
	storm := clk.Now()
	end := storm.Add(window)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add((window+grace)/scale))
	defer cancel()
	codes := map[int]int{}
	var maxPoll int64
	var wg sync.WaitGroup
	for user, v := range load {
		wg.Go(func() {
			for next := clk.Now(); next.Before(end); {
				start := clk.Since(storm)
				v.busy.Store(true)
				code, sars, _, _, refreshed := poll(ctx, user)
				// Late is decided by when the poll ended on the bench clock, not by when the deadline timer fired.
				done := clk.Since(storm)
				late := done > window+grace
				v.busy.Store(false)
				mu.Lock()
				codes[code]++
				maxPoll = max(maxPoll, sars)
				mu.Unlock()
				v.record(start, done, code, refreshed, late)
				if late || ctx.Err() != nil {
					break
				}
				// The next poll starts 5 s after this one started, or now if it took longer.
				next = next.Add(every)
				if now := clk.Now(); now.After(next) {
					next = now
				}
				clk.Sleep(next.Sub(clk.Now()))
			}
		})
	}
	// The warm viewer polls within its allow TTL, noting how many cold viewers are in a poll.
	type warmPoll struct {
		code, cold int
		sars       int64
		took, wall time.Duration
	}
	var warm []warmPoll
	wg.Go(func() {
		for clk.Since(warmAt) < sar.DefaultAllowTTL-10*time.Second {
			n := 0
			for _, v := range load {
				if v.busy.Load() && !v.ok.Load() {
					n++
				}
			}
			code, sars, took, wall, _ := poll(ctx, warmUser)
			warm = append(warm, warmPoll{code, n, sars, took, wall})
			clk.Sleep(every)
		}
	})
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After((window+grace)/scale + 30*time.Second):
		t.Fatalf("load: polls still running 30 s after the %v deadline", window+grace)
	}
	slices.SortFunc(sends, func(a, b time.Time) int { return a.Compare(b) })
	// Peak in any 1 s window, and the most sent above the rate over any interval (the burst used).
	peakSec, j := 0, 0
	for i := range sends {
		for sends[i].Sub(sends[j]) >= time.Second {
			j++
		}
		peakSec = max(peakSec, i-j+1)
	}
	excess, low := 0.0, 0.0
	for i, s := range sends {
		x := float64(i) - float64(sar.DefaultRate)*s.Sub(t0).Seconds()
		if i == 0 || x < low {
			low = x
		}
		excess = max(excess, x-low+1)
	}
	total, cut := 0, 0
	for _, n := range codes {
		total += n
	}
	var firstOK []time.Duration
	worst, worstN, fewest, short := 100.0, 0, -1, 0
	for _, user := range slices.Sorted(maps.Keys(load)) {
		v := load[user]
		cut += v.cut
		if !v.ok.Load() {
			t.Errorf("load %s: %s got no 200 in %v", p.name, user, window)
			continue
		}
		firstOK = append(firstOK, v.first)
		if v.first > bound {
			t.Errorf("load %s: %s first 200 at %v, after %v (%d viewers × %d cold SARs at %d/s, plus 60 s)",
				p.name, user, v.first.Round(time.Second), bound.Round(time.Second), viewers, cold.sars, sar.DefaultRate)
		}
		if fewest < 0 || v.after < fewest {
			fewest = v.after
		}
		if v.after < minScored || v.last-v.first < sar.DenyTTL || v.refreshes == 0 {
			short++
			t.Errorf("load %s: %s scored %d polls (want ≥ %d), the last %v after its first 200 (want ≥ %v), %d deny refreshes",
				p.name, user, v.after, minScored, (v.last - v.first).Round(time.Second), sar.DenyTTL, v.refreshes)
		}
		share := 100 * float64(v.afterOK) / float64(max(v.after, 1))
		if share < worst || share == worst && v.after < worstN {
			worst, worstN = share, v.after
		}
		if share < 95 {
			t.Errorf("load %s: %s answered %d of %d polls 200 after its first (%.1f%%), want ≥ 95%%", p.name, user, v.afterOK, v.after, share)
		}
	}
	got := countOf(reasons)
	if n := got[api.AuditCheckLimit] + got["cut "+api.AuditCheckLimit]; n > 0 || capHits.Load() > 0 {
		t.Errorf("load %s: %d polls answered check-limit, %d passed the check cap", p.name, n, capHits.Load())
	}
	// The full-read timeout is wall time, ten times longer here; a cluster-wide list waiting under the allow TTL hit neither.
	if trace.fullWait >= sar.DefaultAllowTTL {
		t.Errorf("load %s: a cluster-wide list waited %v from its miss to its SAR, past the %v full-read timeout", p.name, trace.fullWait, sar.DefaultAllowTTL)
	}
	var warmTook, warmWall []time.Duration
	for _, w := range warm {
		warmTook, warmWall = append(warmTook, w.took), append(warmWall, w.wall)
		if w.code != http.StatusOK || w.sars > 0 || w.cold <= places || w.took > every {
			t.Errorf("load %s: warm poll → %d with %d SARs in %v while %d cold viewers polled; want 200, no SAR, ≤ %v, more than %d cold",
				p.name, w.code, w.sars, w.took, w.cold, every, places)
		}
	}
	if len(warm) < 5 {
		t.Errorf("load %s: %d warm polls, want ≥ 5", p.name, len(warm))
	}
	if c.peak.Load() > sar.DefaultMaxInFlight {
		t.Errorf("load %s: %d SARs in flight, limit %d", p.name, c.peak.Load(), sar.DefaultMaxInFlight)
	}
	// The SAR start is noted just after its token, so allow a tenth of a second at the rate.
	if slack := float64(sar.DefaultBurst + sar.DefaultRate/10); excess > slack {
		t.Errorf("load %s: %.0f SARs above %d/s over one interval, burst %d", p.name, excess, sar.DefaultRate, sar.DefaultBurst)
	}
	t.Logf("load: %d namespace-grants viewers polling %s every 5 s, starting for %v (clock ×%d, %v per SAR): %d SARs; peak %d in flight (limit %d); peak %d in any 1 s, most above %d/s over any interval %.0f (burst %d)",
		viewers, p.name, window.Round(time.Second), scale, lat, len(sends), c.peak.Load(), sar.DefaultMaxInFlight, peakSec, sar.DefaultRate, excess, sar.DefaultBurst)
	// q is pct, or 0 for no samples.
	q := func(d []time.Duration, n int) time.Duration {
		if len(d) == 0 {
			return 0
		}
		return pct(d, n)
	}
	t.Logf("load: one cold viewer %d SARs (%s); polls %v (%d), %.1f%% answered 200, %d cut by the deadline; 503 reasons %v; cap passed %d; first 200 per viewer p50 %v max %v (bound %v)",
		cold.sars, p.mark(cold.sars), codes, total, 100*float64(codes[http.StatusOK])/float64(max(total, 1)), cut, got, capHits.Load(),
		q(firstOK, 50).Round(time.Second), q(firstOK, 100).Round(time.Second), bound.Round(time.Second))
	t.Logf("load: after the first 200, worst viewer %.1f%% answered 200 of %d scored polls (fewest %d, minimum %d; %d viewers short of polls, time or a deny refresh); most SARs in one poll %d",
		worst, worstN, fewest, minScored, short, maxPoll)
	t.Logf("load: warm viewer %d polls during the storm, p50 %v max %v (wall p50 %v max %v)",
		len(warm), q(warmTook, 50), q(warmTook, 100), q(warmWall, 50), q(warmWall, 100))
	t.Logf("load: counted misses by build %v; before the first 200 %v, after %v; %.0f evicted (metric); longest miss to SAR %v, cluster-wide list %v",
		trace.byBuild, trace.byPhase[0], trace.byPhase[1], metric(reg, "snellius_authz_cache_evictions_total"),
		trace.wait.Round(time.Millisecond), trace.fullWait.Round(time.Millisecond))
}

func countOf(s []string) map[string]int {
	m := map[string]int{}
	for _, x := range s {
		m[x]++
	}
	return m
}

// sarHeapChildEnv makes TestBudgetSARIntegratedHeap measure in this process and print one line.
const sarHeapChildEnv = "SNELLIUS_SAR_HEAP_CHILD"

// sarHeapRun is one child's HeapInuse over the 2.1 baseline after each step.
type sarHeapRun struct {
	Settled, Cache, Sessions int64
	Accounted, Evicted       float64
}

// TestScopedFewerColdChecksLarge: at Large, a viewer allowed 10 of 100
// namespaces asks fewer cold SARs on a list scoped to one of them than unscoped.
func TestScopedFewerColdChecksLarge(t *testing.T) {
	build, m := api.LargeHandlers(t)
	base := "/api/v1/installs/" + m.Installs[0].Resolved.ID()
	cold := func(path string) result {
		c, clk := &fakeChecker{allow: nsGrants}, clocktesting.NewFakeClock(t0)
		h := build(newAuthz(c, clk, nil, lifted), api.WithSecureMode(), api.WithCommitClock(clk.Now))
		return get(h, c, "u", path)
	}
	for _, p := range []string{"/routes", "/gateways", "/listenersets", "/paths", "/backends", "/findings", "/processes"} {
		all, scoped := cold(base+p), cold(base+p+"?namespaces=ns-000")
		t.Logf("%-14s cold SARs unscoped %d → %d, scoped to ns-000 %d → %d", p, all.sars, all.code, scoped.sars, scoped.code)
		if p == "/routes" && (scoped.code != 200 || scoped.sars >= all.sars) {
			t.Errorf("/routes scoped asked %d SARs (%d), unscoped %d", scoped.sars, scoped.code, all.sars)
		}
	}
}

// largePolicies is one TrafficPolicy per Large HTTPRoute, in the route's namespace,
// targeting it and reported on its first parent: a policy per route.
func largePolicies(snap *cache.Snapshot) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, objs := range snap.Objects {
		for _, r := range objs {
			if r.GetKind() != "HTTPRoute" {
				continue
			}
			ps := objects.RouteParentRefs(r)
			if len(ps) == 0 {
				continue
			}
			ok := []metav1.Condition{testutil.Cond("Accepted", metav1.ConditionTrue, "Accepted"), testutil.Cond("Attached", metav1.ConditionTrue, "Attached")}
			p := testutil.TrafficPolicy(r.GetNamespace(), "tp-"+r.GetName(), testutil.PolicyAncestor{Ref: ps[0], Controller: testutil.Controller, Conds: ok})
			p.Object["spec"] = map[string]any{"targetRefs": []any{map[string]any{"group": objects.GatewayAPIGroup, "kind": "HTTPRoute", "name": r.GetName()}}}
			out = append(out, p)
		}
	}
	return out
}

// policyReader may list TrafficPolicies everywhere and everything else in the
// granted namespaces only, with no named get: the most named checks a policy list asks.
func policyReader(a sar.Attributes) bool {
	return a.Verb == "list" && (a.Resource == "trafficpolicies" || a.Namespace != "" && granted(a.Namespace))
}

// A cold Large policies list with one policy per route answers within the
// per-request check budget for every restricted viewer (8.1 re-review: budget).
func TestBudgetSARPoliciesLarge(t *testing.T) {
	build, m := api.TierHandlersWith(t, scale.Large, largePolicies)
	path := "/api/v1/installs/" + m.Installs[0].Resolved.ID() + "/policies?limit=1"
	for _, v := range append(slices.Clone(viewers), viewer{"policy-reader", policyReader}) {
		c, clk := &fakeChecker{allow: v.allow}, clocktesting.NewFakeClock(t0)
		h := build(newAuthz(c, clk, nil, lifted), api.WithSecureMode(), api.WithCommitClock(clk.Now))
		cold := get(h, c, "u", path)
		t.Logf("%-16s /policies?limit=1 cold %d SARs (%d list, %d get) → %d", v.name, cold.sars, c.lists.Load(), c.gets.Load(), cold.code)
		if cold.code != http.StatusOK || cold.sars > api.CheckLimit {
			t.Errorf("%s: cold /policies?limit=1 %d with %d SARs (limit %d)", v.name, cold.code, cold.sars, api.CheckLimit)
		}
	}
}

// secureWith builds secure-mode handlers whose commit clock is the authoriser's,
// so allows dated on a fake clock are judged on it.
func secureWith(build func(api.Authorizer, ...api.HandlerOption) http.Handler) func(api.Authorizer, func() time.Time, ...api.HandlerOption) http.Handler {
	return func(az api.Authorizer, now func() time.Time, opts ...api.HandlerOption) http.Handler {
		return build(az, append([]api.HandlerOption{api.WithSecureMode(), api.WithCommitClock(now)}, opts...)...)
	}
}

// detailPages are the detail paths of the deepest visible route and the most used visible Service.
func detailPages(t *testing.T, ix *explore.Index, base string) (route, svc string) {
	t.Helper()
	deepest, depth := api.DeepestChild(ix, canView)
	var s objects.Ref
	for r, uses := range ix.ByBackend {
		if r.Kind == "Service" && canView(r) && (s.Name == "" || len(uses) > len(ix.ByBackend[s]) ||
			len(uses) == len(ix.ByBackend[s]) && r.String() < s.String()) {
			s = r
		}
	}
	if depth == 0 || s.Name == "" {
		t.Fatalf("no visible deepest child (%s, depth %d) or Service (%s)", deepest, depth, s)
	}
	t.Logf("deepest visible child %s at depth %d; backend detail %s with %d uses", deepest, depth, s, len(ix.ByBackend[s]))
	return base + "/routes/" + deepest.Group + "/" + deepest.Kind + "/" + deepest.Namespace + "/" + deepest.Name,
		base + "/backends/-/Service/" + s.Namespace + "/" + s.Name
}

// TestBudgetSARNobodyInstalls reports /installs for a viewer allowed nothing on
// Large and Extreme: the full read plus the findings-basis buckets, with no
// reachability check (D-103), against the per-request cap. Report-only.
func TestBudgetSARNobodyInstalls(t *testing.T) {
	nobody := func(sar.Attributes) bool { return false }
	for _, tier := range []scale.Tier{scale.Large, scale.Extreme} {
		build, m := api.TierHandlers(t, tier)
		c, clk := &fakeChecker{allow: nobody}, clocktesting.NewFakeClock(t0)
		h := secureWith(build)(newAuthz(c, clk, nil, lifted), clk.Now)
		cold := get(h, c, "nobody", "/api/v1/installs")
		warm := get(h, c, "nobody", "/api/v1/installs")
		verdict := map[bool]string{true: fmt.Sprintf("✅ ≤ %d", api.CheckLimit), false: fmt.Sprintf("❌ > %d", api.CheckLimit)}[cold.sars <= api.CheckLimit]
		t.Logf("nobody %-8s /installs (%d installs, %d findings) cold %d SARs (%d list, %d get) → %d (%s); warm %d SARs → %d",
			tier.Name, len(m.Installs), len(m.Installs[0].Health.Findings), cold.sars, c.lists.Load(), c.gets.Load(), cold.code, verdict, warm.sars, warm.code)
	}
}

// TestBudgetProjectionCost reports one warm request's time and allocation in
// secure mode on large-paths, a namespace-grants viewer (projected per request)
// against a full reader (memoised). The SAR cache is warm on a frozen clock, so
// the difference is the per-viewer projection and row state. Report-only.
func TestBudgetProjectionCost(t *testing.T) {
	build, m := api.TierHandlers(t, scale.Large.Paths())
	base := "/api/v1/installs/" + m.Installs[0].Resolved.ID()
	route, svc := detailPages(t, m.Installs[0].Index, base)
	pages := []page{
		{name: "/paths", path: base + "/paths"},
		{name: "/backends", path: base + "/backends"},
		{name: "backend detail", path: svc},
		{name: "route detail", path: route},
		{name: "/routes", path: base + "/routes"},
		{name: "/meta", path: "/api/v1/meta"},
	}
	for _, v := range []viewer{{"namespace-grants", nsGrants}, {"full-read", fullRead}} {
		c, clk := &fakeChecker{allow: v.allow}, clocktesting.NewFakeClock(t0)
		h := secureWith(build)(newAuthz(c, clk, nil, lifted), clk.Now)
		for _, p := range pages {
			if r := get(h, c, "u", p.path); r.code != http.StatusOK {
				t.Fatalf("%s %s: %d", v.name, p.name, r.code)
			}
			req := request("u", p.path)
			n := c.total()
			res := testing.Benchmark(func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					h.ServeHTTP(httptest.NewRecorder(), req)
				}
			})
			t.Logf("projection %-16s %-15s warm %v, %.2f MB, %d allocs per request (%d runs, %d SARs)",
				v.name, p.name, time.Duration(res.NsPerOp()).Round(time.Microsecond), float64(res.AllocedBytesPerOp())/1e6, res.AllocsPerOp(), res.N, c.total()-n)
		}
	}
}

// TestBudgetSARIntegratedHeap reports D-057's retained heap with the permission
// cache at its budget and session.DefaultMax sessions on the worst gated case. Report-only.
func TestBudgetSARIntegratedHeap(t *testing.T) {
	if os.Getenv(sarHeapChildEnv) != "" {
		sarHeapChild(t)
		return
	}
	const mib = 1 << 20
	var runs []sarHeapRun
	for range 3 {
		cmd := exec.Command(os.Args[0], "-test.run=^TestBudgetSARIntegratedHeap$", "-test.count=1", "-test.timeout=0")
		cmd.Env = append(os.Environ(), sarHeapChildEnv+"=1")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("heap child: %v\n%s", err, out)
		}
		var r sarHeapRun
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		for sc.Scan() {
			if j, ok := strings.CutPrefix(sc.Text(), "sarheap: "); ok {
				if err := json.Unmarshal([]byte(j), &r); err != nil {
					t.Fatal(err)
				}
			}
		}
		if r.Settled == 0 {
			t.Fatalf("heap child printed no result:\n%s", out)
		}
		t.Logf("run: settled %+.1f MiB, cache at budget %+.1f MiB (accounted %.2f MiB, %.0f evicted), %d sessions %+.1f MiB, total %+.1f MiB",
			float64(r.Settled)/mib, float64(r.Cache-r.Settled)/mib, r.Accounted/mib, r.Evicted, session.DefaultMax, float64(r.Sessions-r.Cache)/mib, float64(r.Sessions)/mib)
		runs = append(runs, r)
	}
	med := func(f func(sarHeapRun) int64) float64 {
		v := make([]int64, len(runs))
		for i, r := range runs {
			v[i] = f(r)
		}
		slices.Sort(v)
		return float64(v[len(v)/2]) / mib
	}
	total := med(func(r sarHeapRun) int64 { return r.Sessions })
	verdict := map[bool]string{true: "✅ ≤ 64 MiB", false: "❌ > 64 MiB"}[total <= 64]
	t.Logf("large-contradicting-worst + permission cache at budget + %d sessions: median %+.1f MiB over the 2.1 baseline (%s, report-only); settled %+.1f, cache %+.1f, sessions %+.1f",
		session.DefaultMax, total, verdict, med(func(r sarHeapRun) int64 { return r.Settled }), med(func(r sarHeapRun) int64 { return r.Cache - r.Settled }),
		med(func(r sarHeapRun) int64 { return r.Sessions - r.Cache }))
}

// sarHeapChild measures one sarHeapRun and prints it.
func sarHeapChild(t *testing.T) {
	inuse := func() int64 {
		runtime.GC()
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return int64(ms.HeapInuse) - api.HeapBaseline //nolint:gosec // heap sizes fit
	}
	keep := api.SettledWorstHeap(t)
	var r sarHeapRun
	r.Settled = inuse()

	// Namespace-grants viewers: a full read, the list of four kinds in each namespace, and 40 named gets.
	reg := obs.NewRegistry()
	az := newAuthz(&fakeChecker{allow: nsGrants}, clocktesting.NewFakeClock(t0), reg, lifted)
	kinds := []objects.GroupKind{{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute"}, {Group: "gateway.networking.k8s.io", Kind: "Gateway"},
		{Group: "", Kind: "Service"}, {Group: "gateway.networking.k8s.io", Kind: "GRPCRoute"}}
	for u, evictedAt := 0, 0; evictedAt == 0 || u < 2*evictedAt; u++ {
		req := request(fmt.Sprintf("user-%04d@example.com", u), "/")
		if _, err := az.FullRead(req); err != nil {
			t.Fatal(err)
		}
		for ns := range 100 {
			for _, gk := range kinds {
				if _, err := az.CanList(req, gk, fmt.Sprintf("ns-%03d", ns)); err != nil {
					t.Fatal(err)
				}
			}
		}
		for i := range 40 {
			ref := objects.Ref{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: fmt.Sprintf("ns-%03d", i%10*10), Name: fmt.Sprintf("r-%05d", i)}
			if _, err := az.AuthorizeNamedErr(req, ref); err != nil {
				t.Fatal(err)
			}
		}
		if evictedAt == 0 && metric(reg, "snellius_authz_cache_evictions_total") > 0 {
			evictedAt = u + 1
		}
	}
	r.Cache = inuse()
	r.Accounted, r.Evicted = metric(reg, "snellius_authz_cache_bytes"), metric(reg, "snellius_authz_cache_evictions_total")

	// session.DefaultMax sessions with tokens of typical size and ten groups.
	store := session.New(session.Options{})
	groups := make([]string, 10)
	for i := range groups {
		groups[i] = fmt.Sprintf("oidc:team-%02d-platform-viewers", i)
	}
	for i := range session.DefaultMax {
		store.Complete(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/auth/callback", nil), oidc.Result{
			Identity: oidc.Identity{Username: fmt.Sprintf("oidc:user-%04d@example.com", i), UID: fmt.Sprintf("%036d", i), Groups: slices.Clone(groups)},
			Issuer:   "https://issuer.example.com/realms/platform", Subject: fmt.Sprintf("%036d", i),
			IDToken: strings.Repeat("i", 1200), AccessToken: strings.Repeat("a", 1400), RefreshToken: strings.Repeat("r", 800),
			Expiry: time.Now().Add(time.Hour), ReturnPath: "/",
		})
	}
	r.Sessions = inuse()
	runtime.KeepAlive(keep)
	runtime.KeepAlive(az)
	runtime.KeepAlive(store)
	j, _ := json.Marshal(r)
	fmt.Println("sarheap: " + string(j))
}
