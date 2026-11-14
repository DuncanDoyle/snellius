package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/extension"
)

// TestMain discards the default logger so served apps keep test output clean.
func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.DiscardHandler))
	os.Exit(m.Run())
}

type listeners struct{ main, probe net.Listener }

// startServe runs serve on two ephemeral ports and waits until both are bound.
func startServe(t *testing.T, a *App, ctx context.Context) (listeners, chan error) {
	t.Helper()
	bound := make(chan listeners, 1)
	served := make(chan error, 1)
	go func() {
		served <- serve(ctx, a, "127.0.0.1:0", "127.0.0.1:0", func(m, p net.Listener) { bound <- listeners{m, p} })
	}()
	return testutil.Recv(t, bound, "serve to listen"), served
}

func getBody(t *testing.T, url string) (int, string) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, string(b)
}

// Both listeners answer; the probe listener serves probes and metrics, no API or UI.
func TestServeStartsProbeListener(t *testing.T) {
	a := build(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls, served := startServe(t, a, ctx)
	main, probe := "http://"+ls.main.Addr().String(), "http://"+ls.probe.Addr().String()
	if code, _ := getBody(t, main+"/healthz"); code != 200 {
		t.Fatalf("main /healthz: %d", code)
	}
	if code, _ := getBody(t, probe+"/healthz"); code != 200 {
		t.Fatalf("probe /healthz: %d", code)
	}
	code, body := getBody(t, probe+"/metrics")
	if code != 200 || !strings.Contains(body, `snellius_auth_mode{mode="local"} 1`) {
		t.Fatalf("probe /metrics: %d\n%s", code, body)
	}
	for _, p := range []string{"/api/v1/installs", "/"} {
		if code, _ := getBody(t, probe+p); code != 404 {
			t.Fatalf("probe %s: %d, want 404", p, code)
		}
	}
	cancel()
	if err := testutil.Recv(t, served, "serve to return"); err != nil {
		t.Fatal(err)
	}
}

// A failing second bind returns the error and closes the first listener.
func TestServeSecondBindFails(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	mainAddr := free.Addr().String()
	_ = free.Close()
	a := build(t)
	if err := serve(context.Background(), a, mainAddr, busy.Addr().String(), nil); err == nil {
		t.Fatal("serve must fail when the probe address is in use")
	}
	ln, err := net.Listen("tcp", mainAddr)
	if err != nil {
		t.Fatalf("the first listener must be closed: %v", err)
	}
	_ = ln.Close()
	time.Sleep(100 * time.Millisecond)
	if a.Engine.Current() != nil {
		t.Fatal("no component may run after serve returned an error")
	}
}

// One server failing stops the other; nothing keeps serving.
func TestServeOneServerFails(t *testing.T) {
	for _, which := range []string{"main", "probe"} {
		t.Run(which, func(t *testing.T) {
			a := build(t)
			ls, served := startServe(t, a, context.Background())
			failing, other := ls.main, ls.probe
			if which == "probe" {
				failing, other = ls.probe, ls.main
			}
			_ = failing.Close()
			if err := testutil.Recv(t, served, "serve to return"); err == nil {
				t.Fatal("serve must return the failing server's error")
			}
			if c, err := net.DialTimeout("tcp", other.Addr().String(), time.Second); err == nil {
				_ = c.Close()
				t.Fatal("the other server must stop")
			}
		})
	}
}

// Cancelling ctx lets an in-flight request on each listener finish.
func TestServeDrainsBoth(t *testing.T) {
	a := build(t)
	entered, release := make(chan struct{}, 2), make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		select {
		case <-release:
			_, _ = io.WriteString(w, "done")
		case <-time.After(testutil.RecvTimeout):
		}
	})
	a.Handler = slow
	a.probe = slow
	ctx, cancel := context.WithCancel(context.Background())
	ls, served := startServe(t, a, ctx)
	resp := make(chan string, 2)
	for _, l := range []net.Listener{ls.main, ls.probe} {
		go func() {
			r, err := http.Get("http://" + l.Addr().String() + "/")
			if err != nil {
				resp <- err.Error()
				return
			}
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			resp <- string(b)
		}()
	}
	testutil.Recv(t, entered, "the first request")
	testutil.Recv(t, entered, "the second request")
	cancel()
	select {
	case err := <-served:
		t.Fatalf("serve returned with requests in flight: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if got := testutil.Recv(t, resp, "an in-flight response"); got != "done" {
			t.Fatalf("in-flight request must complete, got %q", got)
		}
	}
	if err := testutil.Recv(t, served, "serve to return"); err != nil {
		t.Fatal(err)
	}
}

// The served secure-mode app answers every API route, extension routes, other
// methods and encoded paths included, with 503 and never runs extension code.
func TestServedSecureModeGatesTheAPI(t *testing.T) {
	var calls atomic.Int32
	count := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) })
	ext := extension.WithHandlers("acme", func(r extension.Router, _ extension.Deps) {
		r.Handle("GET /hi", count)
		r.Handle("POST /hi", count)
	})
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil))
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}},
		Options{AuthMode: ModeSecure, PollInterval: 10 * time.Millisecond, MetricsInterval: time.Hour, UI: ui, AuditOut: io.Discard},
		extension.New(ext))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ls, served := startServe(t, a, ctx)
	defer func() {
		cancel()
		if err := testutil.Recv(t, served, "serve to return"); err != nil {
			t.Error(err)
		}
	}()
	eventually(t, "an install in the model", func() bool { return len(a.Engine.Installs()) > 0 })
	addr := ls.main.Addr().String()
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/v1/meta"},
		{"GET", "/api/v1/installs"},
		{"POST", "/api/v1/installs"},
		{"DELETE", "/api/v1/meta"},
		{"GET", "/%61pi/v1/meta"},
		{"GET", "/api/ext/acme/hi"},
		{"POST", "/api/ext/acme/hi"},
		{"GET", "/%61pi/ext/acme/hi"},
	} {
		req := &http.Request{Method: c.method, URL: &url.URL{Scheme: "http", Host: addr, Opaque: c.path}, Host: addr, Header: http.Header{}}
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.StatusCode != http.StatusServiceUnavailable || strings.TrimSpace(string(b)) != `{"error":"secure-mode-incomplete"}` {
			t.Errorf("%s %s: %d %.80q", c.method, c.path, r.StatusCode, b)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("extension handler ran %d times behind the gate", n)
	}
}
