package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HostGuard serves only requests whose Host is localhost, 127.0.0.1, [::1]
// (any port) or one of allowed (any port); others get 421. A request with an
// Origin that is not its own http(s) origin gets 403. It never sets CORS headers.
func HostGuard(allowed []string, next http.Handler) http.Handler {
	ok := map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}
	for _, a := range allowed {
		if a = strings.ToLower(strings.TrimSpace(a)); a != "" {
			ok[a] = true
		}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !ok[hostName(r.Host)] {
			http.Error(w, "misdirected request", http.StatusMisdirectedRequest)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !sameOrigin(o, r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin reports whether origin is a well-formed http(s) origin whose
// scheme, host and effective port equal those of r. No proxy is trusted, so
// the request scheme is https only over TLS.
func sameOrigin(origin string, r *http.Request) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if !strings.EqualFold(u.Scheme, scheme) {
		return false
	}
	oh, op := hostPort(u.Host, scheme)
	rh, rp := hostPort(r.Host, scheme)
	return oh == rh && op == rp
}

// hostPort splits a host[:port] value into its lowercase host and its port,
// using scheme's default port when none is given.
func hostPort(hostport, scheme string) (string, string) {
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		h, p = strings.Trim(hostport, "[]"), ""
	}
	if p == "" {
		p = "80"
		if scheme == "https" {
			p = "443"
		}
	}
	return strings.ToLower(h), p
}

// hostName returns the lowercase host of a Host header value without its port.
func hostName(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = strings.Trim(hostport, "[]")
	}
	return strings.ToLower(h)
}
