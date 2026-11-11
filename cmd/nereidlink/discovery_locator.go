// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// discovery_locator.go adapts the presence service to the narrow locator the
// discovery approval flow needs (migration 0046). A gateway is itself a
// device in the presence KV keyed "<tenant>.<gateway>", so its live location
// yields the ingress node owning the MQTT connection that must receive the
// asynchronous approval result.

package main

import (
	"context"

	"github.com/nereidai/nereidlink/internal/discovery"
	"github.com/nereidai/nereidlink/internal/presence"
)

// discoveryPresenceLocator implements discovery.LocationResolver over the
// shared presence service.
type discoveryPresenceLocator struct {
	presence *presence.Service
}

// LookupGateway resolves the ingress that owns the gateway's live session.
func (l discoveryPresenceLocator) LookupGateway(ctx context.Context, tenantID, gatewayID string) (discovery.GatewayLocation, error) {
	loc, err := l.presence.Lookup(ctx, presence.Identity{TenantID: tenantID, DeviceID: gatewayID})
	if err != nil {
		return discovery.GatewayLocation{}, err
	}
	return discovery.GatewayLocation{Found: loc.Found, IngressID: loc.IngressID}, nil
}
