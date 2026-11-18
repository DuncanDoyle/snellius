package sar

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"

	clocktesting "k8s.io/utils/clock/testing"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// attrsThatConcatAlike are Attributes whose fields join to the same text.
var attrsThatConcatAlike = []Attributes{
	{Verb: "get", Group: gwGroup, Resource: "httproutes", Namespace: "ab", Name: "c"},
	{Verb: "get", Group: gwGroup, Resource: "httproutes", Namespace: "a", Name: "bc"},
	{Verb: "get", Group: gwGroup, Resource: "httproutes", Namespace: "abc"},
	{Verb: "get", Group: gwGroup, Resource: "httproutes", Name: "abc"},
	{Verb: "get", Group: gwGroup, Resource: "httproutes\x01a", Namespace: "bc"},
	{Verb: "get", Group: gwGroup + "httproutes", Namespace: "abc"},
	{Verb: "get" + gwGroup, Resource: "httproutes", Namespace: "abc"},
}

func TestAttrsEncodeApart(t *testing.T) {
	seen := map[string]int{}
	for i, a := range attrsThatConcatAlike {
		k := string(appendAttrs(nil, a))
		if j, ok := seen[k]; ok {
			t.Fatalf("attributes %d and %d encode alike: %+v, %+v", j, i, attrsThatConcatAlike[j], a)
		}
		seen[k] = i
	}
}

// Each cached answer belongs to its identity and its exact attributes.
func TestAnswersKeptApart(t *testing.T) {
	f := newFixture(func(id oidc.Identity, a Attributes) (bool, error) {
		return (id.Username == "bob") != (a.Name == "bc"), nil
	})
	want := func(id oidc.Identity, a Attributes) bool { return (id.Username == "bob") != (a.Name == "bc") }
	alice, bob := user("alice"), user("bob")
	for range 2 {
		for _, id := range []oidc.Identity{alice, bob} {
			for _, a := range attrsThatConcatAlike {
				if got := f.checkAt(t, id, a).allowed; got != want(id, a) {
					t.Fatalf("%s %+v allowed = %v", id.Username, a, got)
				}
			}
		}
	}
	f.expectSARs(t, 2*len(attrsThatConcatAlike), "second round from the cache")
}

// An identity whose entries were all evicted asks again; another identity's answers never stand in.
func TestIdentityReturnsAfterEviction(t *testing.T) {
	f := newFixture(func(id oidc.Identity, _ Attributes) (bool, error) { return id.Username == "bob", nil })
	alice, bob := user("alice"), user("bob")
	f.checkAt(t, alice, listAt("shop"))
	f.checkAt(t, bob, listAt("shop"))
	// Room for nothing more, so bob's next entry evicts alice's only one and her identity.
	setMax(f.a.cache, func(n int64) int64 { return n })
	f.checkAt(t, bob, listAt("ops"))
	f.a.cache.mu.Lock()
	_, kept := f.a.cache.ids[hashIdentity(alice)]
	f.a.cache.mu.Unlock()
	if kept {
		t.Fatal("alice's identity kept after her last entry was evicted")
	}
	if f.checkAt(t, alice, listAt("shop")).allowed {
		t.Fatal("alice allowed after her identity was evicted")
	}
	f.expectSARs(t, 4, "alice asked again")
}

// 50 viewers with 500 checks each, the largest page's load, fit the default budget.
func TestFiftyViewersFit(t *testing.T) {
	const viewers, checks = 50, 500
	var sars atomic.Int64
	allow := checkerFunc(func(context.Context, oidc.Identity, Attributes) (bool, error) { sars.Add(1); return true, nil })
	a := New(Options{Checker: allow, Kinds: testKinds, Clock: clocktesting.NewFakeClock(t0), Metrics: obs.NewRegistry(),
		IdentityFrom: identityFrom, MaxInFlight: unlimited, Rate: unlimited, Burst: unlimited})
	ids := make([]oidc.Identity, viewers)
	for v := range ids {
		ids[v] = user(fmt.Sprintf("viewer-%02d@example.com", v))
	}
	attrs := make([]Attributes, checks)
	for i := range attrs {
		attrs[i] = getAt(fmt.Sprintf("ns-%03d", i%100), fmt.Sprintf("backend-%05d", i))
	}
	before := heapNow()
	for round := range 2 {
		for _, id := range ids {
			h := hashIdentity(id)
			for _, at := range attrs {
				if _, err := a.check(request(id), id, h, at); err != nil {
					t.Fatal(err)
				}
			}
		}
		if n := sars.Load(); n != viewers*checks {
			t.Fatalf("round %d: %d SARs, want %d: answers were evicted", round+1, n, viewers*checks)
		}
	}
	heap := heapNow() - before
	c := a.cache
	c.mu.Lock()
	acc := c.accounted()
	c.mu.Unlock()
	runtime.KeepAlive(a)
	t.Logf("%d entries: accounted %d B (%.0f B each, %.0f%% of the budget), heap %d B (%.2f accounted/heap)",
		viewers*checks, acc, float64(acc)/(viewers*checks), 100*float64(acc)/DefaultMaxBytes, heap, float64(acc)/float64(heap))
	if r := float64(acc) / float64(heap); r < 0.9 || r > 1.25 {
		t.Fatalf("accounted %d B is %.2f× the heap delta %d B, want 0.9–1.25", acc, r, heap)
	}
}
