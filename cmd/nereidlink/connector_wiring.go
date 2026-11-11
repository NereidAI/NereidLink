// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// connector_wiring.go maps connector lifecycle configuration to the
// service tuning, leaving empty fields at the ADR defaults (pending event
// after 5 minutes, failed escalation after 1 hour, 1 minute sweep).

package main

import (
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/connector"
)

// connectorTuning maps declarative configuration onto connector.Tuning.
func connectorTuning(cfg *config.Config) connector.Tuning {
	tune := connector.DefaultTuning()
	if d, ok := optionalDuration(cfg.Connector.PendingAfter); ok && d > 0 {
		tune.PendingAfter = d
	}
	if d, ok := optionalDuration(cfg.Connector.FailedEscalationAfter); ok && d > 0 {
		tune.FailedEscalationAfter = d
	}
	if d, ok := optionalDuration(cfg.Connector.SweepInterval); ok && d > 0 {
		tune.SweepInterval = d
	}
	tune.StrictReconcile = cfg.Connector.StrictReconcile
	return tune
}
