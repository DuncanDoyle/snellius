// Package limits counts hits on Snellius's own size limits and logs them,
// rate-limited, so operators can spot capacity problems or abuse (D-100, D-112).
// A record names the limit and the install, never an object or a namespace.
package limits

import (
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// Limit names one of Snellius's own size limits: the counter's label value.
type Limit string

// The limits. The delegation ones are per install, hit while a model is built.
const (
	// AuthzChecks is the per-request cap on uncached permission checks (D-100).
	AuthzChecks Limit = "authz-checks"
	// DelegationEdges caps the selected delegation edges of an install (rules §5.7).
	DelegationEdges Limit = "delegation-edges"
	// DelegationContexts caps the delegation contexts computed for an install.
	DelegationContexts Limit = "delegation-contexts"
	// DelegationMatchers caps the matcher steps of an install's delegation traversal.
	DelegationMatchers Limit = "delegation-matchers"
	// DelegationDepth caps the routes on one delegation path.
	DelegationDepth Limit = "delegation-depth"
	// DelegationRetained caps the delegation contexts an install keeps for display.
	DelegationRetained Limit = "delegation-retained-contexts"
)

// All lists every Limit: the counter's only label values.
var All = []Limit{AuthzChecks, DelegationEdges, DelegationContexts, DelegationMatchers, DelegationDepth, DelegationRetained}

// Window is the shortest time between two log lines for one limit and install.
const Window = time.Minute

// Message is the log line's message.
const Message = "size limit hit"

// Recorder counts every hit in snellius_limit_hits_total{limit} and logs at
// most one warning per limit and install per Window; an ended window's
// held-back hits are logged when a later line drops it. Safe for concurrent use.
type Recorder struct {
	hits *obs.CounterVec
	log  *slog.Logger
	now  func() time.Time

	mu      sync.Mutex
	windows map[key]*window
}

type key struct {
	limit   Limit
	install string
}

// window is one logged line's window and the hits it suppressed.
type window struct {
	start      time.Time
	suppressed int
}

// New registers the counter on r, every limit at zero. A nil r, log or now
// means a private registry, slog.Default() and time.Now.
func New(r *obs.Registry, log *slog.Logger, now func() time.Time) *Recorder {
	if r == nil {
		r = obs.NewRegistry()
	}
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	hits := r.Counter("snellius_limit_hits_total", "Times one of Snellius's own size limits was hit, by limit.", "limit")
	for _, l := range All {
		hits.Add(0, string(l))
	}
	return &Recorder{hits: hits, log: log, now: now, windows: map[key]*window{}}
}

// Hit records one hit of l for install. session is the request's audit ID for
// a request limit and "" for a compute bound. An unknown l and a nil r do nothing.
func (r *Recorder) Hit(l Limit, install, session string) {
	if r == nil || !slices.Contains(All, l) {
		return
	}
	r.hits.Inc(string(l))
	now := r.now()
	k := key{l, install}
	r.mu.Lock()
	w := r.windows[k]
	if w != nil && now.Sub(w.start) < Window {
		w.suppressed++
		r.mu.Unlock()
		return
	}
	suppressed := 0
	if w != nil {
		suppressed = w.suppressed
	}
	// Ended windows are dropped, so gone installs leave nothing behind; held-back hits get a line first.
	var held []key
	var counts []int
	for kk, ww := range r.windows {
		if kk != k && now.Sub(ww.start) >= Window {
			if ww.suppressed > 0 {
				held, counts = append(held, kk), append(counts, ww.suppressed)
			}
			delete(r.windows, kk)
		}
	}
	r.windows[k] = &window{start: now}
	r.mu.Unlock()
	for i, kk := range held {
		r.log.Warn(Message, "limit", string(kk.limit), "install", kk.install, "suppressed", counts[i])
	}
	attrs := []any{"limit", string(l), "install", install}
	if session != "" {
		attrs = append(attrs, "session", session)
	}
	r.log.Warn(Message, append(attrs, "suppressed", suppressed)...)
}
