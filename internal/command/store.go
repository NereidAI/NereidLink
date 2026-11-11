// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound indicates a requested catalog entry or command is absent.
var ErrNotFound = errors.New("command not found")

// CatalogEntry is one declared command for a device.
type CatalogEntry struct {
	// Name identifies the command.
	Name string

	// Interaction is the catalog-declared interaction mode.
	Interaction string

	// Timeliness is the catalog-declared timeliness mode.
	Timeliness string

	// DefaultTimeout bounds the response wait when the caller omits it.
	DefaultTimeout time.Duration

	// AllowRead permits dispatching this name as a read.
	AllowRead bool

	// AllowWrite permits dispatching this name as a write.
	AllowWrite bool

	// Enabled switches the entry off without deleting it.
	Enabled bool

	// Source is "manual" (operator-declared) or "auto" (materialized from
	// the bound profile).
	Source string

	// Description is an optional operator note.
	Description string

	// CreatedAt is the declaration time.
	CreatedAt time.Time
}

// Store provides the PostgreSQL operations of the command service.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs the command store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// DeviceState returns the lifecycle state of a device. The command path
// only accepts commands for ACTIVE devices.
func (s *Store) DeviceState(ctx context.Context, tenantID, deviceID string) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx,
		`SELECT state FROM device
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find device state: %w", err)
	}
	return state, nil
}

// DeviceProtocol returns the configured protocol of a device. It lets the
// router gate add-on protocols on their module heartbeat state.
func (s *Store) DeviceProtocol(ctx context.Context, tenantID, deviceID string) (string, error) {
	var protocol string
	err := s.pool.QueryRow(ctx,
		`SELECT protocol FROM device
		 WHERE id = $1 AND tenant_id = $2`,
		deviceID, tenantID).Scan(&protocol)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find device protocol: %w", err)
	}
	return protocol, nil
}

// FindCatalog resolves a declared, enabled command for a device. An
// undeclared or disabled name is rejected here, matching the
// measurement-point whitelist model.
func (s *Store) FindCatalog(ctx context.Context, tenantID, deviceID, name string) (CatalogEntry, error) {
	var entry CatalogEntry
	var timeoutMS int64
	err := s.pool.QueryRow(ctx,
		`SELECT `+catalogScanColumns+`
		 FROM command_catalog
		 WHERE tenant_id = $1 AND device_id = $2 AND name = $3
		   AND enabled = true`,
		tenantID, deviceID, name).
		Scan(&entry.Name, &entry.Interaction, &entry.Timeliness, &timeoutMS,
			&entry.AllowRead, &entry.AllowWrite, &entry.Enabled,
			&entry.Source, &entry.Description, &entry.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogEntry{}, ErrNotFound
	}
	if err != nil {
		return CatalogEntry{}, fmt.Errorf("find command catalog: %w", err)
	}
	entry.DefaultTimeout = time.Duration(timeoutMS) * time.Millisecond
	return entry, nil
}

// CatalogInput is one create/update payload for a declared command.
type CatalogInput struct {
	// Name identifies the command. Ignored on path-scoped updates.
	Name string

	// Interaction is one-way or request-response.
	Interaction string

	// Timeliness is realtime or persistent.
	Timeliness string

	// DefaultTimeoutMS bounds the response wait; 0 means platform default.
	DefaultTimeoutMS int64

	// AllowRead / AllowWrite require at least one true (DB constraint).
	AllowRead  bool
	AllowWrite bool

	// Enabled switches the declaration on or off.
	Enabled bool

	// Description is an optional note.
	Description string

	// Source is manual or auto; the CRUD API only writes manual rows.
	Source string
}

// catalogScanColumns is the ordered projection every catalog read shares.
const catalogScanColumns = `name, interaction, timeliness, default_timeout_ms,
        allow_read, allow_write, enabled, source, description, created_at`

// scanCatalog drains one catalog row.
func scanCatalog(scanner pgx.Row) (CatalogEntry, error) {
	var entry CatalogEntry
	var timeoutMS int64
	err := scanner.Scan(&entry.Name, &entry.Interaction, &entry.Timeliness,
		&timeoutMS, &entry.AllowRead, &entry.AllowWrite, &entry.Enabled,
		&entry.Source, &entry.Description, &entry.CreatedAt)
	if err != nil {
		return CatalogEntry{}, err
	}
	entry.DefaultTimeout = time.Duration(timeoutMS) * time.Millisecond
	return entry, nil
}

// ListCatalog returns the declared commands of one device, newest first.
func (s *Store) ListCatalog(ctx context.Context, tenantID, deviceID string) ([]CatalogEntry, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+catalogScanColumns+`
		 FROM command_catalog
		 WHERE tenant_id = $1 AND device_id = $2
		 ORDER BY created_at DESC, name ASC`,
		tenantID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list command catalog: %w", err)
	}
	defer rows.Close()

	var entries []CatalogEntry
	for rows.Next() {
		entry, err := scanCatalog(rows)
		if err != nil {
			return nil, fmt.Errorf("scan command catalog: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate command catalog: %w", err)
	}
	return entries, nil
}

// UpsertCatalog inserts one catalog entry or replaces it when the name
// already exists. Automatic entries may only be overwritten through
// UpsertAutoCatalog.
func (s *Store) UpsertCatalog(ctx context.Context, tenantID, deviceID string, in CatalogInput) (CatalogEntry, error) {
	source := in.Source
	if source == "" {
		source = SourceManual
	}
	row := s.pool.QueryRow(ctx,
		`INSERT INTO command_catalog
		   (tenant_id, device_id, name, interaction, timeliness,
		    default_timeout_ms, allow_read, allow_write, enabled,
		    description, source, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11, now())
		 ON CONFLICT (tenant_id, device_id, name) DO UPDATE
		 SET interaction = EXCLUDED.interaction,
		     timeliness = EXCLUDED.timeliness,
		     default_timeout_ms = EXCLUDED.default_timeout_ms,
		     allow_read = EXCLUDED.allow_read,
		     allow_write = EXCLUDED.allow_write,
		     enabled = EXCLUDED.enabled,
		     description = EXCLUDED.description,
		     source = EXCLUDED.source
		 RETURNING `+catalogScanColumns,
		tenantID, deviceID, in.Name, in.Interaction, in.Timeliness,
		in.DefaultTimeoutMS, in.AllowRead, in.AllowWrite, in.Enabled,
		in.Description, source)
	entry, err := scanCatalog(row)
	if err != nil {
		return CatalogEntry{}, fmt.Errorf("upsert command catalog: %w", err)
	}
	return entry, nil
}

// UpsertAutoCatalog inserts an automatic entry when absent, but never
// overwrites an operator's local overrides of an existing automatic row
// (enabled switch, timeout, direction flags) and never replaces a manual
// declaration. It reports whether a new automatic row was inserted.
func (s *Store) UpsertAutoCatalog(ctx context.Context, tenantID, deviceID string, in CatalogInput) (bool, error) {
	cmdTag, err := s.pool.Exec(ctx,
		`INSERT INTO command_catalog
		   (tenant_id, device_id, name, interaction, timeliness,
		    default_timeout_ms, allow_read, allow_write, enabled,
		    description, source, created_at)
		 SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'auto', now()
		 WHERE NOT EXISTS (
		   SELECT 1 FROM command_catalog
		   WHERE tenant_id = $1 AND device_id = $2 AND name = $3
		 )`,
		tenantID, deviceID, in.Name, in.Interaction, in.Timeliness,
		in.DefaultTimeoutMS, in.AllowRead, in.AllowWrite, true, in.Description)
	if err != nil {
		return false, fmt.Errorf("insert auto command catalog: %w", err)
	}
	return cmdTag.RowsAffected() > 0, nil
}

// DeleteAutoCatalog removes automatic entries of a device whose names are
// not in the keep set. Manual entries and automatic rows retained by
// operator overrides elsewhere are untouched.
func (s *Store) DeleteAutoCatalog(ctx context.Context, tenantID, deviceID string, keep map[string]struct{}) error {
	rows, err := s.pool.Query(ctx,
		`SELECT name FROM command_catalog
		 WHERE tenant_id = $1 AND device_id = $2 AND source = 'auto'`,
		tenantID, deviceID)
	if err != nil {
		return fmt.Errorf("list auto command catalog: %w", err)
	}
	var stale []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scan auto command catalog: %w", err)
		}
		if _, ok := keep[name]; !ok {
			stale = append(stale, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate auto command catalog: %w", err)
	}
	for _, name := range stale {
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM command_catalog
			 WHERE tenant_id = $1 AND device_id = $2 AND name = $3
			   AND source = 'auto'`,
			tenantID, deviceID, name); err != nil {
			return fmt.Errorf("delete auto command catalog: %w", err)
		}
	}
	return nil
}

// DeleteCatalog removes one declared command.
func (s *Store) DeleteCatalog(ctx context.Context, tenantID, deviceID, name string) error {
	cmdTag, err := s.pool.Exec(ctx,
		`DELETE FROM command_catalog
		 WHERE tenant_id = $1 AND device_id = $2 AND name = $3`,
		tenantID, deviceID, name)
	if err != nil {
		return fmt.Errorf("delete command catalog: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Insert records a new command at its initial status with its TTL deadline.
func (s *Store) Insert(ctx context.Context, c Command, status string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO command
		   (id, tenant_id, device_id, name, params, interaction, timeliness,
		    timeout_ms, state, created_at, expires_at, direction)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		c.ID, c.TenantID, c.DeviceID, c.Name, string(c.Params),
		c.Interaction, c.Timeliness, c.Timeout.Milliseconds(), status,
		c.CreatedAt, expiresAt, c.Direction)
	if err != nil {
		return fmt.Errorf("insert command: %w", err)
	}
	return nil
}

// ClaimPending atomically reserves up to limit pending commands of a device
// for this router instance, so concurrent router replicas cannot deliver
// the same command twice.
func (s *Store) ClaimPending(ctx context.Context, tenantID, deviceID, claimID string, limit int) ([]Command, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT id, tenant_id::text, device_id::text, name, params::text,
		        interaction, timeliness, timeout_ms, created_at, direction
		 FROM command
		 WHERE tenant_id = $1 AND device_id = $2 AND state = $3
		 ORDER BY created_at
		 LIMIT $4
		 FOR UPDATE SKIP LOCKED`,
		tenantID, deviceID, StatusPending, limit)
	if err != nil {
		return nil, fmt.Errorf("select pending: %w", err)
	}

	commands, err := scanCommands(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan pending: %w", err)
	}

	for _, c := range commands {
		if _, err := tx.Exec(ctx,
			`UPDATE command SET state = $1, claimed_by = $2, claimed_at = now()
			 WHERE id = $3`, StatusClaimed, claimID, c.ID); err != nil {
			return nil, fmt.Errorf("claim command: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit claim: %w", err)
	}
	return commands, nil
}

// Complete applies a terminal status to a command.
func (s *Store) Complete(ctx context.Context, id, status string, response []byte, failure string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE command
		 SET state = $1,
		     response = CASE WHEN $2::text IS NULL THEN NULL ELSE $2::jsonb END,
		     error = $3,
		     delivered_at = CASE WHEN $1 IN ($4,$5,$6,$7) THEN COALESCE(delivered_at, now()) ELSE delivered_at END,
		     finished_at = now()
		 WHERE id = $8`,
		status, nullableJSON(response), nullableText(failure),
		StatusDelivered, StatusSucceeded, StatusFailed, StatusTimeout, id)
	if err != nil {
		return fmt.Errorf("complete command: %w", err)
	}
	return nil
}

// CorrectLateResult updates a timed-out command when its device response
// arrives after the synchronous deadline. The already returned HTTP result
// is unchanged; this correction affects only subsequent stored-state reads.
func (s *Store) CorrectLateResult(ctx context.Context, event Event) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE command
		 SET state = $1,
		     response = CASE WHEN $2::text IS NULL THEN NULL ELSE $2::jsonb END,
		     error = NULL,
		     finished_at = now()
		 WHERE id = $3 AND tenant_id = $4 AND state = $5`,
		StatusSucceeded, nullableJSON([]byte(event.Response)),
		event.CommandID, event.TenantID, StatusTimeout)
	if err != nil {
		return fmt.Errorf("correct late command result: %w", err)
	}
	return nil
}

// ReleaseClaim returns claimed-but-undelivered commands to pending, used when
// a dispatch attempt fails before delivery so the command can be retried.
func (s *Store) ReleaseClaim(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE command SET state = $1, claimed_by = NULL, claimed_at = NULL
		 WHERE id = $2 AND state = $3`,
		StatusPending, id, StatusClaimed)
	if err != nil {
		return fmt.Errorf("release claim: %w", err)
	}
	return nil
}

// ExpireDue marks persistent commands whose TTL elapsed as EXPIRED and
// returns the expired identifiers for event publication.
func (s *Store) ExpireDue(ctx context.Context, now time.Time) ([]Event, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE command
		 SET state = $1, finished_at = now()
		 WHERE timeliness = $2
		   AND state IN ($3,$4)
		   AND expires_at <= $5
		 RETURNING id, tenant_id::text, device_id::text`,
		StatusExpired, TimelinessPersistent, StatusPending, StatusClaimed, now)
	if err != nil {
		return nil, fmt.Errorf("expire commands: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var event Event
		event.Status = StatusExpired
		event.Time = now.Format(time.RFC3339)
		if err := rows.Scan(&event.CommandID, &event.TenantID, &event.DeviceID); err != nil {
			return nil, fmt.Errorf("scan expired: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan expired rows: %w", err)
	}
	return events, nil
}

// scanCommands drains command rows from a claim query.
func scanCommands(rows pgx.Rows) ([]Command, error) {
	var commands []Command
	for rows.Next() {
		var c Command
		var params string
		var timeoutMS int64
		var direction string
		if err := rows.Scan(&c.ID, &c.TenantID, &c.DeviceID, &c.Name, &params,
			&c.Interaction, &c.Timeliness, &timeoutMS, &c.CreatedAt, &direction); err != nil {
			return nil, fmt.Errorf("scan command: %w", err)
		}
		c.Params = []byte(params)
		c.Timeout = time.Duration(timeoutMS) * time.Millisecond
		c.Direction = direction
		commands = append(commands, c)
	}
	return commands, nil
}

// nullableJSON converts an empty response to nil so the CASE expression
// keeps the column null.
func nullableJSON(raw []byte) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

// nullableText converts an empty string failure detail to nil.
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}
