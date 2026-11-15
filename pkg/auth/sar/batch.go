package sar

import (
	"net/http"
	"sync"

	"github.com/DuncanDoyle/snellius/pkg/api"
)

// batchParallel is the most list checks one ResolveLists call runs at once (D-100 item 6).
const batchParallel = 8

// ResolveLists implements api.BatchAuthorizer: each answer is the list half
// of AuthorizeErr, resolved through the cache batchParallel at a time. An
// unmapped kind, or a namespaced kind without a namespace, is false without a
// SAR. It stops at the first error; a request that ends stops queued work and
// returns its context's error.
func (a *Authorizer) ResolveLists(r *http.Request, qs []api.ListQuery) ([]bool, error) {
	id, ok := a.identityFrom(r.Context())
	if !ok {
		return nil, ErrNoIdentity
	}
	h := hashIdentity(id)
	ctx := r.Context()
	out := make([]bool, len(qs))
	var (
		mu       sync.Mutex
		next     int
		firstErr error
		panicked any
	)
	take := func() (int, bool) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr != nil || panicked != nil || ctx.Err() != nil || next == len(qs) {
			return 0, false
		}
		next++
		return next - 1, true
	}
	fail := func(err error) {
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}
	var wg sync.WaitGroup
	for range min(batchParallel, len(qs)) {
		wg.Go(func() {
			// A panic stops the batch and continues in the caller's goroutine.
			defer func() {
				if p := recover(); p != nil {
					mu.Lock()
					if panicked == nil {
						panicked = p
					}
					mu.Unlock()
				}
			}()
			for {
				i, ok := take()
				if !ok {
					return
				}
				at, ok := a.listAttrs(qs[i].Kind.Group, qs[i].Kind.Kind, qs[i].Namespace)
				if !ok {
					continue
				}
				d, err := a.check(r, id, h, at)
				allowed, err := a.allow(r, d, err)
				if err != nil {
					fail(err)
					return
				}
				out[i] = allowed
			}
		})
	}
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// NamedGetScan implements api.ListScanner: whether a list denial still asks a named get.
func (a *Authorizer) NamedGetScan() bool { return a.namedGetScan }
