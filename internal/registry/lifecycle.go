// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// lifecycle.go implements the device ownership transitions from
// ADR-0013: time-limited single-use claiming, ownership transfer, and
// the two-stage decommission/reactivate boundaries. External
// collaborators (presence, live transport clients, alarms) are reached
// through narrow interfaces and only after the database transaction
// commits.

package registry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/presence"
	"github.com/nereidai/nereidlink/internal/rbac"
)

const (
	// defaultClaimTTL is used when no token lifetime is supplied.
	defaultClaimTTL = 24 * time.Hour

	// defaultRecallWindow is the default reactivation window.
	defaultRecallWindow = 90 * 24 * time.Hour

	// claimTokenRandomBytes is the entropy of a claim token.
	claimTokenRandomBytes = 32

	// archiveSweepInterval is how often expired recall windows are swept.
	archiveSweepInterval = time.Hour

	// archiveSweepBatch bounds one sweep so a large backlog cannot hold a
	// connection for an unbounded amount of time.
	archiveSweepBatch = 500
)

// ArchiveExpiredRecalls moves devices whose recall window has closed from
// DECOMMISSIONED to ARCHIVED and returns how many were archived.
//
// Without this the recall window is decorative: an expired device stays
// DECOMMISSIONED forever, reactivate refuses it with 409, and nothing ever
// reaches ARCHIVED. This is the second stage of ADR-0013, and it stays
// deliberately cheap -- the row stays in the main table with all of its time
// series intact. Cold storage (archive_catalog, compressed partitions, S3) is
// a separate, still-unimplemented stage; archiving here only closes the
// lifecycle so the state machine is total.
//
// The sweep runs on a timer rather than at decommission time, because the
// transition is driven by the passage of time, not by an event.
func (a *Admin) ArchiveExpiredRecalls(ctx context.Context) (int, error) {
	tag, err := a.pool.Exec(ctx,
		`UPDATE device
		 SET state = $1,
		     archived_at = now(),
		     updated_at = now()
		 WHERE id IN (
		     SELECT id FROM device
		     WHERE state = $2
		       AND recall_until IS NOT NULL
		       AND recall_until < now()
		     ORDER BY recall_until
		     LIMIT $3
		     FOR UPDATE SKIP LOCKED
		 )`,
		StateArchived, StateDecommissioned, archiveSweepBatch)
	if err != nil {
		return 0, mapSQLError(err)
	}
	archived := int(tag.RowsAffected())

	// Write an ownership event per archived device so the lifecycle history
	// stays as complete as the explicitly driven transitions.
	rows, err := a.pool.Query(ctx,
		`SELECT id, tenant_id FROM device
		 WHERE state = $1 AND archived_at > now() - interval '5 minutes'`,
		StateArchived)
	if err != nil {
		return archived, err
	}
	defer rows.Close()
	for rows.Next() {
		var deviceID, tenantID string
		if err := rows.Scan(&deviceID, &tenantID); err != nil {
			return archived, err
		}
		if err := a.insertOwnershipEvent(ctx, a.pool, ownershipEventInput{
			tenantID:  tenantID,
			deviceID:  deviceID,
			eventType: OwnershipArchived,
			details:   map[string]any{"trigger": "recall_expired"},
		}); err != nil {
			return archived, err
		}
	}
	return archived, rows.Err()
}

// PresenceEvictor force-removes a device presence entry. It is
// implemented by *presence.Service.
type PresenceEvictor interface {
	// ForceRemove deletes presence and emits one OFFLINE boundary.
	ForceRemove(ctx context.Context, id presence.Identity, reason string) error
}

// DeviceDisconnector closes a live transport client of a device. It is
// implemented by the MQTT ingress.
type DeviceDisconnector interface {
	// DisconnectDevice closes the live client, if present.
	DisconnectDevice(ctx context.Context, tenantID, deviceID string) error
}

// DeviceAlarmCloser closes live alarms of a device during transfer. It
// is implemented by *alarm.Service.
type DeviceAlarmCloser interface {
	// ClearActiveForDevice clears live alarms and returns the count.
	ClearActiveForDevice(ctx context.Context, tenantID, deviceID string) (int64, error)
}

// SetPresenceEvictor wires the presence eviction collaborator.
func (a *Admin) SetPresenceEvictor(e PresenceEvictor) {
	a.presenceEvictor = e
}

// SetDeviceDisconnector wires the live-client disconnect collaborator.
func (a *Admin) SetDeviceDisconnector(d DeviceDisconnector) {
	a.deviceDisconnector = d
}

// SetAlarmCloser wires the alarm clearing collaborator.
func (a *Admin) SetAlarmCloser(c DeviceAlarmCloser) {
	a.alarmCloser = c
}

// IssueClaimToken creates a single-use, time-limited claim token for an
// UNCLAIMED device. The plaintext is returned once; only its SHA-256
// hash is stored.
func (a *Admin) IssueClaimToken(ctx context.Context, tenantID, deviceID string, ttl time.Duration) (ClaimTokenResult, error) {
	state, err := a.deviceState(ctx, a.pool, tenantID, deviceID)
	if err != nil {
		return ClaimTokenResult{}, err
	}
	if state != StateUnclaimed {
		return ClaimTokenResult{}, fmt.Errorf("%w: only UNCLAIMED devices can issue claim tokens", ErrConflict)
	}

	if ttl <= 0 {
		ttl = defaultClaimTTL
	}
	raw := make([]byte, claimTokenRandomBytes)
	if _, err := rand.Read(raw); err != nil {
		return ClaimTokenResult{}, fmt.Errorf("generate claim token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	tokenHash := hashClaimToken(token)
	expiresAt := time.Now().UTC().Add(ttl)

	if _, err := a.pool.Exec(ctx,
		`INSERT INTO device_claim
		   (id, tenant_id, device_id, token_hash, status, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.NewString(), tenantID, deviceID, tokenHash, ClaimIssued, expiresAt); err != nil {
		return ClaimTokenResult{}, mapSQLError(err)
	}

	return ClaimTokenResult{
		DeviceID:  deviceID,
		Token:     token,
		ExpiresAt: expiresAt.Format(time.RFC3339),
	}, nil
}

// ClaimDevice consumes a claim token and assigns an UNCLAIMED device to
// the chosen customer/group, moving it to ACTIVE. actorID records the
// operator in the ownership history.
func (a *Admin) ClaimDevice(ctx context.Context, tenantID, deviceID string, input ClaimInput, actorID string) (DeviceDTO, error) {
	if input.Token == "" {
		return DeviceDTO{}, invalid("claim token is required")
	}
	if err := a.validateOwnershipTarget(ctx, tenantID, input.CustomerID, input.GroupID); err != nil {
		return DeviceDTO{}, err
	}

	tokenHash := hashClaimToken(input.Token)
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var claimID, status string
	var expiresAt time.Time
	err = tx.QueryRow(ctx,
		`SELECT id, status, expires_at
		 FROM device_claim
		 WHERE token_hash = $1 AND device_id = $2 AND tenant_id = $3
		 FOR UPDATE`,
		tokenHash, deviceID, tenantID).Scan(&claimID, &status, &expiresAt)
	if err != nil {
		return DeviceDTO{}, mapClaimTokenError(err)
	}
	if status != ClaimIssued || time.Now().UTC().After(expiresAt) {
		return DeviceDTO{}, fmt.Errorf("%w: claim token is no longer valid", ErrConflict)
	}

	current, err := a.deviceState(ctx, tx, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	if current != StateUnclaimed {
		return DeviceDTO{}, fmt.Errorf("%w: device is not awaiting claim", ErrConflict)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET state = $1,
		     customer_id = NULLIF($2,'')::uuid,
		     group_id = NULLIF($3,'')::uuid,
		     claimed_at = now(),
		     updated_at = now()
		 WHERE id = $4 AND tenant_id = $5`,
		StateActive, input.CustomerID, input.GroupID, deviceID, tenantID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device_claim
		 SET status = $1, claimed_by = NULLIF($2,'')::uuid, claimed_at = now()
		 WHERE id = $3`,
		ClaimClaimed, actorID, claimID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if err := a.insertOwnershipEvent(ctx, tx, ownershipEventInput{
		tenantID:   tenantID,
		deviceID:   deviceID,
		eventType:  OwnershipClaimed,
		toCustomer: input.CustomerID,
		toGroup:    input.GroupID,
		actorID:    actorID,
	}); err != nil {
		return DeviceDTO{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	return a.loadDevice(ctx, tenantID, deviceID)
}

// TransferDevice moves an ACTIVE device to another customer/group. The
// device identity and time series are preserved; optionally credentials
// are reset (which also ends the live session) and live alarms closed.
func (a *Admin) TransferDevice(ctx context.Context, tenantID, deviceID string, input TransferInput, actorID string) (DeviceDTO, error) {
	if input.AlarmPolicy == "" {
		input.AlarmPolicy = AlarmPolicyRetain
	}
	if input.AlarmPolicy != AlarmPolicyRetain && input.AlarmPolicy != AlarmPolicyClear {
		return DeviceDTO{}, invalid("alarm_policy must be retain or clear")
	}
	if err := a.validateOwnershipTarget(ctx, tenantID, input.CustomerID, input.GroupID); err != nil {
		return DeviceDTO{}, err
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read current ownership under a lock. The scope predicate runs here,
	// before the lock, so a caller cannot even lock a device outside their
	// data scope; the deviceState check below then enforces the ACTIVE gate.
	var fromCustomer, fromGroup string
	scope := rbac.ScopeFromContext(ctx)
	clause, scopeArgs := scope.DevicePredicate("d", 2)
	err = tx.QueryRow(ctx,
		`SELECT COALESCE(d.customer_id::text, ''), COALESCE(d.group_id::text, '')
		 FROM device d
		 WHERE d.id = $1 AND d.tenant_id = $2`+clause+`
		 FOR UPDATE`,
		append([]any{deviceID, tenantID}, scopeArgs...)...).Scan(&fromCustomer, &fromGroup)
	if err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	state, err := a.deviceState(ctx, tx, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	var resetCredentialIDs []string
	if state != StateActive {
		return DeviceDTO{}, fmt.Errorf("%w: only ACTIVE devices can be transferred", ErrConflict)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET customer_id = NULLIF($1,'')::uuid,
		     group_id = NULLIF($2,'')::uuid,
		     updated_at = now()
		 WHERE id = $3 AND tenant_id = $4`,
		input.CustomerID, input.GroupID, deviceID, tenantID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if input.ResetCredentials {
		rows, err := tx.Query(ctx,
			`DELETE FROM device_credential WHERE device_id = $1
			 RETURNING credentials_id`, deviceID)
		if err != nil {
			return DeviceDTO{}, mapSQLError(err)
		}
		for rows.Next() {
			var credentialsID string
			if err := rows.Scan(&credentialsID); err != nil {
				rows.Close()
				return DeviceDTO{}, err
			}
			resetCredentialIDs = append(resetCredentialIDs, credentialsID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return DeviceDTO{}, err
		}
	}

	if err := a.insertOwnershipEvent(ctx, tx, ownershipEventInput{
		tenantID:     tenantID,
		deviceID:     deviceID,
		eventType:    OwnershipTransferred,
		fromCustomer: fromCustomer,
		fromGroup:    fromGroup,
		toCustomer:   input.CustomerID,
		toGroup:      input.GroupID,
		actorID:      actorID,
		details: map[string]any{
			"reset_credentials": input.ResetCredentials,
			"alarm_policy":      input.AlarmPolicy,
		},
	}); err != nil {
		return DeviceDTO{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	// Post-commit side effects are best effort: the lifecycle boundary
	// is already durable, so their failure must not unwind the transfer.
	if input.ResetCredentials {
		for _, credentialsID := range resetCredentialIDs {
			a.notifyCredentialInvalidated(ctx, credentialsID)
		}
		a.evictPresence(ctx, tenantID, deviceID, "transferred")
		if a.deviceDisconnector != nil {
			if err := a.deviceDisconnector.DisconnectDevice(ctx, tenantID, deviceID); err != nil {
				slog.Warn("device disconnect after transfer failed",
					"device", deviceID, "error", err)
			}
		}
	}
	if input.AlarmPolicy == AlarmPolicyClear && a.alarmCloser != nil {
		if _, err := a.alarmCloser.ClearActiveForDevice(ctx, tenantID, deviceID); err != nil {
			slog.Warn("clear alarms during transfer failed",
				"device", deviceID, "error", err)
		}
	}

	return a.loadDevice(ctx, tenantID, deviceID)
}

// DecommissionDevice retires a device: credentials are deleted, the
// live session ends and the device moves into a recall window during
// which it can be reactivated.
func (a *Admin) DecommissionDevice(ctx context.Context, tenantID, deviceID string, recall time.Duration, actorID string) (DeviceDTO, error) {
	if recall <= 0 {
		recall = defaultRecallWindow
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state, err := a.deviceState(ctx, tx, tenantID, deviceID)
	if err != nil {
		return DeviceDTO{}, err
	}
	if state != StateActive && state != StateUnclaimed {
		return DeviceDTO{}, fmt.Errorf("%w: only ACTIVE or UNCLAIMED devices can be decommissioned", ErrConflict)
	}

	now := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET state = $1,
		     decommissioned_at = $2,
		     recall_until = $3,
		     updated_at = $2
		 WHERE id = $4 AND tenant_id = $5`,
		StateDecommissioned, now, now.Add(recall), deviceID, tenantID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	// Credentials are disabled rather than deleted. Keeping the rows means
	// the answer to "which credential did this device use, and was it ever
	// compromised" stays available after the device retires -- and a retired
	// device is precisely the one an operator may need to audit later.
	// LoadCredential already requires status = 'ACTIVE', so a DISABLED
	// credential can no longer open a connection.
	var decommissionedCredentialIDs []string
	rows, err := tx.Query(ctx,
		`UPDATE device_credential
		 SET status = 'DISABLED',
		     updated_at = $2
		 WHERE device_id = $1 AND status <> 'DISABLED'
		 RETURNING credentials_id`,
		deviceID, now)
	if err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	for rows.Next() {
		var credentialsID string
		if err := rows.Scan(&credentialsID); err != nil {
			rows.Close()
			return DeviceDTO{}, err
		}
		decommissionedCredentialIDs = append(decommissionedCredentialIDs, credentialsID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return DeviceDTO{}, err
	}

	if err := a.insertOwnershipEvent(ctx, tx, ownershipEventInput{
		tenantID:  tenantID,
		deviceID:  deviceID,
		eventType: OwnershipDecommissioned,
		actorID:   actorID,
	}); err != nil {
		return DeviceDTO{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	for _, credentialsID := range decommissionedCredentialIDs {
		a.notifyCredentialInvalidated(ctx, credentialsID)
	}
	a.evictPresence(ctx, tenantID, deviceID, presence.ReasonDecommissioned)
	if a.deviceDisconnector != nil {
		if err := a.deviceDisconnector.DisconnectDevice(ctx, tenantID, deviceID); err != nil {
			slog.Warn("device disconnect during decommission failed",
				"device", deviceID, "error", err)
		}
	}

	return a.loadDevice(ctx, tenantID, deviceID)
}

// BulkDecommissionResult reports the outcome of a bulk decommission so the
// API can tell the operator exactly what happened to every selected device
// instead of a single opaque count.
type BulkDecommissionResult struct {
	// Decommissioned counts the devices moved into the recall window.
	Decommissioned int `json:"decommissioned"`
	// Skipped lists devices that could not be decommissioned, with the
	// reason: unknown id, other tenant, or a state that forbids it.
	Skipped []BulkDecommissionSkip `json:"skipped"`
}

// BulkDecommissionSkip is one device the bulk operation refused to touch.
type BulkDecommissionSkip struct {
	// DeviceID is the requested device identifier.
	DeviceID string `json:"device_id"`
	// Reason explains why the device was left untouched.
	Reason string `json:"reason"`
}

// MaxBulkDecommission caps one request so a fat-fingered selection cannot
// hold a long transaction open or evict presence for an unbounded set.
const MaxBulkDecommission = 200

// BulkDecommissionDevice decommissions several devices in one transaction,
// applying exactly the same transition as DecommissionDevice. It is the
// bulk counterpart of the single-device delete: the row is never physically
// removed (ADR-0013/ADR-0017), credentials are revoked, telemetry and
// history are retained, and the device stays reactivatable until its recall
// window closes.
//
// A device that cannot be decommissioned does not abort the batch: it is
// reported in Skipped so one already-decommissioned row cannot make an
// operator retry a selection of fifty devices. Unknown identifiers and
// other tenants' devices are skipped rather than treated as errors, because
// a stale browser tab must not leak whether an id exists elsewhere.
func (a *Admin) BulkDecommissionDevice(ctx context.Context, tenantID string,
	deviceIDs []string, recall time.Duration, actorID string) (BulkDecommissionResult, error) {
	result := BulkDecommissionResult{Skipped: []BulkDecommissionSkip{}}
	if recall <= 0 {
		recall = defaultRecallWindow
	}
	if len(deviceIDs) == 0 {
		return result, invalid("no devices selected")
	}
	if len(deviceIDs) > MaxBulkDecommission {
		return result, invalid(fmt.Sprintf(
			"at most %d devices can be decommissioned at once", MaxBulkDecommission))
	}

	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return result, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := time.Now().UTC()
	decommissioned := make([]string, 0, len(deviceIDs))
	bulkCredentialIDs := make([]string, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		state, err := a.deviceState(ctx, tx, tenantID, deviceID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				result.Skipped = append(result.Skipped, BulkDecommissionSkip{
					DeviceID: deviceID, Reason: "not found in this tenant"})
				continue
			}
			return result, err
		}
		if state != StateActive && state != StateUnclaimed {
			result.Skipped = append(result.Skipped, BulkDecommissionSkip{
				DeviceID: deviceID,
				Reason:   fmt.Sprintf("state %s cannot be decommissioned", state)})
			continue
		}
		if _, err := tx.Exec(ctx,
			`UPDATE device
			 SET state = $1,
			     decommissioned_at = $2,
			     recall_until = $3,
			     updated_at = $2
			 WHERE id = $4 AND tenant_id = $5`,
			StateDecommissioned, now, now.Add(recall), deviceID, tenantID); err != nil {
			return result, mapSQLError(err)
		}
		// Same rule as the single-device path: disable, never delete, so a
		// retired device keeps an auditable credential history.
		credentialRows, err := tx.Query(ctx,
			`UPDATE device_credential
			 SET status = 'DISABLED',
			     updated_at = $2
			 WHERE device_id = $1 AND status <> 'DISABLED'
			 RETURNING credentials_id`,
			deviceID, now)
		if err != nil {
			return result, mapSQLError(err)
		}
		for credentialRows.Next() {
			var credentialsID string
			if err := credentialRows.Scan(&credentialsID); err != nil {
				credentialRows.Close()
				return result, err
			}
			bulkCredentialIDs = append(bulkCredentialIDs, credentialsID)
		}
		credentialRows.Close()
		if err := credentialRows.Err(); err != nil {
			return result, err
		}
		if err := a.insertOwnershipEvent(ctx, tx, ownershipEventInput{
			tenantID:  tenantID,
			deviceID:  deviceID,
			eventType: OwnershipDecommissioned,
			actorID:   actorID,
			details:   map[string]any{"bulk": true},
		}); err != nil {
			return result, err
		}
		decommissioned = append(decommissioned, deviceID)
	}

	if err := tx.Commit(ctx); err != nil {
		return result, mapSQLError(err)
	}
	result.Decommissioned = len(decommissioned)

	// Presence eviction and the live disconnect are best-effort side
	// effects after the commit, exactly as in the single-device path: the
	// devices are already decommissioned, so a failing side effect must not
	// undo or report the transition as failed.
	for _, credentialsID := range bulkCredentialIDs {
		a.notifyCredentialInvalidated(ctx, credentialsID)
	}
	for _, deviceID := range decommissioned {
		a.evictPresence(ctx, tenantID, deviceID, presence.ReasonDecommissioned)
		if a.deviceDisconnector != nil {
			if err := a.deviceDisconnector.DisconnectDevice(ctx, tenantID, deviceID); err != nil {
				slog.Warn("device disconnect during bulk decommission failed",
					"device", deviceID, "error", err)
			}
		}
	}

	if a.auditor != nil {
		if _, err := a.auditor.Append(ctx, audit.Input{
			TenantID:     tenantID,
			ActorID:      actorID,
			EventType:    audit.EventCredential,
			Action:       "bulk-decommission",
			ResourceType: "device",
			Details: map[string]any{
				"requested": len(deviceIDs),
				"count":     result.Decommissioned,
				"skipped":   len(result.Skipped),
			},
		}); err != nil {
			slog.Warn("bulk decommission audit append failed",
				"tenant", tenantID, "error", err)
		}
	}
	return result, nil
}

// ReactivateDevice restores a decommissioned device to ACTIVE while it
// is still inside its recall window.
//
// Credentials were disabled at decommission, not deleted, so this method
// deliberately does not re-enable them: a credential that was exposed while
// the device was out of service must not silently start working again.
// The operator issues a new credential explicitly, which is also what makes
// the reactivation auditable.
func (a *Admin) ReactivateDevice(ctx context.Context, tenantID, deviceID, actorID string) (DeviceDTO, error) {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return DeviceDTO{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Scope-narrowed locked read of state and recall window: reactivating a
	// device outside the caller's data scope is refused as not-found.
	var state string
	var recallUntil *time.Time
	reScope := rbac.ScopeFromContext(ctx)
	reClause, reArgs := reScope.DevicePredicate("d", 2)
	err = tx.QueryRow(ctx,
		`SELECT d.state, d.recall_until
		 FROM device d
		 WHERE d.id = $1 AND d.tenant_id = $2`+reClause+`
		 FOR UPDATE`,
		append([]any{deviceID, tenantID}, reArgs...)...).Scan(&state, &recallUntil)
	if err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	if state != StateDecommissioned {
		return DeviceDTO{}, fmt.Errorf("%w: device is not decommissioned", ErrConflict)
	}
	if recallUntil == nil || time.Now().UTC().After(*recallUntil) {
		return DeviceDTO{}, fmt.Errorf("%w: recall window has expired", ErrConflict)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET state = $1,
		     decommissioned_at = NULL,
		     recall_until = NULL,
		     updated_at = now()
		 WHERE id = $2 AND tenant_id = $3`,
		StateActive, deviceID, tenantID); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}

	if err := a.insertOwnershipEvent(ctx, tx, ownershipEventInput{
		tenantID:  tenantID,
		deviceID:  deviceID,
		eventType: OwnershipReactivated,
		actorID:   actorID,
	}); err != nil {
		return DeviceDTO{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	return a.loadDevice(ctx, tenantID, deviceID)
}

// ListOwnershipEvents returns one page of append-only ownership history
// for a device.
func (a *Admin) ListOwnershipEvents(ctx context.Context, tenantID, deviceID string, page, pageSize int) (apipage.Page[OwnershipEventDTO], error) {
	// Ownership history reveals transfers between customers, so it inherits
	// the device's visibility: an out-of-scope device yields not-found.
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return apipage.Page[OwnershipEventDTO]{}, err
	}
	var total int
	if err := a.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM device_ownership_event
		 WHERE tenant_id = $1 AND device_id = $2`,
		tenantID, deviceID).Scan(&total); err != nil {
		return apipage.Page[OwnershipEventDTO]{}, fmt.Errorf("count ownership events: %w", err)
	}

	rows, err := a.pool.Query(ctx,
		`SELECT id::text, device_id::text, event_type,
		        COALESCE(from_customer_id::text, ''),
		        COALESCE(to_customer_id::text, ''),
		        COALESCE(from_group_id::text, ''),
		        COALESCE(to_group_id::text, ''),
		        COALESCE(actor_id::text, ''),
		        details, created_at
		 FROM device_ownership_event
		 WHERE tenant_id = $1 AND device_id = $2
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		tenantID, deviceID, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[OwnershipEventDTO]{}, fmt.Errorf("list ownership events: %w", err)
	}
	defer rows.Close()

	events := []OwnershipEventDTO{}
	for rows.Next() {
		var event OwnershipEventDTO
		var details []byte
		var createdAt time.Time
		if err := rows.Scan(&event.ID, &event.DeviceID, &event.EventType,
			&event.FromCustomerID, &event.ToCustomerID,
			&event.FromGroupID, &event.ToGroupID,
			&event.ActorID, &details, &createdAt); err != nil {
			return apipage.Page[OwnershipEventDTO]{}, fmt.Errorf("scan ownership event: %w", err)
		}
		if len(details) > 0 {
			if err := json.Unmarshal(details, &event.Details); err != nil {
				return apipage.Page[OwnershipEventDTO]{}, err
			}
		}
		event.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[OwnershipEventDTO]{}, err
	}
	return apipage.New(events, total, page, pageSize), nil
}

// ownershipEventInput carries fields for one history insert.
type ownershipEventInput struct {
	tenantID     string
	deviceID     string
	eventType    string
	fromCustomer string
	toCustomer   string
	fromGroup    string
	toGroup      string
	actorID      string
	details      map[string]any
}

// insertOwnershipEvent writes one append-only ownership history row.
// execRunner is the subset shared by *pgxpool.Pool and pgx.Tx, so ownership
// events can be written inside a caller's transaction or directly against the
// pool when the caller has none.
type execRunner interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func (a *Admin) insertOwnershipEvent(ctx context.Context, tx execRunner, in ownershipEventInput) error {
	details := in.details
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_ownership_event
		   (id, tenant_id, device_id, event_type,
		    from_customer_id, to_customer_id,
		    from_group_id, to_group_id, actor_id, details)
		 VALUES ($1,$2,$3,$4,
		         NULLIF($5,'')::uuid, NULLIF($6,'')::uuid,
		         NULLIF($7,'')::uuid, NULLIF($8,'')::uuid,
		         NULLIF($9,'')::uuid, $10::jsonb)`,
		uuid.NewString(), in.tenantID, in.deviceID, in.eventType,
		in.fromCustomer, in.toCustomer, in.fromGroup, in.toGroup,
		in.actorID, string(raw)); err != nil {
		return mapSQLError(err)
	}
	return nil
}

// validateOwnershipTarget validates an optional customer/group target.
func (a *Admin) validateOwnershipTarget(ctx context.Context, tenantID, customerID, groupID string) error {
	if customerID != "" {
		if _, err := a.GetCustomer(ctx, tenantID, customerID); err != nil {
			return err
		}
	}
	if groupID == "" {
		return nil
	}
	group, err := a.getGroup(ctx, tenantID, groupID)
	if err != nil {
		return err
	}
	if customerID != "" && group.CustomerID != "" && group.CustomerID != customerID {
		return invalid("group belongs to a different customer")
	}
	return nil
}

// deviceState reads the lifecycle state of a tenant-owned device that the
// caller's data scope is allowed to see. It is the shared chokepoint for the
// lifecycle write actions (update, claim, transfer, decommission, quarantine),
// so narrowing here means an out-of-scope device is indistinguishable from a
// nonexistent one: the read returns ErrNotFound and every downstream mutation
// is refused before any row is touched.
func (a *Admin) deviceState(ctx context.Context, runner queryRunner, tenantID, deviceID string) (string, error) {
	var state string
	scope := rbac.ScopeFromContext(ctx)
	clause, args := scope.DevicePredicate("d", 2)
	err := runner.QueryRow(ctx,
		`SELECT d.state FROM device d
		 WHERE d.id = $1 AND d.tenant_id = $2`+clause,
		append([]any{deviceID, tenantID}, args...)...).Scan(&state)
	if err != nil {
		return "", mapSQLError(err)
	}
	return state, nil
}

// lockScopedDeviceState reads the lifecycle state of a device visible under
// the caller's data scope and takes a row lock (FOR UPDATE) inside an open
// transaction. The lock prevents a concurrent lifecycle transition from racing
// the state check, and the scope predicate refuses an out-of-scope device
// before the lock, so a crafted id cannot be used to lock someone else's row.
func (a *Admin) lockScopedDeviceState(ctx context.Context, tx pgx.Tx, tenantID, deviceID string) (string, error) {
	var state string
	scope := rbac.ScopeFromContext(ctx)
	clause, args := scope.DevicePredicate("d", 2)
	err := tx.QueryRow(ctx,
		`SELECT d.state FROM device d
		 WHERE d.id = $1 AND d.tenant_id = $2`+clause+`
		 FOR UPDATE`,
		append([]any{deviceID, tenantID}, args...)...).Scan(&state)
	if err != nil {
		return "", mapSQLError(err)
	}
	return state, nil
}

// loadDevice loads the full device DTO without presence enrichment. The lookup
// is narrowed by the caller's data scope for the same reason as deviceState:
// lifecycle flows that reload the device after a mutation must not hand a full
// DTO back for a device the caller was not allowed to address.
func (a *Admin) loadDevice(ctx context.Context, tenantID, deviceID string) (DeviceDTO, error) {
	var device DeviceDTO
	var claimedAt, decommissionedAt, recallUntil, quarantinedAt *time.Time
	var createdAt, updatedAt time.Time
	var alias, notes string
	var lastActivity *time.Time
	scope := rbac.ScopeFromContext(ctx)
	clause, args := scope.DevicePredicate("d", 2)
	// The last-seen LATERALs call the migration-0093 plpgsql helpers rather
	// than inline max(time) subqueries to force a custom plan per device
	// (generic plans choose the wrong hypertable index; see L-150).
	err := a.pool.QueryRow(ctx,
		`SELECT d.id::text, d.canonical_name,
		        COALESCE(d.profile_id::text, ''),
		        COALESCE(d.rule_chain_group_id::text, ''),
		        COALESCE(d.customer_id::text, ''),
		        COALESCE(d.group_id::text, ''),
		        d.is_gateway, COALESCE(d.gateway_id::text, ''),
		        COALESCE(gw.canonical_name, ''),
		        d.state, d.claimed_at, d.decommissioned_at, d.recall_until,
		        d.quarantined_at, COALESCE(d.quarantine_reason, ''),
		        d.created_at, d.updated_at,
		        COALESCE(d.alias, ''), COALESCE(d.notes, ''),
		        last_activity.at,
		        COALESCE(ci.id::text, ''), COALESCE(ci.connector_type, ''),
		        COALESCE(ci.state, ''),
		        (SELECT count(*)::int FROM connector_instance cg
		          WHERE cg.tenant_id = d.tenant_id AND cg.gateway_id = d.id
		            AND cg.device_id IS NULL)
		 FROM device d
		 LEFT JOIN device gw ON gw.id = d.gateway_id
		 LEFT JOIN connector_instance ci
		   ON ci.tenant_id = d.tenant_id AND ci.device_id = d.id
		 LEFT JOIN LATERAL (
		     SELECT last_presence_time(d.id) AS t
		 ) pres ON true
		 LEFT JOIN LATERAL (
		     SELECT last_telemetry_time(d.id) AS t
		 ) tele ON true
		 LEFT JOIN LATERAL (
		     SELECT GREATEST(pres.t, tele.t) AS at
		 ) last_activity ON true
		 WHERE d.id = $1 AND d.tenant_id = $2`+clause,
		append([]any{deviceID, tenantID}, args...)...).Scan(&device.ID, &device.CanonicalName,
		&device.ProfileID, &device.RuleChainGroupID,
		&device.CustomerID, &device.GroupID,
		&device.IsGateway, &device.GatewayID, &device.GatewayName,
		&device.State,
		&claimedAt, &decommissionedAt, &recallUntil,
		&quarantinedAt, &device.QuarantineReason,
		&createdAt, &updatedAt, &alias, &notes, &lastActivity,
		&device.ConnectorID, &device.ConnectorType, &device.ConnectorState,
		&device.GatewayConnectorCount)
	if err != nil {
		return DeviceDTO{}, mapSQLError(err)
	}
	device.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	device.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	device.Alias = alias
	device.Notes = notes
	if lastActivity != nil {
		device.LastActivityAt = lastActivity.UTC().Format(time.RFC3339)
	}
	formatOptionalTime(claimedAt, &device.ClaimedAt)
	formatOptionalTime(decommissionedAt, &device.DecommissionedAt)
	formatOptionalTime(recallUntil, &device.RecallUntil)
	formatOptionalTime(quarantinedAt, &device.QuarantinedAt)
	return device, nil
}

// evictPresence removes presence and logs failures.
func (a *Admin) evictPresence(ctx context.Context, tenantID, deviceID, reason string) {
	if a.presenceEvictor == nil {
		return
	}
	if err := a.presenceEvictor.ForceRemove(ctx, presence.Identity{
		TenantID: tenantID,
		DeviceID: deviceID,
	}, reason); err != nil {
		slog.Warn("presence eviction failed",
			"device", deviceID, "error", err)
	}
}

// hashClaimToken returns the hex SHA-256 of a claim token.
func hashClaimToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// mapClaimTokenError maps a missing claim row to an invalid-argument
// error rather than exposing not-found semantics to callers.
func mapClaimTokenError(err error) error {
	if mapped := mapSQLError(err); errors.Is(mapped, ErrNotFound) {
		return fmt.Errorf("%w: unknown claim token", ErrInvalidArgument)
	} else {
		return mapped
	}
}

// formatOptionalTime writes an optional timestamp as RFC3339 UTC.
func formatOptionalTime(value *time.Time, target *string) {
	if value != nil {
		*target = value.UTC().Format(time.RFC3339)
	}
}

// Ensure pgxpool is referenced for interface documentation use.
var _ = (*pgxpool.Pool)(nil)
