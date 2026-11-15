package oidc

import (
	"context"
	"errors"
	"time"

	"golang.org/x/oauth2"
)

// Refresh failure categories. Refresh returns only these, so no issuer or
// cluster error text, which could quote a token, reaches a caller or a log.
var (
	// ErrNoRefreshToken means the sign-in returned no refresh token, so the identity cannot be renewed.
	ErrNoRefreshToken     = errors.New("no-refresh-token")
	ErrRefreshUnavailable = errors.New("refresh-unavailable")
	ErrTokenRefresh       = errors.New("token-refresh-failed")
	ErrIDTokenInvalid     = errors.New("id-token-invalid")
	ErrTokenReviewRefused = errors.New("token-review-refused")
	ErrTokenReviewFailed  = errors.New("token-review-failed")
)

// Refresh redeems refreshToken at the token endpoint (OIDC Core §12), checks the
// new ID token's claims and reviews it. The caller compares iss and sub with the
// session's. Every error is one of the categories above.
func (f *Flow) Refresh(ctx context.Context, refreshToken string) (Result, error) {
	if refreshToken == "" {
		return Result{}, ErrNoRefreshToken
	}
	eps := f.prov.Endpoints()
	if eps == nil {
		return Result{}, ErrRefreshUnavailable
	}
	secret, err := f.clientSecret()
	if err != nil {
		return Result{}, ErrRefreshUnavailable
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, f.prov.HTTPClient())
	// An expired token makes the source redeem the refresh token.
	old := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}
	tok, err := f.oauth(eps, secret).TokenSource(ctx, old).Token()
	if err != nil {
		return Result{}, ErrTokenRefresh
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Result{}, ErrIDTokenInvalid
	}
	cl, err := f.checkTokenClaims(raw)
	if err != nil {
		return Result{}, ErrIDTokenInvalid
	}
	id, err := f.reviewer.Review(ctx, raw)
	switch {
	case errors.Is(err, ErrUnauthenticated):
		return Result{}, ErrTokenReviewRefused
	case err != nil:
		return Result{}, ErrTokenReviewFailed
	}
	rt := tok.RefreshToken
	if rt == "" {
		rt = refreshToken
	}
	return Result{Identity: id, Issuer: cl.Iss, Subject: cl.Sub, IDToken: raw, Expiry: unix(*cl.Exp),
		AccessToken: tok.AccessToken, RefreshToken: rt}, nil
}
