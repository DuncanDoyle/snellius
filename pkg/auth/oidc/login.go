package oidc

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"k8s.io/utils/clock"

	"github.com/DuncanDoyle/snellius/pkg/audit"
)

// Sign-in failure reasons, the only detail that reaches the browser.
const (
	ReasonAudience        = "audience"
	ReasonUnauthenticated = "unauthenticated"
	ReasonInvalid         = "invalid"
)

const (
	loginCookie   = "snellius_login"
	cookiePath    = "/auth/"
	clockSkew     = 2 * time.Minute
	maxIDToken    = 64 << 10
	maxReturnPath = 2048
)

// Result is a completed sign-in. Its tokens stay on the server.
type Result struct {
	// Identity is what TokenReview returned; Issuer and Subject come from the ID token.
	Identity        Identity
	Issuer, Subject string
	IDToken         string
	// Expiry is the ID token's exp.
	Expiry                    time.Time
	AccessToken, RefreshToken string
	// ReturnPath is a same-origin relative path.
	ReturnPath string
}

// Completer finishes a sign-in that passed every check, usually by starting a session.
type Completer interface {
	Complete(w http.ResponseWriter, r *http.Request, res Result)
}

// RedirectCompleter redirects to the return path and keeps nothing.
type RedirectCompleter struct{}

// Complete implements Completer.
func (RedirectCompleter) Complete(w http.ResponseWriter, r *http.Request, res Result) {
	http.Redirect(w, r, res.ReturnPath, http.StatusFound)
}

// Auditor receives audit records; *audit.Logger is one.
type Auditor interface {
	Emit(ctx context.Context, rec audit.Record)
}

// FlowOptions configure a Flow.
type FlowOptions struct {
	Config Config
	// Provider supplies the discovered endpoints and the HTTP client for the code exchange.
	Provider  *Provider
	Reviewer  Reviewer
	Completer Completer
	Audit     Auditor
	// Clock checks ID-token times; nil means the real clock.
	Clock clock.PassiveClock
	// CookieSecure marks the login cookie Secure.
	CookieSecure bool
	// Logger receives failure steps, never a token; nil means slog.Default().
	Logger *slog.Logger
}

// Flow serves /auth/login and /auth/callback: the authorization code flow
// with PKCE, checked in the order of design §4.1.
type Flow struct {
	cfg       Config
	prov      *Provider
	reviewer  Reviewer
	completer Completer
	audit     Auditor
	clk       clock.PassiveClock
	secure    bool
	log       *slog.Logger
	tx        *transactions
}

// NewFlow builds a Flow. A client secret file, when configured, must be readable and not empty.
func NewFlow(o FlowOptions) (*Flow, error) {
	f := &Flow{cfg: o.Config, prov: o.Provider, reviewer: o.Reviewer, completer: o.Completer, audit: o.Audit,
		clk: o.Clock, secure: o.CookieSecure, log: o.Logger}
	if _, err := f.clientSecret(); err != nil {
		return nil, err
	}
	if f.clk == nil {
		f.clk = clock.RealClock{}
	}
	if f.log == nil {
		f.log = slog.Default()
	}
	f.tx = newTransactions(f.clk, maxTransactions)
	return f, nil
}

// clientSecret reads the secret file on each use, so a rotated Secret applies; "" for a public client.
func (f *Flow) clientSecret() (string, error) {
	if f.cfg.ClientSecretFile == "" {
		return "", nil
	}
	b, err := os.ReadFile(f.cfg.ClientSecretFile)
	if err != nil {
		return "", fmt.Errorf("read OIDC client secret file: %w", err)
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", errors.New("OIDC client secret file is empty")
	}
	return s, nil
}

func (f *Flow) oauth(eps *Endpoints, secret string) *oauth2.Config {
	style := oauth2.AuthStyleInParams
	if secret != "" {
		style = oauth2.AuthStyleInHeader
	}
	scopes := f.cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	return &oauth2.Config{ClientID: f.cfg.ClientID, ClientSecret: secret, RedirectURL: f.cfg.RedirectURL, Scopes: scopes,
		Endpoint: oauth2.Endpoint{AuthURL: eps.AuthorizationEndpoint, TokenURL: eps.TokenEndpoint, AuthStyle: style}}
}

// Login serves GET /auth/login?return=<path>: it opens a transaction and
// redirects to the authorization endpoint.
func (f *Flow) Login(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	eps := f.prov.Endpoints()
	if eps == nil {
		http.Error(w, "sign-in is unavailable", http.StatusServiceUnavailable)
		return
	}
	state, nonce := random(), random()
	verifier := oauth2.GenerateVerifier()
	id, err := f.tx.put(transaction{state: state, nonce: nonce, verifier: verifier, returnPath: safeReturnPath(r.URL.Query().Get("return"))})
	if err != nil {
		http.Error(w, "sign-in is unavailable", http.StatusServiceUnavailable)
		return
	}
	http.SetCookie(w, f.cookie(id, int(transactionTTL/time.Second)))
	u := f.oauth(eps, "").AuthCodeURL(state, oauth2.SetAuthURLParam("nonce", nonce), oauth2.S256ChallengeOption(verifier))
	http.Redirect(w, r, u, http.StatusFound)
}

// Callback serves GET /auth/callback. It checks the transaction, state, code
// exchange, ID-token claims and TokenReview in that order, then hands the
// result to the Completer. Any failure redirects to /signin-failed with a reason.
func (f *Flow) Callback(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query()
	var tx transaction
	res := txMissing
	if c, err := r.Cookie(loginCookie); err == nil {
		tx, res = f.tx.take(c.Value, q.Get("state"))
	}
	switch res {
	case txMismatch:
		// The cookie and transaction stay, so a forged callback cannot cancel a pending sign-in.
		f.fail(w, r, ReasonInvalid, "state mismatch")
		return
	case txMissing:
		http.SetCookie(w, f.cookie("", -1))
		f.fail(w, r, ReasonInvalid, "no open login transaction")
		return
	}
	http.SetCookie(w, f.cookie("", -1))
	if q.Get("error") != "" || q.Get("code") == "" {
		f.fail(w, r, ReasonInvalid, "the issuer returned no code")
		return
	}
	eps := f.prov.Endpoints()
	secret, err := f.clientSecret()
	if eps == nil || err != nil {
		f.fail(w, r, ReasonInvalid, "sign-in is not ready")
		return
	}
	ctx := context.WithValue(r.Context(), oauth2.HTTPClient, f.prov.HTTPClient())
	tok, err := f.oauth(eps, secret).Exchange(ctx, q.Get("code"), oauth2.VerifierOption(tx.verifier))
	if err != nil {
		f.fail(w, r, ReasonInvalid, "code exchange failed", exchangeAttrs(err)...)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	cl, err := f.checkClaims(raw, tx.nonce)
	if err != nil {
		f.fail(w, r, ReasonInvalid, "ID token "+err.Error())
		return
	}
	id, err := f.reviewer.Review(r.Context(), raw)
	switch {
	case errors.Is(err, ErrAudience):
		f.fail(w, r, ReasonAudience, "token review refused the audience")
		return
	case errors.Is(err, ErrUnauthenticated):
		f.fail(w, r, ReasonUnauthenticated, "token review did not authenticate the token")
		return
	case err != nil:
		f.fail(w, r, ReasonInvalid, "token review failed")
		return
	}
	f.completer.Complete(w, r, Result{Identity: id, Issuer: cl.Iss, Subject: cl.Sub, IDToken: raw, Expiry: unix(*cl.Exp),
		AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken, ReturnPath: safeReturnPath(tx.returnPath)})
}

// oauthErrors are the RFC 6749 §5.2 token error codes; any other value is logged as "other".
var oauthErrors = []string{"invalid_request", "invalid_client", "invalid_grant", "unauthorized_client", "unsupported_grant_type",
	"invalid_scope", "server_error", "temporarily_unavailable"}

// exchangeAttrs describes a code-exchange failure by status and a standard error code only, never issuer text.
func exchangeAttrs(err error) []any {
	var re *oauth2.RetrieveError
	if !errors.As(err, &re) || re.Response == nil {
		return nil
	}
	code := "other"
	if slices.Contains(oauthErrors, re.ErrorCode) {
		code = re.ErrorCode
	}
	return []any{"status", re.Response.StatusCode, "error", code}
}

// fail audits a failed sign-in and redirects to /signin-failed. step is a fixed description, never a token.
func (f *Flow) fail(w http.ResponseWriter, r *http.Request, reason, step string, attrs ...any) {
	rec := audit.FromRequest(r)
	rec.Level, rec.Action, rec.Outcome, rec.Reason = audit.LevelSecurity, "login", "failure", reason
	if rec.Request.Route == "" {
		rec.Request.Route = r.URL.Path
	}
	f.audit.Emit(r.Context(), rec)
	f.log.Warn("sign-in failed", append([]any{"reason", reason, "step", step, "requestID", audit.RequestIDFrom(r.Context())}, attrs...)...)
	http.Redirect(w, r, "/signin-failed?reason="+reason, http.StatusFound)
}

func (f *Flow) cookie(value string, maxAge int) *http.Cookie {
	//nolint:gosec // Secure follows --cookie-secure, true by default
	return &http.Cookie{Name: loginCookie, Value: value, Path: cookiePath, MaxAge: maxAge, HttpOnly: true, Secure: f.secure,
		SameSite: http.SameSiteLaxMode}
}

// idClaims are the ID-token claims the callback checks.
type idClaims struct {
	Iss   string   `json:"iss"`
	Sub   string   `json:"sub"`
	Aud   audience `json:"aud"`
	Azp   string   `json:"azp"`
	Exp   *float64 `json:"exp"`
	Iat   *float64 `json:"iat"`
	Nonce string   `json:"nonce"`
}

// audience is the aud claim: a string or an array of strings.
type audience []string

// UnmarshalJSON implements json.Unmarshaler.
func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*a = many
	return nil
}

// checkClaims decodes the ID token's payload without verifying its signature
// (TokenReview does that) and checks it per OIDC Core §3.1.3.7. Errors name the claim only.
func (f *Flow) checkClaims(raw, nonce string) (*idClaims, error) {
	parts := strings.Split(raw, ".")
	if raw == "" || len(raw) > maxIDToken || len(parts) != 3 {
		return nil, errors.New("is missing or malformed")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, errors.New("payload is not base64url")
	}
	var c idClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("payload is not a claims object")
	}
	now := f.clk.Now()
	switch {
	case c.Iss != f.cfg.IssuerURL:
		return nil, errors.New("iss does not match the issuer")
	case c.Sub == "":
		return nil, errors.New("has no sub")
	case !slices.Contains(c.Aud, f.cfg.ClientID):
		return nil, errors.New("aud does not contain the client ID")
	case len(c.Aud) > 1 && c.Azp == "":
		return nil, errors.New("has several audiences and no azp")
	case c.Azp != "" && c.Azp != f.cfg.ClientID:
		return nil, errors.New("azp is not the client ID")
	case c.Exp == nil || c.Iat == nil:
		return nil, errors.New("has no exp or iat")
	case now.After(unix(*c.Exp).Add(clockSkew)):
		return nil, errors.New("has expired")
	// iat has no lower bound: some issuers backdate it, and exp bounds the token's age.
	case unix(*c.Iat).After(now.Add(clockSkew)):
		return nil, errors.New("iat is in the future")
	case subtle.ConstantTimeCompare([]byte(c.Nonce), []byte(nonce)) != 1:
		return nil, errors.New("nonce does not match")
	}
	return &c, nil
}

func unix(s float64) time.Time { return time.Unix(int64(s), 0) }

// safeReturnPath returns p, normalized as http.Redirect would, when it is a
// same-origin relative path; else "/". Browsers read a backslash as a slash, so any is refused.
func safeReturnPath(p string) string {
	if len(p) > maxReturnPath || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") || strings.Contains(p, `\`) {
		return "/"
	}
	for _, c := range p {
		if c < 0x20 || c == 0x7f {
			return "/"
		}
	}
	p = cleanRedirectPath(p)
	if strings.HasPrefix(p, "//") {
		return "/"
	}
	if u, err := url.Parse(p); err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return p
}

// cleanRedirectPath cleans the path before any query, keeping a trailing slash, as http.Redirect does.
func cleanRedirectPath(p string) string {
	var query string
	if i := strings.Index(p, "?"); i != -1 {
		p, query = p[:i], p[i:]
	}
	trailing := strings.HasSuffix(p, "/")
	p = path.Clean(p)
	if trailing && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p + query
}

// random returns 256 random bits, base64url encoded.
func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
