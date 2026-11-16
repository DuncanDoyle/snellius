package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	clocktesting "k8s.io/utils/clock/testing"

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

// The CheckLimit-th uncached check is answered; one more fails the request.
func TestCommitCheckLimit(t *testing.T) {
	for _, n := range []int{CheckLimit, CheckLimit + 1} {
		a := &errAuthz{spend: true}
		var reasons []string
		srv := secureServer(t, staticModel{}, a, WithExtMounts(refsExt(a, n)), WithAuthzAudit(func(_ *http.Request, why string) { reasons = append(reasons, why) }))
		if n == CheckLimit {
			if code, _, body := fetchRaw(t, srv, "/api/ext/refs/n"); code != http.StatusOK || body != strconv.Itoa(CheckLimit)+"\n" {
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

// A commit check answering nil replaces the handler's response, also over a
// recorded authorisation error; answering the request lets the response through.
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
	check := func(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
		if !signedIn {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"session-ended"}`))
			return nil, nil
		}
		return r, nil
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
		{&errAuthz{spend: true}, CheckLimit + 1, []string{AuditCheckLimit}},
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
		WithCommitCheck(func(w http.ResponseWriter, _ *http.Request) (*http.Request, error) {
			w.WriteHeader(http.StatusUnauthorized)
			return nil, nil
		}))
	if code, _, _ := fetchRaw(t, srv, "/api/ext/refs/n"); code != http.StatusUnauthorized || reasons != nil {
		t.Errorf("ended session: %d, audit %v", code, reasons)
	}
}

// A commit check that answers a stale identity, or replaces the identity on
// every build, ends in 503 authz-unavailable after at most two builds.
func TestCommitIdentityChecks(t *testing.T) {
	type key struct{}
	cases := map[string]struct {
		check  func(http.ResponseWriter, *http.Request) (*http.Request, error)
		builds int
	}{
		"stale": {func(_ http.ResponseWriter, r *http.Request) (*http.Request, error) { return r, errors.New("stale") }, 1},
		"replaced": {func(_ http.ResponseWriter, r *http.Request) (*http.Request, error) {
			return r.WithContext(context.WithValue(r.Context(), key{}, 1)), nil
		}, 2},
	}
	for name, c := range cases {
		var reasons []string
		builds := 0
		ext := ExtMount{Name: "probe", Pattern: "GET /data", Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			builds++
			WriteJSON(w, "data")
		})}
		srv := secureServer(t, staticModel{}, &errAuthz{}, WithExtMounts(ext), WithCommitCheck(c.check),
			WithAuthzAudit(func(_ *http.Request, why string) { reasons = append(reasons, why) }))
		if code, _, body := fetchRaw(t, srv, "/api/ext/probe/data"); code != http.StatusServiceUnavailable || body != authzUnavailableBody ||
			builds != c.builds || !slices.Equal(reasons, []string{AuditIdentity}) {
			t.Errorf("%s: %d %q after %d builds, audit %v", name, code, body, builds, reasons)
		}
	}
}

// The closed gate stays in front of everything: no full read, no check.
func TestGateStillAuthoritative(t *testing.T) {
	a := &fullAuthz{errAuthz: &errAuthz{}, all: true}
	srv := httptest.NewServer(NewHandler(staticModel{}, fstest.MapFS{"index.html": {Data: []byte("spa")}}, a,
		WithExtMounts(refsExt(a, 3)), WithSecureMode(false), WithCommitCheck(func(_ http.ResponseWriter, r *http.Request) (*http.Request, error) {
			t.Error("commit check ran behind the gate")
			return r, nil
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

// commitT0 is the fake clock's start in the validity-at-commit tests.
var commitT0 = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// expiringAuthz allows every ref but deny's, noting each allow as valid for a
// minute from the clock's now. step runs inside the n-th call, counted over
// every build; full, when set, answers the n-th FullRead.
type expiringAuthz struct {
	clock *clocktesting.FakeClock
	deny  func(objects.Ref) bool
	spend bool
	step  func(n int)
	full  []bool

	mu           sync.Mutex
	calls, reads int
}

func (a *expiringAuthz) Authorize(r *http.Request, ref objects.Ref) bool {
	ok, err := a.AuthorizeErr(r, ref)
	if err != nil {
		ReportAuthzError(r, err)
	}
	return ok
}

func (a *expiringAuthz) AuthorizeErr(r *http.Request, ref objects.Ref) (bool, error) {
	a.mu.Lock()
	a.calls++
	n := a.calls
	a.mu.Unlock()
	if a.spend {
		if err := SpendCheck(r); err != nil {
			return false, err
		}
	}
	if a.step != nil {
		a.step(n)
	}
	if a.deny != nil && a.deny(ref) {
		return false, nil
	}
	NoteAllow(r, a.clock.Now().Add(time.Minute))
	return true, nil
}

// expiringFull is expiringAuthz with a full read; each answer steps the clock
// a minute after noting a "true", which then expires during the build.
type expiringFull struct{ *expiringAuthz }

func (a expiringFull) FullRead(r *http.Request) (bool, error) {
	a.mu.Lock()
	a.reads++
	all := a.full[a.reads-1]
	a.mu.Unlock()
	if all {
		NoteAllow(r, a.clock.Now().Add(time.Minute))
		a.clock.Step(time.Minute)
	}
	return all, nil
}

func (a *expiringAuthz) counts() (calls, reads int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls, a.reads
}

// builds counts handler runs; its mount writes the run and how many of n refs it may see.
type builds struct {
	mu sync.Mutex
	n  int
}

func (b *builds) count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.n
}

func (b *builds) mount(a Authorizer, n int) ExtMount {
	return ExtMount{Name: "b", Pattern: "GET /n", Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.n++
		run := b.n
		b.mu.Unlock()
		items := make([]int, n)
		for i := range items {
			items[i] = i
		}
		vis, _ := Visible(r, a, items, func(i int) objects.Ref { return objects.Ref{Kind: "HTTPRoute", Namespace: "shop", Name: fmt.Sprint(i)} })
		WriteJSON(w, map[string]int{"build": run, "visible": len(vis)})
	})}
}

// auditLog collects the audit reasons of a server.
type auditLog struct {
	mu      sync.Mutex
	reasons []string
}

func (l *auditLog) option() HandlerOption {
	return WithAuthzAudit(func(_ *http.Request, why string) {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.reasons = append(l.reasons, why)
	})
}

func (l *auditLog) got() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.reasons)
}

// expiringServer serves a builds mount over n refs with a, commit reading clk.
func expiringServer(t *testing.T, a Authorizer, clk *clocktesting.FakeClock, n int, opts ...HandlerOption) (*httptest.Server, *builds, *auditLog) {
	t.Helper()
	b, l := &builds{}, &auditLog{}
	opts = append([]HandlerOption{WithExtMounts(b.mount(a, n)), WithCommitClock(clk.Now), l.option()}, opts...)
	return secureServer(t, staticModel{}, a, opts...), b, l
}

// A response whose allows outlive the commit is written after one build.
func TestCommitValidResponseWritten(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	a := &expiringAuthz{clock: clk}
	srv, b, l := expiringServer(t, a, clk, 3)
	if code, _, body := fetchRaw(t, srv, "/api/ext/b/n"); code != http.StatusOK || body != `{"build":1,"visible":3}`+"\n" {
		t.Errorf("%d %q", code, body)
	}
	if b.count() != 1 || l.got() != nil {
		t.Errorf("%d builds, audit %v", b.count(), l.got())
	}
}

// An allow that expires during the build is rebuilt once; the second body is written.
func TestCommitExpiredRebuilt(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	a := &expiringAuthz{clock: clk, step: func(n int) {
		if n == 2 {
			clk.Step(time.Minute)
		}
	}}
	srv, b, l := expiringServer(t, a, clk, 3)
	if code, _, body := fetchRaw(t, srv, "/api/ext/b/n"); code != http.StatusOK || body != `{"build":2,"visible":3}`+"\n" {
		t.Errorf("%d %q", code, body)
	}
	if calls, _ := a.counts(); b.count() != 2 || calls != 6 || l.got() != nil {
		t.Errorf("%d builds, %d checks, audit %v", b.count(), calls, l.got())
	}
}

// Expiry during both builds answers 503 authz-unavailable, audited as expired.
func TestCommitExpiredTwice503(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	a := &expiringAuthz{clock: clk, step: func(n int) {
		if n%3 == 0 {
			clk.Step(time.Minute)
		}
	}}
	srv, b, l := expiringServer(t, a, clk, 3)
	wantUnavailable(t, srv, "/api/ext/b/n")
	if b.count() != 2 || !slices.Equal(l.got(), []string{AuditExpired}) {
		t.Errorf("%d builds, audit %v", b.count(), l.got())
	}
}

// A rebuild keeps the request's check count: four fifths of the limit twice pass it.
func TestCommitRebuildSharesCap(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	per := CheckLimit * 4 / 5
	a := &expiringAuthz{clock: clk, spend: true, step: func(n int) {
		if n == per {
			clk.Step(time.Minute)
		}
	}}
	srv, b, l := expiringServer(t, a, clk, per)
	wantUnavailable(t, srv, "/api/ext/b/n")
	if b.count() != 2 || !slices.Equal(l.got(), []string{AuditCheckLimit}) {
		t.Errorf("%d builds, audit %v", b.count(), l.got())
	}
}

// The first build expires and the session ends during the second: the
// session's 401 is answered, with nothing of either build.
func TestLogoutDuringRebuild(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	var mu sync.Mutex
	signedIn, checked := true, 0
	a := &expiringAuthz{clock: clk, step: func(n int) {
		switch n {
		case 3:
			clk.Step(time.Minute)
		case 4:
			mu.Lock()
			signedIn = false
			mu.Unlock()
		}
	}}
	check := WithCommitCheck(func(w http.ResponseWriter, r *http.Request) (*http.Request, error) {
		mu.Lock()
		defer mu.Unlock()
		checked++
		if !signedIn {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"sign-in-required"}`))
			return nil, nil
		}
		return r, nil
	})
	srv, b, l := expiringServer(t, a, clk, 3, check)
	if code, _, body := fetchRaw(t, srv, "/api/ext/b/n"); code != http.StatusUnauthorized || body != `{"error":"sign-in-required"}` {
		t.Errorf("%d %q", code, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if b.count() != 2 || checked != 2 || l.got() != nil {
		t.Errorf("%d builds, %d session checks, audit %v", b.count(), checked, l.got())
	}
}

// A full-read "true" that expires during the first build is asked again by
// the second, whose new answer (false) decides, not the first build's memo.
func TestExpiredFullReadRebuild(t *testing.T) {
	clk := clocktesting.NewFakeClock(commitT0)
	a := expiringFull{&expiringAuthz{clock: clk, full: []bool{true, false}, deny: func(r objects.Ref) bool { return r.Name == "1" }}}
	srv, b, _ := expiringServer(t, a, clk, 3)
	if code, _, body := fetchRaw(t, srv, "/api/ext/b/n"); code != http.StatusOK || body != `{"build":2,"visible":2}`+"\n" {
		t.Errorf("%d %q", code, body)
	}
	if calls, reads := a.counts(); b.count() != 2 || reads != 2 || calls != 3 {
		t.Errorf("%d builds, %d full reads, %d checks", b.count(), reads, calls)
	}
}
