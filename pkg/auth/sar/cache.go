package sar

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// DefaultMaxBytes is the permission cache's byte budget when Options.MaxBytes is zero (D-100).
const DefaultMaxBytes = 8 << 20

// maxFlights bounds the SARs and full-read evaluations in flight across identities.
const maxFlights = 4096

// ErrOverloaded is the error of a check that would start a flight past maxFlights.
var ErrOverloaded = fmt.Errorf("%w: overloaded", ErrCheck)

// decision is a SAR answer valid until expires, the SAR's start plus the
// answer's TTL.
type decision struct {
	allowed bool
	expires time.Time
}

type flightKey struct {
	id    idHash
	attrs Attributes
}

// flight is one SAR in progress; done closes once d and err are set.
// retry marks a leader whose context ended before admission, so nothing was sent
// for it (for a full read, before its evaluation finished): a waiter leads again.
type flight struct {
	done  chan struct{}
	d     decision
	err   error
	retry bool
}

// idEntry is one identity's metadata: how many entries it has and its full-read entry.
type idEntry struct {
	n    int
	full *list.Element
}

// entry is a cached decision, or with full set the identity's full-read decision for kinds.
type entry struct {
	key   flightKey
	kinds string
	full  bool
	d     decision
}

// cache holds decisions and full-read decisions under one byte budget with
// one LRU across identities, and merges identical concurrent checks (design
// §6.2, D-100). An allow lives allowTTL, a deny up to DenyTTL; an expiry never moves.
// An evicted entry is a miss.
type cache struct {
	checker Checker
	// lim admits every SAR the cache sends (D-100 item 6).
	lim      *limiter
	clock    clock.PassiveClock
	allowTTL time.Duration
	// rand returns a value in [0, 1); tests replace it.
	rand func() float64
	m    *metrics
	log  *slog.Logger

	mu        sync.Mutex
	decisions map[flightKey]*list.Element
	ids       map[idHash]*idEntry
	lru       *list.List // decisions and full-read entries, most recently used at the front
	// used is entries and identities; each map is charged at its peak live count
	// and counts its deletes since it was last replaced.
	used, max        int64
	decPeak, idsPeak int
	decDels, idsDels int

	flights     map[flightKey]*flight
	fullFlights map[fullKey]*flight

	// Test hooks, nil in production: a waiter joined a flight; a waiter's flight ended.
	joined, woke func()
}

// denyJitter is the default source of a deny's jitter; tests replace it.
var denyJitter = rand.Float64

func newCache(c Checker, lim *limiter, clk clock.PassiveClock, allowTTL time.Duration, maxBytes int64, m *metrics) *cache {
	return &cache{
		checker: c, lim: lim, clock: clk, allowTTL: allowTTL, m: m, log: slog.Default(), max: maxBytes, rand: denyJitter,
		decisions: map[flightKey]*list.Element{}, ids: map[idHash]*idEntry{}, lru: list.New(),
		flights: map[flightKey]*flight{}, fullFlights: map[fullKey]*flight{},
	}
}

// ttl is the lifetime of an answer: allowTTL for an allow, a deny's is uniform in [MinDenyTTL, DenyTTL].
func (c *cache) ttl(allowed bool) time.Duration {
	if allowed {
		return c.allowTTL
	}
	return MinDenyTTL + time.Duration(c.rand()*float64(DenyTTL-MinDenyTTL))
}

// check answers a for the identity from the cache, a running flight or a new
// SAR. Every miss calls spend first with why it missed (an api.Miss reason);
// a hit does not. A waiter whose leader ended before admission leads a new
// attempt without spending again.
func (c *cache) check(ctx context.Context, spend func(reason string) error, id oidc.Identity, h idHash, a Attributes) (decision, error) {
	k := flightKey{h, a}
	requeued := false
	for {
		c.mu.Lock()
		d, ok, reason := c.lookup(h, a, c.clock.Now())
		if ok {
			c.mu.Unlock()
			c.m.cache.Inc("hit")
			return d, nil
		}
		if !requeued {
			if err := spend(reason); err != nil {
				c.mu.Unlock()
				return decision{}, err
			}
		}
		if f := c.flights[k]; f != nil {
			c.mu.Unlock()
			c.m.cache.Inc("merged")
			if c.joined != nil {
				c.joined()
			}
			select {
			case <-f.done:
			case <-ctx.Done():
				return decision{}, ctx.Err()
			}
			if f.retry {
				requeued = true
				continue
			}
			if f.err != nil {
				return decision{}, f.err
			}
			if c.woke != nil {
				c.woke()
			}
			// A result past its expiry by now is a miss, never served.
			if c.clock.Now().Before(f.d.expires) {
				return f.d, nil
			}
			continue
		}
		if c.flying() >= maxFlights {
			c.mu.Unlock()
			return decision{}, ErrOverloaded
		}
		f := &flight{done: make(chan struct{})}
		c.flights[k] = f
		c.mu.Unlock()
		c.m.cache.Inc("miss")
		return c.fly(ctx, f, k, id, a)
	}
}

// fly admits f's SAR through the limiter under ctx, then runs it, and
// completes f even when the Checker panics: waiters get an error and the
// panic continues to the request's own recovery. A ctx that ends before
// admission sends nothing and lets a waiter retry (plan-7.3b #6).
func (c *cache) fly(ctx context.Context, f *flight, k flightKey, id oidc.Identity, a Attributes) (decision, error) {
	sent := false
	defer func() {
		if !sent {
			f.d, f.err = decision{}, checkError(categoryPanic)
		}
		c.mu.Lock()
		delete(c.flights, k)
		if f.err == nil {
			c.store(k, f.d)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	release, err := c.lim.acquire(ctx)
	if err != nil {
		f.err, f.retry, sent = err, ctx.Err() != nil, true
		return decision{}, err
	}
	defer release()
	f.d, f.err = c.send(ctx, id, a)
	sent = true
	return f.d, f.err
}

// send runs one admitted SAR, anchoring its expiry at the start with the answer's TTL.
// A result already expired when it returns is an error. Waiters keep the SAR
// running when the request that started it goes away, but never past allowTTL.
func (c *cache) send(ctx context.Context, id oidc.Identity, a Attributes) (decision, error) {
	start := c.clock.Now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.allowTTL)
	defer cancel()
	allowed, err := c.checker.Check(ctx, id, a)
	if err != nil && !errors.Is(err, ErrCheck) && ctx.Err() != nil {
		err = checkError(categoryTimeout)
	}
	end := c.clock.Now()
	d := decision{allowed: allowed, expires: start.Add(c.ttl(allowed))}
	if err == nil && !d.expires.After(end) {
		c.log.Warn("authorisation check failed", "category", categoryLate, "verb", a.Verb, "group", a.Group, "resource", a.Resource)
		err = checkError(categoryLate)
	}
	c.m.checked(a.Verb, allowed, err, end.Sub(start))
	if err != nil {
		return decision{}, err
	}
	return d, nil
}

// flying is the SARs and full-read evaluations in flight.
func (c *cache) flying() int { return len(c.flights) + len(c.fullFlights) }

// lookup returns an unexpired decision and marks it used; an expired one is
// dropped. A miss says why, as an api.Miss reason.
func (c *cache) lookup(h idHash, a Attributes, now time.Time) (decision, bool, string) {
	el := c.decisions[flightKey{h, a}]
	if el == nil {
		return decision{}, false, api.MissAbsent
	}
	e := el.Value.(*entry)
	if !now.Before(e.d.expires) {
		c.remove(el)
		c.report()
		if e.d.allowed {
			return decision{}, false, api.MissAllowExpired
		}
		return decision{}, false, api.MissDenyExpired
	}
	c.lru.MoveToFront(el)
	return e.d, true, ""
}

// lookupFull returns the identity's unexpired full-read decision for k's kinds
// and marks it used; an expired one is dropped.
func (c *cache) lookupFull(k fullKey, now time.Time) (decision, bool) {
	ie := c.ids[k.id]
	if ie == nil || ie.full == nil {
		return decision{}, false
	}
	e := ie.full.Value.(*entry)
	if !now.Before(e.d.expires) {
		c.remove(ie.full)
		c.report()
		return decision{}, false
	}
	if e.kinds != k.kinds {
		return decision{}, false
	}
	c.lru.MoveToFront(ie.full)
	return e.d, true
}

// store keeps d for k as the most recently used entry.
func (c *cache) store(k flightKey, d decision) {
	if el := c.decisions[k]; el != nil {
		el.Value.(*entry).d = d
		c.lru.MoveToFront(el)
		return
	}
	el := c.insert(&entry{key: k, d: d})
	c.decisions[k] = el
	c.decPeak = max(c.decPeak, len(c.decisions))
	c.evict(el)
}

// storeFull keeps d as the identity's full-read decision for k's kinds,
// replacing its decision for any other kinds.
func (c *cache) storeFull(k fullKey, d decision) {
	if ie := c.ids[k.id]; ie != nil && ie.full != nil {
		e := ie.full.Value.(*entry)
		c.used += int64(len(k.kinds) - len(e.kinds))
		e.kinds, e.d = k.kinds, d
		c.lru.MoveToFront(ie.full)
		c.evict(ie.full)
		return
	}
	el := c.insert(&entry{key: flightKey{id: k.id}, kinds: k.kinds, full: true, d: d})
	c.ids[k.id].full = el
	c.evict(el)
}

// insert puts e at the front of the LRU, adding its identity when new.
func (c *cache) insert(e *entry) *list.Element {
	ie := c.ids[e.key.id]
	if ie == nil {
		ie = &idEntry{}
		c.ids[e.key.id] = ie
		c.idsPeak = max(c.idsPeak, len(c.ids))
		c.used += identityBytes
	}
	ie.n++
	c.used += e.size()
	return c.lru.PushFront(e)
}

// accounted is the cache's charge against the budget: entries, identities
// and each map at its peak, which compaction lowers.
func (c *cache) accounted() int64 {
	return c.used + int64(c.decPeak)*decisionSlotBytes + int64(c.idsPeak)*identitySlotBytes
}

// evict drops least recently used entries, never keep, until the cache fits its budget.
func (c *cache) evict(keep *list.Element) {
	for c.accounted() > c.max {
		el := c.lru.Back()
		if el == keep {
			break
		}
		c.remove(el)
		c.m.evictions.Inc()
	}
	c.report()
}

// remove drops el, and its identity when that was its last entry, compacting the maps.
func (c *cache) remove(el *list.Element) {
	e := c.lru.Remove(el).(*entry)
	c.used -= e.size()
	ie := c.ids[e.key.id]
	if e.full {
		ie.full = nil
	} else {
		delete(c.decisions, e.key)
		c.decisions = compact(c.decisions, &c.decPeak, &c.decDels)
	}
	if ie.n--; ie.n == 0 {
		delete(c.ids, e.key.id)
		c.used -= identityBytes
		c.ids = compact(c.ids, &c.idsPeak, &c.idsDels)
	}
}

// compact counts a delete from m and returns a copy sized to its entries once
// they fell below half of *peak or *peak deletes passed: a map keeps its capacity,
// and under churn its tombstones make it grow (TestRetainedHeapAfterChurn).
func compact[K comparable, V any](m map[K]V, peak, dels *int) map[K]V {
	*dels++
	if len(m) >= *peak/2 && *dels < *peak {
		return m
	}
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	*peak, *dels = len(m), 0
	return out
}

// report sets the byte and identity gauges.
func (c *cache) report() {
	c.m.bytes.Set(float64(c.accounted()))
	c.m.identities.Set(float64(len(c.ids)))
}

// sweep drops expired entries and identities left empty.
func (c *cache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for el := c.lru.Front(); el != nil; {
		next := el.Next()
		if !now.Before(el.Value.(*entry).d.expires) {
			c.remove(el)
		}
		el = next
	}
	c.report()
}
