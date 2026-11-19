package sar_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/auth/sar"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// asUser returns a request signed in as user and the cancel of its context.
func asUser(user string) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), userKey{}, user))
	return httptest.NewRequest(http.MethodGet, "/api/v1/installs", nil).WithContext(ctx), cancel
}

// gatedChecker blocks every SAR on gate, signals started and tracks the most at once.
type gatedChecker struct {
	started   chan sar.Attributes
	gate      chan struct{}
	now, peak atomic.Int32
	calls     atomic.Int32
}

func newGated(n int) *gatedChecker {
	return &gatedChecker{started: make(chan sar.Attributes, n), gate: make(chan struct{})}
}

func (g *gatedChecker) Check(_ context.Context, _ oidc.Identity, a sar.Attributes) (bool, error) {
	g.calls.Add(1)
	n := g.now.Add(1)
	defer g.now.Add(-1)
	for p := g.peak.Load(); n > p && !g.peak.CompareAndSwap(p, n); p = g.peak.Load() {
	}
	g.started <- a
	<-g.gate
	return true, nil
}

func routeBuckets(n int) []api.ListQuery {
	qs := make([]api.ListQuery, n)
	for i := range qs {
		qs[i] = api.ListQuery{Kind: objects.GroupKind{Group: routesGroup, Kind: "HTTPRoute"}, Namespace: fmt.Sprintf("ns-%03d", i)}
	}
	return qs
}

func TestBatchResolve(t *testing.T) {
	t.Run("serial answers", func(t *testing.T) {
		decide := grant(nil)
		batch, serial := newAuthz(&checker{decide: decide}, clocktesting.NewFakeClock(t0)), newAuthz(&checker{decide: decide}, clocktesting.NewFakeClock(t0))
		gk := func(group, kind string) objects.GroupKind { return objects.GroupKind{Group: group, Kind: kind} }
		qs := append(routeBuckets(3),
			api.ListQuery{Kind: gk(routesGroup, "Gateway"), Namespace: "ns-000"},
			api.ListQuery{Kind: gk(routesGroup, "HTTPRoute")},
			api.ListQuery{Kind: gk("example.com", "Widget"), Namespace: "ns-000"},
			api.ListQuery{Kind: gk(routesGroup, "GatewayClass"), Namespace: "ns-000"})
		r, cancel := asUser("team")
		defer cancel()
		got, err := batch.ResolveLists(r, qs)
		if err != nil {
			t.Fatal(err)
		}
		for i, q := range qs {
			want, err := serial.AuthorizeErr(r, objects.Ref{Group: q.Kind.Group, Kind: q.Kind.Kind, Namespace: q.Namespace, Name: "x"})
			if err != nil || got[i] != want {
				t.Errorf("%+v: batch %v, serial %v (%v)", q, got[i], want, err)
			}
		}
		if !got[0] || got[1] {
			t.Errorf("team's grant: %v", got)
		}
	})
	t.Run("eight workers", func(t *testing.T) {
		before := runtime.NumGoroutine()
		g := newGated(20)
		a := newAuthz(g, clocktesting.NewFakeClock(t0))
		r, cancel := asUser("team")
		defer cancel()
		done := make(chan error, 1)
		go func() { _, err := a.ResolveLists(r, routeBuckets(20)); done <- err }()
		for range 8 {
			<-g.started
		}
		if n := g.now.Load(); n != 8 {
			t.Fatalf("%d SARs at once, want 8", n)
		}
		close(g.gate)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if p, n := g.peak.Load(), g.calls.Load(); p != 8 || n != 20 {
			t.Fatalf("peak %d, calls %d; want 8 and 20", p, n)
		}
		settled(t, before)
	})
	t.Run("cancel", func(t *testing.T) {
		before := runtime.NumGoroutine()
		g := newGated(20)
		a := newAuthz(g, clocktesting.NewFakeClock(t0))
		r, cancel := asUser("team")
		done := make(chan error, 1)
		go func() { _, err := a.ResolveLists(r, routeBuckets(20)); done <- err }()
		for range 8 {
			<-g.started
		}
		cancel()
		close(g.gate)
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled batch: %v, want context.Canceled", err)
		}
		if n := g.calls.Load(); n != 8 {
			t.Fatalf("%d SARs after the cancel, want the 8 already running", n)
		}
		settled(t, before)
	})
	t.Run("one bucket errors", func(t *testing.T) {
		src, base := world(t)
		allow := grant(nil)
		c := &checker{decide: func(user string, a sar.Attributes, n int) (bool, error) {
			if a.Verb == "list" && a.Resource == "httproutes" && a.Namespace == "ns-002" {
				return false, sar.ErrCheck
			}
			return allow(user, a, n)
		}}
		srv := serve(t, src, newAuthz(c, clocktesting.NewFakeClock(t0)))
		for _, p := range []string{"/routes", "/paths"} {
			if code, body := call(t, srv, "team", base+p); code != http.StatusServiceUnavailable || string(body) != `{"error":"authz-unavailable"}`+"\n" {
				t.Errorf("GET %s: %d %s", p, code, body)
			}
		}
	})
}

// settled fails t when more goroutines run than before; it yields, never sleeps, a bounded number of times.
func settled(t *testing.T, before int) {
	t.Helper()
	for i := 0; i < 100000 && runtime.NumGoroutine() > before; i++ {
		runtime.Gosched()
	}
	if n := runtime.NumGoroutine(); n > before {
		t.Fatalf("%d goroutines left running, want at most %d", n, before)
	}
}
