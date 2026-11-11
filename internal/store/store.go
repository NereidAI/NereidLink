// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package store provides the PostgreSQL persistence layer: connection
// management, embedded schema migrations, deterministic seed data and the
// small set of SQL queries used by M0 services.
//
// Services never manage DDL themselves: schema changes ship as numbered SQL
// files embedded into the binary and are applied explicitly through the
// `migrate` command (never automatically at startup, which would be unsafe
// with multiple cluster replicas).
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/identity"
	"github.com/nereidai/nereidlink/internal/rbac"

	// Register the postgres migrate driver via blank import.
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
)

// migrationsFS embeds the SQL migration files so the shipped binary is
// self-contained for version upgrades.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Connect creates the shared pgx connection pool. The pool is safe for
// concurrent use by all services running in the all-in-one process.
func Connect(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	return pgxpool.New(ctx, dsn)
}

// Migrate applies all pending up migrations. It is invoked exclusively by the
// `migrate` CLI command. ErrNoChange is treated as success.
//
// After the schema is up to date, Migrate backfills the telemetry rollups.
// That step lives here rather than in migration SQL because
// refresh_continuous_aggregate() refuses to run inside a transaction block
// ("SQLSTATE 25001") while golang-migrate wraps every migration file in one.
// Until the backfill runs, the rollups hold no history and the telemetry
// chart would answer every historical window with zero rows, so a failure
// here is returned rather than logged and ignored.
func Migrate(dsn string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, dsn)
	if err != nil {
		return err
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		return err
	}
	return backfillTelemetryRollups(dsn)
}

// telemetryRollups are the continuous aggregates defined by migration 0024.
// The hourly aggregate is listed first because the daily one is hierarchical:
// refreshing it refreshes the hourly buckets it is derived from.
var telemetryRollups = []string{
	"ts_telemetry_hourly",
	"ts_telemetry_daily",
}

// backfillTelemetryRollups materializes existing history into the continuous
// aggregates.
//
// The refresh is idempotent and skips the still-open window, so running it on
// every migrate is safe and costs nothing once the rollups have caught up.
// Hourly is refreshed before daily because daily is defined over hourly and
// would otherwise rebuild from a stale hourly rollup.
func backfillTelemetryRollups(dsn string) error {
	ctx, cancel := context.WithTimeout(context.Background(), telemetryRollupBackfillTimeout)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect for rollup backfill: %w", err)
	}
	defer pool.Close()

	for _, rollup := range telemetryRollups {
		started := time.Now()
		if _, err := pool.Exec(ctx, `CALL refresh_continuous_aggregate($1, NULL, NULL)`, rollup); err != nil {
			// 42P01 undefined_table means migration 0024 has not been applied,
			// so there is nothing to backfill and nothing routes to a rollup.
			if isUndefinedTable(err) {
				return nil
			}
			return fmt.Errorf("backfill rollup %s: %w", rollup, err)
		}
		slog.Info("refreshed telemetry rollup", "rollup", rollup, "took", time.Since(started))
	}
	return nil
}

// telemetryRollupBackfillTimeout bounds one migrate invocation's backfill.
// Backfilling a large hypertable can legitimately take a long time, but it
// must not hang forever holding the caller's migrate command.
const telemetryRollupBackfillTimeout = 2 * time.Hour

// isUndefinedTable reports whether err is PostgreSQL's undefined_table.
func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "42P01"
	}
	return false
}

// SeedParams holds the deterministic identifiers written by the seed command.
type SeedParams struct {
	// TenantID is the seeded tenant UUID.
	TenantID string
	// DeviceID is the seeded device UUID.
	DeviceID string
	// DeviceToken is the device access token.
	DeviceToken string
}

// Seed writes the minimal development dataset (one tenant, one device, one
// token credential) inside a single transaction. Every statement is conflict
// tolerant so repeated runs converge to the same state.
func Seed(ctx context.Context, pool *pgxpool.Pool, p SeedParams) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'Default Tenant')
		 ON CONFLICT (id) DO NOTHING`, p.TenantID); err != nil {
		return fmt.Errorf("seed tenant: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name)
		 VALUES ($1, $2, 'seed-device')
		 ON CONFLICT (id) DO NOTHING`, p.DeviceID, p.TenantID); err != nil {
		return fmt.Errorf("seed device: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO device_credential (id, device_id, type, credentials_id, credentials_value)
		 VALUES (gen_random_uuid(), $1, 'TOKEN', $2, '')
		 ON CONFLICT (credentials_id) DO NOTHING`,
		p.DeviceID, p.DeviceToken); err != nil {
		return fmt.Errorf("seed credential: %w", err)
	}

	if err := seedDefaultProfile(ctx, tx, p); err != nil {
		return err
	}

	// Declare the two development commands so the M2 path exercises both
	// one-way/persistent and request-response/realtime catalog entries.
	if _, err := tx.Exec(ctx,
		`INSERT INTO command_catalog
		   (tenant_id, device_id, name, interaction, timeliness, default_timeout_ms)
		 VALUES
		   ($1, $2, 'reboot', 'one-way', 'persistent', 0),
		   ($1, $2, 'set-led', 'request-response', 'realtime', 5000)
		 ON CONFLICT (tenant_id, device_id, name) DO NOTHING`,
		p.TenantID, p.DeviceID); err != nil {
		return fmt.Errorf("seed command catalog: %w", err)
	}

	if err := seedDefaultRuleChain(ctx, tx, p.TenantID); err != nil {
		return err
	}

	if err := seedAdminUser(ctx, pool, p.TenantID); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

const (
	seedProfileID        = "20000000-0000-0000-0000-000000000001"
	seedTempDescriptorID = "20000000-0000-0000-0000-000000000101"
	seedHumDescriptorID  = "20000000-0000-0000-0000-000000000102"
)

// seedDefaultProfile creates the development profile and assigns it to
// the seed device. It deliberately declares no descriptors: the platform
// must not invent units or transforms the operator never configured. Any
// descriptors left from older seed runs (including their units stamped
// onto stored rows) are removed so the seed is a clean blank profile.
func seedDefaultProfile(ctx context.Context, tx pgx.Tx, p SeedParams) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name)
		 VALUES ($1, $2, 'Default Profile')
		 ON CONFLICT (id) DO NOTHING`,
		seedProfileID, p.TenantID); err != nil {
		return fmt.Errorf("seed device profile: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE device
		 SET profile_id = $1
		 WHERE id = $2 AND tenant_id = $3`,
		seedProfileID, p.DeviceID, p.TenantID); err != nil {
		return fmt.Errorf("attach seed device profile: %w", err)
	}

	// Remove the legacy seed descriptors by their deterministic IDs and
	// clear units they stamped onto the seed device's stored rows. Scope
	// is limited to those rows; operator-defined descriptors are untouched.
	if _, err := tx.Exec(ctx,
		`DELETE FROM measurement_descriptor WHERE id = ANY($1)`,
		[]string{seedTempDescriptorID, seedHumDescriptorID}); err != nil {
		return fmt.Errorf("remove legacy seed descriptors: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE ts_telemetry
		 SET unit = ''
		 WHERE device_id = $1 AND measurement_key = ANY($2)`,
		p.DeviceID, []string{"temperature", "humidity"}); err != nil {
		return fmt.Errorf("clear legacy seed units: %w", err)
	}
	return nil
}

// Deterministic identifiers of the seeded default rule chain and its group.
const (
	seedRootChainGroupID = "10000000-0000-0000-0000-000000000002"
	seedRootChainID      = "10000000-0000-0000-0000-000000000001"
	seedSwitchNode       = "10000000-0000-0000-0000-000000000101"
	seedCreateNode       = "10000000-0000-0000-0000-000000000102"
	seedClearNode        = "10000000-0000-0000-0000-000000000103"
	seedHotEdge          = "10000000-0000-0000-0000-000000000201"
	seedColdEdge         = "10000000-0000-0000-0000-000000000202"
)

// seedDefaultRuleChain inserts the MVP root chain: a Switch evaluates only
// temperature frames, routing hot frames to Create Alarm and cooled frames
// to Clear Alarm. Non-temperature frames match no case and pass through
// untouched.
func seedDefaultRuleChain(ctx context.Context, tx pgx.Tx, tenantID string) error {
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_chain_group (id, tenant_id, name, is_default)
		 VALUES ($1, $2, 'Root Rule Chain', TRUE)
		 -- Ignore any unique violation (id or tenant+name): seed must be
		 -- re-runnable against an already provisioned database.
		 ON CONFLICT DO NOTHING`,
		seedRootChainGroupID, tenantID); err != nil {
		return fmt.Errorf("seed rule chain group: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_chain
		   (id, tenant_id, name, version, state, is_root, chain_group_id)
		 VALUES ($1, $2, 'Root Rule Chain', 1, 'ACTIVE', TRUE, $3)
		 ON CONFLICT (id) DO NOTHING`,
		seedRootChainID, tenantID, seedRootChainGroupID); err != nil {
		return fmt.Errorf("seed rule chain: %w", err)
	}

	switchConfig := `{"cases":[
	  {"label":"Hot","script":"return msg.measurement_key === 'temperature' && msg.temperature > 24;"},
	  {"label":"Cold","script":"return msg.measurement_key === 'temperature' && msg.temperature <= 24;"}
	]}`
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_node (id, chain_id, type, name, config)
		 VALUES ($1, $2, 'switch', 'Temperature routing', $3)
		 ON CONFLICT (id) DO NOTHING`,
		seedSwitchNode, seedRootChainID, switchConfig); err != nil {
		return fmt.Errorf("seed switch node: %w", err)
	}

	createConfig := `{"alarm_type":"HighTemperature","severity":"MAJOR"}`
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_node (id, chain_id, type, name, config)
		 VALUES ($1, $2, 'create-alarm', 'Raise high-temperature alarm', $3)
		 ON CONFLICT (id) DO NOTHING`,
		seedCreateNode, seedRootChainID, createConfig); err != nil {
		return fmt.Errorf("seed create-alarm node: %w", err)
	}

	clearConfig := `{"alarm_type":"HighTemperature"}`
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_node (id, chain_id, type, name, config)
		 VALUES ($1, $2, 'clear-alarm', 'Clear high-temperature alarm', $3)
		 ON CONFLICT (id) DO NOTHING`,
		seedClearNode, seedRootChainID, clearConfig); err != nil {
		return fmt.Errorf("seed clear-alarm node: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_edge (id, chain_id, from_node, to_node, relation)
		 VALUES ($1, $2, $3, $4, 'Hot'),
		        ($5, $2, $3, $6, 'Cold')
		 ON CONFLICT (id) DO NOTHING`,
		seedHotEdge, seedRootChainID, seedSwitchNode, seedCreateNode,
		seedColdEdge, seedClearNode); err != nil {
		return fmt.Errorf("seed rule edges: %w", err)
	}
	return nil
}

// Deterministic credentials of the development super administrator.
const (
	seedAdminEmail    = "admin@nereidai.local"
	seedAdminPassword = "Admin12345!"
)

// seedAdminUser creates the single super administrator account when
// absent. It is intentionally a platform admin (tenant_id NULL) rather
// than a member of a tenant, matching the cross-tenant operator role.
//
// Migration 0049 may have already created the same admin with an EMPTY
// password for first-run bootstrap. `nereidlink seed` is an explicit
// developer/deterministic-E2E action, so it fills that empty hash with the
// well-known development password. An admin that already has any password
// (production bootstrap or a prior seed) is never overwritten.
func seedAdminUser(ctx context.Context, pool *pgxpool.Pool, tenantID string) error {
	var exists bool
	if err := pool.QueryRow(ctx,
		`SELECT TRUE FROM app_user
		 WHERE email = $1 AND tenant_id IS NULL`,
		seedAdminEmail).Scan(&exists); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check admin user: %w", err)
	}
	if !exists {
		if _, err := identity.CreateUser(ctx, pool, identity.DefaultPasswordPolicy(),
			identity.CreateUserInput{
				Email:         seedAdminEmail,
				DisplayName:   "Platform Admin",
				Password:      seedAdminPassword,
				PlatformAdmin: true,
				EmailVerified: true,
			}); err != nil {
			return fmt.Errorf("seed admin user: %w", err)
		}
	} else if err := setDevPasswordWhenUnset(ctx, pool); err != nil {
		return err
	}

	var adminID string
	if err := pool.QueryRow(ctx,
		`SELECT id::text FROM app_user WHERE email = $1 AND tenant_id IS NULL`,
		seedAdminEmail).Scan(&adminID); err != nil {
		return fmt.Errorf("read admin id: %w", err)
	} // Tenant Admin membership in the seed tenant lets the platform
	// operator reach business data; platform administration alone must
	// not pierce tenant data (ADR-0022). Membership is ensured on
	// every run so older databases missing it converge.
	if err := rbac.EnsureMembership(ctx, pool, adminID, tenantID,
		rbac.RoleTenantAdmin); err != nil {
		return fmt.Errorf("seed admin membership: %w", err)
	}
	return nil
}

// setDevPasswordWhenUnset fills the development seed password only when the
// admin account exists but has no password yet (the migration-0049 bootstrap
// state). An already-configured password is never overwritten.
func setDevPasswordWhenUnset(ctx context.Context, pool *pgxpool.Pool) error {
	hash, err := identity.HashSecret(seedAdminPassword)
	if err != nil {
		return fmt.Errorf("hash seed admin password: %w", err)
	}
	tag, err := pool.Exec(ctx,
		`UPDATE app_user
		 SET password_hash = $2,
		     must_change_password = FALSE,
		     email_verified = TRUE,
		     updated_at = now()
		 WHERE email = $1 AND tenant_id IS NULL AND password_hash = ''`,
		seedAdminEmail, hash)
	if err != nil {
		return fmt.Errorf("set seed admin password: %w", err)
	}
	_ = tag
	return nil
}

// StoredCredential is one active credential row loaded for the hot
// authentication path. It carries the mechanism type and stored secret
// material so the registry can verify possession per mechanism rather
// than through one hard-coded equality match.
type StoredCredential struct {
	// ID is the credential row identifier.
	ID string
	// TenantID is the tenant that owns the device.
	TenantID string
	// DeviceID is the authenticated device identifier.
	DeviceID string
	// Type is the credential mechanism: TOKEN, PASSWORD, JWT or X509.
	Type string
	// CredentialsID is the public identifier presented as username.
	CredentialsID string
	// CredentialsValue holds the mechanism-specific verification
	// material (token, argon2 PHC, HMAC secret or public key PEM).
	CredentialsValue string
	// Extra carries optional mechanism constraints as raw JSONB
	// (e.g. JWT issuer/subject/audience restrictions).
	Extra []byte

	// ExpiresAt bounds the credential validity; nil means no expiry. It
	// lets callers cap a cache entry TTL at the credential window.
	ExpiresAt *time.Time

	// CredentialEpoch is the tenant epoch stamped at load, used to fence
	// cached entries after an emergency epoch bump.
	CredentialEpoch int64
}

// LoadCredential resolves one credential by its globally unique
// credentials_id and applies the shared validity gates: ACTIVE status,
// effective/expiry window, ACTIVE tenant and ACTIVE device. Secret
// verification is mechanism-specific and left to the caller so adding
// a mechanism never changes this query.
func LoadCredential(ctx context.Context, pool *pgxpool.Pool, credentialsID string) (StoredCredential, error) {
	var c StoredCredential
	err := pool.QueryRow(ctx,
		`SELECT c.id::text, d.tenant_id::text, d.id::text, c.type,
		        c.credentials_id, c.credentials_value,
		        COALESCE(c.extra, '{}'::jsonb),
		        c.expires_at, t.credential_epoch
		 FROM device_credential c
		 JOIN device d ON d.id = c.device_id
		 JOIN tenant t ON t.id = d.tenant_id
		 WHERE c.credentials_id = $1
		   AND c.status = 'ACTIVE'
		   AND (c.effective_at IS NULL OR c.effective_at <= now())
		   AND (c.expires_at IS NULL OR c.expires_at > now())
		   AND t.status = 'ACTIVE'
		   AND d.state = 'ACTIVE'`,
		credentialsID).Scan(&c.ID, &c.TenantID, &c.DeviceID, &c.Type,
		&c.CredentialsID, &c.CredentialsValue, &c.Extra,
		&c.ExpiresAt, &c.CredentialEpoch)
	if err != nil {
		return StoredCredential{}, fmt.Errorf("credential lookup: %w", err)
	}
	return c, nil
}
