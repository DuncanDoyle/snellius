package engine

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/computed"
	"github.com/DuncanDoyle/snellius/pkg/limits"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// limitCounts reads snellius_limit_hits_total per limit from reg.
func limitCounts(t *testing.T, reg *obs.Registry) map[limits.Limit]string {
	t.Helper()
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := map[limits.Limit]string{}
	for _, l := range strings.Split(rec.Body.String(), "\n") {
		if rest, ok := strings.CutPrefix(l, `snellius_limit_hits_total{limit="`); ok {
			name, v, _ := strings.Cut(rest, `"} `)
			out[limits.Limit(name)] = v
		}
	}
	return out
}

// Each compute bound counts once per delegation computation that hits it, not
// per model build, and logs once per window with the install and no route.
func TestComputeBoundsCountedAndLogged(t *testing.T) {
	capped := dworld{routes: append(dbase(testutil.DelegateWildcard("apps")), dleaf("apps", "o", "/other", nil))}
	mid := testutil.WithRules(testutil.HTTPRoute("shop", "mid", nil, under("c2")), drule([]string{"/api/c2"}, testutil.DelegateTo("shop", "leaf")))
	deep := dworld{routes: append(dbase(), mid, dleaf("shop", "leaf", "/api/c2", nil, under("mid")))}
	deep.routes[1] = testutil.WithRules(testutil.HTTPRoute("shop", "c2", nil, under("api")), drule([]string{"/api/c2"}, testutil.DelegateTo("shop", "mid")))
	clear := dworld{routes: []*unstructured.Unstructured{droot("api", "/api", map[string]any{"name": "svc", "port": int64(80)})}}
	cases := []struct {
		limit limits.Limit
		world dworld
		set   func(*computed.Bounds)
	}{
		{limits.DelegationContexts, capped, func(b *computed.Bounds) { b.Contexts = 1 }},
		{limits.DelegationEdges, capped, func(b *computed.Bounds) { b.Edges = 1 }},
		{limits.DelegationMatchers, capped, func(b *computed.Bounds) { b.Matchers = 1 }},
		{limits.DelegationDepth, deep, func(b *computed.Bounds) { b.Depth = 2 }},
		{limits.DelegationRetained, capped, func(b *computed.Bounds) { b.ContextsPerInstall = 1 }},
	}
	for _, c := range cases {
		t.Run(string(c.limit), func(t *testing.T) {
			reg := obs.NewRegistry()
			var log bytes.Buffer
			s := newDStepper(t, func(cfg *Config) {
				cfg.DelegationBounds = computed.DefaultBounds
				c.set(&cfg.DelegationBounds)
				cfg.Logger = slog.New(slog.NewJSONHandler(&log, nil))
				cfg.Limits = limits.New(reg, cfg.Logger, cfg.Now)
			})
			// Hits at 0 and 3s; 1s reuses the computation, so it is no hit.
			steps := []dworld{c.world, c.world, clear, c.world}
			for i, w := range steps {
				w.step(s, time.Duration(i)*time.Second)
			}
			if got := limitCounts(t, reg); got[c.limit] != "2" {
				t.Fatalf("counts %v, want %s at 2", got, c.limit)
			}
			clear.step(s, limits.Window)
			m := c.world.step(s, limits.Window+time.Second)
			want := map[limits.Limit]string{}
			for _, l := range limits.All {
				want[l] = "0"
			}
			want[c.limit] = "3"
			if got := limitCounts(t, reg); !maps.Equal(got, want) {
				t.Fatalf("counts %v, want %v", got, want)
			}
			var lines []map[string]any
			for _, l := range strings.Split(strings.TrimSpace(log.String()), "\n") {
				rec := map[string]any{}
				if err := json.Unmarshal([]byte(l), &rec); err != nil {
					t.Fatal(err)
				}
				if rec["msg"] == limits.Message {
					lines = append(lines, rec)
				}
			}
			id := m.Installs[0].Resolved.ID()
			if len(lines) != 2 || lines[0]["suppressed"] != float64(0) || lines[1]["suppressed"] != float64(1) {
				t.Fatalf("want two lines, the second with one suppressed hit:\n%s", log.String())
			}
			for _, l := range lines {
				keys := slices.Sorted(maps.Keys(l))
				if !slices.Equal(keys, []string{"install", "level", "limit", "msg", "suppressed", "time"}) || l["install"] != id || l["limit"] != string(c.limit) {
					t.Fatalf("line %v", l)
				}
			}
			for _, l := range lines {
				b, _ := json.Marshal(l)
				for _, name := range []string{"shop", "apps", "c2", "leaf"} {
					if strings.Contains(string(b), name) {
						t.Fatalf("limit line names %q: %s", name, b)
					}
				}
			}
		})
	}
}
