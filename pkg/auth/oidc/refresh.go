package oidc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/oauth2"
)

// ErrNoRefreshToken means the sign-in returned no refresh token, so the identity cannot be renewed.
var ErrNoRefreshToken = errors.New("no refresh token")

// Refresh redeems refreshToken at the token endpoint (OIDC Core §12), checks the
// new ID token's claims and reviews it. The caller compares iss and sub with the
// session's. Errors name the failed step, never a token.
func (f *Flow) Refresh(ctx context.Context, refreshToken string) (Result, error) {
	if refreshToken == "" {
		return Result{}, ErrNoRefreshToken
	}
	eps := f.prov.Endpoints()
	if eps == nil {
		return Result{}, errors.New("refresh: the issuer is not discovered")
	}
	secret, err := f.clientSecret()
	if err != nil {
		return Result{}, fmt.Errorf("refresh: %w", err)
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, f.prov.HTTPClient())
	// An expired token makes the source redeem the refresh token.
	old := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}
	tok, err := f.oauth(eps, secret).TokenSource(ctx, old).Token()
	if err != nil {
		return Result{}, errors.New("refresh: the token endpoint refused the refresh token")
	}
	raw, _ := tok.Extra("id_token").(string)
	if raw == "" {
		return Result{}, errors.New("refresh: the issuer returned no ID token")
	}
	cl, err := f.checkTokenClaims(raw)
	if err != nil {
		return Result{}, fmt.Errorf("refresh: ID token %w", err)
	}
	id, err := f.reviewer.Review(ctx, raw)
	if err != nil {
		return Result{}, fmt.Errorf("refresh: %w", err)
	}
	rt := tok.RefreshToken
	if rt == "" {
		rt = refreshToken
	}
	return Result{Identity: id, Issuer: cl.Iss, Subject: cl.Sub, IDToken: raw, Expiry: unix(*cl.Exp),
		AccessToken: tok.AccessToken, RefreshToken: rt}, nil
}
