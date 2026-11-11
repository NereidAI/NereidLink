// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package modelinventory

import (
	"context"
	"crypto/md5" //nolint:gosec // #nosec G501 -- non-cryptographic deterministic id, must match Postgres md5()::uuid
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// selectColumns is the canonical gateway_model_inventory projection. Tenant
// and gateway are always bound from the caller, not the row.
const selectColumns = `
	model_id, version, revision, task, family, backend, server_family,
	model_name, state, reason, labels, active_bindings, stale, stale_at,
	first_seen, updated_at, failed_alarm`

// PGStore persists the gateway model inventory (migration 0086). Every read
// and write is scoped by tenant_id so a status report or REST call can never
// touch another tenant's inventory.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore constructs a PGStore.
func NewStore(pool *pgxpool.Pool) *PGStore { return &PGStore{pool: pool} }

// scanModel scans one row projected with selectColumns. tenantID/gatewayID
// are supplied by the caller.
func scanModel(row pgx.Row, tenantID, gatewayID string) (Model, error) {
	var (
		m      Model
		labels []byte
	)
	m.TenantID = tenantID
	m.GatewayID = gatewayID
	if err := row.Scan(
		&m.ModelID, &m.Version, &m.Revision, &m.Task, &m.Family,
		&m.Backend, &m.ServerFamily, &m.ModelName, &m.State, &m.Reason,
		&labels, &m.ActiveBindings, &m.Stale, &m.StaleAt,
		&m.FirstSeen, &m.UpdatedAt, &m.failedAlarm,
	); err != nil {
		return Model{}, err
	}
	m.Labels = []string{}
	if len(labels) > 0 && string(labels) != "null" {
		if err := json.Unmarshal(labels, &m.Labels); err != nil {
			return Model{}, fmt.Errorf("decode model labels: %w", err)
		}
	}
	return m, nil
}

// GatewayExists guards reconciliation/REST against a wrong tenant or an
// unknown/non-gateway target.
func (s *PGStore) GatewayExists(ctx context.Context, tenantID, gatewayID string) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device
		               WHERE id=$1 AND tenant_id=$2 AND is_gateway)`,
		gatewayID, tenantID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check model gateway: %w", err)
	}
	return exists, nil
}

// ListByGateway returns the gateway's inventory, newest change first. The
// slice is always non-nil (an empty gateway is "[]" over REST, never null).
func (s *PGStore) ListByGateway(ctx context.Context, tenantID, gatewayID string) ([]Model, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+selectColumns+`
		 FROM gateway_model_inventory
		 WHERE tenant_id=$1 AND gateway_id=$2
		 ORDER BY updated_at DESC, model_id, version`,
		tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list model inventory: %w", err)
	}
	defer rows.Close()
	out := make([]Model, 0)
	for rows.Next() {
		m, err := scanModel(rows, tenantID, gatewayID)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Reconcile applies one authoritative full-replacement models segment for a
// gateway in a single transaction: reported entries are upserted, rows absent
// from the present list are hard-deleted with an audit event each, and stale
// marking from a prior lease expiry is lifted. It returns the signals the
// service needs to open/clear alarms without a second round trip.
func (s *PGStore) Reconcile(ctx context.Context, tenantID, gatewayID string,
	reported []Input, at time.Time) (ReconcileResult, error) {
	var res ReconcileResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, fmt.Errorf("begin model reconcile: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the gateway's current inventory for the duration of the full
	// replacement so concurrent manifest cycles cannot interleave deletes.
	existingRows, err := tx.Query(ctx,
		`SELECT `+selectColumns+`
		 FROM gateway_model_inventory
		 WHERE tenant_id=$1 AND gateway_id=$2
		 FOR UPDATE`,
		tenantID, gatewayID)
	if err != nil {
		return res, fmt.Errorf("load model inventory: %w", err)
	}
	existing := map[string]Model{}
	for existingRows.Next() {
		m, err := scanModel(existingRows, tenantID, gatewayID)
		if err != nil {
			existingRows.Close()
			return res, err
		}
		existing[m.Key()] = m
	}
	existingRows.Close()
	if err := existingRows.Err(); err != nil {
		return res, err
	}

	// Touch the per-segment freshness watermark in the same transaction:
	// a present segment (including an explicit empty list) proves the edge
	// still advertises models, so omission-only silence is detectable by the
	// watchdog even while other manifest segments renew the frame lease.
	if _, err := tx.Exec(ctx,
		`INSERT INTO gateway_model_segment_watermark
		   (tenant_id, gateway_id, last_models_at)
		 VALUES ($1,$2,$3)
		 ON CONFLICT (tenant_id, gateway_id) DO UPDATE
		   SET last_models_at=EXCLUDED.last_models_at`,
		tenantID, gatewayID, at); err != nil {
		return res, fmt.Errorf("touch models segment watermark: %w", err)
	}

	seen := map[string]struct{}{}
	for _, in := range reported {
		if err := in.Validate(); err != nil {
			return res, err
		}
		key := in.ID + "\x00" + in.Version
		if _, dup := seen[key]; dup {
			// A full list must not repeat an inventory key; skip the
			// duplicate rather than double-upserting.
			continue
		}
		seen[key] = struct{}{}

		prev, had := existing[key]
		if had && prev.Stale {
			res.WasStale = true
		}
		// failed_alarm is reset when the row recovers, preserved across
		// non-stale FAILED re-reports (one-shot raise), and re-armed after a
		// stale gap so a still-failed model raises again on fresh reports.
		newFailedAlarm := false
		if in.State == StateFailed && had {
			newFailedAlarm = prev.failedAlarm && !prev.Stale
		}
		// A model that left FAILED clears its alarm; a pruned FAILED model
		// is collected below.
		if had && prev.State == StateFailed && in.State != StateFailed {
			res.ClearedFailed = append(res.ClearedFailed, prev)
		}

		labels := rawJSON(in.Labels, "[]")
		if _, err := tx.Exec(ctx,
			`INSERT INTO gateway_model_inventory
			   (tenant_id, gateway_id, model_id, version, revision, task,
			    family, backend, server_family, model_name, state, reason,
			    labels, active_bindings, stale, stale_at, failed_alarm,
			    first_seen, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,
			         FALSE,NULL,$15,$16,now())
			 ON CONFLICT (tenant_id, gateway_id, model_id, version) DO UPDATE
			   SET revision=EXCLUDED.revision, task=EXCLUDED.task,
			       family=EXCLUDED.family, backend=EXCLUDED.backend,
			       server_family=EXCLUDED.server_family,
			       model_name=EXCLUDED.model_name, state=EXCLUDED.state,
			       reason=EXCLUDED.reason, labels=EXCLUDED.labels,
			       active_bindings=EXCLUDED.active_bindings,
			       stale=FALSE, stale_at=NULL,
			       failed_alarm=EXCLUDED.failed_alarm,
			       updated_at=now()`,
			tenantID, gatewayID, in.ID, in.Version, in.Revision, in.Task,
			in.Family, in.Backend, in.ServerFamily, in.ModelName, in.State,
			in.Reason, string(labels), in.ActiveBindings, newFailedAlarm, at,
		); err != nil {
			return res, fmt.Errorf("upsert model %s/%s: %w", in.ID, in.Version, err)
		}
	}

	// Prune rows absent from a present (possibly empty) list. Models are not
	// presence entities: disappearance is a hard delete, with one durable
	// audit row (and a clear of any open FAILED alarm).
	for key, prev := range existing {
		if _, keep := seen[key]; keep {
			continue
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM gateway_model_inventory
			 WHERE tenant_id=$1 AND gateway_id=$2 AND model_id=$3 AND version=$4`,
			tenantID, gatewayID, prev.ModelID, prev.Version); err != nil {
			return res, fmt.Errorf("prune model %s/%s: %w", prev.ModelID, prev.Version, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO gateway_model_inventory_event
			   (id, tenant_id, gateway_id, model_id, version, event,
			    last_state, observed_at)
			 VALUES ($1,$2,$3,$4,$5,'pruned',$6,$7)`,
			uuid.NewString(), tenantID, gatewayID, prev.ModelID, prev.Version,
			prev.State, at); err != nil {
			return res, fmt.Errorf("record model prune %s/%s: %w", prev.ModelID, prev.Version, err)
		}
		res.Pruned = append(res.Pruned, PrunedEntry{
			ModelID: prev.ModelID, Version: prev.Version, LastState: prev.State,
		})
		if prev.State == StateFailed {
			res.ClearedFailed = append(res.ClearedFailed, prev)
		}
		if prev.Stale {
			res.WasStale = true
		}
	}

	finalRows, err := tx.Query(ctx,
		`SELECT `+selectColumns+`
		 FROM gateway_model_inventory
		 WHERE tenant_id=$1 AND gateway_id=$2
		 ORDER BY model_id, version`,
		tenantID, gatewayID)
	if err != nil {
		return res, fmt.Errorf("reload model inventory: %w", err)
	}
	res.Upserted = make([]Model, 0)
	for finalRows.Next() {
		m, err := scanModel(finalRows, tenantID, gatewayID)
		if err != nil {
			finalRows.Close()
			return res, err
		}
		res.Upserted = append(res.Upserted, m)
	}
	finalRows.Close()
	if err := finalRows.Err(); err != nil {
		return res, err
	}

	if err := tx.Commit(ctx); err != nil {
		return res, fmt.Errorf("commit model reconcile: %w", err)
	}
	return res, nil
}

// ClaimFailedForRaise atomically claims FAILED, non-stale rows that have not
// yet raised an alarm, so at most one replica raises each. The claim UPDATE
// is the same restart-safe pattern the connector sweep uses.
func (s *PGStore) ClaimFailedForRaise(ctx context.Context) ([]Model, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE gateway_model_inventory
		 SET failed_alarm=TRUE
		 WHERE state=$1 AND failed_alarm=FALSE AND stale=FALSE
		 RETURNING tenant_id::text, gateway_id::text, `+selectColumns,
		StateFailed)
	if err != nil {
		return nil, fmt.Errorf("claim failed models: %w", err)
	}
	defer rows.Close()
	return scanClaimedModels(rows)
}

// MarkStale flags every inventory row of a gateway whose gw-manifest lease
// expired. It reports whether any row newly became stale (drives the one
// per-gateway stale alarm). Rows are kept, not pruned.
func (s *PGStore) MarkStale(ctx context.Context, tenantID, gatewayID string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE gateway_model_inventory
		 SET stale=TRUE, stale_at=$3
		 WHERE tenant_id=$1 AND gateway_id=$2 AND stale=FALSE`,
		tenantID, gatewayID, at)
	if err != nil {
		return false, fmt.Errorf("mark model inventory stale: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkInventoryStale flips stale=FALSE rows to stale for every gateway whose
// models-segment watermark is older than cutoff (the edge keeps sending
// manifest frames but stopped carrying the models segment). It returns the
// distinct gateways whose rows newly flipped, so the caller raises exactly
// one per-gateway stale alarm. The UPDATE is atomic, so running the sweep on
// every replica claims each gateway at most once.
func (s *PGStore) MarkInventoryStale(ctx context.Context, cutoff, at time.Time) ([]GatewayRef, error) {
	rows, err := s.pool.Query(ctx,
		`WITH due AS (
		     SELECT tenant_id, gateway_id
		       FROM gateway_model_segment_watermark
		      WHERE last_models_at < $1
		 ),
		 flipped AS (
		     UPDATE gateway_model_inventory mi
		        SET stale=TRUE, stale_at=$2
		       FROM due d
		      WHERE mi.tenant_id=d.tenant_id
		        AND mi.gateway_id=d.gateway_id
		        AND mi.stale=FALSE
		    RETURNING mi.tenant_id::text, mi.gateway_id::text
		 )
		 SELECT DISTINCT tenant_id, gateway_id FROM flipped`,
		cutoff, at)
	if err != nil {
		return nil, fmt.Errorf("mark segment-silent inventory stale: %w", err)
	}
	defer rows.Close()
	out := make([]GatewayRef, 0)
	seen := map[string]struct{}{}
	for rows.Next() {
		var ref GatewayRef
		if err := rows.Scan(&ref.TenantID, &ref.GatewayID); err != nil {
			return nil, err
		}
		if _, dup := seen[ref.GatewayID]; dup {
			continue
		}
		seen[ref.GatewayID] = struct{}{}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// RuntimeRow is one inference-capable gateway's raw shadow inference block.
type RuntimeRow struct {
	TenantID  string
	GatewayID string
	Block     map[string]any
}

// ListRuntimeBlocks returns the shadow reported "inference" block for every
// gateway that negotiated the inference capability. The shadow stores
// reported state verbatim as free JSON (no schema change); a gateway with no
// block yet returns a nil Block so the watchdog can skip it.
func (s *PGStore) ListRuntimeBlocks(ctx context.Context) ([]RuntimeRow, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.tenant_id::text, d.id::text,
		        COALESCE(ds.reported->'inference', 'null'::jsonb)
		 FROM device d
		 JOIN device_shadow ds ON ds.device_id = d.id
		 WHERE d.is_gateway
		   AND d.agent_inference IS DISTINCT FROM '{}'::jsonb`)
	if err != nil {
		return nil, fmt.Errorf("list inference runtime blocks: %w", err)
	}
	defer rows.Close()
	out := make([]RuntimeRow, 0)
	for rows.Next() {
		var (
			r   RuntimeRow
			raw []byte
		)
		if err := rows.Scan(&r.TenantID, &r.GatewayID, &raw); err != nil {
			return nil, err
		}
		r.Block = map[string]any{}
		if len(raw) > 0 && string(raw) != "null" {
			if err := json.Unmarshal(raw, &r.Block); err != nil {
				// A malformed block is treated as absent rather than
				// failing the whole sweep.
				r.Block = nil
			}
		} else {
			r.Block = nil
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanClaimedModels(rows pgx.Rows) ([]Model, error) {
	out := make([]Model, 0)
	for rows.Next() {
		var (
			m      Model
			labels []byte
		)
		if err := rows.Scan(
			&m.TenantID, &m.GatewayID,
			&m.ModelID, &m.Version, &m.Revision, &m.Task, &m.Family,
			&m.Backend, &m.ServerFamily, &m.ModelName, &m.State, &m.Reason,
			&labels, &m.ActiveBindings, &m.Stale, &m.StaleAt,
			&m.FirstSeen, &m.UpdatedAt, &m.failedAlarm,
		); err != nil {
			return nil, err
		}
		m.Labels = []string{}
		if len(labels) > 0 && string(labels) != "null" {
			if err := json.Unmarshal(labels, &m.Labels); err != nil {
				return nil, fmt.Errorf("decode model labels: %w", err)
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// deterministicUUID reproduces Postgres md5(join)::uuid: a 32-char md5 hex
// rendered with standard 8-4-4-4-12 dashes. It keeps the Go provisioning seed
// convergent with the SQL migration seed (migration 0086).
func deterministicUUID(parts ...string) string {
	//nolint:gosec // #nosec G401 -- id derivation, not a security control; must equal Postgres md5()::uuid
	sum := md5.Sum([]byte(joinParts(parts)))
	h := hex.EncodeToString(sum[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func joinParts(parts []string) string {
	out := ""
	for _, p := range parts {
		out += p
	}
	return out
}
