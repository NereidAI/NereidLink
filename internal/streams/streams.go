// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package streams centralizes JetStream topology management.
//
// All stream names, subject layouts and shard calculations live behind this
// package so that producers and consumers never hard-code topology. The
// number of streams is fixed with respect to the tenant count: every message
// domain is split into partition.StreamShards physical streams, each of which
// is an independent Raft group whose leader can be placed on any cluster
// node, giving linear throughput growth.
package streams

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/sdk/partition"
)

// Domain identifiers, kept as constants to avoid subject typos across
// producers and consumers.
const (
	// TelemetrySubjectPrefix is the subject root for raw device telemetry.
	TelemetrySubjectPrefix = "nl.telemetry"
)

// TelemetryStreamName returns the physical stream name for a shard.
func TelemetryStreamName(shard uint32) string {
	return fmt.Sprintf("TL_S%d", shard)
}

// TelemetrySubject builds the concrete telemetry subject for one partition.
// Layout: nl.telemetry.<shard>.<tenant>.<partition>.
// The tenant segment allows subject-filter subscriptions while the shard
// segment pins the message to the correct physical stream.
func TelemetrySubject(shard uint32, tenantID string, part uint32) string {
	return fmt.Sprintf("%s.s%d.%s.%d", TelemetrySubjectPrefix, shard, tenantID, part)
}

// TelemetryWildcard matches every telemetry shard. JetStream publishes are
// also ordinary Core NATS publishes, so Core subscribers use this wildcard
// for real-time fanout without binding to a specific stream.
//
// NATS requires '*' to occupy a whole token; 's*' would be a literal token
// and match nothing, hence the separate wildcard token layout.
func TelemetryWildcard() string {
	return TelemetrySubjectPrefix + ".*.>"
}

// EnsureTelemetryStreams creates or updates every telemetry shard stream.
// CreateOrUpdateStream is idempotent, so the function is safe to call on
// every startup and after a partially completed provisioning run. replicas
// is the JetStream Raft factor: 1 for the bundled single NATS, 3 (or 5) for
// a clustered deployment.
func EnsureTelemetryStreams(ctx context.Context, js jetstream.JetStream, replicas int) error {
	if replicas < 1 {
		replicas = 1
	}
	for shard := 0; shard < partition.StreamShards; shard++ {
		cfg := jetstream.StreamConfig{
			Name:      TelemetryStreamName(uint32(shard)),
			Subjects:  []string{fmt.Sprintf("%s.s%d.>", TelemetrySubjectPrefix, shard)},
			Retention: jetstream.LimitsPolicy,
			// TL streams are processing buffers only; TimescaleDB is the
			// authoritative store. Seven days bound replay needs.
			MaxAge: 7 * 24 * time.Hour,
			// M0/single-node deployments run R1; the Helm HA topology sets
			// this to 3.
			Replicas: replicas,
		}
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}
