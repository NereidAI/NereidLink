// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package sso implements NereidSphere (NS) one-time handoff ticket SSO.
// This is an alternative to OIDC where the suite portal (NS) owns the
// identity plane: NS mints a 30-second single-use ticket after the user
// logs in, redirects the browser to the product with the ticket, and the
// product exchanges it server-to-server at NS's /api/v1/auth/ticket/verify
// using its shared secret.
package sso

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// ErrInvalidTicket means the ticket was not found, expired, replayed or
// targeted at a different product audience.
var ErrInvalidTicket = errors.New("sso: invalid or expired ticket")

// ErrTicketStoreUnavailable means NS could not enforce ticket single-use
// (its shared replay store is down); NS deliberately refuses instead of
// letting a possibly-replayed ticket through.
var ErrTicketStoreUnavailable = errors.New("sso: ticket store unavailable")

// Config holds the NS ticket mode configuration.
type Config struct {
	NSBaseURL       string
	ClientID        string
	SSOSecret       string
	CallbackPath    string
	FrontendBaseURL string
	TenantID        string
	DefaultRole     string
	// RoleMap maps an NS identity role (a Keycloak realm role carried in
	// the ticket's identity.roles) to a built-in NL role. Used only on the
	// no-groups fallback path.
	RoleMap map[string]string
	// GroupRole maps the last path segment of a
	// /nereidlink/{tenant}/{role} group (admins|engineers|read_only) to a
	// built-in NL role. The tenant is the middle path segment, so it is not
	// part of the key.
	GroupRole map[string]string
	// ReconcileMemberships removes suite-managed membership roles (and
	// empty memberships) no longer present in the ticket's group set on
	// each group-based SSO login.
	ReconcileMemberships bool
	// CAFile optionally trusts an additional PEM bundle for the HTTPS call
	// to NS (internal/private CA).
	CAFile string
}

// Identity mirrors the NS ticket verify response identity object
// (internal/auth/session.go Identity in NereidSphere).
type Identity struct {
	// Sub is the stable provider subject identifier.
	Sub string `json:"sub"`
	// Email is the IdP-verified mailbox.
	Email string `json:"email"`
	// Name is the display name.
	Name string `json:"name"`
	// Username is the preferred_username claim.
	Username string `json:"preferred_username"`
	// Roles are the IdP realm roles carried through the NS session.
	Roles []string `json:"roles"`
	// Groups are the NI realm-group paths carried by NS (omitempty at the
	// NS side, so it is absent — not empty — for users with no suite
	// assignments). Link consumes only /nereidlink/{tenant}/{role} entries.
	Groups []string `json:"groups"`
}

// tenantRole is one parsed /nereidlink/{tenant}/{role} suite assignment.
type tenantRole struct {
	TenantID string
	RoleKey  string // admins | engineers | read_only
}

// linkGroupPrefix identifies Link product groups in the NI group path
// convention /nereid{product}/{tenant}/{role}.
const linkGroupPrefix = "/nereidlink/"

// parseLinkGroups returns the Link assignments found in groups, ignoring
// other-product entries, malformed paths and non-UUID tenants.
func parseLinkGroups(groups []string) []tenantRole {
	var out []tenantRole
	for _, g := range groups {
		if !strings.HasPrefix(g, linkGroupPrefix) {
			continue
		}
		rest := strings.TrimPrefix(g, linkGroupPrefix)
		parts := strings.Split(rest, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		tenantID, roleKey := parts[0], parts[1]
		if _, err := uuid.Parse(tenantID); err != nil {
			continue // Link tenant ids are UUIDs
		}
		out = append(out, tenantRole{TenantID: tenantID, RoleKey: roleKey})
	}
	return out
}

// TicketVerifyResponse is the NS ticket verification response.
type TicketVerifyResponse struct {
	Identity Identity `json:"identity"`
	// To is the product-internal path the user originally asked for.
	To string `json:"to"`
}

// IdentityProvisioner is the subset of identity.Service used for JIT
// provisioning and local session issuance.
type IdentityProvisioner interface {
	UpsertExternalUser(ctx context.Context, in identity.ExternalUser) (identity.User, error)
	IssuePairForUser(ctx context.Context, userID, ip, userAgent string) (identity.TokenPair, error)
}

// Service handles NS ticket verification and local session issuance.
type Service struct {
	cfg        Config
	ident      IdentityProvisioner
	pool       *pgxpool.Pool
	httpClient *http.Client
}

// NewService creates a new NS ticket SSO service.
func NewService(cfg Config, ident IdentityProvisioner, pool *pgxpool.Pool) (*Service, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read sso ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("sso ca_file %q contains no PEM certificate", cfg.CAFile)
		}
		if transport.TLSClientConfig == nil {
			transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &Service{
		cfg:        cfg,
		ident:      ident,
		pool:       pool,
		httpClient: &http.Client{Timeout: 10 * time.Second, Transport: transport},
	}, nil
}

// VerifyAndSession exchanges the ticket with NS, JIT-provisions the user,
// maps a role, and issues a local session token pair.
func (s *Service) VerifyAndSession(ctx context.Context, ticket, ip, userAgent string) (identity.TokenPair, string, error) {
	verifyURL := s.cfg.NSBaseURL + "/api/v1/auth/ticket/verify"
	reqBody, _ := json.Marshal(struct {
		Ticket string `json:"ticket"`
	}{Ticket: ticket})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, bytes.NewReader(reqBody))
	if err != nil {
		return identity.TokenPair{}, "", fmt.Errorf("build verify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NS-Client-Id", s.cfg.ClientID)
	req.Header.Set("X-NS-Client-Secret", s.cfg.SSOSecret)

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return identity.TokenPair{}, "", fmt.Errorf("ticket verify request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
		// verified below
	case http.StatusServiceUnavailable:
		return identity.TokenPair{}, "", ErrTicketStoreUnavailable
	case http.StatusUnauthorized:
		return identity.TokenPair{}, "", errors.New("sso: invalid NS client credentials")
	default:
		var errResp struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		switch errResp.Error.Code {
		case "TICKET_INVALID", "TICKET_EXPIRED", "TICKET_AUDIENCE",
			"TICKET_NOT_FOUND", "TICKET_UNSUPPORTED", "BAD_REQUEST":
			return identity.TokenPair{}, "",
				fmt.Errorf("%w: %s", ErrInvalidTicket, errResp.Error.Code)
		}
		return identity.TokenPair{}, "", fmt.Errorf("sso: ticket verify failed (%d): %s",
			resp.StatusCode, errResp.Error.Code)
	}

	var verifyResp TicketVerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		return identity.TokenPair{}, "", fmt.Errorf("decode verify response: %w", err)
	}
	ident := verifyResp.Identity
	if ident.Email == "" {
		return identity.TokenPair{}, "", errors.New("sso: ticket identity carries no email")
	}

	assignments := parseLinkGroups(ident.Groups)

	// Home tenant for a freshly JIT-provisioned account: the suite
	// assignment's tenant. The NS console assigns one tenant per product,
	// so this is normally the single group tenant; with several groups
	// pick deterministically (sorted first) and applyGroupAssignments
	// self-heals the home on later logins. Group-less tickets keep the
	// configured fallback tenant.
	homeTenant := s.cfg.TenantID
	if len(assignments) > 0 {
		if t := firstTenant(assignments); t != "" {
			homeTenant = t
		}
	}

	// The mailbox in an NS session came from a verified OIDC id token.
	user, err := s.ident.UpsertExternalUser(ctx, identity.ExternalUser{
		Email:         ident.Email,
		DisplayName:   ident.Name,
		EmailVerified: true,
		TenantID:      homeTenant,
	})
	if err != nil {
		return identity.TokenPair{}, "", fmt.Errorf("upsert external user: %w", err)
	}

	if len(assignments) == 0 {
		// Backward-compatible path: the ticket carries no Link group
		// assignment (older NS or unassigned user). Keep mapping the
		// realm role into the configured default tenant.
		role := s.mapRole(ident.Roles)
		if role == "" {
			return identity.TokenPair{}, "", errors.New("sso: no role mapped")
		}
		// EnsureMembership is additive and idempotent: an existing platform
		// admin/TENANT_ADMIN membership is never downgraded by the mapped role.
		if s.cfg.TenantID != "" {
			if err := rbac.EnsureMembership(ctx, s.pool, user.ID, s.cfg.TenantID, role); err != nil {
				return identity.TokenPair{}, "", fmt.Errorf("sso role mapping: %w", err)
			}
		}
	} else {
		// Suite-managed multi-tenant path: each /nereidlink/{tenant}/{role}
		// group becomes a local membership role, and access revoked in NS
		// is reconciled away.
		if err := s.applyGroupAssignments(ctx, user.ID, assignments); err != nil {
			return identity.TokenPair{}, "", err
		}
	}

	pair, err := s.ident.IssuePairForUser(ctx, user.ID, ip, userAgent)
	if err != nil {
		return identity.TokenPair{}, "", fmt.Errorf("issue token pair: %w", err)
	}
	return pair, verifyResp.To, nil
}

// mapRole returns the first configured role present in the ticket's role
// list, otherwise the default role.
func (s *Service) mapRole(roles []string) string {
	for _, r := range roles {
		if mapped, ok := s.cfg.RoleMap[r]; ok && validRole(mapped) {
			return mapped
		}
	}
	if validRole(s.cfg.DefaultRole) {
		return s.cfg.DefaultRole
	}
	return ""
}

// firstTenant returns the deterministic (sorted) first tenant id among
// the assignments, used to anchor a freshly provisioned account.
func firstTenant(assignments []tenantRole) string {
	if len(assignments) == 0 {
		return ""
	}
	ids := make(map[string]struct{}, len(assignments))
	for _, a := range assignments {
		ids[a.TenantID] = struct{}{}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out[0]
}

// mapGroupRoleKey maps the role segment of a /nereidlink/{tenant}/{role}
// group to a built-in role; "" means unknown/unconfigured.
func (s *Service) mapGroupRoleKey(roleKey string) string {
	role, ok := s.cfg.GroupRole[roleKey]
	if !ok || !validRole(role) {
		return ""
	}
	return role
}

// applyGroupAssignments materialises the NS group assignments as local
// membership roles and then reconciles away suite-managed access that NS
// no longer grants. Unknown role keys skip their entry; other failures
// fail the login.
func (s *Service) applyGroupAssignments(ctx context.Context, userID string, assignments []tenantRole) error {
	// Deduplicate per tenant; NS assigns one role per product+tenant, but
	// converge defensively on duplicate groups.
	target := make(map[string]string, len(assignments))
	for _, a := range assignments {
		role := s.mapGroupRoleKey(a.RoleKey)
		if role == "" {
			slog.WarnContext(ctx, "sso: unknown link group role",
				"key", a.RoleKey, "tenant", a.TenantID)
			continue
		}
		target[a.TenantID] = role
	}

	for tenantID, role := range target {
		if err := rbac.EnsureMembershipScoped(ctx, s.pool, userID, tenantID,
			role, rbac.ScopeTenant, ""); err != nil {
			return fmt.Errorf("sso group assignment: %w", err)
		}
	}

	if s.cfg.ReconcileMemberships {
		if err := s.reconcileMemberships(ctx, userID, target); err != nil {
			return err
		}
	}
	if err := s.anchorHomeTenant(ctx, userID, target); err != nil {
		return err
	}
	return nil
}

// anchorHomeTenant keeps the account's home tenant aligned with its
// suite assignment: when app_user.tenant_id is not one of the assigned
// tenants (a JIT account mis-anchored to the fallback tenant before its
// groups existed, or an assignment moved to another tenant), move it to
// the deterministic first target. Platform admins and accounts whose
// home is already inside the assignment set are untouched.
func (s *Service) anchorHomeTenant(ctx context.Context, userID string, target map[string]string) error {
	if len(target) == 0 {
		return nil
	}
	tenants := make([]string, 0, len(target))
	for id := range target {
		tenants = append(tenants, id)
	}
	sort.Strings(tenants)
	desired := tenants[0]

	_, err := s.pool.Exec(ctx, `
		UPDATE app_user
		   SET tenant_id = $1::uuid
		 WHERE id = $2::uuid
		   AND is_platform_admin = FALSE
		   AND (tenant_id IS NULL OR NOT (tenant_id = ANY($3::uuid[])))`,
		desired, userID, tenants)
	if err != nil {
		return fmt.Errorf("sso anchor home tenant: %w", err)
	}
	return nil
}

// reconcileMemberships converges local memberships with the NS group
// set: on a still-assigned tenant it drops a stale suite role after an
// in-console role change (read_only -> engineers), and on tenants the
// group set no longer covers it removes the managed roles and deletes
// the membership once it carries no roles. Membership rows holding
// CUSTOMER_USER are curated local access and are left untouched, as are
// non-managed roles; platform admins are skipped entirely.
func (s *Service) reconcileMemberships(ctx context.Context, userID string, target map[string]string) error {
	var platformAdmin bool
	if err := s.pool.QueryRow(ctx,
		`SELECT is_platform_admin FROM app_user WHERE id = $1::uuid`,
		userID).Scan(&platformAdmin); err != nil {
		return fmt.Errorf("sso reconcile: read user: %w", err)
	}
	if platformAdmin {
		return nil
	}

	managed := make(map[string]bool)
	for _, role := range s.cfg.GroupRole {
		if validRole(role) && role != rbac.RoleCustomerUser {
			managed[role] = true
		}
	}

	memberships, err := rbac.ListUserMemberships(ctx, s.pool, userID)
	if err != nil {
		return fmt.Errorf("sso reconcile: %w", err)
	}
	for _, m := range memberships {
		roles := roleSet(m.Roles)
		// Never touch curated customer access.
		if roles[rbac.RoleCustomerUser] {
			continue
		}

		if desired, keep := target[m.TenantID]; keep {
			// Suite still manages this tenant: drop only the stale managed
			// roles. This covers a role change in the NS console
			// (read_only -> engineers): without it both roles would union
			// and the user would keep the higher access. Local, non-managed
			// roles on the membership are left untouched.
			var stale []string
			for _, r := range m.Roles {
				if managed[r] && r != desired {
					stale = append(stale, r)
				}
			}
			if len(stale) > 0 {
				if _, err := rbac.RemoveRoles(ctx, s.pool, m.MembershipID, stale); err != nil {
					return fmt.Errorf("sso reconcile: %w", err)
				}
			}
			continue
		}

		// Tenant no longer in the group set. Per the conservative removal
		// policy, act only when every role on the membership is
		// suite-managed; a membership carrying a local/non-managed role is
		// left alone.
		if len(m.Roles) == 0 {
			continue
		}
		allManaged := true
		for _, r := range m.Roles {
			if !managed[r] {
				allManaged = false
				break
			}
		}
		if !allManaged {
			continue
		}
		remaining, err := rbac.RemoveRoles(ctx, s.pool, m.MembershipID, m.Roles)
		if err != nil {
			return fmt.Errorf("sso reconcile: %w", err)
		}
		if remaining == 0 {
			if err := rbac.DeleteMembership(ctx, s.pool, m.MembershipID); err != nil {
				return fmt.Errorf("sso reconcile: %w", err)
			}
		}
	}
	return nil
}

// roleSet converts a role slice to a lookup set.
func roleSet(roles []string) map[string]bool {
	out := make(map[string]bool, len(roles))
	for _, r := range roles {
		out[r] = true
	}
	return out
}

// IntegrationSecret returns the shared secret that authenticates the
// NS-stamped integration calls (the same secret used for ticket exchange).
func (s *Service) IntegrationSecret() string {
	return s.cfg.SSOSecret
}

// validRole accepts the assignable built-in human roles. Platform admin and
// service-account roles can never be granted from an external ticket.
func validRole(role string) bool {
	switch role {
	case rbac.RoleTenantAdmin, rbac.RoleEngineer,
		rbac.RoleCustomerUser, rbac.RoleReadOnly:
		return true
	default:
		return false
	}
}

// CallbackPath returns the browser-facing callback path registered in NS
// (the product's sso_callback_path).
func (s *Service) CallbackPath() string {
	return s.cfg.CallbackPath
}

// LoginURL returns the NS portal launch URL that starts the suite SSO
// flow for this product (GET /auth/enter/{product_id}).
func (s *Service) LoginURL() string {
	return strings.TrimRight(s.cfg.NSBaseURL, "/") + "/auth/enter/" +
		url.PathEscape(s.cfg.ClientID)
}

// CallbackURL returns the absolute callback URL for the given origin.
func (s *Service) CallbackURL(baseURL string) string {
	u, err := url.Parse(baseURL)
	if err != nil {
		return s.cfg.CallbackPath
	}
	u.Path = s.cfg.CallbackPath
	u.RawQuery = ""
	return u.String()
}
