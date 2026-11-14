package cli

import (
	"reflect"
	"strings"

	"flag"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"testing"
	"time"
)

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
