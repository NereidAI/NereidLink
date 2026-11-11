// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discovery_wiring.go maps auto-provisioning configuration onto the
// discovery policy. Numeric limits fall back to package defaults (100 new
// devices per frame, 1000 candidates per frame) when left unset.

package main

import (
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/discovery"
)

// discoveryPolicy maps declarative configuration onto discovery.Policy.
func discoveryPolicy(cfg *config.Config) discovery.Policy {
	return discovery.Policy{
		Enabled:        cfg.AutoProvision.Enabled,
		MaxPerCycle:    cfg.AutoProvision.MaxPerCycle,
		MaxCandidates:  cfg.AutoProvision.MaxCandidates,
		DefaultProfile: cfg.AutoProvision.DefaultProfile,
		ApprovalMode:   cfg.AutoProvision.ApprovalMode,
	}
}
