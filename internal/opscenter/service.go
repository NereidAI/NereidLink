// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the transactional core of dual control: order
// creation, the review decisions with cross-identity/cross-channel
// enforcement, initiator cancellation, one-time execution marking and
// the expiry sweep. Every transition appends a HIGH_RISK_OPERATION
// audit event on a best-effort basis.

package opscenter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/audit"
)

var (
	// ErrNotFound means the order does not exist.
	ErrNotFound = errors.New("ops order not found")

	// ErrInvalidArgument means a request field is invalid.
	ErrInvalidArgument = errors.New("invalid ops order argument")

	// ErrNotPending means the order left the pending state already.
	ErrNotPending = errors.New("ops order is not pending")

	// ErrOrderExpired means the validity window has elapsed.
	ErrOrderExpired = errors.New("ops order expired")

	// ErrSameChannel blocks review within the same channel; the
	// super administrator reviewing on the web must confirm an order
	// the operator opened from the CLI.
	ErrSameChannel = errors.New("review channel must differ from initiator channel")

	// ErrNotInitiator blocks cancellation by another operator.
	ErrNotInitiator = errors.New("only the initiator can cancel")

	// ErrNotApproved blocks execution without an approval.
	ErrNotApproved = errors.New("ops order is not approved")
)

// defaultTTL matches the ADR-0023 standard validity window.
const defaultTTL = 24 * time.Hour

// maxTTL bounds how long an unreviewed order may wait.
const maxTTL = 72 * time.Hour

// orderColumns lists every persisted field in scan order.
const orderColumns = `
	id::text, tenant_id::text, action, summary, payload::text, content_hash,
	initiator_id::text, initiator_name, initiator_channel, status,
	reviewer_id::text, reviewer_name, reviewer_channel, review_reason,
	reviewed_at, executed_at, exec_result::text, expires_at, created_at`

// Auditor appends tamper-evident events. *audit.Service satisfies it.
type Auditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// Service manages operation orders.
type Service struct {
	pool      *pgxpool.Pool
	auditor   Auditor
	executors map[string]ActionExecutor
	now       func() time.Time
}

// New constructs the ops center service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, now: time.Now}
}

// SetAuditor wires the audit collaborator.
func (s *Service) SetAuditor(a Auditor) {
	s.auditor = a
}

// Reviewer carries the deciding identity and its channel.
type Reviewer struct {
	// ID is the reviewer account id.
	ID string
	// Name is the reviewer email.
	Name string
	// Channel must differ from the initiator channel.
	Channel string
	// Reason explains the decision; required for rejection.
	Reason string
}

// Create opens a pending operation order after validating the action,
// channel and payload and binding the content hash.
func (s *Service) Create(ctx context.Context, input CreateOrderInput) (Order, error) {
	if !allowedActions[input.Action] {
		return Order{}, invalid("unsupported action " + input.Action)
	}
	// Orders can only be opened from the CLI; the web side confirms.
	if input.InitiatorChannel != ChannelCLI {
		return Order{}, invalid("operation orders must be opened from the CLI")
	}
	if input.InitiatorName == "" {
		return Order{}, invalid("operator name is required")
	}
	hash, canonical, err := contentHash(input.TenantID, input.Action, input.Payload)
	if err != nil {
		return Order{}, invalid(err.Error())
	}
	ttl := input.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < 0 || ttl > maxTTL {
		return Order{}, invalid("ttl must be between 0 and 72h")
	}
	expiresAt := s.now().UTC().Add(ttl)

	var id string
	var createdAt time.Time
	err = s.pool.QueryRow(ctx,
		`INSERT INTO ops_order
		   (tenant_id, action, summary, payload, content_hash,
		    initiator_id, initiator_name, initiator_channel, expires_at)
		 VALUES (NULLIF($1,'')::uuid, $2, $3, $4::jsonb, $5,
		         NULLIF($6,'')::uuid, $7, $8, $9)
		 RETURNING id::text, created_at`,
		input.TenantID, input.Action, input.Summary, []byte(canonical), hash,
		input.InitiatorID, input.InitiatorName, input.InitiatorChannel,
		expiresAt).Scan(&id, &createdAt)
	if err != nil {
		return Order{}, fmt.Errorf("create ops order: %w", err)
	}

	order, err := s.Get(ctx, id)
	if err != nil {
		return Order{}, err
	}
	s.record(ctx, audit.Input{
		TenantID:     input.TenantID,
		ActorID:      input.InitiatorID,
		ActorName:    input.InitiatorName,
		EventType:    audit.EventHighRisk,
		Action:       "order-create",
		ResourceType: "ops_order",
		ResourceID:   id,
		Details: map[string]any{
			"action": input.Action,
		},
	})
	return order, nil
}

// Approve enforces dual control and moves a pending order to APPROVED.
func (s *Service) Approve(ctx context.Context, orderID string, reviewer Reviewer) (Order, error) {
	order, err := s.loadPending(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if err := validateReviewer(order, reviewer); err != nil {
		return Order{}, err
	}
	approved, err := s.decide(ctx, order, StatusApproved, reviewer)
	if err != nil {
		return Order{}, err
	}
	return s.afterApproval(ctx, approved)
}

// Reject enforces the same dual-control checks and records the reason.
func (s *Service) Reject(ctx context.Context, orderID string, reviewer Reviewer) (Order, error) {
	if reviewer.Reason == "" {
		return Order{}, invalid("rejection reason is required")
	}
	order, err := s.loadPending(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if err := validateReviewer(order, reviewer); err != nil {
		return Order{}, err
	}
	return s.decide(ctx, order, StatusRejected, reviewer)
}

// Cancel lets the initiating operator withdraw a still-pending order.
func (s *Service) Cancel(ctx context.Context, orderID, operatorName string) (Order, error) {
	order, err := s.loadPending(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if order.InitiatorName != operatorName {
		return Order{}, ErrNotInitiator
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE ops_order SET status = $2
		 WHERE id = $1 AND status = $3`,
		order.ID, StatusCancelled, StatusPending); err != nil {
		return Order{}, fmt.Errorf("cancel order: %w", err)
	}
	s.record(ctx, audit.Input{
		TenantID:     order.TenantID,
		ActorID:      order.InitiatorID,
		ActorName:    operatorName,
		EventType:    audit.EventHighRisk,
		Action:       "order-cancel",
		ResourceType: "ops_order",
		ResourceID:   order.ID,
	})
	return s.Get(ctx, orderID)
}

// MarkExecuted records that an approved action has run and closes the
// order; the transition is one-way.
func (s *Service) MarkExecuted(ctx context.Context, orderID string, result []byte) (Order, error) {
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if order.Status != StatusApproved {
		return Order{}, ErrNotApproved
	}
	if len(result) == 0 {
		result = []byte(`{}`)
	}
	if !jsonObject(result) {
		return Order{}, invalid("execution result must be a JSON object")
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE ops_order
		 SET status = $2, executed_at = now(), exec_result = $3::jsonb
		 WHERE id = $1 AND status = $4`,
		order.ID, StatusExecuted, result, StatusApproved)
	if err != nil {
		return Order{}, fmt.Errorf("mark executed: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Order{}, ErrNotApproved
	}
	s.record(ctx, audit.Input{
		TenantID:     order.TenantID,
		EventType:    audit.EventHighRisk,
		Action:       "order-executed",
		ResourceType: "ops_order",
		ResourceID:   order.ID,
	})
	return s.Get(ctx, orderID)
}

// ExpireStale marks every pending order past its validity window and
// returns the number expired. It is invoked by the background sweeper
// and lazily during review.
func (s *Service) ExpireStale(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE ops_order SET status = $2
		 WHERE status = $1 AND expires_at < now()`,
		StatusPending, StatusExpired)
	if err != nil {
		return 0, fmt.Errorf("expire stale orders: %w", err)
	}
	count := tag.RowsAffected()
	if count > 0 {
		s.record(ctx, audit.Input{
			EventType:    audit.EventHighRisk,
			Action:       "order-expire-sweep",
			ResourceType: "ops_order",
			Details:      map[string]any{"count": count},
		})
	}
	return count, nil
}

// Get loads one order.
func (s *Service) Get(ctx context.Context, orderID string) (Order, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+orderColumns+` FROM ops_order WHERE id = $1`, orderID)
	order, err := scanOrder(row)
	if errors.Is(err, ErrNotFound) {
		return Order{}, ErrNotFound
	}
	return order, err
}

// ListFilter narrows an order listing.
type ListFilter struct {
	// Status keeps every state when empty.
	Status string
	// TenantID keeps all tenants when empty.
	TenantID string
}

// List returns orders matching the filter, newest first.
func (s *Service) List(ctx context.Context, filter ListFilter) ([]Order, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+orderColumns+` FROM ops_order
		 WHERE ($1 = '' OR status = $1)
		   AND ($2 = '' OR tenant_id = NULLIF($2,'')::uuid)
		 ORDER BY created_at DESC`,
		filter.Status, filter.TenantID)
	if err != nil {
		return nil, fmt.Errorf("list orders: %w", err)
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		order, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

// loadPending returns a pending order after lazily expiring stale rows
// and checking the individual order's window.
func (s *Service) loadPending(ctx context.Context, orderID string) (Order, error) {
	if _, err := s.ExpireStale(ctx); err != nil {
		return Order{}, err
	}
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if order.Status == StatusExpired {
		return Order{}, ErrOrderExpired
	}
	if order.Status != StatusPending {
		return Order{}, ErrNotPending
	}
	expiresAt, err := time.Parse(time.RFC3339, order.ExpiresAt)
	if err == nil && s.now().UTC().After(expiresAt) {
		if _, err := s.pool.Exec(ctx,
			`UPDATE ops_order SET status = $2
			 WHERE id = $1 AND status = $3`,
			order.ID, StatusExpired, StatusPending); err != nil {
			return Order{}, fmt.Errorf("expire order: %w", err)
		}
		return Order{}, ErrOrderExpired
	}
	return order, nil
}

// validateReviewer enforces cross-channel confirmation: the super
// administrator on the web confirms an order opened from the CLI.
func validateReviewer(order Order, reviewer Reviewer) error {
	if reviewer.ID == "" || reviewer.Name == "" {
		return invalid("reviewer identity is required")
	}
	if reviewer.Channel != ChannelWeb && reviewer.Channel != ChannelCLI {
		return invalid("reviewer channel must be WEB or CLI")
	}
	// Ordering matters: same-channel is the precise cross-channel
	// invariant and must surface before the web-only rule below.
	if reviewer.Channel == order.InitiatorChannel {
		return ErrSameChannel
	}
	if reviewer.Channel != ChannelWeb {
		return invalid("orders can only be confirmed on the web")
	}
	return nil
}

// decide writes the review decision and returns the updated order.
func (s *Service) decide(ctx context.Context, order Order, status string, reviewer Reviewer) (Order, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE ops_order
		 SET status = $2, reviewer_id = $3, reviewer_name = $4,
		     reviewer_channel = $5, review_reason = $6, reviewed_at = now()
		 WHERE id = $1 AND status = $7`,
		order.ID, status, reviewer.ID, reviewer.Name, reviewer.Channel,
		reviewer.Reason, StatusPending)
	if err != nil {
		return Order{}, fmt.Errorf("decide order: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Order{}, ErrNotPending
	}
	action := "order-approve"
	if status == StatusRejected {
		action = "order-reject"
	}
	s.record(ctx, audit.Input{
		TenantID:     order.TenantID,
		ActorID:      reviewer.ID,
		ActorName:    reviewer.Name,
		EventType:    audit.EventHighRisk,
		Action:       action,
		ResourceType: "ops_order",
		ResourceID:   order.ID,
		Details: map[string]any{
			"reason": reviewer.Reason,
		},
	})
	return s.Get(ctx, order.ID)
}

// record appends a best-effort audit event; audit failure never blocks
// the operation but is surfaced as an error log.
func (s *Service) record(ctx context.Context, input audit.Input) {
	if s.auditor == nil {
		return
	}
	if _, err := s.auditor.Append(ctx, input); err != nil {
		slog.Error("record ops order audit event failed",
			"resource", input.ResourceID, "action", input.Action, "error", err)
	}
}

// invalid wraps a validation message.
func invalid(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidArgument, message)
}
