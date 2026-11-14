package app

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/extension"
)

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}
func (l *lockedBuf) String() string { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

func buildAudit(t *testing.T, o Options) (*App, *lockedBuf, error) {
	t.Helper()
	dyn, kube := testutil.Clients(t)
	out := &lockedBuf{}
	o.PollInterval, o.MetricsInterval, o.UI, o.AuditOut = 10*time.Millisecond, time.Hour, ui, out
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}}, o, extension.New())
	return a, out, err
}

// Startup writes one record with the mode and listen address; a refused request still gets an ID.
func TestServeAuditsStartupAndTagsRequests(t *testing.T) {
	for _, mode := range []string{ModeLocal, ModeSecure} {
		t.Run(mode, func(t *testing.T) {
			a, out, err := buildAudit(t, Options{AuthMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ls, served := startServe(t, a, ctx)
			if mode == ModeLocal {
				req, _ := http.NewRequest("GET", "http://"+ls.main.Addr().String()+"/healthz", nil)
				req.Host = "evil.example"
				r, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				_ = r.Body.Close()
				if r.StatusCode == 200 || r.Header.Get("X-Request-Id") == "" {
					t.Fatalf("refused request: %d, id %q", r.StatusCode, r.Header.Get("X-Request-Id"))
				}
			}
			cancel()
			if err := testutil.Recv(t, served, "serve to return"); err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(out.String()), "\n")
			if len(lines) != 1 || !strings.Contains(lines[0], `"action":"startup"`) ||
				!strings.Contains(lines[0], `"mode":"`+mode+`"`) || !strings.Contains(lines[0], `"listen":"127.0.0.1:0"`) ||
				!strings.Contains(lines[0], `"log":"snellius.audit"`) {
				t.Fatalf("audit output: %q", out.String())
			}
		})
	}
}

func TestBuildRejectsUnknownAuditLevel(t *testing.T) {
	if _, _, err := buildAudit(t, Options{AuditLevels: "security,bogus"}); err == nil {
		t.Fatal("want error")
	}
}

// A record emitted by a request that is still draining at shutdown is written.
func TestServeWritesRecordsFromDrainingRequests(t *testing.T) {
	a, out, err := buildAudit(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	started, release := make(chan struct{}), make(chan struct{})
	a.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		a.Audit.Emit(r.Context(), audit.Record{Level: audit.LevelSecurity, Action: "drained"})
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls, served := startServe(t, a, ctx)
	go func() {
		if r, err := http.Get("http://" + ls.main.Addr().String() + "/"); err == nil {
			_ = r.Body.Close()
		}
	}()
	testutil.Recv(t, started, "request to start")
	cancel()
	// Once the listener refuses connections, shutdown is draining the request.
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := net.DialTimeout("tcp", ls.main.Addr().String(), 100*time.Millisecond)
		if err != nil {
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener never closed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The audit context must outlive the cancelled serve context.
	time.Sleep(100 * time.Millisecond)
	close(release)
	if err := testutil.Recv(t, served, "serve to return"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"action":"drained"`) {
		t.Fatalf("drained record lost: %q", out.String())
	}
}

// Local mode logs one port-forward exposure warning with the bind address; secure mode none.
func TestServeWarnsAboutLocalModeExposure(t *testing.T) {
	for mode, want := range map[string]int{ModeLocal: 1, ModeSecure: 0} {
		t.Run(mode, func(t *testing.T) {
			logs := &lockedBuf{}
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
			t.Cleanup(func() { slog.SetDefault(old) })
			a, _, err := buildAudit(t, Options{AuthMode: mode})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			ls, served := startServe(t, a, ctx)
			cancel()
			if err := testutil.Recv(t, served, "serve to return"); err != nil {
				t.Fatal(err)
			}
			var warns []string
			for _, l := range strings.Split(logs.String(), "\n") {
				if strings.Contains(l, "level=WARN") && strings.Contains(l, "port-forward") {
					warns = append(warns, l)
				}
			}
			if len(warns) != want {
				t.Fatalf("%d port-forward warnings, want %d:\n%s", len(warns), want, logs.String())
			}
			if want == 1 && (!strings.Contains(warns[0], "sees everything it reads") || !strings.Contains(warns[0], "addr="+ls.main.Addr().String())) {
				t.Fatalf("warning lacks the exposure or bind address: %s", warns[0])
			}
		})
	}
}

// A request still running when the drain times out is cut off, and its later
// audit record is counted as dropped rather than queued for a stopped writer.
func TestServeDrainTimeoutDropsLateRecords(t *testing.T) {
	old := drainTimeout
	drainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { drainTimeout = old })
	a, out, err := buildAudit(t, Options{})
	if err != nil {
		t.Fatal(err)
	}
	started, release, emitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
	a.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		a.Audit.Emit(r.Context(), audit.Record{Level: audit.LevelSecurity, Action: "late"})
		close(emitted)
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls, served := startServe(t, a, ctx)
	clientErr := make(chan error, 1)
	go func() {
		r, err := http.Get("http://" + ls.main.Addr().String() + "/")
		if err == nil {
			_ = r.Body.Close()
		}
		clientErr <- err
	}()
	testutil.Recv(t, started, "request to start")
	cancel()
	if err := testutil.Recv(t, served, "serve to return after the drain timeout"); err != nil {
		t.Fatal(err)
	}
	if err := testutil.Recv(t, clientErr, "the cut-off request to end"); err == nil {
		t.Fatal("the request running past the drain timeout must be cut off")
	}
	close(release)
	testutil.Recv(t, emitted, "the late emit to return")
	rec := httptest.NewRecorder()
	a.Metrics.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(rec.Body.String(), `snellius_audit_dropped_total{level="security"} 1`) || strings.Contains(out.String(), `"action":"late"`) {
		t.Fatalf("late record not dropped:\n%s\naudit: %s", rec.Body.String(), out.String())
	}
}
