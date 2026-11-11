// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// integration.go serves the NereidSphere (NS) suite admin console: a small
// read-only tenant picker reached through the NS reverse proxy, which
// proves origin with the shared integration secret.

package sso

import (
	"context"
	"fmt"
)

// TenantInfo is one row of the NS console tenant picker.
type TenantInfo struct {
	// TenantID is the tenant UUID used inside /nereidlink/{tenant}/{role}
	// group paths.
	TenantID string `json:"tenant_id"`
	// TenantName is the human-readable tenant name.
	TenantName string `json:"tenant_name"`
}

// ListTenants returns every ACTIVE tenant for the NS console picker,
// ordered by name. Read-only; the NS gateway performs the authorization
// (suite admin) and stamps the integration secret.
func (s *Service) ListTenants(ctx context.Context) ([]TenantInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, name
		  FROM tenant
		 WHERE status = 'ACTIVE'
		 ORDER BY name, id::text`)
	if err != nil {
		return nil, fmt.Errorf("list tenants: %w", err)
	}
	defer rows.Close()

	out := make([]TenantInfo, 0)
	for rows.Next() {
		var t TenantInfo
		if err := rows.Scan(&t.TenantID, &t.TenantName); err != nil {
			return nil, fmt.Errorf("scan tenant: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
