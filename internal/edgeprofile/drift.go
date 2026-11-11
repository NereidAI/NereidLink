// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SubjectProfilePrefix is the Core NATS root for profile library advisories:
// nl.profile.<tenant>.<gateway>.
const SubjectProfilePrefix = "nl.profile"

// natsPublisher is the Core NATS surface the sink needs. *nats.Conn satisfies
// it; tests use a recording double.
type natsPublisher interface {
	Publish(subject string, data []byte) error
}

// NatsDriftSink publishes profile drift/collision advisories to Core NATS.
type NatsDriftSink struct {
	nc natsPublisher
}

// NewNatsDriftSink constructs the default Core NATS sink.
func NewNatsDriftSink(nc natsPublisher) *NatsDriftSink {
	return &NatsDriftSink{nc: nc}
}

// profileAdvisory is the published payload.
type profileAdvisory struct {
	Kind        string    `json:"kind"`
	Time        time.Time `json:"time"`
	TenantID    string    `json:"tenant_id"`
	GatewayID   string    `json:"gateway_id"`
	ProfileID   string    `json:"profile_id"`
	ProfileName string    `json:"profile_name"`
}

// EmitProfileDrift implements DriftSink.
func (s *NatsDriftSink) EmitProfileDrift(_ context.Context, event DriftEvent) error {
	if s == nil || s.nc == nil {
		return nil
	}
	raw, err := json.Marshal(profileAdvisory{
		Kind:        event.Kind,
		Time:        time.Now().UTC(),
		TenantID:    event.TenantID,
		GatewayID:   event.GatewayID,
		ProfileID:   event.ProfileID,
		ProfileName: event.ProfileName,
	})
	if err != nil {
		return fmt.Errorf("marshal profile advisory: %w", err)
	}
	return s.nc.Publish(
		fmt.Sprintf("%s.%s.%s", SubjectProfilePrefix, event.TenantID, event.GatewayID),
		raw)
}
