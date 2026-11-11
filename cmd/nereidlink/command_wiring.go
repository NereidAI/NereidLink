// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/nereidai/nereidlink/internal/command"
	"github.com/nereidai/nereidlink/internal/config"
)

// commandTuning maps declarative configuration to command service tuning.
func commandTuning(cfg *config.Config) (command.Tuning, error) {
	return command.Tuning{
		PersistentTTL:   mustDuration(cfg.Command.PersistentTTL),
		DeliveryTimeout: mustDuration(cfg.Command.DeliveryTimeout),
		SweepInterval:   mustDuration(cfg.Command.SweepInterval),
		ClaimBatchSize:  cfg.Command.ClaimBatchSize,
	}, nil
}
