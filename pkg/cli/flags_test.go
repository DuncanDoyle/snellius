package cli

import (
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// The allow TTL defaults to 60 s (D-100).
func TestAuthzCacheTTLDefault60s(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil || o.AuthzCacheTTL != 60*time.Second {
		t.Fatalf("default %v, %v", o.AuthzCacheTTL, err)
	}
}

// The cache TTL is refused outside 30 s to 15 min.
func TestAuthzCacheTTLFlag(t *testing.T) {
	for _, bad := range []string{"29s", "16m"} {
		if _, _, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--authz-cache-ttl=" + bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	for arg, want := range map[string]time.Duration{"30s": 30 * time.Second, "15m": 15 * time.Minute} {
		_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--authz-cache-ttl=" + arg})
		if err != nil || o.AuthzCacheTTL != want {
			t.Errorf("%s: %v, %v", arg, o.AuthzCacheTTL, err)
		}
	}
}

// The defaults are the stock binary's; class-only installs are off unless asked for.
func TestParseDefaultsAndClassOnly(t *testing.T) {
	kc, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil {
		t.Fatal(err)
	}
	if kc != "" || o.PollInterval != 30*time.Second || o.MetricsInterval != 10*time.Second || o.DelegationEdgeCap != 0 || o.ClassOnlyInstalls ||
		o.Diagnostics.NoEvents || o.Diagnostics.NoCRDs || o.Diagnostics.NoProxyStats || o.Diagnostics.NoEndpoints || o.Diagnostics.ControllerLogs || o.UI == nil {
		t.Fatalf("defaults %+v", o)
	}
	kc, o, err = Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--kubeconfig=/k", "--class-only-installs", "--diagnostics-events=false", "--install=kgw/kgateway"})
	if err != nil || kc != "/k" || !o.ClassOnlyInstalls || !o.Diagnostics.NoEvents || o.Pinned != "kgw/kgateway" {
		t.Fatalf("parsed %q %+v, %v", kc, o, err)
	}
}

func TestParseAuditLevels(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil || o.AuditLevels != "security,agent,reads,summaries" {
		t.Fatalf("default: %q, %v", o.AuditLevels, err)
	}
	if _, _, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--audit-levels=security,bogus"}); err == nil {
		t.Fatal("unknown level must fail")
	}
}

// The OIDC flags land in the options; the scopes default to the full set.
func TestParseOIDCFlags(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(o.OIDC.Scopes, ","); got != "openid,profile,email,groups,offline_access" || o.OIDC.Set() {
		t.Fatalf("defaults: scopes %q, set %v", got, o.OIDC.Set())
	}
	_, o, err = Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--oidc-issuer-url=https://idp.example.com", "--oidc-client-id=snellius",
		"--oidc-client-secret-file=/s", "--oidc-redirect-url=https://s.example.com/auth/callback", "--oidc-scopes=openid, email", "--oidc-ca-file=/ca.pem"})
	want := oidc.Config{IssuerURL: "https://idp.example.com", ClientID: "snellius", ClientSecretFile: "/s",
		RedirectURL: "https://s.example.com/auth/callback", Scopes: []string{"openid", "email"}, CAFile: "/ca.pem"}
	if err != nil || !reflect.DeepEqual(o.OIDC, want) {
		t.Fatalf("parsed %+v, %v; want %+v", o.OIDC, err, want)
	}
}

// Login and session cookies are Secure unless --cookie-secure=false.
func TestParseCookieSecure(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil || o.CookieInsecure {
		t.Fatalf("default CookieInsecure = %v, %v; want false", o.CookieInsecure, err)
	}
	_, o, err = Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--cookie-secure=false"})
	if err != nil || !o.CookieInsecure {
		t.Fatalf("CookieInsecure = %v, %v; want true", o.CookieInsecure, err)
	}
}

func TestParseSessionFlags(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil || o.SessionIdle != 30*time.Minute || o.SessionAbsolute != 8*time.Hour || o.TrustedProxyCIDRs != nil {
		t.Fatalf("defaults: idle %v, absolute %v, cidrs %v, %v", o.SessionIdle, o.SessionAbsolute, o.TrustedProxyCIDRs, err)
	}
	_, o, err = Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--session-idle=5m", "--session-absolute=1h",
		"--trusted-proxy-cidrs=10.0.0.0/8, 192.168.0.0/16"})
	if err != nil || o.SessionIdle != 5*time.Minute || o.SessionAbsolute != time.Hour || len(o.TrustedProxyCIDRs) != 2 {
		t.Fatalf("parsed: idle %v, absolute %v, cidrs %v, %v", o.SessionIdle, o.SessionAbsolute, o.TrustedProxyCIDRs, err)
	}
	for _, bad := range [][]string{{"--trusted-proxy-cidrs=10.0.0.0/33"}, {"--session-idle=0"}, {"--session-absolute=-1h"}} {
		if _, _, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), bad); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}

// The named get scan is off unless asked for.
func TestNamedGetScanFlag(t *testing.T) {
	if _, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil); err != nil || o.AuthzNamedGetScan {
		t.Fatalf("default %v, %v", o.AuthzNamedGetScan, err)
	}
	if _, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--authz-named-get-scan"}); err != nil || !o.AuthzNamedGetScan {
		t.Fatalf("--authz-named-get-scan: %v, %v", o.AuthzNamedGetScan, err)
	}
}

// The global SAR limits default to 32 at once, 100 per second and a burst of 32 (D-100); each is at least 1.
func TestAuthzLimitFlags(t *testing.T) {
	_, o, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), nil)
	if err != nil || o.AuthzMaxInFlight != 32 || o.AuthzRate != 100 || o.AuthzBurst != 32 {
		t.Fatalf("defaults %d, %d, %d, %v", o.AuthzMaxInFlight, o.AuthzRate, o.AuthzBurst, err)
	}
	_, o, err = Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{"--authz-max-in-flight=8", "--authz-rate=1", "--authz-burst=200"})
	if err != nil || o.AuthzMaxInFlight != 8 || o.AuthzRate != 1 || o.AuthzBurst != 200 {
		t.Fatalf("parsed %d, %d, %d, %v", o.AuthzMaxInFlight, o.AuthzRate, o.AuthzBurst, err)
	}
	for _, bad := range []string{"--authz-max-in-flight=0", "--authz-rate=0", "--authz-burst=-1"} {
		if _, _, err := Parse(flag.NewFlagSet("t", flag.ContinueOnError), []string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
