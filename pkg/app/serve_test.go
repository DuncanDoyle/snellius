package app

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"
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
// methods and encoded paths included, with 401 without a session, and serves a
// signed-in viewer. Extension code runs only behind a session.
func TestServedSecureModeRequiresSession(t *testing.T) {
	var calls atomic.Int32
	count := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1) })
	ext := extension.WithHandlers("acme", func(r extension.Router, _ extension.Deps) {
		r.Handle("GET /hi", count)
		r.Handle("POST /hi", count)
	})
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil))
	kube.PrependReactor("create", "subjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
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
	signIn := httptest.NewRecorder()
	a.sessions.Complete(signIn, httptest.NewRequest("GET", "/auth/callback", nil), oidc.Result{Identity: oidc.Identity{Username: "u"},
		Expiry: time.Now().Add(time.Hour), ReturnPath: "/"})
	session := signIn.Result().Cookies()[0]
	// served is the status with a session; 0 means the same 404 an unknown route gets.
	for _, c := range []struct {
		method, path string
		served       int
		ext          bool
	}{
		{"GET", "/api/v1/meta", http.StatusOK, false},
		{"GET", "/api/v1/installs", http.StatusOK, false},
		{"POST", "/api/v1/installs", 0, false},
		{"DELETE", "/api/v1/meta", 0, false},
		{"GET", "/%61pi/v1/meta", http.StatusOK, false},
		{"GET", "/api/ext/acme/hi", http.StatusOK, true},
		{"POST", "/api/ext/acme/hi", http.StatusOK, true},
		{"GET", "/%61pi/ext/acme/hi", http.StatusOK, true},
	} {
		for _, withSession := range []bool{false, true} {
			before := calls.Load()
			req := &http.Request{Method: c.method, URL: &url.URL{Scheme: "http", Host: addr, Opaque: c.path}, Host: addr, Header: http.Header{}}
			code := http.StatusUnauthorized
			if withSession {
				req.AddCookie(session)
				code = c.served
				if code == 0 {
					code = http.StatusNotFound
				}
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			if r.StatusCode != code || strings.Contains(string(b), "secure-mode-incomplete") {
				t.Errorf("%s %s (session %v): %d %.80q, want %d", c.method, c.path, withSession, r.StatusCode, b, code)
			}
			// Extension code runs for a signed-in viewer only.
			if ran := calls.Load() - before; ran != 0 != (withSession && c.ext) {
				t.Errorf("%s %s (session %v): extension ran %d times", c.method, c.path, withSession, ran)
			}
		}
	}
}
