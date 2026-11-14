package oidc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"k8s.io/utils/clock"
)

// Backoff bounds for discovery retries.
const (
	initialBackoff = time.Second
	maxBackoff     = time.Minute
	maxDocBytes    = 1 << 20
)

// Endpoints are the discovered provider endpoints.
type Endpoints struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	// EndSessionEndpoint is empty when the provider has none.
	EndSessionEndpoint string
}

// Provider discovers the issuer's endpoints and holds the HTTP client for
// discovery and token calls.
type Provider struct {
	cfg    Config
	client *http.Client
	clk    clock.Clock

	mu      sync.Mutex
	eps     *Endpoints
	lastErr error
}

// NewProvider builds a Provider. With cfg.CAFile, its PEM certificates are
// added to a copy of the system roots; an unreadable or empty bundle is an error.
func NewProvider(cfg Config, clk clock.Clock) (*Provider, error) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		pemBytes, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read OIDC CA file: %w", err)
		}
		if !pool.AppendCertsFromPEM(pemBytes) {
			return nil, errors.New("OIDC CA file holds no PEM certificate")
		}
		tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	}
	return &Provider{cfg: cfg, clk: clk, client: &http.Client{Transport: tr, Timeout: 10 * time.Second,
		// A redirect is a failure: it could carry the code or secret to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		lastErr: errors.New("discovery has not run")}, nil
}

// HTTPClient is the client for discovery and token calls; it trusts the configured CA.
func (p *Provider) HTTPClient() *http.Client { return p.client }

// Ready reports whether discovery succeeded; when not, err is the last failure.
func (p *Provider) Ready() (ready bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eps != nil, p.lastErr
}

// Endpoints returns the discovered endpoints, or nil before discovery succeeds.
func (p *Provider) Endpoints() *Endpoints {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.eps
}

// Run discovers until it succeeds, retrying with a doubling backoff from 1 s to
// 1 min, and returns on success or when ctx is done.
func (p *Provider) Run(ctx context.Context) {
	delay := initialBackoff
	for {
		eps, err := p.Discover(ctx, p.client)
		p.mu.Lock()
		p.eps, p.lastErr = eps, err
		p.mu.Unlock()
		if err == nil || ctx.Err() != nil {
			return
		}
		t := p.clk.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
		delay = nextBackoff(delay)
	}
}

func nextBackoff(d time.Duration) time.Duration { return min(d*2, maxBackoff) }

// Discover reads the issuer's discovery document with client. The document's
// issuer must equal the configured one, and the endpoints must always be https.
func (p *Provider) Discover(ctx context.Context, client *http.Client) (*Endpoints, error) {
	u := strings.TrimSuffix(p.cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("discovery request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discovery request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discovery: status %d", resp.StatusCode)
	}
	var doc struct {
		Issuer        string `json:"issuer"`
		Authorization string `json:"authorization_endpoint"`
		Token         string `json:"token_endpoint"`
		EndSession    string `json:"end_session_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxDocBytes)).Decode(&doc); err != nil {
		return nil, fmt.Errorf("discovery document: %w", err)
	}
	if doc.Issuer != p.cfg.IssuerURL {
		return nil, fmt.Errorf("discovery issuer %q does not match the configured issuer %q", doc.Issuer, p.cfg.IssuerURL)
	}
	for _, e := range []struct{ name, v string }{{"authorization_endpoint", doc.Authorization}, {"token_endpoint", doc.Token}} {
		if why := checkURL(e.v, false); why != "" {
			return nil, fmt.Errorf("discovery %s %s", e.name, why)
		}
	}
	if doc.EndSession != "" {
		if why := checkURL(doc.EndSession, false); why != "" {
			return nil, fmt.Errorf("discovery end_session_endpoint %s", why)
		}
	}
	return &Endpoints{AuthorizationEndpoint: doc.Authorization, TokenEndpoint: doc.Token, EndSessionEndpoint: doc.EndSession}, nil
}
