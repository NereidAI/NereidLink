// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists forwarder configurations and dead letters. Rows hold no
// receiver connection state; the worker is fully stateless across restarts.

package egress

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound means no forwarder/DLQ row matched.
var ErrNotFound = errors.New("forwarder not found")

// ErrNameTaken means a forwarder with that name already exists for the tenant.
var ErrNameTaken = errors.New("forwarder name already exists")

// Store reads and writes forwarder rows.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a forwarder store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// forwarderColumns is the read projection kept in one place.
const forwarderColumns = `id::text, tenant_id::text, name, sink_type,
       sink_config, encoder, max_attempts, message_ttl_ms,
       max_buffer_messages, enabled,
       COALESCE(created_by::text, ''), created_at, updated_at`

// scanForwarder scans one row using the forwarderColumns projection.
func scanForwarder(row pgx.Row) (Forwarder, error) {
	var f Forwarder
	err := row.Scan(&f.ID, &f.TenantID, &f.Name, &f.SinkType,
		&f.SinkConfig, &f.Encoder, &f.MaxAttempts, &f.MessageTTLMS,
		&f.MaxBufferMessages, &f.Enabled,
		&f.CreatedBy, &f.CreatedAt, &f.UpdatedAt)
	return f, err
}

// Create inserts a new forwarder. The caller assigns the id and stamps the
// timestamps; a duplicate (tenant,name) returns ErrNameTaken.
func (s *Store) Create(ctx context.Context, f *Forwarder) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO forwarder
		   (id, tenant_id, name, sink_type, sink_config, encoder,
		    max_attempts, message_ttl_ms, max_buffer_messages, enabled,
		    created_by, created_at, updated_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12)`,
		f.ID, f.TenantID, f.Name, f.SinkType, f.SinkConfig, f.Encoder,
		f.MaxAttempts, f.MessageTTLMS, f.MaxBufferMessages, f.Enabled,
		nullable(f.CreatedBy), f.CreatedAt)
	return mapWriteError(err)
}

// Update applies a partial edit to an existing forwarder and bumps updated_at.
// Only the fields in the input change; max_attempts only changes when > 0.
func (s *Store) Update(
	ctx context.Context,
	tenantID, id string,
	in Input,
) (Forwarder, error) {
	now := time.Now()
	// sink_type is intentionally not updated: it is immutable after
	// creation and the service layer already rejects a mismatching value.
	row := s.pool.QueryRow(ctx,
		`UPDATE forwarder SET
		    name=$3, sink_config=$4,
		    encoder=$5,
		    max_attempts=CASE WHEN $6>0 THEN $6 ELSE max_attempts END,
		    message_ttl_ms=CASE WHEN $9>0 THEN $9 ELSE message_ttl_ms END,
		    max_buffer_messages=CASE WHEN $10>0 THEN $10 ELSE max_buffer_messages END,
		    enabled=$7, updated_at=$8
		 WHERE tenant_id=$1 AND id=$2
		 RETURNING `+forwarderColumns,
		tenantID, id, in.Name, in.SinkConfig,
		encoderDefault(in.Encoder), in.MaxAttempts, in.Enabled, now,
		in.MessageTTLMS, in.MaxBufferMessages)
	f, err := scanForwarder(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Forwarder{}, ErrNotFound
	}
	if err != nil {
		return Forwarder{}, mapWriteError(err)
	}
	return f, nil
}

// Delete removes one forwarder; its DLQ rows cascade. The JetStream stream is
// deleted separately by the service alongside the row.
func (s *Store) Delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM forwarder WHERE tenant_id=$1 AND id=$2`,
		tenantID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Get loads one forwarder.
func (s *Store) Get(ctx context.Context, tenantID, id string) (Forwarder, error) {
	f, err := scanForwarder(s.pool.QueryRow(ctx,
		`SELECT `+forwarderColumns+`
		 FROM forwarder WHERE tenant_id=$1 AND id=$2`,
		tenantID, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Forwarder{}, ErrNotFound
	}
	return f, err
}

// List returns every forwarder owned by a tenant, ordered by name.
func (s *Store) List(ctx context.Context, tenantID string) ([]Forwarder, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+forwarderColumns+`
		 FROM forwarder WHERE tenant_id=$1 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// ListAll returns every forwarder across tenants, used by the worker to
// reconcile desired consumers.
func (s *Store) ListAll(ctx context.Context) ([]Forwarder, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+forwarderColumns+` FROM forwarder ORDER BY tenant_id, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return collect(rows)
}

// collect drains a forwarder row set.
func collect(rows pgx.Rows) ([]Forwarder, error) {
	out := []Forwarder{}
	for rows.Next() {
		f, err := scanForwarder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// InsertDLQ parks one failed envelope.
func (s *Store) InsertDLQ(ctx context.Context, dlq *DeadLetter) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO forwarder_dlq
		   (id, tenant_id, forwarder_id, payload, error, attempts,
		    last_tried_at, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		dlq.ID, dlq.TenantID, dlq.ForwarderID, dlq.Envelope, dlq.Error,
		dlq.Attempts, dlq.LastTriedAt, time.Now())
	return err
}

// ListDLQ returns dead letters for a tenant, optionally for one forwarder,
// newest first.
func (s *Store) ListDLQ(
	ctx context.Context,
	tenantID string,
	forwarderID string,
) ([]DeadLetter, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if forwarderID != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, tenant_id::text, forwarder_id::text, payload,
			        error, attempts, last_tried_at, replayed_at, created_at
			 FROM forwarder_dlq
			 WHERE tenant_id=$1 AND forwarder_id=$2
			 ORDER BY created_at DESC`,
			tenantID, forwarderID)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, tenant_id::text, forwarder_id::text, payload,
			        error, attempts, last_tried_at, replayed_at, created_at
			 FROM forwarder_dlq
			 WHERE tenant_id=$1
			 ORDER BY created_at DESC`,
			tenantID)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []DeadLetter{}
	for rows.Next() {
		var d DeadLetter
		if err := rows.Scan(&d.ID, &d.TenantID, &d.ForwarderID, &d.Envelope,
			&d.Error, &d.Attempts, &d.LastTriedAt, &d.ReplayedAt,
			&d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkReplayed stamps a DLQ row after its envelope was re-published.
func (s *Store) MarkReplayed(ctx context.Context, tenantID, dlqID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE forwarder_dlq SET replayed_at=now()
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, dlqID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// InsertDelivery appends one delivery-log row.
func (s *Store) InsertDelivery(ctx context.Context, d *Delivery) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO forwarder_delivery
		   (id, tenant_id, forwarder_id, envelope_id, device_id,
		    status, code, reason, attempts, occurred_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		d.ID, d.TenantID, d.ForwarderID, d.EnvelopeID, d.DeviceID,
		d.Status, d.Code, d.Reason, d.Attempts, d.OccurredAt)
	return err
}

// ListDeliveries returns up to limit newest delivery-log rows for a tenant,
// optionally restricted to one forwarder.
func (s *Store) ListDeliveries(
	ctx context.Context,
	tenantID string,
	forwarderID string,
	limit int,
) ([]Delivery, error) {
	if limit <= 0 {
		limit = deliveryListLimit
	}
	var (
		rows pgx.Rows
		err  error
	)
	if forwarderID != "" {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, tenant_id::text, forwarder_id::text,
			        envelope_id, device_id, status, code, reason, attempts,
			        occurred_at
			 FROM forwarder_delivery
			 WHERE tenant_id=$1 AND forwarder_id=$2
			 ORDER BY occurred_at DESC
			 LIMIT $3`,
			tenantID, forwarderID, limit)
	} else {
		rows, err = s.pool.Query(ctx,
			`SELECT id::text, tenant_id::text, forwarder_id::text,
			        envelope_id, device_id, status, code, reason, attempts,
			        occurred_at
			 FROM forwarder_delivery
			 WHERE tenant_id=$1
			 ORDER BY occurred_at DESC
			 LIMIT $2`,
			tenantID, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Delivery{}
	for rows.Next() {
		var d Delivery
		if err := rows.Scan(&d.ID, &d.TenantID, &d.ForwarderID,
			&d.EnvelopeID, &d.DeviceID, &d.Status, &d.Code, &d.Reason,
			&d.Attempts, &d.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// PruneDeliveries bounds the log: rows older than olderThan are deleted, and
// for any forwarder above capPerForwarder the oldest excess rows are removed.
func (s *Store) PruneDeliveries(
	ctx context.Context,
	olderThan time.Time,
	capPerForwarder int,
) error {
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM forwarder_delivery WHERE occurred_at < $1`,
		olderThan); err != nil {
		return err
	}
	if capPerForwarder <= 0 {
		return nil
	}
	// Rank within each forwarder newest-first and drop only the excess of
	// partitions that exceed the cap, so small forwarders are untouched.
	_, err := s.pool.Exec(ctx,
		`DELETE FROM forwarder_delivery AS d
		 USING (
		   SELECT id FROM (
		     SELECT id,
		            row_number() OVER (
		              PARTITION BY forwarder_id
		              ORDER BY occurred_at DESC, id
		            ) AS rn,
		            count(*) OVER (PARTITION BY forwarder_id) AS cnt
		     FROM forwarder_delivery
		   ) ranked
		   WHERE ranked.cnt > $1 AND ranked.rn > $1
		 ) AS keep
		 WHERE d.id = keep.id`,
		capPerForwarder)
	return err
}

// nullable maps an empty string to SQL NULL.
func nullable(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// encoderDefault defaults an empty encoder to JSON.
func encoderDefault(v string) string {
	if v == "" {
		return EncoderJSON
	}
	return v
}

// mapWriteError converts a unique violation to ErrNameTaken.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrNameTaken
	}
	return err
}
