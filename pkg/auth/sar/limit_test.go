package sar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// unlimited lifts a global SAR limit in tests whose fake clock never refills the rate.
const unlimited = 1 << 30

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

// peak tracks the most concurrent calls between enter and leave.
type peak struct{ now, max atomic.Int32 }

func (p *peak) enter() {
	n := p.now.Add(1)
	for m := p.max.Load(); n > m && !p.max.CompareAndSwap(m, n); m = p.max.Load() {
	}
}

func (p *peak) leave() { p.now.Add(-1) }

// limitedFixture is an Authorizer with the given limits over checker, its limiter's waits signalled on waiting.
func limitedFixture(checker Checker, clk *clocktesting.FakeClock, maxInFlight, rate, burst int, waiting chan<- struct{}) *Authorizer {
	a := New(Options{Checker: checker, Kinds: testKinds, Clock: clk, Metrics: obs.NewRegistry(), IdentityFrom: identityFrom,
		MaxInFlight: maxInFlight, Rate: rate, Burst: burst})
	if waiting != nil {
		a.cache.lim.waiting = func() { waiting <- struct{}{} }
	}
	return a
}

// cancellable returns a request signed in as id and the cancel of its context.
func cancellable(id oidc.Identity) (*http.Request, context.CancelFunc) {
	r := request(id)
	ctx, cancel := context.WithCancel(r.Context())
	return r.WithContext(ctx), cancel
}

func receive(t *testing.T, ch <-chan struct{}, n int, what string) {
	t.Helper()
	for range n {
		<-ch
	}
	t.Logf("%d %s", n, what)
}

// limiterState returns l's permits in use, waiters for a permit and checks queued.
func limiterState(l *limiter) (inFlight, waiting, queued int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inFlight, l.permits.Len(), l.queued
}

func TestLimiterInFlight(t *testing.T) {
	before := runtime.NumGoroutine()
	var p peak
	started, waiting, gate := make(chan struct{}, 64), make(chan struct{}, 64), make(chan struct{})
	a := limitedFixture(checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) {
		p.enter()
		defer p.leave()
		started <- struct{}{}
		<-gate
		return true, nil
	}), clocktesting.NewFakeClock(t0), DefaultMaxInFlight, unlimited, unlimited, waiting)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			if _, err := a.AuthorizeErr(request(user("alice")), route(fmt.Sprintf("ns%02d", i), "r")); err != nil {
				t.Error(err)
			}
		})
	}
	receive(t, started, 32, "sent")
	receive(t, waiting, 32, "queued")
	if n, w, q := limiterState(a.cache.lim); n != 32 || w != 32 || q != 32 {
		t.Fatalf("in flight %d, waiting %d, queued %d; want 32 each", n, w, q)
	}
	close(gate)
	wg.Wait()
	if m := p.max.Load(); m != 32 {
		t.Fatalf("at most %d SARs at once, want 32", m)
	}
	settled(t, before)
}

func TestLimiterRate(t *testing.T) {
	clk := clocktesting.NewFakeClock(t0)
	l := newLimiter(clk, unlimited, 100, 32)
	waiting, admitted := make(chan struct{}, 64), make(chan struct{}, 64)
	l.waiting = func() { waiting <- struct{}{} }
	run := func(n int) *sync.WaitGroup {
		var wg sync.WaitGroup
		for range n {
			wg.Go(func() {
				release, err := l.acquire(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				release()
				admitted <- struct{}{}
			})
		}
		return &wg
	}
	quiet := func(when string) {
		t.Helper()
		if len(admitted) != 0 {
			t.Fatalf("%s: %d more admitted", when, len(admitted))
		}
	}
	wg := run(40)
	receive(t, admitted, 32, "admitted by the burst")
	receive(t, waiting, 8, "waiting for a token")
	quiet("burst spent")
	// One token per 10 ms: the first wakes, the other seven wait again.
	clk.Step(10 * time.Millisecond)
	receive(t, admitted, 1, "admitted after 10ms")
	receive(t, waiting, 7, "waiting again")
	quiet("after 10ms")
	clk.Step(70 * time.Millisecond)
	receive(t, admitted, 7, "admitted after 80ms")
	wg.Wait()
	if len(waiting) != 0 {
		t.Fatal("a waiter waited again with tokens to spare")
	}
	// After a long idle the bucket holds the burst, never more.
	clk.Step(10 * time.Second)
	wg = run(33)
	receive(t, admitted, 32, "admitted after idle")
	receive(t, waiting, 1, "over the burst")
	quiet("after idle")
	clk.Step(10 * time.Millisecond)
	wg.Wait()
}

// fillQueue holds l's only permit and queues maxQueued waiters behind it; release lets them all through.
func fillQueue(t *testing.T, l *limiter, waiting <-chan struct{}) (release func(), done *sync.WaitGroup) {
	t.Helper()
	held, err := l.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done = &sync.WaitGroup{}
	for range maxQueued {
		done.Go(func() {
			r, err := l.acquire(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			r()
		})
	}
	receive(t, waiting, maxQueued, "queued")
	return held, done
}

func TestLimiterQueueBound(t *testing.T) {
	before := runtime.NumGoroutine()
	waiting := make(chan struct{}, maxQueued)
	l := newLimiter(clocktesting.NewFakeClock(t0), 1, unlimited, unlimited)
	l.waiting = func() { waiting <- struct{}{} }
	held, done := fillQueue(t, l, waiting)
	if _, err := l.acquire(context.Background()); !errors.Is(err, ErrOverloaded) || !errors.Is(err, ErrCheck) {
		t.Fatalf("waiter %d: error %v, want ErrOverloaded", maxQueued+1, err)
	}
	held()
	done.Wait()
	settled(t, before)
}

func TestLimiterOverloadRecovers(t *testing.T) {
	waiting := make(chan struct{}, maxQueued)
	l := newLimiter(clocktesting.NewFakeClock(t0), 1, unlimited, unlimited)
	l.waiting = func() { waiting <- struct{}{} }
	held, done := fillQueue(t, l, waiting)
	if _, err := l.acquire(context.Background()); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("full queue: error %v, want ErrOverloaded", err)
	}
	held()
	done.Wait()
	if n, w, q := limiterState(l); n != 0 || w != 0 || q != 0 {
		t.Fatalf("drained: in flight %d, waiting %d, queued %d", n, w, q)
	}
	release, err := l.acquire(context.Background())
	if err != nil {
		t.Fatalf("after draining: %v", err)
	}
	release()
	if len(waiting) != 0 {
		t.Fatal("a check after draining waited")
	}
}

func TestLimiterQueuedCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	started, waiting, gate := make(chan Attributes, 2), make(chan struct{}, 2), make(chan struct{})
	a := limitedFixture(checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
		started <- at
		<-gate
		return true, nil
	}), clocktesting.NewFakeClock(t0), 1, unlimited, unlimited, waiting)
	held := make(chan error, 1)
	go func() { _, err := a.AuthorizeErr(request(user("alice")), route("held", "r")); held <- err }()
	<-started
	r, cancel := cancellable(user("alice"))
	queued := make(chan error, 1)
	go func() {
		_, err := a.AuthorizeErr(r, route("queued", "r"))
		queued <- err
	}()
	<-waiting
	cancel()
	if err := <-queued; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued check: %v, want context.Canceled", err)
	}
	if n, w, q := limiterState(a.cache.lim); n != 1 || w != 0 || q != 0 {
		t.Fatalf("after cancel: in flight %d, waiting %d, queued %d; want 1, 0, 0", n, w, q)
	}
	close(gate)
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	if len(started) != 0 {
		t.Fatalf("the cancelled check was sent: %v", <-started)
	}
	if _, err := a.AuthorizeErr(request(user("alice")), route("next", "r")); err != nil {
		t.Fatal(err)
	}
	if at := <-started; at.Namespace != "next" || len(waiting) != 0 {
		t.Fatalf("next check: sent %v, waited %v", at, len(waiting) != 0)
	}
	settled(t, before)
}

func TestLimiterReleasesOnCheckerErrorAndPanic(t *testing.T) {
	before := runtime.NumGoroutine()
	a := limitedFixture(checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
		switch at.Namespace {
		case "panic":
			panic("checker")
		case "error":
			return false, checkError(categoryRequest)
		}
		return true, nil
	}), clocktesting.NewFakeClock(t0), DefaultMaxInFlight, unlimited, unlimited, nil)
	var wg sync.WaitGroup
	for i := range 64 {
		wg.Go(func() {
			ns := "error"
			if i == 0 {
				ns = "panic"
				defer func() {
					if recover() == nil {
						t.Error("the checker's panic did not reach the caller")
					}
				}()
			}
			if _, err := a.AuthorizeErr(request(user(fmt.Sprintf("u%d", i))), route(ns, "r")); !errors.Is(err, ErrCheck) {
				t.Errorf("check %d: %v, want ErrCheck", i, err)
			}
		})
	}
	wg.Wait()
	if n, w, q := limiterState(a.cache.lim); n != 0 || w != 0 || q != 0 {
		t.Fatalf("in flight %d, waiting %d, queued %d after errors and a panic", n, w, q)
	}
	// All 32 permits are back: 32 checks run at once.
	started, gate := make(chan struct{}, 32), make(chan struct{})
	a.cache.checker = checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) {
		started <- struct{}{}
		<-gate
		return true, nil
	})
	for i := range 32 {
		wg.Go(func() {
			if _, err := a.AuthorizeErr(request(user("bob")), route(fmt.Sprintf("ok%d", i), "r")); err != nil {
				t.Error(err)
			}
		})
	}
	receive(t, started, 32, "sent at once")
	close(gate)
	wg.Wait()
	settled(t, before)
}

func TestMergedFlightLeaderCancelled(t *testing.T) {
	t.Run("queued", func(t *testing.T) {
		before := runtime.NumGoroutine()
		started, waiting, joined, gate := make(chan Attributes, 4), make(chan struct{}, 4), make(chan struct{}, 1), make(chan struct{})
		var sent atomic.Int32
		a := limitedFixture(checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
			if at.Namespace == "shop" {
				sent.Add(1)
			}
			started <- at
			<-gate
			return true, nil
		}), clocktesting.NewFakeClock(t0), 1, unlimited, unlimited, waiting)
		a.cache.joined = func() { joined <- struct{}{} }
		held := make(chan error, 1)
		go func() { _, err := a.AuthorizeErr(request(user("alice")), route("held", "r")); held <- err }()
		<-started
		r, cancel := cancellable(user("alice"))
		leader, waiter := make(chan error, 1), make(chan error, 1)
		go func() {
			_, err := a.AuthorizeErr(r, route("shop", "web"))
			leader <- err
		}()
		<-waiting
		go func() {
			ok, err := a.AuthorizeErr(request(user("alice")), route("shop", "web"))
			if err == nil && !ok {
				err = errors.New("denied")
			}
			waiter <- err
		}()
		<-joined
		cancel()
		if err := <-leader; !errors.Is(err, context.Canceled) {
			t.Fatalf("leader: %v, want context.Canceled", err)
		}
		// The waiter leads a new attempt, queued behind the held permit.
		<-waiting
		close(gate)
		if err := <-held; err != nil {
			t.Fatal(err)
		}
		if err := <-waiter; err != nil {
			t.Fatalf("waiter: %v", err)
		}
		if n := sent.Load(); n != 1 {
			t.Fatalf("%d SARs for shop, want 1", n)
		}
		settled(t, before)
	})
	t.Run("running", func(t *testing.T) {
		before := runtime.NumGoroutine()
		started, joined, gate := make(chan struct{}, 2), make(chan struct{}, 1), make(chan struct{})
		var sent atomic.Int32
		a := limitedFixture(checkerFunc(func(ctx context.Context, _ oidc.Identity, _ Attributes) (bool, error) {
			sent.Add(1)
			started <- struct{}{}
			<-gate
			// The SAR runs on a context detached from the cancelled request.
			return ctx.Err() == nil, ctx.Err()
		}), clocktesting.NewFakeClock(t0), 1, unlimited, unlimited, nil)
		a.cache.joined = func() { joined <- struct{}{} }
		r, cancel := cancellable(user("alice"))
		leader, waiter := make(chan error, 1), make(chan bool, 1)
		go func() {
			_, err := a.AuthorizeErr(r, route("shop", "web"))
			leader <- err
		}()
		<-started
		go func() {
			ok, err := a.AuthorizeErr(request(user("alice")), route("shop", "web"))
			waiter <- err == nil && ok
		}()
		<-joined
		cancel()
		close(gate)
		if !<-waiter {
			t.Fatal("the waiter did not get the running SAR's allow")
		}
		// The leader runs the SAR itself, so it returns the answer too.
		if err := <-leader; err != nil {
			t.Fatalf("leader: %v", err)
		}
		if !cached(a.cache, user("alice"), listAt("shop")) {
			t.Fatal("the running SAR's answer was not cached")
		}
		if n := sent.Load(); n != 1 {
			t.Fatalf("%d SARs, want 1", n)
		}
		settled(t, before)
	})
}

// mixedKinds serves testKinds by name and orderedKinds as the full-read set.
type mixedKinds struct{ *orderedKinds }

func (mixedKinds) Lookup(group, kind string) (Kind, bool) { return testKinds.Lookup(group, kind) }

func TestMixedTrafficSharesLimit(t *testing.T) {
	before := runtime.NumGoroutine()
	clk := clocktesting.NewFakeClock(t0)
	var (
		p     peak
		mu    sync.Mutex
		times []time.Time
	)
	gate := make(chan struct{})
	started := make(chan struct{}, 256)
	checker := checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
		p.enter()
		defer p.leave()
		mu.Lock()
		times = append(times, clk.Now())
		mu.Unlock()
		started <- struct{}{}
		<-gate
		// Cluster-wide lists allow, so full reads ask every kind; a named list denies, so its get follows.
		return at.Namespace == "" || at.Verb == "get" || at.Namespace[0] == 'b', nil
	})
	kinds := &orderedKinds{kinds: kindsN(20), key: "v1"}
	// A burst of 32, not the default, so the rate binds within this traffic.
	a := New(Options{Checker: checker, Kinds: mixedKinds{orderedKinds: kinds}, Clock: clk, Metrics: obs.NewRegistry(), IdentityFrom: identityFrom,
		Burst: 32})
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Go(func() {
			if ok, err := a.FullRead(request(user(fmt.Sprintf("full%d", i)))); err != nil || !ok {
				t.Errorf("full read %d: %v, %v", i, ok, err)
			}
		})
	}
	for i := range 10 {
		wg.Go(func() {
			if ok, err := a.AuthorizeNamedErr(request(user("named")), route(fmt.Sprintf("n%d", i), "web")); err != nil || !ok {
				t.Errorf("named %d: %v, %v", i, ok, err)
			}
		})
	}
	for i := range 2 {
		wg.Go(func() {
			qs := make([]api.ListQuery, 30)
			for j := range qs {
				qs[j] = api.ListQuery{Kind: objects.GroupKind{Group: gwGroup, Kind: "HTTPRoute"}, Namespace: fmt.Sprintf("b%02d", j)}
			}
			res, err := a.ResolveLists(request(user(fmt.Sprintf("batch%d", i))), qs)
			if err != nil || len(res) != 30 || !res[29] {
				t.Errorf("batch %d: %v, %v", i, res, err)
			}
		})
	}
	// 32 permits and 32 tokens: the 33rd SAR waits however much traffic remains.
	receive(t, started, 32, "sent at once")
	if n := p.now.Load(); n != 32 {
		t.Fatalf("%d SARs in flight, want 32", n)
	}
	close(gate)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	for finished := false; !finished; {
		select {
		case <-done:
			finished = true
		default:
			clk.Step(10 * time.Millisecond)
			runtime.Gosched()
		}
	}
	if m := p.max.Load(); m > 32 {
		t.Fatalf("%d SARs at once, want at most 32", m)
	}
	mu.Lock()
	defer mu.Unlock()
	want := 4*20 + 10*2 + 2*30
	if len(times) != want {
		t.Fatalf("%d SARs, want %d", len(times), want)
	}
	// By any time T, at most the burst plus the rate times T - t0 were sent.
	for i, at := range times {
		sentBy := 0
		for _, x := range times {
			if !x.After(at) {
				sentBy++
			}
		}
		if limit := 32 + int(at.Sub(t0)/(10*time.Millisecond)); sentBy > limit {
			t.Fatalf("SAR %d: %d sent by %v, over %d", i, sentBy, at.Sub(t0), limit)
		}
	}
	settled(t, before)
}
