package session

import (
	"context"
	"encoding/json"
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
}

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
// origin. It deletes the session and answers the next URL: the issuer's
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
		s.mu.Lock()
		sess := s.sessions[hashKey(c.Value)]
		if sess != nil {
			s.remove(sess)
		}
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
	if c != nil {
		http.SetCookie(w, s.cookie("", -1))
	}
	reason := "sign-in-required"
	if st == stateEnded {
		reason = "session-ended"
	}
	writeJSON(w, http.StatusUnauthorized, map[string]string{"error": reason, "login": "/auth/login?return=" + url.QueryEscape(returnPath(r))})
	return Info{}, false
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
