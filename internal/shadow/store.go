// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go contains the SQL helpers for shadow rows and change events.

package shadow

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

func ensureShadowRow(ctx context.Context, tx pgx.Tx, deviceID string) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO device_shadow (device_id)
		 VALUES ($1) ON CONFLICT (device_id) DO NOTHING`, deviceID)
	return err
}

func lockRow(ctx context.Context, tx pgx.Tx, deviceID string) (State, error) {
	return queryState(ctx, tx.QueryRow(ctx,
		`SELECT device_id::text, desired, reported, desired_version,
		        reported_version, desired_etag, desired_at, reported_at,
		        updated_at
		 FROM device_shadow
		 WHERE device_id = $1
		 FOR UPDATE`, deviceID))
}

func (s *Service) readRow(ctx context.Context, deviceID string) (State, bool, error) {
	state, err := queryState(ctx, s.pool.QueryRow(ctx,
		`SELECT device_id::text, desired, reported, desired_version,
		        reported_version, desired_etag, desired_at, reported_at,
		        updated_at
		 FROM device_shadow
		 WHERE device_id = $1`, deviceID))
	if err == pgx.ErrNoRows {
		return State{}, false, nil
	}
	if err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func queryState(ctx context.Context, row pgx.Row) (State, error) {
	var state State
	var desired, reported map[string]any
	err := row.Scan(&state.DeviceID, &desired, &reported, &state.DesiredVersion,
		&state.ReportedVersion, &state.ETag, &state.DesiredAt, &state.ReportedAt,
		&state.UpdatedAt)
	if err != nil {
		return State{}, err
	}
	if desired == nil {
		desired = map[string]any{}
	}
	if reported == nil {
		reported = map[string]any{}
	}
	state.Desired = desired
	state.Reported = reported
	state.Delta = computeDelta(desired, reported)
	state.InSync = len(state.Delta) == 0
	return state, nil
}

func writeDesired(ctx context.Context, tx pgx.Tx, state State, now time.Time) error {
	desired, err := json.Marshal(state.Desired)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE device_shadow
		 SET desired = $2::jsonb, desired_version = $3, desired_etag = $4,
		     desired_at = $5, updated_at = $6
		 WHERE device_id = $1`,
		state.DeviceID, desired, state.DesiredVersion, state.ETag,
		state.DesiredAt, now)
	return err
}

func writeReported(ctx context.Context, tx pgx.Tx, state State, now time.Time) error {
	reported, err := json.Marshal(state.Reported)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`UPDATE device_shadow
		 SET reported = $2::jsonb, reported_version = $3, reported_at = $4,
		     updated_at = $5
		 WHERE device_id = $1`,
		state.DeviceID, reported, state.ReportedVersion, state.ReportedAt, now)
	return err
}

func insertEvent(ctx context.Context, tx pgx.Tx, tenantID, deviceID, phase string,
	version int64, state map[string]any, at time.Time) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO ts_shadow_event
		    (time, tenant_id, device_id, phase, version, state)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb)`,
		at, tenantID, deviceID, phase, version, data)
	return err
}
