// Package session holds secure-mode sessions: the server side of a signed-in
// browser, its identity refresh, and the /auth/me and /auth/logout routes.
package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/obs"
)

// CookieName is the session cookie.
const CookieName = "snellius_session"

// Defaults for Options.
const (
	DefaultIdle     = 30 * time.Minute
	DefaultAbsolute = 8 * time.Hour
	DefaultMax      = 1000
)

const (
	sweepEvery = time.Minute
	// reviewTTL bounds how long a TokenReview answer stays fresh (design §4.4).
	reviewTTL = 15 * time.Minute
	// refreshAhead starts a background refresh this long before staleness.
	refreshAhead   = 2 * time.Minute
	refreshTimeout = 30 * time.Second
)

// Session end reasons in audit records.
const (
	reasonIdle     = "idle"
	reasonAbsolute = "absolute"
	reasonRefresh  = "refresh"
	reasonBound    = "bound"
)

// Refresher renews an identity with a refresh token; *oidc.Flow is one.
type Refresher interface {
	Refresh(ctx context.Context, refreshToken string) (oidc.Result, error)
}

// Options configure a Store. Zero durations and bound take the defaults.
type Options struct {
	Idle, Absolute time.Duration
	Max            int
	// Clock is nil for the real clock.
	Clock clock.WithTicker
	// Refresher renews identities; nil ends a session when its identity goes stale.
	Refresher Refresher
	Audit     oidc.Auditor
	// Metrics receives the snellius_sessions_active gauge; nil means a private registry.
	Metrics *obs.Registry
	// CookieSecure marks the session cookie Secure.
	CookieSecure bool
	// ClientID and RedirectURL build the logout origin check and the RP-initiated logout URL.
	ClientID, RedirectURL string
	// EndSession returns the issuer's end_session_endpoint, or "" when it has none.
	EndSession func() string
	// Logger receives refresh failure steps, never a token; nil means slog.Default().
	Logger *slog.Logger
}

// Store holds sessions in memory, keyed by a hash of the cookie value.
type Store struct {
	o      Options
	clk    clock.WithTicker
	log    *slog.Logger
	active *obs.GaugeVec
	// origin is the public origin of the redirect URL; "" refuses every logout.
	origin string

	mu       sync.Mutex
	sessions map[string]*session
	// ended holds an end notice per ended session's key, for Absolute after the end and at most Max of them.
	ended map[string]time.Time
}

// session is one signed-in browser. Its refresh token never leaves the server.
type session struct {
	key, auditID     string
	info             Info
	refreshToken     string
	expiry, reviewed time.Time
	created, seen    time.Time
	// refreshing is closed when the refresh in flight completes; nil when none runs.
	refreshing chan struct{}
}

// New builds a Store. Start its sweeper with Run.
func New(o Options) *Store {
	if o.Idle <= 0 {
		o.Idle = DefaultIdle
	}
	if o.Absolute <= 0 {
		o.Absolute = DefaultAbsolute
	}
	if o.Max <= 0 {
		o.Max = DefaultMax
	}
	if o.Metrics == nil {
		o.Metrics = obs.NewRegistry()
	}
	s := &Store{o: o, clk: o.Clock, log: o.Logger, origin: originOf(o.RedirectURL), sessions: map[string]*session{}, ended: map[string]time.Time{},
		active: o.Metrics.Gauge("snellius_sessions_active", "Signed-in sessions held in memory.")}
	if s.clk == nil {
		s.clk = clock.RealClock{}
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	s.active.Set(0)
	return s
}

// Complete starts a session for a sign-in, replacing the browser's previous
// one or its end notice, then redirects to the return path. It implements oidc.Completer.
func (s *Store) Complete(w http.ResponseWriter, r *http.Request, res oidc.Result) {
	value := newCookieValue()
	now := s.clk.Now()
	sess := &session{key: hashKey(value), auditID: newAuditID(), refreshToken: res.RefreshToken,
		expiry: res.Expiry, reviewed: now, created: now, seen: now,
		info: Info{Identity: res.Identity, Issuer: res.Issuer, Subject: res.Subject}}
	sess.info.AuditID = sess.auditID
	s.mu.Lock()
	if c, err := r.Cookie(CookieName); err == nil {
		oldKey := hashKey(c.Value)
		if old := s.sessions[oldKey]; old != nil {
			s.remove(old)
		}
		delete(s.ended, oldKey)
	}
	if len(s.sessions) >= s.o.Max {
		s.evictOldestIdle(r.Context())
	}
	s.sessions[sess.key] = sess
	s.updateGauge()
	s.mu.Unlock()
	s.emit(r.Context(), auditRecord(r, sess, "login", "success", ""))
	http.SetCookie(w, s.cookie(value, 0))
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, res.ReturnPath, http.StatusFound)
}

// Run sweeps expired sessions every minute until ctx is done.
func (s *Store) Run(ctx context.Context) {
	t := s.clk.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C():
			s.sweep()
		}
	}
}

// sweep ends idle, absolute and stale-without-refresh sessions and forgets old end notices.
func (s *Store) sweep() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.clk.Now()
	for _, sess := range s.sessions {
		if why := s.expired(sess, now); why != "" {
			s.end(context.Background(), sess, why)
		} else if !now.Before(freshUntil(sess)) && !s.canRefresh(sess) {
			s.end(context.Background(), sess, reasonRefresh)
		}
	}
	for k, at := range s.ended {
		if s.noticeExpired(at, now) {
			delete(s.ended, k)
		}
	}
}

// noticeExpired reports whether an end notice written at at has lived its Absolute at now.
func (s *Store) noticeExpired(at, now time.Time) bool {
	return now.Sub(at) >= s.o.Absolute
}

// expired returns the reason sess has ended at now, or "".
func (s *Store) expired(sess *session, now time.Time) string {
	switch {
	case now.Sub(sess.created) >= s.o.Absolute:
		return reasonAbsolute
	case now.Sub(sess.seen) >= s.o.Idle:
		return reasonIdle
	}
	return ""
}

// freshUntil is the earlier of the ID token's exp and the last review plus reviewTTL.
func freshUntil(sess *session) time.Time {
	r := sess.reviewed.Add(reviewTTL)
	if sess.expiry.Before(r) {
		return sess.expiry
	}
	return r
}

func (s *Store) canRefresh(sess *session) bool {
	return s.o.Refresher != nil && sess.refreshToken != ""
}

// end removes sess with an end notice for its cookie and audits why. s.mu is held.
func (s *Store) end(ctx context.Context, sess *session, reason string) {
	s.remove(sess)
	if len(s.ended) >= s.o.Max {
		s.forgetOldestNotice()
	}
	s.ended[sess.key] = s.clk.Now()
	rec := auditRecord(nil, sess, "session-ended", "failure", reason)
	if reason == reasonBound {
		rec.Action = "session-evicted"
	}
	s.emit(ctx, rec)
}

// remove deletes sess without a notice. s.mu is held.
func (s *Store) remove(sess *session) {
	delete(s.sessions, sess.key)
	s.updateGauge()
}

// evictOldestIdle ends the session unused for longest. s.mu is held.
func (s *Store) evictOldestIdle(ctx context.Context) {
	var oldest *session
	for _, sess := range s.sessions {
		if oldest == nil || sess.seen.Before(oldest.seen) {
			oldest = sess
		}
	}
	if oldest != nil {
		s.end(ctx, oldest, reasonBound)
	}
}

// forgetOldestNotice drops the oldest end notice; its cookie then hears sign-in-required. s.mu is held.
func (s *Store) forgetOldestNotice() {
	var key string
	var at time.Time
	for k, t := range s.ended {
		if key == "" || t.Before(at) {
			key, at = k, t
		}
	}
	delete(s.ended, key)
}

func (s *Store) updateGauge() { s.active.Set(float64(len(s.sessions))) }

func (s *Store) emit(ctx context.Context, rec audit.Record) {
	if s.o.Audit != nil {
		s.o.Audit.Emit(ctx, rec)
	}
}

// auditRecord is a security record about sess; r may be nil for the sweeper and refreshes.
func auditRecord(r *http.Request, sess *session, action, outcome, reason string) audit.Record {
	var rec audit.Record
	if r != nil {
		rec = audit.FromRequest(r)
		if rec.Request.Route == "" {
			rec.Request.Route = r.URL.Path
		}
	}
	rec.Level, rec.Action, rec.Outcome, rec.Reason = audit.LevelSecurity, action, outcome, reason
	rec.User = audit.Identity{Issuer: sess.info.Issuer, Subject: sess.info.Subject}
	rec.Kube = audit.Kube{Username: sess.info.Identity.Username, Groups: sess.info.Identity.Groups}
	rec.Session = sess.auditID
	return rec
}

func (s *Store) cookie(value string, maxAge int) *http.Cookie {
	//nolint:gosec // Secure follows --cookie-secure, true by default
	return &http.Cookie{Name: CookieName, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.o.CookieSecure,
		SameSite: http.SameSiteLaxMode}
}

// hashKey keys the store by a hash, so the cookie value itself is never held.
func hashKey(value string) string {
	sum := sha256.Sum256([]byte(value))
	return string(sum[:])
}

// newCookieValue returns 256 random bits, base64url encoded.
func newCookieValue() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// newAuditID returns 128 random bits in hex, unrelated to the cookie value.
func newAuditID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
