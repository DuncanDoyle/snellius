package audit

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

// Once the writer has stopped, Emit returns at once and counts the record as dropped.
func TestEmitAfterStopCountsDropped(t *testing.T) {
	var buf syncBuf
	reg := obs.NewRegistry()
	l := New(&buf, levels(t, ""), reg)
	drain(t, l, func() {})
	done := make(chan struct{})
	go func() {
		for range 3 {
			l.Emit(context.Background(), Record{Level: LevelSecurity, Action: "late"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Emit blocked after the writer stopped")
	}
	if n := len(l.queue); n != 0 || buf.String() != "" {
		t.Fatalf("%d records queued, output %q; want none", n, buf.String())
	}
	rec := httptest.NewRecorder()
	reg.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `snellius_audit_dropped_total{level="security"} 3`) {
		t.Fatalf("late records not counted as dropped:\n%s", rec.Body.String())
	}
}

// A caller's X-Request-Id and X-Forwarded-For count only from a peer in a trusted CIDR.
func TestRequestIDTrusting(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("fd00::/8")}
	cases := []struct {
		name, peer, id, xff string
		cidrs               []netip.Prefix
		wantID, wantSource  string
	}{
		{name: "trusted peer keeps a valid ID", peer: "10.1.2.3:5000", id: "abc-123_x.y", cidrs: trusted, wantID: "abc-123_x.y", wantSource: "10.1.2.3"},
		{name: "trusted IPv6 peer", peer: "[fd00::1]:5000", id: "v6", cidrs: trusted, wantID: "v6", wantSource: "fd00::1"},
		{name: "untrusted peer", peer: "192.0.2.7:5000", id: "abc", xff: "198.51.100.1", cidrs: trusted, wantSource: "192.0.2.7"},
		{name: "no CIDRs", peer: "10.1.2.3:5000", id: "abc", xff: "198.51.100.1", wantSource: "10.1.2.3"},
		{name: "invalid characters", peer: "10.1.2.3:5000", id: "a b", cidrs: trusted, wantSource: "10.1.2.3"},
		{name: "too long", peer: "10.1.2.3:5000", id: strings.Repeat("a", 65), cidrs: trusted, wantSource: "10.1.2.3"},
		{name: "longest kept", peer: "10.1.2.3:5000", id: strings.Repeat("a", 64), cidrs: trusted, wantID: strings.Repeat("a", 64), wantSource: "10.1.2.3"},
		// The source is the right-most address that is not a trusted proxy; a client cannot prepend its way in.
		{name: "forwarded source", peer: "10.1.2.3:5000", xff: "203.0.113.9, 198.51.100.1, 10.9.9.9", cidrs: trusted, wantSource: "198.51.100.1"},
		{name: "forwarded garbage", peer: "10.1.2.3:5000", xff: "not-an-ip", cidrs: trusted, wantSource: "10.1.2.3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var seenID, seenSource string
			h := RequestIDTrusting(c.cidrs, http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				seenID, seenSource = RequestIDFrom(r.Context()), FromRequest(r).Source
			}))
			req := httptest.NewRequest("GET", "/", nil)
			req.RemoteAddr = c.peer
			if c.id != "" {
				req.Header.Set("X-Request-Id", c.id)
			}
			if c.xff != "" {
				req.Header.Set("X-Forwarded-For", c.xff)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			got := rec.Header().Get("X-Request-Id")
			if got != seenID || got == "" {
				t.Fatalf("header %q, context %q", got, seenID)
			}
			if c.wantID != "" && got != c.wantID {
				t.Errorf("ID = %q, want %q", got, c.wantID)
			}
			if c.wantID == "" && got == c.id {
				t.Errorf("untrusted ID %q kept", got)
			}
			if seenSource != c.wantSource {
				t.Errorf("source = %q, want %q", seenSource, c.wantSource)
			}
		})
	}
}

func TestParseCIDRs(t *testing.T) {
	got, err := ParseCIDRs(" 10.0.0.0/8, ,fd00::1/8")
	if err != nil || len(got) != 2 || got[1].String() != "fd00::/8" {
		t.Fatalf("ParseCIDRs = %v, %v", got, err)
	}
	if none, err := ParseCIDRs(""); err != nil || none != nil {
		t.Fatalf("empty = %v, %v", none, err)
	}
	if _, err := ParseCIDRs("10.0.0.1"); err == nil {
		t.Fatal("a bare address was accepted")
	}
}
