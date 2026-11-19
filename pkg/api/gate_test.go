package api

import (
	"encoding/json"
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

// A nil Authorizer in secure mode is DenyAll, never AllowAll: a handler with data
// shows a viewer nothing it would show under AllowAll.
func TestSecureModeNilAuthorizerFailsClosed(t *testing.T) {
	routes := func(opts ...HandlerOption) Page[json.RawMessage] {
		_, srv, _ := startWith(t, setup{opts: opts}, world()...)
		insts := get[[]InstallSummary](t, srv, "/api/v1/installs", 200)
		if len(insts) == 0 {
			t.Fatal("no install in the world")
		}
		return get[Page[json.RawMessage]](t, srv, "/api/v1/installs/"+insts[0].ID+"/routes", 200)
	}
	if open := routes(); len(open.Items) == 0 {
		t.Fatal("local mode lists no routes; the comparison proves nothing")
	}
	if closed := routes(WithSecureMode()); len(closed.Items) != 0 || closed.Total != 0 {
		t.Errorf("secure mode with a nil authoriser lists %d routes (total %d), want none", len(closed.Items), closed.Total)
	}
}

// Secure mode never answers 503 secure-mode-incomplete, runs extension mounts, and serves the SPA and probes.
func TestSecureModeServesAPI(t *testing.T) {
	srv, calls := gateServer(t, WithSecureMode())
	for _, p := range gatePaths {
		if _, body := gateGet(t, srv.URL+p); strings.Contains(body, "secure-mode-incomplete") {
			t.Errorf("%s: got %q", p, body)
		}
	}
	if *calls != 1 {
		t.Errorf("extension handler ran %d times, want 1", *calls)
	}
	for _, p := range []string{"/healthz", "/"} {
		if code, _ := gateGet(t, srv.URL+p); code != http.StatusOK {
			t.Errorf("%s: got %d", p, code)
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
