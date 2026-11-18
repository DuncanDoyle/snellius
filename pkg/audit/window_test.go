package audit

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// running starts l.Run on clk and waits until both window tickers exist; stop ends it.
func running(t *testing.T, l *Logger, clk *clocktesting.FakeClock) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	waitFor(t, "the window tickers", func() bool { return clk.Waiters() >= 2 })
	return func() { cancel(); <-done }
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(time.Millisecond)
	}
}

// lines decodes the JSON lines in s.
func lines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("line %q: %v", l, err)
		}
		out = append(out, m)
	}
	return out
}

func summary(route string) Record {
	return Record{Level: LevelSummaries, Action: "list", Session: "s1", Request: Request{ID: "req-1", Method: "GET", Route: route}}
}

// Summaries collapse per key with a count and flush on the window's tick;
// the next window starts afresh.
func TestSummaryWindowFlushes(t *testing.T) {
	var buf syncBuf
	clk := clocktesting.NewFakeClock(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC))
	l := New(&buf, levels(t, ""), nil, WithClock(clk), WithServer("pod-a"))
	stop := running(t, l, clk)
	defer stop()
	for range 3 {
		l.Summarize("a", summary("GET /a"))
	}
	l.Summarize("b", summary("GET /b"))
	clk.Step(SummaryWindow - time.Second)
	if buf.String() != "" {
		t.Fatalf("flushed before the window ended: %s", buf.String())
	}
	clk.Step(time.Second)
	waitFor(t, "the summary flush", func() bool { return strings.Count(buf.String(), "\n") == 2 })
	got := lines(t, buf.String())
	if got[0]["count"] != 3.0 || got[1]["count"] != 1.0 || got[0]["level"] != LevelSummaries || got[0]["server"] != "pod-a" ||
		got[0]["time"] != "2026-11-18T10:00:00Z" || got[0]["id"] == nil {
		t.Fatalf("summaries %v", got)
	}
	if req := got[0]["request"].(map[string]any); req["id"] != nil || req["route"] != "GET /a" {
		t.Fatalf("summary request %v: want the route and no request ID", req)
	}
	l.Summarize("a", summary("GET /a"))
	clk.Step(SummaryWindow)
	waitFor(t, "the second flush", func() bool { return strings.Count(buf.String(), "\n") == 3 })
	if got := lines(t, buf.String()); got[2]["count"] != 1.0 {
		t.Fatalf("second window counts %v, want 1", got[2]["count"])
	}
}

// stallWriter blocks its first write until release is closed.
type stallWriter struct {
	syncBuf
	once             sync.Once
	entered, release chan struct{}
}

func (s *stallWriter) Write(p []byte) (int, error) {
	s.once.Do(func() { close(s.entered); <-s.release })
	return s.syncBuf.Write(p)
}

// A tick closes the window even while a write is stalled: later requests
// start the next window instead of joining the closed one.
func TestWindowRollsOverDuringStalledWrite(t *testing.T) {
	w := &stallWriter{entered: make(chan struct{}), release: make(chan struct{})}
	clk := clocktesting.NewFakeClock(time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC))
	l := New(w, levels(t, ""), nil, WithClock(clk))
	stop := running(t, l, clk)
	l.Emit(context.Background(), Record{Level: LevelSecurity, Action: "startup"})
	<-w.entered
	l.Summarize("a", summary("GET /a"))
	clk.Step(SummaryWindow)
	waitFor(t, "the summary window to close while the write is stalled", func() bool {
		l.summaries.mu.Lock()
		defer l.summaries.mu.Unlock()
		return len(l.summaries.open) == 0
	})
	l.Summarize("a", summary("GET /a"))
	close(w.release)
	stop()
	var counts []any
	for _, r := range lines(t, w.String()) {
		if r["action"] == "list" {
			counts = append(counts, r["count"])
		}
	}
	if len(counts) != 2 || counts[0] != 1.0 || counts[1] != 1.0 {
		t.Fatalf("summary counts %v, want one per window", counts)
	}
}

// Stopping the writer flushes the open summaries and denials.
func TestWindowsFlushOnShutdown(t *testing.T) {
	var buf syncBuf
	l := New(&buf, levels(t, ""), nil)
	drain(t, l, func() {
		l.Summarize("a", summary("GET /a"))
		l.Summarize("a", summary("GET /a"))
		l.Collapse(context.Background(), "d", Record{Level: LevelSecurity, Action: "denial"})
	})
	got := lines(t, buf.String())
	if len(got) != 2 {
		t.Fatalf("want a denial and a summary, got %v", got)
	}
	for _, r := range got {
		if r["action"] == "list" && r["count"] != 2.0 || r["action"] == "denial" && r["count"] != 1.0 {
			t.Fatalf("record %v", r)
		}
	}
}

// Past the open-key cap the oldest key is flushed at once, with its count.
func TestWindowKeyCap(t *testing.T) {
	var buf syncBuf
	clk := clocktesting.NewFakeClock(time.Now())
	l := New(&buf, levels(t, ""), nil, WithClock(clk), WithMaxOpenKeys(2))
	stop := running(t, l, clk)
	l.Summarize("a", summary("GET /a"))
	l.Summarize("a", summary("GET /a"))
	l.Summarize("b", summary("GET /b"))
	l.Summarize("c", summary("GET /c"))
	waitFor(t, "the oldest key's early flush", func() bool { return strings.Count(buf.String(), "\n") == 1 })
	if got := lines(t, buf.String()); got[0]["request"].(map[string]any)["route"] != "GET /a" || got[0]["count"] != 2.0 {
		t.Fatalf("early flush %v, want GET /a with count 2", got)
	}
	stop()
	got := lines(t, buf.String())
	if len(got) != 3 || got[1]["request"].(map[string]any)["route"] != "GET /b" || got[2]["request"].(map[string]any)["route"] != "GET /c" {
		t.Fatalf("records %v, want a, then b and c at shutdown", got)
	}
	if MaxOpenKeys != 10000 {
		t.Fatalf("MaxOpenKeys = %d, want 10000 (design §9.4)", MaxOpenKeys)
	}
}

// Repeated denials of one request within the window are one record with a
// count; another request, or the next window, is another record.
func TestDenialsCollapse(t *testing.T) {
	var buf syncBuf
	clk := clocktesting.NewFakeClock(time.Now())
	l := New(&buf, levels(t, ""), nil, WithClock(clk))
	stop := running(t, l, clk)
	defer stop()
	deny := Record{Level: LevelSecurity, Action: "denial", Refs: []string{"g/K/ns/x"}}
	for range 4 {
		l.Collapse(context.Background(), "x", deny)
	}
	l.Collapse(context.Background(), "y", deny)
	clk.Step(DenialWindow)
	waitFor(t, "the denial flush", func() bool { return strings.Count(buf.String(), "\n") == 2 })
	got := lines(t, buf.String())
	if got[0]["count"] != 4.0 || got[1]["count"] != 1.0 {
		t.Fatalf("denials %v, want counts 4 and 1", got)
	}
	l.Collapse(context.Background(), "x", deny)
	clk.Step(DenialWindow)
	waitFor(t, "the next window", func() bool { return strings.Count(buf.String(), "\n") == 3 })
}

// A disabled level is never held; a window record after the writer stopped is counted as dropped.
func TestWindowLevelsAndDrops(t *testing.T) {
	var buf syncBuf
	l := New(&buf, levels(t, "security"), nil)
	drain(t, l, func() { l.Summarize("a", summary("GET /a")) })
	if buf.String() != "" {
		t.Fatalf("summaries off, yet %s", buf.String())
	}
	reg := obs.NewRegistry()
	l2 := New(&buf, levels(t, ""), reg)
	drain(t, l2, func() {})
	l2.Summarize("a", summary("GET /a"))
	l2.Collapse(context.Background(), "d", Record{Level: LevelSecurity, Action: "denial"})
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, want := range []string{`snellius_audit_dropped_total{level="summaries"} 1`, `snellius_audit_dropped_total{level="security"} 1`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics lack %s:\n%s", want, rec.Body.String())
		}
	}
	if buf.String() != "" {
		t.Fatalf("wrote after stop: %s", buf.String())
	}
}

// Written records are counted per level on /metrics, collapsed ones once each.
func TestWrittenCountedPerLevel(t *testing.T) {
	var buf syncBuf
	reg := obs.NewRegistry()
	l := New(&buf, levels(t, "security,reads,summaries,disclosure,requests"), reg)
	drain(t, l, func() {
		for _, lv := range []string{LevelReads, LevelDisclosure, LevelRequests} {
			l.Emit(context.Background(), Record{Level: lv, Action: "x"})
		}
		l.Summarize("a", summary("GET /a"))
		l.Summarize("a", summary("GET /a"))
	})
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	for _, lv := range []string{LevelReads, LevelDisclosure, LevelRequests, LevelSummaries} {
		if want := `snellius_audit_records_total{level="` + lv + `"} 1`; !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics lack %s:\n%s", want, rec.Body.String())
		}
	}
}

// The agent level is a known level that nothing emits yet.
func TestAgentLevelDefined(t *testing.T) {
	ls, err := ParseLevels("agent")
	if err != nil || !ls[LevelAgent] {
		t.Fatalf("agent level: %v %v", ls, err)
	}
	def := levels(t, "")
	for _, lv := range []string{LevelSecurity, LevelAgent, LevelReads, LevelSummaries} {
		if !def[lv] {
			t.Errorf("default levels lack %s", lv)
		}
	}
	if def[LevelDisclosure] || def[LevelRequests] {
		t.Errorf("default levels %v hold an opt-in level", def)
	}
}
