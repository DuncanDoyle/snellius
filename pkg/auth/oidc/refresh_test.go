package oidc

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestRefresh(t *testing.T) {
	f := newFlowFixture(t, "s3cret")
	f.reviewer.id = Identity{Username: "oidc:alice", Groups: []string{"oidc:ops"}}
	res, err := f.flow.Refresh(context.Background(), testRefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	f.idp.mu.Lock()
	idTok, form := f.idp.idToken, f.idp.form
	f.idp.mu.Unlock()
	if res.Issuer != f.idp.srv.URL || res.Subject != "alice-sub" || res.IDToken != idTok || res.AccessToken != "refreshed-access" {
		t.Fatalf("result = %+v", res)
	}
	// The refresh token is kept when the issuer does not rotate it.
	if res.RefreshToken != testRefreshToken || res.Identity.Groups[0] != "oidc:ops" || f.reviewer.token != idTok {
		t.Fatalf("refresh token %q, groups %v, reviewed %q", res.RefreshToken, res.Identity.Groups, f.reviewer.token)
	}
	if form.Get("grant_type") != "refresh_token" || !f.idp.basicAuth {
		t.Fatalf("token request %v, basic auth %v", form, f.idp.basicAuth)
	}
	if !res.Expiry.Equal(f.clk.Now().Add(5 * time.Minute).Truncate(time.Second)) {
		t.Fatalf("expiry %v", res.Expiry)
	}
}

func TestRefreshFailures(t *testing.T) {
	cases := map[string]func(f *flowFixture) (token string){
		"rejected refresh token": func(*flowFixture) string { return "other" },
		"no ID token":            func(f *flowFixture) string { f.idp.noIDToken = true; return testRefreshToken },
		"wrong audience": func(f *flowFixture) string {
			f.idp.claims = func(c map[string]any) { c["aud"] = "other" }
			return testRefreshToken
		},
		"expired": func(f *flowFixture) string {
			f.idp.claims = func(c map[string]any) { c["exp"] = f.clk.Now().Add(-5 * time.Minute).Unix() }
			return testRefreshToken
		},
		"review refused": func(f *flowFixture) string { f.reviewer.err = ErrUnauthenticated; return testRefreshToken },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFlowFixture(t, "")
			tok := setup(f)
			if _, err := f.flow.Refresh(context.Background(), tok); err == nil {
				t.Fatal("refresh succeeded")
			} else if strings.Contains(err.Error(), testRefreshToken) || strings.Contains(err.Error(), "eyJ") {
				t.Fatalf("error carries a token: %v", err)
			}
		})
	}
	t.Run("no refresh token", func(t *testing.T) {
		f := newFlowFixture(t, "")
		if _, err := f.flow.Refresh(context.Background(), ""); !errors.Is(err, ErrNoRefreshToken) {
			t.Fatalf("err = %v", err)
		}
		if f.idp.refreshes != 0 {
			t.Fatal("asked the issuer without a refresh token")
		}
	})
}

// A reviewer error is reduced to a fixed category; its text never reaches the caller.
func TestRefreshErrorsAreCategories(t *testing.T) {
	const leak = "id=eyJleak access=access-leak refresh=refresh-leak cookie=cookie-leak"
	for name, c := range map[string]struct {
		err  error
		want error
	}{
		"unknown":  {errors.New(leak), ErrTokenReviewFailed},
		"refused":  {fmt.Errorf("%s: %w", leak, ErrUnauthenticated), ErrTokenReviewRefused},
		"audience": {fmt.Errorf("%s: %w", leak, ErrAudience), ErrTokenReviewRefused},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFlowFixture(t, "")
			f.reviewer.err = c.err
			_, err := f.flow.Refresh(context.Background(), testRefreshToken)
			if !errors.Is(err, c.want) || strings.Contains(err.Error(), "leak") {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}
