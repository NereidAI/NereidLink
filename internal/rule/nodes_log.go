// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_log.go implements the Log node.

package rule

import (
	"context"
	"log/slog"
)

// logConfig is the Log node configuration.
type logConfig struct {
	// Message is the logged text.
	Message string `json:"message"`
}

// logNode emits a structured log line.
type logNode struct {
	id     string
	config logConfig
}

func (n *logNode) ID() string   { return n.id }
func (n *logNode) Type() string { return NodeTypeLog }

func (n *logNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	slog.Info("rule log",
		"message", n.config.Message,
		"tenant", in.TenantID,
		"device", in.DeviceID,
		"trace", in.TraceID)
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}
