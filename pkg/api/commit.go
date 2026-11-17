package api

import (
	"bytes"
	"errors"
	"maps"
	"net/http"
	"strings"
	"time"
)

// Reasons the commit wrapper passes to the WithAuthzAudit hook.
const (
	AuditCheckFailed    = "check-failed"
	AuditCheckLimit     = "check-limit"
	AuditFullReadFailed = "full-read-failed"
	AuditExpired        = "expired"
	AuditIdentity       = "identity-stale"
)

// commitAttempts is how many times commit builds a response whose allows expired (D-100).
const commitAttempts = 2

// authzUnavailable is the error body of a request whose authorisation failed (design §6.3).
const authzUnavailable = "authz-unavailable"

// WithCommitCheck runs check on every secure /api/ request r after its handler
// and before its response is written. Answering r commits; nil means check wrote
// its own response, which replaces the handler's; another request rebuilds the
// response with it; an error answers 503 authz-unavailable.
func WithCommitCheck(check func(w http.ResponseWriter, r *http.Request) (*http.Request, error)) HandlerOption {
	return func(c *handlerConfig) { c.commitCheck = check }
}

// WithAuthzAudit calls audit once for each secure /api/ request answered 503
// authz-unavailable, with AuditCheckFailed, AuditCheckLimit, AuditFullReadFailed,
// AuditExpired or AuditIdentity.
func WithAuthzAudit(audit func(r *http.Request, reason string)) HandlerOption {
	return func(c *handlerConfig) { c.authzAudit = audit }
}

// WithCheckCapHit calls hit once for each secure /api/ request that passes
// CheckLimit, whatever it is answered, before the answer is chosen.
func WithCheckCapHit(hit func(r *http.Request)) HandlerOption {
	return func(c *handlerConfig) { c.capHit = hit }
}

// WithCommitClock sets the clock commit compares allow expiries with; without it, time.Now.
func WithCommitClock(now func() time.Time) HandlerOption {
	return func(c *handlerConfig) { c.commitClock = now }
}

// commit is the response-commit protocol of secure mode (design §6.3, D-100):
// every /api/ response is built in a buffer, then the session, a recorded
// authorisation error and the earliest allow expiry are checked, in that order,
// after every build. A response whose allows expired, or whose identity a refresh
// replaced, is built once more from fresh request state, keeping only the check
// count. Other paths pass through.
func commit(next http.Handler, check func(http.ResponseWriter, *http.Request) (*http.Request, error), audit func(*http.Request, string), capHit func(*http.Request), now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	unavailable := func(w http.ResponseWriter, r *http.Request, reason string) {
		if audit != nil {
			audit(r, reason)
		}
		w.Header().Set("Cache-Control", "no-store")
		writeErr(w, http.StatusServiceUnavailable, authzUnavailable)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, base *http.Request) {
		if !strings.HasPrefix(base.URL.Path, "/api/") {
			next.ServeHTTP(w, base)
			return
		}
		checks := 0
		for attempt := 1; ; attempt++ {
			r, st := withAuthzStateFrom(base, checks)
			buf := &bufferedResponse{header: http.Header{}}
			next.ServeHTTP(buf, r)
			if capHit != nil && st.capPassed() {
				capHit(r)
			}
			// An ended session answers 401 even when an authorisation error was recorded;
			// that error answers 503 before a stale or replaced identity is looked at.
			cur, stale := r, error(nil)
			if check != nil {
				if cur, stale = check(w, r); cur == nil && stale == nil {
					return
				}
			}
			if err := st.failed(); err != nil {
				unavailable(w, r, auditReason(err, st.fullReadFailed()))
				return
			}
			switch {
			case stale != nil:
				unavailable(w, r, AuditIdentity)
				return
			case cur != r:
				if attempt == commitAttempts {
					unavailable(w, r, AuditIdentity)
					return
				}
				base, checks = cur, st.spent()
				continue
			}
			if exp := st.allowExpiry(); exp.IsZero() || exp.After(now()) {
				buf.writeTo(w)
				return
			}
			if attempt == commitAttempts {
				unavailable(w, r, AuditExpired)
				return
			}
			checks = st.spent()
		}
	})
}

// auditReason names why a request answered authz-unavailable.
func auditReason(err error, fullFailed bool) string {
	switch {
	case fullFailed:
		return AuditFullReadFailed
	case errors.Is(err, ErrCheckLimit):
		return AuditCheckLimit
	}
	return AuditCheckFailed
}

// bufferedResponse holds a handler's status, headers and body until commit decides.
type bufferedResponse struct {
	header http.Header
	code   int
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }

func (b *bufferedResponse) WriteHeader(code int) {
	if b.code == 0 {
		b.code = code
	}
}

func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

// writeTo sends the buffered response to w.
func (b *bufferedResponse) writeTo(w http.ResponseWriter) {
	maps.Copy(w.Header(), b.header)
	if b.code == 0 {
		b.code = http.StatusOK
	}
	w.WriteHeader(b.code)
	_, _ = w.Write(b.body.Bytes())
}
