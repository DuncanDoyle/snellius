package limits

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

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

var t0 = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

// fixture is a Recorder on its own registry, JSON log and settable clock.
type fixture struct {
	reg *obs.Registry
	log bytes.Buffer
	now time.Time
	rec *Recorder
}

func newFixture() *fixture {
	f := &fixture{reg: obs.NewRegistry(), now: t0}
	f.rec = New(f.reg, slog.New(slog.NewJSONHandler(&f.log, nil)), func() time.Time { return f.now })
	return f
}

// exposition is the registry's /metrics body.
func (f *fixture) exposition() string {
	rec := httptest.NewRecorder()
	f.reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

// lines are the log records as attribute maps.
func (f *fixture) lines(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(f.log.String()), "\n") {
		if l == "" {
			continue
		}
		m := map[string]any{}
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

// Every hit counts; every limit's series exists from the start, at zero.
func TestHitCountsEveryHit(t *testing.T) {
	f := newFixture()
	for _, l := range All {
		if !strings.Contains(f.exposition(), `snellius_limit_hits_total{limit="`+string(l)+`"} 0`) {
			t.Fatalf("no zero series for %s:\n%s", l, f.exposition())
		}
	}
	for range 3 {
		f.rec.Hit(DelegationEdges, "infra.kgw", "")
	}
	f.rec.Hit(AuthzChecks, "infra.kgw", "a1")
	exp := f.exposition()
	for _, want := range []string{`{limit="delegation-edges"} 3`, `{limit="authz-checks"} 1`, `{limit="delegation-contexts"} 0`} {
		if !strings.Contains(exp, "snellius_limit_hits_total"+want+"\n") {
			t.Errorf("exposition lacks %s:\n%s", want, exp)
		}
	}
}

// One line per limit and install per window; the next one carries the suppressed count.
func TestLogOncePerWindow(t *testing.T) {
	f := newFixture()
	for _, d := range []time.Duration{0, 10 * time.Second, Window - time.Nanosecond} {
		f.now = t0.Add(d)
		f.rec.Hit(DelegationContexts, "infra.kgw", "")
	}
	f.rec.Hit(DelegationContexts, "apps.other", "")
	f.rec.Hit(DelegationEdges, "infra.kgw", "")
	if n := len(f.lines(t)); n != 3 {
		t.Fatalf("%d lines in the first window, want 3:\n%s", n, f.log.String())
	}
	f.now = t0.Add(Window)
	f.rec.Hit(DelegationContexts, "infra.kgw", "")
	ls := f.lines(t)
	if len(ls) != 4 || ls[3]["limit"] != "delegation-contexts" || ls[3]["install"] != "infra.kgw" || ls[3]["suppressed"] != float64(2) {
		t.Fatalf("after the window: %v", ls)
	}
	if ls[0]["suppressed"] != float64(0) || ls[0]["level"] != "WARN" {
		t.Fatalf("first line: %v", ls[0])
	}
	if !strings.Contains(f.exposition(), `snellius_limit_hits_total{limit="delegation-contexts"} 5`) {
		t.Fatalf("suppressed hits must still count:\n%s", f.exposition())
	}
}

// A line carries the limit, the install, the session's audit ID for a request
// limit and the suppressed count: no object, namespace or hidden count.
func TestLogCarriesNoObject(t *testing.T) {
	f := newFixture()
	f.rec.Hit(AuthzChecks, "infra.kgw", "5f3a")
	f.rec.Hit(DelegationRetained, "infra.kgw", "")
	ls := f.lines(t)
	want := [][]string{
		{"install", "level", "limit", "msg", "session", "suppressed", "time"},
		{"install", "level", "limit", "msg", "suppressed", "time"},
	}
	for i, l := range ls {
		if keys := slices.Sorted(maps.Keys(l)); !slices.Equal(keys, want[i]) {
			t.Errorf("line %d keys %v, want %v", i, keys, want[i])
		}
	}
	if ls[0]["session"] != "5f3a" || ls[0]["msg"] != Message {
		t.Fatalf("request line: %v", ls[0])
	}
}

// An unknown limit adds no series and no line; a nil Recorder does nothing.
func TestFixedLabels(t *testing.T) {
	f := newFixture()
	f.rec.Hit(Limit("shop/web"), "infra.kgw", "")
	if strings.Contains(f.exposition(), "shop/web") || f.log.Len() != 0 {
		t.Fatalf("unknown limit recorded:\n%s\n%s", f.exposition(), f.log.String())
	}
	var nilRec *Recorder
	nilRec.Hit(AuthzChecks, "", "")
}

// Windows that ended without a suppressed hit are forgotten.
func TestQuietWindowsForgotten(t *testing.T) {
	f := newFixture()
	f.rec.Hit(DelegationEdges, "a", "")
	f.rec.Hit(DelegationEdges, "b", "")
	f.rec.Hit(DelegationEdges, "b", "")
	f.now = t0.Add(Window)
	f.rec.Hit(DelegationEdges, "c", "")
	f.rec.mu.Lock()
	defer f.rec.mu.Unlock()
	if _, ok := f.rec.windows[key{DelegationEdges, "a"}]; ok || len(f.rec.windows) != 2 {
		t.Fatalf("windows %v, want b (suppressed) and c", f.rec.windows)
	}
}
