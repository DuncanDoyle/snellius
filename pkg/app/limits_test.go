package app

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clienttesting "k8s.io/client-go/testing"

	"github.com/DuncanDoyle/snellius/internal/testutil"
	"github.com/DuncanDoyle/snellius/pkg/api"
	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
	"github.com/DuncanDoyle/snellius/pkg/extension"
	"github.com/DuncanDoyle/snellius/pkg/install"
	"github.com/DuncanDoyle/snellius/pkg/limits"
)

// A request past the check cap counts once in snellius_limit_hits_total and
// logs once per window, with the session's audit ID and no object, also when
// its session ends during the request and it answers 401.
func TestCheckLimitCountedAndLogged(t *testing.T) {
	var log lockedBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&log, nil)))
	defer slog.SetDefault(old)
	idp, ca := newSignInIdP(t)
	dyn, kube := testutil.Clients(t, testutil.ControllerDeployment("kgw", "kgateway", "cr.kgateway.dev/kgateway-dev/kgateway:v2.4.0", 1, 1))
	kube.PrependReactor("create", "tokenreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authnv1.TokenReview{Status: authnv1.TokenReviewStatus{Authenticated: true, User: authnv1.UserInfo{Username: "oidc:alice"}}}, nil
	})
	kube.PrependReactor("create", "subjectaccessreviews", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, &authzv1.SubjectAccessReview{Status: authzv1.SubjectAccessReviewStatus{Allowed: true}}, nil
	})
	// flood spends one check past the cap on a hidden route per request; ?end=1
	// signs out after the first check, once the request is admitted (D-116).
	var (
		a   *App
		err error
	)
	flood := extension.WithHandlers("flood", func(r extension.Router, _ extension.Deps) {
		r.Handle("GET /n", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for i := range api.CheckLimit + 1 {
				if err := api.SpendCheck(r); err != nil {
					api.ReportAuthzError(r, err)
					break
				}
				if i == 0 && r.URL.Query().Get("end") != "" {
					call(a, "POST", "/auth/logout", r.Cookies(), map[string]string{"Origin": "https://s.example.com"})
				}
			}
			api.WriteJSON(w, "shop/web")
		}))
	})
	audits := &lockedBuf{}
	o := secureOpts()
	o.OIDC = oidc.Config{IssuerURL: idp.srv.URL, ClientID: "c", RedirectURL: "https://s.example.com/auth/callback", CAFile: ca}
	o.AuditOut, o.completeSecure = audits, true
	a, err = Build(Clients{Dynamic: dyn, Kube: kube, Discovery: kube.Discovery(), Scraper: &stubScraper{}}, o, extension.New(flood))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.oidc.Run(ctx)
	a.Start(ctx)
	eventually(t, "discovery", func() bool { ok, _ := a.oidc.Ready(); return ok })
	if rec := call(a, "GET", "/api/ext/flood/n?end=1", signIn(t, a, idp), nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("flood with sign-out = %d %s, want 401", rec.Code, rec.Body.String())
	}
	sess := signIn(t, a, idp)
	for range 2 {
		if rec := call(a, "GET", "/api/ext/flood/n", sess, nil); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("flood = %d %s, want 503", rec.Code, rec.Body.String())
		}
	}
	m := httptest.NewRecorder()
	a.Metrics.ServeHTTP(m, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(m.Body.String(), `snellius_limit_hits_total{limit="authz-checks"} 3`+"\n") {
		t.Fatalf("metrics:\n%s", m.Body.String())
	}
	done, stop := context.WithCancel(context.Background())
	stop()
	a.Audit.Run(done)
	var session string
	for _, l := range strings.Split(audits.String(), "\n") {
		rec := map[string]any{}
		if json.Unmarshal([]byte(l), &rec) == nil && rec["action"] == "login" && session == "" {
			session, _ = rec["session"].(string)
		}
	}
	var lines []map[string]any
	for _, l := range strings.Split(log.String(), "\n") {
		rec := map[string]any{}
		if json.Unmarshal([]byte(l), &rec) == nil && rec["msg"] == limits.Message {
			lines = append(lines, rec)
		}
	}
	if len(lines) != 1 || session == "" || lines[0]["session"] != session || lines[0]["limit"] != "authz-checks" || lines[0]["install"] != "" {
		t.Fatalf("want one line with session %q:\n%s", session, log.String())
	}
	if keys := slices.Sorted(maps.Keys(lines[0])); !slices.Equal(keys, []string{"install", "level", "limit", "msg", "session", "suppressed", "time"}) {
		t.Fatalf("line keys %v", keys)
	}
	if b, _ := json.Marshal(lines[0]); bytes.Contains(b, []byte("shop")) || bytes.Contains(b, []byte("alice")) {
		t.Fatalf("line names an object or a person: %s", b)
	}
}

// The install a request names is the one in its path, when it is current.
func TestRequestInstall(t *testing.T) {
	current := func() []install.Install { return []install.Install{{Namespace: "infra", Name: "kgw"}} }
	for path, want := range map[string]string{
		"/api/v1/installs/infra.kgw/routes":         "infra.kgw",
		"/api/v1/installs/infra.kgw":                "infra.kgw",
		"/api/v1/installs/shop.web/routes":          "",
		"/api/v1/installs":                          "",
		"/api/ext/flood/n":                          "",
		"/api/v1/installs/infra.kgw/routes/a/b/c/d": "infra.kgw",
	} {
		if got := requestInstall(httptest.NewRequest("GET", path, nil), current); got != want {
			t.Errorf("%s: %q, want %q", path, got, want)
		}
	}
}
