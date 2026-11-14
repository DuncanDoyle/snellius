package api

import (
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
