// Command oidc-issuer is an OpenID provider for the secure-mode e2e tests. It
// serves HTTPS with a certificate from a CA it generates and writes to --ca-out.
//
//	oidc-issuer --issuer https://oidc.snellius.test:18443 --client-id snellius \
//	  --redirect-uri http://127.0.0.1:18080/auth/callback --user alice=team-a,team-b --user bob=team-b
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// multi is a repeatable string flag.
type multi []string

func (m *multi) String() string     { return strings.Join(*m, " ") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("oidc-issuer", flag.ContinueOnError)
	issuerURL := fs.String("issuer", "https://oidc.snellius.test:18443", "issuer URL; its host is the certificate's SAN")
	listen := fs.String("listen", ":18443", "listen address")
	caOut := fs.String("ca-out", "ca.pem", "where to write the generated CA certificate (PEM)")
	clientID := fs.String("client-id", "snellius", "the one registered client")
	secretFile := fs.String("client-secret-file", "", "file holding the client secret; empty accepts a public client")
	ttl := fs.Duration("token-ttl", time.Hour, "ID token lifetime")
	var redirects, users multi
	fs.Var(&redirects, "redirect-uri", "registered redirect URI (repeatable)")
	fs.Var(&users, "user", "test user as name=group1,group2 (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	u, err := url.Parse(*issuerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return fmt.Errorf("--issuer must be an https URL, got %q", *issuerURL)
	}
	if len(redirects) == 0 || len(users) == 0 {
		return errors.New("need at least one --redirect-uri and one --user")
	}
	parsed, err := parseUsers(users)
	if err != nil {
		return err
	}
	var secret string
	if *secretFile != "" {
		b, err := os.ReadFile(*secretFile)
		if err != nil {
			return err
		}
		secret = strings.TrimSpace(string(b))
	}
	cert, caPEM, err := newServerCert(u.Hostname())
	if err != nil {
		return err
	}
	if err := os.WriteFile(*caOut, caPEM, 0o600); err != nil {
		return err
	}
	iss, err := NewIssuer(Config{URL: strings.TrimSuffix(*issuerURL, "/"), ClientID: *clientID, ClientSecret: secret,
		RedirectURIs: redirects, Users: parsed, TokenTTL: *ttl})
	if err != nil {
		return err
	}
	srv := &http.Server{Addr: *listen, Handler: iss.Handler(), ReadHeaderTimeout: 10 * time.Second,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	log.Printf("oidc-issuer %s listening on %s", iss.URL, *listen)
	return srv.ListenAndServeTLS("", "")
}

// parseUsers reads name=group1,group2 entries; a user may have no groups.
func parseUsers(in []string) ([]user, error) {
	out := make([]user, 0, len(in))
	for _, s := range in {
		name, groups, _ := strings.Cut(s, "=")
		if name == "" {
			return nil, fmt.Errorf("--user %q has no name", s)
		}
		u := user{name: name}
		for _, g := range strings.Split(groups, ",") {
			if g != "" {
				u.groups = append(u.groups, g)
			}
		}
		out = append(out, u)
	}
	return out, nil
}

// newServerCert returns a TLS certificate for host, signed by a fresh CA, and the CA as PEM.
func newServerCert(host string) (tls.Certificate, []byte, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	now := time.Now()
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "snellius test issuer CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	ca, err := x509.ParseCertificate(caDER)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if ip := net.ParseIP(host); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	} else {
		tmpl.DNSNames = []string{host}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), nil
}
