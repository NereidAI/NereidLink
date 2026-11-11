// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the management surface over the forwarder store. It validates
// input, keeps the bounded JetStream stream aligned with a forwarder row, and
// provides DLQ inspection and replay. It holds no receiver connections.

package egress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
)

// Validation errors for management calls.
var (
	// ErrInvalidArgument means a required field is missing or out of range.
	ErrInvalidArgument = errors.New("invalid forwarder argument")
)

// maxNameLen bounds a forwarder name, matching the column usage.
const maxNameLen = 255

// defaultMaxAttempts applies when none is supplied.
const defaultMaxAttempts = 5

// Default buffer policy; kept conservative so a recovering receiver gets
// only recent data rather than the whole outage window.
const (
	// defaultMessageTTLMS is one hour.
	defaultMessageTTLMS int64 = 3_600_000
	// defaultMaxBufferMessages bounds the held message count.
	defaultMaxBufferMessages = 1000
	// minMessageTTLMS rejects sub-second TTLs.
	minMessageTTLMS int64 = 1_000
	// maxMessageTTLMS is 30 days.
	maxMessageTTLMS int64 = 2_592_000_000
	// maxBufferMessages bounds the configured buffer.
	maxBufferMessages = 1_000_000
)

// Service manages forwarders for the REST handlers.
type Service struct {
	store    *Store
	js       jetstream.JetStream
	replicas int
}

// NewService constructs the forwarder management service. replicas is the
// JetStream Raft factor (1 for the bundled single NATS, 3/5 clustered).
func NewService(js jetstream.JetStream, store *Store, replicas int) *Service {
	if replicas < 1 {
		replicas = 1
	}
	return &Service{store: store, js: js, replicas: replicas}
}

// List returns a tenant's forwarders.
func (s *Service) List(ctx context.Context, tenantID string) ([]Forwarder, error) {
	return s.store.List(ctx, tenantID)
}

// Get loads one forwarder.
func (s *Service) Get(ctx context.Context, tenantID, id string) (Forwarder, error) {
	return s.store.Get(ctx, tenantID, id)
}

// Create validates and persists a forwarder and ensures its bounded stream.
// actor is the principal id used for audit.
func (s *Service) Create(
	ctx context.Context,
	tenantID, actor string,
	in Input,
) (Forwarder, error) {
	f, err := buildValid(tenantID, actor, in)
	if err != nil {
		return Forwarder{}, err
	}
	// Validate the sink configuration before persisting so a bad config is
	// rejected rather than stored.
	if _, err := buildSink(f); err != nil {
		return Forwarder{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}

	if err := s.store.Create(ctx, &f); err != nil {
		return Forwarder{}, err
	}
	if err := EnsureStream(ctx, s.js, f, s.replicas); err != nil {
		// Keep the row and stream consistent: undo the row when the stream
		// cannot be created; the worker retry would otherwise keep failing.
		_ = s.store.Delete(ctx, tenantID, f.ID)
		return Forwarder{}, err
	}
	return f, nil
}

// Update applies a validated edit and keeps the stream aligned.
func (s *Service) Update(
	ctx context.Context,
	tenantID, id string,
	in Input,
) (Forwarder, error) {
	existing, err := s.store.Get(ctx, tenantID, id)
	if err != nil {
		return Forwarder{}, err
	}
	// Sink type is fixed after creation: changing it would orphan the
	// existing stream semantics and stored config shape. Require the
	// caller to create a new forwarder instead.
	if in.SinkType != existing.SinkType {
		return Forwarder{}, fmt.Errorf(
			"%w: sink_type cannot be changed after creation (current: %s); create a new forwarder instead",
			ErrInvalidArgument, existing.SinkType)
	}
	if err := validateInput(in); err != nil {
		return Forwarder{}, err
	}
	f, err := s.store.Update(ctx, tenantID, id, in)
	if err != nil {
		return Forwarder{}, err
	}
	// Rebuilding the sink validates the new config; stream caps stay the
	// same, so only ensure it exists.
	if _, err := buildSink(f); err != nil {
		return Forwarder{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	if err := EnsureStream(ctx, s.js, f, s.replicas); err != nil {
		return f, err
	}
	return f, nil
}

// Delete removes the forwarder, its DLQ rows (cascade) and its stream.
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	if err := s.store.Delete(ctx, tenantID, id); err != nil {
		return err
	}
	if err := DeleteStream(ctx, s.js, tenantID, id); err != nil {
		return err
	}
	return nil
}

// ListDLQ returns a tenant's dead letters, optionally scoped to one forwarder.
func (s *Service) ListDLQ(
	ctx context.Context,
	tenantID, forwarderID string,
) ([]DeadLetter, error) {
	return s.store.ListDLQ(ctx, tenantID, forwarderID)
}

// ListDeliveries returns a tenant's delivery-log rows, optionally scoped to
// one forwarder, newest first and capped by limit.
func (s *Service) ListDeliveries(
	ctx context.Context,
	tenantID, forwarderID string,
	limit int,
) ([]Delivery, error) {
	return s.store.ListDeliveries(ctx, tenantID, forwarderID, limit)
}

// Replay re-publishes one dead-letter envelope onto the forwarder stream and
// marks the row replayed. A forwarder deleted after the DLQ row was written
// makes replay impossible.
func (s *Service) Replay(ctx context.Context, tenantID, dlqID string) error {
	rows, err := s.store.ListDLQ(ctx, tenantID, "")
	if err != nil {
		return err
	}
	var target *DeadLetter
	for i := range rows {
		if rows[i].ID == dlqID {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}
	if _, err := s.store.Get(ctx, tenantID, target.ForwarderID); err != nil {
		return fmt.Errorf("%w: forwarder for dlq row missing", ErrInvalidArgument)
	}

	raw, err := json.Marshal(target.Envelope)
	if err != nil {
		return err
	}
	if _, err := s.js.Publish(ctx,
		Subject(tenantID, target.ForwarderID), raw); err != nil {
		return err
	}
	return s.store.MarkReplayed(ctx, tenantID, dlqID)
}

// buildValid assembles a forwarder row from validated input.
func buildValid(tenantID, actor string, in Input) (Forwarder, error) {
	if err := validateInput(in); err != nil {
		return Forwarder{}, err
	}
	now := time.Now()
	attempts := in.MaxAttempts
	if attempts <= 0 {
		attempts = defaultMaxAttempts
	}
	ttl := in.MessageTTLMS
	if ttl <= 0 {
		ttl = defaultMessageTTLMS
	}
	buffer := in.MaxBufferMessages
	if buffer <= 0 {
		buffer = defaultMaxBufferMessages
	}
	return Forwarder{
		ID:                uuid.NewString(),
		TenantID:          tenantID,
		Name:              in.Name,
		SinkType:          in.SinkType,
		SinkConfig:        in.SinkConfig,
		Encoder:           encoderDefault(in.Encoder),
		MaxAttempts:       attempts,
		MessageTTLMS:      ttl,
		MaxBufferMessages: buffer,
		Enabled:           in.Enabled,
		CreatedBy:         actor,
		CreatedAt:         now,
		UpdatedAt:         now,
	}, nil
}

// validateInput checks the management write.
func validateInput(in Input) error {
	if len(in.Name) == 0 || len(in.Name) > maxNameLen {
		return fmt.Errorf("%w: name length must be 1..%d",
			ErrInvalidArgument, maxNameLen)
	}
	if in.SinkType == "" {
		return fmt.Errorf("%w: sink_type required", ErrInvalidArgument)
	}
	if in.Encoder != "" && in.Encoder != EncoderJSON {
		return fmt.Errorf("%w: unsupported encoder %q",
			ErrInvalidArgument, in.Encoder)
	}
	if in.MaxAttempts < 0 || in.MaxAttempts > 50 {
		return fmt.Errorf("%w: max_attempts must be 0..50",
			ErrInvalidArgument)
	}
	if in.MessageTTLMS != 0 &&
		(in.MessageTTLMS < minMessageTTLMS || in.MessageTTLMS > maxMessageTTLMS) {
		return fmt.Errorf("%w: message_ttl_ms must be %d..%d",
			ErrInvalidArgument, minMessageTTLMS, maxMessageTTLMS)
	}
	if in.MaxBufferMessages < 0 || in.MaxBufferMessages > maxBufferMessages {
		return fmt.Errorf("%w: max_buffer_messages must be 0..%d",
			ErrInvalidArgument, maxBufferMessages)
	}
	return nil
}
