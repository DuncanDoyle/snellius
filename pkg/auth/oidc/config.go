// Package oidc holds the OpenID Connect settings and discovery for secure-mode sign-in.
package oidc

import (
	"net/url"
	"slices"
	"strings"
)

// DefaultScopes are requested when Config.Scopes is empty.
var DefaultScopes = []string{"openid", "profile", "email", "groups", "offline_access"}

// Config is the OpenID Connect client configuration.
type Config struct {
	// IssuerURL is the issuer identifier; discovery reads it plus /.well-known/openid-configuration.
	IssuerURL string
	// ClientID identifies Snellius at the issuer.
	ClientID string
	// ClientSecretFile holds the client secret; empty means a public client.
	ClientSecretFile string
	// RedirectURL is the registered callback, normally https://<host>/auth/callback.
	RedirectURL string
	// Scopes are the requested scopes; empty means DefaultScopes.
	Scopes []string
	// CAFile is a PEM bundle added to the system roots for issuers behind a private CA.
	CAFile string
}

// Set reports whether any OIDC setting differs from its default.
func (c Config) Set() bool {
	customScopes := len(c.Scopes) > 0 && !slices.Equal(c.Scopes, DefaultScopes)
	return c.IssuerURL != "" || c.ClientID != "" || c.ClientSecretFile != "" || c.RedirectURL != "" || c.CAFile != "" || customScopes
}

// Validate checks that the issuer and redirect URLs are absolute https and the
// client ID is set. The redirect may be http on a loopback host, for testing.
// The error names every problem and holds no secret.
func (c Config) Validate() error {
	var bad []string
	if err := checkURL(c.IssuerURL, false); err != "" {
		bad = append(bad, "issuer URL "+err)
	}
	if c.ClientID == "" {
		bad = append(bad, "client ID is required")
	}
	if err := checkURL(c.RedirectURL, true); err != "" {
		bad = append(bad, "redirect URL "+err)
	}
	if len(c.Scopes) > 0 && !slices.Contains(c.Scopes, "openid") {
		bad = append(bad, `scopes must include "openid"`)
	}
	if len(bad) == 0 {
		return nil
	}
	return &configError{strings.Join(bad, "; ")}
}

type configError struct{ msg string }

func (e *configError) Error() string { return e.msg }

// checkURL returns why s is not an acceptable absolute https URL, or "".
// With loopbackHTTP, http is accepted when the host is localhost or 127.0.0.1.
func checkURL(s string, loopbackHTTP bool) string {
	if s == "" {
		return "is required"
	}
	u, err := url.Parse(s)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return "must be an absolute URL"
	}
	if u.Fragment != "" || u.User != nil {
		return "must not have a fragment or user info"
	}
	switch {
	case u.Scheme == "https":
		return ""
	case u.Scheme == "http" && loopbackHTTP && isLoopback(u.Hostname()):
		return ""
	}
	return "must use https"
}

func isLoopback(host string) bool { return host == "localhost" || host == "127.0.0.1" }
