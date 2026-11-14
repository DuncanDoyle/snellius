package obs

import (
	"net/http/httptest"
	"testing"
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
