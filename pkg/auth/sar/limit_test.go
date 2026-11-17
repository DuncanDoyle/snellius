package sar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/engine"
	"github.com/DuncanDoyle/snellius/pkg/objects"
	"github.com/DuncanDoyle/snellius/pkg/obs"
	"github.com/DuncanDoyle/snellius/pkg/platform"
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

// A full read's constituents queue under its request: when the only requester
// cancels, they leave the queue at once and none is sent; a waiter that remains
// leads the evaluation again and each kind is sent once.
func TestFullReadQueuedCancel(t *testing.T) {
	for _, withWaiter := range []bool{false, true} {
		t.Run(fmt.Sprintf("waiter=%v", withWaiter), func(t *testing.T) {
			before := runtime.NumGoroutine()
			started, waiting, joined, gate := make(chan Attributes, 8), make(chan struct{}, 8), make(chan struct{}, 1), make(chan struct{})
			kinds := &orderedKinds{kinds: kindsN(3), key: "v1"}
			a := New(Options{Checker: checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
				started <- at
				<-gate
				return true, nil
			}), Kinds: mixedKinds{orderedKinds: kinds}, Clock: clocktesting.NewFakeClock(t0), Metrics: obs.NewRegistry(), IdentityFrom: identityFrom,
				MaxInFlight: 1, Rate: unlimited, Burst: unlimited})
			a.cache.lim.waiting = func() { waiting <- struct{}{} }
			a.full.joined = func() { joined <- struct{}{} }
			held := make(chan error, 1)
			go func() { _, err := a.AuthorizeErr(request(user("alice")), route("held", "r")); held <- err }()
			<-started
			r, cancel := cancellable(user("alice"))
			leader, waiter := make(chan error, 1), make(chan error, 1)
			go func() { _, err := a.FullRead(r); leader <- err }()
			receive(t, waiting, 3, "constituents queued")
			if withWaiter {
				go func() {
					ok, err := a.FullRead(request(user("alice")))
					if err == nil && !ok {
						err = errors.New("false")
					}
					waiter <- err
				}()
				<-joined
			}
			cancel()
			if err := testutil.Recv(t, leader, "the cancelled leader to return"); !errors.Is(err, context.Canceled) {
				t.Fatalf("leader: %v, want context.Canceled", err)
			}
			if !withWaiter {
				if n, w, q := limiterState(a.cache.lim); n != 1 || w != 0 || q != 0 {
					t.Fatalf("after cancel: in flight %d, waiting %d, queued %d; want 1, 0, 0", n, w, q)
				}
			} else {
				receive(t, waiting, 3, "constituents queued by the waiter")
			}
			close(gate)
			if err := <-held; err != nil {
				t.Fatal(err)
			}
			want := 0
			if withWaiter {
				if err := <-waiter; err != nil {
					t.Fatalf("waiter: %v", err)
				}
				want = 3
			}
			if len(started) != want {
				t.Fatalf("%d constituent SARs sent, want %d", len(started), want)
			}
			settled(t, before)
		})
	}
}

// admissionState returns q's requests admitted and waiting, and the identities waiting.
func admissionState(q *admission) (admitted, waiting, ids int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.admitted, q.waiters.Len(), len(q.perID)
}

// The places are the in-flight limit over the per-request parallelism, at least one (D-116).
func TestAdmissionPlaces(t *testing.T) {
	for _, c := range []struct{ inFlight, places int }{{0, 4}, {DefaultMaxInFlight, 4}, {64, 8}, {4, 1}} {
		a := New(Options{Checker: &fakeChecker{}, Kinds: testKinds, MaxInFlight: c.inFlight})
		if a.admission.max != c.places {
			t.Errorf("MaxInFlight %d: %d places, want %d", c.inFlight, a.admission.max, c.places)
		}
	}
}

// Requests are admitted oldest first, at most max at once.
func TestAdmissionFIFO(t *testing.T) {
	before := runtime.NumGoroutine()
	q := newAdmission(2)
	waiting := make(chan struct{}, 3)
	q.waiting = func() { waiting <- struct{}{} }
	var held []func()
	for _, name := range []string{"a", "b"} {
		release, err := q.admit(context.Background(), hashIdentity(user(name)))
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, release)
	}
	type admitted struct {
		i       int
		release func()
	}
	order := make(chan admitted, 3)
	for i := range 3 {
		go func() {
			release, err := q.admit(context.Background(), hashIdentity(user(fmt.Sprintf("w%d", i))))
			if err != nil {
				t.Error(err)
				release = func() {}
			}
			order <- admitted{i, release}
		}()
		testutil.Recv(t, waiting, fmt.Sprintf("waiter %d to queue", i))
	}
	if n, w, _ := admissionState(q); n != 2 || w != 3 {
		t.Fatalf("%d admitted, %d waiting; want 2, 3", n, w)
	}
	next := func(want int) admitted {
		t.Helper()
		got := testutil.Recv(t, order, fmt.Sprintf("waiter %d", want))
		if got.i != want || len(order) != 0 {
			t.Fatalf("admitted waiter %d (%d more), want %d alone", got.i, len(order), want)
		}
		if n, _, _ := admissionState(q); n != 2 {
			t.Fatalf("%d admitted, want 2", n)
		}
		return got
	}
	held[0]()
	w0 := next(0)
	held[1]()
	w1 := next(1)
	w0.release()
	w0.release()
	w2 := next(2)
	w1.release()
	w2.release()
	if n, w, ids := admissionState(q); n != 0 || w != 0 || ids != 0 {
		t.Fatalf("drained: %d admitted, %d waiting, %d identities", n, w, ids)
	}
	settled(t, before)
}

// A cancelled waiter leaves the queue at once and never takes a place.
func TestAdmissionCancel(t *testing.T) {
	before := runtime.NumGoroutine()
	q := newAdmission(1)
	waiting := make(chan struct{}, 1)
	q.waiting = func() { waiting <- struct{}{} }
	held, err := q.admit(context.Background(), hashIdentity(user("holder")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := q.admit(ctx, hashIdentity(user("alice")))
		done <- err
	}()
	testutil.Recv(t, waiting, "alice to queue")
	cancel()
	if err := testutil.Recv(t, done, "the cancelled waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v, want context.Canceled", err)
	}
	if n, w, ids := admissionState(q); n != 1 || w != 0 || ids != 0 {
		t.Fatalf("after cancel: %d admitted, %d waiting, %d identities; want 1, 0, 0", n, w, ids)
	}
	held()
	release, err := q.admit(context.Background(), hashIdentity(user("bob")))
	if err != nil || len(waiting) != 0 {
		t.Fatalf("after the place came back: %v, waited %v", err, len(waiting) != 0)
	}
	release()
	settled(t, before)
}

// Waiting requests are bounded per identity and in all: past either bound
// admit answers ErrOverloaded at once, and the queue drains after.
func TestAdmissionBounds(t *testing.T) {
	before := runtime.NumGoroutine()
	q := newAdmission(1)
	waiting := make(chan struct{}, maxAdmitWaiting)
	q.waiting = func() { waiting <- struct{}{} }
	held, err := q.admit(context.Background(), hashIdentity(user("holder")))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	queue := func(name string) {
		wg.Go(func() {
			release, err := q.admit(context.Background(), hashIdentity(user(name)))
			if err != nil {
				t.Error(err)
				return
			}
			release()
		})
		testutil.Recv(t, waiting, name+" to queue")
	}
	for range maxAdmitPerID {
		queue("alice")
	}
	if _, err := q.admit(context.Background(), hashIdentity(user("alice"))); !errors.Is(err, ErrOverloaded) || !errors.Is(err, ErrCheck) {
		t.Fatalf("alice's request %d: %v, want ErrOverloaded", maxAdmitPerID+1, err)
	}
	for i := range maxAdmitWaiting - maxAdmitPerID {
		queue(fmt.Sprintf("u%03d", i))
	}
	if _, err := q.admit(context.Background(), hashIdentity(user("bob"))); !errors.Is(err, ErrOverloaded) {
		t.Fatalf("request %d: %v, want ErrOverloaded", maxAdmitWaiting+1, err)
	}
	held()
	wg.Wait()
	if n, w, ids := admissionState(q); n != 0 || w != 0 || ids != 0 {
		t.Fatalf("drained: %d admitted, %d waiting, %d identities", n, w, ids)
	}
	release, err := q.admit(context.Background(), hashIdentity(user("bob")))
	if err != nil || len(waiting) != 0 {
		t.Fatalf("after draining: %v, waited %v", err, len(waiting) != 0)
	}
	release()
	settled(t, before)
}

// noModel is a ModelSource without a model: the admission tests serve only an extension.
type noModel struct{}

func (noModel) Current() *engine.Published { return nil }
func (noModel) Platform() platform.Info    { return platform.Info{} }

// admitFixture is an Authorizer with one admission place over checker, its
// admission waits signalled on waiting, and the API over it in complete
// secure mode. Its extension GET /api/ext/admit/{list} authorises route "a"
// in each comma-separated namespace of list and writes the namespaces it may
// see, comma-separated; an allowed namespace "panic" panics after the checks.
// The list "full" writes the full read's answer instead.
func admitFixture(checker Checker, clk *clocktesting.FakeClock, waiting chan<- struct{}, opts ...api.HandlerOption) (*Authorizer, http.Handler) {
	a := limitedFixture(checker, clk, batchParallel, unlimited, unlimited, nil)
	a.admission.waiting = func() { waiting <- struct{}{} }
	ext := api.ExtMount{Name: "admit", Pattern: "GET /{list}", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("list") == "full" {
			all, err := a.FullRead(r)
			api.ReportAuthzError(r, err)
			api.WriteJSON(w, all)
			return
		}
		var seen []string
		for ns := range strings.SplitSeq(r.PathValue("list"), ",") {
			if a.Authorize(r, route(ns, "a")) {
				seen = append(seen, ns)
			}
		}
		if slices.Contains(seen, "panic") {
			panic("handler")
		}
		api.WriteJSON(w, strings.Join(seen, ","))
	})}
	opts = append([]api.HandlerOption{api.WithSecureMode(true), api.WithCommitClock(clk.Now), api.WithExtMounts(ext)}, opts...)
	return a, api.NewHandler(noModel{}, fstest.MapFS{}, a, opts...)
}

// admitRequest is GET /api/ext/admit/{list} as id under ctx.
func admitRequest(ctx context.Context, id oidc.Identity, list string) *http.Request {
	return httptest.NewRequestWithContext(context.WithValue(ctx, idKey{}, id), http.MethodGet, "/api/ext/admit/"+list, nil)
}

// admitGet serves admitRequest on h.
func admitGet(ctx context.Context, h http.Handler, id oidc.Identity, list string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, admitRequest(ctx, id, list))
	return w
}

// wantSeen checks that w answered 200 with the namespaces seen.
func wantSeen(t *testing.T, w *httptest.ResponseRecorder, who, seen string) {
	t.Helper()
	if want := `"` + seen + `"` + "\n"; w.Code != http.StatusOK || w.Body.String() != want {
		t.Errorf("%s: %d %q, want 200 %q", who, w.Code, w.Body.String(), want)
	}
}

// A request whose checks are cached never waits. One that needs a SAR waits
// for a place, in arrival order, and holds it until it is answered (D-116).
func TestAdmissionAtCommit(t *testing.T) {
	before := runtime.NumGoroutine()
	clk := clocktesting.NewFakeClock(t0)
	waiting := make(chan struct{}, 3)
	var a *Authorizer
	var sent atomic.Int32
	a, h := admitFixture(checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) {
		sent.Add(1)
		if n, _, _ := admissionState(a.admission); n != 1 {
			t.Errorf("a SAR sent with %d requests admitted, want 1", n)
		}
		return true, nil
	}), clk, waiting)
	names := map[idHash]string{}
	for _, who := range []string{"alice", "holder", "bob", "carol", "dave"} {
		names[hashIdentity(user(who))] = who
	}
	// granted runs under the admission's lock, in the order places are given.
	var order []string
	a.admission.granted = func(h idHash) { order = append(order, names[h]) }
	wantSeen(t, admitGet(context.Background(), h, user("alice"), "shop"), "alice cold", "shop")
	held, err := a.Admit(request(user("holder")))
	if err != nil {
		t.Fatal(err)
	}
	n := sent.Load()
	wantSeen(t, admitGet(context.Background(), h, user("alice"), "shop"), "alice warm", "shop")
	if len(waiting) != 0 || sent.Load() != n {
		t.Fatalf("a cached request waited (%v) or sent %d SARs", len(waiting) != 0, sent.Load()-n)
	}
	done := make(chan *httptest.ResponseRecorder, 3)
	for _, who := range []string{"bob", "carol", "dave"} {
		go func() { done <- admitGet(context.Background(), h, user(who), "shop") }()
		testutil.Recv(t, waiting, who+" to queue")
	}
	if n, w, _ := admissionState(a.admission); n != 1 || w != 3 {
		t.Fatalf("%d admitted, %d waiting; want 1, 3", n, w)
	}
	held()
	for range 3 {
		wantSeen(t, testutil.Recv(t, done, "an answer"), "a queued viewer", "shop")
	}
	if want := []string{"alice", "holder", "bob", "carol", "dave"}; !slices.Equal(order, want) {
		t.Errorf("admitted in order %v, want %v", order, want)
	}
	if n, w, ids := admissionState(a.admission); n != 0 || w != 0 || ids != 0 {
		t.Fatalf("after the answers: %d admitted, %d waiting, %d identities", n, w, ids)
	}
	settled(t, before)
}

// Every way out of a request gives its place back: a SAR error, a handler
// panic, a cancelled wait; a full queue answers 503 at once (D-116).
func TestAdmissionReleased(t *testing.T) {
	before := runtime.NumGoroutine()
	clk := clocktesting.NewFakeClock(t0)
	waiting := make(chan struct{}, maxAdmitPerID)
	var mu sync.Mutex
	var reasons []string
	a, h := admitFixture(checkerFunc(func(_ context.Context, _ oidc.Identity, at Attributes) (bool, error) {
		if at.Namespace == "error" {
			return false, checkError(categoryRequest)
		}
		return true, nil
	}), clk, waiting, api.WithAuthzAudit(func(_ *http.Request, why string) {
		mu.Lock()
		defer mu.Unlock()
		reasons = append(reasons, why)
	}))
	free := func(when string, admitted int) {
		t.Helper()
		if n, w, ids := admissionState(a.admission); n != admitted || w != 0 || ids != 0 {
			t.Fatalf("%s: %d admitted, %d waiting, %d identities; want %d, 0, 0", when, n, w, ids, admitted)
		}
	}
	if w := admitGet(context.Background(), h, user("alice"), "error"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("SAR error: %d", w.Code)
	}
	free("after a SAR error", 0)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the handler's panic did not reach the caller")
			}
		}()
		admitGet(context.Background(), h, user("alice"), "panic")
	}()
	free("after a panic", 0)

	held, err := a.Admit(request(user("holder")))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	codes := make(chan int, maxAdmitPerID)
	go func() { codes <- admitGet(ctx, h, user("bob"), "shop").Code }()
	testutil.Recv(t, waiting, "bob to queue")
	cancel()
	if code := testutil.Recv(t, codes, "bob's answer"); code != http.StatusServiceUnavailable {
		t.Errorf("cancelled wait: %d", code)
	}
	free("after a cancelled wait", 1)

	ctx, cancel = context.WithCancel(context.Background())
	for i := range maxAdmitPerID {
		go func() { codes <- admitGet(ctx, h, user("carol"), fmt.Sprintf("ns%d", i)).Code }()
		testutil.Recv(t, waiting, "carol to queue")
	}
	mu.Lock()
	reasons = nil
	mu.Unlock()
	if w := admitGet(context.Background(), h, user("carol"), "more"); w.Code != http.StatusServiceUnavailable {
		t.Errorf("full queue: %d", w.Code)
	}
	mu.Lock()
	if len(reasons) != 1 || reasons[0] != api.AuditCheckFailed {
		t.Errorf("full queue audited %v, want check-failed", reasons)
	}
	mu.Unlock()
	cancel()
	for range maxAdmitPerID {
		if code := testutil.Recv(t, codes, "carol's answers"); code != http.StatusServiceUnavailable {
			t.Errorf("cancelled wait: %d", code)
		}
	}
	held()
	free("after the queue drained", 0)
	settled(t, before)
}

// An allow read before the wait for admission, which expires during it and is
// revoked, is asked again by the admitted build: the response hides its route.
func TestAdmissionExpiredWhileWaiting(t *testing.T) {
	clk := clocktesting.NewFakeClock(t0)
	waiting := make(chan struct{}, 1)
	var mu sync.Mutex
	revoked := false
	c := &fakeChecker{decide: func(_ oidc.Identity, at Attributes) (bool, error) {
		mu.Lock()
		defer mu.Unlock()
		return !revoked || at.Namespace != "shop", nil
	}}
	a, h := admitFixture(c, clk, waiting)
	wantSeen(t, admitGet(context.Background(), h, user("alice"), "shop"), "before", "shop")
	held, err := a.Admit(request(user("holder")))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- admitGet(context.Background(), h, user("alice"), "shop,blog") }()
	// The first build read shop's cached allow, then needed blog.
	testutil.Recv(t, waiting, "alice to queue")
	clk.Step(DefaultAllowTTL)
	mu.Lock()
	revoked = true
	mu.Unlock()
	held()
	wantSeen(t, testutil.Recv(t, done, "alice's answer"), "after the wait", "blog")
	n := 0
	for _, x := range c.calls {
		if x.a == listAt("shop") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("list in shop sent %d times, want again after the wait", n)
	}
}

// stallWriter blocks every Write until unblock closes, signalling writing first.
type stallWriter struct {
	*httptest.ResponseRecorder
	writing chan<- struct{}
	unblock <-chan struct{}
}

func (s *stallWriter) Write(p []byte) (int, error) {
	select {
	case s.writing <- struct{}{}:
	default:
	}
	<-s.unblock
	return s.ResponseRecorder.Write(p)
}

// A response is final before it is written: a client that stalls the write
// holds no place, and another cold request is admitted meanwhile (D-116).
func TestAdmissionReleasedBeforeWrite(t *testing.T) {
	clk := clocktesting.NewFakeClock(t0)
	a, h := admitFixture(checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) { return true, nil }), clk, make(chan struct{}, 2))
	writing, unblock := make(chan struct{}, 1), make(chan struct{})
	stalled := &stallWriter{ResponseRecorder: httptest.NewRecorder(), writing: writing, unblock: unblock}
	first := make(chan struct{})
	go func() {
		h.ServeHTTP(stalled, admitRequest(context.Background(), user("alice"), "shop"))
		close(first)
	}()
	testutil.Recv(t, writing, "alice's response to be written")
	if n, _, _ := admissionState(a.admission); n != 0 {
		t.Errorf("%d places held while a response is written, want 0", n)
	}
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- admitGet(context.Background(), h, user("bob"), "blog") }()
	wantSeen(t, testutil.Recv(t, second, "bob's answer while alice's write stalls"), "bob", "blog")
	close(unblock)
	testutil.Recv(t, first, "alice's write")
	wantSeen(t, stalled.ResponseRecorder, "alice", "shop")
}

// A request that joins a full read another, admitted, request is evaluating
// is not queued and sends no SARs of its own (D-116 item 2).
func TestAdmissionFullReadFollower(t *testing.T) {
	clk := clocktesting.NewFakeClock(t0)
	started, release := make(chan struct{}, 8), make(chan struct{})
	var sent atomic.Int32
	waiting := make(chan struct{}, 2)
	a, h := admitFixture(checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) {
		sent.Add(1)
		started <- struct{}{}
		<-release
		return true, nil
	}), clk, waiting)
	joined := make(chan struct{}, 1)
	a.full.joined = func() { joined <- struct{}{} }
	answers := make(chan *httptest.ResponseRecorder, 2)
	go func() { answers <- admitGet(context.Background(), h, user("alice"), "full") }()
	testutil.Recv(t, started, "the admitted full read")
	go func() { answers <- admitGet(context.Background(), h, user("alice"), "full") }()
	testutil.Recv(t, joined, "the second request to join the evaluation")
	close(release)
	for range 2 {
		if w := testutil.Recv(t, answers, "an answer"); w.Code != http.StatusOK || w.Body.String() != "true\n" {
			t.Errorf("full read: %d %q, want true", w.Code, w.Body.String())
		}
	}
	kinds, _ := testKinds.All()
	if len(waiting) != 0 || int(sent.Load()) != len(kinds) {
		t.Errorf("queued %v, %d SARs; want no wait and one list per kind (%d)", len(waiting) != 0, sent.Load(), len(kinds))
	}
}
