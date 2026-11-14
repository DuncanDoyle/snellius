package audit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/obs"
)

const defaultBuffer = 1024

// Logger writes enabled records as JSON lines. Emit never blocks: a full
// queue drops the record and counts it.
type Logger struct {
	levels  Levels
	queue   chan Record
	log     *slog.Logger
	written *obs.CounterVec
	dropped *obs.CounterVec
}

// Option configures a Logger.
type Option func(*Logger)

// WithBuffer sets the queue size.
func WithBuffer(n int) Option {
	return func(l *Logger) { l.queue = make(chan Record, n) }
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
		dropped: reg.Counter("snellius_audit_dropped_total", "Audit records dropped because the queue was full.", "level")}
	for _, o := range opts {
		o(l)
	}
	if l.queue == nil {
		l.queue = make(chan Record, defaultBuffer)
	}
	return l
}

// Enabled reports whether level is on.
func (l *Logger) Enabled(level string) bool { return l != nil && l.levels[level] }

// Emit queues rec if its level is enabled, filling the time, ID and request ID.
func (l *Logger) Emit(ctx context.Context, rec Record) {
	if !l.Enabled(rec.Level) {
		return
	}
	if rec.Time.IsZero() {
		rec.Time = time.Now()
	}
	if rec.ID == "" {
		rec.ID = newID()
	}
	if rec.Request.ID == "" {
		rec.Request.ID = RequestIDFrom(ctx)
	}
	select {
	case l.queue <- rec:
	default:
		l.dropped.Inc(rec.Level)
	}
}

// Run writes queued records until ctx is done, then writes what is left.
func (l *Logger) Run(ctx context.Context) {
	for {
		select {
		case rec := <-l.queue:
			l.write(rec)
		case <-ctx.Done():
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

func (l *Logger) write(rec Record) {
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

type ctxKey struct{}

// RequestIDFrom returns the request ID in ctx, or empty.
func RequestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// RequestID gives every request a fresh ID, sets it as the X-Request-Id
// response header and stores it in the request context.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newID()
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}
