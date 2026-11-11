// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package usermgmt

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"
)

// SubjectEventPrefix is the Core NATS root for tenant member
// administration change notifications consumed by the console WebSocket
// hub. Layout is "nl.member.event.<tenant>" (tenant on token 4), mirroring
// the connector/secret event planes rather than the JetStream subjects.
const SubjectEventPrefix = "nl.member.event"

// EventWildcard matches every member change event subject.
const EventWildcard = "nl.member.event.>"

// Member lifecycle event kinds carried on SubjectEventPrefix. They are
// change notifications only: an open console re-fetches the tenant's
// member list rather than patching state from the frame.
const (
	// EventCreated signals a new member account was opened.
	EventCreated = "created"

	// EventRoles signals a member's role set was replaced.
	EventRoles = "roles"

	// EventDataScope signals a member's data scope was replaced.
	EventDataScope = "data_scope"

	// EventEnabled signals a disabled member was re-enabled.
	EventEnabled = "enabled"

	// EventDisabled signals a member was disabled.
	EventDisabled = "disabled"

	// EventRemoved signals a member account was removed.
	EventRemoved = "removed"
)

// EventFrame is one tenant member administration change notification.
type EventFrame struct {
	// Kind is one of the Event* constants.
	Kind string `json:"kind"`

	// TenantID scopes the frame; it is also encoded in the NATS subject
	// and must never be trusted from the payload for fanout boundaries.
	TenantID string `json:"tenant_id"`

	// UserID identifies the affected member account.
	UserID string `json:"user_id"`

	// ActorID identifies the platform operator who made the change.
	ActorID string `json:"actor_id,omitempty"`

	// At is the event time as RFC3339 UTC.
	At string `json:"at"`
}

// SetEventBus wires the Core NATS connection used for change
// notifications. It stays optional: without a bus the service is fully
// functional and member administration simply emits no frames.
func (s *Service) SetEventBus(nc *nats.Conn) {
	s.nc = nc
}

// publishEvent emits a best-effort member change notification so that an
// open console can refresh the tenant's member list. A failure is logged
// and never affects the administration path: the frame is a notification,
// not part of the state machine, and the console also re-reads on mount.
func (s *Service) publishEvent(ctx context.Context, kind, tenantID, userID string) {
	if s.nc == nil {
		return
	}
	frame := EventFrame{
		Kind:     kind,
		TenantID: tenantID,
		UserID:   userID,
		ActorID:  actorFromContext(ctx),
		At:       time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal member event failed", "error", err)
		return
	}
	subject := fmt.Sprintf("%s.%s", SubjectEventPrefix, tenantID)
	if err := s.nc.Publish(subject, data); err != nil {
		slog.Error("publish member event failed", "error", err, "subject", subject)
	}
}
