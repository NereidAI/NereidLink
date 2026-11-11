// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"testing"
	"time"
)

// TestToWireCommandCarriesGatewayID proves the presence-derived gateway id
// reaches the dispatch protobuf so an ingress relays a sub-device command to
// the gateway session; a direct device keeps it empty.
func TestToWireCommandCarriesGatewayID(t *testing.T) {
	created := time.UnixMilli(1_700_000_000_000)
	c := Command{
		ID:          "cmd-1",
		TenantID:    "t",
		DeviceID:    "sub",
		Name:        "reboot",
		Params:      []byte(`{"delay":1}`),
		Interaction: InteractionRequestResponse,
		Timeliness:  TimelinessRealtime,
		Timeout:     5 * time.Second,
		CreatedAt:   created,
	}

	relayed := toWireCommand(c, "gw-7")
	if relayed.GatewayId != "gw-7" {
		t.Fatalf("gateway id = %q, want gw-7", relayed.GatewayId)
	}
	if relayed.DeviceId != "sub" || relayed.Name != "reboot" {
		t.Fatalf("identity fields lost: %+v", relayed)
	}
	if relayed.TimeoutMs != 5000 || relayed.CreatedAtMs != created.UnixMilli() {
		t.Fatalf("timing fields wrong: timeout=%d created=%d", relayed.TimeoutMs, relayed.CreatedAtMs)
	}

	direct := toWireCommand(c, "")
	if direct.GatewayId != "" {
		t.Fatalf("direct device must carry an empty gateway id, got %q", direct.GatewayId)
	}
}
