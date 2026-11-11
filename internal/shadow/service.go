// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements shadow persistence, optimistic updates, device
// reports and desired-state push.

package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/audit"
)

// SubjectDesiredPrefix is the ingress-facing desired-state subject root.
const SubjectDesiredPrefix = "nl.shadow.desired"

// SubjectEventPrefix is the console-facing shadow change subject root. It is
// deliberately separate from the ingress desired root: console frames carry
// the full post-commit shadow and must never be replayed onto devices.
const SubjectEventPrefix = "nl.shadow.event"

// Service owns the current shadow rows and their change events.
type Service struct {
	pool *pgxpool.Pool
	nc   *nats.Conn

	audit *audit.Service
}

// New constructs a shadow service.
func New(pool *pgxpool.Pool, nc *nats.Conn) *Service {
	return &Service{pool: pool, nc: nc}
}

// SetAuditor wires audit logging for desired/report changes.
func (s *Service) SetAuditor(auditor *audit.Service) {
	s.audit = auditor
}

// Get returns the current shadow. A missing row is reported as an empty
// shadow after confirming the device exists.
func (s *Service) Get(ctx context.Context, deviceID string) (State, error) {
	state, found, err := s.readRow(ctx, deviceID)
	if err != nil {
		return State{}, err
	}
	if found {
		return state, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device WHERE id = $1)`, deviceID).
		Scan(&exists); err != nil {
		return State{}, err
	}
	if !exists {
		return State{}, ErrDeviceNotFound
	}
	state = newState(deviceID)
	return state, nil
}

// UpdateDesired merges a platform patch, increments its version on change
// and pushes the complete desired state.
func (s *Service) UpdateDesired(ctx context.Context, deviceID string, patch DesiredPatch) (State, error) {
	if patch.State == nil {
		return State{}, fmt.Errorf("%w: state is required", ErrInvalidPatch)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID string
	if err := tx.QueryRow(ctx,
		`SELECT tenant_id::text FROM device WHERE id = $1 FOR UPDATE`,
		deviceID).Scan(&tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return State{}, ErrDeviceNotFound
		}
		return State{}, err
	}
	if err := ensureShadowRow(ctx, tx, deviceID); err != nil {
		return State{}, err
	}
	current, err := lockRow(ctx, tx, deviceID)
	if err != nil {
		return State{}, err
	}
	if patch.ExpectedETag != "" && patch.ExpectedETag != current.ETag {
		return State{}, ErrETagMismatch
	}

	merged := mergePatch(current.Desired, patch.State)
	now := time.Now().UTC()
	if !reflect.DeepEqual(current.Desired, merged) {
		current.DesiredVersion++
	}
	current.ETag = etag(current.DesiredVersion)
	current.Desired = merged
	current.Delta = computeDelta(merged, current.Reported)
	current.InSync = len(current.Delta) == 0
	current.DesiredAt = &now
	current.UpdatedAt = now

	if err := writeDesired(ctx, tx, current, now); err != nil {
		return State{}, err
	}
	if err := insertEvent(ctx, tx, tenantID, current.DeviceID, "desired",
		current.DesiredVersion, current.Desired, now); err != nil {
		return State{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return State{}, err
	}
	s.auditChange(ctx, tenantID, current, "desired")
	s.pushDesired(ctx, tenantID, current)
	s.publishEvent(tenantID, current, SourceServer)
	return current, nil
}

// UpdateReported merges device-reported state and records the applied version.
func (s *Service) UpdateReported(ctx context.Context, deviceID string, patch ReportedPatch) (State, error) {
	if patch.State == nil {
		return State{}, fmt.Errorf("%w: state is required", ErrInvalidPatch)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID string
	if err := tx.QueryRow(ctx,
		`SELECT tenant_id::text FROM device WHERE id = $1 FOR UPDATE`,
		deviceID).Scan(&tenantID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return State{}, ErrDeviceNotFound
		}
		return State{}, err
	}
	if err := ensureShadowRow(ctx, tx, deviceID); err != nil {
		return State{}, err
	}
	current, err := lockRow(ctx, tx, deviceID)
	if err != nil {
		return State{}, err
	}

	merged := mergePatch(current.Reported, patch.State)
	now := time.Now().UTC()
	current.Reported = merged
	if patch.Version != nil {
		current.ReportedVersion = *patch.Version
	} else {
		current.ReportedVersion = current.DesiredVersion
	}
	current.ReportedAt = &now
	current.UpdatedAt = now
	current.Delta = computeDelta(current.Desired, merged)
	current.InSync = len(current.Delta) == 0

	if err := writeReported(ctx, tx, current, now); err != nil {
		return State{}, err
	}
	if err := insertEvent(ctx, tx, tenantID, current.DeviceID, "reported",
		current.ReportedVersion, current.Reported, now); err != nil {
		return State{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return State{}, err
	}
	s.auditChange(ctx, tenantID, current, "reported")
	s.publishEvent(tenantID, current, SourceDevice)
	return current, nil
}

// Run pushes desired state when a presence ONLINE event is observed.
func (s *Service) Run(ctx context.Context) error {
	sub, err := s.nc.Subscribe("nl.presence.*.*", func(msg *nats.Msg) {
		var event presenceEvent
		if err := json.Unmarshal(msg.Data, &event); err != nil || event.State != "ONLINE" {
			return
		}
		state, found, err := s.readRow(context.Background(), event.DeviceID)
		if err != nil || !found || state.DesiredVersion == 0 {
			return
		}
		s.pushDesired(context.Background(), event.TenantID, state)
	})
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()
	<-ctx.Done()
	return ctx.Err()
}

func (s *Service) pushDesired(ctx context.Context, tenantID string, state State) {
	if s.nc == nil {
		return
	}
	frame := PushFrame{
		TenantID: tenantID, DeviceID: state.DeviceID,
		Version: state.DesiredVersion, State: state.Desired,
	}
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal desired shadow frame", "error", err)
		return
	}
	subject := fmt.Sprintf("%s.%s.%s", SubjectDesiredPrefix, tenantID, state.DeviceID)
	if err := s.nc.Publish(subject, data); err != nil {
		slog.Error("publish desired shadow frame", "error", err, "subject", subject)
	}
}

// publishEvent fans one console-plane frame carrying the complete post-commit
// shadow to nl.shadow.event.<tenant>.<device>. Hub subscribers replace their
// whole shadow view from it; best-effort, like the ingress desired push.
func (s *Service) publishEvent(tenantID string, state State, source string) {
	if s.nc == nil {
		return
	}
	frame := EventFrame{State: state, TenantID: tenantID, Source: source}
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal shadow event frame", "error", err)
		return
	}
	subject := fmt.Sprintf("%s.%s.%s", SubjectEventPrefix, tenantID, state.DeviceID)
	if err := s.nc.Publish(subject, data); err != nil {
		slog.Error("publish shadow event frame", "error", err, "subject", subject)
	}
}

func (s *Service) auditChange(ctx context.Context, tenantID string, state State, phase string) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Append(ctx, audit.Input{
		TenantID: tenantID, EventType: audit.EventConfigChange,
		Action: "shadow-" + phase, ResourceType: "device_shadow",
		ResourceID: state.DeviceID,
		Details: map[string]any{
			"desired_version":  state.DesiredVersion,
			"reported_version": state.ReportedVersion,
			"in_sync":          state.InSync,
		},
	}); err != nil {
		slog.Error("failed to audit shadow change", "error", err)
	}
}

type presenceEvent struct {
	TenantID string `json:"tenant_id"`
	DeviceID string `json:"device_id"`
	State    string `json:"state"`
}
