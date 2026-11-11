// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// quarantine.go implements incident-response quarantine entry and
// release. Only ACTIVE devices can be quarantined: the transition is
// durable, credentials can no longer open connections, and the live
// session ends. A platform administrator may release a quarantined
// device directly once the incident is resolved; the ADR-0023
// quarantine.release order remains available for changes requiring
// CLI/web dual control.

package registry

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/audit"
)

// Quarantine reason codes. The vocabulary is closed so that
// "how many devices were quarantined for credential compromise" is a
// queryable question rather than a full-text search over free text, and so
// the ADR-0014 automatic escalation path has codes to choose from when it
// quarantines a device without a human present.
const (
	// ReasonCredentialCompromise marks a suspected leaked or misused
	// credential. The value is a taxonomy code, not a secret.
	//nolint:gosec
	ReasonCredentialCompromise = "credential-compromise"

	// ReasonDeviceStolen marks a device believed to be in hostile hands.
	ReasonDeviceStolen = "device-stolen"

	// ReasonBurstFlood marks a device exceeding its declared rate or
	// burst budget, the ADR-0014 escalation trigger.
	ReasonBurstFlood = "burst-flood"

	// ReasonFirmwareAnomaly marks suspicious device behaviour.
	ReasonFirmwareAnomaly = "firmware-anomaly"

	// ReasonOther is the catch-all and requires a free-text note so the
	// code alone never carries the whole story.
	ReasonOther = "other"
)

// quarantineReasonCodes is the accepted vocabulary.
var quarantineReasonCodes = []string{
	ReasonCredentialCompromise,
	ReasonDeviceStolen,
	ReasonBurstFlood,
	ReasonFirmwareAnomaly,
	ReasonOther,
}

// QuarantineReasonCodes returns a copy of the accepted reason codes so the
// API layer can publish the vocabulary without exposing the mutable slice.
func QuarantineReasonCodes() []string {
	out := make([]string, len(quarantineReasonCodes))
	copy(out, quarantineReasonCodes)
	return out
}

// normalizeQuarantineReason validates a reason code and the accompanying
// note. The code must be known; a note is mandatory for "other" so the
// incident is still described when no structured code fits.
func normalizeQuarantineReason(code, note string) (string, string, error) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	if normalized == "" {
		return "", "", invalid("quarantine reason code is required")
	}
	known := false
	for _, candidate := range quarantineReasonCodes {
		if candidate == normalized {
			known = true
			break
		}
	}
	if !known {
		return "", "", invalidf("unknown quarantine reason %q; supported codes are %s",
			code, strings.Join(quarantineReasonCodes, ", "))
	}
	trimmedNote := strings.TrimSpace(note)
	if normalized == ReasonOther && trimmedNote == "" {
		return "", "", invalid("a note is required when the reason code is other")
	}
	return normalized, trimmedNote, nil
}

// QuarantineDevice moves an ACTIVE device into quarantine. The reason code
// and its note are recorded on the device and in the audit event; actorID
// attributes the acting administrator.
func (a *Admin) QuarantineDevice(ctx context.Context, tenantID, deviceID,
	reasonCode, note, actorID string) (DeviceDTO, error) {
	code, trimmedNote, err := normalizeQuarantineReason(reasonCode, note)
	if err != nil {
		return DeviceDTO{}, err
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read the state under a row lock, narrowed by the caller's data scope,
	// so an out-of-scope device is refused before any mutation.
	state, err := a.lockScopedDeviceState(ctx, tx, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	if state != StateActive {
		return DeviceDTO{}, fmt.Errorf("%w: only ACTIVE devices can be quarantined", ErrConflict)
	}

	now := time.Now().UTC()
	// The stored reason keeps the structured code and the free-text note
	// together so a single column stays readable in psql, while the code
	// prefix remains machine-parseable for reporting.
	stored := code
	if trimmedNote != "" {
		stored = code + ": " + trimmedNote
	}
	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET state = 'QUARANTINED',
		     quarantined_at = $3,
		     quarantine_reason = $4,
		     updated_at = $3
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID, now, stored); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	// Post-commit side effects are best effort: the quarantine
	// boundary is already durable, so their failure must not unwind it.
	a.evictPresence(ctx, tenantID, deviceID, "quarantined")
	if a.deviceDisconnector != nil {
		if err := a.deviceDisconnector.DisconnectDevice(ctx, tenantID, deviceID); err != nil {
			slog.Warn("device disconnect after quarantine failed",
				"device", deviceID, "error", err)
		}
	}
	if a.auditor != nil {
		_, err = a.auditor.Append(ctx, audit.Input{
			TenantID:     tenantID,
			ActorID:      actorID,
			EventType:    audit.EventCredential,
			Action:       "quarantine",
			ResourceType: "device",
			ResourceID:   deviceID,
			Details: map[string]any{
				"reason_code": code,
				"note":        trimmedNote,
			},
		})
		if err != nil {
			slog.Warn("quarantine audit append failed",
				"device", deviceID, "error", err)
		}
	}

	// An incident this severe must be visible in the alarm stream, not only
	// in the audit log: without this the quarantine is silent to anyone
	// watching alarms rather than the device page.
	if a.alarmRaiser != nil {
		if _, err := a.alarmRaiser.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			AlarmType: "DEVICE_QUARANTINED",
			Severity:  "CRITICAL",
			DedupeKey: "device-quarantined",
			Details: map[string]any{
				"reason_code": code,
				"note":        trimmedNote,
			},
		}); err != nil {
			slog.Warn("quarantine alarm raise failed",
				"device", deviceID, "error", err)
		}
	}

	return a.loadDevice(ctx, tenantID, deviceID)
}

// ReleaseFromQuarantine lifts quarantine and returns the device to the
// ACTIVE lifecycle state. note records why the incident is resolved and
// is required so releases stay auditable. The previous quarantine
// columns are cleared because the audit event already retains them.
//
// Credentials are deliberately left untouched: quarantine never revoked them,
// so a released device reconnects with the identity it already had. That is
// what makes quarantine reversible in one click. When the incident was a
// credential compromise the operator revokes the credential explicitly
// instead, so the decision stays visible in the audit trail rather than
// silently bundled into the release.
func (a *Admin) ReleaseFromQuarantine(ctx context.Context, tenantID, deviceID, note, actorID string) (DeviceDTO, error) {
	if strings.TrimSpace(note) == "" {
		return DeviceDTO{}, invalid("release note is required")
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Scope-narrowed locked state read; releasing another customer's device
	// must fail as not-found, same as the read surface.
	state, err := a.lockScopedDeviceState(ctx, tx, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	if state != StateQuarantined {
		return DeviceDTO{}, fmt.Errorf("%w: only QUARANTINED devices can be released", ErrConflict)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET state = 'ACTIVE',
		     quarantined_at = NULL,
		     -- quarantine_reason is NOT NULL DEFAULT '' (migration 0021),
		     -- so it must be reset to the empty marker, not NULL: a NULL here
		     -- fails the whole release with SQLSTATE 23502.
		     quarantine_reason = '',
		     updated_at = now()
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if a.auditor != nil {
		if _, err := a.auditor.Append(ctx, audit.Input{
			TenantID:     tenantID,
			ActorID:      actorID,
			EventType:    audit.EventCredential,
			Action:       "quarantine-release",
			ResourceType: "device",
			ResourceID:   deviceID,
			Details: map[string]any{
				"note": strings.TrimSpace(note),
			},
		}); err != nil {
			slog.Warn("quarantine release audit append failed",
				"device", deviceID, "error", err)
		}
	}

	// Clear the quarantine alarm so the incident does not stay red in the
	// alarm list after the device was released.
	if a.alarmRaiser != nil {
		if _, err := a.alarmRaiser.ClearAlarm(ctx, tenantID, deviceID, "device-quarantined"); err != nil {
			slog.Warn("quarantine alarm clear failed",
				"device", deviceID, "error", err)
		}
	}

	return a.loadDevice(ctx, tenantID, deviceID)
}
