//go:build bench

package api_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
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

	"k8s.io/utils/clock"
	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/auth/session"
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
	// sent, when set, is called as each SAR starts.
	sent func()
}

func (c *fakeChecker) Check(_ context.Context, _ oidc.Identity, a sar.Attributes) (bool, error) {
	n := c.inFlight.Add(1)
	defer c.inFlight.Add(-1)
	for p := c.peak.Load(); n > p && !c.peak.CompareAndSwap(p, n); p = c.peak.Load() {
	}
	if c.sent != nil {
		c.sent()
	}
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
// Large against D-100 item 7. Report-only: it logs and never fails on a number.
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
		{"/routes", base + "/routes", 400},
		{"/gateways", base + "/gateways", 400},
		{"/listenersets", base + "/listenersets", 400},
		{"/backends", base + "/backends", 550},
		{"/paths", base + "/paths", 400},
		{"route detail", base + "/routes/" + deepest.Group + "/" + deepest.Kind + "/" + deepest.Namespace + "/" + deepest.Name, 0},
		{"backend detail", base + "/backends/-/Service/" + svc.Namespace + "/" + svc.Name, 0},
	}
	// The commit clock is the authoriser's, so allows dated on a fake clock are judged on it.
	secure := func(az api.Authorizer, now func() time.Time, opts ...api.HandlerOption) http.Handler {
		return build(az, append([]api.HandlerOption{api.WithSecureMode(true), api.WithCommitClock(now)}, opts...)...)
	}
	fake := func(v viewer, opts ...func(*sar.Options)) (*fakeChecker, *clocktesting.FakeClock, http.Handler) {
		c, clk := &fakeChecker{allow: v.allow}, clocktesting.NewFakeClock(t0)
		return c, clk, secure(newAuthz(c, clk, nil, append([]func(*sar.Options){lifted}, opts...)...), clk.Now)
	}

	// Buffered response per page: the full-read viewer sees the most.
	fc, _, fh := fake(viewer{"full-read", fullRead})
	for _, p := range pages {
		r := get(fh, fc, "operator", p.path)
		msg := fmt.Sprintf("full-read %-15s %d, %d B buffered, %d SARs", p.name, r.code, r.body, r.sars)
		if p.name[0] == '/' {
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

	t.Run("load", func(t *testing.T) { loadTest(t, secure, pages[0]) })

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

// loadTest runs 50 namespace-grants viewers polling p every 5 s at once with
// the D-100 limits, on a clock 10× wall time, and reports the global SAR rate.
func loadTest(t *testing.T, secure func(api.Authorizer, func() time.Time, ...api.HandlerOption) http.Handler, p page) {
	const viewers, scale, lat = 50, 10, 2 * time.Millisecond
	const run = 6 * time.Minute
	clk := fastClock{start: time.Now(), scale: scale}
	var mu sync.Mutex
	var sends []time.Time
	var reasons []string
	c := &fakeChecker{lat: lat / scale, allow: nsGrants, sent: func() {
		now := clk.Now()
		mu.Lock()
		sends = append(sends, now)
		mu.Unlock()
	}}
	audit := func(_ *http.Request, reason string) { mu.Lock(); reasons = append(reasons, reason); mu.Unlock() }
	h := secure(newAuthz(c, clk, nil), clk.Now, api.WithAuthzAudit(audit))
	codes := map[int]int{}
	var firstOK []time.Duration
	var wg sync.WaitGroup
	end := clk.Now().Add(run)
	for u := range viewers {
		wg.Go(func() {
			var first time.Duration
			for next := clk.Now(); next.Before(end); {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, request(fmt.Sprintf("viewer-%02d", u), p.path))
				mu.Lock()
				codes[w.Code]++
				mu.Unlock()
				if first == 0 && w.Code == http.StatusOK {
					first = clk.Since(t0)
				}
				// The next poll starts 5 s after this one started, or now if it took longer.
				next = next.Add(5 * time.Second)
				if now := clk.Now(); now.After(next) {
					next = now
				}
				clk.Sleep(next.Sub(clk.Now()))
			}
			mu.Lock()
			firstOK = append(firstOK, first)
			mu.Unlock()
		})
	}
	wg.Wait()
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
	total := 0
	for _, n := range codes {
		total += n
	}
	t.Logf("load: %d namespace-grants viewers polling %s every 5 s for %v (clock ×%d, %v per SAR): %d SARs; peak %d in flight (limit %d); peak %d in any 1 s, most above %d/s over any interval %.0f (burst %d)",
		viewers, p.name, run, scale, lat, len(sends), c.peak.Load(), sar.DefaultMaxInFlight, peakSec, sar.DefaultRate, excess, sar.DefaultBurst)
	t.Logf("load: polls %v (%d), %.1f%% answered 200; 503 reasons %v; first 200 per viewer p50 %v max %v",
		codes, total, 100*float64(codes[http.StatusOK])/float64(total), countOf(reasons), pct(firstOK, 50).Round(time.Second), pct(firstOK, 100).Round(time.Second))
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

// TestBudgetSARIntegratedHeap reports D-057's retained heap with the permission
// cache at its budget and 5,000 sessions on the worst gated case. Report-only.
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
		t.Logf("run: settled %+.1f MiB, cache at budget %+.1f MiB (accounted %.2f MiB, %.0f evicted), 5,000 sessions %+.1f MiB, total %+.1f MiB",
			float64(r.Settled)/mib, float64(r.Cache-r.Settled)/mib, r.Accounted/mib, r.Evicted, float64(r.Sessions-r.Cache)/mib, float64(r.Sessions)/mib)
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
	t.Logf("large-contradicting-worst + permission cache at budget + 5,000 sessions: median %+.1f MiB over the 2.1 baseline (%s, report-only); settled %+.1f, cache %+.1f, sessions %+.1f",
		total, verdict, med(func(r sarHeapRun) int64 { return r.Settled }), med(func(r sarHeapRun) int64 { return r.Cache - r.Settled }),
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

	// 5,000 sessions with tokens of typical size and ten groups.
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
