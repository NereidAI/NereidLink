// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists connector desired specs and reported status in
// connector_instance (migration 0040). All access is multi-tenant: every
// read and write is scoped by tenant_id so a status report or REST call can
// never touch another tenant's spec.
package connector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound indicates the spec does not exist in the caller's tenant.
var ErrNotFound = errors.New("connector spec not found")

// ErrInvalid indicates an invalid desired input.
var ErrInvalid = errors.New("connector spec invalid")

// ErrGatewayNotFound indicates the target is not a gateway in the tenant.
var ErrGatewayNotFound = errors.New("gateway not found")

// ErrConflict indicates a unique constraint clash, e.g. a second connector
// instance bound to the same sub-device.
var ErrConflict = errors.New("connector spec conflict")

// Store is the PostgreSQL-backed connector instance store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store.
func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// normalizeRaw returns a non-nill JSON document, defaulting empty input to
// the supplied fallback so jsonb columns never receive SQL NULL (API arrays
// and objects are emitted as [], {} rather than null).
func normalizeRaw(raw json.RawMessage, fallback string) []byte {
	if len(raw) == 0 {
		return []byte(fallback)
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return []byte(fallback)
	}
	return []byte(raw)
}

// GatewayExists reports that a device owned by the tenant exists, guarding a
// connector create against a foreign-key violation from a wrong tenant or an
// unknown gateway.
func (s *PGStore) GatewayExists(ctx context.Context, tenantID, gatewayID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device
		               WHERE id = $1 AND tenant_id = $2 AND is_gateway)`,
		gatewayID, tenantID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check connector gateway: %w", err)
	}
	return exists, nil
}

// DeviceBelongsToGateway reports that deviceID is an ACTIVE/UNCLAIMED
// sub-device bound to gatewayID in the tenant. It guards a connector create
// against binding an instance to another gateway's (or nonexistent) device.
func (s *PGStore) DeviceBelongsToGateway(ctx context.Context, tenantID, gatewayID, deviceID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device
		               WHERE id = $1 AND tenant_id = $2
		                 AND gateway_id = $3 AND NOT is_gateway
		                 AND state IN ('ACTIVE','UNCLAIMED'))`,
		deviceID, tenantID, gatewayID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check connector device binding: %w", err)
	}
	return exists, nil
}

// FetchTemplates returns the connector template keyed by profile id for the
// given tenant. Profiles that exist but carry no connector_type are still
// returned (ConnectorType empty) so callers can distinguish "unknown
// profile" (absent key) from "profile without a template".
func (s *PGStore) FetchTemplates(ctx context.Context, tenantID string, profileIDs []string) (map[string]Template, error) {
	out := map[string]Template{}
	if len(profileIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, connector_type, connector_type_version,
		        connector_config_template, connector_mappings_template, protocol
		 FROM device_profile
		 WHERE tenant_id = $1 AND id = ANY($2::uuid[])`,
		tenantID, profileIDs)
	if err != nil {
		return nil, fmt.Errorf("fetch connector templates: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var tpl Template
		if err := rows.Scan(&tpl.ProfileID, &tpl.ConnectorType, &tpl.TypeVersion,
			&tpl.Config, &tpl.Mappings, &tpl.Protocol); err != nil {
			return nil, err
		}
		out[tpl.ProfileID] = tpl
	}
	return out, rows.Err()
}

// nullableID passes an empty string as SQL NULL for the nullable binding
// columns device_id/profile_id.
func nullableID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// Create inserts a new desired spec at revision 1 in PENDING and returns the
// row. Config/mappings are the raw per-instance overrides; when ProfileID is
// set the effective config/mappings are merged at dispatch time. ConnectorType
// is resolved by the caller (explicit value or profile template) because the
// column is NOT NULL. The caller publishes the downlink push.
func (s *PGStore) Create(ctx context.Context, tenantID, gatewayID string, in SaveInput, connectorType, typeVersion string) (Spec, error) {
	cfg := normalizeRaw(in.Config, "{}")
	mappings := normalizeRaw(in.Mappings, "[]")
	health, err := healthColumn(in.HealthCheck)
	if err != nil {
		return Spec{}, err
	}

	row := s.pool.QueryRow(ctx,
		`INSERT INTO connector_instance
		    (tenant_id, gateway_id, device_id, profile_id,
		     connector_type, type_version, config, mappings, health_check,
		     revision, state)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,1,'PENDING')
		 RETURNING `+returningColumns,
		tenantID, gatewayID, nullableID(in.DeviceID), nullableID(in.ProfileID),
		connectorType, typeVersion, cfg, mappings, health)
	return mapSpecError(scanSpec(row))
}

// healthColumn validates the desired probe policy and returns the JSONB
// value ({} when disabled).
func healthColumn(raw json.RawMessage) ([]byte, error) {
	hc, err := normalizeHealthCheck(raw)
	if err != nil {
		return nil, err
	}
	return healthCheckColumn(hc)
}

// mapSpecError converts a unique-violation on the per-device unique index
// into ErrConflict while leaving other scan errors untouched.
func mapSpecError(sp Spec, err error) (Spec, error) {
	if err == nil {
		return sp, nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return Spec{}, fmt.Errorf("%w: connector instance already exists for this device", ErrConflict)
	}
	return sp, err
}

// Update overwrites the desired fields, bumps the revision, resets the
// lifecycle to PENDING and clears any prior reported state so a new desired
// revision is applied and reported again. connectorType/typeVersion are the
// values resolved by the caller (explicit or from the profile template).
func (s *PGStore) Update(ctx context.Context, tenantID, specID string, in SaveInput, connectorType, typeVersion string) (Spec, error) {
	cfg := normalizeRaw(in.Config, "{}")
	mappings := normalizeRaw(in.Mappings, "[]")
	health, err := healthColumn(in.HealthCheck)
	if err != nil {
		return Spec{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("begin connector update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx,
		`UPDATE connector_instance SET
		     device_id = $3, profile_id = $4,
		     connector_type = $5, type_version = $6, config = $7,
		     mappings = $8, health_check = $9,
		     revision = revision + 1, state = 'PENDING',
		     reported_revision = 0, report_reason = '', reported_stats = NULL,
		     observed_revision = NULL, observed_hash = '',
		     not_applied_alarm = FALSE, failed_alarm = FALSE,
		     failed_critical = FALSE, failed_at = NULL,
		     dispatched_at = NULL, reported_at = NULL,
		     desired_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING `+returningColumns,
		tenantID, specID, nullableID(in.DeviceID), nullableID(in.ProfileID),
		connectorType, typeVersion, cfg, mappings, health)
	sp, err := scanSpec(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNotFound
	}
	if sp, err = mapSpecError(sp, err); err != nil {
		return Spec{}, err
	}
	if err := supersedeDeletes(ctx, tx, sp); err != nil {
		return Spec{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Spec{}, fmt.Errorf("commit connector update: %w", err)
	}
	return sp, nil
}

// Delete atomically removes desired and persists its explicit-delete intent.
// The caller publishes only after this transaction commits.
func (s *PGStore) Delete(ctx context.Context, tenantID, specID string) (Spec, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("begin connector delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	sp, err := scanSpec(tx.QueryRow(ctx,
		`DELETE FROM connector_instance WHERE tenant_id = $1 AND id = $2
		 RETURNING `+returningColumns, tenantID, specID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNotFound
	}
	if err != nil {
		return Spec{}, fmt.Errorf("delete connector spec: %w", err)
	}
	var revision int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO connector_delete_outbox
		    (tenant_id,gateway_id,spec_id,device_id,connector_type,type_version,delete_revision)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (tenant_id,gateway_id,spec_id) DO UPDATE SET
		   device_id=EXCLUDED.device_id, connector_type=EXCLUDED.connector_type,
		   type_version=EXCLUDED.type_version,
		   delete_revision=GREATEST(connector_delete_outbox.delete_revision,EXCLUDED.delete_revision),
		   state='PENDING', last_error='', updated_at=now()
		 RETURNING delete_revision`,
		sp.TenantID, sp.GatewayID, sp.ID, nullableID(sp.DeviceID),
		sp.ConnectorType, sp.TypeVersion, sp.Revision).Scan(&revision); err != nil {
		return Spec{}, fmt.Errorf("persist connector delete tombstone: %w", err)
	}
	sp.Revision = revision
	if err := tx.Commit(ctx); err != nil {
		return Spec{}, fmt.Errorf("commit connector delete: %w", err)
	}
	return sp, nil
}

func supersedeDeletes(ctx context.Context, tx pgx.Tx, sp Spec) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM connector_delete_outbox
		 WHERE tenant_id=$1 AND gateway_id=$2 AND spec_id=$3 AND delete_revision < $4`,
		sp.TenantID, sp.GatewayID, sp.ID, sp.Revision); err != nil {
		return fmt.Errorf("supersede connector delete tombstone: %w", err)
	}
	return nil
}

// Get returns one spec scoped by tenant.
func (s *PGStore) Get(ctx context.Context, tenantID, specID string) (Spec, error) {
	row := s.pool.QueryRow(ctx, specColumns+`
	     FROM connector_instance WHERE tenant_id = $1 AND id = $2`,
		tenantID, specID)
	sp, err := scanSpec(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNotFound
	}
	return sp, err
}

// ListByGateway returns every spec for a gateway, newest first.
func (s *PGStore) ListByGateway(ctx context.Context, tenantID, gatewayID string) ([]Spec, error) {
	rows, err := s.pool.Query(ctx,
		specColumns+` FROM connector_instance
		 WHERE tenant_id = $1 AND gateway_id = $2
		 ORDER BY created_at`, tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list connector specs: %w", err)
	}
	defer rows.Close()
	return collectSpecs(rows)
}

// ListAllForGateway returns every spec for a gateway regardless of tenant;
// used by the post-hello redrive where the gateway identity is already
// authenticated. GatewayID is the UUID device id.
func (s *PGStore) ListAllForGateway(ctx context.Context, gatewayID string) ([]Spec, error) {
	rows, err := s.pool.Query(ctx,
		specColumns+` FROM connector_instance
		 WHERE gateway_id = $1 ORDER BY created_at`, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("redrive connector specs: %w", err)
	}
	defer rows.Close()
	return collectSpecs(rows)
}

// ListDeleteTombstones returns at most limit unacknowledged explicit deletes,
// prioritizing never-attempted rows and then the least recently attempted. An
// empty gateway selects all gateways for the
// background replay sweep. A desired row defensively suppresses an older
// tombstone even if a caller missed fencing.
func (s *PGStore) ListDeleteTombstones(ctx context.Context, gatewayID string, limit int) ([]DeleteTombstone, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx,
		`SELECT o.tenant_id::text,o.gateway_id::text,o.spec_id::text,
		        COALESCE(o.device_id::text,''),o.connector_type,o.type_version,
		        o.delete_revision,o.state,o.attempt_count,o.last_error,
		        o.created_at,o.updated_at,o.last_attempt_at
		 FROM connector_delete_outbox o
		 WHERE ($1 = '' OR o.gateway_id = NULLIF($1,'')::uuid)
		   AND NOT EXISTS (SELECT 1 FROM connector_instance ci
		                   WHERE ci.tenant_id=o.tenant_id AND ci.gateway_id=o.gateway_id
		                     AND ci.id=o.spec_id)
		 ORDER BY o.last_attempt_at NULLS FIRST,o.created_at,o.spec_id
		 LIMIT $2`, gatewayID, limit)
	if err != nil {
		return nil, fmt.Errorf("list connector delete tombstones: %w", err)
	}
	defer rows.Close()
	out := []DeleteTombstone{}
	for rows.Next() {
		var tombstone DeleteTombstone
		if err := rows.Scan(&tombstone.TenantID, &tombstone.GatewayID, &tombstone.SpecID,
			&tombstone.DeviceID, &tombstone.ConnectorType, &tombstone.TypeVersion,
			&tombstone.DeleteRevision, &tombstone.State, &tombstone.AttemptCount,
			&tombstone.LastError, &tombstone.CreatedAt, &tombstone.UpdatedAt,
			&tombstone.LastAttemptAt); err != nil {
			return nil, fmt.Errorf("scan connector delete tombstone: %w", err)
		}
		out = append(out, tombstone)
	}
	return out, rows.Err()
}

// MarkDeleteAttempt records transport success/failure without acknowledging
// edge removal. Revision fencing prevents an old completion from touching a
// newer tombstone.
func (s *PGStore) MarkDeleteAttempt(ctx context.Context, tombstone DeleteTombstone, at time.Time, lastError string) error {
	state := StateDispatched
	if lastError != "" {
		state = StatePending
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE connector_delete_outbox SET
		   state=$5,attempt_count=attempt_count+1,last_error=$6,
		   last_attempt_at=$7,updated_at=now()
		 WHERE tenant_id=$1 AND gateway_id=$2 AND spec_id=$3 AND delete_revision=$4`,
		tombstone.TenantID, tombstone.GatewayID, tombstone.SpecID,
		tombstone.DeleteRevision, state, lastError, at)
	if err != nil {
		return fmt.Errorf("mark connector delete attempt: %w", err)
	}
	return nil
}

// MarkDispatched records the first desired push for a fresh revision and moves
// a PENDING spec to DISPATCHED. It deliberately never touches CREATING or a
// terminal state: the post-hello redrive re-pushes every spec, including ones
// the edge already reports CREATING/APPLIED, and that retransmission must not
// regress reported lifecycle state.
func (s *PGStore) MarkDispatched(ctx context.Context, tenantID, specID string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE connector_instance
		 SET dispatched_at = $3, state = 'DISPATCHED', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND state = 'PENDING'`,
		tenantID, specID, at)
	if err != nil {
		return fmt.Errorf("mark connector dispatched: %w", err)
	}
	return nil
}

// ApplyStatus records the edge-reported state for a spec revision. APPLIED
// and DEGRADED are success-ish reports that clear every escalation flag; a
// new FAILED report stamps failed_at once while preserving the alarm flags
// so the 1-hour MAJOR→CRITICAL escalation is driven by the sweep. It
// returns ErrNotFound when the spec is unknown to the tenant (the edge must
// re-sync its desired set).
func (s *PGStore) ApplyStatus(ctx context.Context, tenantID string, f StatusFrame, at time.Time) (Spec, error) {
	if !validReportedState(f.State) {
		return Spec{}, fmt.Errorf("%w: unknown state %q", ErrInvalid, f.State)
	}
	var stats any
	if f.Stats != nil {
		raw, err := json.Marshal(f.Stats)
		if err != nil {
			return Spec{}, fmt.Errorf("%w: encode stats: %v", ErrInvalid, err)
		}
		stats = raw
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE connector_instance SET
		     state = $3, reported_revision = $4, report_reason = $5,
		     reported_at = $6, updated_at = now(), reported_stats = $7,
		     observed_revision = CASE WHEN $3 = 'APPLIED' THEN $8::bigint ELSE NULL END,
		     observed_hash = CASE WHEN $3 = 'APPLIED' THEN $9 ELSE '' END,
		     not_applied_alarm = CASE WHEN $3 = 'FAILED' THEN not_applied_alarm
		                              ELSE FALSE END,
		     failed_at = CASE WHEN $3 = 'FAILED'
		                      THEN coalesce(failed_at, $6) ELSE NULL END,
		     failed_alarm = CASE WHEN $3 IN ('APPLIED','DEGRADED')
		                         THEN FALSE ELSE failed_alarm END,
		     failed_critical = CASE WHEN $3 IN ('APPLIED','DEGRADED')
		                            THEN FALSE ELSE failed_critical END
		 WHERE tenant_id = $1 AND id = $2 AND revision = $4
		 RETURNING `+returningColumns,
		tenantID, f.SpecID, f.State, f.Revision, f.Reason, at, stats,
		f.ObservedRevision, f.ObservedHash)
	sp, err := scanSpec(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// A different current revision is a fenced no-op, not a missing spec.
		// A deleted/foreign id remains ErrNotFound and cannot affect outbox state.
		current, getErr := s.Get(ctx, tenantID, f.SpecID)
		if getErr != nil {
			return Spec{}, getErr
		}
		return current, nil
	}
	return sp, err
}

// StampManifest records the full-list connector watermark from one gateway
// manifest in a single statement. Every spec of the gateway that the edge
// reported is updated with its applied revision, config hash and the cycle
// time; specs the edge omitted are left untouched so their stale/NULL
// watermark marks them missing. It returns the post-update desired rows so
// the caller can diff without a second round trip.
func (s *PGStore) StampManifest(ctx context.Context, tenantID, gatewayID string, reported []ReportedInstance, at time.Time) ([]Spec, error) {
	ids := make([]string, 0, len(reported))
	revs := make([]int64, 0, len(reported))
	hashes := make([]string, 0, len(reported))
	seen := map[string]struct{}{}
	for _, r := range reported {
		if r.SpecID == "" {
			continue
		}
		if _, dup := seen[r.SpecID]; dup {
			continue // full list carries each spec at most once
		}
		seen[r.SpecID] = struct{}{}
		ids = append(ids, r.SpecID)
		revs = append(revs, r.Revision)
		hashes = append(hashes, r.ConfigHash)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("stamp connector manifest: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if len(ids) > 0 {
		// One batched update: the positional arrays are aligned by index.
		// Tenant + gateway scoping ensures a report for another tenant's or
		// another gateway's spec id cannot move its watermark.
		if _, err := tx.Exec(ctx,
			`UPDATE connector_instance AS ci
			 SET manifest_revision = u.rev, reported_config_hash = u.hash,
			     last_manifest_at = $3, updated_at = now()
			 FROM unnest($4::uuid[], $5::bigint[], $6::text[])
			      AS u(id, rev, hash)
			 WHERE ci.tenant_id = $1 AND ci.gateway_id = $2 AND ci.id = u.id`,
			tenantID, gatewayID, at, ids, revs, hashes); err != nil {
			return nil, fmt.Errorf("stamp connector manifest: %w", err)
		}
	}
	// Persist the gateway-level watermark (last full-list time and the exact
	// reported spec-id set) in the same tx so a later read-only classification
	// can derive both missing desired specs and extra edge specs without
	// accepting another manifest (migration 0055). This runs even for an empty
	// reported list: an empty full-list manifest means every desired spec is
	// missing, not that no manifest was seen.
	if _, err := tx.Exec(ctx,
		`INSERT INTO connector_manifest_watermark
		    (tenant_id, gateway_id, observed_at, reported_ids)
		 VALUES ($1,$2,$3,$4::text[])
		 ON CONFLICT (tenant_id, gateway_id) DO UPDATE
		   SET observed_at = EXCLUDED.observed_at,
		       reported_ids = EXCLUDED.reported_ids`,
		tenantID, gatewayID, at, ids); err != nil {
		return nil, fmt.Errorf("stamp connector manifest watermark: %w", err)
	}
	rows, err := tx.Query(ctx,
		specColumns+` FROM connector_instance
		 WHERE tenant_id = $1 AND gateway_id = $2
		 ORDER BY created_at`, tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("stamp connector manifest list: %w", err)
	}
	specs, err := collectSpecs(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit connector manifest: %w", err)
	}
	return specs, nil
}

// GetWatermark returns the last full connector manifest a gateway reported, or
// ErrNotFound when it has never sent one. Used by the read-only reconcile view
// (migration 0055).
func (s *PGStore) GetWatermark(ctx context.Context, tenantID, gatewayID string) (ManifestWatermark, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT tenant_id::text, gateway_id::text, observed_at, reported_ids
		 FROM connector_manifest_watermark
		 WHERE tenant_id=$1 AND gateway_id=$2`,
		tenantID, gatewayID)
	var wm ManifestWatermark
	if err := row.Scan(&wm.TenantID, &wm.GatewayID, &wm.ObservedAt, &wm.SpecIDs); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ManifestWatermark{}, ErrNotFound
		}
		return ManifestWatermark{}, fmt.Errorf("get connector manifest watermark: %w", err)
	}
	if wm.SpecIDs == nil {
		wm.SpecIDs = []string{}
	}
	return wm, nil
}

// BumpRevision advances desired by one and resets the lifecycle to PENDING
// without changing any desired content. It is used by strict reconciliation
// when the edge runs the same revision with mutated config: the edge dedupes
// downlinks by (spec_id, revision), so re-pushing the identical revision would
// be discarded; an NL-side bump forces the desired content to be re-applied.
// It returns the updated spec, or ErrNotFound when the spec is gone.
func (s *PGStore) BumpRevision(ctx context.Context, tenantID, specID string) (Spec, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Spec{}, fmt.Errorf("begin connector revision bump: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	row := tx.QueryRow(ctx,
		`UPDATE connector_instance SET
		     revision = revision + 1, state = 'PENDING',
		     reported_revision = 0, report_reason = '', reported_stats = NULL,
		     observed_revision = NULL, observed_hash = '',
		     not_applied_alarm = FALSE, failed_alarm = FALSE,
		     failed_critical = FALSE, failed_at = NULL,
		     dispatched_at = NULL, reported_at = NULL,
		     desired_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2
		 RETURNING `+returningColumns,
		tenantID, specID)
	sp, err := scanSpec(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spec{}, ErrNotFound
	}
	if err != nil {
		return Spec{}, err
	}
	if err := supersedeDeletes(ctx, tx, sp); err != nil {
		return Spec{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Spec{}, fmt.Errorf("commit connector revision bump: %w", err)
	}
	return sp, nil
}

// ClaimPendingForAlarm atomically marks and returns open specs whose latest// desired revision has spent longer than age from desired_at without an
// APPLIED report and for which the WARNING not-applied alarm is not yet open.
// The UPDATE...RETURNING claim makes raising at-most-once across restarts and
// replicas; the flag is re-armed by a desired update or a FAILED report and
// cleared on APPLIED/DEGRADED.
func (s *PGStore) ClaimPendingForAlarm(ctx context.Context, _ time.Time, ageCutoff time.Time) ([]Spec, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE connector_instance
		 SET not_applied_alarm = TRUE
		 WHERE id IN (
		   SELECT id FROM connector_instance
		   WHERE state IN ('PENDING','DISPATCHED','CREATING')
		     AND not_applied_alarm = FALSE
		     AND desired_at <= $1
		 )
		 RETURNING `+returningColumns,
		ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim pending connector specs: %w", err)
	}
	defer rows.Close()
	return collectSpecs(rows)
}

// ClaimFailedForRaise marks and returns specs newly reported FAILED that do
// not yet have the MAJOR failed alarm open.
func (s *PGStore) ClaimFailedForRaise(ctx context.Context, _ time.Time) ([]Spec, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE connector_instance
		 SET failed_alarm = TRUE
		 WHERE state = 'FAILED' AND failed_alarm = FALSE
		 RETURNING `+returningColumns)
	if err != nil {
		return nil, fmt.Errorf("claim failed connector specs: %w", err)
	}
	defer rows.Close()
	return collectSpecs(rows)
}

// ClaimFailedForEscalation marks and returns FAILED specs whose failure has
// persisted longer than age from the first failed_at and that have the MAJOR
// alarm open but are not yet CRITICAL.
func (s *PGStore) ClaimFailedForEscalation(ctx context.Context, _ time.Time, ageCutoff time.Time) ([]Spec, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE connector_instance
		 SET failed_critical = TRUE
		 WHERE state = 'FAILED' AND failed_alarm = TRUE
		   AND failed_critical = FALSE AND failed_at <= $1
		 RETURNING `+returningColumns, ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim failed-escalation connector specs: %w", err)
	}
	defer rows.Close()
	return collectSpecs(rows)
}

func validReportedState(state string) bool {
	switch state {
	case StateCreating, StateApplied, StateFailed, StateDegraded:
		return true
	}
	return false
}

const returningColumns = `       id::text, tenant_id::text, gateway_id::text,
       COALESCE(device_id::text, ''), COALESCE(profile_id::text, ''),
       connector_type, type_version, config, mappings, revision,
       state, reported_revision, report_reason, not_applied_alarm,
       failed_alarm, failed_critical, failed_at,
       created_at, updated_at, desired_at, dispatched_at, reported_at,
       manifest_revision, reported_config_hash, last_manifest_at,
       reported_stats, observed_revision, observed_hash, health_check`

const specColumns = "SELECT " + returningColumns

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanSpec(row rowScanner) (Spec, error) {
	var sp Spec
	var health json.RawMessage
	var stats json.RawMessage
	err := row.Scan(&sp.ID, &sp.TenantID, &sp.GatewayID,
		&sp.DeviceID, &sp.ProfileID,
		&sp.ConnectorType, &sp.TypeVersion, &sp.Config, &sp.Mappings,
		&sp.Revision, &sp.State, &sp.ReportedRevision, &sp.ReportReason,
		&sp.NotAppliedAlarm, &sp.FailedAlarm, &sp.FailedCritical, &sp.FailedAt,
		&sp.CreatedAt, &sp.UpdatedAt, &sp.DesiredAt,
		&sp.DispatchedAt, &sp.ReportedAt,
		&sp.ManifestRevision, &sp.ManifestHash, &sp.LastManifestAt,
		&stats, &sp.ObservedRevision, &sp.ObservedHash, &health)
	if err != nil {
		return Spec{}, fmt.Errorf("scan connector spec: %w", err)
	}
	if len(health) > 0 && string(health) != "{}" {
		if err := json.Unmarshal(health, &sp.HealthCheck); err != nil {
			return Spec{}, fmt.Errorf("scan connector spec health_check: %w", err)
		}
	}
	if len(stats) > 0 {
		sp.Stats = &StatusStats{}
		if err := json.Unmarshal(stats, sp.Stats); err != nil {
			return Spec{}, fmt.Errorf("scan connector spec stats: %w", err)
		}
	}
	return sp, nil
}

func collectSpecs(rows pgx.Rows) ([]Spec, error) {
	out := []Spec{}
	for rows.Next() {
		sp, err := scanSpec(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, rows.Err()
}
