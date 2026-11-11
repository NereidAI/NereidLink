// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// credentials.go implements the device credential lifecycle from
// ADR-0009. Each device can hold several credentials with an enable
// state and a validity window; rotation uses an overlap window
// (register the new credential while the old still validates) then
// disables the old. A background scanner marks past-due credentials
// EXPIRED and raises escalating alarms at the 30d/7d/1d thresholds.
// A per-tenant epoch backs future emergency bulk invalidation once
// the multi-level cache lands.

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/identity"
)

const (
	// credentialScanInterval is how often the expiry scanner runs.
	credentialScanInterval = time.Hour

	// warnLookahead bounds the expiry scan candidate window.
	warnLookahead = 30 * 24 * time.Hour

	// tokenSecretBytes is the entropy length of a server-generated
	// TOKEN secret before URL-safe base64 encoding.
	tokenSecretBytes = 32
)

// CredentialAlarmRaiser raises credential expiry alarms. It is
// implemented by *alarm.Service.
type CredentialAlarmRaiser interface {
	// CreateAlarm opens or escalates the alarm for a dedupe key.
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	// ClearAlarm resolves an open alarm so a lifecycle transition does not
	// leave the incident permanently raised.
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// CredentialAuditor appends tamper-evident audit events. It is
// implemented by *audit.Service.
type CredentialAuditor interface {
	// Append writes one chained audit event.
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// SetCredentialAlarmRaiser wires the expiry alarm collaborator.
func (a *Admin) SetCredentialAlarmRaiser(r CredentialAlarmRaiser) {
	a.alarmRaiser = r
}

// SetCredentialAuditor wires the audit event collaborator.
func (a *Admin) SetCredentialAuditor(ad CredentialAuditor) {
	a.auditor = ad
}

// parseCredentialWindow parses and cross-validates the optional
// effective/expiry timestamps. Nil timestamps mean "now" and "never".
func parseCredentialWindow(input CredentialDTO) (*time.Time, *time.Time, error) {
	var effectiveAt, expiresAt *time.Time
	if input.EffectiveAt != "" {
		parsed, err := time.Parse(time.RFC3339, input.EffectiveAt)
		if err != nil {
			return nil, nil, invalid("effective_at must be RFC3339")
		}
		effectiveAt = &parsed
	}
	if input.ExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, input.ExpiresAt)
		if err != nil {
			return nil, nil, invalid("expires_at must be RFC3339")
		}
		expiresAt = &parsed
	}
	if effectiveAt != nil && expiresAt != nil && !expiresAt.After(*effectiveAt) {
		return nil, nil, invalid("expires_at must be after effective_at")
	}
	return effectiveAt, expiresAt, nil
}

// SetCredentialStatus enables or disables one credential. Disabling a
// credential ends the live session so a device must re-authenticate;
// with another valid credential it can reconnect immediately.
func (a *Admin) SetCredentialStatus(ctx context.Context, tenantID, deviceID, credentialID, status, actorID, ip string) (CredentialDTO, error) {
	if status != CredentialActive && status != CredentialDisabled {
		return CredentialDTO{}, invalid("status must be ACTIVE or DISABLED")
	}
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return CredentialDTO{}, err
	}

	var dto CredentialDTO
	var effectiveAt, expiresAt time.Time
	var effectiveNull, expiresNull bool
	err := a.pool.QueryRow(ctx,
		`UPDATE device_credential
		 SET status = $1, updated_at = now()
		 WHERE id = $2 AND device_id = $3
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $4)
		 RETURNING id::text, type, credentials_id, status,
		           COALESCE(effective_at, to_timestamp(0)),
		           effective_at IS NULL,
		           COALESCE(expires_at, to_timestamp(0)),
		           expires_at IS NULL,
		           warn_level`,
		status, credentialID, deviceID, tenantID).Scan(
		&dto.ID, &dto.Type, &dto.CredentialsID, &dto.Status,
		&effectiveAt, &effectiveNull, &expiresAt, &expiresNull, &dto.WarnLevel)
	if err != nil {
		return CredentialDTO{}, mapSQLError(err)
	}
	if !effectiveNull {
		dto.EffectiveAt = effectiveAt.UTC().Format(time.RFC3339)
	}
	if !expiresNull {
		dto.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}

	action := "enable"
	if status == CredentialDisabled {
		action = "disable"
	}
	a.recordCredentialAudit(ctx, tenantID, deviceID, action, credentialID, ip, actorID, nil)

	if status == CredentialDisabled {
		a.notifyCredentialInvalidated(ctx, dto.CredentialsID)
		a.evictPresence(ctx, tenantID, deviceID, "credential disabled")
		a.disconnectDevice(ctx, tenantID, deviceID)
	}
	return dto, nil
}

// RotateCredential replaces a credential secret and optionally its
// expiry in place, then ends the live session so the device reconnects
// with the new secret. The warning bookkeeping resets so future
// threshold alarms fire against the new expiry.
func (a *Admin) RotateCredential(ctx context.Context, tenantID, deviceID, credentialID, newSecret, newExpiresAt, actorID, ip string) (CredentialDTO, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return CredentialDTO{}, err
	}

	// A PASSWORD rotation must carry a replacement value; other types
	// may rotate only their expiry (secret rotation stays with PKI).
	var credType string
	if err := a.pool.QueryRow(ctx,
		`SELECT type FROM device_credential
		 WHERE id = $1 AND device_id = $2
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $3)`,
		credentialID, deviceID, tenantID).Scan(&credType); err != nil {
		return CredentialDTO{}, mapSQLError(err)
	}
	if credType == "PASSWORD" && newSecret == "" {
		return CredentialDTO{}, invalid("password value is required for rotation")
	}
	if credType == "PASSWORD" && !strings.HasPrefix(newSecret, "$argon2id$") {
		// Rotation must never persist a plaintext replacement.
		hashed, err := identity.HashSecret(newSecret)
		if err != nil {
			return CredentialDTO{}, fmt.Errorf("hash password: %w", err)
		}
		newSecret = hashed
	}

	var expiresAt *time.Time
	if newExpiresAt != "" {
		parsed, err := time.Parse(time.RFC3339, newExpiresAt)
		if err != nil {
			return CredentialDTO{}, invalid("expires_at must be RFC3339")
		}
		expiresAt = &parsed
	}

	var dto CredentialDTO
	var effectiveAt, expiresAtScan time.Time
	var effectiveNull, expiresNull bool
	err := a.pool.QueryRow(ctx,
		`UPDATE device_credential
		 SET credentials_value = $1,
		     expires_at = $2,
		     warn_level = '',
		     updated_at = now()
		 WHERE id = $3 AND device_id = $4
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $5)
		 RETURNING id::text, type, credentials_id, status,
		           COALESCE(effective_at, to_timestamp(0)),
		           effective_at IS NULL,
		           COALESCE(expires_at, to_timestamp(0)),
		           expires_at IS NULL,
		           warn_level`,
		newSecret, expiresAt, credentialID, deviceID, tenantID).Scan(
		&dto.ID, &dto.Type, &dto.CredentialsID, &dto.Status,
		&effectiveAt, &effectiveNull, &expiresAtScan, &expiresNull, &dto.WarnLevel)
	if err != nil {
		return CredentialDTO{}, mapSQLError(err)
	}
	if !effectiveNull {
		dto.EffectiveAt = effectiveAt.UTC().Format(time.RFC3339)
	}
	if !expiresNull {
		dto.ExpiresAt = expiresAtScan.UTC().Format(time.RFC3339)
	}

	a.recordCredentialAudit(ctx, tenantID, deviceID, "rotate", credentialID, ip, actorID, nil)
	a.notifyCredentialInvalidated(ctx, dto.CredentialsID)
	a.evictPresence(ctx, tenantID, deviceID, "credential rotated")
	a.disconnectDevice(ctx, tenantID, deviceID)
	return dto, nil
}

// RevokeAllCredentials disables every credential of one device, used
// for emergency revocation. The live session ends immediately.
func (a *Admin) RevokeAllCredentials(ctx context.Context, tenantID, deviceID, actorID, ip string) (int64, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return 0, err
	}
	rows, err := a.pool.Query(ctx,
		`UPDATE device_credential
		 SET status = $1, updated_at = now()
		 WHERE device_id = $2 AND status <> $1
		 RETURNING credentials_id`,
		CredentialDisabled, deviceID)
	if err != nil {
		return 0, mapSQLError(err)
	}
	var credentialsIDs []string
	for rows.Next() {
		var credentialsID string
		if err := rows.Scan(&credentialsID); err != nil {
			rows.Close()
			return 0, err
		}
		credentialsIDs = append(credentialsIDs, credentialsID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	a.recordCredentialAudit(ctx, tenantID, deviceID, "revoke-all", deviceID, ip, actorID, nil)
	if len(credentialsIDs) > 0 {
		for _, credentialsID := range credentialsIDs {
			a.notifyCredentialInvalidated(ctx, credentialsID)
		}
		a.evictPresence(ctx, tenantID, deviceID, "credentials revoked")
		a.disconnectDevice(ctx, tenantID, deviceID)
	}
	return int64(len(credentialsIDs)), nil
}

// RevealCredential returns stored credential material for inspection
// and download by a tenant administrator (ADR-0009). TOKEN secrets,
// JWT verification key material and X509 certificate PEM are returned
// in CredentialsValue; X509 also returns the decrypted private key in
// PrivateKeyPEM. PASSWORD rows held as argon2id PHC are one-way hashes:
// the response sets revealable=false; rotation is the only way to set a
// known replacement. Every reveal is recorded as a CREDENTIAL_MANAGE
// audit event.
func (a *Admin) RevealCredential(ctx context.Context, tenantID, deviceID, credentialID, actorID, ip string) (CredentialDTO, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return CredentialDTO{}, err
	}

	var dto CredentialDTO
	var extraJSON []byte
	err := a.pool.QueryRow(ctx,
		`SELECT id::text, type, credentials_id, credentials_value, status, extra
		 FROM device_credential
		 WHERE id = $1 AND device_id = $2
		   AND device_id IN (SELECT id FROM device WHERE tenant_id = $3)`,
		credentialID, deviceID, tenantID).Scan(
		&dto.ID, &dto.Type, &dto.CredentialsID, &dto.CredentialsValue,
		&dto.Status, &extraJSON)
	if err != nil {
		return CredentialDTO{}, mapSQLError(err)
	}

	revealable := true
	if dto.Type == "PASSWORD" && strings.HasPrefix(dto.CredentialsValue, "$argon2id$") {
		// Argon2id is a one-way function; the plaintext password is
		// neither stored nor recoverable, so the API must not pretend.
		dto.CredentialsValue = ""
		revealable = false
	}
	if dto.Type == "X509" {
		// Return the certificate explicitly and unwrap the stored
		// private key so both stay downloadable after creation.
		dto.CertificatePEM = dto.CredentialsValue
		var extra map[string]any
		_ = json.Unmarshal(extraJSON, &extra)
		if blob, ok := extra["encrypted_private_key"].(string); ok {
			if a.certIssuer == nil {
				return CredentialDTO{}, invalid("private key decryption is not configured")
			}
			keyPEM, decryptErr := a.certIssuer.DecryptPrivateKey(blob)
			if decryptErr != nil {
				return CredentialDTO{}, fmt.Errorf("decrypt stored private key: %w", decryptErr)
			}
			dto.PrivateKeyPEM = string(keyPEM)
		}
	}
	dto.Revealable = &revealable
	a.recordCredentialAudit(ctx, tenantID, deviceID, "reveal", credentialID, ip, actorID,
		map[string]any{"type": dto.Type, "revealable": revealable})
	return dto, nil
}

// BumpCredentialEpoch raises the per-tenant credential epoch. Once the
// L1/L2 credential cache exists, this forces every cached decision for
// the tenant to be re-evaluated, implementing ADR-0009 emergency bulk
// invalidation. It is recorded in the tenant audit log.
func (a *Admin) BumpCredentialEpoch(ctx context.Context, tenantID, actorID, ip string) (int64, error) {
	var epoch int64
	err := a.pool.QueryRow(ctx,
		`UPDATE tenant
		 SET credential_epoch = credential_epoch + 1
		 WHERE id = $1
		 RETURNING credential_epoch`,
		tenantID).Scan(&epoch)
	if err != nil {
		return 0, mapSQLError(err)
	}
	a.recordCredentialAudit(ctx, tenantID, "", "bump-epoch", tenantID, ip, actorID,
		map[string]any{"credential_epoch": epoch})
	a.notifyEpochInvalidated(ctx, tenantID)
	return epoch, nil
}

// notifyCredentialInvalidated publishes a single-credential cache eviction.
// A failed publish leaves the TTL as the backstop; the event is best effort
// so a messaging issue cannot block the credential lifecycle.
func (a *Admin) notifyCredentialInvalidated(ctx context.Context, credentialsID string) {
	if a.credentialCacheInvalidator == nil || credentialsID == "" {
		return
	}
	if err := a.credentialCacheInvalidator.CredentialInvalidated(ctx, credentialsID); err != nil {
		slog.Warn("credential invalidation event failed",
			"credentials_id", credentialsID, "error", err)
	}
}

// notifyEpochInvalidated publishes a tenant-wide cache fence.
func (a *Admin) notifyEpochInvalidated(ctx context.Context, tenantID string) {
	if a.credentialCacheInvalidator == nil || tenantID == "" {
		return
	}
	if err := a.credentialCacheInvalidator.EpochInvalidated(ctx, tenantID); err != nil {
		slog.Warn("epoch invalidation event failed",
			"tenant_id", tenantID, "error", err)
	}
}

// BackfillCertFingerprints computes and stores the SHA-256 leaf
// fingerprint for X509 credentials created before migration 0034. It is
// run once at startup. Rows that already have a fingerprint are skipped,
// so the function is idempotent. A malformed stored certificate is
// logged and left for manual repair rather than failing startup.
func (a *Admin) BackfillCertFingerprints(ctx context.Context) (int, error) {
	rows, err := a.pool.Query(ctx,
		`SELECT id::text, credentials_value
		 FROM device_credential
		 WHERE type = 'X509' AND cert_fingerprint IS NULL`)
	if err != nil {
		return 0, fmt.Errorf("select fingerprints to backfill: %w", err)
	}
	type pending struct {
		id    string
		value string
	}
	var candidates []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.value); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	filled := 0
	for _, c := range candidates {
		cert, parseErr := parseCredentialCertificate(c.value)
		if parseErr != nil {
			slog.Warn("skip fingerprint backfill for malformed certificate",
				"credential", c.id, "error", parseErr)
			continue
		}
		fp := certFingerprintSHA256(cert.Raw)
		if _, err := a.pool.Exec(ctx,
			`UPDATE device_credential
			 SET cert_fingerprint = $2, updated_at = now()
			 WHERE id = $1 AND cert_fingerprint IS NULL`,
			c.id, fp); err != nil {
			return filled, fmt.Errorf("backfill fingerprint %s: %w", c.id, mapSQLError(err))
		}
		filled++
	}
	return filled, nil
}

// RunCredentialExpiry periodically marks expired credentials and
// raises threshold alarms until the context is cancelled.
func (a *Admin) RunCredentialExpiry(ctx context.Context) error {
	return runPeriodic(ctx, credentialScanInterval, "credential expiry scan",
		func(now time.Time) error { return a.ScanCredentialExpiry(ctx, now.UTC()) })
}

// RunLifecycleSweep archives devices whose recall window has closed. It runs
// on its own ticker because the ARCHIVED transition is driven by elapsed time
// rather than by any request, so nothing else in the system would ever trigger
// it.
func (a *Admin) RunLifecycleSweep(ctx context.Context) error {
	return runPeriodic(ctx, archiveSweepInterval, "device archive sweep",
		func(time.Time) error {
			archived, err := a.ArchiveExpiredRecalls(ctx)
			if err != nil {
				return err
			}
			if archived > 0 {
				slog.Info("archived devices with expired recall windows",
					"count", archived)
			}
			return nil
		})
}

// runPeriodic runs work once immediately and then on every tick until the
// context is cancelled. Running immediately matters: on a restart the backlog
// must not wait a full interval before the state machine becomes consistent.
func runPeriodic(ctx context.Context, interval time.Duration, label string,
	work func(time.Time) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if err := work(time.Now().UTC()); err != nil {
		slog.Error(label+" failed", "error", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if err := work(now.UTC()); err != nil {
				slog.Error(label+" failed", "error", err)
			}
		}
	}
}

// expiryCandidate is one credential nearing or past its expiry.
type expiryCandidate struct {
	tenantID      string
	credentialID  string
	credentialsID string
	deviceID      string
	expiresAt     time.Time
	warnLevel     string
}

// ScanCredentialExpiry transitions ACTIVE credentials past their
// expiry to EXPIRED and raises an escalating alarm the first time each
// 30d/7d/1d threshold is crossed. It is idempotent: warn_level tracks
// announced thresholds and the expiry UPDATE only matches ACTIVE rows.
func (a *Admin) ScanCredentialExpiry(ctx context.Context, now time.Time) error {
	rows, err := a.pool.Query(ctx,
		`SELECT d.tenant_id::text, c.id::text, c.credentials_id, d.id::text, c.expires_at, c.warn_level
		 FROM device_credential c
		 JOIN device d ON d.id = c.device_id
		 WHERE c.status = 'ACTIVE'
		   AND c.expires_at IS NOT NULL
		   AND c.expires_at <= $1`,
		now.Add(warnLookahead))
	if err != nil {
		return fmt.Errorf("load expiry candidates: %w", err)
	}

	candidates := []expiryCandidate{}
	for rows.Next() {
		var candidate expiryCandidate
		if err := rows.Scan(&candidate.tenantID, &candidate.credentialID,
			&candidate.credentialsID, &candidate.deviceID, &candidate.expiresAt,
			&candidate.warnLevel); err != nil {
			rows.Close()
			return fmt.Errorf("scan expiry candidate: %w", err)
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()

	for _, candidate := range candidates {
		expired := !candidate.expiresAt.After(now)
		targetLevel := warnLevelFor(candidate.expiresAt.Sub(now), expired)
		if !expired && warnRank(targetLevel) <= warnRank(candidate.warnLevel) {
			continue
		}
		newStatus := CredentialActive
		if expired {
			newStatus = CredentialExpired
		}
		if _, err := a.pool.Exec(ctx,
			`UPDATE device_credential
			 SET status = $1,
			     warn_level = CASE WHEN $2 = '' THEN warn_level ELSE $2 END,
			     updated_at = now()
			 WHERE id = $3`,
			newStatus, targetLevel, candidate.credentialID); err != nil {
			return fmt.Errorf("update credential expiry state: %w", err)
		}
		if expired {
			a.notifyCredentialInvalidated(ctx, candidate.credentialsID)
		}
		a.raiseExpiryAlarm(ctx, candidate, targetLevel, expired)
	}
	return nil
}

// warnLevelFor maps the remaining lifetime to the strongest threshold
// crossed; expired yields the terminal level.
func warnLevelFor(remaining time.Duration, expired bool) string {
	switch {
	case expired:
		return "expired"
	case remaining <= 24*time.Hour:
		return Warn1d
	case remaining <= 7*24*time.Hour:
		return Warn7d
	default:
		return Warn30d
	}
}

// warnRank orders warning levels so thresholds only escalate.
func warnRank(level string) int {
	switch level {
	case Warn30d:
		return 1
	case Warn7d:
		return 2
	case Warn1d:
		return 3
	case "expired":
		return 4
	default:
		return 0
	}
}

// raiseExpiryAlarm opens or escalates the stable per-credential expiry
// alarm. A fixed dedupe key keeps the three thresholds in one alarm
// that escalates toward expiry.
func (a *Admin) raiseExpiryAlarm(ctx context.Context, candidate expiryCandidate, level string, expired bool) {
	if a.alarmRaiser == nil {
		return
	}
	severity := severityForWarn(level)
	alarmType := "Credential expiring"
	if expired {
		alarmType = "Credential expired"
	}
	if _, err := a.alarmRaiser.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  candidate.tenantID,
		DeviceID:  candidate.deviceID,
		AlarmType: alarmType,
		Severity:  severity,
		DedupeKey: "credential-expiry:" + candidate.credentialID,
		Propagate: true,
		Details: map[string]any{
			"level":      level,
			"expires_at": candidate.expiresAt.UTC().Format(time.RFC3339),
		},
	}); err != nil {
		slog.Warn("raise credential expiry alarm failed",
			"credential", candidate.credentialID, "error", err)
	}
}

// severityForWarn maps a warning level to alarm severity.
func severityForWarn(level string) string {
	switch level {
	case "expired":
		return "CRITICAL"
	case Warn1d:
		return "MAJOR"
	case Warn7d:
		return "MINOR"
	default:
		return "WARNING"
	}
}

// disconnectDevice closes the live transport client, logging failures.
func (a *Admin) disconnectDevice(ctx context.Context, tenantID, deviceID string) {
	if a.deviceDisconnector == nil {
		return
	}
	if err := a.deviceDisconnector.DisconnectDevice(ctx, tenantID, deviceID); err != nil {
		slog.Warn("device disconnect failed", "device", deviceID, "error", err)
	}
}

// recordCredentialAudit appends a best-effort tamper-evident event. A
// failed audit write must not block the lifecycle boundary but is
// surfaced as an error log, consistent with the identity service.
func (a *Admin) recordCredentialAudit(ctx context.Context, tenantID, deviceID, action, resourceID, ip, actorID string, details map[string]any) {
	if a.auditor == nil {
		return
	}
	if details == nil {
		details = map[string]any{}
	}
	if _, err := a.auditor.Append(ctx, audit.Input{
		TenantID:     tenantID,
		ActorID:      actorID,
		EventType:    audit.EventCredential,
		Action:       action,
		ResourceType: "device_credential",
		ResourceID:   resourceID,
		Details:      details,
		IP:           ip,
	}); err != nil {
		slog.Error("record credential audit event failed",
			"device", deviceID, "action", action, "error", err)
	}
}
