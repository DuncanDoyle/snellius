package obs

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestMetricsExposition(t *testing.T) {
	r := NewRegistry()
	g := r.Gauge("b_gauge", "A gauge.", "mode")
	c := r.Counter("a_total", "A counter.", "path", "code")
	g.Set(1, "local")
	c.Inc("/x", "200")
	c.Add(2, "/x", "200")
	c.Inc("/a\\b\"c\nd", "500")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	want := "# HELP a_total A counter.\n# TYPE a_total counter\n" +
		"a_total{path=\"/a\\\\b\\\"c\\nd\",code=\"500\"} 1\n" +
		"a_total{path=\"/x\",code=\"200\"} 3\n" +
		"# HELP b_gauge A gauge.\n# TYPE b_gauge gauge\n" +
		"b_gauge{mode=\"local\"} 1\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("exposition:\n%s\nwant:\n%s", got, want)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type %q", ct)
	}
}

// blockedWriter signals its first Write and then blocks until release closes.
type blockedWriter struct {
	*httptest.ResponseRecorder
	entered, release chan struct{}
}

func (w blockedWriter) Write(p []byte) (int, error) {
	close(w.entered)
	<-w.release
	return len(p), nil
}

func (w blockedWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }

// A scrape stalled on its client does not block counter updates.
func TestStalledScrapeDoesNotBlockInc(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("a_total", "A counter.")
	w := blockedWriter{httptest.NewRecorder(), make(chan struct{}), make(chan struct{})}
	defer close(w.release)
	go r.ServeHTTP(w, httptest.NewRequest("GET", "/metrics", nil))
	select {
	case <-w.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the scrape never wrote")
	}
	done := make(chan struct{})
	go func() { c.Inc(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Inc blocked behind a stalled scrape")
	}
}
