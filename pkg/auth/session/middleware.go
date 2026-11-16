package session

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// Info is the signed-in identity a request carries.
type Info struct {
	Identity oidc.Identity
	// Issuer and Subject name the person; AuditID is the session's opaque audit ID.
	Issuer, Subject, AuditID string
	// gen counts the refreshes that changed Identity; the commit check compares it.
	gen uint64
}

// ErrStale is the commit check's answer while the session's identity awaits its refresh.
var ErrStale = errors.New("session identity awaits its refresh")

type ctxKey struct{}

func withInfo(ctx context.Context, i Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, i)
}

// FromContext returns the session identity of a request that passed Middleware.
func FromContext(ctx context.Context) (Info, bool) {
	i, ok := ctx.Value(ctxKey{}).(Info)
	return i, ok
}

// IdentityFrom returns the Kubernetes identity of a request that passed Middleware.
func IdentityFrom(ctx context.Context) (oidc.Identity, bool) {
	i, ok := FromContext(ctx)
	return i.Identity, ok
}

// Middleware lets an /api/ request through only with a fresh session, carrying
// its identity in the context; others get 401. Other paths pass unchanged.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		info, ok := s.fromRequest(w, r)
		if !ok {
			return
		}
		next.ServeHTTP(w, r.WithContext(withInfo(r.Context(), info)))
	})
}

// StillSignedIn is the API's commit check for r, a request that passed Middleware.
// It answers r while the session holds r's identity and that identity is fresh,
// a request carrying the current identity when a refresh replaced it, and
// ErrStale while the identity awaits a refresh. When the session ended it writes
// the 401 a new request with the cookie would get and answers nil. It never refreshes or waits.
func (s *Store) StillSignedIn(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
	info, ok := FromContext(r.Context())
	c, err := r.Cookie(CookieName)
	if !ok || err != nil {
		s.writeUnauthorized(w, r, stateNone, nil)
		return nil, nil
	}
	key := hashKey(c.Value)
	s.mu.Lock()
	cur, st := s.current(r.Context(), key, info.AuditID)
	s.mu.Unlock()
	switch {
	case st == stateStale:
		return r, ErrStale
	case st == stateOK && cur.gen == info.gen:
		return r, nil
	case st == stateOK:
		return r.WithContext(withInfo(r.Context(), cur)), nil
	}
	s.writeUnauthorized(w, r, st, c)
	return nil, nil
}

// current resolves key without refreshing: the identity while the session with
// auditID is held, unexpired and fresh; stateStale while it awaits a refresh;
// else the cookie's end notice. s.mu is held.
func (s *Store) current(ctx context.Context, key, auditID string) (Info, state) {
	sess := s.sessions[key]
	if sess == nil || sess.auditID != auditID {
		return Info{}, s.notice(key)
	}
	now := s.clk.Now()
	why := s.expired(sess, now)
	if why == "" && !now.Before(freshUntil(sess)) {
		if s.canRefresh(sess) {
			return Info{}, stateStale
		}
		why = reasonRefresh
	}
	if why == "" {
		return sess.info, stateOK
	}
	s.end(ctx, sess, why)
	return Info{}, s.notice(key)
}

// Me serves GET /auth/me: the session's username and groups.
func (s *Store) Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	info, ok := s.fromRequest(w, r)
	if !ok {
		return
	}
	groups := info.Identity.Groups
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, struct {
		Username string   `json:"username"`
		Groups   []string `json:"groups"`
	}{info.Identity.Username, groups})
}

// Logout serves POST /auth/logout. The Origin header must be the redirect URL's
// origin. It deletes the session or its end notice and answers the next URL: the issuer's
// end-session endpoint without an ID token hint, else "/".
func (s *Store) Logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if o := r.Header.Get("Origin"); s.origin == "" || o != s.origin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "origin-refused"})
		return
	}
	if c, err := r.Cookie(CookieName); err == nil {
		key := hashKey(c.Value)
		s.mu.Lock()
		sess := s.sessions[key]
		if sess != nil {
			s.remove(sess)
		}
		delete(s.ended, key)
		s.mu.Unlock()
		if sess != nil {
			s.emit(r.Context(), auditRecord(r, sess, "logout", "success", ""))
		}
	}
	http.SetCookie(w, s.cookie("", -1))
	writeJSON(w, http.StatusOK, map[string]string{"next": s.logoutURL()})
}

// logoutURL is the RP-initiated logout URL, or "/" without an end-session endpoint.
func (s *Store) logoutURL() string {
	if s.o.EndSession == nil {
		return "/"
	}
	u, err := url.Parse(s.o.EndSession())
	if err != nil || u.Host == "" || s.origin == "" {
		return "/"
	}
	q := u.Query()
	// No ID token reaches the browser, even one written into the discovered endpoint.
	q.Del("id_token_hint")
	q.Set("client_id", s.o.ClientID)
	q.Set("post_logout_redirect_uri", s.origin+"/")
	u.RawQuery = q.Encode()
	return u.String()
}

// fromRequest resolves the request's session, or writes the 401 and reports false.
func (s *Store) fromRequest(w http.ResponseWriter, r *http.Request) (Info, bool) {
	st := stateNone
	var info Info
	c, err := r.Cookie(CookieName)
	if err == nil {
		info, st = s.lookup(r.Context(), c.Value)
	}
	if st == stateOK {
		return info, true
	}
	s.writeUnauthorized(w, r, st, c)
	return Info{}, false
}

// writeUnauthorized writes the 401 of a cookie in state st.
func (s *Store) writeUnauthorized(w http.ResponseWriter, r *http.Request, st state, c *http.Cookie) {
	// session-ended keeps the cookie, so every request with it hears the same; it maps to no session.
	reason := "session-ended"
	if st != stateEnded {
		reason = "sign-in-required"
		if c != nil {
			http.SetCookie(w, s.cookie("", -1))
		}
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": reason, "login": "/auth/login?return=" + url.QueryEscape(returnPath(r))})
}

// returnPath is the page that made the request, from its Referer; the login route checks it again.
func returnPath(r *http.Request) string {
	u, err := url.Parse(r.Referer())
	if err != nil || !strings.HasPrefix(u.Path, "/") {
		return "/"
	}
	return u.RequestURI()
}

// originOf returns scheme://host[:port] of an absolute URL, or "".
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
