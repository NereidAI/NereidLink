// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go implements escalation chain persistence: chain headers and
// their ordered steps are written transactionally on create and full
// replace. Read queries return steps ordered ascending.

package escalation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service owns escalation chain persistence.
type Service struct {
	pool *pgxpool.Pool
}

// NewService constructs the escalation service.
func NewService(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// CreateChain validates and persists a new chain with its steps.
func (s *Service) CreateChain(ctx context.Context, tenantID string, in SaveInput) (Chain, error) {
	steps, err := validate(in)
	if err != nil {
		return Chain{}, err
	}
	if err := s.validateGroups(ctx, tenantID, in); err != nil {
		return Chain{}, err
	}

	id := uuid.NewString()
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	repeatInterval, repeatRole, repeatGroup := normalizedRepeat(in)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Chain{}, fmt.Errorf("begin create chain: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		`INSERT INTO escalation_chain
		   (id, tenant_id, name, enabled,
		    repeat_interval_seconds, repeat_target_role, repeat_target_group_id)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		id, tenantID, strings.TrimSpace(in.Name), enabled,
		repeatInterval, repeatRole, nullableGroup(repeatGroup)); err != nil {
		return Chain{}, mapWriteError("create chain", err)
	}
	if err := insertSteps(ctx, tx, id, steps); err != nil {
		return Chain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Chain{}, fmt.Errorf("commit create chain: %w", err)
	}
	return s.GetChain(ctx, tenantID, id)
}

// UpdateChain replaces the chain header fields and its full step list.
func (s *Service) UpdateChain(ctx context.Context, tenantID, chainID string, in SaveInput) (Chain, error) {
	steps, err := validate(in)
	if err != nil {
		return Chain{}, err
	}
	if err := s.validateGroups(ctx, tenantID, in); err != nil {
		return Chain{}, err
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	repeatInterval, repeatRole, repeatGroup := normalizedRepeat(in)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Chain{}, fmt.Errorf("begin update chain: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx,
		`UPDATE escalation_chain
		 SET name=$3, enabled=$4, updated_at=now(),
		     repeat_interval_seconds=$5, repeat_target_role=$6,
		     repeat_target_group_id=$7
		 WHERE id=$1 AND tenant_id=$2`,
		chainID, tenantID, strings.TrimSpace(in.Name), enabled,
		repeatInterval, repeatRole, nullableGroup(repeatGroup))
	if err != nil {
		return Chain{}, mapWriteError("update chain", err)
	}
	if tag.RowsAffected() == 0 {
		return Chain{}, ErrNotFound
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM escalation_step WHERE chain_id=$1`, chainID); err != nil {
		return Chain{}, fmt.Errorf("clear steps: %w", err)
	}
	if err := insertSteps(ctx, tx, chainID, steps); err != nil {
		return Chain{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Chain{}, fmt.Errorf("commit update chain: %w", err)
	}
	return s.GetChain(ctx, tenantID, chainID)
}

// DeleteChain removes a chain. Alarms referencing it keep their rows with
// the link nulled by ON DELETE SET NULL.
func (s *Service) DeleteChain(ctx context.Context, tenantID, chainID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM escalation_chain WHERE id=$1 AND tenant_id=$2`,
		chainID, tenantID)
	if err != nil {
		return fmt.Errorf("delete chain: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// GetChain returns one chain including its steps.
func (s *Service) GetChain(ctx context.Context, tenantID, chainID string) (Chain, error) {
	chain, err := s.readChain(ctx,
		`SELECT id::text, tenant_id::text, name, enabled, created_at, updated_at,
		        repeat_interval_seconds, COALESCE(repeat_target_role,''),
		        COALESCE(repeat_target_group_id::text,'')
		 FROM escalation_chain WHERE id=$1 AND tenant_id=$2`,
		chainID, tenantID)
	if err != nil {
		return Chain{}, err
	}

	steps, err := s.readSteps(ctx, chain.ID)
	if err != nil {
		return Chain{}, err
	}
	chain.Steps = steps
	return chain, nil
}

// ListChains returns every chain of the tenant, ordered by name.
func (s *Service) ListChains(ctx context.Context, tenantID string) ([]Chain, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, tenant_id::text, name, enabled, created_at, updated_at,
		        repeat_interval_seconds, COALESCE(repeat_target_role,''),
		        COALESCE(repeat_target_group_id::text,'')
		 FROM escalation_chain WHERE tenant_id=$1 ORDER BY name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list chains: %w", err)
	}
	defer rows.Close()

	chains := []Chain{}
	for rows.Next() {
		chain, err := scanChain(rows)
		if err != nil {
			return nil, err
		}
		chains = append(chains, chain)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan chains: %w", err)
	}

	for i := range chains {
		steps, err := s.readSteps(ctx, chains[i].ID)
		if err != nil {
			return nil, err
		}
		chains[i].Steps = steps
	}
	return chains, nil
}

// validate normalizes and validates save input, returning steps with
// orders derived from slice position.
func validate(in SaveInput) ([]StepInput, error) {
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalid)
	}
	if len(in.Name) > 200 {
		return nil, fmt.Errorf("%w: name too long", ErrInvalid)
	}
	if len(in.Steps) == 0 {
		return nil, fmt.Errorf("%w: at least one step is required", ErrInvalid)
	}
	for _, step := range in.Steps {
		if step.DelaySeconds < 0 {
			return nil, fmt.Errorf("%w: delay must be non-negative", ErrInvalid)
		}
		if !allowedRoles[step.TargetRole] {
			return nil, fmt.Errorf("%w: unknown target role %q", ErrInvalid, step.TargetRole)
		}
	}
	if in.RepeatIntervalSeconds < 0 {
		return nil, fmt.Errorf("%w: repeat interval must be non-negative", ErrInvalid)
	}
	role := strings.TrimSpace(in.RepeatTargetRole)
	if in.RepeatIntervalSeconds > 0 {
		// A cadence without a target role would silently do nothing.
		if !allowedRoles[role] {
			return nil, fmt.Errorf("%w: repeat target role is required and must be a tenant role", ErrInvalid)
		}
	}
	return in.Steps, nil
}

// normalizedRepeat returns the validated repeat interval/role/group
// triple. A zero interval clears the role and group so the stored
// representation never carries an orphaned target.
func normalizedRepeat(in SaveInput) (int64, string, string) {
	if in.RepeatIntervalSeconds <= 0 {
		return 0, "", ""
	}
	return in.RepeatIntervalSeconds, strings.TrimSpace(in.RepeatTargetRole),
		strings.TrimSpace(in.RepeatTargetGroupID)
}

// nullableGroup turns an empty group id into a NULL bind value so it can
// be written into a nullable UUID column without a cast error.
func nullableGroup(groupID string) any {
	if strings.TrimSpace(groupID) == "" {
		return nil
	}
	return groupID
}

// validateGroups rejects chain targets that reference a device group
// missing from the tenant's own group tree. A step or repeat target may
// name any of the tenant's groups; cross-tenant or dangling ids must fail
// closed with a 400 instead of silently widening the recipient set.
func (s *Service) validateGroups(ctx context.Context, tenantID string, in SaveInput) error {
	ids := []string{}
	for _, step := range in.Steps {
		if id := strings.TrimSpace(step.TargetGroupID); id != "" {
			ids = append(ids, id)
		}
	}
	if id := strings.TrimSpace(in.RepeatTargetGroupID); id != "" {
		ids = append(ids, id)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		var owned bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM device_group WHERE id=$1 AND tenant_id=$2)`,
			id, tenantID).Scan(&owned); err != nil {
			return fmt.Errorf("check escalation group: %w", err)
		}
		if !owned {
			return fmt.Errorf("%w: target device group %s does not belong to this tenant",
				ErrInvalid, id)
		}
	}
	return nil
}

// insertSteps persists steps for a chain inside an open transaction.
func insertSteps(ctx context.Context, tx pgx.Tx, chainID string, steps []StepInput) error {
	for order, step := range steps {
		if _, err := tx.Exec(ctx,
			`INSERT INTO escalation_step
			   (id, chain_id, step_order, delay_seconds, target_role,
			    target_group_id, stop_on_ack, stop_on_clear)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			uuid.NewString(), chainID, order, step.DelaySeconds,
			step.TargetRole, nullableGroup(step.TargetGroupID),
			step.StopOnAck, step.StopOnClear); err != nil {
			return fmt.Errorf("insert step: %w", err)
		}
	}
	return nil
}

// rowScanner abstracts pgx Row and Rows scanning.
type rowScanner interface {
	Scan(dest ...any) error
}

// readChain runs a header query and scans one row.
func (s *Service) readChain(ctx context.Context, query string, args ...any) (Chain, error) {
	chain, err := scanChain(s.pool.QueryRow(ctx, query, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return Chain{}, ErrNotFound
	}
	return chain, err
}

// scanChain scans a chain header row and formats timestamps.
func scanChain(scanner rowScanner) (Chain, error) {
	var chain Chain
	var createdAt, updatedAt time.Time
	var repeatRole, repeatGroup *string
	if err := scanner.Scan(&chain.ID, &chain.TenantID, &chain.Name,
		&chain.Enabled, &createdAt, &updatedAt,
		&chain.RepeatIntervalSeconds, &repeatRole, &repeatGroup); err != nil {
		return Chain{}, fmt.Errorf("scan chain: %w", err)
	}
	if repeatRole != nil {
		chain.RepeatTargetRole = *repeatRole
	}
	if repeatGroup != nil {
		chain.RepeatTargetGroupID = *repeatGroup
	}
	chain.Steps = []Step{}
	chain.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	chain.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return chain, nil
}

// readSteps returns the ordered steps of a chain.
func (s *Service) readSteps(ctx context.Context, chainID string) ([]Step, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, step_order, delay_seconds, target_role,
		        COALESCE(target_group_id::text,''),
		        stop_on_ack, stop_on_clear
		 FROM escalation_step WHERE chain_id=$1 ORDER BY step_order`,
		chainID)
	if err != nil {
		return nil, fmt.Errorf("list steps: %w", err)
	}
	defer rows.Close()

	steps := []Step{}
	for rows.Next() {
		var step Step
		if err := rows.Scan(&step.ID, &step.Order, &step.DelaySeconds,
			&step.TargetRole, &step.TargetGroupID,
			&step.StopOnAck, &step.StopOnClear); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan steps: %w", err)
	}
	return steps, nil
}

// mapWriteError turns a unique-violation on (tenant_id,name) into a
// conflict error suitable for the REST layer; callers map this to 409.
func mapWriteError(action string, err error) error {
	if strings.Contains(err.Error(), "escalation_chain_tenant_name_idx") {
		return fmt.Errorf("%w: chain name already exists", ErrInvalid)
	}
	return fmt.Errorf("%s: %w", action, err)
}
