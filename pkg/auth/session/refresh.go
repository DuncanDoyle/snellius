package session

import "context"

// state is what a request's cookie resolves to.
type state int

const (
	stateNone  state = iota // no session: sign-in required
	stateEnded              // the session ended since the last request
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
			st := s.takeNotice(key)
			s.mu.Unlock()
			return Info{}, st
		}
		now := s.clk.Now()
		if why := s.expired(sess, now); why != "" {
			s.end(ctx, sess, why)
			st := s.takeNotice(key)
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
			st := s.takeNotice(key)
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

// takeNotice reports an end notice once. s.mu is held.
func (s *Store) takeNotice(key string) state {
	if _, ok := s.ended[key]; ok {
		delete(s.ended, key)
		return stateEnded
	}
	return stateNone
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
		s.log.Warn("session refresh failed; the session ends", "step", err.Error(), "session", sess.auditID)
		s.end(context.Background(), sess, reasonRefresh)
		return
	case res.Issuer != sess.info.Issuer || res.Subject != sess.info.Subject:
		s.log.Warn("session refresh returned another iss or sub; the session ends", "session", sess.auditID)
		s.end(context.Background(), sess, reasonRefresh)
		return
	}
	sess.info.Identity = res.Identity
	sess.idToken, sess.access, sess.expiry, sess.reviewed = res.IDToken, res.AccessToken, res.Expiry, now
	if res.RefreshToken != "" {
		sess.refreshToken = res.RefreshToken
	}
	if !now.Before(freshUntil(sess)) {
		s.log.Warn("session refresh returned an expired ID token; the session ends", "session", sess.auditID)
		s.end(context.Background(), sess, reasonRefresh)
	}
}
