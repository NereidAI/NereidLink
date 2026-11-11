// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// advisory.go emits desired-vs-reported drift events. The default mode is
// advisory-only: a manifest mismatch never mutates the registry (it does not
// create unregistered devices or delete bindings), it publishes one event so
// operators and the future strict-reconcile slice can react. Presence itself
// is still converged by Reconcile regardless of this mode.

package manifest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// advisoryPublisher is the Core NATS surface advisory emission needs. It
// is satisfied by *nats.Conn and is narrowed so unit tests can substitute a
// recording publisher without an embedded broker.
type advisoryPublisher interface {
	Publish(subject string, data []byte) error
}

// AdvisoryKind labels the drift direction.
const (
	// AdvisoryDrift is emitted whenever a cycle has missing or
	// unregistered devices.
	AdvisoryDrift = "manifest.drift"

	// AdvisoryLeaseExpired is emitted when a gateway stops reporting.
	AdvisoryLeaseExpired = "manifest.lease_expired"
)

// Advisory is the drift event payload published to Core NATS.
type Advisory struct {
	// Kind is AdvisoryDrift or AdvisoryLeaseExpired.
	Kind string `json:"kind"`

	// Time is the event time.
	Time time.Time `json:"time"`

	// TenantID and GatewayID identify the reconciled gateway.
	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`

	// Revision is the gateway's last observed desired revision (drift).
	Revision int64 `json:"revision,omitempty"`

	// Missing are bound devices the gateway did not report.
	Missing []string `json:"missing,omitempty"`

	// Unregistered are reported devices not bound to the gateway.
	Unregistered []string `json:"unregistered,omitempty"`

	// OnlinePromoted and OfflineDerived record the presence actions taken.
	OnlinePromoted int `json:"online_promoted,omitempty"`
	OfflineDerived int `json:"offline_derived,omitempty"`
}

// advisorySubject builds "<prefix>.<tenant>.<gateway>".
func advisorySubject(tenantID, gatewayID string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectManifestPrefix, tenantID, gatewayID)
}

// emitAdvisory publishes one drift event. Core NATS failures are logged but
// must not block the transport path, matching presence event emission.
func (r *Reconciler) emitAdvisory(ctx context.Context, report Report) {
	advisory := Advisory{
		Kind:           AdvisoryDrift,
		Time:           report.At,
		TenantID:       report.TenantID,
		GatewayID:      report.GatewayID,
		Revision:       report.Revision,
		Missing:        report.Class.Missing,
		Unregistered:   report.Class.Unregistered,
		OnlinePromoted: report.OnlinePromoted,
		OfflineDerived: report.OfflineDerived,
	}
	raw, err := json.Marshal(advisory)
	if err != nil {
		r.log.Error("manifest advisory marshal failed", "error", err)
		return
	}
	if r.publisher == nil {
		return
	}
	if err := r.publisher.Publish(advisorySubject(report.TenantID, report.GatewayID), raw); err != nil {
		r.log.Error("manifest advisory publish failed", "error", err)
	}
}

// emitLeaseExpiredAdvisory publishes one event when a gateway stops
// refreshing its manifest lease. derived is the number of sub-devices
// converged OFFLINE. As with drift, a Core NATS failure is logged only.
func (r *Reconciler) emitLeaseExpiredAdvisory(ctx context.Context, tenantID, gatewayID string, derived int) {
	advisory := Advisory{
		Kind:           AdvisoryLeaseExpired,
		Time:           time.Now().UTC(),
		TenantID:       tenantID,
		GatewayID:      gatewayID,
		OfflineDerived: derived,
	}
	raw, err := json.Marshal(advisory)
	if err != nil {
		r.log.Error("manifest lease-expired advisory marshal failed", "error", err)
		return
	}
	if r.publisher == nil {
		return
	}
	if err := r.publisher.Publish(advisorySubject(tenantID, gatewayID), raw); err != nil {
		r.log.Error("manifest lease-expired advisory publish failed", "error", err)
	}
}
