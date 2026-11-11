// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/presence"
)

// presenceShutdownTimeout bounds presence cleanup during graceful shutdown.
const presenceShutdownTimeout = 30 * time.Second

// presenceTuning maps declarative configuration to presence service tuning.
func presenceTuning(cfg *config.Config) (presence.Tuning, error) {
	return presence.Tuning{
		JSReplicas:           cfg.NATS.JetStreamReplicas,
		HeartbeatInterval:    mustDuration(cfg.Presence.HeartbeatInterval),
		LeaseTTL:             mustDuration(cfg.Presence.LeaseTTL),
		OfflineBatchSize:     cfg.Presence.OfflineBatchSize,
		OfflineBatchInterval: mustDuration(cfg.Presence.OfflineBatchInterval),
		PersistBufferSize:    cfg.Presence.PersistBufferSize,
	}, nil
}
