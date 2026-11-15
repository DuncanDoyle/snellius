package api

import (
	"bytes"
	"errors"
	"maps"
	"net/http"
	"strings"
)

// Reasons the commit wrapper passes to the WithAuthzAudit hook.
const (
	AuditCheckFailed    = "check-failed"
	AuditCheckLimit     = "check-limit"
	AuditFullReadFailed = "full-read-failed"
)

// authzUnavailable is the error body of a request whose authorisation failed (design §6.3).
const authzUnavailable = "authz-unavailable"

// WithCommitCheck runs check on every secure /api/ request after its handler
// and before its response is written. A false answer means check wrote its own
// response, which replaces the handler's.
func WithCommitCheck(check func(w http.ResponseWriter, r *http.Request) bool) HandlerOption {
	return func(c *handlerConfig) { c.commitCheck = check }
}

// WithAuthzAudit calls audit once for each secure /api/ request answered 503
// authz-unavailable, with AuditCheckFailed, AuditCheckLimit or AuditFullReadFailed.
func WithAuthzAudit(audit func(r *http.Request, reason string)) HandlerOption {
	return func(c *handlerConfig) { c.authzAudit = audit }
}

// commit is the response-commit protocol of secure mode (design §6.3): every
// /api/ response is built in a buffer and written only when no authorisation
// error was recorded and the commit check passes. Other paths pass through.
func commit(next http.Handler, a Authorizer, check func(http.ResponseWriter, *http.Request) bool, audit func(*http.Request, string)) http.Handler {
	full, _ := a.(FullReader)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		r, st := withAuthzState(r)
		fullFailed := false
		if full != nil {
			all, err := full.FullRead(r)
			if err != nil {
				ReportAuthzError(r, err)
				fullFailed = true
			}
			st.full = all && err == nil
		}
		buf := &bufferedResponse{header: http.Header{}}
		if !fullFailed {
			next.ServeHTTP(buf, r)
		}
		// An ended session answers 401 even when an authorisation error was recorded.
		if check != nil && !check(w, r) {
			return
		}
		if err := st.failed(); err != nil {
			if audit != nil {
				audit(r, auditReason(err, fullFailed))
			}
			w.Header().Set("Cache-Control", "no-store")
			writeErr(w, http.StatusServiceUnavailable, authzUnavailable)
			return
		}
		buf.writeTo(w)
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
