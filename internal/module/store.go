// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package module

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// moduleColumns is the canonical SELECT list, kept in one place so callers
// scan rows in the same order.
const moduleColumns = `
	id::text, type, name, ingress_id, enabled, config, version, status,
	enrollment_token, registered_at, last_heartbeat, created_at, updated_at`

// Store is the southbound module registry backed by PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a module registry store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// scanRow maps one row onto a Module.
func scanRow(row pgx.Row) (Module, error) {
	var m Module
	err := row.Scan(
		&m.ID, &m.Type, &m.Name, &m.IngressID, &m.Enabled, &m.Config,
		&m.Version, &m.Status, &m.EnrollmentToken, &m.RegisteredAt,
		&m.LastHeartbeat, &m.CreatedAt, &m.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Module{}, ErrNotFound
	}
	if err != nil {
		return Module{}, fmt.Errorf("scan module: %w", err)
	}
	return m, nil
}

// List returns every module ordered by type then name.
func (s *Store) List(ctx context.Context) ([]Module, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+moduleColumns+`
		 FROM southbound_module
		 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("list modules: %w", err)
	}
	defer rows.Close()

	modules := []Module{}
	for rows.Next() {
		m, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		modules = append(modules, m)
	}
	return modules, rows.Err()
}

// Get returns one module by ID.
func (s *Store) Get(ctx context.Context, id string) (Module, error) {
	return scanRow(s.pool.QueryRow(ctx,
		`SELECT `+moduleColumns+`
		 FROM southbound_module
		 WHERE id = $1`, id))
}

// CreateInput provisions a new module.
type CreateInput struct {
	// Type is one of the supported protocol types.
	Type string

	// Name is the unique module name.
	Name string

	// IngressID is the dispatch identity.
	IngressID string

	// Enabled is the initial traffic gate.
	Enabled bool

	// Config holds optional protocol-specific parameters.
	Config json.RawMessage
}

// Create provisions a module and returns it with a generated enrollment
// token. The row exists before the adapter process starts so the token can
// be delivered to the deployment out of band.
func (s *Store) Create(ctx context.Context, input CreateInput) (Module, error) {
	if err := validateCreate(input); err != nil {
		return Module{}, err
	}

	id := uuid.NewString()
	token, err := newToken()
	if err != nil {
		return Module{}, err
	}
	config := input.Config
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}

	return scanRow(s.pool.QueryRow(ctx,
		`INSERT INTO southbound_module
		 (id, type, name, ingress_id, enabled, config, enrollment_token)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+moduleColumns,
		id, input.Type, input.Name, input.IngressID, input.Enabled,
		config, token))
}

// UpdateInput changes mutable module fields. Nil fields keep their value.
type UpdateInput struct {
	// Name renames the module.
	Name *string

	// Enabled flips the traffic gate.
	Enabled *bool

	// Config replaces protocol parameters.
	Config *json.RawMessage
}

// Update applies partial changes and returns the updated module.
func (s *Store) Update(ctx context.Context, id string, input UpdateInput) (Module, error) {
	current, err := s.Get(ctx, id)
	if err != nil {
		return Module{}, err
	}

	name := current.Name
	if input.Name != nil {
		name = *input.Name
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	config := current.Config
	if input.Config != nil {
		config = *input.Config
	}
	if len(config) == 0 {
		config = json.RawMessage(`{}`)
	}

	return scanRow(s.pool.QueryRow(ctx,
		`UPDATE southbound_module
		 SET name = $2, enabled = $3, config = $4, updated_at = now()
		 WHERE id = $1
		 RETURNING `+moduleColumns,
		id, name, enabled, config))
}

// Delete removes a module row.
func (s *Store) Delete(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM southbound_module WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete module: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Register validates an adapter enrollment and flips the module to ONLINE.
// The provisioned type must match so a misconfigured adapter cannot renew
// another module's lease; the ingress identity is returned in the response
// because the adapter cannot know it before registering.
func (s *Store) Register(ctx context.Context, input RegisterInput) (Module, error) {
	if input.EnrollmentToken == "" {
		return Module{}, ErrInvalidEnrollment
	}

	now := time.Now().UTC()
	raw := s.pool.QueryRow(ctx,
		`UPDATE southbound_module
		 SET status = 'ONLINE',
		     version = $2,
		     registered_at = COALESCE(registered_at, $3),
		     last_heartbeat = $3,
		     updated_at = now()
		 WHERE enrollment_token = $1
		   AND type = $4
		 RETURNING `+moduleColumns,
		input.EnrollmentToken, input.Version, now,
		input.Type)
	m, err := scanRow(raw)
	if errors.Is(err, ErrNotFound) {
		// Distinguish an unknown token from a token/type mismatch so
		// operators see the actual misconfiguration.
		var exists bool
		if lookupErr := s.pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM southbound_module
				WHERE enrollment_token = $1)`,
			input.EnrollmentToken).Scan(&exists); lookupErr == nil && exists {
			return Module{}, ErrTypeMismatch
		}
		return Module{}, ErrInvalidEnrollment
	}
	return m, err
}

// Heartbeat renews the lease of a module. The caller is authenticated by
// the enrollment token rather than the ID so a stolen or recreated ID
// alone cannot forge a lease.
func (s *Store) Heartbeat(ctx context.Context, id, token string) (Module, error) {
	now := time.Now().UTC()
	m, err := scanRow(s.pool.QueryRow(ctx,
		`UPDATE southbound_module
		 SET last_heartbeat = $3,
		     status = CASE WHEN enabled THEN 'ONLINE' ELSE status END,
		     updated_at = now()
		 WHERE id = $1 AND enrollment_token = $2
		 RETURNING `+moduleColumns,
		id, token, now))
	if errors.Is(err, ErrNotFound) {
		// Do not reveal whether the ID or the token is wrong.
		return Module{}, ErrInvalidEnrollment
	}
	return m, err
}

// Sweep flips ONLINE modules whose lease has expired to OFFLINE. It returns
// the number of swept modules.
func (s *Store) Sweep(ctx context.Context, leaseWindow time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE southbound_module
		 SET status = 'OFFLINE', updated_at = now()
		 WHERE status = 'ONLINE'
		   AND (last_heartbeat IS NULL
		        OR last_heartbeat < now() - make_interval(secs => $1))`,
		leaseWindow.Seconds())
	if err != nil {
		return 0, fmt.Errorf("sweep modules: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Run sweeps stale leases on every tick until ctx is canceled.
func (s *Store) Run(ctx context.Context, lease HeartbeatLease) error {
	ticker := time.NewTicker(lease.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if _, err := s.Sweep(ctx, lease.Window); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// IsOnline reports whether the module for protocol is provisioned,
// enabled and heartbeat-fresh. protocol is the uppercase device protocol
// value (COAP, LWM2M, SNMP). A missing module is reported as offline
// rather than an error so command dispatch fails fast with MODULE_OFFLINE.
func (s *Store) IsOnline(ctx context.Context, protocol string) (bool, error) {
	var online bool
	err := s.pool.QueryRow(ctx,
		`SELECT enabled AND status = 'ONLINE'
		 FROM southbound_module
		 WHERE type = lower($1)`,
		protocol).Scan(&online)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check module online: %w", err)
	}
	return online, nil
}

// newToken returns 32 random bytes encoded as a 64-character hex string.
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate enrollment token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}
