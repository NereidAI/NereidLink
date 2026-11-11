// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements signed license installation, policy evaluation,
// tenant suspension and clock-rollback defense.

package license

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	natjs "github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/internal/audit"
)

const (
	// clockKVBucket is the NATS KV bucket for the license witness.
	clockKVBucket = "license"

	// clockKVKey is the rollback witness key.
	clockKVKey = "clock"

	// clockTolerance is the maximum tolerated backward movement.
	clockTolerance = 24 * time.Hour
)

// Config configures the license service.
type Config struct {
	// PublicKey verifies vendor-issued license files.
	PublicKey ed25519.PublicKey

	// GracePeriod controls how long warnings remain actionable.
	GracePeriod time.Duration

	// ClockFile is the local 0600 rollback witness path.
	ClockFile string

	// JSReplicas is the JetStream Raft factor for the clock KV (1/3/5).
	JSReplicas int
}

// State is the evaluated license state for one tenant.
type State struct {
	// ReadOnly blocks new devices and new device connections.
	ReadOnly bool `json:"read_only"`

	// Suspended means the tenant was manually suspended.
	Suspended bool `json:"suspended"`

	// Reason explains a restricted state.
	Reason string `json:"reason,omitempty"`

	// DeviceCap is the effective cap, zero means unlimited.
	DeviceCap int64 `json:"device_cap"`

	// DeviceCount is the current registered device count.
	DeviceCount int64 `json:"device_count"`

	// ExpiresAt is the effective license expiry.
	ExpiresAt time.Time `json:"expires_at"`
}

// Service enforces licenses and tenant lifecycle restrictions.
type Service struct {
	pool *pgxpool.Pool
	nc   *nats.Conn
	js   natjs.JetStream
	key  ed25519.PublicKey

	grace      time.Duration
	clockFile  string
	jsReplicas int

	suspendedMu sync.RWMutex
	suspended   map[string]bool
}

// New constructs the license service.
func New(pool *pgxpool.Pool, nc *nats.Conn, js natjs.JetStream, cfg Config) *Service {
	grace := cfg.GracePeriod
	if grace <= 0 {
		grace = 7 * 24 * time.Hour
	}
	jsReplicas := cfg.JSReplicas
	if jsReplicas < 1 {
		jsReplicas = 1
	}
	return &Service{
		pool: pool, nc: nc, js: js, key: cfg.PublicKey,
		grace: grace, clockFile: cfg.ClockFile, jsReplicas: jsReplicas,
		suspended: map[string]bool{},
	}
}

// Install verifies and atomically replaces the current scope license.
func (s *Service) Install(ctx context.Context, file SignedFile, actor string) (Payload, error) {
	payload, err := Decode(file, s.key)
	if err != nil {
		return Payload{}, err
	}
	if payload.Scope != "*" {
		var exists bool
		err = s.pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM tenant WHERE id::text = $1)`,
			payload.Scope).Scan(&exists)
		if err != nil {
			return Payload{}, err
		}
		if !exists {
			return Payload{}, fmt.Errorf("%w: unknown tenant", ErrInvalidLicense)
		}
	}
	features, err := json.Marshal(payload.Features)
	if err != nil {
		return Payload{}, err
	}
	var tenantID any
	if payload.TenantID != "" {
		tenantID = payload.TenantID
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO tenant_license
		    (scope, tenant_id, customer_name, device_cap, tenant_cap, issued_at,
		     expires_at, features, payload, signature)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10)
		 ON CONFLICT (scope) DO UPDATE SET
		    tenant_id = EXCLUDED.tenant_id,
		    customer_name = EXCLUDED.customer_name,
		    device_cap = EXCLUDED.device_cap,
		    tenant_cap = EXCLUDED.tenant_cap,
		    issued_at = EXCLUDED.issued_at,
		    expires_at = EXCLUDED.expires_at,
		    features = EXCLUDED.features,
		    payload = EXCLUDED.payload,
		    signature = EXCLUDED.signature,
		    installed_at = now()`,
		payload.Scope, tenantID, payload.CustomerName, payload.DeviceCap,
		payload.TenantCap, payload.IssuedAt, payload.ExpiresAt, string(features),
		file.Payload, file.Signature)
	if err != nil {
		return Payload{}, fmt.Errorf("install license: %w", err)
	}
	if _, err := s.appendAudit(ctx, audit.Input{
		ActorName: actor, EventType: audit.EventTenantLifecycle,
		Action: "license-install", ResourceType: "tenant_license",
		ResourceID: payload.Scope,
		Details: map[string]any{
			"device_cap": payload.DeviceCap,
			"expires_at": payload.ExpiresAt,
		},
	}); err != nil {
		slog.Error("failed to audit license install", "error", err)
	}
	return payload, nil
}

// TenantState evaluates a tenant license against current time and count.
func (s *Service) TenantState(ctx context.Context, tenantID string, now time.Time) (State, error) {
	status, err := s.tenantStatus(ctx, tenantID)
	if err != nil {
		return State{}, err
	}
	state := State{Suspended: status == "SUSPENDED" || status == "DEPROVISIONED"}
	if status == "DEPROVISIONED" {
		state.ReadOnly = true
		state.Reason = "tenant is deprovisioned"
		return state, nil
	}
	if state.Suspended {
		state.ReadOnly = true
		state.Reason = "tenant is suspended"
		return state, nil
	}

	license, found, err := s.loadLicense(ctx, tenantID)
	if err != nil {
		return State{}, err
	}
	if !found {
		state.Reason = "license is not installed"
		state.ReadOnly = true
		return state, nil
	}
	state.DeviceCap = license.deviceCap
	state.ExpiresAt = license.expiresAt
	if !now.Before(license.expiresAt) {
		state.ReadOnly = true
		state.Reason = "license has expired"
	}
	if err := s.countTenantDevices(ctx, tenantID, &state); err != nil {
		return State{}, err
	}
	if license.deviceCap > 0 && state.DeviceCount > license.deviceCap && !state.ReadOnly {
		state.ReadOnly = true
		state.Reason = "device cap exceeded"
	}
	if reason, restricted := s.clockRestriction(ctx, now); restricted {
		state.ReadOnly = true
		state.Reason = reason
	}
	return state, nil
}

// PlatformState evaluates the deployment license and tenant count.
func (s *Service) PlatformState(ctx context.Context, now time.Time) (State, error) {
	license, found, err := s.loadPlatformLicense(ctx)
	if err != nil {
		return State{}, err
	}
	state := State{}
	if !found {
		state.ReadOnly = true
		state.Reason = "deployment license is not installed"
		return state, nil
	}
	state.DeviceCap = license.tenantCap
	state.ExpiresAt = license.expiresAt
	if !now.Before(license.expiresAt) {
		state.ReadOnly = true
		state.Reason = "deployment license has expired"
	}
	var count int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM tenant`).Scan(&count); err != nil {
		return State{}, err
	}
	state.DeviceCount = count
	if license.tenantCap > 0 && count > license.tenantCap && !state.ReadOnly {
		state.ReadOnly = true
		state.Reason = "tenant cap exceeded"
	}
	if reason, restricted := s.clockRestriction(ctx, now); restricted {
		state.ReadOnly = true
		state.Reason = reason
	}
	return state, nil
}

// EnsurePlatformCanCreateTenant blocks tenant creation under a restricted
// deployment license.
func (s *Service) EnsurePlatformCanCreateTenant(ctx context.Context) error {
	state, err := s.PlatformState(ctx, time.Now().UTC())
	if err != nil {
		return err
	}
	if state.ReadOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, state.Reason)
	}
	return nil
}

// DeploymentInfo describes the installed deployment license and its current
// gate state for the platform administrator console.
type DeploymentInfo struct {
	// Installed reports whether a deployment-scope license row exists.
	Installed bool `json:"installed"`

	// CustomerName is the contract display name; empty when not installed.
	CustomerName string `json:"customer_name,omitempty"`

	// TenantCap limits tenant count; zero means unlimited.
	TenantCap int64 `json:"tenant_cap"`

	// TenantCount is the current number of tenants.
	TenantCount int64 `json:"tenant_count"`

	// IssuedAt is the vendor issuance time; zero when not installed.
	IssuedAt time.Time `json:"issued_at,omitempty"`

	// ExpiresAt is the effective license expiry; zero when not installed.
	ExpiresAt time.Time `json:"expires_at,omitempty"`

	// Features lists purchased feature identifiers; empty list is emitted as
	// [] rather than omitted (project API contract: arrays are never null).
	Features []string `json:"features"`

	// ReadOnly is the effective gate (missing, expired, over-cap or clock).
	ReadOnly bool `json:"read_only"`

	// Reason explains a restricted state.
	Reason string `json:"reason,omitempty"`
}

// DeploymentInfo returns the installed deployment license together with its
// effective gate state so an administrator UI can show status and upload a
// replacement envelope. The effective state reuses PlatformState so the UI
// cannot diverge from the enforcement decision.
func (s *Service) DeploymentInfo(ctx context.Context) (DeploymentInfo, error) {
	state, err := s.PlatformState(ctx, time.Now().UTC())
	if err != nil {
		return DeploymentInfo{}, err
	}
	info := DeploymentInfo{
		TenantCap:   state.DeviceCap,
		TenantCount: state.DeviceCount,
		ExpiresAt:   state.ExpiresAt,
		ReadOnly:    state.ReadOnly,
		Reason:      state.Reason,
	}
	payload, found, err := s.loadDeploymentPayload(ctx)
	if err != nil {
		return DeploymentInfo{}, err
	}
	if found {
		info.Installed = true
		info.CustomerName = payload.CustomerName
		info.IssuedAt = payload.IssuedAt
		info.Features = payload.Features
	}
	return info, nil
}

// InstallDeployment installs a signed deployment-scope license. It enforces
// scope "*" so the administrator endpoint can never be used to attach an
// arbitrary per-tenant license; tenant licenses stay CLI/ops only.
func (s *Service) InstallDeployment(ctx context.Context, file SignedFile, actor string) (Payload, error) {
	payload, err := Decode(file, s.key)
	if err != nil {
		return Payload{}, err
	}
	if payload.Scope != "*" {
		return Payload{}, fmt.Errorf("%w: administrator upload accepts deployment scope only", ErrInvalidLicense)
	}
	return s.Install(ctx, file, actor)
}

// loadDeploymentPayload returns the stored deployment license payload fields.
// It is a display-only read; enforcement continues to use loadPlatformLicense.
func (s *Service) loadDeploymentPayload(ctx context.Context) (Payload, bool, error) {
	var payload Payload
	var features []byte
	err := s.pool.QueryRow(ctx,
		`SELECT customer_name, tenant_cap, issued_at, expires_at, features
		 FROM tenant_license WHERE scope = '*'`).
		Scan(&payload.CustomerName, &payload.TenantCap,
			&payload.IssuedAt, &payload.ExpiresAt, &features)
	if errors.Is(err, pgx.ErrNoRows) {
		return Payload{}, false, nil
	}
	if err != nil {
		return Payload{}, false, err
	}
	if err := json.Unmarshal(features, &payload.Features); err != nil {
		return Payload{}, false, err
	}
	return payload, true, nil
}

// EnsureTenantCanCreateDevice blocks device creation under read-only license.
func (s *Service) EnsureTenantCanCreateDevice(ctx context.Context, tenantID string) error {
	state, err := s.TenantState(ctx, tenantID, time.Now().UTC())
	if err != nil {
		return err
	}
	if state.Suspended {
		return ErrSuspended
	}
	if state.ReadOnly {
		return fmt.Errorf("%w: %s", ErrReadOnly, state.Reason)
	}
	return nil
}

// AllowNewConnection blocks new device ingress connections when restricted.
// Already connected devices are intentionally allowed by ADR-0024.
func (s *Service) AllowNewConnection(ctx context.Context, tenantID string) error {
	return s.EnsureTenantCanCreateDevice(ctx, tenantID)
}

// AllowTenantWrite reports whether an already accepted message may persist.
// Suspension is an immediate write stop; license read-only does not sever
// existing sessions.
func (s *Service) AllowTenantWrite(ctx context.Context, tenantID string) error {
	if s.isSuspendedCached(tenantID) {
		return ErrSuspended
	}
	status, err := s.tenantStatus(ctx, tenantID)
	if err != nil {
		return err
	}
	if status == "SUSPENDED" || status == "DEPROVISIONED" {
		s.setSuspended(tenantID, true)
		return ErrSuspended
	}
	s.setSuspended(tenantID, false)
	return nil
}

// SuspendTenant immediately marks an ACTIVE tenant suspended.
func (s *Service) SuspendTenant(ctx context.Context, tenantID, reason, actor string) error {
	status, err := s.tenantStatus(ctx, tenantID)
	if err != nil {
		return err
	}
	if status != "ACTIVE" {
		return fmt.Errorf("%w: only ACTIVE tenants can be suspended", ErrInvalidLicense)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE tenant SET status = 'SUSPENDED', status_reason = $2, updated_at = now()
		 WHERE id = $1 AND status = 'ACTIVE'`,
		tenantID, reason); err != nil {
		return err
	}
	s.setSuspended(tenantID, true)
	_, err = s.appendAudit(ctx, audit.Input{
		TenantID: tenantID, ActorName: actor,
		EventType: audit.EventTenantLifecycle, Action: "suspend",
		ResourceType: "tenant", ResourceID: tenantID,
		Details: map[string]any{"reason": reason},
	})
	return err
}

// ResumeTenant restores a SUSPENDED tenant to ACTIVE.
func (s *Service) ResumeTenant(ctx context.Context, tenantID, actor string) error {
	status, err := s.tenantStatus(ctx, tenantID)
	if err != nil {
		return err
	}
	if status != "SUSPENDED" {
		return fmt.Errorf("%w: only SUSPENDED tenants can be resumed", ErrInvalidLicense)
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE tenant SET status = 'ACTIVE', status_reason = '', updated_at = now()
		 WHERE id = $1 AND status = 'SUSPENDED'`, tenantID); err != nil {
		return err
	}
	s.setSuspended(tenantID, false)
	_, err = s.appendAudit(ctx, audit.Input{
		TenantID: tenantID, ActorName: actor,
		EventType: audit.EventTenantLifecycle, Action: "resume",
		ResourceType: "tenant", ResourceID: tenantID,
	})
	return err
}

// Run maintains witnesses, suspended-tenant cache and daily usage.
func (s *Service) Run(ctx context.Context) error {
	minute := time.NewTicker(time.Minute)
	hour := time.NewTicker(time.Hour)
	defer minute.Stop()
	defer hour.Stop()

	s.maintain(ctx)
	if err := s.generateUsage(ctx, time.Now().UTC()); err != nil {
		slog.Error("initial license metering failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-minute.C:
			s.maintain(ctx)
		case <-hour.C:
			if err := s.generateUsage(ctx, time.Now().UTC()); err != nil {
				slog.Error("license metering failed", "error", err)
			}
		}
	}
}

// maintain refreshes witnesses and policy cache; errors are surfaced as
// operational alerts because license checks must not silently degrade.
func (s *Service) maintain(ctx context.Context) {
	now := time.Now().UTC()
	if err := s.observeClock(ctx, now); err != nil {
		slog.Error("license clock witness update failed", "error", err)
	}
	if err := s.refreshSuspended(ctx); err != nil {
		slog.Error("suspended tenant cache refresh failed", "error", err)
	}
	if reason, restricted := s.clockRestriction(ctx, now); restricted {
		slog.Error("license clock rollback restriction active", "reason", reason)
		return
	}
	if rollback, err := s.clockRollback(ctx, now); err != nil {
		slog.Error("failed to evaluate license clock rollback", "error", err)
	} else if rollback > 0 {
		slog.Warn("tolerated license clock rollback", "rollback", rollback)
	}
}

type licenseRow struct {
	deviceCap int64
	tenantCap int64
	expiresAt time.Time
}

func (s *Service) loadLicense(ctx context.Context, tenantID string) (licenseRow, bool, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT device_cap, tenant_cap, expires_at
		 FROM tenant_license
		 WHERE scope = $1
		 UNION ALL
		 SELECT device_cap, tenant_cap, expires_at
		 FROM tenant_license
		 WHERE scope = '*' AND NOT EXISTS (
		     SELECT 1 FROM tenant_license WHERE scope = $1)
		 LIMIT 1`, tenantID)
	var license licenseRow
	if err := row.Scan(&license.deviceCap, &license.tenantCap, &license.expiresAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return licenseRow{}, false, nil
		}
		return licenseRow{}, false, err
	}
	return license, true, nil
}

func (s *Service) loadPlatformLicense(ctx context.Context) (licenseRow, bool, error) {
	var license licenseRow
	err := s.pool.QueryRow(ctx,
		`SELECT device_cap, tenant_cap, expires_at
		 FROM tenant_license WHERE scope = '*'`).
		Scan(&license.deviceCap, &license.tenantCap, &license.expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return licenseRow{}, false, nil
	}
	return license, err == nil, err
}

func (s *Service) tenantStatus(ctx context.Context, tenantID string) (string, error) {
	var status string
	err := s.pool.QueryRow(ctx, `SELECT status FROM tenant WHERE id = $1`, tenantID).
		Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("%w: tenant not found", ErrInvalidLicense)
	}
	return status, err
}

func (s *Service) countTenantDevices(ctx context.Context, tenantID string, state *State) error {
	return s.pool.QueryRow(ctx,
		`SELECT count(*) FROM device WHERE tenant_id = $1`, tenantID).
		Scan(&state.DeviceCount)
}

func (s *Service) appendAudit(ctx context.Context, input audit.Input) (audit.Event, error) {
	return audit.New(s.pool).Append(ctx, input)
}

func (s *Service) isSuspendedCached(tenantID string) bool {
	s.suspendedMu.RLock()
	defer s.suspendedMu.RUnlock()
	return s.suspended[tenantID]
}

func (s *Service) setSuspended(tenantID string, value bool) {
	s.suspendedMu.Lock()
	defer s.suspendedMu.Unlock()
	s.suspended[tenantID] = value
}
