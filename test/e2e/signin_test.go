//go:build e2e

package e2e

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/audit"
)

// issuerHost is the test issuer's name; setup.sh maps it for the node and the pod, and this client dials loopback.
const issuerHost = "oidc.snellius.test"

// browser is an HTTP client that behaves like the user's browser in the sign-in
// flow: a cookie jar, redirects followed, and the issuer's name on loopback.
func browser(t *testing.T) *http.Client {
	t.Helper()
	ca, err := os.ReadFile(filepath.Join(filepath.Dir(harnessKubeconfig(t)), "oidc", "ca.pem"))
	if err != nil {
		t.Fatalf("test issuer CA: %v", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		t.Fatal("test issuer CA holds no certificate")
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if host, port, err := net.SplitHostPort(addr); err == nil && host == issuerHost {
				addr = net.JoinHostPort("127.0.0.1", port)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: tr, Jar: jar, Timeout: 30 * time.Second}
}

// signIn runs the real flow for user and returns the signed-in client. The API
// server fetches the issuer's keys in the background, so early refusals are retried.
func signIn(t *testing.T, user string) *http.Client {
	t.Helper()
	var c *http.Client
	eventually(t, func() error {
		c = browser(t)
		resp, err := c.Get(base + "/auth/login?return=/")
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		form := resp.Request.URL
		if form.Hostname() != issuerHost || resp.StatusCode != http.StatusOK {
			return fmt.Errorf("login landed on %s with %d, want the issuer's form", form, resp.StatusCode)
		}
		resp, err = c.PostForm(form.String(), url.Values{"user": {user}})
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		// A fresh sign-in returns to the page it started from, marked for the web UI's intro.
		if got := resp.Request.URL; got.String() != base+"/#signed-in" || resp.StatusCode != http.StatusOK {
			return fmt.Errorf("sign-in ended on %s with %d", got, resp.StatusCode)
		}
		return nil
	})
	return c
}

type me struct {
	Username string   `json:"username"`
	Groups   []string `json:"groups"`
}

// call does one request and returns its status and body.
func call(t *testing.T, c *http.Client, method, path string, hdr map[string]string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	return resp.StatusCode, body
}

func whoami(t *testing.T, c *http.Client) (int, me) {
	t.Helper()
	code, body := call(t, c, http.MethodGet, "/auth/me", nil)
	var m me
	if code == http.StatusOK {
		if err := json.Unmarshal(body, &m); err != nil {
			t.Fatalf("/auth/me: %v: %s", err, body)
		}
	}
	return code, m
}

// checkData reads the data API as a signed-in user. setup.sh grants alice
// get and list on HTTPRoutes in shop and bob nothing, so every install is
// listed with its details hidden, and only alice's routes pages hold rows: shop's.
func checkData(t *testing.T, user string, c *http.Client, wantGood bool) {
	t.Helper()
	code, body := call(t, c, http.MethodGet, "/api/v1/meta", nil)
	if code != http.StatusOK {
		t.Errorf("%s: /api/v1/meta = %d %s", user, code, body)
	}
	code, body = call(t, c, http.MethodGet, "/api/v1/installs", nil)
	var insts []api.InstallSummary
	if err := json.Unmarshal(body, &insts); code != http.StatusOK || err != nil || len(insts) == 0 {
		t.Fatalf("%s: /api/v1/installs = %d %s", user, code, body)
	}
	for _, in := range insts {
		if !in.DetailsHidden {
			t.Errorf("%s: install %s shows its details without any RBAC", user, in.ID)
		}
	}
	good := false
	for _, in := range insts {
		code, body = call(t, c, http.MethodGet, "/api/v1/installs/"+in.ID+"/routes?limit=200", nil)
		var routes api.Page[api.RouteRow]
		if err := json.Unmarshal(body, &routes); code != http.StatusOK || err != nil {
			t.Fatalf("%s: routes of %s = %d %s", user, in.ID, code, body)
		}
		for _, r := range routes.Items {
			if r.Ref.Kind != "HTTPRoute" || r.Ref.Namespace != "shop" {
				t.Errorf("%s: routes of %s show %s %s, beyond the HTTPRoutes in shop", user, in.ID, r.Ref.Kind, r.Ref)
			}
			good = good || r.Ref.Name == "good"
		}
		if !wantGood && (len(routes.Items) != 0 || routes.Total != 0) {
			t.Errorf("%s: routes of %s = %s, want an empty page", user, in.ID, body)
		}
	}
	if good != wantGood {
		t.Errorf("%s: shop/good listed %v, want %v", user, good, wantGood)
	}
}

// TestSignIn signs two users in through the test issuer and checks the
// cluster's view of each, the data API for each, and logout.
func TestSignIn(t *testing.T) {
	if os.Getenv("SECURE") != "1" {
		t.Skip("needs SECURE=1 and a secure-mode install (test/e2e/setup.sh)")
	}
	alice, bob := signIn(t, "alice"), signIn(t, "bob")

	// TokenReview's answer, with the API server's oidc: prefixes, not the ID token's raw claims.
	for _, tc := range []struct {
		c        *http.Client
		user     string
		has      []string
		hasNot   []string
		wantGood bool
	}{
		{alice, "oidc:alice", []string{"oidc:team-a", "oidc:team-b", "system:authenticated"}, nil, true},
		{bob, "oidc:bob", []string{"oidc:team-b", "system:authenticated"}, []string{"oidc:team-a"}, false},
	} {
		code, m := whoami(t, tc.c)
		if code != http.StatusOK || m.Username != tc.user {
			t.Fatalf("/auth/me = %d %+v, want %s", code, m, tc.user)
		}
		for _, g := range tc.has {
			if !slices.Contains(m.Groups, g) {
				t.Errorf("%s: groups %v lack %s", tc.user, m.Groups, g)
			}
		}
		for _, g := range tc.hasNot {
			if slices.Contains(m.Groups, g) {
				t.Errorf("%s: groups %v hold %s", tc.user, m.Groups, g)
			}
		}
		checkData(t, tc.user, tc.c, tc.wantGood)
	}
	// Without a session the data API answers 401.
	if code, _ := call(t, browser(t), http.MethodGet, "/api/v1/installs", nil); code != http.StatusUnauthorized {
		t.Errorf("/api/v1/installs without a session = %d, want 401", code)
	}

	// A logout without the UI's Origin is refused and ends nothing.
	if code, _ := call(t, bob, http.MethodPost, "/auth/logout", map[string]string{"Origin": "http://evil.example"}); code != http.StatusForbidden {
		t.Fatalf("foreign-origin logout = %d", code)
	}
	code, body := call(t, bob, http.MethodPost, "/auth/logout", map[string]string{"Origin": base})
	var out struct{ Next string }
	if err := json.Unmarshal(body, &out); code != http.StatusOK || err != nil {
		t.Fatalf("logout = %d %s", code, body)
	}
	if !strings.Contains(out.Next, issuerHost) || !strings.Contains(out.Next, "/end_session") || strings.Contains(out.Next, "id_token_hint") {
		t.Errorf("logout next = %q, want the issuer's end_session without a token", out.Next)
	}
	if code, _ := whoami(t, bob); code != http.StatusUnauthorized {
		t.Errorf("bob after logout: /auth/me = %d, want 401", code)
	}
	// The issuer sends the browser back to the UI.
	resp, err := bob.Get(out.Next)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.Request.URL.String() != base+"/" {
		t.Errorf("end_session returned to %s", resp.Request.URL)
	}
	// Bob's logout ends only his session.
	if code, m := whoami(t, alice); code != http.StatusOK || m.Username != "oidc:alice" {
		t.Errorf("alice after bob's logout: %d %+v", code, m)
	}
}

// summaryWait covers one summary window (design §9.4) after the list, plus the log's delay.
const summaryWait = audit.SummaryWindow + 30*time.Second

// TestAuditRecords reads a route and lists routes as alice, then finds this
// read's record and this session's list summary in the server's stdout. It
// takes up to one summary window.
func TestAuditRecords(t *testing.T) {
	if os.Getenv("SECURE") != "1" {
		t.Skip("needs SECURE=1 and a secure-mode install (test/e2e/setup.sh)")
	}
	alice := signIn(t, "alice")
	code, body := call(t, alice, http.MethodGet, "/api/v1/installs", nil)
	var insts []api.InstallSummary
	if err := json.Unmarshal(body, &insts); code != http.StatusOK || err != nil {
		t.Fatalf("/api/v1/installs = %d %s", code, body)
	}
	const route = "gateway.networking.k8s.io/HTTPRoute/shop/good"
	readID := ""
	for _, in := range insts {
		if code, body = call(t, alice, http.MethodGet, "/api/v1/installs/"+in.ID+"/routes?namespace=shop", nil); code != http.StatusOK {
			t.Fatalf("routes of %s = %d %s", in.ID, code, body)
		}
		resp, err := alice.Get(base + "/api/v1/installs/" + in.ID + "/routes/" + route)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK && readID == "" {
			readID = resp.Header.Get("X-Request-Id")
		}
	}
	if readID == "" {
		t.Fatalf("no install opened %s for alice", route)
	}
	deadline := time.Now().Add(summaryWait)
	// find returns alice's first record that want accepts; each kubectl call ends by the deadline.
	find := func(want func(rec auditLine) bool) (auditLine, error) {
		end := time.Now().Add(30 * time.Second)
		if end.After(deadline) {
			end = deadline
		}
		ctx, cancel := context.WithDeadline(context.Background(), end)
		defer cancel()
		out, err := kubectlRunContext(ctx, t, "-n", "snellius", "logs", "deploy/ui-snellius", "--since=15m")
		if err != nil {
			return auditLine{}, fmt.Errorf("server logs: %v", err)
		}
		for _, l := range strings.Split(string(out), "\n") {
			var rec auditLine
			if json.Unmarshal([]byte(l), &rec) == nil && rec.Log == "snellius.audit" && rec.Kube.Username == "oidc:alice" && want(rec) {
				return rec, nil
			}
		}
		return auditLine{}, fmt.Errorf("no matching audit record yet")
	}
	poll := func(what string, want func(rec auditLine) bool) auditLine {
		for {
			rec, err := find(want)
			if err == nil {
				return rec
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %v", what, err)
			}
			time.Sleep(10 * time.Second)
		}
	}
	read := poll("read record", func(rec auditLine) bool {
		return rec.Level == "reads" && rec.Action == "read" && rec.Request.ID == readID && slices.Contains(rec.Refs, route) && rec.Session != ""
	})
	poll("list summary", func(rec auditLine) bool {
		return rec.Level == "summaries" && rec.Session == read.Session && rec.Request.Route == "GET /api/v1/installs/{id}/routes" &&
			rec.Request.Filters["namespace"] == "shop" && rec.Count >= 1
	})
}

// auditLine is the part of an audit record TestAuditRecords checks.
type auditLine struct {
	Log, Level, Action, Session string
	Refs                        []string
	Count                       int
	Kube                        struct{ Username string }
	Request                     struct {
		ID, Route string
		Filters   map[string]string
	}
}
