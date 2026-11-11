// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package quota implements per-tenant soft quotas (ADR-0014): the
// registered-device limit, the ingest rate limit and the WebSocket
// subscription limit. Quotas default to the factory values when a tenant
// row has no override; zero means unlimited. Checks used by the registry
// and the WebSocket hub count the current usage and compare it with the
// effective limit. The ingest rate limit is persisted now and enforced by
// the cross-node KV limiter added later.
package quota

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Factory default limits (ADR-0014).
const (
	// DefaultDeviceLimit is 10,000 registered devices.
	DefaultDeviceLimit int64 = 10000

	// DefaultIngestRateLimit is 1,000 messages/second.
	DefaultIngestRateLimit int64 = 1000

	// DefaultWSSubscriptionLimit is 100 subscriptions per tenant.
	DefaultWSSubscriptionLimit int64 = 100
)

// ErrNotFound signals a tenant without a quota override. Callers usually
// prefer Get, which synthesizes defaults instead of this error.
var ErrNotFound = errors.New("tenant quota not found")

// ErrExceeded signals that an operation would exceed a quota.
var ErrExceeded = errors.New("quota exceeded")

// ErrInvalid signals a malformed quota write.
var ErrInvalid = errors.New("invalid tenant quota")

// Quota is the effective quota of one tenant.
type Quota struct {
	// TenantID owns the quota.
	TenantID string `json:"tenant_id"`

	// DeviceLimit bounds registered devices; zero means unlimited.
	DeviceLimit int64 `json:"device_limit"`

	// IngestRateLimit bounds ingest messages/second; zero means unlimited.
	IngestRateLimit int64 `json:"ingest_rate_limit"`

	// WSSubscriptionLimit bounds WS subscriptions; zero means unlimited.
	WSSubscriptionLimit int64 `json:"ws_subscription_limit"`

	// UpdatedAt is the last override time as RFC3339 UTC; empty for defaults.
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Write carries an upsert. Nil fields keep the stored/default value;
// present fields must be non-negative.
type Write struct {
	DeviceLimit         *int64 `json:"device_limit,omitempty"`
	IngestRateLimit     *int64 `json:"ingest_rate_limit,omitempty"`
	WSSubscriptionLimit *int64 `json:"ws_subscription_limit,omitempty"`
}

// Service owns quota persistence.
type Service struct {
	pool *pgxpool.Pool
}

// NewService constructs the quota service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// Get returns the effective quota, synthesizing factory defaults when the
// tenant has no override.
func (s *Service) Get(ctx context.Context, tenantID string) (Quota, error) {
	quota := Quota{TenantID: tenantID}
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT device_limit, ingest_rate_limit, ws_subscription_limit, updated_at
		 FROM tenant_quota WHERE tenant_id = $1`,
		tenantID).Scan(&quota.DeviceLimit, &quota.IngestRateLimit,
		&quota.WSSubscriptionLimit, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Quota{
			TenantID:            tenantID,
			DeviceLimit:         DefaultDeviceLimit,
			IngestRateLimit:     DefaultIngestRateLimit,
			WSSubscriptionLimit: DefaultWSSubscriptionLimit,
		}, nil
	}
	if err != nil {
		return Quota{}, fmt.Errorf("get quota: %w", err)
	}
	quota.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return quota, nil
}

// Upsert creates or updates the quota override. A nil field preserves the
// existing value (or the default on insert).
func (s *Service) Upsert(ctx context.Context, tenantID string, in Write) (Quota, error) {
	if err := validate(in); err != nil {
		return Quota{}, err
	}

	current := Quota{
		DeviceLimit:         DefaultDeviceLimit,
		IngestRateLimit:     DefaultIngestRateLimit,
		WSSubscriptionLimit: DefaultWSSubscriptionLimit,
	}
	if existing, err := s.readOverride(ctx, tenantID); err == nil {
		current = existing
	} else if !errors.Is(err, ErrNotFound) {
		return Quota{}, err
	}

	if in.DeviceLimit != nil {
		current.DeviceLimit = *in.DeviceLimit
	}
	if in.IngestRateLimit != nil {
		current.IngestRateLimit = *in.IngestRateLimit
	}
	if in.WSSubscriptionLimit != nil {
		current.WSSubscriptionLimit = *in.WSSubscriptionLimit
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_quota
		   (tenant_id, device_limit, ingest_rate_limit,
		    ws_subscription_limit, updated_at)
		 VALUES ($1,$2,$3,$4,now())
		 ON CONFLICT (tenant_id) DO UPDATE SET
		   device_limit = EXCLUDED.device_limit,
		   ingest_rate_limit = EXCLUDED.ingest_rate_limit,
		   ws_subscription_limit = EXCLUDED.ws_subscription_limit,
		   updated_at = now()`,
		tenantID, current.DeviceLimit, current.IngestRateLimit,
		current.WSSubscriptionLimit); err != nil {
		return Quota{}, fmt.Errorf("upsert quota: %w", err)
	}
	return s.Get(ctx, tenantID)
}

// EnsureDefault inserts the factory quota for a tenant when no row exists.
// It is called by the tenant provisioning flow.
func (s *Service) EnsureDefault(ctx context.Context, tenantID string) error {
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO tenant_quota (tenant_id) VALUES ($1)
		 ON CONFLICT (tenant_id) DO NOTHING`,
		tenantID); err != nil {
		return fmt.Errorf("ensure default quota: %w", err)
	}
	return nil
}

// CheckDevice verifies one more device fits under the device limit. It
// counts only non-deleted devices of the tenant.
func (s *Service) CheckDevice(ctx context.Context, tenantID string) error {
	quota, err := s.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	if quota.DeviceLimit == 0 {
		return nil
	}
	var count int64
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM device WHERE tenant_id = $1`,
		tenantID).Scan(&count); err != nil {
		return fmt.Errorf("count devices: %w", err)
	}
	if count >= quota.DeviceLimit {
		return fmt.Errorf("%w: device limit %d reached", ErrExceeded, quota.DeviceLimit)
	}
	return nil
}

// CheckSubscription verifies one more WS subscription fits under the
// limit. currentCount is the tenant's active subscription count before
// the new one.
func (s *Service) CheckSubscription(ctx context.Context, tenantID string, currentCount int) error {
	quota, err := s.Get(ctx, tenantID)
	if err != nil {
		return err
	}
	if quota.WSSubscriptionLimit == 0 {
		return nil
	}
	if int64(currentCount) >= quota.WSSubscriptionLimit {
		return fmt.Errorf("%w: ws subscription limit %d reached",
			ErrExceeded, quota.WSSubscriptionLimit)
	}
	return nil
}

// validate rejects negative limits.
func validate(in Write) error {
	for _, value := range []*int64{in.DeviceLimit, in.IngestRateLimit, in.WSSubscriptionLimit} {
		if value != nil && *value < 0 {
			return fmt.Errorf("%w: limits must be non-negative", ErrInvalid)
		}
	}
	return nil
}

// readOverride returns the stored row without default synthesis.
func (s *Service) readOverride(ctx context.Context, tenantID string) (Quota, error) {
	var quota Quota
	var updatedAt time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text, device_limit, ingest_rate_limit,
		        ws_subscription_limit, updated_at
		 FROM tenant_quota WHERE tenant_id = $1`,
		tenantID).Scan(&quota.TenantID, &quota.DeviceLimit,
		&quota.IngestRateLimit, &quota.WSSubscriptionLimit, &updatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Quota{}, ErrNotFound
	}
	if err != nil {
		return Quota{}, fmt.Errorf("read quota override: %w", err)
	}
	quota.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return quota, nil
}
