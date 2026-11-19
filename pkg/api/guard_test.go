package api

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func guardStatus(t *testing.T, allowed []string, host, origin string) int {
	t.Helper()
	h := HostGuard(allowed, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = host
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header set")
	}
	return rec.Code
}

func TestHostGuardRebinding(t *testing.T) {
	allowed := []string{"snellius.internal"}
	cases := map[string]int{
		"evil.example":         http.StatusMisdirectedRequest,
		"evil.example:18182":   http.StatusMisdirectedRequest,
		"":                     http.StatusMisdirectedRequest,
		"localhost:18182":      http.StatusOK,
		"localhost":            http.StatusOK,
		"127.0.0.1:8080":       http.StatusOK,
		"[::1]:8080":           http.StatusOK,
		"[::1]":                http.StatusOK,
		"snellius.internal":    http.StatusOK,
		"snellius.internal:99": http.StatusOK,
	}
	for host, want := range cases {
		if got := guardStatus(t, allowed, host, ""); got != want {
			t.Errorf("Host %q = %d, want %d", host, got, want)
		}
	}
}

func TestHostGuardCrossOrigin(t *testing.T) {
	if got := guardStatus(t, nil, "localhost:8080", "http://evil.example"); got != http.StatusForbidden {
		t.Errorf("cross origin = %d, want 403", got)
	}
	if got := guardStatus(t, nil, "localhost:8080", "http://localhost:3000"); got != http.StatusForbidden {
		t.Errorf("other port origin = %d, want 403", got)
	}
	if got := guardStatus(t, nil, "localhost:8080", "http://localhost:8080"); got != http.StatusOK {
		t.Errorf("same origin = %d, want 200", got)
	}
	if got := guardStatus(t, nil, "localhost:8080", "null"); got != http.StatusForbidden {
		t.Errorf("null origin = %d, want 403", got)
	}
}

// Origin must be a well-formed http(s) origin equal to the request's own scheme, host and port.
func TestHostGuardOriginMatchesRequestOrigin(t *testing.T) {
	cases := []struct {
		host, origin string
		tls          bool
		want         int
	}{
		{"localhost:8080", "", false, http.StatusOK},
		{"localhost:8080", "http://localhost:8080", false, http.StatusOK},
		{"localhost:8080", "HTTP://LOCALHOST:8080", false, http.StatusOK},
		{"localhost:80", "http://localhost", false, http.StatusOK},
		{"localhost", "http://localhost:80", false, http.StatusOK},
		{"localhost", "https://localhost", true, http.StatusOK},
		{"localhost:443", "https://localhost", true, http.StatusOK},
		{"localhost:8080", "https://localhost:8080", false, http.StatusForbidden},
		{"localhost:8080", "http://localhost:8080", true, http.StatusForbidden},
		{"localhost", "https://localhost:80", false, http.StatusForbidden},
		{"localhost:8080", "//localhost:8080", false, http.StatusForbidden},
		{"localhost:8080", "ftp://localhost:8080", false, http.StatusForbidden},
		{"localhost:8080", "null", false, http.StatusForbidden},
		{"localhost:8080", "http://localhost:8080/path", false, http.StatusForbidden},
		{"localhost:8080", "http://user@localhost:8080", false, http.StatusForbidden},
		{"localhost:8080", "http://127.0.0.1:8080", false, http.StatusForbidden},
	}
	h := HostGuard(nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Host = c.host
		if c.tls {
			req.TLS = &tls.ConnectionState{}
		}
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %q, TLS %v, Origin %q = %d, want %d", c.host, c.tls, c.origin, rec.Code, c.want)
		}
	}
}
