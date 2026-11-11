// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package retention manages per-tenant data retention policy and applies
// automatic expiry (ADR-0005).
//
// Policy resolves on three levels: the code defaults below act as the
// global baseline, an optional tenant/domain row overrides the window,
// and a legal-hold flag suspends automatic deletion. Changing a window
// never deletes rows synchronously; a background enforcer computes the
// cutoff and removes expired data. Raw telemetry is deleted only where
// the hourly continuous aggregate already covers the interval, so an
// unrefreshed rollup can never cause detail loss.
package retention

import "errors"

// Domain identifiers for policy scoping.
const (
	// DomainTelemetry covers raw device telemetry detail.
	DomainTelemetry = "telemetry"

	// DomainEvent covers domain/lifecycle events.
	DomainEvent = "event"

	// DomainAlarm covers alarms.
	DomainAlarm = "alarm"

	// DomainAudit covers audit events.
	DomainAudit = "audit"
)

// Defaults is the global baseline retention age in days per domain,
// matching the ADR preset for an installation without object storage:
// the Warm window ends and data is deleted. Zero means automatic
// enforcement keeps the data indefinitely.
var Defaults = map[string]int{
	DomainTelemetry: 365,  // online Hot+Warm window: 12 months
	DomainEvent:     1095, // 3 years
	DomainAlarm:     1825, // 5 years
	DomainAudit:     3650, // 10 years
}

// ErrInvalidDomain means a request names no known domain.
var ErrInvalidDomain = errors.New("invalid retention domain")

// ErrInvalidDays means a retention window is negative.
var ErrInvalidDays = errors.New("retention days must be >= 0")

// Policy is the effective retention view for one tenant/domain.
type Policy struct {
	// TenantID scopes the policy.
	TenantID string `json:"tenant_id"`

	// Domain is one of the Domain* constants.
	Domain string `json:"domain"`

	// RetentionDays is the effective window in days; 0 = keep forever.
	RetentionDays int `json:"retention_days"`

	// LegalHold suspends automatic deletion when true.
	LegalHold bool `json:"legal_hold"`

	// Overridden reports whether the effective window differs from default.
	Overridden bool `json:"overridden"`
}

// Domains returns the supported domains in stable order.
func Domains() []string {
	return []string{
		DomainTelemetry, DomainEvent, DomainAlarm, DomainAudit,
	}
}

// DefaultDays returns the global baseline for domain.
func DefaultDays(domain string) (int, bool) {
	days, ok := Defaults[domain]
	return days, ok
}
