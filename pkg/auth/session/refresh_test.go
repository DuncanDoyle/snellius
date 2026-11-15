package session

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/auth/oidc"
)

// A stale identity waits for one refresh shared by every concurrent request.
func TestRefreshSingleFlight(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.gate = make(chan struct{})
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(16 * time.Minute)
	var wg sync.WaitGroup
	codes := make([]int, 20)
	bodies := make([]string, 20)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := f.me(c)
			codes[i], bodies[i] = rec.Code, strings.TrimSpace(rec.Body.String())
		}()
	}
	waitFor(t, "a refresh", func() bool { return f.refresh.count() > 0 })
	close(f.refresh.gate)
	wg.Wait()
	if n := f.refresh.count(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
	for i := range codes {
		if codes[i] != 200 || bodies[i] != `{"username":"oidc:alice","groups":["oidc:ops"]}` {
			t.Fatalf("request %d = %d %s", i, codes[i], bodies[i])
		}
	}
}

// Close to staleness, requests are served at once while one refresh runs behind them.
func TestRefreshAhead(t *testing.T) {
	f := newFixture(t, nil)
	f.refresh.gate = make(chan struct{})
	c := f.signIn(f.result("oidc:dev"))
	f.clk.Step(14 * time.Minute)
	for range 3 {
		f.wantMe(c, 200, okBody)
	}
	waitFor(t, "a background refresh", func() bool { return f.refresh.count() == 1 })
	close(f.refresh.gate)
	waitFor(t, "groups replaced", func() bool { return strings.Contains(f.me(c).Body.String(), "oidc:ops") })
	if n := f.refresh.count(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
}

// Freshness also ends at the ID token's exp, before the review is 15 minutes old.
func TestRefreshAtTokenExpiry(t *testing.T) {
	f := newFixture(t, nil)
	res := f.result("oidc:dev")
	res.Expiry = f.clk.Now().Add(5 * time.Minute)
	c := f.signIn(res)
	f.clk.Step(6 * time.Minute)
	f.wantMe(c, 200, `{"username":"oidc:alice","groups":["oidc:ops"]}`)
	if n := f.refresh.count(); n != 1 {
		t.Fatalf("refreshes = %d, want 1", n)
	}
}

func TestRefreshFailureEndsSession(t *testing.T) {
	cases := map[string]func(f *fixture){
		"different sub": func(f *fixture) {
			f.refresh.result = func() oidc.Result { r := f.result("oidc:dev"); r.Subject = "mallory-sub"; return r }
		},
		"different iss": func(f *fixture) {
			f.refresh.result = func() oidc.Result { r := f.result("oidc:dev"); r.Issuer = "https://other.example.com"; return r }
		},
		"refresh error": func(f *fixture) { f.refresh.err = errors.New("refused") },
		"already stale": func(f *fixture) {
			f.refresh.result = func() oidc.Result { r := f.result("oidc:dev"); r.Expiry = f.clk.Now(); return r }
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			setup(f)
			c := f.signIn(f.result("oidc:dev"))
			f.clk.Step(16 * time.Minute)
			// The concurrent requests share the one failing refresh; all hear the end.
			f.wantEndedEveryRequest(c)
			if got := f.audit.actions(); strings.Join(got, ",") != "login/success,session-ended/failure/refresh" {
				t.Fatalf("audit = %v", got)
			}
			if g := f.gauge(); g != "0" {
				t.Fatalf("gauge = %s", g)
			}
			if logs := f.logs.String(); !strings.Contains(logs, "the session ends") || strings.Contains(logs, testRefresh) || strings.Contains(logs, c.Value) {
				t.Fatalf("logs = %s", logs)
			}
		})
	}
}

// Without a refresh token, or a refresher, the session ends when the identity goes stale.
func TestNoRefreshTokenEndsWhenStale(t *testing.T) {
	f := newFixture(t, nil)
	res := f.result("oidc:dev")
	res.RefreshToken = ""
	c := f.signIn(res)
	f.clk.Step(14 * time.Minute)
	f.wantMe(c, 200, okBody)
	f.clk.Step(2 * time.Minute)
	f.wantMe(c, 401, endedBody)
	if n := f.refresh.count(); n != 0 {
		t.Fatalf("refreshes = %d", n)
	}
	g := newFixture(t, func(o *Options) { o.Refresher = nil })
	c = g.signIn(g.result("oidc:dev"))
	g.clk.Step(16 * time.Minute)
	g.wantMe(c, 401, endedBody)
}

// A refresh that completes after logout is discarded, success or failure: the session stays gone.
func TestLogoutDuringRefresh(t *testing.T) {
	for name, refreshErr := range map[string]error{"success": nil, "failure": errors.New("refused")} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			f.refresh.gate, f.refresh.called, f.refresh.err = make(chan struct{}), make(chan struct{}, 1), refreshErr
			c := f.signIn(f.result("oidc:dev"))
			f.clk.Step(16 * time.Minute)
			waiting := make(chan *httptest.ResponseRecorder)
			go func() { waiting <- f.me(c) }()
			<-f.refresh.called
			if rec := f.logout(c, "https://snellius.example.com"); rec.Code != 200 {
				t.Fatalf("logout = %d", rec.Code)
			}
			close(f.refresh.gate)
			if rec := <-waiting; rec.Code != http.StatusUnauthorized || strings.TrimSpace(rec.Body.String()) != signInBody {
				t.Fatalf("waiting request = %d %s", rec.Code, rec.Body.String())
			}
			f.wantMe(c, 401, signInBody)
			if got := f.audit.actions(); strings.Join(got, ",") != "login/success,logout/success" {
				t.Fatalf("audit = %v", got)
			}
			if g := f.gauge(); g != "0" {
				t.Fatalf("gauge = %s", g)
			}
		})
	}
}

// Refresh failures log a fixed category, never the refresher's error text.
func TestRefreshFailureLogsCategoryOnly(t *testing.T) {
	cases := map[string]struct {
		err  func(cookie string) error
		edit func(f *fixture)
		want string
	}{
		"unknown error": {err: func(c string) error {
			return errors.New("id-token-value access-token-value " + testRefresh + " " + c)
		}, want: "category=refresh-failed"},
		"review failed": {err: func(c string) error { return fmt.Errorf("%s: %w", c, oidc.ErrTokenReviewFailed) }, want: "category=token-review-failed"},
		"subject changed": {edit: func(f *fixture) {
			f.refresh.result = func() oidc.Result { r := f.result(); r.Subject = "mallory-sub"; return r }
		}, want: "category=subject-changed"},
		"issuer changed": {edit: func(f *fixture) {
			f.refresh.result = func() oidc.Result { r := f.result(); r.Issuer = "https://other.example.com"; return r }
		}, want: "category=issuer-changed"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nil)
			c := f.signIn(f.result("oidc:dev"))
			if tc.err != nil {
				f.refresh.err = tc.err(c.Value)
			}
			if tc.edit != nil {
				tc.edit(f)
			}
			f.clk.Step(16 * time.Minute)
			f.wantMe(c, 401, endedBody)
			logs := f.logs.String()
			if !strings.Contains(logs, tc.want) {
				t.Fatalf("logs = %s; want %s", logs, tc.want)
			}
			for _, secret := range []string{"id-token-value", "access-token-value", testRefresh, c.Value} {
				if strings.Contains(logs, secret) {
					t.Fatalf("logs carry %q: %s", secret, logs)
				}
			}
		})
	}
}
