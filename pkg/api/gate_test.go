package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

var gatePaths = []string{
	"/api/v1/meta",
	"/api/v1/installs",
	"/api/v1/installs/x/overview",
	"/api/v1/installs/x/findings",
	"/api/v1/installs/x/routes/g/k/ns/n",
	"/api/v1/installs/x/resources/g/k/ns/n",
	"/api/v1/installs/x/backends",
	"/api/v1/unknown",
	"/api/ext/probe/data",
}

func gateServer(t *testing.T, opts ...HandlerOption) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	ext := ExtMount{Name: "probe", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
	})}
	opts = append([]HandlerOption{WithExtMounts(ext)}, opts...)
	srv := httptest.NewServer(NewHandler(staticModel{}, fstest.MapFS{"index.html": {Data: []byte("spa")}}, nil, opts...))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func gateGet(t *testing.T, url string) (int, string) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, strings.TrimSpace(string(b))
}

// An incomplete secure mode answers every /api/ route with 503 and runs nothing behind it.
func TestSecureModeGateBlocksEveryAPIRoute(t *testing.T) {
	srv, calls := gateServer(t, WithSecureMode(false))
	for _, p := range gatePaths {
		code, body := gateGet(t, srv.URL+p)
		if code != http.StatusServiceUnavailable || body != `{"error":"secure-mode-incomplete"}` {
			t.Errorf("%s: got %d %q", p, code, body)
		}
	}
	if *calls != 0 {
		t.Errorf("extension handler ran %d times behind the gate", *calls)
	}
	for _, p := range []string{"/healthz", "/"} {
		if code, _ := gateGet(t, srv.URL+p); code == http.StatusServiceUnavailable {
			t.Errorf("%s must stay served, got 503", p)
		}
	}
}

// Local mode serves as before, and an ext mount runs.
func TestLocalModeUnchanged(t *testing.T) {
	srv, calls := gateServer(t)
	if code, _ := gateGet(t, srv.URL+"/api/ext/probe/data"); code != http.StatusOK || *calls != 1 {
		t.Errorf("ext: code %d, calls %d", code, *calls)
	}
	if code, _ := gateGet(t, srv.URL+"/api/v1/unknown"); code != http.StatusNotFound {
		t.Errorf("unknown route: %d", code)
	}
}

// DenyAll denies every ref.
func TestDenyAllDenies(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if (DenyAll{}).Authorize(r, objects.Ref{}) {
		t.Fatal("DenyAll allowed a ref")
	}
	if allowsAll(DenyAll{}) {
		t.Fatal("DenyAll must not take the AllowAll fast path")
	}
}
