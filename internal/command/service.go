// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/presence"
)

// Service runs the background roles of the command package: persistent
// delivery on reconnect and the expiry sweeper.
type Service struct {
	router *Router
	store  *Store
	nc     *nats.Conn
	tune   Tuning
}

// presenceBoundary mirrors presence.Event on the Core NATS channel.
type presenceBoundary struct {
	DeviceID string `json:"device_id"`
	TenantID string `json:"tenant_id"`
	State    string `json:"state"`
}

// NewService constructs the command background service.
func NewService(nc *nats.Conn, router *Router, store *Store, tune Tuning) *Service {
	return &Service{router: router, store: store, nc: nc, tune: tune}
}

// Run starts persistent delivery and expiry sweeping until ctx is canceled.
func (s *Service) Run(ctx context.Context) error {
	sub, err := s.nc.Subscribe(presenceWildcard, func(msg *nats.Msg) {
		s.handlePresence(ctx, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("subscribe presence: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	eventSub, err := s.nc.Subscribe(eventsWildcard, func(msg *nats.Msg) {
		s.handleLifecycleEvent(ctx, msg.Data)
	})
	if err != nil {
		return fmt.Errorf("subscribe command events: %w", err)
	}
	defer func() { _ = eventSub.Unsubscribe() }()

	ticker := time.NewTicker(s.tune.SweepInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweepExpiry(ctx)
		}
	}
}

// handlePresence delivers queued persistent commands after a device reaches
// ONLINE. Commands are claimed first, making the scan safe with multiple
// router replicas.
func (s *Service) handlePresence(ctx context.Context, raw []byte) {
	var boundary presenceBoundary
	if err := json.Unmarshal(raw, &boundary); err != nil {
		slog.Warn("command presence frame decode failed", "error", err)
		return
	}
	if boundary.State != "ONLINE" {
		return
	}

	commands, err := s.store.ClaimPending(ctx,
		boundary.TenantID, boundary.DeviceID,
		s.router.instanceID, s.tune.ClaimBatchSize)
	if err != nil {
		slog.Error("claim pending commands failed", "error", err)
		return
	}

	for _, c := range commands {
		s.deliverClaimed(ctx, c)
	}
}

// deliverClaimed dispatches one claimed command and resolves its terminal
// state. A device that disappeared between the boundary and the dispatch
// returns the command to PENDING for the next ONLINE.
func (s *Service) deliverClaimed(ctx context.Context, c Command) {
	location, err := s.router.locator.Lookup(ctx, presence.Identity{
		TenantID: c.TenantID,
		DeviceID: c.DeviceID,
	})
	if err != nil {
		slog.Error("persistent delivery lookup failed", "command", c.ID, "error", err)
		_ = s.store.ReleaseClaim(ctx, c.ID)
		return
	}

	if !location.Found {
		_ = s.store.ReleaseClaim(ctx, c.ID)
		return
	}

	ack, err := s.router.send(ctx, c, location)
	if err != nil {
		slog.Error("persistent dispatch failed", "command", c.ID, "error", err)
		_ = s.store.ReleaseClaim(ctx, c.ID)
		return
	}

	if ack.Status == StatusNotConnected {
		_ = s.store.ReleaseClaim(ctx, c.ID)
		return
	}
	ack.FinishedAt = time.Now().UTC()
	if err := s.store.Complete(ctx, c.ID, ack.Status, ack.Response, ack.Error); err != nil {
		slog.Error("persistent completion failed", "command", c.ID, "error", err)
	}
	s.router.metrics.Observe(c.TenantID, ack.Status, time.Since(c.CreatedAt))
}

// handleLifecycleEvent corrects stored state when a late device response
// arrives after the synchronous call already ended.
func (s *Service) handleLifecycleEvent(ctx context.Context, raw []byte) {
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		slog.Warn("command lifecycle frame decode failed", "error", err)
		return
	}
	if event.Status != StatusSucceeded {
		return
	}
	if err := s.store.CorrectLateResult(ctx, event); err != nil {
		slog.Error("late command result correction failed", "command", event.CommandID, "error", err)
	}
}

// sweepExpiry terminates overdue commands and publishes one EXPIRED event
// per command so expirations never disappear silently.
func (s *Service) sweepExpiry(ctx context.Context) {
	now := time.Now().UTC()
	events, err := s.store.ExpireDue(ctx, now)
	if err != nil {
		slog.Error("command expiry sweep failed", "error", err)
		return
	}
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			continue
		}
		if err := s.nc.Publish(eventsSubject(event.TenantID, event.DeviceID), raw); err != nil {
			slog.Error("command event publish failed", "command", event.CommandID, "error", err)
		}
	}
}
