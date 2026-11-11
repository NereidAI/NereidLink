// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// gateway.go holds the idempotent presence operations used by manifest
// reconciliation (ADR-0026). Unlike the explicit gateway connect/disconnect
// announcements, a manifest is the gateway's periodic full-list claim of
// which sub-devices are online, so reconciliation must converge presence
// toward that list without emitting spurious boundaries every cycle:
// a device already online under the gateway stays untouched, and a device
// present under a different plane (a direct session or another gateway) is
// never hijacked.

package presence

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
)

// ReasonManifest marks an OFFLINE boundary derived from a periodic manifest
// that no longer lists the sub-device.
const ReasonManifest = "manifest"

// ReasonManifestLeaseExpired marks boundaries derived when a gateway's
// manifest lease TTL elapsed without a fresh manifest.
const ReasonManifestLeaseExpired = "manifest_lease_expired"

// ReasonGatewayLeaseExpired marks boundaries derived when a gateway's
// application heartbeat lease elapsed while its transport stayed up (a
// silently stalled edge), per the ADR-0026 link watchdog DOWN state.
const ReasonGatewayLeaseExpired = "gateway_lease_expired"

// forgetAny drops the local session record for key regardless of which
// fencing token owns it. Manifest reconciliation is an administrative
// authority over gateway-attached devices, not a fenced transport close.
func (s *Service) forgetAny(key string) {
	s.mu.Lock()
	delete(s.sessions, key)
	s.mu.Unlock()
}

// gatewayBinding returns the gateway id recorded on a live presence key. A
// missing key returns jetstream.ErrKeyNotFound.
func (s *Service) gatewayBinding(ctx context.Context, key string) (string, error) {
	entry, err := s.presence.Get(ctx, key)
	if err != nil {
		return "", err
	}
	var value v1.PresenceValue
	if err := proto.Unmarshal(entry.Value(), &value); err != nil {
		return "", err
	}
	return value.GatewayId, nil
}

// EnsureGatewayOnline makes sure id has a live presence attached to gatewayID.
// It is idempotent: when the device is already present under that gateway no
// boundary is emitted (changed=false). A present session owned by another
// plane (direct connection or a different gateway) is left untouched and
// reported as changed=false, so a manifest can never hijack another session.
// Only a missing key is promoted ONLINE.
func (s *Service) EnsureGatewayOnline(ctx context.Context, id Identity, gatewayID string) (bool, error) {
	key := presenceKey(id)
	switch _, err := s.gatewayBinding(ctx, key); {
	case err == nil:
		// Already present; converge only when it is the same gateway.
		return false, nil
	case errors.Is(err, jetstream.ErrKeyNotFound):
		// Promote below.
	default:
		return false, err
	}

	if _, err := s.Connect(ctx, id, "", gatewayID); err != nil {
		return false, err
	}
	return true, nil
}

// EnsureGatewayOffline removes id's presence only when it is attached to
// gatewayID, emitting one OFFLINE boundary. A missing key, or a session owned
// by another plane, is a no-op (changed=false). The fencing-token rule that
// guards explicit disconnects does not apply here: the manifest is an
// authority over gateway-attached devices independent of which session
// attached them.
func (s *Service) EnsureGatewayOffline(ctx context.Context, id Identity, gatewayID, reason string) (bool, error) {
	key := presenceKey(id)
	existing, err := s.gatewayBinding(ctx, key)
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if existing != gatewayID {
		return false, nil
	}
	if err := s.presence.Delete(ctx, key); err != nil &&
		!errors.Is(err, jetstream.ErrKeyNotFound) {
		return false, err
	}
	s.forgetAny(key)
	s.emit(ctx, id, gatewayID, "OFFLINE", reason, "", time.Now().UTC())
	return true, nil
}

// ListGatewaySubdevices returns the identities currently holding a live
// presence attached to gatewayID. It scans the presence KV the same way
// cascadeGatewayOffline does; the scan runs only on manifest reconciliation,
// off the per-frame hot path.
func (s *Service) ListGatewaySubdevices(ctx context.Context, tenantID, gatewayID string) ([]Identity, error) {
	lister, err := s.presence.ListKeys(ctx)
	if err != nil {
		return nil, err
	}
	var out []Identity
	for key := range lister.Keys() {
		prefix := tenantID + "."
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		binding, err := s.gatewayBinding(ctx, key)
		if err != nil {
			continue
		}
		if binding != gatewayID {
			continue
		}
		deviceID, ok := strings.CutPrefix(key, prefix)
		if !ok || deviceID == gatewayID {
			continue
		}
		out = append(out, Identity{TenantID: tenantID, DeviceID: deviceID})
	}
	return out, nil
}

// DeriveGatewayOffline converges every currently-attached sub-device of
// gatewayID to OFFLINE. It is the manifest-lease-expiry backstop: when no
// manifest arrives for LeaseTTL, attached sub-devices cannot be assumed live.
// It returns the number of boundaries emitted.
func (s *Service) DeriveGatewayOffline(ctx context.Context, tenantID, gatewayID, reason string) (int, error) {
	attached, err := s.ListGatewaySubdevices(ctx, tenantID, gatewayID)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, id := range attached {
		changed, err := s.EnsureGatewayOffline(ctx, id, gatewayID, reason)
		if err != nil {
			return count, err
		}
		if changed {
			count++
		}
	}
	return count, nil
}
