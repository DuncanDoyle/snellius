package sar

import (
	"container/list"
	"context"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

// Default global SAR limits (D-100 item 6).
const (
	// DefaultMaxInFlight is the most SARs sent at once when Options.MaxInFlight is zero.
	DefaultMaxInFlight = 32
	// DefaultRate is the SARs per second when Options.Rate is zero.
	DefaultRate = 100
	// DefaultBurst is the SARs sent above the rate when Options.Burst is zero:
	// one cold page never waits on the bucket.
	DefaultBurst = 500
	// MaxRate is the highest --authz-rate, so the token interval never rounds to 0.
	MaxRate = 10000
)

// maxQueued is the most checks waiting for admission; one more is ErrOverloaded.
const maxQueued = 1024

// limiter admits SARs: at most max in flight, and a token bucket of rate per
// second holding burst tokens, in its GCRA form. Waiters queue FIFO for a
// permit, then take a token; a token is spent only on admission.
type limiter struct {
	clock clock.Clock

	mu       sync.Mutex
	max      int
	inFlight int
	// permits are the waiters for a permit, oldest first; release hands one over by closing it.
	permits *list.List
	queued  int
	// interval is one token's worth of time; tolerance is burst-1 of them.
	interval, tolerance time.Duration
	// tat is when the bucket is next full; the zero time means full.
	tat time.Time

	// waiting is a test hook, nil in production: a check started waiting for a permit or a token.
	waiting func()
}

func newLimiter(clk clock.Clock, maxInFlight, rate, burst int) *limiter {
	interval := time.Second / time.Duration(rate)
	return &limiter{clock: clk, max: maxInFlight, permits: list.New(), interval: interval, tolerance: time.Duration(burst-1) * interval}
}

// acquire waits under ctx for a permit and a token and returns the release of
// the permit. A ctx that ends first, or a full queue, admits nothing.
func (l *limiter) acquire(ctx context.Context) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	l.mu.Lock()
	if l.queued >= maxQueued {
		l.mu.Unlock()
		return nil, ErrOverloaded
	}
	l.queued++
	defer func() {
		l.queued--
		l.mu.Unlock()
	}()
	if err := l.permit(ctx); err != nil {
		return nil, err
	}
	if err := l.token(ctx); err != nil {
		l.releaseLocked()
		return nil, err
	}
	return sync.OnceFunc(l.release), nil
}

// permit takes a permit, queueing behind earlier waiters. Called and returns with l.mu held.
func (l *limiter) permit(ctx context.Context) error {
	if l.inFlight < l.max && l.permits.Len() == 0 {
		l.inFlight++
		return nil
	}
	ch := make(chan struct{})
	el := l.permits.PushBack(ch)
	l.mu.Unlock()
	if l.waiting != nil {
		l.waiting()
	}
	select {
	case <-ch:
		l.mu.Lock()
		return nil
	case <-ctx.Done():
	}
	l.mu.Lock()
	select {
	case <-ch:
		// Handed a permit as ctx ended: pass it on.
		l.releaseLocked()
	default:
		l.permits.Remove(el)
	}
	return ctx.Err()
}

// token spends one token, waiting for the bucket to refill. Called and returns with l.mu held.
func (l *limiter) token(ctx context.Context) error {
	for {
		now := l.clock.Now()
		if l.tat.Before(now) {
			l.tat = now
		}
		wait := l.tat.Sub(now) - l.tolerance
		if wait <= 0 {
			l.tat = l.tat.Add(l.interval)
			return nil
		}
		l.mu.Unlock()
		t := l.clock.NewTimer(wait)
		if l.waiting != nil {
			l.waiting()
		}
		select {
		case <-t.C():
		case <-ctx.Done():
			t.Stop()
			l.mu.Lock()
			return ctx.Err()
		}
		l.mu.Lock()
	}
}

func (l *limiter) release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked()
}

// releaseLocked hands a permit to the oldest waiter, else returns it.
func (l *limiter) releaseLocked() {
	if el := l.permits.Front(); el != nil {
		close(l.permits.Remove(el).(chan struct{}))
		return
	}
	l.inFlight--
}

// Bounds on requests waiting for admission (D-116); one more is ErrOverloaded.
const (
	// maxAdmitWaiting is the most requests waiting in all: the load bench's 50 cold viewers twice over,
	// and about ten minutes of 500-SAR pages at the default rate.
	maxAdmitWaiting = 128
	// maxAdmitPerID is the most requests of one identity waiting: a page's parallel calls in a few tabs.
	maxAdmitPerID = 8
)

// admission admits requests that need uncached SARs: at most max at once,
// oldest first, with the waiting requests bounded in all and per identity (D-116).
type admission struct {
	mu       sync.Mutex
	max      int
	admitted int
	// waiters are the requests waiting, oldest first; release hands one a place by closing its ch.
	waiters *list.List
	perID   map[idHash]int

	// waiting is a test hook, nil in production: a request started waiting.
	waiting func()
}

// admitWaiter is one waiting request and its identity.
type admitWaiter struct {
	ch chan struct{}
	id idHash
}

func newAdmission(places int) *admission {
	return &admission{max: max(places, 1), waiters: list.New(), perID: map[idHash]int{}}
}

// admit waits under ctx for a place for identity h and returns its release.
// A ctx that ends first leaves the queue at once; a full queue admits nothing.
func (q *admission) admit(ctx context.Context, h idHash) (release func(), err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	q.mu.Lock()
	if q.admitted < q.max && q.waiters.Len() == 0 {
		q.admitted++
		q.mu.Unlock()
		return sync.OnceFunc(q.release), nil
	}
	if q.waiters.Len() >= maxAdmitWaiting || q.perID[h] >= maxAdmitPerID {
		q.mu.Unlock()
		return nil, ErrOverloaded
	}
	w := &admitWaiter{ch: make(chan struct{}), id: h}
	el := q.waiters.PushBack(w)
	q.perID[h]++
	q.mu.Unlock()
	if q.waiting != nil {
		q.waiting()
	}
	select {
	case <-w.ch:
		return sync.OnceFunc(q.release), nil
	case <-ctx.Done():
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	select {
	case <-w.ch:
		// Admitted as ctx ended: pass the place on.
		q.releaseLocked()
	default:
		q.waiters.Remove(el)
		q.left(h)
	}
	return nil, ctx.Err()
}

func (q *admission) release() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked()
}

// releaseLocked hands a place to the oldest waiter, else returns it.
func (q *admission) releaseLocked() {
	if el := q.waiters.Front(); el != nil {
		w := q.waiters.Remove(el).(*admitWaiter)
		q.left(w.id)
		close(w.ch)
		return
	}
	q.admitted--
}

// left counts a waiter of identity h out of the queue.
func (q *admission) left(h idHash) {
	if q.perID[h]--; q.perID[h] == 0 {
		delete(q.perID, h)
	}
}
