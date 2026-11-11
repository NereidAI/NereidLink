// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates one gateway operation invocation: catalog lookup,
// persistence, presence locating, synchronous Core NATS dispatch, audit and
// the 90-day retention sweep.

package opinvoke

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/opcatalog"
	"github.com/nereidai/nereidlink/internal/presence"
)

// taskStore is the persistence surface Service needs.
type taskStore interface {
	Insert(ctx context.Context, t Task, startedAt time.Time) error
	Complete(ctx context.Context, id, status string, result []byte, reason string, finishedAt time.Time) error
	Get(ctx context.Context, tenantID, id string) (Task, error)
	ListByGateway(ctx context.Context, tenantID, gatewayID string, limit int) ([]Task, error)
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// CatalogLookup returns the descriptor one specific gateway advertised for
// an operation (with tenant-private fallback) and validates invocation params
// against it. *opcatalog.Service satisfies it.
type CatalogLookup interface {
	EffectiveForGateway(ctx context.Context, tenantID, gatewayID, operation string) (opcatalog.View, bool, error)
	ValidateParams(schema json.RawMessage, params map[string]any) error
}

// GatewayLocator resolves the live location of the gateway. *presence.Service
// satisfies it.
type GatewayLocator interface {
	Lookup(ctx context.Context, id presence.Identity) (presence.Location, error)
}

// oneWayAwait bounds the wait for an ingress write acknowledgment of a
// one-way operation (no edge result exists).
const oneWayAwait = 10 * time.Second

// requester is the request/reply transport seam so the service is testable
// without a live NATS server. natsRequester (wrapping *nats.Conn) satisfies
// it.
type requester interface {
	// RequestMsg performs one Core NATS request with context deadline.
	RequestMsg(ctx context.Context, subject string, data []byte) ([]byte, error)
}

// natsRequester adapts *nats.Conn.
type natsRequester struct {
	nc *nats.Conn
}

// RequestMsg implements requester.
func (n natsRequester) RequestMsg(ctx context.Context, subject string, data []byte) ([]byte, error) {
	msg, err := n.nc.RequestMsgWithContext(ctx, &nats.Msg{
		Subject: subject,
		Data:    data,
	})
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

// Service executes gateway operations.
type Service struct {
	req     requester
	store   taskStore
	catalog CatalogLookup
	locator GatewayLocator

	// audit is the permanent record; nil is tolerated in unit tests.
	audit *audit.Service
}

// New constructs an invocation Service.
func New(nc *nats.Conn, store taskStore, catalog CatalogLookup, locator GatewayLocator) *Service {
	return &Service{
		req:     natsRequester{nc: nc},
		store:   store,
		catalog: catalog,
		locator: locator,
	}
}

// SetAuditService wires the chained audit log.
func (s *Service) SetAuditService(service *audit.Service) {
	s.audit = service
}

// Invoke executes one operation synchronously and returns the stored task.
// Risk gating (confirmations, dangerous-action step-up) is the caller's
// responsibility; Invoke only trusts the server-side catalog.
func (s *Service) Invoke(ctx context.Context, in InvokeInput) (Task, error) {
	if in.Actor.UserID == "" {
		return Task{}, errors.New("invoking actor is required")
	}
	view, ok, err := s.catalog.EffectiveForGateway(
		ctx, in.TenantID, in.GatewayID, in.Operation)
	if err != nil {
		return Task{}, err
	}
	if !ok {
		return Task{}, ErrOperationNotFound
	}
	if view.Disabled {
		return Task{}, ErrOperationDisabled
	}
	params := in.Params
	if params == nil {
		params = map[string]any{}
	}
	rawParams, err := json.Marshal(params)
	if err != nil {
		return Task{}, fmt.Errorf("encode params: %w", err)
	}
	if len(rawParams) > MaxParamsBytes {
		return Task{}, ErrParamsTooLarge
	}
	// Enforce the gateway-advertised param schema at the platform boundary;
	// a console that skips validation cannot dispatch malformed params.
	if err := s.catalog.ValidateParams(view.ParamSchema, params); err != nil {
		return Task{}, fmt.Errorf("%w: %v", ErrParamsSchema, err)
	}

	// The per-call override may only lower the descriptor default; both are
	// clamped into the 100ms..60s envelope.
	timeoutMs := clampTimeout(view.TimeoutMs)
	if in.TimeoutMs > 0 {
		if requested := clampTimeout(in.TimeoutMs); requested < timeoutMs {
			timeoutMs = requested
		}
	}

	task := Task{
		ID:             newUUID(),
		TenantID:       in.TenantID,
		GatewayID:      in.GatewayID,
		Operation:      in.Operation,
		TypeVersion:    view.TypeVersion,
		Params:         params,
		Status:         StatusPending,
		Risk:           view.Risk,
		InvokedBy:      in.Actor.UserID,
		InvokedByEmail: in.Actor.Email,
	}
	startedAt := time.Now().UTC()
	task.StartedAt = formatUTC(startedAt)
	if err := s.store.Insert(ctx, task, startedAt); err != nil {
		return Task{}, err
	}

	location, err := s.locator.Lookup(ctx, presence.Identity{
		TenantID: in.TenantID, DeviceID: in.GatewayID,
	})
	if err != nil {
		return s.fail(ctx, task, fmt.Errorf("presence lookup: %w", err))
	}
	if !location.Found {
		// Immediate, terminal, never queued.
		return s.finish(ctx, task, StatusNotConnected, nil, "gateway not connected")
	}

	frame := InvokeFrame{
		ID:          task.ID,
		TenantID:    in.TenantID,
		GatewayID:   in.GatewayID,
		Operation:   in.Operation,
		TypeVersion: view.TypeVersion,
		Params:      params,
		TimeoutMs:   timeoutMs,
		OneWay:      view.Interaction == opcatalog.InteractionOneWay,
	}
	wait := time.Duration(frame.TimeoutMs) * time.Millisecond
	if frame.OneWay {
		wait = oneWayAwait
	}

	result, status, reason := s.dispatch(ctx, location.IngressID, frame, wait)
	return s.finish(ctx, task, status, result, reason)
}

// dispatch publishes the request to the owning ingress and decodes the
// reply. Transport-level failures map to TIMEOUT/FAILED; the reply never
// returns an error so the task always reaches a terminal state.
func (s *Service) dispatch(ctx context.Context, ingressID string, frame InvokeFrame, wait time.Duration) (raw []byte, status, reason string) {
	data, err := json.Marshal(frame)
	if err != nil {
		return nil, StatusFailed, "encode invoke frame: " + err.Error()
	}
	reqCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	reply, err := s.req.RequestMsg(reqCtx, DispatchSubjectPrefix+ingressID, data)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) ||
			errors.Is(err, nats.ErrTimeout) {
			return nil, StatusTimeout, "response timeout"
		}
		if errors.Is(err, nats.ErrNoResponders) {
			return nil, StatusNotConnected, "ingress unavailable"
		}
		return nil, StatusFailed, err.Error()
	}

	var result ResultFrame
	if err := json.Unmarshal(reply, &result); err != nil {
		return nil, StatusFailed, "invalid result frame"
	}
	if len(result.Result) > MaxResultBytes {
		return nil, StatusFailed, "operation result exceeds 64KB"
	}
	if !result.OK {
		return nil, StatusFailed, result.Error
	}
	return result.Result, StatusSucceeded, ""
}

// fail terminalizes a task after an internal error and returns the task
// plus the same error so handlers can map it while the row stays auditable.
func (s *Service) fail(ctx context.Context, task Task, cause error) (Task, error) {
	task, _ = s.finish(ctx, task, StatusFailed, nil, cause.Error())
	return task, cause
}

// finish persists the terminal state, audits and returns the stored task.
func (s *Service) finish(ctx context.Context, task Task, status string, result []byte, reason string) (Task, error) {
	finishedAt := time.Now().UTC()
	if err := s.store.Complete(ctx, task.ID, status, result, reason, finishedAt); err != nil {
		slog.Error("persist operation task completion failed",
			"task", task.ID, "error", err)
	}
	task.Status = status
	task.Reason = reason
	task.Result = result
	task.FinishedAt = formatUTC(finishedAt)
	s.writeAudit(ctx, task)
	return task, nil
}

// writeAudit appends the permanent chained event: actor, gateway, operation,
// risk and outcome. Params and result content are deliberately excluded.
func (s *Service) writeAudit(ctx context.Context, task Task) {
	if s.audit == nil {
		return
	}
	_, err := s.audit.Append(ctx, audit.Input{
		TenantID:     task.TenantID,
		ActorID:      task.InvokedBy,
		ActorName:    task.InvokedByEmail,
		EventType:    audit.EventGatewayOperation,
		Action:       "invoke",
		ResourceType: "gateway",
		ResourceID:   task.GatewayID,
		Details: map[string]any{
			"operation": task.Operation,
			"risk":      task.Risk,
			"task_id":   task.ID,
			"status":    task.Status,
		},
	})
	if err != nil {
		slog.Error("failed to write gateway operation audit event",
			"task", task.ID, "error", err)
	}
}

// Tasks returns one gateway's task history.
func (s *Service) Tasks(ctx context.Context, tenantID, gatewayID string, limit int) ([]Task, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	return s.store.ListByGateway(ctx, tenantID, gatewayID, limit)
}

// GetTask loads one task.
func (s *Service) GetTask(ctx context.Context, tenantID, id string) (Task, error) {
	return s.store.Get(ctx, tenantID, id)
}

// clampTimeout maps a timeout into the hard 100ms..60s range. A non-positive
// value means "no override" and resolves to the 60s cap; the descriptor
// default is applied by the caller before this sees it.
func clampTimeout(timeoutMs int) int {
	if timeoutMs <= 0 {
		return MaxWaitMs
	}
	if timeoutMs > MaxWaitMs {
		return MaxWaitMs
	}
	if timeoutMs < MinWaitMs {
		return MinWaitMs
	}
	return timeoutMs
}

// Sweep deletes terminal tasks past the retention window.
func (s *Service) Sweep(ctx context.Context) error {
	removed, err := s.store.DeleteOlderThan(ctx, time.Now().UTC().Add(-Retention))
	if err != nil {
		return err
	}
	if removed > 0 {
		slog.Info("expired gateway operation tasks removed", "count", removed)
	}
	return nil
}

// Run periodically sweeps old tasks until ctx is canceled.
func (s *Service) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Sweep(ctx); err != nil {
				slog.Error("failed to sweep operation tasks", "error", err)
			}
		}
	}
}

// newUUID renders random bytes as a version-4 UUID.
func newUUID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("generate uuid: %v", err))
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := fmt.Sprintf("%x", raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
