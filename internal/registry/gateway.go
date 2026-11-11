// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gateway.go implements gateway-authorization of passive sub-devices
// (ADR-0010). A gateway authenticates with its own credential and then
// announces sub-devices over the gateway topics; every sub-device must be
// bound to the gateway with gateway_id before its presence is created.

package registry

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ensureGateway verifies that a device is an ACTIVE gateway of the tenant.
// It is used when creating or updating a sub-device binding.
func ensureGateway(ctx context.Context, runner queryRunner, tenantID, gatewayID string) error {
	var state string
	var isGateway bool
	err := runner.QueryRow(ctx,
		`SELECT state, is_gateway FROM device
		 WHERE id = $1 AND tenant_id = $2`,
		gatewayID, tenantID).Scan(&state, &isGateway)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: gateway_id does not reference a device of this tenant", ErrInvalidArgument)
	}
	if err != nil {
		return mapSQLError(err)
	}
	if !isGateway || state != StateActive {
		return fmt.Errorf("%w: gateway_id must reference an ACTIVE gateway device", ErrInvalidArgument)
	}
	return nil
}

// AssertGatewaySubdevice verifies that deviceID is a sub-device of gatewayID
// in tenantID. A sub-device is a device row bound via gateway_id whose
// lifecycle state is ACTIVE or UNCLAIMED (a claimed device keeps its
// binding). ok=false means the gateway may not attach the sub-device.
func (a *Admin) AssertGatewaySubdevice(ctx context.Context, tenantID, gatewayID, deviceID string) (bool, error) {
	var boundGateway string
	var state string
	err := a.pool.QueryRow(ctx,
		`SELECT COALESCE(gateway_id::text, ''), state
		 FROM device
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID).Scan(&boundGateway, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if boundGateway != gatewayID {
		return false, nil
	}
	return state == StateActive || state == StateUnclaimed, nil
}

// ListGatewaySubdevices returns the IDs of every attachable sub-device bound
// to gatewayID in tenantID. It is the "desired" set used by manifest
// reconciliation (ADR-0026): a device is desired when its gateway_id points
// at the gateway and its lifecycle state is ACTIVE or UNCLAIMED, the same
// predicate AssertGatewaySubdevice enforces per device. Quarantined,
// decommissioned and archived devices are intentionally excluded so a stale
// binding can never resurrect a device the lifecycle has removed from service.
func (a *Admin) ListGatewaySubdevices(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id
		 FROM device
		 WHERE tenant_id = $1
		   AND gateway_id = $2
		   AND state IN ($3, $4)
		 ORDER BY id`,
		tenantID, gatewayID, StateActive, StateUnclaimed)
	if err != nil {
		return nil, mapSQLError(err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, mapSQLError(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, mapSQLError(err)
	}
	return ids, nil
}
