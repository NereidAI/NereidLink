// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package oidc implements external OpenID Connect single sign-on (ADR-0021):
// authorization-code + PKCE login against an external IdP, OIDC discovery and
// JWKS verification (go-oidc), and just-in-time local provisioning. A
// successful SSO login mints the same local session/TokenPair as a password
// login, so downstream authorization (RBAC, sessions, WS) is unchanged.
//
// State, nonce and the PKCE verifier are kept in a bounded in-process store
// (all-in-one deployment; no Redis per the architecture), keyed by the
// opaque state parameter and expired by TTL.
package oidc

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	coreoidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Config parameterizes one OIDC relying party.
type Config struct {
	// Issuer is the discovery base URL.
	Issuer string
	// ClientID/Secret identify the relying party.
	ClientID     string
	ClientSecret string
	// RedirectURL must match the IdP registration exactly.
	RedirectURL string
	// FrontendBaseURL is where the browser is sent after completion.
	FrontendBaseURL string
	// Scopes defaults to openid/email/profile when empty.
	Scopes []string

	// TenantID receives JIT-provisioned users and their membership.
	TenantID string
	// DefaultRole is assigned when no group claim maps; empty rejects.
	DefaultRole string
	// GroupsClaim names the claim carrying IdP group names.
	GroupsClaim string
	// GroupRole maps an IdP group name to a built-in role.
	GroupRole map[string]string

	// StateTTL bounds the authorization-state store.
	StateTTL time.Duration
	// CAFile is an optional PEM trusted in addition to system roots for
	// discovery, JWKS and token exchange (internal/private IdP CA).
	CAFile string
	// Store carries single-use login state; nil selects the in-memory
	// store (single process). A KV-backed store is required when multiple
	// serve replicas share one ingress.
	Store StateStore
}

// Sentinel errors.
var (
	ErrDisabled     = errors.New("oidc: single sign-on is not enabled")
	ErrInvalidState = errors.New("oidc: invalid or expired state parameter")
	ErrNoEmail      = errors.New("oidc: id token has no email claim")
	ErrNoRole       = errors.New("oidc: no role mapped for the user's groups and no default role")
)

// IdentityProvisioner is the identity surface the callback drives.
// *identity.Service satisfies it.
type IdentityProvisioner interface {
	UpsertExternalUser(ctx context.Context, in identity.ExternalUser) (identity.User, error)
	IssuePairForUser(ctx context.Context, userID, ip, userAgent string) (identity.TokenPair, error)
}

// Service owns the OIDC provider, oauth2 config and login state.
type Service struct {
	cfg        Config
	provider   *coreoidc.Provider
	verifier   *coreoidc.IDTokenVerifier
	oauth      *oauth2.Config
	httpClient *http.Client
	ident      IdentityProvisioner
	pool       *pgxpool.Pool
	states     StateStore
}

// httpClientFromCAFile builds a client that trusts the system roots plus an
// optional PEM bundle (an internal IdP CA). An empty path keeps the default
// client. A missing/unreadable file or a PEM with no certificate is fatal so
// a misconfigured private CA fails wiring instead of the first login.
func httpClientFromCAFile(caFile string) (*http.Client, error) {
	if caFile == "" {
		return http.DefaultClient, nil
	}
	pem, err := os.ReadFile(caFile) //nolint:gosec // G304: path comes from trusted operator config
	if err != nil {
		return nil, fmt.Errorf("read oidc ca file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("oidc ca file %q contains no PEM certificates", caFile)
	}
	return &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{RootCAs: pool}, //nolint:gosec // system roots + configured IdP CA
		},
	}, nil
}

// NewService discovers the IdP and constructs the relying party. Discovery is
// performed eagerly so a misconfigured issuer fails wiring rather than the
// first login.
func NewService(ctx context.Context, cfg Config, ident IdentityProvisioner, pool *pgxpool.Pool) (*Service, error) {
	if cfg.StateTTL <= 0 {
		cfg.StateTTL = 10 * time.Minute
	}
	if len(cfg.Scopes) == 0 {
		cfg.Scopes = []string{coreoidc.ScopeOpenID, "email", "profile"}
	}
	if cfg.GroupsClaim == "" {
		cfg.GroupsClaim = "groups"
	}

	if cfg.Store == nil {
		cfg.Store = newStateStore(cfg.StateTTL)
	}

	httpClient, err := httpClientFromCAFile(cfg.CAFile)
	if err != nil {
		return nil, err
	}
	// The custom client must carry discovery, JWKS verification and token
	// exchange; go-oidc stores the context client on the provider for its
	// remote key set, and the oauth2 context key is shared.
	discoveryCtx := coreoidc.ClientContext(ctx, httpClient)
	provider, err := coreoidc.NewProvider(discoveryCtx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery: %w", err)
	}
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.RedirectURL,
		Scopes:       cfg.Scopes,
	}
	verifier := provider.Verifier(&coreoidc.Config{ClientID: cfg.ClientID})

	return &Service{
		cfg:        cfg,
		provider:   provider,
		verifier:   verifier,
		oauth:      oauthCfg,
		httpClient: httpClient,
		ident:      ident,
		pool:       pool,
		states:     cfg.Store,
	}, nil
}

// loginState is one in-flight authorization-code transaction.
type loginState struct {
	nonce        string
	codeVerifier string
	redirectTo   string
	expiresAt    time.Time
}

// LoginResult returns the IdP authorization URL and the state stored for it.
func (s *Service) AuthURL(redirectTo string) (string, error) {
	state, err := randomToken(24)
	if err != nil {
		return "", err
	}
	nonce, err := randomToken(24)
	if err != nil {
		return "", err
	}
	verifier := oauth2.GenerateVerifier()

	if err := s.states.put(state, loginState{
		nonce:        nonce,
		codeVerifier: verifier,
		redirectTo:   redirectTo,
		expiresAt:    time.Now().Add(s.cfg.StateTTL),
	}); err != nil {
		return "", err
	}

	return s.oauth.AuthCodeURL(state,
		coreoidc.Nonce(nonce),
		oauth2.S256ChallengeOption(verifier),
	), nil
}

// CallbackResult is a completed SSO login.
type CallbackResult struct {
	// Pair is the local session token pair.
	Pair identity.TokenPair
	// RedirectTo is the SPA path captured at login start.
	RedirectTo string
}

// Complete exchanges the authorization code, verifies the id token, JIT
// provisions/updates the user, maps a role and issues a local session.
func (s *Service) Complete(ctx context.Context, code, stateValue, ip, userAgent string) (CallbackResult, error) {
	st, ok := s.states.take(stateValue)
	if !ok {
		return CallbackResult{}, ErrInvalidState
	}

	token, err := s.oauth.Exchange(coreoidc.ClientContext(ctx, s.httpClient), code,
		oauth2.SetAuthURLParam("code_verifier", st.codeVerifier))
	if err != nil {
		return CallbackResult{}, fmt.Errorf("oidc token exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return CallbackResult{}, errors.New("oidc: no id_token in token response")
	}
	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return CallbackResult{}, fmt.Errorf("oidc verify id token: %w", err)
	}
	if idToken.Nonce != st.nonce {
		return CallbackResult{}, errors.New("oidc: id token nonce mismatch")
	}

	claims, err := decodeClaims(idToken, s.cfg.GroupsClaim)
	if err != nil {
		return CallbackResult{}, err
	}
	if claims.email == "" {
		return CallbackResult{}, ErrNoEmail
	}

	role := s.mapRole(claims.groups)
	if role == "" {
		return CallbackResult{}, ErrNoRole
	}

	user, err := s.ident.UpsertExternalUser(ctx, identity.ExternalUser{
		Email:         claims.email,
		DisplayName:   claims.name,
		EmailVerified: claims.emailVerified,
		TenantID:      s.cfg.TenantID,
	})
	if err != nil {
		return CallbackResult{}, err
	}
	if s.cfg.TenantID != "" {
		if err := rbac.EnsureMembership(ctx, s.pool, user.ID, s.cfg.TenantID, role); err != nil {
			return CallbackResult{}, fmt.Errorf("oidc role mapping: %w", err)
		}
	}

	pair, err := s.ident.IssuePairForUser(ctx, user.ID, ip, userAgent)
	if err != nil {
		return CallbackResult{}, err
	}
	return CallbackResult{Pair: pair, RedirectTo: st.redirectTo}, nil
}

// mapRole returns the first role whose configured group is present.
func (s *Service) mapRole(groups []string) string {
	for _, g := range groups {
		if role, ok := s.cfg.GroupRole[g]; ok && validRole(role) {
			return role
		}
	}
	if validRole(s.cfg.DefaultRole) {
		return s.cfg.DefaultRole
	}
	return ""
}

// validRole accepts the assignable built-in human roles (not service account
// or platform admin, which must never be granted by an IdP group mapping).
func validRole(role string) bool {
	switch role {
	case rbac.RoleTenantAdmin, rbac.RoleEngineer,
		rbac.RoleCustomerUser, rbac.RoleReadOnly:
		return true
	default:
		return false
	}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
