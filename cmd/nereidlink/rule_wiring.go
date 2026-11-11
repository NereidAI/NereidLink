// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// rule_wiring.go maps rule/notify configuration to service tuning values.

package main

import (
	"time"

	"github.com/nereidai/nereidlink/internal/accounting"
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/deadman"
	"github.com/nereidai/nereidlink/internal/notify"
	"github.com/nereidai/nereidlink/sdk/partition"
)

// Debug capture defaults. Values are operational defaults rather than wire
// identifiers; they may move into configuration when the M4 UI exposes them.
const (
	// ruleDebugCaptures bounds retained in/out captures per node.
	ruleDebugCaptures = 100

	// ruleDebugTraces bounds retained traces per chain.
	ruleDebugTraces = 100

	// ruleDebugTTL bounds debug capture lifetime, per ADR-0028.
	ruleDebugTTL = 24 * time.Hour

	// profileCacheTTL bounds descriptor cache age.
	profileCacheTTL = 30 * time.Second

	// profileCacheSize bounds each descriptor-related LRU cache.
	profileCacheSize = 100000
)

// ruleShardCount matches the fixed telemetry stream shard count.
const ruleShardCount = partition.StreamShards

// notifyConfig maps declarative notification configuration.
func notifyConfig(cfg *config.Config) notify.Config {
	return notify.Config{
		FrequencyWindow: mustDuration(cfg.Notify.FrequencyWindow),
		WebhookURL:      cfg.Notify.WebhookURL,
		SMTP: notify.SMTPConfig{
			Enabled:  cfg.Notify.SMTP.Enabled,
			Host:     cfg.Notify.SMTP.Host,
			Port:     cfg.Notify.SMTP.Port,
			From:     cfg.Notify.SMTP.From,
			Username: cfg.Notify.SMTP.Username,
			Password: cfg.Notify.SMTP.Password,
		},
		CriticalWorkers: cfg.Notify.CriticalWorkers,
		Workers:         cfg.Notify.Workers,
		DeliveryTimeout: mustDuration(cfg.Notify.DeliveryTimeout),
	}
}

// accountingSamplerConfig maps the ADR-0025 sampler configuration. The
// node id is passed separately because it falls back to the presence
// ingress identity when the accounting section leaves it empty.
func accountingSamplerConfig(cfg *config.Config, nodeID string) accounting.Config {
	return accounting.Config{
		NodeID:     nodeID,
		SampleRate: cfg.Accounting.SampleRate,
		Capacity:   cfg.Accounting.Capacity,
	}
}

// deadmanConfig maps the independent platform fallback channel (ADR-0019).
func deadmanConfig(cfg *config.Config) deadman.Config {
	return deadman.Config{
		Enabled:    cfg.Deadman.Enabled,
		WebhookURL: cfg.Deadman.WebhookURL,
		SMTP: deadman.SMTPConfig{
			Enabled:    cfg.Deadman.SMTP.Enabled,
			Host:       cfg.Deadman.SMTP.Host,
			Port:       cfg.Deadman.SMTP.Port,
			From:       cfg.Deadman.SMTP.From,
			Username:   cfg.Deadman.SMTP.Username,
			Password:   cfg.Deadman.SMTP.Password,
			Recipients: cfg.Deadman.EmailRecipients,
		},
		Interval:  mustDuration(cfg.Deadman.Interval),
		Threshold: cfg.Deadman.Threshold,
		Timeout:   mustDuration(cfg.Deadman.Timeout),
	}
}
