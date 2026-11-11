// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// message.go defines the canonical message, branch and node contracts that
// flow through a rule chain.

package rule

import "context"

// Msg is the canonical rule-engine message flowing between nodes. Data is the
// message body (one telemetry frame); Metadata carries transport-derived
// read-only context; TraceID joins per-node traces across the chain.
type Msg struct {
	// Type is the source message type, e.g. "telemetry".
	Type string `json:"type"`

	// TenantID owns the originating device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the originating device.
	DeviceID string `json:"device_id"`

	// Ts is the event timestamp in milliseconds.
	Ts int64 `json:"ts"`

	// TraceID uniquely identifies this chain execution for tracing.
	TraceID string `json:"trace_id"`

	// Data is the mutable message body.
	Data map[string]any `json:"data"`

	// Metadata is read-only context such as measurement key and quality.
	Metadata map[string]any `json:"metadata,omitempty"`
}

// Branch is one labeled output of a node. A node may emit several branches,
// including several messages on the same relation (Delimiter).
type Branch struct {
	// Relation selects the outgoing edge.
	Relation string `json:"relation"`

	// Msg is the output message.
	Msg Msg `json:"msg"`
}

// Node executes one rule node.
type Node interface {
	// ID is the node identifier.
	ID() string

	// Type is the node type identifier.
	Type() string

	// Execute evaluates the node. dryRun suppresses real side effects so
	// single-chain test delivery stays non-destructive.
	Execute(ctx context.Context, in Msg, dryRun bool) ([]Branch, error)
}

// Standard relation labels shared by nodes and stored rule edges.
const (
	// RelationSuccess is the standard continuation label.
	RelationSuccess = "Success"

	// RelationTrue and RelationFalse are filter outputs.
	RelationTrue  = "True"
	RelationFalse = "False"

	// RelationFailure is taken on a node execution failure.
	RelationFailure = "Failure"

	// RelationDefault is taken by Switch when no case matches and a default
	// is configured.
	RelationDefault = "Default"
)
