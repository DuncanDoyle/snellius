package session

import (
	"context"
	"errors"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// state is what a request's cookie resolves to.
type state int

const (
	stateNone  state = iota // no session: sign-in required
	stateEnded              // the session ended and its end notice is still held
	stateOK
)

// lookup resolves a cookie value to a fresh identity. A stale identity waits
// for the session's one refresh; a request is never served from a stale one.
func (s *Store) lookup(ctx context.Context, value string) (Info, state) {
	key := hashKey(value)
	for {
		s.mu.Lock()
		sess := s.sessions[key]
		if sess == nil {
			st := s.notice(key)
			s.mu.Unlock()
			return Info{}, st
		}
		now := s.clk.Now()
		if why := s.expired(sess, now); why != "" {
			s.end(ctx, sess, why)
			st := s.notice(key)
			s.mu.Unlock()
			return Info{}, st
		}
		fresh := freshUntil(sess)
		if now.Before(fresh) {
			sess.seen = now
			if !now.Before(fresh.Add(-refreshAhead)) && s.canRefresh(sess) {
				s.startRefresh(sess)
			}
			info := sess.info
			s.mu.Unlock()
			return info, stateOK
		}
		if !s.canRefresh(sess) {
			s.end(ctx, sess, reasonRefresh)
			st := s.notice(key)
			s.mu.Unlock()
			return Info{}, st
		}
		done := s.startRefresh(sess)
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return Info{}, stateNone
		}
	}
}

// notice reports the cookie's end notice on every request until it expires or
// a sign-in or logout deletes it. s.mu is held.
func (s *Store) notice(key string) state {
	at, ok := s.ended[key]
	if !ok {
		return stateNone
	}
	if s.noticeExpired(at, s.clk.Now()) {
		delete(s.ended, key)
		return stateNone
	}
	return stateEnded
}

// startRefresh starts the session's refresh unless one runs, and returns its done channel. s.mu is held.
func (s *Store) startRefresh(sess *session) <-chan struct{} {
	if sess.refreshing == nil {
		sess.refreshing = make(chan struct{})
		go s.refresh(sess, sess.refreshToken, sess.refreshing)
	}
	return sess.refreshing
}

// refresh renews sess's identity. The new ID token must keep iss and sub; any
// failure ends the session. A session removed meanwhile (logout) stays removed.
func (s *Store) refresh(sess *session, refreshToken string, done chan struct{}) {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	res, err := s.o.Refresher.Refresh(ctx, refreshToken)
	cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	defer close(done)
	sess.refreshing = nil
	if s.sessions[sess.key] != sess {
		return
	}
	now := s.clk.Now()
	switch {
	case err != nil:
		s.endRefresh(sess, failureCategory(err))
		return
	case res.Issuer != sess.info.Issuer:
		s.endRefresh(sess, "issuer-changed")
		return
	case res.Subject != sess.info.Subject:
		s.endRefresh(sess, "subject-changed")
		return
	}
	sess.info.Identity = res.Identity
	sess.idToken, sess.access, sess.expiry, sess.reviewed = res.IDToken, res.AccessToken, res.Expiry, now
	if res.RefreshToken != "" {
		sess.refreshToken = res.RefreshToken
	}
	if !now.Before(freshUntil(sess)) {
		s.endRefresh(sess, "token-expired")
	}
}

// endRefresh ends sess after a failed refresh, logging only a fixed category. s.mu is held.
func (s *Store) endRefresh(sess *session, category string) {
	s.log.Warn("session refresh failed; the session ends", "category", category, "session", sess.auditID)
	s.end(context.Background(), sess, reasonRefresh)
}

// refreshCategories are the oidc failures logged by name; any other error is "refresh-failed".
var refreshCategories = []error{oidc.ErrNoRefreshToken, oidc.ErrRefreshUnavailable, oidc.ErrTokenRefresh, oidc.ErrIDTokenInvalid,
	oidc.ErrTokenReviewRefused, oidc.ErrTokenReviewFailed}

// failureCategory names err by its category, never by its text.
func failureCategory(err error) string {
	for _, c := range refreshCategories {
		if errors.Is(err, c) {
			return c.Error()
		}
	}
	return "refresh-failed"
}
