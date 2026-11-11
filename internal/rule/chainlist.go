// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// chainlist.go provides chain version metadata listing for one chain
// group, used by the rule chain editor.

package rule

import (
	"context"
	"fmt"
	"time"
)

// ChainMeta is one chain version row without its graph payload.
type ChainMeta struct {
	// ID is the chain version identifier.
	ID string `json:"id"`

	// GroupID is the owning chain group.
	GroupID string `json:"group_id"`

	// Name is the name snapshot shared across the group versions.
	Name string `json:"name"`

	// Version is the per-group monotonic version number.
	Version int `json:"version"`

	// State is DRAFT, ACTIVE or ARCHIVED.
	State string `json:"state"`

	// IsRoot is retained for backwards-compatible reads.
	IsRoot bool `json:"is_root"`

	// CreatedAt is the creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// ListChainVersions returns every version of one group, newest first.
func (s *ChainStore) ListChainVersions(ctx context.Context, tenantID, groupID string) ([]ChainMeta, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT rc.id::text, rc.chain_group_id::text, rc.name, rc.version,
		        rc.state, rc.is_root, rc.created_at
		 FROM rule_chain rc
		 WHERE rc.tenant_id = $1 AND rc.chain_group_id = $2
		 ORDER BY rc.version DESC`,
		tenantID, groupID)
	if err != nil {
		return nil, fmt.Errorf("list chain versions: %w", err)
	}
	defer rows.Close()

	chains := []ChainMeta{}
	for rows.Next() {
		var chain ChainMeta
		var createdAt time.Time
		if err := rows.Scan(&chain.ID, &chain.GroupID, &chain.Name,
			&chain.Version, &chain.State, &chain.IsRoot, &createdAt); err != nil {
			return nil, fmt.Errorf("scan chain version: %w", err)
		}
		chain.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		chains = append(chains, chain)
	}
	return chains, rows.Err()
}
