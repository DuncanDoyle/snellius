package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"html/template"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

const codeTTL = time.Minute

// user is a test account; its groups go into the ID token's groups claim.
type user struct {
	name   string
	groups []string
}

// grant is what a code or refresh token stands for.
type grant struct {
	user                         string
	clientID, redirectURI, nonce string
	challenge                    string
	expires                      time.Time
}

// Config describes the issuer and its one client.
type Config struct {
	URL          string
	ClientID     string
	ClientSecret string // empty accepts a public client
	RedirectURIs []string
	Users        []user
	TokenTTL     time.Duration
	Now          func() time.Time
}

// Issuer is a minimal OpenID provider for tests: code flow with PKCE S256, refresh tokens and RS256 ID tokens.
type Issuer struct {
	Config
	key *rsa.PrivateKey
	kid string

	mu      sync.Mutex
	codes   map[string]grant
	refresh map[string]grant
}

// NewIssuer generates the signing key.
func NewIssuer(cfg Config) (*Issuer, error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	iss := &Issuer{Config: cfg, key: key}
	sum := sha256.Sum256(key.N.Bytes())
	iss.kid = base64.RawURLEncoding.EncodeToString(sum[:8])
	if iss.TokenTTL == 0 {
		iss.TokenTTL = time.Hour
	}
	if iss.Now == nil {
		iss.Now = time.Now
	}
	iss.codes, iss.refresh = map[string]grant{}, map[string]grant{}
	return iss, nil
}

// Handler serves discovery, JWKS, authorize, token and end_session.
func (i *Issuer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", i.discovery)
	mux.HandleFunc("GET /keys", i.jwks)
	mux.HandleFunc("/authorize", i.authorize)
	mux.HandleFunc("POST /token", i.token)
	mux.HandleFunc("GET /end_session", i.endSession)
	return mux
}

func (i *Issuer) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                i.URL,
		"authorization_endpoint":                i.URL + "/authorize",
		"token_endpoint":                        i.URL + "/token",
		"jwks_uri":                              i.URL + "/keys",
		"end_session_endpoint":                  i.URL + "/end_session",
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
		"code_challenge_methods_supported":      []string{"S256"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"scopes_supported":                      []string{"openid", "profile", "email", "groups", "offline_access"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post", "none"},
		"claims_supported":                      []string{"iss", "sub", "aud", "exp", "iat", "nonce", "groups"},
	})
}

func (i *Issuer) jwks(w http.ResponseWriter, _ *http.Request) {
	pub := i.key.PublicKey
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "use": "sig", "alg": "RS256", "kid": i.kid,
		"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

var formPage = template.Must(template.New("authorize").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>Test issuer sign-in</title></head>
<body><main><h1>Sign in as a test user</h1>
<form method="post" action="/authorize">
{{range .Params}}<input type="hidden" name="{{.Name}}" value="{{.Value}}">
{{end}}<ul>{{range .Users}}<li><button type="submit" name="user" value="{{.Name}}">{{.Name}}</button> <span>{{.Groups}}</span></li>{{end}}</ul>
</form></main></body></html>`))

// authorize shows the user picker (GET) and issues a code for the picked user (POST).
func (i *Issuer) authorize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// The picker posts the request's parameters back as hidden fields.
	q := r.Form
	redirect := q.Get("redirect_uri")
	switch {
	case q.Get("client_id") != i.ClientID:
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	case !slices.Contains(i.RedirectURIs, redirect):
		http.Error(w, "unregistered redirect_uri", http.StatusBadRequest)
		return
	}
	// From here errors go back to the client, as RFC 6749 §4.1.2.1 says.
	switch {
	case q.Get("response_type") != "code":
		redirectWith(w, r, redirect, url.Values{"error": {"unsupported_response_type"}, "state": {q.Get("state")}})
		return
	case q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256":
		redirectWith(w, r, redirect, url.Values{"error": {"invalid_request"}, "state": {q.Get("state")}})
		return
	case !slices.Contains(strings.Fields(q.Get("scope")), "openid"):
		redirectWith(w, r, redirect, url.Values{"error": {"invalid_scope"}, "state": {q.Get("state")}})
		return
	}
	if r.Method == http.MethodGet {
		type row struct{ Name, Groups string }
		rows := make([]row, 0, len(i.Users))
		for _, u := range i.Users {
			rows = append(rows, row{u.name, strings.Join(u.groups, ", ")})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		type param struct{ Name, Value string }
		var params []param
		for _, k := range []string{"response_type", "client_id", "redirect_uri", "scope", "state", "nonce", "code_challenge", "code_challenge_method"} {
			params = append(params, param{k, q.Get(k)})
		}
		_ = formPage.Execute(w, struct {
			Params []param
			Users  []row
		}{params, rows})
		return
	}
	name := r.PostForm.Get("user")
	if i.user(name) == nil {
		http.Error(w, "unknown user", http.StatusBadRequest)
		return
	}
	code := random()
	i.mu.Lock()
	i.codes[code] = grant{user: name, clientID: i.ClientID, redirectURI: redirect, nonce: q.Get("nonce"),
		challenge: q.Get("code_challenge"), expires: i.Now().Add(codeTTL)}
	i.mu.Unlock()
	redirectWith(w, r, redirect, url.Values{"code": {code}, "state": {q.Get("state")}})
}

func (i *Issuer) user(name string) *user {
	for k := range i.Users {
		if i.Users[k].name == name {
			return &i.Users[k]
		}
	}
	return nil
}

// token serves the authorization_code and refresh_token grants.
func (i *Issuer) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		tokenError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !i.clientOK(r) {
		tokenError(w, http.StatusUnauthorized, "invalid_client")
		return
	}
	var g grant
	var ok bool
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		g, ok = i.take(i.codes, r.PostForm.Get("code"))
		if !ok || g.redirectURI != r.PostForm.Get("redirect_uri") || !VerifyPKCE(r.PostForm.Get("code_verifier"), g.challenge) {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
	case "refresh_token":
		g, ok = i.take(i.refresh, r.PostForm.Get("refresh_token"))
		if !ok {
			tokenError(w, http.StatusBadRequest, "invalid_grant")
			return
		}
		g.nonce = "" // a refreshed ID token carries no nonce
	default:
		tokenError(w, http.StatusBadRequest, "unsupported_grant_type")
		return
	}
	idToken, err := i.idToken(g)
	if err != nil {
		tokenError(w, http.StatusInternalServerError, "server_error")
		return
	}
	rt := random()
	i.mu.Lock()
	i.refresh[rt] = grant{user: g.user, clientID: g.clientID, expires: i.Now().Add(24 * time.Hour)}
	i.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"access_token": random(), "token_type": "Bearer",
		"expires_in": int(i.TokenTTL / time.Second), "id_token": idToken, "refresh_token": rt})
}

// take removes key from m, so a code or refresh token works once, and reports whether it was live.
func (i *Issuer) take(m map[string]grant, key string) (grant, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	g, ok := m[key]
	delete(m, key)
	return g, ok && key != "" && g.clientID == i.ClientID && i.Now().Before(g.expires)
}

// clientOK checks client_secret_basic or client_secret_post, or a public client's client_id.
func (i *Issuer) clientOK(r *http.Request) bool {
	id, secret, basic := r.BasicAuth()
	if basic {
		// RFC 6749 §2.3.1: both parts are form-encoded before Basic encoding.
		id, _ = url.QueryUnescape(id)
		secret, _ = url.QueryUnescape(secret)
	} else {
		id, secret = r.PostForm.Get("client_id"), r.PostForm.Get("client_secret")
	}
	if id != i.ClientID {
		return false
	}
	if i.ClientSecret == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(i.ClientSecret)) == 1
}

// idToken signs an RS256 ID token for g.
func (i *Issuer) idToken(g grant) (string, error) {
	u := i.user(g.user)
	if u == nil {
		return "", errors.New("unknown user")
	}
	now := i.Now()
	claims := map[string]any{"iss": i.URL, "sub": u.name, "aud": g.clientID, "azp": g.clientID,
		"iat": now.Unix(), "exp": now.Add(i.TokenTTL).Unix(), "groups": u.groups}
	if g.nonce != "" {
		claims["nonce"] = g.nonce
	}
	return Sign(i.key, i.kid, claims)
}

// endSession redirects to post_logout_redirect_uri when it shares an origin with a registered redirect URI.
func (i *Issuer) endSession(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	next := q.Get("post_logout_redirect_uri")
	if next != "" && q.Get("client_id") == i.ClientID && i.registeredOrigin(next) {
		v := url.Values{}
		if s := q.Get("state"); s != "" {
			v.Set("state", s)
		}
		redirectWith(w, r, next, v)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("signed out\n"))
}

func (i *Issuer) registeredOrigin(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	for _, ru := range i.RedirectURIs {
		if r, err := url.Parse(ru); err == nil && r.Scheme == u.Scheme && r.Host == u.Host {
			return true
		}
	}
	return false
}

// VerifyPKCE checks an S256 code verifier (RFC 7636 §4.6).
func VerifyPKCE(verifier, challenge string) bool {
	if len(verifier) < 43 || len(verifier) > 128 || challenge == "" {
		return false
	}
	sum := sha256.Sum256([]byte(verifier))
	return subtle.ConstantTimeCompare([]byte(b64(sum[:])), []byte(challenge)) == 1
}

// Sign returns a compact RS256 JWS of claims.
func Sign(key *rsa.PrivateKey, kid string, claims map[string]any) (string, error) {
	h, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": kid})
	if err != nil {
		return "", err
	}
	p, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := b64(h) + "." + b64(p)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	return input + "." + b64(sig), nil
}

func redirectWith(w http.ResponseWriter, r *http.Request, target string, add url.Values) {
	u, err := url.Parse(target)
	if err != nil {
		http.Error(w, "bad redirect", http.StatusBadRequest)
		return
	}
	q := u.Query()
	for k, vs := range add {
		if len(vs) > 0 && vs[0] != "" {
			q.Set(k, vs[0])
		}
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound) //nolint:gosec // callers pass only registered redirect URIs or their origin
}

func tokenError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b64(b)
}
