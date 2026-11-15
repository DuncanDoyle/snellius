package sar

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// fullReadParallel is the most constituent SARs one full-read evaluation runs at once.
const fullReadParallel = 8

// ErrExpired is the error of a full-read decision that expired before it was
// returned, on the first evaluation and on the one after it.
var ErrExpired = errors.New("authorisation: full-read decision expired")

// fullKey names one full-read decision: an identity and the served kinds' key.
type fullKey struct {
	id    idHash
	kinds string
}

type fullEntry struct {
	kinds string
	d     decision
}

// fullCache holds one full-read decision per identity and merges concurrent
// evaluations for the same key (design §6.4, D-099). An expiry never moves.
type fullCache struct {
	clock clock.PassiveClock
	m     *metrics

	mu      sync.Mutex
	entries map[idHash]fullEntry
	flights map[fullKey]*flight

	// Test hooks, nil in production: a waiter joined an evaluation; a waiter's evaluation ended.
	joined, woke func()
}

func newFullCache(clk clock.PassiveClock, m *metrics) *fullCache {
	return &fullCache{clock: clk, m: m, entries: map[idHash]fullEntry{}, flights: map[fullKey]*flight{}}
}

// get answers k from the cache, a running evaluation or a new one by eval.
// The caller checks the result's expiry: a waiter may wake after it.
func (c *fullCache) get(ctx context.Context, k fullKey, eval func() (decision, error)) (decision, error) {
	c.mu.Lock()
	if e, ok := c.entries[k.id]; ok && e.kinds == k.kinds {
		if c.clock.Now().Before(e.d.expires) {
			c.mu.Unlock()
			c.m.cache.Inc("fullread-hit")
			return e.d, nil
		}
		delete(c.entries, k.id)
	}
	if f := c.flights[k]; f != nil {
		c.mu.Unlock()
		c.m.cache.Inc("fullread-miss")
		if c.joined != nil {
			c.joined()
		}
		select {
		case <-f.done:
		case <-ctx.Done():
			return decision{}, ctx.Err()
		}
		if c.woke != nil {
			c.woke()
		}
		return f.d, f.err
	}
	f := &flight{done: make(chan struct{})}
	c.flights[k] = f
	c.mu.Unlock()
	c.m.cache.Inc("fullread-miss")
	return c.fly(f, k, eval)
}

// fly runs eval for f and completes f even when eval panics.
func (c *fullCache) fly(f *flight, k fullKey, eval func() (decision, error)) (decision, error) {
	ran := false
	defer func() {
		if !ran {
			f.d, f.err = decision{}, checkError(categoryPanic)
		}
		c.mu.Lock()
		delete(c.flights, k)
		if f.err == nil && c.clock.Now().Before(f.d.expires) {
			c.store(k, f.d)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.d, f.err = eval()
	ran = true
	return f.d, f.err
}

// store keeps d for k, replacing the identity's decision for any other key.
// Past the bound one entry is dropped; it is only evaluated again.
func (c *fullCache) store(k fullKey, d decision) {
	if _, ok := c.entries[k.id]; !ok && len(c.entries) >= maxIdentities {
		for h := range c.entries {
			delete(c.entries, h)
			break
		}
	}
	c.entries[k.id] = fullEntry{kinds: k.kinds, d: d}
}

// sweep drops expired decisions.
func (c *fullCache) sweep(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for h, e := range c.entries {
		if !now.Before(e.d.expires) {
			delete(c.entries, h)
		}
	}
}

// FullRead implements api.FullReader: whether the caller may list every
// served kind cluster-wide. It never returns an expired decision: an expired
// one is evaluated once more, and ErrExpired follows a second expiry.
func (a *Authorizer) FullRead(r *http.Request) (bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return false, ErrNoIdentity
	}
	kinds, key := a.kinds.All()
	k := fullKey{id: hashIdentity(id), kinds: key}
	eval := func() (decision, error) { return a.evaluate(r, id, k.id, kinds) }
	for range 2 {
		d, err := a.full.get(r.Context(), k, eval)
		if err != nil {
			return false, err
		}
		if a.clock.Now().Before(d.expires) {
			return d.allowed, nil
		}
	}
	return false, ErrExpired
}

// evaluate asks cluster-wide list on every kind, a.parallel at a time,
// stopping at the first denial or error. The result expires with the
// earliest constituent; no kinds is false.
func (a *Authorizer) evaluate(r *http.Request, id oidc.Identity, h idHash, kinds []Kind) (decision, error) {
	if len(kinds) == 0 {
		return decision{expires: a.clock.Now().Add(a.cache.ttl)}, nil
	}
	// Waiters share this evaluation, so it outlives its request but not the TTL.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), a.cache.ttl)
	defer cancel()
	r = r.WithContext(ctx)

	var (
		mu       sync.Mutex
		next     int
		stop     bool
		agg      = decision{allowed: true}
		firstErr error
		panicked any
	)
	take := func() (Kind, bool) {
		mu.Lock()
		defer mu.Unlock()
		if stop || next == len(kinds) {
			return Kind{}, false
		}
		next++
		return kinds[next-1], true
	}
	record := func(d decision, err error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
			stop = true
		default:
			if agg.expires.IsZero() || d.expires.Before(agg.expires) {
				agg.expires = d.expires
			}
			if !d.allowed {
				agg.allowed, stop = false, true
			}
		}
	}
	var wg sync.WaitGroup
	for range min(a.parallel, len(kinds)) {
		wg.Go(func() {
			// A panic stops the evaluation and continues in the caller's goroutine.
			defer func() {
				if p := recover(); p != nil {
					mu.Lock()
					stop = true
					if panicked == nil {
						panicked = p
					}
					mu.Unlock()
				}
			}()
			for {
				k, ok := take()
				if !ok {
					return
				}
				d, err := a.check(r, id, h, Attributes{Verb: "list", Group: k.Group, Resource: k.Resource})
				if err != nil && !errors.Is(err, ErrCheck) && ctx.Err() != nil {
					err = checkError(categoryTimeout)
				}
				record(d, err)
				if (err != nil || !d.allowed) && a.fullStopped != nil {
					a.fullStopped()
				}
			}
		})
	}
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
	if firstErr != nil {
		return decision{}, firstErr
	}
	return agg, nil
}
