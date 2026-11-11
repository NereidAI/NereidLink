// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_forward.go implements the Forward node. It hands the current message
// to the egress publisher for one configured forwarder and passes the
// message onward on Success. Publishing is fire-and-forget, so a failed or
// dropped publish is logged but never fails the rule chain or backpressures
// the telemetry path (ADR-0018).

package rule

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// ForwardInput is the envelope passed to the egress publisher. It mirrors
// Msg so the rule package does not depend on the egress package.
type ForwardInput struct {
	// TenantID owns the originating device.
	TenantID string
	// DeviceID is the originating device.
	DeviceID string
	// Ts is the event timestamp in milliseconds.
	Ts int64
	// TraceID joins traces across the chain and the forward.
	TraceID string
	// Channel is the business envelope metadata selecting the northbound stream.
	Channel string
	// Data is the message body.
	Data map[string]any
	// Metadata carries read-only transport context.
	Metadata map[string]any
}

// EgressPublisher enqueues a message for one forwarder.
type EgressPublisher interface {
	// PublishForward publishes the envelope onto the forwarder's stream.
	PublishForward(ctx context.Context, forwarderID string, in ForwardInput) error
}

// forwardConfig is the Forward node configuration.
type forwardConfig struct {
	// ForwarderID identifies the target forwarder.
	ForwarderID string `json:"forwarder_id"`
	// Channel is business envelope metadata; the forwarder still selects the sink.
	Channel string `json:"channel,omitempty"`

	// Fields optionally whitelists top-level or dot-path Data fields. Selected
	// dot paths retain their nested object shape. Empty preserves the full Data
	// map, including embeddings and fields not known to the rule engine.
	Fields []string `json:"fields"`
}

// validate rejects ambiguous whitelist paths before a graph can execute.
func (c forwardConfig) validate() error {
	for _, field := range c.Fields {
		if err := validateDataPath(field); err != nil {
			return fmt.Errorf("field %q: %w", field, err)
		}
	}
	return nil
}

// forwardNode hands the message to one forwarder.
type forwardNode struct {
	id        string
	config    forwardConfig
	publisher EgressPublisher
}

func (n *forwardNode) ID() string   { return n.id }
func (n *forwardNode) Type() string { return NodeTypeForward }

// Execute publishes the current message to the configured forwarder and
// continues on Success regardless of publish outcome.
func (n *forwardNode) Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error) {
	if !dryRun && n.config.ForwarderID != "" && n.publisher != nil {
		err := n.publisher.PublishForward(ctx, n.config.ForwarderID, ForwardInput{
			TenantID: in.TenantID,
			DeviceID: in.DeviceID,
			Ts:       in.Ts,
			TraceID:  in.TraceID,
			Channel:  n.config.Channel,
			Data:     selectForwardData(in.Data, n.config.Fields),
			Metadata: in.Metadata,
		})
		if err != nil {
			// Async by design: a dropped publish must not break the chain.
			slog.Warn("forward node publish dropped",
				"forwarder", n.config.ForwarderID, "error", err)
		}
	}
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}

// selectForwardData preserves nested dot paths and omits missing selections.
// A selected parent retains its complete value; the no-whitelist path returns
// the original map so generic forwarding never implicitly drops embeddings.
func selectForwardData(data map[string]any, fields []string) map[string]any {
	if len(fields) == 0 {
		return data
	}
	selected := make(map[string]any, len(fields))
	for _, field := range fields {
		value, exists := resolveDataPath(data, field)
		if !exists {
			continue
		}
		segments := strings.Split(field, ".")
		current := selected
		for _, segment := range segments[:len(segments)-1] {
			child, ok := current[segment].(map[string]any)
			if !ok {
				child = map[string]any{}
			} else {
				// A previously selected parent may still alias the source map.
				// Clone on descent so overlapping whitelist paths never mutate it.
				child = copyMetadata(child)
			}
			current[segment] = child
			current = child
		}
		current[segments[len(segments)-1]] = value
	}
	return selected
}
