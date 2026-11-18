package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
)

// auditedApp is a secure-mode app over two Gateways, infra/edge and
// vault/secret, whose SAR answers refuse everything in vault and cluster-wide.
type auditedApp struct {
	a       *App
	out     *lockedBuf
	session *http.Cookie
	install string
}

func newAuditedApp(t *testing.T, levels string) *auditedApp {
	t.Helper()
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1),
		testutil.GatewayClass("kgateway", testutil.Controller, true), testutil.Gateway("infra", "edge", "kgateway", nil),
		testutil.Gateway("vault", "secret", "kgateway", nil))
	kube.PrependReactor("create", "subjectaccessreviews", func(act clienttesting.Action) (bool, runtime.Object, error) {
		sar := act.(clienttesting.CreateAction).GetObject().(*authzv1.SubjectAccessReview)
		ra := sar.Spec.ResourceAttributes
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: ra != nil && ra.Namespace != "" && ra.Namespace != "vault"}}, nil
	})
	out := &lockedBuf{}
	o := secureOpts()
	o.AuditOut, o.AuditLevels = out, levels
	a, err := Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}}, o, extension.New())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.Start(ctx)
	eventually(t, "an install in the model", func() bool { return len(a.Engine.Installs()) > 0 })
	eventually(t, "a synced model", func() bool { p := a.Engine.Current(); return p != nil && p.Model.Snapshot.Synced })
	signIn := httptest.NewRecorder()
	a.sessions.Complete(signIn, httptest.NewRequest("GET", "/auth/callback", nil), oidc.Result{Identity: oidc.Identity{Username: "oidc:alice", Groups: []string{"oidc:dev"}},
		Issuer: "https://idp.example", Subject: "alice-sub", IDToken: "id-tok-secret", AccessToken: "acc-tok-secret", RefreshToken: "ref-tok-secret", Expiry: time.Now().Add(time.Hour), ReturnPath: "/"})
	return &auditedApp{a: a, out: out, session: signIn.Result().Cookies()[0], install: a.Engine.Installs()[0].ID()}
}

// get calls path with the session and returns the status and body.
func (x *auditedApp) get(t *testing.T, path string, hdr map[string]string) (int, string) {
	t.Helper()
	rec := call(x.a, "GET", path, []*http.Cookie{x.session}, hdr)
	return rec.Code, rec.Body.String()
}

// records stops the writer, which flushes the windows, and returns the records with action.
func (x *auditedApp) records(t *testing.T, action string) []map[string]any {
	t.Helper()
	done, stop := context.WithCancel(context.Background())
	stop()
	x.a.Audit.Run(done)
	var out []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(x.out.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		if m["action"] == action {
			out = append(out, m)
		}
	}
	return out
}

// A detail read writes one reads record with the object's ref, the session, the identities and the request.
func TestAuditReadRecord(t *testing.T) {
	x := newAuditedApp(t, "")
	path := "/api/v1/installs/" + x.install + "/gateways/infra/edge"
	if code, body := x.get(t, path, nil); code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, code, body)
	}
	recs := x.records(t, "read")
	if len(recs) != 1 {
		t.Fatalf("%d read records, want 1: %s", len(recs), x.out.String())
	}
	line, _ := json.Marshal(recs[0])
	for _, want := range []string{`"level":"reads"`, `"outcome":"success"`, `"refs":["gateway.networking.k8s.io/Gateway/infra/edge"]`,
		`"route":"GET /api/v1/installs/{id}/gateways/{ns}/{name}"`, `"install":"` + x.install + `"`, `"username":"oidc:alice"`,
		`"subject":"alice-sub"`, `"issuer":"https://idp.example"`, `"session":"`, `"schema":"1"`, `"server":"`} {
		if !strings.Contains(string(line), want) {
			t.Errorf("read record lacks %s: %s", want, line)
		}
	}
}

// Lists and overviews are summarised per session, endpoint and filter set,
// with a count; rows left out of a list are filtering, not denials.
func TestAuditSummaryRecords(t *testing.T) {
	x := newAuditedApp(t, "")
	base := "/api/v1/installs/" + x.install
	for _, p := range []string{base + "/gateways?namespace=infra", base + "/gateways?namespace=infra", base + "/gateways", base + "/overview"} {
		if code, body := x.get(t, p, nil); code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", p, code, body)
		}
	}
	recs := x.records(t, "list")
	got := map[string]float64{}
	for _, r := range recs {
		req := r["request"].(map[string]any)
		key := req["route"].(string)
		if f, ok := req["filters"].(map[string]any); ok {
			key += " namespace=" + f["namespace"].(string)
		}
		if _, ok := req["id"]; ok || r["level"] != "summaries" {
			t.Errorf("summary carries a request ID or the wrong level: %v", r)
		}
		got[key] = r["count"].(float64)
	}
	want := map[string]float64{"GET /api/v1/installs/{id}/gateways namespace=infra": 2, "GET /api/v1/installs/{id}/gateways": 1,
		"GET /api/v1/installs/{id}/overview": 1}
	if len(got) != len(want) {
		t.Fatalf("summaries %v, want %v", got, want)
	}
	for k, n := range want {
		if got[k] != n {
			t.Errorf("summary %s count %v, want %v (all: %v)", k, got[k], n, got)
		}
	}
	if d := x.records(t, "denial"); len(d) != 0 {
		t.Errorf("a filtered list wrote denials: %v", d)
	}
}

// A detail the viewer may not open writes a security denial; repeats collapse
// into one record with a count, and an absent object writes none.
func TestAuditDenialCollapses(t *testing.T) {
	x := newAuditedApp(t, "")
	base := "/api/v1/installs/" + x.install
	for range 3 {
		if code, _ := x.get(t, base+"/gateways/vault/secret", nil); code != http.StatusNotFound {
			t.Fatalf("denied detail = %d, want 404", code)
		}
	}
	if code, _ := x.get(t, base+"/gateways/infra/absent", nil); code != http.StatusNotFound {
		t.Fatalf("absent detail = %d, want 404", code)
	}
	recs := x.records(t, "denial")
	if len(recs) != 1 {
		t.Fatalf("%d denial records, want 1: %s", len(recs), x.out.String())
	}
	line, _ := json.Marshal(recs[0])
	for _, want := range []string{`"level":"security"`, `"outcome":"failure"`, `"reason":"forbidden"`, `"count":3`,
		`"refs":["gateway.networking.k8s.io/Gateway/vault/secret"]`, `"username":"oidc:alice"`} {
		if !strings.Contains(string(line), want) {
			t.Errorf("denial record lacks %s: %s", want, line)
		}
	}
	if r := x.records(t, "read"); len(r) != 0 {
		t.Errorf("refused reads wrote read records: %v", r)
	}
}

// With disclosure and requests on, a list writes the refs it returned and every API request a record.
func TestAuditDisclosureAndRequests(t *testing.T) {
	x := newAuditedApp(t, "security,disclosure,requests")
	base := "/api/v1/installs/" + x.install
	if code, body := x.get(t, base+"/gateways", nil); code != http.StatusOK {
		t.Fatalf("list = %d %s", code, body)
	}
	if code, _ := x.get(t, base+"/gateways/vault/secret", nil); code != http.StatusNotFound {
		t.Fatalf("denied detail = %d", code)
	}
	disc := x.records(t, "disclosure")
	if len(disc) != 1 {
		t.Fatalf("%d disclosure records, want 1: %s", len(disc), x.out.String())
	}
	refs, _ := json.Marshal(disc[0]["refs"])
	if !strings.Contains(string(refs), `"gateway.networking.k8s.io/Gateway/infra/edge"`) || strings.Contains(string(refs), "vault") {
		t.Errorf("disclosed refs %s, want infra/edge and nothing in vault", refs)
	}
	reqs := x.records(t, "request")
	if len(reqs) != 2 {
		t.Fatalf("%d request records, want 2: %s", len(reqs), x.out.String())
	}
	if reqs[0]["reason"] != "200" || reqs[1]["reason"] != "404" || reqs[1]["outcome"] != "failure" {
		t.Errorf("request records %v", reqs)
	}
	// Levels left off write nothing.
	if r := x.records(t, "list"); len(r) != 0 {
		t.Errorf("summaries off, yet %v", r)
	}
}

// No record carries a token, cookie, raw query string or response body.
func TestAuditNeverLogged(t *testing.T) {
	x := newAuditedApp(t, "security,reads,summaries,disclosure,requests")
	base := "/api/v1/installs/" + x.install
	hdr := map[string]string{"Authorization": "Bearer tok-secret", "X-Api-Token": "hdr-secret"}
	var bodies []string
	for _, p := range []string{base + "/gateways?namespace=infra&q=query-secret", base + "/gateways/infra/edge",
		base + "/resources/gateway.networking.k8s.io/Gateway/infra/edge"} {
		code, body := x.get(t, p, hdr)
		if code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", p, code, body)
		}
		bodies = append(bodies, body)
	}
	x.records(t, "")
	out := x.out.String()
	for _, s := range []string{"tok-secret", "hdr-secret", "Bearer", "id-tok-secret", "acc-tok-secret", "ref-tok-secret", "query-secret", "q=", x.session.Value, x.session.Name} {
		if strings.Contains(out, s) {
			t.Errorf("audit output carries %q:\n%s", s, out)
		}
	}
	// No response body appears whole, nor a field only bodies hold.
	for _, b := range bodies {
		if strings.Contains(out, strings.TrimSpace(b)) {
			t.Errorf("audit output carries a response body")
		}
	}
	for _, s := range []string{"collectedAt", "gatewayClassName", "apiVersion"} {
		if strings.Contains(out, s) {
			t.Errorf("audit output carries body field %q", s)
		}
	}
	if !strings.Contains(out, `"filters":{"namespace":"infra"}`) {
		t.Errorf("allow-listed filter missing:\n%s", out)
	}
}

// Local mode writes only the mode-at-startup record, whatever the levels.
func TestAuditLocalModeOnlyStartup(t *testing.T) {
	a, out, err := buildAudit(t, Options{AuthMode: ModeLocal, AuditLevels: "security,agent,reads,summaries,disclosure,requests"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ls, served := startServe(t, a, ctx)
	eventually(t, "a synced model", func() bool { p := a.Engine.Current(); return p != nil && p.Model.Snapshot.Synced })
	for _, p := range []string{"/api/v1/meta", "/api/v1/installs", "/api/v1/installs/x/gateways/infra/edge"} {
		r, err := http.Get("http://" + ls.main.Addr().String() + p)
		if err != nil {
			t.Fatal(err)
		}
		_ = r.Body.Close()
	}
	cancel()
	if err := testutil.Recv(t, served, "serve to return"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"action":"startup"`) {
		t.Fatalf("local mode wrote %q, want only the startup record", out.String())
	}
}
