// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists gateway secret bindings (migration 0071).

package secretbinding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const columns = `id::text, tenant_id::text, gateway_id::text, secret_name,
	secret_type, type_version, selectors, revision, status, reason,
	dispatched_at, applied_at, removed_at, created_at, updated_at`

// scanBinding decodes one binding row.
func scanBinding(row pgx.Row) (Binding, error) {
	var b Binding
	err := row.Scan(&b.ID, &b.TenantID, &b.GatewayID, &b.SecretName,
		&b.SecretType, &b.TypeVersion, &b.Selectors, &b.Revision, &b.Status,
		&b.Reason, &b.DispatchedAt, &b.AppliedAt, &b.RemovedAt,
		&b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// Store is the PostgreSQL binding store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a binding store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Get loads one binding for a gateway.
func (s *Store) Get(ctx context.Context, tenantID, gatewayID, secretName string) (Binding, error) {
	b, err := scanBinding(s.pool.QueryRow(ctx,
		`SELECT `+columns+` FROM gateway_secret_binding
		 WHERE tenant_id=$1 AND gateway_id=$2 AND secret_name=$3`,
		tenantID, gatewayID, secretName))
	if errors.Is(err, pgx.ErrNoRows) {
		return Binding{}, ErrNotFound
	}
	if err != nil {
		return Binding{}, fmt.Errorf("load binding: %w", err)
	}
	return b, nil
}

// ListByGateway lists a gateway's bindings, optionally excluding REMOVED.
func (s *Store) ListByGateway(ctx context.Context, tenantID, gatewayID string, includeRemoved bool) ([]Binding, error) {
	query := `SELECT ` + columns + ` FROM gateway_secret_binding
	 WHERE tenant_id=$1 AND gateway_id=$2`
	if !includeRemoved {
		query += ` AND status <> 'REMOVED'`
	}
	query += ` ORDER BY secret_name`
	return s.collect(ctx, query, tenantID, gatewayID)
}

// ListBySecret lists non-removed bindings for one vault entry across
// gateways (used by deletion protection).
func (s *Store) ListBySecret(ctx context.Context, tenantID, secretName string) ([]Binding, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columns+` FROM gateway_secret_binding
		 WHERE tenant_id=$1 AND secret_name=$2 AND status <> 'REMOVED'
		 ORDER BY gateway_id::text`,
		tenantID, secretName)
	if err != nil {
		return nil, fmt.Errorf("list bindings by secret: %w", err)
	}
	defer rows.Close()
	return collectRows(rows)
}

// ActiveGatewayIDs returns the distinct gateway ids a secret is bound to
// (non-removed), for deletion-protection prompts.
func (s *Store) ActiveGatewayIDs(ctx context.Context, tenantID, secretName string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT gateway_id::text FROM gateway_secret_binding
		 WHERE tenant_id=$1 AND secret_name=$2 AND status <> 'REMOVED'`,
		tenantID, secretName)
	if err != nil {
		return nil, fmt.Errorf("list binding gateways: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// References lists every non-removed binding of one secret with the
// gateway's display name (joined from device). Used by the vault deletion
// guard to show which gateways still hold a copy.
func (s *Store) References(ctx context.Context, tenantID, secretName string) ([]Reference, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT b.gateway_id::text, d.canonical_name, b.status
		 FROM gateway_secret_binding b
		 JOIN device d ON d.id = b.gateway_id
		 WHERE b.tenant_id=$1 AND b.secret_name=$2 AND b.status <> 'REMOVED'
		 ORDER BY d.canonical_name`,
		tenantID, secretName)
	if err != nil {
		return nil, fmt.Errorf("list secret references: %w", err)
	}
	defer rows.Close()
	out := []Reference{}
	for rows.Next() {
		var ref Reference
		if err := rows.Scan(&ref.GatewayID, &ref.GatewayName, &ref.Status); err != nil {
			return nil, err
		}
		out = append(out, ref)
	}
	return out, rows.Err()
}

// Upsert inserts or merges one desired change, bumping revision and
// resetting to PENDING. A REMOVING/REMOVED row being re-declared becomes an
// active desired binding again (re-delivery).
func (s *Store) Upsert(ctx context.Context, tenantID, gatewayID string, change DesiredChange, at time.Time) (Binding, error) {
	b, err := scanBinding(s.pool.QueryRow(ctx,
		`INSERT INTO gateway_secret_binding
		 (tenant_id, gateway_id, secret_name, secret_type, type_version,
		  selectors, revision, status, desired_at)
		 VALUES ($1,$2,$3,$4,$5,$6,1,'PENDING',$7)
		 ON CONFLICT (gateway_id, secret_name) DO UPDATE
		 SET secret_type=EXCLUDED.secret_type,
		     type_version=EXCLUDED.type_version,
		     selectors=EXCLUDED.selectors,
		     revision=gateway_secret_binding.revision+1,
		     status='PENDING',
		     reason='',
		     not_applied_alarm=FALSE,
		     failed_alarm=FALSE,
		     failed_critical=FALSE,
		     desired_at=$7,
		     failed_at=NULL,
		     dispatched_at=NULL,
		     applied_at=NULL,
		     removed_at=NULL,
		     updated_at=$7
		 RETURNING `+columns,
		tenantID, gatewayID, change.SecretName, change.SecretType,
		change.TypeVersion, change.Selectors, at))
	if err != nil {
		return Binding{}, fmt.Errorf("upsert binding: %w", err)
	}
	return b, nil
}

// Withdraw moves an active binding to REMOVING with a bumped revision. A
// missing/removed row is an error so callers can treat withdrawal
// deterministically.
func (s *Store) Withdraw(ctx context.Context, tenantID, gatewayID, secretName string, at time.Time) (Binding, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE gateway_secret_binding
		 SET revision=revision+1, status='REMOVING', updated_at=$4
		 WHERE tenant_id=$1 AND gateway_id=$2 AND secret_name=$3
		   AND status NOT IN ('REMOVING','REMOVED')`,
		tenantID, gatewayID, secretName, at)
	if err != nil {
		return Binding{}, fmt.Errorf("withdraw binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Binding{}, ErrNotFound
	}
	return s.Get(ctx, tenantID, gatewayID, secretName)
}

// MarkDispatched stamps DISPATCHED after delivery. It is idempotent: a
// repeated redrive of an already-DISPATCHED revision is a no-op rather than
// an error.
func (s *Store) MarkDispatched(ctx context.Context, tenantID, gatewayID string, revision int64, at time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE gateway_secret_binding
		 SET status='DISPATCHED', dispatched_at=$4, updated_at=$4
		 WHERE tenant_id=$1 AND gateway_id=$2 AND revision=$3
		   AND status IN ('PENDING','FAILED')`,
		tenantID, gatewayID, revision, at); err != nil {
		return fmt.Errorf("mark dispatched: %w", err)
	}
	return nil
}

// Report stores an edge uplink (APPLIED/FAILED; REMOVED completes a
// withdrawal).
func (s *Store) Report(ctx context.Context, tenantID, gatewayID string, report Report, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE gateway_secret_binding
		 SET status=$4,
		     reason=$5,
		     applied_at=CASE WHEN $4='APPLIED' THEN $6::timestamptz ELSE applied_at END,
		     removed_at=CASE WHEN $4='REMOVED' THEN $6::timestamptz ELSE removed_at END,
		     not_applied_alarm=CASE WHEN $4='FAILED' THEN not_applied_alarm
		                            ELSE FALSE END,
		     failed_at=CASE WHEN $4='FAILED' THEN coalesce(failed_at,$6::timestamptz)
		                    ELSE NULL END,
		     failed_alarm=CASE WHEN $4 IN ('APPLIED','REMOVED') THEN FALSE
		                       ELSE failed_alarm END,
		     failed_critical=CASE WHEN $4 IN ('APPLIED','REMOVED') THEN FALSE
		                            ELSE failed_critical END,
		     updated_at=$6
		 WHERE tenant_id=$1 AND gateway_id=$2 AND secret_name=$3`,
		tenantID, gatewayID, report.SecretName, report.Status, report.Reason, at)
	if err != nil {
		return fmt.Errorf("report binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkRemoved completes a withdrawal after the edge confirms removal.
func (s *Store) MarkRemoved(ctx context.Context, tenantID, gatewayID, secretName string, at time.Time) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE gateway_secret_binding
		 SET status='REMOVED', removed_at=$4, updated_at=$4
		 WHERE tenant_id=$1 AND gateway_id=$2 AND secret_name=$3
		   AND status='REMOVING'`,
		tenantID, gatewayID, secretName, at)
	if err != nil {
		return fmt.Errorf("mark removed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ClaimPendingForAlarm atomically marks and returns bindings whose latest
// desired revision has spent longer than age without an APPLIED report and
// whose WARNING not-applied alarm is not yet open. The UPDATE...RETURNING
// claim makes raising at-most-once across restarts and replicas; the flag is
// re-armed by a desired update and cleared on APPLIED/REMOVED.
func (s *Store) ClaimPendingForAlarm(ctx context.Context, ageCutoff time.Time) ([]Binding, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE gateway_secret_binding
		 SET not_applied_alarm=TRUE
		 WHERE id IN (
		   SELECT id FROM gateway_secret_binding
		   WHERE status IN ('PENDING','DISPATCHED')
		     AND not_applied_alarm=FALSE
		     AND desired_at <= $1
		 )
		 RETURNING `+columns,
		ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim pending bindings: %w", err)
	}
	defer rows.Close()
	return collectRows(rows)
}

// ClaimFailedForRaise marks and returns bindings newly reported FAILED that
// do not yet have the MAJOR failed alarm open.
func (s *Store) ClaimFailedForRaise(ctx context.Context) ([]Binding, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE gateway_secret_binding
		 SET failed_alarm=TRUE
		 WHERE status='FAILED' AND failed_alarm=FALSE
		 RETURNING `+columns)
	if err != nil {
		return nil, fmt.Errorf("claim failed bindings: %w", err)
	}
	defer rows.Close()
	return collectRows(rows)
}

// ClaimFailedForEscalation marks and returns FAILED bindings whose failure
// has persisted longer than age and that have the MAJOR alarm open but are
// not yet CRITICAL.
func (s *Store) ClaimFailedForEscalation(ctx context.Context, ageCutoff time.Time) ([]Binding, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE gateway_secret_binding
		 SET failed_critical=TRUE
		 WHERE status='FAILED' AND failed_alarm=TRUE
		   AND failed_critical=FALSE AND failed_at <= $1
		 RETURNING `+columns,
		ageCutoff)
	if err != nil {
		return nil, fmt.Errorf("claim failed-escalation bindings: %w", err)
	}
	defer rows.Close()
	return collectRows(rows)
}

func (s *Store) collect(ctx context.Context, query string, args ...any) ([]Binding, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list bindings: %w", err)
	}
	defer rows.Close()
	return collectRows(rows)
}

func collectRows(rows pgx.Rows) ([]Binding, error) {
	out := []Binding{}
	for rows.Next() {
		b, err := scanBinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
