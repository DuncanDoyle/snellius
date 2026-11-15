package sar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

func allowAll(oidc.Identity, Attributes) (bool, error) { return true, nil }

// checkAt runs one cache check for id and returns its decision.
func (f *fixture) checkAt(t *testing.T, id oidc.Identity, a Attributes) decision {
	t.Helper()
	d, err := f.a.check(request(id), id, hashIdentity(id), a)
	if err != nil {
		t.Fatalf("check(%+v) error = %v", a, err)
	}
	return d
}

// expectSARs fails unless the checker has been asked exactly n times.
func (f *fixture) expectSARs(t *testing.T, n int, when string) {
	t.Helper()
	if got := f.checker.count(); got != n {
		t.Fatalf("%s: %d SARs, want %d", when, got, n)
	}
}

func TestTTLAnchoredAtStart(t *testing.T) {
	var f *fixture
	f = newFixture(func(oidc.Identity, Attributes) (bool, error) {
		f.clock.Step(5 * time.Second) // the SAR takes 5 s
		return true, nil
	})
	if d := f.checkAt(t, user("alice"), listAt("shop")); !d.expires.Equal(t0.Add(30 * time.Second)) {
		t.Fatalf("expires = %v, want start + TTL", d.expires)
	}
	f.clock.Step(24 * time.Second)
	f.checkAt(t, user("alice"), listAt("shop"))
	f.expectSARs(t, 1, "24 s after it returned")
	f.clock.Step(time.Second)
	f.checkAt(t, user("alice"), listAt("shop"))
	f.expectSARs(t, 2, "25 s after it returned")
}

func TestNoExtensionPastExpiry(t *testing.T) {
	f := newFixture(allowAll)
	f.checkAt(t, user("alice"), listAt("shop"))
	for range 29 {
		f.clock.Step(time.Second)
		if d := f.checkAt(t, user("alice"), listAt("shop")); !d.expires.Equal(t0.Add(30 * time.Second)) {
			t.Fatalf("a hit moved expires to %v", d.expires)
		}
	}
	f.expectSARs(t, 1, "hits every second")
	f.clock.Step(time.Second)
	if d := f.checkAt(t, user("alice"), listAt("shop")); !d.expires.Equal(t0.Add(60 * time.Second)) {
		t.Fatalf("new SAR expires = %v, want its own start + TTL", d.expires)
	}
	f.expectSARs(t, 2, "at start + TTL")
}

func TestRevokedAfterTTL(t *testing.T) {
	granted := true
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) { return granted, nil })
	if !mustAuthorize(t, f.a, user("alice"), route("shop", "web")) {
		t.Fatal("denied while granted")
	}
	f.clock.Step(time.Second)
	granted = false
	for range 28 {
		if !mustAuthorize(t, f.a, user("alice"), route("shop", "web")) {
			t.Fatalf("denied at %v, before start + TTL", f.clock.Now().Sub(t0))
		}
		f.clock.Step(time.Second)
	}
	f.clock.Step(time.Second)
	if mustAuthorize(t, f.a, user("alice"), route("shop", "web")) {
		t.Fatal("still allowed at start + TTL after the revoke")
	}
}

func TestIdentitiesNeverShare(t *testing.T) {
	f := newFixture(allowAll)
	base := oidc.Identity{Username: "alice", UID: "u1", Groups: []string{"dev"}, Extra: map[string][]string{"k": {"v"}}}
	variants := []oidc.Identity{
		base,
		{Username: "alice", UID: "u1", Groups: []string{"dev", "ops"}, Extra: base.Extra},
		{Username: "alice", UID: "u1", Groups: base.Groups, Extra: map[string][]string{"k": {"w"}}},
		{Username: "alice", UID: "u1", Groups: base.Groups, Extra: map[string][]string{"kv": {""}}},
		{Username: "alice", UID: "u2", Groups: base.Groups, Extra: base.Extra},
		{Username: "bob", Groups: []string{"a", "bc"}},
		{Username: "bob", Groups: []string{"ab", "c"}},
	}
	for i, id := range variants {
		f.checkAt(t, id, listAt("shop"))
		f.expectSARs(t, i+1, fmt.Sprintf("identity %d", i))
	}
	// The same identity with groups and extra values in another order shares.
	f.checkAt(t, oidc.Identity{Username: "bob", Groups: []string{"c", "ab"}}, listAt("shop"))
	f.expectSARs(t, len(variants), "reordered groups")
}

// blocking is a checker that reports each SAR on started and answers once release closes.
func blocking(started chan<- string, release <-chan struct{}) func(oidc.Identity, Attributes) (bool, error) {
	return func(id oidc.Identity, _ Attributes) (bool, error) {
		started <- id.Username
		<-release
		return true, nil
	}
}

func TestMergedChecks(t *testing.T) {
	started, release, joined := make(chan string, 4), make(chan struct{}), make(chan struct{}, 64)
	f := newFixture(blocking(started, release))
	f.a.cache.joined = func() { joined <- struct{}{} }
	var spends int
	var mu sync.Mutex
	f.a.spend = func(*http.Request) error { mu.Lock(); spends++; mu.Unlock(); return nil }

	var wg sync.WaitGroup
	results := make(chan bool, 51)
	run := func(id oidc.Identity) {
		wg.Go(func() {
			ok, err := f.a.AuthorizeErr(request(id), route("shop", "web"))
			results <- ok && err == nil
		})
	}
	for range 50 {
		run(user("alice"))
	}
	if who := <-started; who != "alice" {
		t.Fatalf("first SAR for %s", who)
	}
	for range 49 {
		<-joined
	}
	run(user("bob"))
	if who := <-started; who != "bob" {
		t.Fatalf("second SAR for %s, want bob's own", who)
	}
	close(release)
	wg.Wait()
	close(results)
	for ok := range results {
		if !ok {
			t.Fatal("a merged check was not allowed")
		}
	}
	f.expectSARs(t, 2, "50 merged checks and one other identity")
	if spends != 51 {
		t.Errorf("SpendCheck called %d times, want 51 (one per miss, merged waits included)", spends)
	}
}

func TestMergedWaiterAnchor(t *testing.T) {
	run := func(f *fixture, id oidc.Identity) <-chan decision {
		out := make(chan decision, 1)
		go func() {
			d, err := f.a.check(request(id), id, hashIdentity(id), listAt("shop"))
			if err != nil {
				d = decision{}
			}
			out <- d
		}()
		return out
	}
	t.Run("waiter gets the flight's expiry", func(t *testing.T) {
		started, release, joined := make(chan string, 4), make(chan struct{}), make(chan struct{}, 4)
		f := newFixture(blocking(started, release))
		f.a.cache.joined = func() { joined <- struct{}{} }
		owner := run(f, user("alice"))
		<-started
		f.clock.Step(9 * time.Second)
		waiter := run(f, user("alice"))
		<-joined
		close(release)
		want := t0.Add(30 * time.Second)
		for name, ch := range map[string]<-chan decision{"owner": owner, "waiter": waiter} {
			if d := <-ch; !d.allowed || !d.expires.Equal(want) {
				t.Errorf("%s got %+v, want allowed until start + TTL", name, d)
			}
		}
		f.expectSARs(t, 1, "merged")
	})
	t.Run("waiter past the expiry checks again", func(t *testing.T) {
		started, release, joined := make(chan string, 4), make(chan struct{}), make(chan struct{}, 4)
		f := newFixture(blocking(started, release))
		f.a.cache.joined = func() { joined <- struct{}{} }
		f.a.cache.woke = func() { f.clock.SetTime(t0.Add(30 * time.Second)) }
		owner := run(f, user("alice"))
		<-started
		f.clock.Step(9 * time.Second)
		waiter := run(f, user("alice"))
		<-joined
		close(release)
		if d := <-owner; !d.expires.Equal(t0.Add(30 * time.Second)) {
			t.Errorf("owner got %+v", d)
		}
		if d := <-waiter; !d.allowed || !d.expires.Equal(t0.Add(60*time.Second)) {
			t.Errorf("waiter got %+v, want a fresh SAR's decision", d)
		}
		<-started
		f.expectSARs(t, 2, "waiter woke past expiry")
	})
}

func TestLateResultDiscarded(t *testing.T) {
	var f *fixture
	late := true
	f = newFixture(func(oidc.Identity, Attributes) (bool, error) {
		if late {
			f.clock.Step(30 * time.Second)
		}
		return true, nil
	})
	ok, err := f.a.AuthorizeErr(request(user("alice")), route("shop", "web"))
	if ok || !errors.Is(err, ErrCheck) || !strings.Contains(err.Error(), categoryLate) {
		t.Fatalf("AuthorizeErr() = %v, %v; want a late-result error, never allow", ok, err)
	}
	late = false
	f.checkAt(t, user("alice"), listAt("shop"))
	f.expectSARs(t, 2, "after a late result")
}

func TestErrorsNotCached(t *testing.T) {
	fail := true
	f := newFixture(func(oidc.Identity, Attributes) (bool, error) {
		if fail {
			return false, checkError(categoryTimeout)
		}
		return true, nil
	})
	if _, err := f.a.AuthorizeErr(request(user("alice")), route("shop", "web")); !errors.Is(err, ErrCheck) {
		t.Fatalf("error = %v, want ErrCheck", err)
	}
	fail = false
	if !mustAuthorize(t, f.a, user("alice"), route("shop", "web")) {
		t.Fatal("denied after the error cleared")
	}
	f.expectSARs(t, 2, "after an error")
}

func TestPerIdentityLRU(t *testing.T) {
	f := newFixture(allowAll)
	name := func(i int) Attributes { return getAt("shop", fmt.Sprintf("r%d", i)) }
	for i := range maxEntriesPerIdentity {
		f.checkAt(t, user("alice"), name(i))
	}
	f.checkAt(t, user("alice"), name(0)) // r0 becomes the most recently used
	f.checkAt(t, user("alice"), name(maxEntriesPerIdentity))
	n := maxEntriesPerIdentity + 1
	f.expectSARs(t, n, "filled")
	f.checkAt(t, user("alice"), name(0))
	f.expectSARs(t, n, "r0 was used last, so kept")
	f.checkAt(t, user("alice"), name(1))
	f.expectSARs(t, n+1, "r1 was least recently used, so evicted")
}

func TestIdentityBound(t *testing.T) {
	f := newFixture(allowAll)
	id := func(i int) oidc.Identity { return user(fmt.Sprintf("u%d", i)) }
	for i := range maxIdentities {
		f.checkAt(t, id(i), listAt("shop"))
	}
	f.checkAt(t, id(0), listAt("shop"))
	f.checkAt(t, id(maxIdentities), listAt("shop"))
	n := maxIdentities + 1
	f.expectSARs(t, n, "filled")
	if len(f.a.cache.ids) != maxIdentities {
		t.Fatalf("%d identities cached, want %d", len(f.a.cache.ids), maxIdentities)
	}
	f.checkAt(t, id(0), listAt("shop"))
	f.expectSARs(t, n, "u0 was used last, so kept")
	f.checkAt(t, id(1), listAt("shop"))
	f.expectSARs(t, n+1, "u1 was least recently used, so dropped")
}

func TestSweep(t *testing.T) {
	f := newFixture(allowAll)
	swept := make(chan struct{})
	f.a.swept = func() { swept <- struct{}{} }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticker := f.clock.NewTicker(sweepEvery)
	go f.a.run(ctx, ticker.C())

	f.checkAt(t, user("alice"), listAt("shop")) // expires at 30 s
	f.clock.Step(40 * time.Second)
	f.checkAt(t, user("bob"), listAt("shop")) // expires at 70 s
	f.checkAt(t, user("bob"), listAt("ops"))
	gauge := func() string { return metricLine(t, f, "snellius_authz_identities") }
	if g := gauge(); g != "2" {
		t.Fatalf("gauge = %s before the sweep, want 2", g)
	}
	f.clock.Step(20 * time.Second) // the first tick, at 60 s
	<-swept
	if _, ok := f.a.cache.ids[hashIdentity(user("alice"))]; ok || len(f.a.cache.ids) != 1 || gauge() != "1" {
		t.Fatalf("after the first sweep: %d identities, gauge %s; want only bob", len(f.a.cache.ids), gauge())
	}
	f.clock.Step(sweepEvery)
	<-swept
	if len(f.a.cache.ids) != 0 || gauge() != "0" {
		t.Fatalf("after the second sweep: %d identities, gauge %s; want none", len(f.a.cache.ids), gauge())
	}
}

func TestSpendOnMissOnly(t *testing.T) {
	f := newFixture(grants(call{"alice", listAt("shop")}))
	spends := 0
	f.a.spend = func(*http.Request) error { spends++; return nil }
	mustAuthorize(t, f.a, user("alice"), route("shop", "web"))
	mustAuthorize(t, f.a, user("alice"), route("shop", "web"))
	if spends != 1 {
		t.Fatalf("SpendCheck called %d times for a miss then a hit, want 1", spends)
	}
	f.a.spend = func(*http.Request) error { return api.ErrCheckLimit }
	if _, err := f.a.AuthorizeErr(request(user("alice")), route("ops", "web")); !errors.Is(err, api.ErrCheckLimit) {
		t.Fatalf("error past the limit = %v, want ErrCheckLimit", err)
	}
	f.expectSARs(t, 1, "past the limit")
}

func TestMetrics(t *testing.T) {
	var f *fixture
	f = newFixture(func(id oidc.Identity, a Attributes) (bool, error) {
		f.clock.Step(2 * time.Second)
		switch {
		case id.Username == "carol":
			return false, checkError(categoryRequest)
		case a.Verb == "list" && id.Username == "alice":
			return true, nil
		}
		return false, nil
	})
	mustAuthorize(t, f.a, user("alice"), route("shop", "web"))
	mustAuthorize(t, f.a, user("alice"), route("shop", "web"))
	mustAuthorize(t, f.a, user("bob"), route("shop", "web"))
	_, _ = f.a.AuthorizeErr(request(user("carol")), route("shop", "web"))
	for name, want := range map[string]string{
		`snellius_authz_checks_total{verb="list",outcome="allowed"}`: "1",
		`snellius_authz_checks_total{verb="list",outcome="denied"}`:  "1",
		`snellius_authz_checks_total{verb="get",outcome="denied"}`:   "1",
		`snellius_authz_checks_total{verb="list",outcome="error"}`:   "1",
		`snellius_authz_check_seconds_total`:                         "8",
		`snellius_authz_cache_total{result="hit"}`:                   "1",
		`snellius_authz_cache_total{result="miss"}`:                  "4",
		`snellius_authz_identities`:                                  "2",
	} {
		if got := metricLine(t, f, name); got != want {
			t.Errorf("%s = %q, want %s", name, got, want)
		}
	}
}

// metricLine returns the value of the series named exactly name in the exposition.
func metricLine(t *testing.T, f *fixture, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.reg.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for line := range strings.SplitSeq(rec.Body.String(), "\n") {
		if v, ok := strings.CutPrefix(line, name+" "); ok {
			return v
		}
	}
	return ""
}
