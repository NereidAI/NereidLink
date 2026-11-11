// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// executor.go binds approved actions to concrete handlers. Approval
// runs the registered handler immediately (ADR-0023: the reviewed
// order is what executes); a handler failure leaves the order APPROVED
// so execution can be retried explicitly without a second review.

package opscenter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// ActionExecutor runs the action of one approved order and returns a
// JSON object result for the exec_result ledger.
type ActionExecutor func(ctx context.Context, order Order) (json.RawMessage, error)

// SetExecutors registers the action handler map in the service.
func (s *Service) SetExecutors(executors map[string]ActionExecutor) {
	s.executors = executors
}

// Execute retries the handler of an already-approved order and closes
// the order on success.
func (s *Service) Execute(ctx context.Context, orderID string) (Order, error) {
	order, err := s.Get(ctx, orderID)
	if err != nil {
		return Order{}, err
	}
	if order.Status != StatusApproved {
		return Order{}, ErrNotApproved
	}
	result, err := s.runExecutor(ctx, order)
	if err != nil {
		return order, err
	}
	return s.MarkExecuted(ctx, order.ID, result)
}

// runExecutor looks up and invokes the handler, enforcing its result
// shape; an unregistered action is a configuration error.
func (s *Service) runExecutor(ctx context.Context, order Order) (json.RawMessage, error) {
	if s.executors == nil {
		return nil, fmt.Errorf("%w: no executors configured", ErrInvalidArgument)
	}
	handler, ok := s.executors[order.Action]
	if !ok {
		return nil, fmt.Errorf("%w: no executor for action %s", ErrInvalidArgument, order.Action)
	}
	result, err := handler(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("run %s: %w", order.Action, err)
	}
	if !jsonObject(result) {
		return nil, fmt.Errorf("%w: executor result must be a JSON object", ErrInvalidArgument)
	}
	return result, nil
}

// afterApproval executes immediately following a successful approval.
// A missing executor is acceptable (the order stays approved for a
// later explicit Execute call); handler errors are returned while the
// order remains APPROVED.
func (s *Service) afterApproval(ctx context.Context, order Order) (Order, error) {
	if s.executors == nil {
		return order, nil
	}
	if _, ok := s.executors[order.Action]; !ok {
		return order, nil
	}
	result, err := s.runExecutor(ctx, order)
	if err != nil {
		return order, err
	}
	return s.MarkExecuted(ctx, order.ID, result)
}

// RunSweeper periodically expires stale pending orders until ctx is
// cancelled. Sweep errors are logged by the caller's logger choice via
// the returned terminal error; transient failures only stop the loop
// on context cancellation.
func (s *Service) RunSweeper(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if _, err := s.ExpireStale(ctx); err != nil {
				if errors.Is(err, context.Canceled) {
					return err
				}
				slog.Error("ops order expiry sweep failed", "error", err)
			}
		}
	}
}
