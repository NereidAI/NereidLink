// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// streams.go lets external adapters ensure the shared telemetry JetStream
// topology before publishing, for example when an adapter container
// starts before the core. It mirrors the internal stream provisioning so
// adapters never hard-code stream names.

package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/sdk/partition"
)

// Protocol identifiers stamped on Envelopes. Adapters for additional
// protocols use their own short identifier string; these are the
// built-in values.
const (
	// ProtocolMQTT identifies the MQTT transport.
	ProtocolMQTT = "MQTT"

	// ProtocolHTTP identifies the HTTP transport.
	ProtocolHTTP = "HTTP"

	// ProtocolCoAP identifies the CoAP transport.
	ProtocolCoAP = "COAP"
)

// StreamName returns the physical telemetry stream name for a shard.
func StreamName(shard uint32) string {
	return fmt.Sprintf("TL_S%d", shard)
}

// EnsureStreams creates or updates every telemetry shard stream.
// CreateOrUpdateStream is idempotent, so it is safe to call on every adapter
// startup and after a partially completed provisioning run. Telemetry
// streams are processing buffers only; MaxAge bounds replay. The optional
// replicas argument sets the JetStream Raft factor (default 1; clustered
// NATS deployments pass 3 or 5).
func EnsureStreams(ctx context.Context, js jetstream.JetStream, replicas ...int) error {
	r := 1
	if len(replicas) > 0 && replicas[0] > 0 {
		r = replicas[0]
	}
	for shard := 0; shard < partition.StreamShards; shard++ {
		cfg := jetstream.StreamConfig{
			Name: StreamName(uint32(shard)),
			Subjects: []string{
				fmt.Sprintf("%s.s%d.>", SubjectPrefix, shard),
			},
			Retention: jetstream.LimitsPolicy,
			MaxAge:    7 * 24 * time.Hour,
			Replicas:  r,
		}
		if _, err := js.CreateOrUpdateStream(ctx, cfg); err != nil {
			return fmt.Errorf("ensure stream %s: %w", cfg.Name, err)
		}
	}
	return nil
}
