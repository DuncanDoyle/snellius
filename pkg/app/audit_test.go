package app

import (
	"bytes"
	"context"
	"net"
	"net/http"
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
