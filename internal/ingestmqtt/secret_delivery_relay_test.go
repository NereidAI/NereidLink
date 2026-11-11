// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secret_delivery_relay_test.go verifies the hello-negotiated feature gate
// that keeps secret-delivery frames away from firmware which does not
// implement them (ADR-0034).

package ingestmqtt

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/edge"
	"github.com/nereidai/nereidlink/internal/secretbinding"
)

// TestMarkFeaturesAndHasFeature checks the session feature record used by
// the relay gate: absent before negotiation, present after, fail-closed for
// an unknown client.
func TestMarkFeaturesAndHasFeature(t *testing.T) {
	hook := &AuthHook{connections: map[string]connection{
		"c": {},
	}}

	if hook.clientHasFeature("c", edge.FeatureSecrets) {
		t.Fatal("feature must be absent before hello records it")
	}
	if hook.clientHasFeature("gone", edge.FeatureSecrets) {
		t.Fatal("unknown client must fail closed")
	}

	hook.markFeatures("c", []string{
		edge.FeatureTelemetry, edge.FeatureSecrets,
	})
	if !hook.clientHasFeature("c", edge.FeatureSecrets) {
		t.Fatal("secrets feature should be negotiated")
	}
	if hook.clientHasFeature("c", edge.FeatureOTA) {
		t.Fatal("unadvertised feature must not be present")
	}
}

// countingDeliveryResolver fails the test if the relay asks it to open
// documents; the gate must short-circuit before resolution.
type countingDeliveryResolver struct {
	called bool
}

func (c *countingDeliveryResolver) ResolveDocuments(_ context.Context, _ string, _ []string) (map[string]map[string]any, error) {
	c.called = true
	return nil, nil
}

// TestHandleSecretDesiredSkipsUnsupportedFirmware ensures an online session
// that did not negotiate the secrets feature receives no sync frame: the
// handler returns before touching the broker (nil here would panic) or the
// vault resolver.
func TestHandleSecretDesiredSkipsUnsupportedFirmware(t *testing.T) {
	resolver := &countingDeliveryResolver{}
	hook := &AuthHook{
		connections: map[string]connection{
			"c": {features: []string{edge.FeatureTelemetry}},
		},
		devices: map[string]string{"t.gw": "c"},
	}
	relay := &CommandRelay{
		hooks:          hook,
		deliveryDocs:   resolver,
		dispatchMarker: nil,
	}

	frame := secretbinding.PushFrame{
		TenantID:  "t",
		GatewayID: "gw",
		Entries: []secretbinding.Entry{{
			Name:     "cam1",
			Revision: 1,
			Document: map[string]any{
				"password": "secret://cam1#password",
			},
		}},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}

	relay.handleSecretDesired(&nats.Msg{Data: raw})

	if resolver.called {
		t.Fatal("vault must not be opened for unsupported firmware")
	}
}

// TestHandleSecretDesiredSkipsUnknownGateway verifies that a desired frame
// for a gateway with no live session on this ingress is simply dropped; the
// post-hello redrive delivers it on reconnect.
func TestHandleSecretDesiredSkipsUnknownGateway(t *testing.T) {
	resolver := &countingDeliveryResolver{}
	hook := &AuthHook{
		connections: map[string]connection{},
		devices:     map[string]string{},
	}
	relay := &CommandRelay{hooks: hook, deliveryDocs: resolver}

	raw, err := json.Marshal(secretbinding.PushFrame{
		TenantID:  "t",
		GatewayID: "offline",
		Entries:   []secretbinding.Entry{{Name: "cam1", Revision: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}

	relay.handleSecretDesired(&nats.Msg{Data: raw})

	if resolver.called {
		t.Fatal("vault must not be opened for an offline gateway")
	}
}
