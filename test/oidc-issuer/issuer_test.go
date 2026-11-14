package main

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"
)

const (
	redirect = "http://127.0.0.1:18080/auth/callback"
	verifier = "dBjftJeZ4CVP-mJ92K9CD7wVJ0GZv3SsUvBEhKbAvy0aBc" // 46 chars
)

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestVerifyPKCE(t *testing.T) {
	// The challenge was computed independently with openssl dgst -sha256 and base64url.
	if !VerifyPKCE("dBjftJeZ4CVP-mJ92K9CD7wVJ0GZv3SsUvBEhKbAvy0", "Xqe9Iwhv_GZDLHLsleWDAYVd3fMQ_MM6Ta4cb2Hom3U") {
		t.Fatal("matching verifier refused")
	}
	for name, v := range map[string]string{"wrong": strings.Repeat("a", 43), "short": "abc", "long": strings.Repeat("a", 129), "empty": ""} {
		if VerifyPKCE(v, challenge(verifier)) {
			t.Errorf("%s verifier accepted", name)
		}
	}
	if VerifyPKCE(verifier, "") {
		t.Error("empty challenge accepted")
	}
}

// fixture serves an issuer over TLS and returns it with a client that keeps redirects visible.
func fixture(t *testing.T, secret string, now func() time.Time) (*Issuer, *httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	iss, err := NewIssuer(Config{URL: srv.URL, ClientID: "snellius", ClientSecret: secret, RedirectURIs: []string{redirect},
		Users: []user{{"alice", []string{"team-a", "team-b"}}, {"bob", []string{"team-b"}}}, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = iss.Handler()
	c := srv.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return iss, srv, c
}

func authQuery(nonce string) url.Values {
	return url.Values{"response_type": {"code"}, "client_id": {"snellius"}, "redirect_uri": {redirect}, "scope": {"openid groups"},
		"state": {"st"}, "nonce": {nonce}, "code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"}}
}

// pick loads the picker at authURL and submits it for name as a browser would: its hidden fields plus the button.
func pick(t *testing.T, c *http.Client, authURL, name string) *http.Response {
	t.Helper()
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /authorize = %d", resp.StatusCode)
	}
	action := regexp.MustCompile(`<form method="post" action="([^"]*)"`).FindSubmatch(page)
	if action == nil {
		t.Fatalf("no form: %s", page)
	}
	form := url.Values{"user": {name}}
	for _, m := range regexp.MustCompile(`<input type="hidden" name="([^"]*)" value="([^"]*)">`).FindAllSubmatch(page, -1) {
		form.Set(html.UnescapeString(string(m[1])), html.UnescapeString(string(m[2])))
	}
	target, _ := resp.Request.URL.Parse(html.UnescapeString(string(action[1])))
	resp, err = c.PostForm(target.String(), form)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp
}

// login picks user at /authorize and returns the code.
func login(t *testing.T, srv *httptest.Server, c *http.Client, name string) string {
	t.Helper()
	resp := pick(t, c, srv.URL+"/authorize?"+authQuery("n-1").Encode(), name)
	loc, err := url.Parse(resp.Header.Get("Location"))
	if resp.StatusCode != http.StatusFound || err != nil || !strings.HasPrefix(loc.String(), redirect+"?") {
		t.Fatalf("POST /authorize = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if loc.Query().Get("state") != "st" {
		t.Fatalf("state = %q", loc.Query().Get("state"))
	}
	return loc.Query().Get("code")
}

type tokens struct {
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
	status       int
}

func postToken(t *testing.T, srv *httptest.Server, c *http.Client, form url.Values, secret string) tokens {
	t.Helper()
	if secret == "" {
		form.Set("client_id", "snellius") // public client
	}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/token", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if secret != "" {
		req.SetBasicAuth("snellius", url.QueryEscape(secret))
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tk tokens
	if err := json.NewDecoder(resp.Body).Decode(&tk); err != nil {
		t.Fatal(err)
	}
	tk.status = resp.StatusCode
	return tk
}

func codeForm(code string) url.Values {
	return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}}
}

// verify checks raw against the issuer's JWKS and returns its claims.
func verify(t *testing.T, srv *httptest.Server, c *http.Client, raw string) map[string]any {
	t.Helper()
	resp, err := c.Get(srv.URL + "/keys")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var set struct {
		Keys []struct{ Kid, N, E string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil || len(set.Keys) != 1 {
		t.Fatalf("jwks: %v %+v", err, set)
	}
	n, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].N)
	e, _ := base64.RawURLEncoding.DecodeString(set.Keys[0].E)
	pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		t.Fatalf("malformed token %q", raw)
	}
	var hdr map[string]string
	h, _ := base64.RawURLEncoding.DecodeString(parts[0])
	_ = json.Unmarshal(h, &hdr)
	if hdr["alg"] != "RS256" || hdr["kid"] != set.Keys[0].Kid {
		t.Fatalf("header %v, jwks kid %s", hdr, set.Keys[0].Kid)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	p, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if err := json.Unmarshal(p, &claims); err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestCodeFlowSignsVerifiableIDToken(t *testing.T) {
	_, srv, c := fixture(t, "s3cret/+", nil)
	tk := postToken(t, srv, c, codeForm(login(t, srv, c, "alice")), "s3cret/+")
	if tk.status != http.StatusOK {
		t.Fatalf("token = %d %s", tk.status, tk.Error)
	}
	cl := verify(t, srv, c, tk.IDToken)
	if cl["iss"] != srv.URL || cl["sub"] != "alice" || cl["aud"] != "snellius" || cl["nonce"] != "n-1" {
		t.Fatalf("claims %v", cl)
	}
	if g, _ := cl["groups"].([]any); len(g) != 2 || g[0] != "team-a" || g[1] != "team-b" {
		t.Fatalf("groups %v", cl["groups"])
	}
	if _, ok := cl["exp"].(float64); !ok {
		t.Fatalf("no exp: %v", cl)
	}
}

func TestCodeIsOneUseAndBound(t *testing.T) {
	_, srv, c := fixture(t, "", nil)
	code := login(t, srv, c, "bob")
	bad := codeForm(code)
	bad.Set("code_verifier", strings.Repeat("x", 43))
	if tk := postToken(t, srv, c, bad, ""); tk.Error != "invalid_grant" {
		t.Fatalf("wrong verifier: %+v", tk)
	}
	// The failed attempt burned the code.
	if tk := postToken(t, srv, c, codeForm(code), ""); tk.Error != "invalid_grant" {
		t.Fatalf("reused code: %+v", tk)
	}
	code = login(t, srv, c, "bob")
	if tk := postToken(t, srv, c, codeForm(code), ""); tk.status != http.StatusOK {
		t.Fatalf("first use: %+v", tk)
	}
	if tk := postToken(t, srv, c, codeForm(code), ""); tk.Error != "invalid_grant" {
		t.Fatalf("second use: %+v", tk)
	}
	other := codeForm(login(t, srv, c, "bob"))
	other.Set("redirect_uri", "http://127.0.0.1:18080/other")
	if tk := postToken(t, srv, c, other, ""); tk.Error != "invalid_grant" {
		t.Fatalf("redirect mismatch: %+v", tk)
	}
}

func TestClientSecretRequired(t *testing.T) {
	_, srv, c := fixture(t, "right", nil)
	if tk := postToken(t, srv, c, codeForm(login(t, srv, c, "alice")), "wrong"); tk.status != http.StatusUnauthorized || tk.Error != "invalid_client" {
		t.Fatalf("wrong secret: %+v", tk)
	}
	if tk := postToken(t, srv, c, codeForm(login(t, srv, c, "alice")), ""); tk.Error != "invalid_client" {
		t.Fatalf("no secret: %+v", tk)
	}
}

func TestCodeExpires(t *testing.T) {
	now := time.Now()
	_, srv, c := fixture(t, "", func() time.Time { return now })
	code := login(t, srv, c, "alice")
	now = now.Add(codeTTL + time.Second)
	if tk := postToken(t, srv, c, codeForm(code), ""); tk.Error != "invalid_grant" {
		t.Fatalf("expired code: %+v", tk)
	}
}

func TestRefreshRotates(t *testing.T) {
	_, srv, c := fixture(t, "", nil)
	first := postToken(t, srv, c, codeForm(login(t, srv, c, "bob")), "")
	refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {first.RefreshToken}}
	second := postToken(t, srv, c, refresh, "")
	if second.status != http.StatusOK || second.RefreshToken == "" || second.RefreshToken == first.RefreshToken {
		t.Fatalf("refresh: %+v", second)
	}
	cl := verify(t, srv, c, second.IDToken)
	if _, ok := cl["nonce"]; ok || cl["sub"] != "bob" {
		t.Fatalf("refreshed claims %v", cl)
	}
	if tk := postToken(t, srv, c, refresh, ""); tk.Error != "invalid_grant" {
		t.Fatalf("old refresh token reused: %+v", tk)
	}
}

func TestAuthorizeRefusesBadRequests(t *testing.T) {
	_, srv, c := fixture(t, "", nil)
	get := func(mut func(url.Values)) *http.Response {
		q := authQuery("n")
		mut(q)
		resp, err := c.Get(srv.URL + "/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp
	}
	if r := get(func(q url.Values) { q.Set("redirect_uri", "https://evil.example/cb") }); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unregistered redirect = %d", r.StatusCode)
	}
	if r := get(func(q url.Values) { q.Set("client_id", "other") }); r.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown client = %d", r.StatusCode)
	}
	r := get(func(q url.Values) { q.Del("code_challenge") })
	if loc, _ := url.Parse(r.Header.Get("Location")); r.StatusCode != http.StatusFound || loc.Query().Get("error") != "invalid_request" {
		t.Errorf("no PKCE = %d %s", r.StatusCode, r.Header.Get("Location"))
	}
}

func TestDiscoveryAndEndSession(t *testing.T) {
	_, srv, c := fixture(t, "", nil)
	resp, err := c.Get(srv.URL + "/.well-known/openid-configuration")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&doc)
	_ = resp.Body.Close()
	if doc["issuer"] != srv.URL || doc["end_session_endpoint"] != srv.URL+"/end_session" {
		t.Fatalf("discovery %v", doc)
	}
	q := url.Values{"client_id": {"snellius"}, "post_logout_redirect_uri": {"http://127.0.0.1:18080/"}}
	resp, err = c.Get(srv.URL + "/end_session?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "http://127.0.0.1:18080/" {
		t.Fatalf("end_session = %d %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	q.Set("post_logout_redirect_uri", "https://evil.example/")
	resp, err = c.Get(srv.URL + "/end_session?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("foreign post-logout redirect = %d", resp.StatusCode)
	}
}

func TestServerCertCoversHost(t *testing.T) {
	cert, caPEM, err := newServerCert("oidc.snellius.test")
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA PEM unreadable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "oidc.snellius.test", Roots: pool}); err != nil {
		t.Fatal(err)
	}
}
