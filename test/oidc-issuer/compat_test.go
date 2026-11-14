package main

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/audit"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

type nopAudit struct{}

func (nopAudit) Emit(context.Context, audit.Record) {}

// recordingReviewer stands in for TokenReview: it keeps the ID token and maps it to a fixed identity.
type recordingReviewer struct{ tokens []string }

func (r *recordingReviewer) Review(_ context.Context, raw string) (oidc.Identity, error) {
	r.tokens = append(r.tokens, raw)
	return oidc.Identity{Username: "oidc:alice"}, nil
}

type capture struct{ res *oidc.Result }

func (c *capture) Complete(w http.ResponseWriter, r *http.Request, res oidc.Result) {
	c.res = &res
	http.Redirect(w, r, res.ReturnPath, http.StatusFound)
}

// Snellius's own sign-in flow completes against the issuer: discovery, PKCE,
// a confidential client and refresh, as the secure-mode e2e uses them.
func TestSnelliusFlowAgainstIssuer(t *testing.T) {
	_, srv, c := fixture(t, "s3cret", nil)
	dir := t.TempDir()
	ca := filepath.Join(dir, "ca.pem")
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := oidc.Config{IssuerURL: srv.URL, ClientID: "snellius", ClientSecretFile: secret, RedirectURL: redirect, CAFile: ca}
	prov, err := oidc.NewProvider(cfg, clock.RealClock{})
	if err != nil {
		t.Fatal(err)
	}
	prov.Run(context.Background()) // returns once discovery succeeds
	if ok, err := prov.Ready(); !ok {
		t.Fatalf("discovery: %v", err)
	}
	rev, done := &recordingReviewer{}, &capture{}
	flow, err := oidc.NewFlow(oidc.FlowOptions{Config: cfg, Provider: prov, Reviewer: rev, Completer: done, Audit: nopAudit{}})
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	flow.Login(rec, httptest.NewRequest(http.MethodGet, "/auth/login?return=/routes", nil))
	auth := rec.Result().Header.Get("Location")
	cookies := rec.Result().Cookies()
	resp := pick(t, c, auth, "alice")
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("pick user = %d", resp.StatusCode)
	}
	cb, _ := url.Parse(resp.Header.Get("Location"))
	req := httptest.NewRequest(http.MethodGet, "/auth/callback?"+cb.RawQuery, nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec = httptest.NewRecorder()
	flow.Callback(rec, req)
	if done.res == nil {
		t.Fatalf("callback did not complete: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if done.res.Subject != "alice" || done.res.ReturnPath != "/routes" || done.res.RefreshToken == "" {
		t.Fatalf("result %+v", done.res)
	}
	if cl := verify(t, srv, c, rev.tokens[0]); cl["aud"] != "snellius" {
		t.Fatalf("reviewed token claims %v", cl)
	}

	res, err := flow.Refresh(context.Background(), done.res.RefreshToken)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if res.Subject != "alice" || res.RefreshToken == done.res.RefreshToken {
		t.Fatalf("refreshed %+v", res)
	}
}
