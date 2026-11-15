package sar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

const testGroup = "test.example.io"

// orderedKinds serves kinds in a fixed order under a key the test changes.
type orderedKinds struct {
	mu    sync.Mutex
	kinds []Kind
	key   string
}

func (k *orderedKinds) Lookup(string, string) (Kind, bool) { return Kind{}, false }

func (k *orderedKinds) All() ([]Kind, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]Kind(nil), k.kinds...), k.key
}

func (k *orderedKinds) set(key string, kinds ...Kind) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.kinds, k.key = kinds, key
}

func kindN(i int) Kind { return Kind{Group: testGroup, Resource: fmt.Sprintf("k%d", i)} }

func kindsN(n int) []Kind {
	out := make([]Kind, n)
	for i := range out {
		out[i] = kindN(i)
	}
	return out
}

// clusterList is the cluster-wide list on resource that a full read asks.
func clusterList(resource string) Attributes {
	return Attributes{Verb: "list", Group: testGroup, Resource: resource}
}

// newFullFixture serves n ordered kinds under key "v1".
func newFullFixture(n int, decide func(oidc.Identity, Attributes) (bool, error)) (*fixture, *orderedKinds) {
	kinds := &orderedKinds{kinds: kindsN(n), key: "v1"}
	f := &fixture{checker: &fakeChecker{decide: decide}, clock: clocktesting.NewFakeClock(t0), reg: obs.NewRegistry()}
	f.a = New(Options{Checker: f.checker, Kinds: kinds, AllowTTL: 30 * time.Second, Clock: f.clock, Metrics: f.reg, IdentityFrom: identityFrom,
		MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	return f, kinds
}

func mustFullRead(t *testing.T, a *Authorizer, id oidc.Identity) bool {
	t.Helper()
	ok, err := a.FullRead(request(id))
	if err != nil {
		t.Fatalf("FullRead() error = %v", err)
	}
	return ok
}

// cachedFull returns alice's cached full-read decision.
func cachedFull(f *fixture) (entry, bool) {
	return cachedFullOf(f, user("alice"))
}

// cachedFullOf returns id's cached full-read decision.
func cachedFullOf(f *fixture, id oidc.Identity) (entry, bool) {
	c := f.a.cache
	c.mu.Lock()
	defer c.mu.Unlock()
	ie := c.ids[hashIdentity(id)]
	if ie == nil || ie.full == nil {
		return entry{}, false
	}
	return *ie.full.Value.(*entry), true
}

func TestFullReadAllowed(t *testing.T) {
	var inflight, peak atomic.Int32
	f, _ := newFullFixture(20, func(id oidc.Identity, a Attributes) (bool, error) {
		n := inflight.Add(1)
		defer inflight.Add(-1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		return a.Namespace == "" && a.Verb == "list", nil
	})
	if !mustFullRead(t, f.a, user("alice")) {
		t.Fatal("a cluster-wide reader was not a full reader")
	}
	f.expectSARs(t, 20, "first full read")
	if p := peak.Load(); p > fullReadParallel {
		t.Fatalf("%d SARs at once, want at most %d", p, fullReadParallel)
	}
	f.clock.Step(29 * time.Second)
	if !mustFullRead(t, f.a, user("alice")) {
		t.Fatal("a cached full read was not true")
	}
	f.expectSARs(t, 20, "within the TTL")
	for name, want := range map[string]string{
		`snellius_authz_cache_total{result="fullread-hit"}`:  "1",
		`snellius_authz_cache_total{result="fullread-miss"}`: "1",
	} {
		if got := metricLine(t, f, name); got != want {
			t.Errorf("%s = %q, want %s", name, got, want)
		}
	}
}

// defaultTTL rebuilds f's Authorizer over kinds with the default allow TTL, one kind at a time.
func defaultTTL(f *fixture, kinds Kinds) {
	f.a = New(Options{Checker: f.checker, Kinds: kinds, Clock: f.clock, Metrics: f.reg, IdentityFrom: identityFrom,
		MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	f.a.parallel = 1
}

// A denial at t with an allow at t+1 s caches "false" until t + 5 min, not t + 61 s.
func TestFullReadFalseWitness(t *testing.T) {
	f, kinds := newFullFixture(2, func(_ oidc.Identity, a Attributes) (bool, error) { return a.Resource == "k0", nil })
	f.reg = obs.NewRegistry()
	defaultTTL(f, kinds)
	f.checkAt(t, user("alice"), clusterList("k1")) // the denial, at t
	f.clock.Step(time.Second)
	if mustFullRead(t, f.a, user("alice")) { // k0 allowed at t+1 s, k1 the cached denial
		t.Fatal("a denied kind gave a full read")
	}
	if e, ok := cachedFull(f); !ok || e.d.allowed || !e.d.expires.Equal(t0.Add(DenyTTL)) {
		t.Fatalf("cached %+v, want the denial's expiry t + 5 min", e)
	}
	f.clock.SetTime(t0.Add(DenyTTL - time.Nanosecond))
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("the cached false turned true")
	}
	f.expectSARs(t, 2, "past the allow's expiry, before the denial's")
}

// A full-read "true" is noted with its expiry, fresh and cached; "false" is not noted.
func TestFullReadNotesTrue(t *testing.T) {
	for _, all := range []bool{true, false} {
		f, _ := newFullFixture(3, func(_ oidc.Identity, a Attributes) (bool, error) { return all || a.Resource != "k1", nil })
		var noted []time.Time
		f.a.note = func(_ *http.Request, exp time.Time) { noted = append(noted, exp) }
		f.a.parallel = 1
		for range 2 {
			if got := mustFullRead(t, f.a, user("alice")); got != all {
				t.Fatalf("full read %v, want %v", got, all)
			}
		}
		var want []time.Time
		if all {
			want = []time.Time{t0.Add(30 * time.Second), t0.Add(30 * time.Second)}
		}
		if !slices.Equal(noted, want) {
			t.Errorf("all=%v: noted %v, want %v", all, noted, want)
		}
	}
}

// "True" expires at its earliest allowing constituent, a cached one included.
func TestFullReadTrueEarliestAllow(t *testing.T) {
	f, kinds := newFullFixture(2, allowAll)
	f.reg = obs.NewRegistry()
	defaultTTL(f, kinds)
	f.checkAt(t, user("alice"), clusterList("k1")) // allowed until t + 60 s
	f.clock.Step(10 * time.Second)
	if !mustFullRead(t, f.a, user("alice")) { // k0 allowed until t + 70 s
		t.Fatal("not a full reader")
	}
	if e, ok := cachedFull(f); !ok || !e.d.allowed || !e.d.expires.Equal(t0.Add(DefaultAllowTTL)) {
		t.Fatalf("cached %+v, want the earliest allow's expiry t + 60 s", e)
	}
	f.clock.SetTime(t0.Add(DefaultAllowTTL))
	mustFullRead(t, f.a, user("alice"))
	f.expectSARs(t, 3, "k1 asked again at its expiry")
}

func TestFullReadExpiresFromEarliest(t *testing.T) {
	var f *fixture
	f, _ = newFullFixture(4, func(oidc.Identity, Attributes) (bool, error) {
		f.clock.Step(time.Second) // constituents start at t, t+1 s, t+2 s, t+3 s
		return true, nil
	})
	f.a.parallel = 1
	mustFullRead(t, f.a, user("alice"))
	if e, ok := cachedFull(f); !ok || !e.d.expires.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("cached %+v, want expiry at the earliest start + TTL", e)
	}
	f.clock.SetTime(t0.Add(29 * time.Second))
	mustFullRead(t, f.a, user("alice"))
	f.expectSARs(t, 4, "before the earliest expiry")
	f.clock.SetTime(t0.Add(30 * time.Second))
	mustFullRead(t, f.a, user("alice"))
	if n := f.checker.count(); n == 4 {
		t.Fatal("no SAR at the earliest constituent's expiry")
	}
}

func TestFullReadLazy(t *testing.T) {
	f, _ := newFullFixture(3, allowAll)
	swept := make(chan struct{})
	f.a.swept = func() { swept <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticker := f.clock.NewTicker(sweepEvery)
	go f.a.run(ctx, ticker.C())

	mustFullRead(t, f.a, user("alice"))
	f.clock.Step(sweepEvery) // past the 30 s expiry
	<-swept
	f.expectSARs(t, 3, "past expiry with no request")
	if _, ok := cachedFull(f); ok {
		t.Fatal("the sweep kept an expired full read")
	}
	mustFullRead(t, f.a, user("alice"))
	f.expectSARs(t, 6, "the next request")
}

func TestFullReadDeniedStopsEarly(t *testing.T) {
	started, release := make(chan struct{}, 32), make(chan struct{})
	f, _ := newFullFixture(20, func(_ oidc.Identity, a Attributes) (bool, error) {
		if a.Resource == "k0" {
			// Deny once every other worker holds a kind, so exactly one batch runs.
			for range fullReadParallel - 1 {
				<-started
			}
			return false, nil
		}
		started <- struct{}{}
		<-release
		return true, nil
	})
	f.a.fullStopped = func() { close(release) }
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("a denied kind gave a full read")
	}
	f.expectSARs(t, fullReadParallel, "stopped at the denial")
	if e, ok := cachedFull(f); !ok || e.d.allowed || !e.d.expires.Equal(t0.Add(DenyTTL)) {
		t.Fatalf("cached %+v, want a deny until start + the deny TTL", e)
	}
	f.clock.Step(29 * time.Second)
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("the cached deny turned into a full read")
	}
	f.expectSARs(t, fullReadParallel, "a cached deny")
}

func TestFullReadErrorFails(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	f, _ := newFullFixture(3, func(_ oidc.Identity, a Attributes) (bool, error) {
		if a.Resource == "k1" && fail.Load() {
			return false, checkError(categoryTimeout)
		}
		return true, nil
	})
	if ok, err := f.a.FullRead(request(user("alice"))); ok || !errors.Is(err, ErrCheck) {
		t.Fatalf("FullRead() = %v, %v; want an ErrCheck, never true", ok, err)
	}
	if _, ok := cachedFull(f); ok {
		t.Fatal("an error was cached")
	}
	fail.Store(false)
	if !mustFullRead(t, f.a, user("alice")) {
		t.Fatal("not a full reader once the error cleared")
	}
	k1 := 0
	for _, c := range f.checker.recorded() {
		if c.a == clusterList("k1") {
			k1++
		}
	}
	if k1 != 2 {
		t.Fatalf("k1 asked %d times, want 2 (the error is not cached)", k1)
	}
}

func TestFullReadRevoked(t *testing.T) {
	var granted atomic.Bool
	granted.Store(true)
	f, _ := newFullFixture(3, func(oidc.Identity, Attributes) (bool, error) { return granted.Load(), nil })
	mustFullRead(t, f.a, user("alice"))
	f.clock.Step(time.Second)
	granted.Store(false)
	for range 28 {
		if !mustFullRead(t, f.a, user("alice")) {
			t.Fatalf("not a full reader at %v, before start + TTL", f.clock.Now().Sub(t0))
		}
		f.clock.Step(time.Second)
	}
	f.clock.Step(time.Second)
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("still a full reader at start + TTL after the revoke")
	}
}

func TestFullReadKindsChange(t *testing.T) {
	f, kinds := newFullFixture(3, grants(
		call{"alice", clusterList("k0")}, call{"alice", clusterList("k1")}, call{"alice", clusterList("k2")},
	))
	mustFullRead(t, f.a, user("alice"))
	kinds.set("v2", kindsN(4)...) // a CRD served that alice cannot list
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("a new kind kept the old full read")
	}
	f.expectSARs(t, 4, "only the new kind asked")
	kinds.set("v3", kindsN(3)...) // the CRD removed
	if !mustFullRead(t, f.a, user("alice")) {
		t.Fatal("not a full reader once the kind was removed")
	}
	if got := metricLine(t, f, `snellius_authz_cache_total{result="fullread-miss"}`); got != "3" {
		t.Fatalf("fullread-miss = %s, want 3 (each key change is a miss)", got)
	}
}

func TestFullReadMerged(t *testing.T) {
	started, release, joined := make(chan string, 8), make(chan struct{}), make(chan struct{}, 64)
	f, _ := newFullFixture(3, blocking(started, release))
	f.a.full.joined = func() { joined <- struct{}{} }
	var wg sync.WaitGroup
	results := make(chan bool, 50)
	for range 50 {
		wg.Go(func() {
			ok, err := f.a.FullRead(request(user("alice")))
			results <- ok && err == nil
		})
	}
	for range 3 {
		<-started
	}
	for range 49 {
		<-joined
	}
	close(release)
	wg.Wait()
	close(results)
	for ok := range results {
		if !ok {
			t.Fatal("a merged full read was not true")
		}
	}
	f.expectSARs(t, 3, "50 merged full reads")
}

func TestFullReadNearExpiryHit(t *testing.T) {
	var f *fixture
	var slow, granted atomic.Bool
	slow.Store(true)
	granted.Store(true)
	f, _ = newFullFixture(2, func(_ oidc.Identity, a Attributes) (bool, error) {
		if a.Resource == "k1" && slow.Swap(false) {
			f.clock.Step(2 * time.Second)
		}
		return a.Resource == "k1" || granted.Load(), nil
	})
	f.a.parallel = 1
	f.checkAt(t, user("alice"), clusterList("k0")) // allowed until t+30 s
	granted.Store(false)                           // a fresh k0 SAR now denies
	f.clock.SetTime(t0.Add(29 * time.Second))
	// k0 is a hit until t+30 s; k1's SAR starts at t+29 s and completes at t+31 s.
	if mustFullRead(t, f.a, user("alice")) {
		t.Fatal("returned the expired aggregate")
	}
	f.expectSARs(t, 3, "k0 cached, k1, then k0 again")
	// The re-evaluation's k0 denial, from t+31 s, is the "false" witness.
	if e, ok := cachedFull(f); !ok || !e.d.expires.Equal(t0.Add(31*time.Second+DenyTTL)) {
		t.Fatalf("cached %+v, want the re-evaluation's denial expiry", e)
	}
}

func TestFullReadExpiredTwice(t *testing.T) {
	var f *fixture
	f, _ = newFullFixture(2, func(_ oidc.Identity, a Attributes) (bool, error) {
		step := time.Second
		if a.Resource == "k1" {
			step = 29 * time.Second
		}
		f.clock.Step(step)
		return true, nil
	})
	f.a.parallel = 1
	f.checkAt(t, user("alice"), clusterList("k0")) // t..t+1 s, allowed until t+30 s
	// First: k0 hit, k1 from t+1 s to t+30 s. Second: k0 from t+30 s, k1 from t+31 s to t+60 s.
	ok, err := f.a.FullRead(request(user("alice")))
	if ok || !errors.Is(err, ErrExpired) {
		t.Fatalf("FullRead() = %v, %v; want ErrExpired", ok, err)
	}
	f.expectSARs(t, 4, "two evaluations")
	if _, ok := cachedFull(f); ok {
		t.Fatal("an expired aggregate was cached")
	}
}

func TestFullReadMergedWaiterExpiry(t *testing.T) {
	type result struct {
		ok  bool
		err error
	}
	// run starts an owner and a merged waiter; the waiter wakes at the
	// aggregate's expiry, when the next SARs answer with second.
	run := func(second func() (bool, error)) (owner, waiter result) {
		started, release, joined := make(chan string, 8), make(chan struct{}), make(chan struct{}, 4)
		var after atomic.Bool
		f, _ := newFullFixture(3, func(id oidc.Identity, _ Attributes) (bool, error) {
			if after.Load() {
				return second()
			}
			started <- id.Username
			<-release
			return true, nil
		})
		f.a.full.joined = func() { joined <- struct{}{} }
		f.a.full.woke = func() {
			after.Store(true)
			f.clock.SetTime(t0.Add(30 * time.Second))
		}
		call := func() <-chan result {
			out := make(chan result, 1)
			go func() {
				ok, err := f.a.FullRead(request(user("alice")))
				out <- result{ok, err}
			}()
			return out
		}
		o := call()
		for range 3 {
			<-started
		}
		w := call()
		<-joined
		close(release)
		return <-o, <-w
	}
	t.Run("re-evaluated decision", func(t *testing.T) {
		owner, waiter := run(func() (bool, error) { return false, nil })
		if waiter.ok || waiter.err != nil {
			t.Fatalf("waiter got %+v, want the re-evaluated deny", waiter)
		}
		if owner.err != nil {
			t.Fatalf("owner error = %v", owner.err)
		}
	})
	t.Run("re-evaluation error", func(t *testing.T) {
		owner, waiter := run(func() (bool, error) { return false, checkError(categoryRequest) })
		if waiter.ok || !errors.Is(waiter.err, ErrCheck) {
			t.Fatalf("waiter got %+v, want the re-evaluation's error", waiter)
		}
		if !owner.ok && !errors.Is(owner.err, ErrCheck) {
			t.Fatalf("owner got %+v, want the decision from before expiry or the error", owner)
		}
	})
}
