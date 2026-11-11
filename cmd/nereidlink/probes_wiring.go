// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// probes_wiring.go maps on-demand probe configuration onto probe.Tuning,
// leaving empty fields at the ADR defaults (100 devices/task, 1 QPS and 5
// concurrent per principal, 10s per-device cooldown, 10s probe/60s task
// timeouts, 24h result retention, 1m sweep).

package main

import (
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/probe"
)

// probeTuning maps declarative configuration onto probe.Tuning.
func probeTuning(cfg *config.Config) probe.Tuning {
	tune := probe.DefaultTuning()
	if cfg.Probe.MaxDevices > 0 {
		tune.MaxDevices = cfg.Probe.MaxDevices
	}
	if cfg.Probe.MaxConcurrent > 0 {
		tune.MaxConcurrent = cfg.Probe.MaxConcurrent
	}
	if d, ok := optionalDuration(cfg.Probe.CreateRate); ok && d > 0 {
		tune.CreateRate = d
	}
	if d, ok := optionalDuration(cfg.Probe.Cooldown); ok && d > 0 {
		tune.Cooldown = d
	}
	if d, ok := optionalDuration(cfg.Probe.DefaultTimeout); ok && d > 0 {
		tune.DefaultTimeout = d
	}
	if d, ok := optionalDuration(cfg.Probe.TaskDeadline); ok && d > 0 {
		tune.TaskDeadline = d
	}
	if d, ok := optionalDuration(cfg.Probe.Retention); ok && d > 0 {
		tune.Retention = d
	}
	if d, ok := optionalDuration(cfg.Probe.SweepInterval); ok && d > 0 {
		tune.SweepInterval = d
	}
	return tune
}
