// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// external.go serves just-in-time (JIT) provisioning for external IdPs
// (OIDC, ADR-0021): a user authenticated by an external issuer is created
// locally with no password the first time they log in, and a normal local
// session/token pair is then issued so the rest of the platform authorizes
// exactly like a local account.

package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ExternalUser carries the verified claims the JIT path needs.
type ExternalUser struct {
	// Email is the stable login identifier (normalized lower-case).
	Email string

	// DisplayName is optional; defaults to the email local part.
	DisplayName string

	// EmailVerified records IdP ownership proof.
	EmailVerified bool

	// TenantID attaches the provisioned account; empty for platform admin.
	TenantID string
}

// UpsertExternalUser finds the unique account by email or inserts one with
// an empty password_hash (login is only ever possible through the IdP).
// A disabled account is rejected so an operator lockout is honored.
func (s *Service) UpsertExternalUser(ctx context.Context, in ExternalUser) (User, error) {
	email := normalizeEmail(in.Email)
	if email == "" {
		return User{}, fmt.Errorf("%w: email claim is required", ErrInvalidArgument)
	}
	matches, err := findUsersByEmailGlobal(ctx, s.pool, email)
	if err != nil {
		return User{}, err
	}
	if len(matches) > 1 {
		return User{}, ErrLoginAmbiguous
	}
	if len(matches) == 1 {
		if matches[0].Disabled {
			return User{}, ErrUserDisabled
		}
		return matches[0].User, nil
	}

	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		displayName = strings.SplitN(email, "@", 2)[0]
	}
	id := uuid.NewString()
	var tenantID any
	if in.TenantID != "" {
		tenantID = in.TenantID
	}

	var createdAt time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO app_user
		   (id, tenant_id, email, display_name, password_hash,
		    is_platform_admin, email_verified)
		 VALUES ($1, $2, $3, $4, '', false, $5)
		 RETURNING created_at`,
		id, tenantID, email, displayName, in.EmailVerified).Scan(&createdAt)
	if err != nil {
		// Global email uniqueness is enforced in code; a concurrent
		// provisioning race surfaces as the per-tenant unique index. Either
		// way, re-read the single account.
		var pgErr interface{ SQLState() string }
		if errors.As(err, &pgErr) && pgErr.SQLState() == "23505" {
			matches, rerr := findUsersByEmailGlobal(ctx, s.pool, email)
			if rerr != nil {
				return User{}, rerr
			}
			if len(matches) != 1 {
				return User{}, ErrUserNotFound
			}
			if matches[0].Disabled {
				return User{}, ErrUserDisabled
			}
			return matches[0].User, nil
		}
		return User{}, mapIdentitySQLError(err)
	}

	return User{
		ID:            id,
		TenantID:      in.TenantID,
		Email:         email,
		DisplayName:   displayName,
		EmailVerified: in.EmailVerified,
		CreatedAt:     createdAt.UTC().Format(time.RFC3339),
	}, nil
}

// IssuePairForUser creates a session and token pair for an already
// authenticated user (external IdP login). It is the exported entry the OIDC
// callback uses after JIT provisioning and role mapping.
func (s *Service) IssuePairForUser(ctx context.Context, userID, ip, userAgent string) (TokenPair, error) {
	row, err := findUserByID(ctx, s.pool, userID)
	if err != nil {
		return TokenPair{}, err
	}
	if row.Disabled {
		return TokenPair{}, ErrUserDisabled
	}
	return s.issuePair(ctx, row, ip, userAgent)
}
