// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// monitor.go wraps the pure Detector with the background sweep and Core
// NATS advisory publishing. Ingest transports call Observe* on uplinks and
// connection lifecycle events; the monitor turns Detector transitions into
// advisories and periodically evaluates silence-driven SUSPECT/DOWN moves.

package linkhealth

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/presence"
)

// SubjectPrefix is the Core NATS root for gateway link-health advisories,
// suffixed by "<tenant>.<gateway>".
const SubjectPrefix = "nl.linkhealth"

// Advisory is a published link-health transition.
type Advisory struct {
	// Kind is the transition kind: "suspect", "down", "recovered",
	// "flapping" or "flap_settled".
	Kind string `json:"kind"`

	// Time is the transition time.
	Time time.Time `json:"time"`

	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`

	// From and To are the link states around the transition.
	From State `json:"from"`
	To   State `json:"to"`

	// Reason explains the transition (see Reason constants).
	Reason Reason `json:"reason"`

	// Source marks edge-originated advisories ("edge"); empty for
	// lease-driven ones.
	Source string `json:"source,omitempty"`
}

// Kind values carried by advisories.
const (
	KindSuspect     = "suspect"
	KindDown        = "down"
	KindPlanned     = "planned_down"
	KindRecovered   = "recovered"
	KindFlapping    = "flapping"
	KindFlapSettled = "flap_settled"
)

// publisher is the narrow NATS surface advisories need; satisfied by
// *nats.Conn and replaceable in tests.
type publisher interface {
	Publish(subject string, data []byte) error
}

// Cascade drives gateway-attached sub-devices OFFLINE when a gateway's
// application link goes DOWN while its transport stays up (a silent stall).
// It is satisfied by *presence.Service; nil leaves presence untouched.
type Cascade interface {
	DeriveGatewayOffline(ctx context.Context, tenantID, gatewayID, reason string) (int, error)
}

// Monitor runs the watchdog sweep and publishes advisories.
type Monitor struct {
	detector *Detector
	nc       publisher
	cascade  Cascade
	log      *slog.Logger
}

// NewMonitor constructs a Monitor. nc may be nil in tests, which disables
// publishing while keeping state transitions.
func NewMonitor(nc *nats.Conn, tune Tuning, log *slog.Logger) *Monitor {
	if log == nil {
		log = slog.Default()
	}
	return &Monitor{
		detector: NewDetector(tune),
		nc:       nc,
		log:      log,
	}
}

// SetCascade wires the presence backstop used when a gateway goes DOWN via
// application-lease expiry while its transport is still up.
func (m *Monitor) SetCascade(cascade Cascade) {
	m.cascade = cascade
}

// Run evaluates all links on a fixed cadence until ctx is canceled.
func (m *Monitor) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.detector.tune.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			for _, tr := range m.detector.Evaluate(now.UTC()) {
				m.publishTransition(ctx, tr)
			}
		}
	}
}

// ObserveConnect records a negotiated gateway session.
func (m *Monitor) ObserveConnect(ctx context.Context, tenantID, gatewayID string, at time.Time) {
	if tr, ok := m.detector.Connect(tenantID, gatewayID, at); ok {
		m.publishTransition(ctx, tr)
	}
}

// ObserveHeartbeat records an application heartbeat.
func (m *Monitor) ObserveHeartbeat(ctx context.Context, tenantID, gatewayID string, at time.Time) {
	if tr, ok := m.detector.Heartbeat(tenantID, gatewayID, at); ok {
		m.publishTransition(ctx, tr)
	}
}

// ObserveGoodbye records a shutdown notice. planned distinguishes an
// intentional shutdown (PLANNED_DOWN, no flap) from an unexpected goodbye.
func (m *Monitor) ObserveGoodbye(ctx context.Context, tenantID, gatewayID string, planned bool, reason string, at time.Time) {
	if tr, ok := m.detector.Goodbye(tenantID, gatewayID, Goodbye{Planned: planned, Reason: reason}, at); ok {
		m.publishTransition(ctx, tr)
	}
}

// ObserveTransportClosed records an ungraceful physical disconnect.
func (m *Monitor) ObserveTransportClosed(ctx context.Context, tenantID, gatewayID string, at time.Time) {
	if tr, ok := m.detector.TransportClosed(tenantID, gatewayID, at); ok {
		m.publishTransition(ctx, tr)
	}
}

// ObserveEdgeEpisode records an edge-reported episode that did not match a
// lease episode. It publishes a flapping advisory when the edge flap window
// crosses the threshold and a flap_settled advisory once the burst ages out
// (the latter is produced by Run's evaluation).
func (m *Monitor) ObserveEdgeEpisode(ctx context.Context, tenantID, gatewayID string, at time.Time) {
	if tr, ok := m.detector.ObserveEdgeEpisode(tenantID, gatewayID, at); ok {
		m.publishTransition(ctx, tr)
	}
}

// State exposes the current link state.
func (m *Monitor) State(tenantID, gatewayID string) State {
	return m.detector.State(tenantID, gatewayID)
}

// Remove forgets a gateway's watchdog state.
func (m *Monitor) Remove(tenantID, gatewayID string) {
	m.detector.Remove(tenantID, gatewayID)
}

// subject builds "<prefix>.<tenant>.<gateway>".
func subject(tenantID, gatewayID string) string {
	return SubjectPrefix + "." + tenantID + "." + gatewayID
}

// kindFor maps a transition to its advisory kind. Edge flap transitions are
// handled first; otherwise a lease flap crossing is attached to the DOWN
// transition that caused it.
func kindFor(tr Transition) string {
	if tr.Source == SourceEdge {
		switch {
		case tr.EdgeFlap:
			return KindFlapping
		case tr.EdgeSettled:
			return KindFlapSettled
		default:
			return ""
		}
	}
	if tr.Reason == ReasonPlannedDown {
		return KindPlanned
	}
	if tr.Flapping {
		return KindFlapping
	}
	switch {
	case tr.To == StateSuspect:
		return KindSuspect
	case tr.To == StateDown:
		return KindDown
	case tr.To == StateHealthy && tr.From == StateDown:
		return KindRecovered
	default:
		return ""
	}
}

// publishTransition publishes the advisory for one transition. A healthy
// refresh or a planned DOWN that does not require an alert is still emitted
// as a state event, but flapping drives a distinct kind. A lease-expired DOWN
// while the transport is up also converges attached sub-devices OFFLINE; the
// transport-down path is cascaded by the ingress OnDisconnect hook instead.
// Edge-originated transitions never cascade presence (the edge episode
// already reflects the real transport event). Core NATS failures are logged
// only; watchdog state already advanced.
func (m *Monitor) publishTransition(ctx context.Context, tr Transition) {
	kind := kindFor(tr)
	if kind == "" {
		return
	}
	if tr.Source != SourceEdge &&
		tr.To == StateDown && tr.Reason == ReasonLeaseExpired && m.cascade != nil {
		if n, err := m.cascade.DeriveGatewayOffline(ctx,
			tr.TenantID, tr.GatewayID, presence.ReasonGatewayLeaseExpired); err != nil {
			m.log.Error("link-health presence cascade failed",
				"gateway", tr.GatewayID, "error", err)
		} else if n > 0 {
			m.log.Warn("link lease expired with live transport; sub-devices derived offline",
				"gateway", tr.GatewayID, "count", n)
		}
	}
	raw, err := json.Marshal(Advisory{
		Kind:      kind,
		Time:      tr.At,
		TenantID:  tr.TenantID,
		GatewayID: tr.GatewayID,
		From:      tr.From,
		To:        tr.To,
		Reason:    tr.Reason,
		Source:    tr.Source,
	})
	if err != nil {
		m.log.Error("link-health advisory marshal failed", "error", err)
		return
	}
	if m.nc == nil {
		return
	}
	if err := m.nc.Publish(subject(tr.TenantID, tr.GatewayID), raw); err != nil {
		m.log.Error("link-health advisory publish failed", "error", err)
	}
}
