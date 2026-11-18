package sar

import (
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

// idEntry is one identity's metadata: its hash, how many entries it has and its full-read entry.
type idEntry struct {
	h    idHash
	n    int
	full *entry
}

// entry is a cached decision, or the identity's full-read decision when it is
// id.full. attrs is the decision's encoded Attributes or the full read's kinds key.
type entry struct {
	prev, next *entry // LRU ring
	id         *idEntry
	attrs      string
	d          decision
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
	decisions map[decKey]*entry
	ids       map[idHash]*idEntry
	lru       entry // sentinel of the ring of decisions and full-read entries, most recently used next
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
	ca := &cache{
		checker: c, lim: lim, clock: clk, allowTTL: allowTTL, m: m, log: slog.Default(), max: maxBytes, rand: denyJitter,
		decisions: map[decKey]*entry{}, ids: map[idHash]*idEntry{},
		flights: map[flightKey]*flight{}, fullFlights: map[fullKey]*flight{},
	}
	ca.lru.prev, ca.lru.next = &ca.lru, &ca.lru
	return ca
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
	e := c.find(h, a)
	if e == nil {
		return decision{}, false, api.MissAbsent
	}
	if !now.Before(e.d.expires) {
		c.remove(e)
		c.report()
		if e.d.allowed {
			return decision{}, false, api.MissAllowExpired
		}
		return decision{}, false, api.MissDenyExpired
	}
	c.toFront(e)
	return e.d, true, ""
}

// find returns the identity's cached decision for a, expired or not, or nil.
func (c *cache) find(h idHash, a Attributes) *entry {
	ie := c.ids[h]
	if ie == nil {
		return nil
	}
	var buf [128]byte
	return c.decisions[decKey{ie, string(appendAttrs(buf[:0], a))}]
}

// lookupFull returns the identity's unexpired full-read decision for k's kinds
// and marks it used; an expired one is dropped.
func (c *cache) lookupFull(k fullKey, now time.Time) (decision, bool) {
	ie := c.ids[k.id]
	if ie == nil || ie.full == nil {
		return decision{}, false
	}
	e := ie.full
	if !now.Before(e.d.expires) {
		c.remove(e)
		c.report()
		return decision{}, false
	}
	if e.attrs != k.kinds {
		return decision{}, false
	}
	c.toFront(e)
	return e.d, true
}

// store keeps d for k as the most recently used entry.
func (c *cache) store(k flightKey, d decision) {
	if e := c.find(k.id, k.attrs); e != nil {
		e.d = d
		c.toFront(e)
		return
	}
	var buf [128]byte
	ie := c.identity(k.id)
	e := &entry{id: ie, attrs: string(appendAttrs(buf[:0], k.attrs)), d: d}
	c.insert(e)
	c.decisions[decKey{ie, e.attrs}] = e
	c.decPeak = max(c.decPeak, len(c.decisions))
	c.evict(e)
}

// storeFull keeps d as the identity's full-read decision for k's kinds,
// replacing its decision for any other kinds.
func (c *cache) storeFull(k fullKey, d decision) {
	if ie := c.ids[k.id]; ie != nil && ie.full != nil {
		e := ie.full
		c.used += stringBytes(k.kinds) - stringBytes(e.attrs)
		e.attrs, e.d = k.kinds, d
		c.toFront(e)
		c.evict(e)
		return
	}
	ie := c.identity(k.id)
	e := &entry{id: ie, attrs: k.kinds, d: d}
	c.insert(e)
	ie.full = e
	c.evict(e)
}

// identity returns h's metadata, adding it when new.
func (c *cache) identity(h idHash) *idEntry {
	ie := c.ids[h]
	if ie == nil {
		ie = &idEntry{h: h}
		c.ids[h] = ie
		c.idsPeak = max(c.idsPeak, len(c.ids))
		c.used += identityBytes
	}
	return ie
}

// insert counts e against its identity and puts it at the front of the LRU.
func (c *cache) insert(e *entry) {
	e.id.n++
	c.used += e.size()
	e.prev, e.next = &c.lru, c.lru.next
	e.prev.next, e.next.prev = e, e
}

// toFront marks e most recently used.
func (c *cache) toFront(e *entry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = &c.lru, c.lru.next
	e.prev.next, e.next.prev = e, e
}

// accounted is the cache's charge against the budget: entries, identities
// and each map at its peak, which compaction lowers.
func (c *cache) accounted() int64 {
	return c.used + int64(c.decPeak)*decisionSlotBytes + int64(c.idsPeak)*identitySlotBytes
}

// evict drops least recently used entries, never keep, until the cache fits its budget.
func (c *cache) evict(keep *entry) {
	for c.accounted() > c.max {
		e := c.lru.prev
		if e == keep {
			break
		}
		c.remove(e)
		c.m.evictions.Inc()
	}
	c.report()
}

// remove drops e, and its identity when that was its last entry, compacting the maps.
func (c *cache) remove(e *entry) {
	e.prev.next, e.next.prev = e.next, e.prev
	e.prev, e.next = nil, nil
	c.used -= e.size()
	ie := e.id
	if ie.full == e {
		ie.full = nil
	} else {
		delete(c.decisions, decKey{ie, e.attrs})
		c.decisions = compact(c.decisions, &c.decPeak, &c.decDels)
	}
	if ie.n--; ie.n == 0 {
		delete(c.ids, ie.h)
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
	for e := c.lru.next; e != &c.lru; {
		next := e.next
		if !now.Before(e.d.expires) {
			c.remove(e)
		}
		e = next
	}
	c.report()
}
