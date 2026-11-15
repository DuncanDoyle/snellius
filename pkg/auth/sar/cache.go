package sar

import (
	"container/list"
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// Cache bounds (design §6.4).
const (
	maxEntriesPerIdentity = 10_000
	maxIdentities         = 2_000
)

// decision is a SAR answer valid until expires, the SAR's start plus the TTL.
type decision struct {
	allowed bool
	expires time.Time
}

type flightKey struct {
	id    idHash
	attrs Attributes
}

// flight is one SAR in progress; done closes once d and err are set.
type flight struct {
	done chan struct{}
	d    decision
	err  error
}

// idEntry is one identity's decisions, most recently used at the front.
type idEntry struct {
	hash    idHash
	elem    *list.Element
	entries map[Attributes]*list.Element
	lru     *list.List
}

type entry struct {
	attrs Attributes
	d     decision
}

// cache holds decisions per identity and merges identical concurrent checks
// (design §6.2, D-099). An entry's expiry never moves.
type cache struct {
	checker Checker
	clock   clock.PassiveClock
	ttl     time.Duration
	m       *metrics
	log     *slog.Logger

	mu      sync.Mutex
	ids     map[idHash]*idEntry
	idLRU   *list.List
	flights map[flightKey]*flight

	// Test hooks, nil in production: a waiter joined a flight; a waiter's flight ended.
	joined, woke func()
}

func newCache(c Checker, clk clock.PassiveClock, ttl time.Duration, m *metrics) *cache {
	return &cache{
		checker: c, clock: clk, ttl: ttl, m: m, log: slog.Default(),
		ids: map[idHash]*idEntry{}, idLRU: list.New(), flights: map[flightKey]*flight{},
	}
}

// check answers a for the identity from the cache, a running flight or a new
// SAR. Every miss calls spend first; a hit does not.
func (c *cache) check(ctx context.Context, spend func() error, id oidc.Identity, h idHash, a Attributes) (decision, error) {
	k := flightKey{h, a}
	for {
		c.mu.Lock()
		if d, ok := c.lookup(h, a, c.clock.Now()); ok {
			c.mu.Unlock()
			c.m.cache.Inc("hit")
			return d, nil
		}
		if err := spend(); err != nil {
			c.mu.Unlock()
			return decision{}, err
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
		f := &flight{done: make(chan struct{})}
		c.flights[k] = f
		c.mu.Unlock()
		c.m.cache.Inc("miss")
		return c.fly(ctx, f, k, id, a)
	}
}

// fly runs f's SAR and completes f even when the Checker panics: waiters
// get an error and the panic continues to the request's own recovery.
func (c *cache) fly(ctx context.Context, f *flight, k flightKey, id oidc.Identity, a Attributes) (decision, error) {
	sent := false
	defer func() {
		if !sent {
			f.d, f.err = decision{}, checkError(categoryPanic)
		}
		c.mu.Lock()
		delete(c.flights, k)
		if f.err == nil {
			c.store(k.id, a, f.d)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.d, f.err = c.send(ctx, id, a)
	sent = true
	return f.d, f.err
}

// send runs one SAR, anchoring its expiry at the start. A result that is
// already expired when it returns is an error. Waiters keep the SAR running
// when the request that started it goes away, but never past the TTL.
func (c *cache) send(ctx context.Context, id oidc.Identity, a Attributes) (decision, error) {
	start := c.clock.Now()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.ttl)
	defer cancel()
	allowed, err := c.checker.Check(ctx, id, a)
	if err != nil && !errors.Is(err, ErrCheck) && ctx.Err() != nil {
		err = checkError(categoryTimeout)
	}
	end := c.clock.Now()
	d := decision{allowed: allowed, expires: start.Add(c.ttl)}
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

// lookup returns an unexpired decision and marks it used; an expired one is dropped.
func (c *cache) lookup(h idHash, a Attributes, now time.Time) (decision, bool) {
	ie := c.ids[h]
	if ie == nil {
		return decision{}, false
	}
	el := ie.entries[a]
	if el == nil {
		return decision{}, false
	}
	e := el.Value.(*entry)
	if !now.Before(e.d.expires) {
		c.remove(ie, el)
		return decision{}, false
	}
	ie.lru.MoveToFront(el)
	c.idLRU.MoveToFront(ie.elem)
	return e.d, true
}

// store keeps d, evicting the least recently used entry or identity past the bounds.
func (c *cache) store(h idHash, a Attributes, d decision) {
	ie := c.ids[h]
	if ie == nil {
		if len(c.ids) >= maxIdentities {
			c.drop(c.idLRU.Back().Value.(*idEntry))
		}
		ie = &idEntry{hash: h, entries: map[Attributes]*list.Element{}, lru: list.New()}
		ie.elem = c.idLRU.PushFront(ie)
		c.ids[h] = ie
		c.m.identities.Set(float64(len(c.ids)))
	} else {
		c.idLRU.MoveToFront(ie.elem)
	}
	if el := ie.entries[a]; el != nil {
		el.Value.(*entry).d = d
		ie.lru.MoveToFront(el)
		return
	}
	if ie.lru.Len() >= maxEntriesPerIdentity {
		c.remove(ie, ie.lru.Back())
	}
	ie.entries[a] = ie.lru.PushFront(&entry{attrs: a, d: d})
}

// remove drops one entry; the identity stays until the sweep, so store can refill it.
func (c *cache) remove(ie *idEntry, el *list.Element) {
	delete(ie.entries, el.Value.(*entry).attrs)
	ie.lru.Remove(el)
}

func (c *cache) drop(ie *idEntry) {
	c.idLRU.Remove(ie.elem)
	delete(c.ids, ie.hash)
	c.m.identities.Set(float64(len(c.ids)))
}

// sweep drops expired entries and identities left empty.
func (c *cache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ie := range c.ids {
		for el := ie.lru.Front(); el != nil; {
			next := el.Next()
			if !now.Before(el.Value.(*entry).d.expires) {
				c.remove(ie, el)
			}
			el = next
		}
		if ie.lru.Len() == 0 {
			c.drop(ie)
		}
	}
	c.m.identities.Set(float64(len(c.ids)))
}
