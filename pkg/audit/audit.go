package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

const defaultBuffer = 1024

// Logger writes enabled records as JSON lines. Emit never blocks: a full
// queue, or a writer that has stopped, drops the record and counts it.
// Summarize and Collapse hold records in windows that Run flushes.
type Logger struct {
	levels  Levels
	queue   chan Record
	mu      sync.RWMutex
	stopped bool // set once Run stops reading; guarded by mu
	log     *slog.Logger
	written *obs.CounterVec
	dropped *obs.CounterVec
	clock   clock.WithTicker
	server  string
	maxOpen int
	// summaries and denials collapse records per key until Run flushes them.
	summaries, denials *window
}

// Option configures a Logger.
type Option func(*Logger)

// WithBuffer sets the queue size.
func WithBuffer(n int) Option {
	return func(l *Logger) { l.queue = make(chan Record, n) }
}

// WithClock sets the clock that times records and flushes; without it, the real clock.
func WithClock(c clock.WithTicker) Option {
	return func(l *Logger) { l.clock = c }
}

// WithServer sets the server instance name written in every record.
func WithServer(name string) Option {
	return func(l *Logger) { l.server = name }
}

// WithMaxOpenKeys sets the open-key cap of each window; without it, MaxOpenKeys.
func WithMaxOpenKeys(n int) Option {
	return func(l *Logger) { l.maxOpen = n }
}

// New returns a Logger that writes to w. Start it with Run. reg may be nil.
func New(w io.Writer, levels Levels, reg *obs.Registry, opts ...Option) *Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		// Drop the handler's own time, level and msg; the record carries its own.
		if len(groups) > 0 {
			return a
		}
		_, isLevel := a.Value.Any().(slog.Level)
		if (a.Key == slog.TimeKey && a.Value.Kind() == slog.KindTime) || (a.Key == slog.LevelKey && isLevel) || a.Key == slog.MessageKey {
			return slog.Attr{}
		}
		return a
	}})
	if reg == nil {
		reg = obs.NewRegistry()
	}
	l := &Logger{levels: levels, log: slog.New(h).With("log", "snellius.audit"),
		written: reg.Counter("snellius_audit_records_total", "Audit records written.", "level"),
		dropped: reg.Counter("snellius_audit_dropped_total", "Audit records dropped because the queue was full or the writer had stopped.", "level")}
	for _, o := range opts {
		o(l)
	}
	if l.queue == nil {
		l.queue = make(chan Record, defaultBuffer)
	}
	if l.clock == nil {
		l.clock = clock.RealClock{}
	}
	if l.maxOpen <= 0 {
		l.maxOpen = MaxOpenKeys
	}
	l.summaries, l.denials = newWindow(l.maxOpen), newWindow(l.maxOpen)
	return l
}

// Enabled reports whether level is on.
func (l *Logger) Enabled(level string) bool { return l != nil && l.levels[level] }

// Emit queues rec if its level is enabled, filling the time, ID and request ID.
func (l *Logger) Emit(ctx context.Context, rec Record) {
	if !l.Enabled(rec.Level) {
		return
	}
	if rec.Request.ID == "" {
		rec.Request.ID = RequestIDFrom(ctx)
	}
	l.enqueue(l.stamp(rec))
}

// Summarize counts rec under key in the summary window, flushed every
// SummaryWindow and when Run stops. A summary stands for many requests, so it
// carries no request ID; its time is the first request's.
func (l *Logger) Summarize(key string, rec Record) {
	rec.Request.ID = ""
	l.collect(l.summaries, key, rec)
}

// Collapse counts rec under key in the denial window, flushed every
// DenialWindow and when Run stops: repeats of one denial become one record
// with a count. Its time and request ID are the first denial's.
func (l *Logger) Collapse(ctx context.Context, key string, rec Record) {
	if rec.Request.ID == "" {
		rec.Request.ID = RequestIDFrom(ctx)
	}
	l.collect(l.denials, key, rec)
}

func (l *Logger) collect(w *window, key string, rec Record) {
	if !l.Enabled(rec.Level) {
		return
	}
	rec = l.stamp(rec)
	var evicted *Record
	l.mu.RLock()
	stopped := l.stopped
	if !stopped {
		evicted = w.add(key, rec)
	}
	l.mu.RUnlock()
	if stopped {
		l.dropped.Inc(rec.Level)
	}
	if evicted != nil {
		l.enqueue(*evicted)
	}
}

// stamp fills rec's time and server.
func (l *Logger) stamp(rec Record) Record {
	if rec.Time.IsZero() {
		rec.Time = l.clock.Now()
	}
	if rec.Server == "" {
		rec.Server = l.server
	}
	return rec
}

// enqueue queues rec without blocking, or drops and counts it.
func (l *Logger) enqueue(rec Record) {
	queued := false
	l.mu.RLock()
	if !l.stopped {
		select {
		case l.queue <- rec:
			queued = true
		default:
		}
	}
	l.mu.RUnlock()
	if !queued {
		l.dropped.Inc(rec.Level)
	}
}

// Run writes queued records and flushes the windows on their periods until
// ctx is done, then writes what is left, the open windows included. Records
// emitted after that are dropped.
func (l *Logger) Run(ctx context.Context) {
	sums, dens := l.clock.NewTicker(SummaryWindow), l.clock.NewTicker(DenialWindow)
	defer sums.Stop()
	defer dens.Stop()
	for {
		select {
		case rec := <-l.queue:
			l.write(rec)
		case <-sums.C():
			l.flush(l.summaries)
		case <-dens.C():
			l.flush(l.denials)
		case <-ctx.Done():
			l.mu.Lock()
			l.stopped = true
			l.mu.Unlock()
			l.flush(l.denials)
			l.flush(l.summaries)
			for {
				select {
				case rec := <-l.queue:
					l.write(rec)
				default:
					return
				}
			}
		}
	}
}

// flush writes w's open records.
func (l *Logger) flush(w *window) {
	for _, rec := range w.take() {
		l.write(rec)
	}
}

func (l *Logger) write(rec Record) {
	if rec.ID == "" {
		rec.ID = newID()
	}
	l.log.LogAttrs(context.Background(), slog.LevelInfo, "", attrs(rec)...)
	l.written.Inc(rec.Level)
}

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// attrs lists the non-empty fields in a fixed order.
func attrs(r Record) []slog.Attr {
	out := []slog.Attr{slog.String("schema", Schema)}
	add := func(k, v string) {
		if v != "" {
			out = append(out, slog.String(k, v))
		}
	}
	group := func(k string, a ...slog.Attr) {
		var kept []any
		for _, x := range a {
			if x.Key != "" {
				kept = append(kept, x)
			}
		}
		if len(kept) > 0 {
			out = append(out, slog.Group(k, kept...))
		}
	}
	str := func(k, v string) slog.Attr {
		if v == "" {
			return slog.Attr{}
		}
		return slog.String(k, v)
	}
	add("id", r.ID)
	out = append(out, slog.String("time", r.Time.UTC().Format(time.RFC3339Nano)))
	add("level", r.Level)
	add("action", r.Action)
	add("outcome", r.Outcome)
	add("reason", r.Reason)
	add("mode", r.Mode)
	add("listen", r.Listen)
	group("user", str("issuer", r.User.Issuer), str("subject", r.User.Subject))
	kube := []slog.Attr{str("username", r.Kube.Username)}
	if len(r.Kube.Groups) > 0 {
		kube = append(kube, slog.Any("groups", r.Kube.Groups))
	}
	group("kube", kube...)
	add("session", r.Session)
	add("agent", r.Agent)
	add("onBehalfOf", r.OnBehalfOf)
	req := []slog.Attr{str("id", r.Request.ID), str("method", r.Request.Method), str("route", r.Request.Route)}
	if len(r.Request.Filters) > 0 {
		keys := make([]string, 0, len(r.Request.Filters))
		for k := range r.Request.Filters {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var f []any
		for _, k := range keys {
			f = append(f, slog.String(k, r.Request.Filters[k]))
		}
		req = append(req, slog.Group("filters", f...))
	}
	group("request", req...)
	add("install", r.Install)
	add("server", r.Server)
	if len(r.Refs) > 0 {
		out = append(out, slog.Any("refs", r.Refs))
	}
	group("authz", str("revision", r.AuthzRev))
	add("source", r.Source)
	add("userAgent", r.UserAgent)
	if r.Count > 0 {
		out = append(out, slog.Int("count", r.Count))
	}
	return out
}

type (
	ctxKey    struct{}
	sourceKey struct{}
)

// RequestIDFrom returns the request ID in ctx, or empty.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// sourceFrom returns the client address RequestIDTrusting derived from X-Forwarded-For, or empty.
func sourceFrom(ctx context.Context) string {
	s, _ := ctx.Value(sourceKey{}).(string)
	return s
}

// RequestID gives every request a fresh ID, sets it as the X-Request-Id
// response header and stores it in the request context.
func RequestID(next http.Handler) http.Handler { return RequestIDTrusting(nil, next) }

const maxRequestID = 64

// RequestIDTrusting is RequestID, except that a request from a peer in cidrs
// keeps a valid incoming X-Request-Id, and its X-Forwarded-For gives the audit
// source: the right-most address that is not itself in cidrs.
func RequestIDTrusting(cidrs []netip.Prefix, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		id := newID()
		if trusted(cidrs, peerAddr(r)) {
			if in := r.Header.Get("X-Request-Id"); validRequestID(in) {
				id = in
			}
			if src := forwardedSource(cidrs, r.Header.Values("X-Forwarded-For")); src != "" {
				ctx = context.WithValue(ctx, sourceKey{}, src)
			}
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, ctxKey{}, id)))
	})
}

func peerAddr(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	return ap.Addr().Unmap()
}

func trusted(cidrs []netip.Prefix, a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	for _, p := range cidrs {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// forwardedSource walks X-Forwarded-For from the right, past trusted proxies;
// empty when an entry is not an address or every entry is trusted.
func forwardedSource(cidrs []netip.Prefix, headers []string) string {
	var hops []string
	for _, h := range headers {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return ""
		}
		if a = a.Unmap(); !trusted(cidrs, a) {
			return a.String()
		}
	}
	return ""
}

// validRequestID accepts 1 to 64 characters of [A-Za-z0-9._-].
func validRequestID(s string) bool {
	if s == "" || len(s) > maxRequestID {
		return false
	}
	for _, c := range s {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-'
		if !ok {
			return false
		}
	}
	return true
}

// ParseCIDRs parses a comma list of CIDRs; empty means none.
func ParseCIDRs(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v == "" {
			continue
		}
		p, err := netip.ParsePrefix(v)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy CIDR %q: %w", v, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}
