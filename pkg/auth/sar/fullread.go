package sar

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

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

// fullCache answers full reads from the shared cache, one decision per
// identity in its LRU and budget, and merges concurrent evaluations for the
// same key (design §6.4, D-100). An expiry never moves.
type fullCache struct {
	c *cache

	// Test hooks, nil in production: a waiter joined an evaluation; a waiter's evaluation ended.
	joined, woke func()
}

// get answers k from the cache, a running evaluation or a new one by eval,
// which runs under ctx. A waiter whose leader's request ended first leads a new
// evaluation, as cache.check does. The caller checks the result's expiry: a waiter may wake after it.
func (fc *fullCache) get(ctx context.Context, k fullKey, eval func() (decision, error)) (decision, error) {
	c := fc.c
	for {
		c.mu.Lock()
		if d, ok := c.lookupFull(k, c.clock.Now()); ok {
			c.mu.Unlock()
			c.m.cache.Inc("fullread-hit")
			return d, nil
		}
		if f := c.fullFlights[k]; f != nil {
			c.mu.Unlock()
			c.m.cache.Inc("fullread-miss")
			if fc.joined != nil {
				fc.joined()
			}
			select {
			case <-f.done:
			case <-ctx.Done():
				return decision{}, ctx.Err()
			}
			if f.retry {
				continue
			}
			if fc.woke != nil {
				fc.woke()
			}
			return f.d, f.err
		}
		if c.flying() >= maxFlights {
			c.mu.Unlock()
			return decision{}, ErrOverloaded
		}
		f := &flight{done: make(chan struct{})}
		c.fullFlights[k] = f
		c.mu.Unlock()
		c.m.cache.Inc("fullread-miss")
		return fc.fly(ctx, f, k, eval)
	}
}

// fly runs eval for f and completes f even when eval panics. An evaluation
// that fails after the leader's ctx ended lets a waiter retry.
func (fc *fullCache) fly(ctx context.Context, f *flight, k fullKey, eval func() (decision, error)) (decision, error) {
	c := fc.c
	ran := false
	defer func() {
		if !ran {
			f.d, f.err = decision{}, checkError(categoryPanic)
		}
		c.mu.Lock()
		delete(c.fullFlights, k)
		if f.err == nil && c.clock.Now().Before(f.d.expires) {
			c.storeFull(k, f.d)
		}
		c.mu.Unlock()
		close(f.done)
	}()
	f.d, f.err = eval()
	f.retry = f.err != nil && ctx.Err() != nil
	ran = true
	return f.d, f.err
}

// FullRead implements api.FullReader: whether the caller may list every
// served kind cluster-wide. It never returns an expired decision: an expired
// one is evaluated once more, and ErrExpired follows a second expiry. A "true"
// is noted with its expiry for the commit wrapper (D-100).
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
			return a.allow(r, d, nil)
		}
	}
	return false, ErrExpired
}

// evaluate asks cluster-wide list on every kind, a.parallel at a time,
// stopping at the first denial or error (D-100). "False" expires with its
// latest denying constituent, "true" with its earliest allow; no kinds is false.
func (a *Authorizer) evaluate(r *http.Request, id oidc.Identity, h idHash, kinds []Kind) (decision, error) {
	if len(kinds) == 0 {
		return decision{expires: a.clock.Now().Add(a.cache.allowTTL)}, nil
	}
	// Constituents queue under the request's context, so its end frees their places;
	// an admitted SAR runs detached (cache.send). No evaluation outlives the allow TTL.
	reqCtx := r.Context()
	ctx, cancel := context.WithTimeout(reqCtx, a.cache.allowTTL)
	defer cancel()
	r = r.WithContext(ctx)
	// stopped is the error of a constituent ctx cut short: the request's own when it ended, else a timeout.
	stopped := func(err error) error {
		switch {
		case err == nil || errors.Is(err, ErrCheck) || ctx.Err() == nil:
			return err
		case reqCtx.Err() != nil:
			return reqCtx.Err()
		}
		return checkError(categoryTimeout)
	}

	var (
		mu       sync.Mutex
		next     int
		stop     bool
		allowExp time.Time
		denyExp  time.Time
		firstErr error
		panicked any
	)
	take := func() (Kind, bool) {
		mu.Lock()
		defer mu.Unlock()
		if stop || next == len(kinds) {
			return Kind{}, false
		}
		if err := ctx.Err(); err != nil {
			// No kind is scheduled once the request ends or the evaluation times out.
			firstErr, stop = stopped(err), true
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
		case d.allowed:
			if allowExp.IsZero() || d.expires.Before(allowExp) {
				allowExp = d.expires
			}
		default:
			// Any denial is a witness for "false"; incidental allows never shorten it.
			if d.expires.After(denyExp) {
				denyExp = d.expires
			}
			stop = true
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
				err = stopped(err)
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
	if !denyExp.IsZero() {
		return decision{expires: denyExp}, nil
	}
	return decision{allowed: true, expires: allowExp}, nil
}
