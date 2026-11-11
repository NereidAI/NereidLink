// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"time"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/manifest"
)

// manifestDefaults are applied when a manifest duration is left unset. The
// edge sends on 5-minute wall-clock boundaries; a 16-minute lease survives
// roughly three missed cycles before attached sub-devices are derived
// offline, and a 1-minute sweep bounds how late the silent-TTL-expiry
// backstop fires.
var manifestDefaults = struct {
	leaseTTL      time.Duration
	sweepInterval time.Duration
}{
	leaseTTL:      16 * time.Minute,
	sweepInterval: 1 * time.Minute,
}

// manifestTuning maps declarative configuration to manifest tuning, filling
// defaults for any empty duration.
func manifestTuning(cfg *config.Config) manifest.Tuning {
	tune := manifest.Tuning{
		JSReplicas:    cfg.NATS.JetStreamReplicas,
		LeaseTTL:      manifestDefaults.leaseTTL,
		SweepInterval: manifestDefaults.sweepInterval,
	}
	if d, ok := optionalDuration(cfg.Manifest.LeaseTTL); ok && d > 0 {
		tune.LeaseTTL = d
	}
	if d, ok := optionalDuration(cfg.Manifest.SweepInterval); ok && d > 0 {
		tune.SweepInterval = d
	}
	return tune
}

// optionalDuration parses a duration string, treating empty as unset rather
// than an error so omitted config fields fall back to defaults.
func optionalDuration(value string) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		panic(err)
	}
	return d, true
}
