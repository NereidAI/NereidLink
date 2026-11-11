// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package provision implements the outbox-style tenant provisioning
// flow required by ADR-0027 (Q24). Creating a tenant inserts the tenant
// row (status PROVISIONING) and a task row in one transaction. A
// background reconciler then claims due tasks and builds, in order:
//
//  1. the shared JetStream telemetry topology,
//  2. the tenant default Device Profile (empty, no descriptors) and the
//     ADR-0039 detection/v1 task-family system profile,
//  3. the tenant default root rule chain (switch/create-alarm/
//     clear-alarm),
//  4. tenant quotas (added with ADR-0014).
//
// Every step is idempotent by name so a retried task converges over a
// half-finished run. Only when all steps succeed is the tenant flipped
// to ACTIVE; a failed attempt is recorded with the error and retried
// after exponential backoff. A non-ACTIVE tenant cannot authenticate
// devices (enforced in store.FindCredential) or log in (enforced with
// the identity plane).
package provision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/internal/modelinventory"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// Provisioning status values shared with the tenant table.
const (
	// StatusProvisioning means the tenant is being prepared and must not
	// be used.
	StatusProvisioning = "PROVISIONING"

	// StatusActive means every provisioning step succeeded.
	StatusActive = "ACTIVE"

	// StatusFailed means the last attempt failed and the task is waiting
	// for its retry time.
	StatusFailed = "FAILED"
)

// Default entity names provisioned for every tenant. They are the
// idempotency keys: a retried run finds these rows by name instead of
// creating duplicates.
const (
	// DefaultProfileName is the empty profile a new tenant starts with.
	DefaultProfileName = "Default Profile"

	// RootChainName is the tenant fallback rule chain and its group.
	RootChainName = "Root Rule Chain"
)

// Task is one provisioning outbox row with its tenant display name.
type Task struct {
	// TenantID is the provisioned tenant identifier.
	TenantID string `json:"tenant_id"`

	// TenantName is the display name copied from the tenant table.
	TenantName string `json:"tenant_name"`

	// TenantStatus is the live tenant-table state (ACTIVE/SUSPENDED),
	// which can diverge from the task Status after suspension.
	TenantStatus string `json:"tenant_status"`

	// Status is PROVISIONING, ACTIVE or FAILED.
	Status string `json:"status"`

	// Attempts is the number of reconcile attempts made so far.
	Attempts int `json:"attempts"`

	// LastError describes the most recent failure.
	LastError string `json:"last_error"`

	// RunAfter is the earliest time the task may be claimed; it doubles
	// as a worker lease while a run is in progress.
	RunAfter time.Time `json:"run_after"`
}

// Tuning controls the reconciler timing.
type Tuning struct {
	// JSReplicas is the JetStream Raft factor for streams/KV (1, 3 or 5).
	JSReplicas int

	// PollInterval is how often the loop looks for due tasks.
	PollInterval time.Duration

	// Lease is how long a claimed task is hidden from other workers;
	// it expires automatically when a worker dies mid-run.
	Lease time.Duration

	// MaxAttempts stops retrying after this many failures; 0 means
	// unlimited (an operator must intervene via the FAILED view).
	MaxAttempts int

	// MinBackoff is the first retry delay.
	MinBackoff time.Duration

	// MaxBackoff caps the exponential backoff.
	MaxBackoff time.Duration
}

// Service reconciles tenant provisioning tasks.
type Service struct {
	pool *pgxpool.Pool
	js   jetstream.JetStream
	tune Tuning

	// quotaInitializer creates the default tenant quota before activation.
	quotaInitializer QuotaInitializer
}

// QuotaInitializer inserts the factory quota row for a new tenant.
type QuotaInitializer interface {
	EnsureDefault(ctx context.Context, tenantID string) error
}

// SetQuotaInitializer wires the default quota initializer.
func (s *Service) SetQuotaInitializer(initializer QuotaInitializer) {
	s.quotaInitializer = initializer
}

// New constructs the provisioning service.
func New(pool *pgxpool.Pool, js jetstream.JetStream, tune Tuning) *Service {
	return &Service{pool: pool, js: js, tune: tune}
}

// CreateTenant inserts a new tenant and its provisioning task in one
// transaction. The returned ID is usable for status polling but not for
// device connections until provisioning completes.
func (s *Service) CreateTenant(ctx context.Context, name string) (string, error) {
	name = trimName(name)
	if name == "" {
		return "", fmt.Errorf("%w: tenant name is required", ErrInvalidArgument)
	}

	tenantID := uuid.NewString()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant (id, name, status)
		 VALUES ($1, $2, $3)`,
		tenantID, name, StatusProvisioning); err != nil {
		return "", fmt.Errorf("insert tenant: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO tenant_provision_task (tenant_id, status)
		 VALUES ($1, $2)`,
		tenantID, StatusProvisioning); err != nil {
		return "", fmt.Errorf("insert provisioning task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return tenantID, nil
}

// GetTask returns one provisioning task.
func (s *Service) GetTask(ctx context.Context, tenantID string) (Task, error) {
	task, err := scanTask(s.pool.QueryRow(ctx,
		`SELECT t.tenant_id::text, n.name, n.status, t.status, t.attempts,
		        t.last_error, t.run_after
		 FROM tenant_provision_task t
		 JOIN tenant n ON n.id = t.tenant_id
		 WHERE t.tenant_id = $1`,
		tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// ListTasks returns every provisioning task, newest first.
func (s *Service) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.tenant_id::text, n.name, n.status, t.status, t.attempts,
		        t.last_error, t.run_after
		 FROM tenant_provision_task t
		 JOIN tenant n ON n.id = t.tenant_id
		 ORDER BY t.created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list provisioning tasks: %w", err)
	}
	defer rows.Close()

	tasks := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// Retry reschedules a FAILED task for immediate pickup.
func (s *Service) Retry(ctx context.Context, tenantID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE tenant_provision_task
		 SET status = $1, last_error = '', run_after = now(), updated_at = now()
		 WHERE tenant_id = $2 AND status = $3`,
		StatusProvisioning, tenantID, StatusFailed)
	if err != nil {
		return fmt.Errorf("retry provisioning task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Run reconciles due tasks until ctx is canceled. Polling instead of
// waking on a signal keeps the component free of extra NATS subjects;
// provisioning latency is bounded by PollInterval (seconds, not a user
// facing path).
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.tune.PollInterval)
	defer ticker.Stop()

	for {
		if err := s.reconcileDue(ctx); err != nil && !errors.Is(err, errNoDueTask) {
			slog.Warn("tenant provisioning pass failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// errNoDueTask marks a claim miss so the Run loop does not log it.
var errNoDueTask = errors.New("no due provisioning task")

// reconcileDue claims and reconciles one due task.
func (s *Service) reconcileDue(ctx context.Context) error {
	tenantID, err := s.claimDueTask(ctx)
	if err != nil {
		return err
	}

	if err := s.reconcile(ctx, tenantID); err != nil {
		slog.Warn("tenant provisioning failed", "tenant", tenantID, "error", err)
		if failErr := s.markFailed(ctx, tenantID, err); failErr != nil {
			return fmt.Errorf("record provisioning failure: %w (original: %v)", failErr, err)
		}
		return nil
	}
	return nil
}

// claimDueTask atomically takes one runnable task. FOR UPDATE SKIP
// LOCKED makes the claim cluster-wide safe; run_after is pushed forward
// by Lease so a claimed task is invisible to other workers and an
// expired lease (dead worker) frees it automatically.
func (s *Service) claimDueTask(ctx context.Context) (string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var tenantID string
	err = tx.QueryRow(ctx,
		`SELECT tenant_id
		 FROM tenant_provision_task
		 WHERE status IN ($1, $2) AND run_after <= now()
		 ORDER BY run_after
		 LIMIT 1
		 FOR UPDATE SKIP LOCKED`,
		StatusProvisioning, StatusFailed).Scan(&tenantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNoDueTask
	}
	if err != nil {
		return "", fmt.Errorf("claim provisioning task: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE tenant_provision_task
		 SET status = $1,
		     attempts = attempts + 1,
		     run_after = now() + $2,
		     updated_at = now()
		 WHERE tenant_id = $3`,
		StatusProvisioning, s.tune.Lease, tenantID); err != nil {
		return "", fmt.Errorf("lease provisioning task: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return tenantID, nil
}

// reconcile runs every provisioning step for one tenant.
func (s *Service) reconcile(ctx context.Context, tenantID string) error {
	// Step 1: shared telemetry topology. Streams are global, but a
	// fresh deployment may never have started another component that
	// ensures them, so the reconciler does it explicitly.
	if err := sdktelemetry.EnsureStreams(ctx, s.js, s.tune.JSReplicas); err != nil {
		return err
	}

	// Step 2: default empty profile.
	if err := s.ensureDefaultProfile(ctx, tenantID); err != nil {
		return err
	}

	// Step 2b: task-family system profiles (ADR-0039 detection/v1). The
	// virtual inference sub-device references detection/v1; seeding here
	// gives every newly provisioned tenant the same system profile the
	// migration gives existing tenants. Idempotent.
	if err := modelinventory.NewStore(s.pool).EnsureSystemProfile(ctx, tenantID); err != nil {
		return fmt.Errorf("seed task-family system profiles: %w", err)
	}

	// Step 3: default root chain group with an ACTIVE root chain.
	if err := s.ensureRootChain(ctx, tenantID); err != nil {
		return err
	}

	// Step 4: initialize the default quota, then activate the tenant.
	if s.quotaInitializer != nil {
		if err := s.quotaInitializer.EnsureDefault(ctx, tenantID); err != nil {
			return err
		}
	}
	return s.activate(ctx, tenantID)
}

// activate flips tenant and task to ACTIVE in one transaction.
func (s *Service) activate(ctx context.Context, tenantID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`UPDATE tenant
		 SET status = $1, status_reason = '', provisioned_at = now()
		 WHERE id = $2`,
		StatusActive, tenantID); err != nil {
		return fmt.Errorf("activate tenant: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE tenant_provision_task
		 SET status = $1, last_error = '', run_after = now(), updated_at = now()
		 WHERE tenant_id = $2`,
		StatusActive, tenantID); err != nil {
		return fmt.Errorf("activate provisioning task: %w", err)
	}
	return tx.Commit(ctx)
}

// markFailed records the error and schedules the next attempt with
// exponential backoff. Exhausted tasks stay FAILED with run_after far
// in the future so operators notice them.
func (s *Service) markFailed(ctx context.Context, tenantID string, cause error) error {
	var attempts int
	if err := s.pool.QueryRow(ctx,
		`SELECT attempts FROM tenant_provision_task WHERE tenant_id = $1`,
		tenantID).Scan(&attempts); err != nil {
		return fmt.Errorf("read attempts: %w", err)
	}

	delay := backoff(s.tune, attempts)
	if s.tune.MaxAttempts > 0 && attempts >= s.tune.MaxAttempts {
		// Give up: hide the task from the reconciler for a year so the
		// FAILED row demands an explicit operator retry.
		delay = 365 * 24 * time.Hour
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE tenant_provision_task
		 SET status = $1, last_error = $2, run_after = now() + $3, updated_at = now()
		 WHERE tenant_id = $4`,
		StatusFailed, cause.Error(), delay, tenantID); err != nil {
		return fmt.Errorf("record failure: %w", err)
	}
	return nil
}

// backoff returns the retry delay after the given number of attempts,
// doubling MinBackoff each time and capping at MaxBackoff.
func backoff(tune Tuning, attempts int) time.Duration {
	delay := tune.MinBackoff
	for i := 0; i < attempts; i++ {
		delay *= 2
		if delay >= tune.MaxBackoff {
			return tune.MaxBackoff
		}
	}
	if delay > tune.MaxBackoff {
		return tune.MaxBackoff
	}
	return delay
}

// ensureDefaultProfile gets or creates the tenant default profile. No
// descriptors are inserted: the platform never invents measurements.
func (s *Service) ensureDefaultProfile(ctx context.Context, tenantID string) error {
	var id string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM device_profile
		 WHERE tenant_id = $1 AND name = $2`,
		tenantID, DefaultProfileName).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find default profile: %w", err)
	}

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (tenant_id, name) DO NOTHING`,
		uuid.NewString(), tenantID, DefaultProfileName); err != nil {
		return fmt.Errorf("create default profile: %w", err)
	}
	return nil
}

// ensureRootChain gets or creates the tenant default chain group and
// guarantees one ACTIVE root chain inside it. A run that died after
// creating the group but before the chain therefore still converges.
func (s *Service) ensureRootChain(ctx context.Context, tenantID string) error {
	groupID, err := s.findOrCreateGroup(ctx, tenantID)
	if err != nil {
		return err
	}

	var chainID string
	err = s.pool.QueryRow(ctx,
		`SELECT rc.id::text
		 FROM rule_chain rc
		 WHERE rc.chain_group_id = $1 AND rc.state = 'ACTIVE'`,
		groupID).Scan(&chainID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("find active root chain: %w", err)
	}
	return s.buildRootChain(ctx, tenantID, groupID)
}

// findOrCreateGroup gets or creates the default chain group by name.
func (s *Service) findOrCreateGroup(ctx context.Context, tenantID string) (string, error) {
	var groupID string
	err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM rule_chain_group
		 WHERE tenant_id = $1 AND name = $2`,
		tenantID, RootChainName).Scan(&groupID)
	if err == nil {
		return groupID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("find root chain group: %w", err)
	}

	groupID = uuid.NewString()
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO rule_chain_group (id, tenant_id, name, is_default)
		 VALUES ($1, $2, $3, TRUE)
		 ON CONFLICT (tenant_id, name) DO NOTHING`,
		groupID, tenantID, RootChainName); err != nil {
		return "", fmt.Errorf("create root chain group: %w", err)
	}

	// ON CONFLICT DO NOTHING may have skipped the insert because
	// another writer raced us; re-read the actual row.
	if err := s.pool.QueryRow(ctx,
		`SELECT id::text FROM rule_chain_group
		 WHERE tenant_id = $1 AND name = $2`,
		tenantID, RootChainName).Scan(&groupID); err != nil {
		return "", fmt.Errorf("re-read root chain group: %w", err)
	}
	return groupID, nil
}

// buildRootChain inserts the ACTIVE root chain with the same graph the
// seed command provisions: a Switch on temperature frames feeding
// Create Alarm (hot) and Clear Alarm (cold).
func (s *Service) buildRootChain(ctx context.Context, tenantID, groupID string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	chainID := uuid.NewString()
	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_chain
		   (id, tenant_id, name, version, state, is_root, chain_group_id)
		 VALUES ($1, $2, $3, 1, 'ACTIVE', TRUE, $4)`,
		chainID, tenantID, RootChainName, groupID); err != nil {
		return fmt.Errorf("insert root chain: %w", err)
	}

	switchID := uuid.NewString()
	createID := uuid.NewString()
	clearID := uuid.NewString()

	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_node (id, chain_id, type, name, config)
		 VALUES ($1, $2, 'switch', 'Temperature routing', $3),
		        ($4, $2, 'create-alarm', 'Raise high-temperature alarm', $5),
		        ($6, $2, 'clear-alarm', 'Clear high-temperature alarm', $7)`,
		switchID, chainID, rootSwitchConfig,
		createID, rootCreateAlarmConfig,
		clearID, rootClearAlarmConfig); err != nil {
		return fmt.Errorf("insert root chain nodes: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO rule_edge (id, chain_id, from_node, to_node, relation)
		 VALUES (gen_random_uuid(), $1, $2, $3, 'Hot'),
		        (gen_random_uuid(), $1, $4, $5, 'Cold')`,
		chainID, switchID, createID, switchID, clearID); err != nil {
		return fmt.Errorf("insert root chain edges: %w", err)
	}
	return tx.Commit(ctx)
}

// Provisioned root chain node configs. They must stay semantically
// identical to the seed graph in internal/store; both describe the
// default temperature alarm behavior a fresh tenant starts with.
var (
	rootSwitchConfig = `{"cases":[
	  {"label":"Hot","script":"return msg.measurement_key === 'temperature' && msg.temperature > 24;"},
	  {"label":"Cold","script":"return msg.measurement_key === 'temperature' && msg.temperature <= 24;"}
	]}`

	rootCreateAlarmConfig = `{"alarm_type":"HighTemperature","severity":"MAJOR"}`

	rootClearAlarmConfig = `{"alarm_type":"HighTemperature"}`
)

// scanTask scans one task row.
func scanTask(row pgx.Row) (Task, error) {
	var task Task
	err := row.Scan(&task.TenantID, &task.TenantName, &task.TenantStatus,
		&task.Status, &task.Attempts, &task.LastError, &task.RunAfter)
	if err != nil {
		return Task{}, err
	}
	return task, nil
}

// trimName normalizes a tenant display name.
func trimName(name string) string {
	return strings.TrimSpace(name)
}
