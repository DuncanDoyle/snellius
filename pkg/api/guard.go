package api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// HostGuard serves only requests whose Host is localhost, 127.0.0.1, [::1]
// (any port) or one of allowed (any port); others get 421. A request whose
// Origin differs from its Host gets 403. It never sets CORS headers.
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
		if o := r.Header.Get("Origin"); o != "" {
			u, err := url.Parse(o)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				http.Error(w, "cross-origin request refused", http.StatusForbidden)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostName returns the lowercase host of a Host header value without its port.
func hostName(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		h = strings.Trim(hostport, "[]")
	}
	return strings.ToLower(h)
}
