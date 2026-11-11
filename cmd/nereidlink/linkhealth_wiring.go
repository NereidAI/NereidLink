// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/linkhealth"
)

// linkHealthTuning maps declarative configuration to the ADR-0026 watchdog
// tuning, leaving empty fields at the built-in ADR defaults (30s heartbeat,
// SUSPECT after 45s, DOWN after 90s, flapping at >=5 in 10m).
func linkHealthTuning(cfg *config.Config) linkhealth.Tuning {
	tune := linkhealth.DefaultTuning()
	if d, ok := optionalDuration(cfg.LinkHealth.HeartbeatInterval); ok && d > 0 {
		tune.HeartbeatInterval = d
	}
	if d, ok := optionalDuration(cfg.LinkHealth.SuspectAfter); ok && d > 0 {
		tune.SuspectAfter = d
	}
	if d, ok := optionalDuration(cfg.LinkHealth.DownAfter); ok && d > 0 {
		tune.DownAfter = d
	}
	if d, ok := optionalDuration(cfg.LinkHealth.SweepInterval); ok && d > 0 {
		tune.SweepInterval = d
	}
	if d, ok := optionalDuration(cfg.LinkHealth.FlapWindow); ok && d > 0 {
		tune.FlapWindow = d
	}
	if cfg.LinkHealth.FlapThreshold > 0 {
		tune.FlapThreshold = cfg.LinkHealth.FlapThreshold
	}
	return tune
}
