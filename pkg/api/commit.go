package api

import (
	"bytes"
	"errors"
	"io"
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

// MaxRequestBody bounds the body of a secure /api/ request other than GET or
// HEAD, which commit reads in full before admission; a larger one answers 413.
const MaxRequestBody = 1 << 20

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

// WithCheckTrace calls trace for each uncached check a secure /api/ request
// counts, with the build it was counted in (1-based, discarded builds
// included), why the check was a miss (a Miss reason, or "" when the
// authoriser gives none) and the authoriser's key for the check. It runs while
// the authoriser holds its locks, so it must be quick and must not call back.
func WithCheckTrace(trace func(r *http.Request, build int, reason string, key any)) HandlerOption {
	return func(c *handlerConfig) { c.checkTrace = trace }
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
// count. With admit, a GET or HEAD build that needs an uncached check before
// admission is discarded, the request waits for admission and is built afresh
// in the same attempt; other methods have their body read, up to MaxRequestBody,
// then are admitted before their first build, and every build reads that body.
// Admission is given back once the response is final, before it is written
// (D-116). Other paths pass through.
func commit(next http.Handler, admit Admitter, check func(http.ResponseWriter, *http.Request) (*http.Request, error), audit func(*http.Request, string), capHit func(*http.Request), trace func(*http.Request, int, string, any), now func() time.Time) http.Handler {
	if now == nil {
		now = time.Now
	}
	return http.HandlerFunc(func(w http.ResponseWriter, base *http.Request) {
		if !strings.HasPrefix(base.URL.Path, "/api/") {
			next.ServeHTTP(w, base)
			return
		}
		var release func()
		// done gives admission back; the deferred call covers a panic.
		done := func() {
			if release != nil {
				release()
				release = nil
			}
		}
		defer done()
		unavailable := func(r *http.Request, reason string) {
			done()
			if audit != nil {
				audit(r, reason)
			}
			w.Header().Set("Cache-Control", "no-store")
			writeErr(w, http.StatusServiceUnavailable, authzUnavailable)
		}
		body, buffered := []byte(nil), false
		if base.Method != http.MethodGet && base.Method != http.MethodHead && base.Body != nil {
			// Read before admission, so a stalled upload holds no place; every build reads it afresh.
			var err error
			if body, err = io.ReadAll(http.MaxBytesReader(w, base.Body, MaxRequestBody)); err != nil {
				if tooLarge := (*http.MaxBytesError)(nil); errors.As(err, &tooLarge) {
					writeErr(w, http.StatusRequestEntityTooLarge, "request body too large")
					return
				}
				writeErr(w, http.StatusBadRequest, "request body unreadable")
				return
			}
			buffered = true
		}
		if admit != nil && base.Method != http.MethodGet && base.Method != http.MethodHead {
			// Never built twice for admission: its body or side effects may not replay.
			rel, err := admit.Admit(base)
			if err != nil {
				unavailable(base, auditReason(err, false))
				return
			}
			release = rel
		}
		gated := admit != nil && release == nil
		checks, builds := 0, 0
		for attempt := 1; ; attempt++ {
			r, st := withAuthzStateFrom(base, checks, gated)
			if buffered {
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			if builds++; trace != nil {
				build := builds
				st.trace = func(reason string, key any) { trace(r, build, reason, key) }
			}
			buf := &bufferedResponse{header: http.Header{}}
			next.ServeHTTP(buf, r)
			if capHit != nil && st.capPassed() {
				capHit(r)
			}
			// An ended session answers 401 even when an authorisation error was recorded;
			// that error answers 503 before a stale or replaced identity is looked at.
			cur, stale := r, error(nil)
			if check != nil {
				// A response the check writes, such as an ended session's 401, gives admission back first.
				if cur, stale = check(releasingWriter{w, done}, r); cur == nil && stale == nil {
					return
				}
			}
			if st.notAdmitted() && stale == nil {
				// Nothing of this build is kept: the admitted build asks afresh, in the same attempt.
				rel, err := admit.Admit(cur)
				if err != nil {
					unavailable(r, auditReason(err, false))
					return
				}
				release, base, gated = rel, cur, false
				attempt--
				continue
			}
			// This build's own refusal, with a stale identity, answers as that identity below.
			if err := st.failed(); err != nil && (!st.notAdmitted() || !errors.Is(err, ErrNotAdmitted)) {
				unavailable(r, auditReason(err, st.fullReadFailed()))
				return
			}
			switch {
			case stale != nil:
				unavailable(r, AuditIdentity)
				return
			case cur != r:
				if attempt == commitAttempts {
					unavailable(r, AuditIdentity)
					return
				}
				base, checks = cur, st.spent()
				continue
			}
			if exp := st.allowExpiry(); exp.IsZero() || exp.After(now()) {
				// The response is final: a slow client must not hold the place while it is written.
				done()
				buf.writeTo(w)
				return
			}
			if attempt == commitAttempts {
				unavailable(r, AuditExpired)
				return
			}
			checks = st.spent()
		}
	})
}

// releasingWriter calls release before the first status or byte reaches its ResponseWriter.
type releasingWriter struct {
	http.ResponseWriter
	release func()
}

func (w releasingWriter) WriteHeader(code int) {
	w.release()
	w.ResponseWriter.WriteHeader(code)
}

func (w releasingWriter) Write(p []byte) (int, error) {
	w.release()
	return w.ResponseWriter.Write(p)
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
