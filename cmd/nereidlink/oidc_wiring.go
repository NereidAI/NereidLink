// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/nereidai/nereidlink/internal/identity"
	appoidc "github.com/nereidai/nereidlink/internal/oidc"
	"github.com/nereidai/nereidlink/internal/sso"
)

// oidcAdapter narrows *oidc.Service to the gateway handler interface.
type oidcAdapter struct {
	svc *appoidc.Service
}

func (a oidcAdapter) AuthURL(redirectTo string) (string, error) {
	return a.svc.AuthURL(redirectTo)
}

func (a oidcAdapter) Complete(ctx context.Context, code, state, ip, userAgent string) (identity.TokenPair, string, error) {
	res, err := a.svc.Complete(ctx, code, state, ip, userAgent)
	if err != nil {
		return identity.TokenPair{}, "", err
	}
	return res.Pair, res.RedirectTo, nil
}

// ssoAdapter narrows *sso.Service to the gateway handler interface.
type ssoAdapter struct {
	svc *sso.Service
}

func (a ssoAdapter) VerifyAndSession(ctx context.Context, ticket, ip, userAgent string) (identity.TokenPair, string, error) {
	return a.svc.VerifyAndSession(ctx, ticket, ip, userAgent)
}

func (a ssoAdapter) CallbackPath() string {
	return a.svc.CallbackPath()
}

func (a ssoAdapter) LoginURL() string {
	return a.svc.LoginURL()
}

func (a ssoAdapter) ListTenants(ctx context.Context) ([]sso.TenantInfo, error) {
	return a.svc.ListTenants(ctx)
}

func (a ssoAdapter) IntegrationSecret() string {
	return a.svc.IntegrationSecret()
}
