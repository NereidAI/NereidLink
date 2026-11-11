// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attribute_adapter.go wires registry attribute writes to Core NATS so an
// MQTT ingress owning the target device's live session can push the change to
// the device. The registry stays free of a NATS dependency.
package main

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/attributeevent"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// natsAttributePublisher publishes one server/shared attribute change on the
// per-device push subject.
type natsAttributePublisher struct {
	nc *nats.Conn
}

// PublishAttributePush implements registry.AttributePublisher. A publish
// failure is logged and swallowed: the attribute row is already committed and
// downlink delivery is best-effort by contract.
func (p *natsAttributePublisher) PublishAttributePush(frame mqttapi.AttributePushFrame) {
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal attribute push frame", "error", err, "device", frame.DeviceID)
		return
	}
	subject := fmt.Sprintf("%s.%s.%s",
		mqttapi.SubjectAttributePushPrefix, frame.TenantID, frame.DeviceID)
	if err := p.nc.Publish(subject, data); err != nil {
		slog.Error("publish attribute push", "error", err, "subject", subject)
	}
}

// PublishAttributeEvent implements both registry.AttributeEventPublisher and
// rule.AttributeEventPublisher. It fans the committed change to the
// console-plane hub for live WebSocket delivery; best-effort like the push.
func (p *natsAttributePublisher) PublishAttributeEvent(frame attributeevent.Event) {
	data, err := json.Marshal(frame)
	if err != nil {
		slog.Error("marshal attribute event frame", "error", err, "device", frame.DeviceID)
		return
	}
	subject := fmt.Sprintf("%s.%s.%s",
		attributeevent.SubjectPrefix, frame.TenantID, frame.DeviceID)
	if err := p.nc.Publish(subject, data); err != nil {
		slog.Error("publish attribute event", "error", err, "subject", subject)
	}
}
