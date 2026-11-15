package api

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/DuncanDoyle/snellius/pkg/objects"
)

const authzUnavailableBody = `{"error":"authz-unavailable"}` + "\n"

var errOutage = errors.New("SAR unavailable")

// errAuthz is an ErrAuthorizer that denies deny's refs and fails its failAt-th
// call (1-based; 0 never). With spend set, each call spends an uncached check.
type errAuthz struct {
	mu     sync.Mutex
	deny   func(objects.Ref) bool
	failAt int
	spend  bool
	calls  int
}

func (a *errAuthz) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	if a.spend {
		if err := SpendCheck(r); err != nil {
			return false, err
		}
	}
	if n == a.failAt {
		return false, errOutage
	}
	return a.deny == nil || !a.deny(ref), nil
}

// Authorize reports an error for the request, as the SAR authoriser does.
func (a *errAuthz) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, err := a.AuthorizeErr(r, ref)
	if err != nil {
		ReportAuthzError(r, err)
		return false
	}
	return ok
}

func (a *errAuthz) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

// fullAuthz is errAuthz with a full-read answer.
type fullAuthz struct {
	*errAuthz
	all   bool
	err   error
	reads int
}

func (a *fullAuthz) FullRead(*http.Request) (bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reads++
	return a.all, a.err
}

func (a *fullAuthz) readCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.reads
}

// listAuthz adds CanList to errAuthz, failing with err when set.
type listAuthz struct {
	*errAuthz
	err error
}

func (a listAuthz) CanList(_ *http.Request, gk objects.GroupKind, ns string) (bool, error) {
	return gk.Kind == "HTTPRoute" && ns == "shop", a.err
}

// secureServer serves src in complete secure mode with a.
func secureServer(t *testing.T, src ModelSource, a Authorizer, opts ...HandlerOption) *httptest.Server {
	t.Helper()
	opts = append([]HandlerOption{WithSecureMode(true)}, opts...)
	srv := httptest.NewServer(NewHandler(src, fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}}, a, opts...))
	t.Cleanup(srv.Close)
	return srv
}

// fetchRaw returns the status, headers and body of GET path.
func fetchRaw(t *testing.T, srv *httptest.Server, path string) (int, http.Header, string) {
	t.Helper()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, string(b)
}

// wantUnavailable checks that path answered exactly the authz-unavailable 503.
func wantUnavailable(t *testing.T, srv *httptest.Server, path string) {
	t.Helper()
	code, h, body := fetchRaw(t, srv, path)
	if code != http.StatusServiceUnavailable || body != authzUnavailableBody || h.Get("Cache-Control") != "no-store" ||
		h.Get("Content-Type") != "application/json" || h.Get("Collected-At") != "" || h.Get("X-Partial") != "" {
		t.Errorf("GET %s: %d %v %q, want only the authz-unavailable 503", path, code, h, body)
	}
}

// commitLists are the list routes over visWorld.
var commitLists = []string{
	"/api/v1/installs", base + "/overview", base + "/health", base + "/findings", base + "/diagnostics", base + "/processes",
	listBase + "/routes?limit=200", listBase + "/gateways", listBase + "/listenersets", listBase + "/paths", listBase + "/backends",
}

// An error on any ref of a list answers the 503 and nothing of the list.
func TestCommitOutageNeverPartial(t *testing.T) {
	e, _, _ := startWith(t, setup{}, visWorld()...)
	for _, p := range commitLists {
		counter := &errAuthz{deny: hiddenSome}
		if code, _, _ := fetchRaw(t, secureServer(t, e, counter), p); code != http.StatusOK {
			t.Fatalf("GET %s without an outage: %d", p, code)
		}
		m := counter.count()
		if m == 0 {
			t.Errorf("GET %s made no checks", p)
			continue
		}
		for _, n := range slices.Compact([]int{1, (m + 1) / 2, m}) {
			a := &errAuthz{deny: hiddenSome, failAt: n}
			wantUnavailable(t, secureServer(t, e, a), p)
			if a.count() != n {
				t.Errorf("GET %s failing at %d: %d checks; later checks must not ask", p, n, a.count())
			}
		}
	}
}

// A route that answers 404 on denial answers 503 when the check fails.
func TestCommitOutageNever404(t *testing.T) {
	e, _, _ := startWith(t, setup{}, visWorld()...)
	cases := []struct {
		path string
		deny int
	}{
		{backendsURL + "/gateway.kgateway.dev/Backend/shop/be-x", http.StatusNotFound},
		{routeDetail + "shop/secret", http.StatusNotFound},
		{routeDetail + "shop/secret/delegation", http.StatusNotFound},
		{lsDetail + "infra/ls1", http.StatusNotFound},
		{base + "/resources/gateway.networking.k8s.io/HTTPRoute/shop/secret", http.StatusNotFound},
	}
	for _, f := range get[HealthResponse](t, secureServer(t, e, &errAuthz{deny: hiddenSome}), base+"/health", 200).Findings {
		if f.Resource != nil && hiddenSome(f.Resource.Ref) {
			cases = append(cases, struct {
				path string
				deny int
			}{base + "/findings/" + f.ID, http.StatusOK})
			break
		}
	}
	if len(cases) != 6 {
		t.Fatal("visWorld has no finding on a denied resource")
	}
	for _, c := range cases {
		if code, _, body := fetchRaw(t, secureServer(t, e, &errAuthz{deny: hiddenSome}), c.path); code != c.deny {
			t.Errorf("GET %s denied: %d %s, want %d", c.path, code, body, c.deny)
		}
		wantUnavailable(t, secureServer(t, e, &errAuthz{deny: hiddenSome, failAt: 1}), c.path)
	}
}

// refsExt asks about n distinct refs through Visible and writes how many it may see.
func refsExt(a Authorizer, n int) ExtMount {
	return ExtMount{Name: "refs", Pattern: "GET /n", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		vis, _ := Visible(r, a, items, func(i int) objects.Ref { return objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: fmt.Sprint(i)} })
		WriteJSON(w, len(vis))
	})}
}

// The 500th uncached check is answered; the 501st fails the request.
func TestCommitCheckLimit(t *testing.T) {
	for _, n := range []int{checkLimit, checkLimit + 1} {
		a := &errAuthz{spend: true}
		var reasons []string
		srv := secureServer(t, staticModel{}, a, WithExtMounts(refsExt(a, n)), WithAuthzAudit(func(_ *http.Request, why string) { reasons = append(reasons, why) }))
		if n == checkLimit {
			if code, _, body := fetchRaw(t, srv, "/api/ext/refs/n"); code != http.StatusOK || body != "500\n" {
				t.Errorf("%d checks: %d %q", n, code, body)
			}
			continue
		}
		wantUnavailable(t, srv, "/api/ext/refs/n")
		if !slices.Equal(reasons, []string{AuditCheckLimit}) {
			t.Errorf("audit %v, want check-limit", reasons)
		}
	}
}

// Under a full read no check is made and every answer is AllowAll's byte for byte.
func TestCommitFullReadSkipsChecks(t *testing.T) {
	e, _, _ := startWith(t, setup{}, visWorld()...)
	full := &fullAuthz{errAuthz: &errAuthz{deny: hiddenSome}, all: true}
	srv, ref := secureServer(t, e, full), secureServer(t, e, AllowAll{})
	for _, p := range append(slices.Clone(visRequests), listBase+"/paths", listBase+"/backends", backendsURL+"/gateway.kgateway.dev/Backend/shop/be-x") {
		gc, _, got := fetchRaw(t, srv, p)
		wc, _, want := fetchRaw(t, ref, p)
		if gc != wc || got != want {
			t.Errorf("GET %s differs from AllowAll: %d %d\n%s\n%s", p, gc, wc, got, want)
		}
		if !bytes.Equal(wire(t, srv, p), wire(t, ref, p)) {
			t.Errorf("GET %s: headers differ from AllowAll", p)
		}
	}
	if n := full.count(); n != 0 {
		t.Errorf("%d checks under a full read", n)
	}
	if full.reads == 0 {
		t.Error("FullRead was never asked")
	}
}

// A full-read error answers 503 for a route that asks a population, with no
// check after it; a route that asks none never runs the full read (D-100).
func TestCommitFullReadError(t *testing.T) {
	ext := ExtMount{Name: "probe", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, "data")
	})}
	full := &fullAuthz{errAuthz: &errAuthz{}, err: errOutage}
	srv := secureServer(t, staticModel{}, full, WithExtMounts(ext, refsExt(full, 3)))
	for _, p := range []string{"/api/ext/probe/data", "/api/v1/meta"} {
		if code, _, _ := fetchRaw(t, srv, p); code != http.StatusOK {
			t.Errorf("GET %s: %d without a population", p, code)
		}
	}
	if n := full.readCount(); n != 0 {
		t.Errorf("%d full reads without a population", n)
	}
	wantUnavailable(t, srv, "/api/ext/refs/n")
	if n := full.readCount(); n != 1 || full.count() != 0 {
		t.Errorf("%d full reads, %d checks", n, full.count())
	}
	if code, _, body := fetchRaw(t, srv, "/"); code != http.StatusOK || !strings.Contains(body, "spa") {
		t.Errorf("SPA: %d %q", code, body)
	}
}

// An extension's failed check answers 503 with nothing it wrote, whether the
// check goes through Visible or a direct Authorize after writing.
func TestCommitExtMount(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprintf("fullRead=%v", all), func(t *testing.T) {
			a := &fullAuthz{errAuthz: &errAuthz{failAt: 2}, all: all}
			late := ExtMount{Name: "late", Pattern: "GET /x", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Partial", "1")
				_, _ = w.Write([]byte(`{"partial":`))
				a.Authorize(r, objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: "a"})
				a.Authorize(r, objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: "b"})
				_, _ = w.Write([]byte(`true}`))
			})}
			srv := secureServer(t, staticModel{}, a, WithExtMounts(refsExt(a, 3), late))
			if all {
				// A full read answers Visible without asking.
				if code, _, body := fetchRaw(t, srv, "/api/ext/refs/n"); code != http.StatusOK || body != "3\n" {
					t.Errorf("Visible under a full read: %d %q", code, body)
				}
				a.calls = 0
			} else {
				wantUnavailable(t, srv, "/api/ext/refs/n")
				a.calls = 0
			}
			wantUnavailable(t, srv, "/api/ext/late/x")
		})
	}
}

// A commit check answering false replaces the handler's response, also over
// a recorded authorisation error; true lets the response through.
func TestCommitCheckReplacesBody(t *testing.T) {
	var a Authorizer
	// The handler asks a population, so a failing full read records its error.
	ext := ExtMount{Name: "probe", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Visible(r, a, []int{1}, func(int) objects.Ref { return objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: "a"} })
		w.Header().Set("X-Partial", "1")
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("handler body"))
	})}
	signedIn := true
	check := func(w http.ResponseWriter, _ *http.Request) bool {
		if !signedIn {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"session-ended"}`))
		}
		return signedIn
	}
	for _, a = range []Authorizer{&errAuthz{}, &fullAuthz{errAuthz: &errAuthz{}, err: errOutage}} {
		srv := secureServer(t, staticModel{}, a, WithExtMounts(ext), WithCommitCheck(check))
		signedIn = false
		if code, h, body := fetchRaw(t, srv, "/api/ext/probe/data"); code != http.StatusUnauthorized || body != `{"error":"session-ended"}` || h.Get("X-Partial") != "" {
			t.Errorf("%T ended: %d %v %q", a, code, h, body)
		}
		signedIn = true
		code, h, body := fetchRaw(t, srv, "/api/ext/probe/data")
		if _, failing := a.(*fullAuthz); failing {
			if code != http.StatusServiceUnavailable || body != authzUnavailableBody {
				t.Errorf("%T signed in: %d %q", a, code, body)
			}
		} else if code != http.StatusTeapot || body != "handler body" || h.Get("X-Partial") != "1" {
			t.Errorf("%T signed in: %d %v %q", a, code, h, body)
		}
	}
}

// The audit hook hears why each 503 was answered, and nothing else.
func TestCommitAuditReasons(t *testing.T) {
	var reasons []string
	audit := WithAuthzAudit(func(_ *http.Request, why string) { reasons = append(reasons, why) })
	cases := []struct {
		a    Authorizer
		n    int
		want []string
	}{
		{&errAuthz{}, 3, nil},
		{&errAuthz{failAt: 2}, 3, []string{AuditCheckFailed}},
		{&errAuthz{spend: true}, checkLimit + 1, []string{AuditCheckLimit}},
		{&fullAuthz{errAuthz: &errAuthz{}, err: errOutage}, 3, []string{AuditFullReadFailed}},
	}
	for _, c := range cases {
		reasons = nil
		srv := secureServer(t, staticModel{}, c.a, WithExtMounts(refsExt(c.a, c.n)), audit)
		_, _, _ = fetchRaw(t, srv, "/api/ext/refs/n")
		if !slices.Equal(reasons, c.want) {
			t.Errorf("%T: audit %v, want %v", c.a, reasons, c.want)
		}
	}
	// An ended session is not an authorisation outage.
	reasons = nil
	srv := secureServer(t, staticModel{}, &errAuthz{failAt: 1}, WithExtMounts(refsExt(&errAuthz{failAt: 1}, 1)), audit,
		WithCommitCheck(func(w http.ResponseWriter, _ *http.Request) bool {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}))
	if code, _, _ := fetchRaw(t, srv, "/api/ext/refs/n"); code != http.StatusUnauthorized || reasons != nil {
		t.Errorf("ended session: %d, audit %v", code, reasons)
	}
}

// The closed gate stays in front of everything: no full read, no check.
func TestGateStillAuthoritative(t *testing.T) {
	a := &fullAuthz{errAuthz: &errAuthz{}, all: true}
	srv := httptest.NewServer(NewHandler(staticModel{}, fstest.MapFS{"index.html": {Data: []byte("spa")}}, a,
		WithExtMounts(refsExt(a, 3)), WithSecureMode(false), WithCommitCheck(func(http.ResponseWriter, *http.Request) bool {
			t.Error("commit check ran behind the gate")
			return true
		})))
	t.Cleanup(srv.Close)
	for _, p := range append(slices.Clone(gatePaths), "/api/ext/refs/n") {
		if code, _, body := fetchRaw(t, srv, p); code != http.StatusServiceUnavailable || body != `{"error":"secure-mode-incomplete"}`+"\n" {
			t.Errorf("GET %s: %d %q", p, code, body)
		}
	}
	if a.count() != 0 || a.reads != 0 {
		t.Errorf("%d checks, %d full reads behind the gate", a.count(), a.reads)
	}
}

// canList is allow-all under a full read, false without a ListAuthorizer, and
// records an error like can.
func TestViewCanList(t *testing.T) {
	routes := objects.GroupKind{Group: objects.GatewayAPIGroup, Kind: "HTTPRoute"}
	r, st := withAuthzState(httptest.NewRequest("GET", "/api/x", nil))
	h := &handler{authz: &errAuthz{}}
	if h.view(r).canList(routes, "shop") {
		t.Error("an Authorizer without CanList allowed a list")
	}
	h.authz = listAuthz{errAuthz: &errAuthz{}}
	if v := h.view(r); !v.canList(routes, "shop") || v.canList(routes, "blog") {
		t.Error("CanList answers not followed")
	}
	full, _ := withAuthzState(httptest.NewRequest("GET", "/api/x", nil))
	if !(&handler{authz: &fullAuthz{errAuthz: &errAuthz{}, all: true}}).view(full).canList(routes, "blog") {
		t.Error("a full read must allow every list")
	}
	h.authz = listAuthz{errAuthz: &errAuthz{}, err: errOutage}
	if h.view(r).canList(routes, "shop") || st.failed() != errOutage {
		t.Errorf("error not recorded: %v", st.failed())
	}
}
