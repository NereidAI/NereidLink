// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// groupstore.go manages rule-chain groups: the stable, tenant-scoped
// identity that profile and device bindings point at. Version rows live
// in rule_chain and are handled by chainstore.go.

package rule

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrGroupNotFound indicates the referenced chain group does not exist.
	ErrGroupNotFound = errors.New("rule chain group not found")

	// ErrGroupNameConflict indicates another group of the tenant uses the name.
	ErrGroupNameConflict = errors.New("rule chain group name already exists")

	// ErrGroupInUse indicates the group is still bound to devices or
	// profiles and therefore cannot be deleted.
	ErrGroupInUse = errors.New("rule chain group is in use")

	// ErrInvalidArgument indicates a malformed chain group request.
	ErrInvalidArgument = errors.New("invalid rule chain argument")
)

// GroupMeta is one chain group with its active version summary.
type GroupMeta struct {
	// ID is the stable group identifier referenced by bindings.
	ID string `json:"id"`

	// Name is the tenant-unique group name.
	Name string `json:"name"`

	// IsDefault marks the tenant fallback group.
	IsDefault bool `json:"is_default"`

	// Enabled suspends execution while keeping bindings and the active
	// version; a disabled group is skipped during chain resolution.
	Enabled bool `json:"enabled"`

	// ActiveVersion is the version number of the active chain, or 0 when
	// the group only holds drafts.
	ActiveVersion int `json:"active_version"`

	// ActiveChainID is the active version row identifier, or empty.
	ActiveChainID string `json:"active_chain_id,omitempty"`

	// LatestVersion is the highest version number of the group.
	LatestVersion int `json:"latest_version"`

	// DeviceCount is the number of devices directly bound to the group.
	DeviceCount int `json:"device_count"`

	// ProfileCount is the number of profiles directly bound to the group.
	ProfileCount int `json:"profile_count"`

	// CreatedAt is the group creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// ListGroups returns every chain group of a tenant with its version
// summary and binding counts, ordered with the default group first.
func (s *ChainStore) ListGroups(ctx context.Context, tenantID string) ([]GroupMeta, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT g.id::text, g.name, g.is_default, g.enabled, g.created_at,
		        COALESCE(MAX(rc.version), 0),
		        COALESCE(MAX(rc.version)
		                 FILTER (WHERE rc.state = 'ACTIVE'), 0),
		        COALESCE(MAX(rc.id::text)
		                 FILTER (WHERE rc.state = 'ACTIVE'), ''),
		        (SELECT count(*) FROM device d
		          WHERE d.rule_chain_group_id = g.id
		            AND d.tenant_id = g.tenant_id),
		        (SELECT count(*) FROM device_profile p
		          WHERE p.rule_chain_group_id = g.id
		            AND p.tenant_id = g.tenant_id)
		 FROM rule_chain_group g
		 LEFT JOIN rule_chain rc ON rc.chain_group_id = g.id
		 WHERE g.tenant_id = $1
		 GROUP BY g.id, g.name, g.is_default, g.enabled, g.created_at
		 ORDER BY g.is_default DESC, g.name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list chain groups: %w", err)
	}
	defer rows.Close()

	groups := []GroupMeta{}
	for rows.Next() {
		var group GroupMeta
		var createdAt time.Time
		if err := rows.Scan(&group.ID, &group.Name, &group.IsDefault,
			&group.Enabled,
			&createdAt,
			&group.LatestVersion, &group.ActiveVersion,
			&group.ActiveChainID, &group.DeviceCount,
			&group.ProfileCount); err != nil {
			return nil, fmt.Errorf("scan chain group: %w", err)
		}
		group.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

// SetGroupEnabled turns execution of one chain group on or off without
// touching its bindings or active version. A disabled group is skipped
// during chain resolution.
func (s *ChainStore) SetGroupEnabled(ctx context.Context, tenantID, groupID string, enabled bool) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE rule_chain_group SET enabled = $3, updated_at = now()
		 WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID, enabled)
	if err != nil {
		return fmt.Errorf("set chain group enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// CreateGroup creates an empty named chain group for a tenant. Draft
// versions are added afterwards by the chain editor.
func (s *ChainStore) CreateGroup(ctx context.Context, tenantID, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: name is required", ErrInvalidArgument)
	}

	groupID := uuid.NewString()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO rule_chain_group (id, tenant_id, name)
		 VALUES ($1, $2, $3)`,
		groupID, tenantID, name); err != nil {
		if isUniqueViolation(err) {
			return "", ErrGroupNameConflict
		}
		return "", fmt.Errorf("create chain group: %w", err)
	}
	return groupID, nil
}

// RenameGroup changes the display name of a chain group. Existing version
// rows keep the name snapshot they were published with.
func (s *ChainStore) RenameGroup(ctx context.Context, tenantID, groupID, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: name is required", ErrInvalidArgument)
	}

	tag, err := s.pool.Exec(ctx,
		`UPDATE rule_chain_group SET name = $3, updated_at = now()
		 WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID, name)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrGroupNameConflict
		}
		return fmt.Errorf("rename chain group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrGroupNotFound
	}
	return nil
}

// EnsureGroupOwned validates that groupID belongs to tenantID.
func (s *ChainStore) EnsureGroupOwned(ctx context.Context, tenantID, groupID string) error {
	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT TRUE FROM rule_chain_group WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID).Scan(&ok); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupNotFound
		}
		return fmt.Errorf("check chain group ownership: %w", err)
	}
	return nil
}

// DeleteGroup removes a chain group that is neither the tenant default nor
// bound to any device or profile. Version rows cascade. The tenant default
// group and referenced groups cannot be deleted; callers must rebind or
// delete the referencing rows first.
func (s *ChainStore) DeleteGroup(ctx context.Context, tenantID, groupID string) error {
	var isDefault bool
	if err := s.pool.QueryRow(ctx,
		`SELECT is_default FROM rule_chain_group
		 WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID).Scan(&isDefault); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrGroupNotFound
		}
		return fmt.Errorf("load chain group: %w", err)
	}
	if isDefault {
		return fmt.Errorf("%w: the tenant default chain group cannot be deleted",
			ErrInvalidArgument)
	}

	var deviceCount, profileCount int
	if err := s.pool.QueryRow(ctx,
		`SELECT (SELECT count(*) FROM device
		          WHERE tenant_id = $2 AND rule_chain_group_id = $1),
		        (SELECT count(*) FROM device_profile
		          WHERE tenant_id = $2 AND rule_chain_group_id = $1)`,
		groupID, tenantID).Scan(&deviceCount, &profileCount); err != nil {
		return fmt.Errorf("count chain group bindings: %w", err)
	}
	if deviceCount+profileCount > 0 {
		return fmt.Errorf("%w: bound to %d device(s) and %d profile(s)",
			ErrGroupInUse, deviceCount, profileCount)
	}

	if _, err := s.pool.Exec(ctx,
		`DELETE FROM rule_chain_group WHERE id = $1 AND tenant_id = $2`,
		groupID, tenantID); err != nil {
		return fmt.Errorf("delete chain group: %w", err)
	}
	return nil
}

// isUniqueViolation reports whether err is a PostgreSQL unique violation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
