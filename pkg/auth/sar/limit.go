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
	// DefaultBurst is the SARs sent at once above the rate when Options.Burst is zero.
	DefaultBurst = 32
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
