package audit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

// drain runs the writer, emits via f, and returns the output after the flush.
func drain(t *testing.T, l *Logger, f func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { l.Run(ctx); close(done) }()
	f()
	cancel()
	<-done
}

func levels(t *testing.T, s string) Levels {
	t.Helper()
	ls, err := ParseLevels(s)
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestRecordJSONSchema(t *testing.T) {
	var buf syncBuf
	l := New(&buf, levels(t, ""), nil)
	drain(t, l, func() {
		l.Emit(context.Background(), Record{
			Time: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), ID: "abc", Level: LevelSecurity,
			Action: "startup", Outcome: "success", Mode: "local", Listen: "127.0.0.1:8080",
		})
	})
	want := `{"log":"snellius.audit","schema":"1","id":"abc","time":"2026-11-14T10:00:00Z","level":"security","action":"startup","outcome":"success","mode":"local","listen":"127.0.0.1:8080"}` + "\n"
	if got := buf.String(); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestDisabledLevelEmitsNothing(t *testing.T) {
	var buf syncBuf
	reg := obs.NewRegistry()
	l := New(&buf, levels(t, ""), reg)
	drain(t, l, func() { l.Emit(context.Background(), Record{Level: LevelRequests, Action: "x"}) })
	if buf.String() != "" {
		t.Fatalf("wrote %q", buf.String())
	}
	var m bytes.Buffer
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	m.Write(rec.Body.Bytes())
	if strings.Contains(m.String(), `level="requests"`) {
		t.Fatalf("counted a disabled level:\n%s", m.String())
	}
}

func TestDropCountsWhenFull(t *testing.T) {
	var buf syncBuf
	reg := obs.NewRegistry()
	l := New(&buf, levels(t, ""), reg, WithBuffer(1))
	// No writer is running: the second and third records find the queue full.
	for range 3 {
		l.Emit(context.Background(), Record{Level: LevelSecurity, Action: "x"})
	}
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `snellius_audit_dropped_total{level="security"} 2`) {
		t.Fatalf("dropped not counted:\n%s", rec.Body.String())
	}
}

func TestRequestIDGenerated(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = RequestIDFrom(r.Context()) }))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Request-Id", "caller-chosen")
	h.ServeHTTP(rec, req)
	got := rec.Header().Get("X-Request-Id")
	if got == "" || got == "caller-chosen" || got != seen {
		t.Fatalf("header %q, context %q", got, seen)
	}
}

func TestRecordNeverCarriesSecrets(t *testing.T) {
	var buf syncBuf
	l := New(&buf, levels(t, ""), nil)
	req := httptest.NewRequest("GET", "/api/x?token=querysecret", nil)
	req.Header.Set("Cookie", "snellius_session=cookiesecret")
	req.Header.Set("Authorization", "Bearer authsecret")
	drain(t, l, func() {
		rec := FromRequest(req)
		rec.Level, rec.Action = LevelSecurity, "denial"
		l.Emit(req.Context(), rec)
	})
	out := buf.String()
	if out == "" {
		t.Fatal("nothing written")
	}
	for _, s := range []string{"cookiesecret", "authsecret", "querysecret", "Bearer", "snellius_session"} {
		if strings.Contains(out, s) {
			t.Fatalf("record carries %q: %s", s, out)
		}
	}
}

func TestParseLevelsRejectsUnknown(t *testing.T) {
	if _, err := ParseLevels("security,bogus"); err == nil {
		t.Fatal("want error")
	}
}
