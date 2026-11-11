// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package linkhealth implements the application-level gateway watchdog
// slice of ADR-0026 ("heartbeat 30s + lease TTL 90s, missed 1 beat SUSPECT,
// missed 3 beats DOWN; goodbye PLANNED_DOWN does not alert; flapping 10min
// >= 5").
//
// It is deliberately kept off the connection plane. internal/presence stays
// a binary ONLINE/OFFLINE transport model and already cascades sub-devices
// on a TCP teardown; linkhealth tracks the *application* link state so a
// silently stalled edge (live MQTT transport, dead application) is still
// noticed, and it emits advisories rather than mutating presence. A future
// slice correlates these into alarm/escalation actions.
//
// It also holds an independent flap window over edge-reported link episodes
// (v1/gateway/linkhealth): episodes the lease never saw are aggregated per
// gateway for a flapping advisory and its settle, distinct from the lease
// flap window so one outage is never counted twice.
//
// The model is transport-independent and uses injected time so the
// SUSPECT/DOWN/flap state machine is unit-testable without a broker.
package linkhealth

import (
	"sync"
	"time"
)

// State is the application-link state of one gateway.
type State string

const (
	// StateUnknown is the initial state before any signal for a gateway.
	StateUnknown State = "UNKNOWN"

	// StateHealthy means the gateway has negotiated and is heartbeating.
	StateHealthy State = "HEALTHY"

	// StateSuspect means one heartbeat deadline was missed; the link may
	// recover without a DOWN episode.
	StateSuspect State = "SUSPECT"

	// StateDown means enough heartbeats were missed to declare the
	// application link down, or an ungraceful disconnect was observed.
	StateDown State = "DOWN"
)

// Reason labels why a state transition happened.
type Reason string

const (
	// ReasonConnected marks a healthy session start (hello completed).
	ReasonConnected Reason = "connected"

	// ReasonHeartbeat marks recovery from a fresh heartbeat.
	ReasonHeartbeat Reason = "heartbeat"

	// ReasonMissedBeat marks the HEALTHY -> SUSPECT transition.
	ReasonMissedBeat Reason = "missed_beat"

	// ReasonLeaseExpired marks the SUSPECT -> DOWN transition after the
	// down threshold.
	ReasonLeaseExpired Reason = "lease_expired"

	// ReasonTransportDown marks an ungraceful physical disconnect (the MQTT
	// LWT fired without a prior planned goodbye).
	ReasonTransportDown Reason = "transport_down"

	// ReasonPlannedDown marks a graceful goodbye shutdown; it is not an
	// alert condition and never counts toward flap.
	ReasonPlannedDown Reason = "planned_down"
)

// Frame is a heartbeat uplink. The watchdog is time based, so Seq is
// informational only.
type Frame struct {
	// Seq is the edge heartbeat sequence number.
	Seq int64 `json:"seq"`

	// ClientTime is the edge clock at send, epoch milliseconds; used only
	// for skew observability, not for lease decisions.
	ClientTime int64 `json:"client_time,omitempty"`
}

// Goodbye is the graceful-shutdown uplink (also the LWT payload topic).
type Goodbye struct {
	// Reason is the edge-supplied shutdown reason.
	Reason string `json:"reason,omitempty"`

	// Planned reports an intentional shutdown. The broker LWT path leaves
	// it false, which distinguishes a crash from a planned stop.
	Planned bool `json:"planned,omitempty"`
}

// plannedGoodbyeReasons always mean a planned shutdown even if an older edge
// omits the Planned bool.
var plannedGoodbyeReasons = map[string]struct{}{
	"planned": {}, "shutdown": {}, "upgrade": {}, "restart": {}, "planned_down": {},
}

// isPlanned reports whether a goodbye represents intentional downtime.
func (g Goodbye) isPlanned() bool {
	if g.Planned {
		return true
	}
	_, ok := plannedGoodbyeReasons[g.Reason]
	return ok
}

// Tuning configures the watchdog windows.
type Tuning struct {
	// HeartbeatInterval is the expected heartbeat period.
	HeartbeatInterval time.Duration

	// SuspectAfter is the silence before the first missed beat declares
	// SUSPECT (ADR: ~1.5 heartbeat intervals).
	SuspectAfter time.Duration

	// DownAfter is the total silence from the last heartbeat required to
	// declare DOWN (ADR: ~three heartbeat intervals).
	DownAfter time.Duration

	// SweepInterval paces the global time-based evaluation.
	SweepInterval time.Duration

	// FlapWindow bounds the flap-counting sliding window (ADR: 10m).
	FlapWindow time.Duration

	// FlapThreshold is the number of unplanned down episodes within
	// FlapWindow that triggers a flapping advisory (ADR: >=5).
	FlapThreshold int
}

// DefaultTuning returns the ADR-specified watchdog windows.
func DefaultTuning() Tuning {
	return Tuning{
		HeartbeatInterval: 30 * time.Second,
		SuspectAfter:      45 * time.Second,
		DownAfter:         90 * time.Second,
		SweepInterval:     10 * time.Second,
		FlapWindow:        10 * time.Minute,
		FlapThreshold:     5,
	}
}

// link is the per-gateway watchdog state.
type link struct {
	state    State
	lastBeat time.Time

	// flapStarts are unplanned DOWN episode starts inside the flap window.
	flapStarts []time.Time

	// planned marks the current down episode as a planned shutdown.
	planned bool

	// flapAdvised records whether a flap advisory is already outstanding
	// for the current burst, so one burst alerts once until it settles.
	flapAdvised bool

	// edgeFlapStarts are starts of edge-reported episodes (that did not
	// match a lease episode) inside the flap window.
	edgeFlapStarts []time.Time

	// edgeFlapAdvised records an outstanding edge flap burst, mirroring
	// flapAdvised for the edge window.
	edgeFlapAdvised bool
}

// Transition describes one state change for the caller to publish.
type Transition struct {
	TenantID  string
	GatewayID string
	From      State
	To        State
	Reason    Reason
	At        time.Time

	// Flapping is set on the lease-driven transition that crosses the flap
	// threshold.
	Flapping bool

	// Source marks edge-originated transitions: "edge" for the edge episode
	// flap crossing and its settle; empty for lease/watchdog transitions.
	Source string

	// EdgeFlap is set on the edge-episode transition crossing the flap
	// threshold.
	EdgeFlap bool

	// EdgeSettled is set when an edge flap burst has aged out of the window.
	EdgeSettled bool
}

// SourceEdge marks advisories derived from edge-reported episodes.
const SourceEdge = "edge"

// Detector holds watchdog state for all gateways. Construct with NewDetector.
type Detector struct {
	tune Tuning

	mu    sync.Mutex
	links map[string]*link
}

// dkey builds the per-gateway map key.
func dkey(tenantID, gatewayID string) string {
	return tenantID + "." + gatewayID
}

// NewDetector constructs a Detector, filling defaults for zero fields.
func NewDetector(tune Tuning) *Detector {
	base := DefaultTuning()
	if tune.HeartbeatInterval > 0 {
		base.HeartbeatInterval = tune.HeartbeatInterval
	}
	if tune.SuspectAfter > 0 {
		base.SuspectAfter = tune.SuspectAfter
	}
	if tune.DownAfter > 0 {
		base.DownAfter = tune.DownAfter
	}
	if tune.SweepInterval > 0 {
		base.SweepInterval = tune.SweepInterval
	}
	if tune.FlapWindow > 0 {
		base.FlapWindow = tune.FlapWindow
	}
	if tune.FlapThreshold > 0 {
		base.FlapThreshold = tune.FlapThreshold
	}
	return &Detector{tune: base, links: map[string]*link{}}
}

// getOrCreate returns the link for a key, creating it in StateUnknown.
func (d *Detector) getOrCreate(key string) *link {
	l, ok := d.links[key]
	if !ok {
		l = &link{state: StateUnknown}
		d.links[key] = l
	}
	return l
}

// Connect records a negotiated session and returns the transition to
// HEALTHY. It is idempotent for an already up link.
func (d *Detector) Connect(tenantID, gatewayID string, at time.Time) (Transition, bool) {
	key := dkey(tenantID, gatewayID)
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.getOrCreate(key)
	if l.state == StateHealthy || l.state == StateSuspect {
		return Transition{}, false
	}
	from := l.state
	l.state = StateHealthy
	l.lastBeat = at
	l.planned = false
	return Transition{
		TenantID: tenantID, GatewayID: gatewayID,
		From: from, To: StateHealthy, Reason: ReasonConnected, At: at,
	}, true
}

// Heartbeat records a fresh application heartbeat and returns a transition
// only when it recovers a DOWN link back to HEALTHY. A SUSPECT link is
// silently returned to HEALTHY (the missed beat raised no outage).
func (d *Detector) Heartbeat(tenantID, gatewayID string, at time.Time) (Transition, bool) {
	key := dkey(tenantID, gatewayID)
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.getOrCreate(key)
	l.lastBeat = at
	l.planned = false
	if l.state != StateDown {
		l.state = StateHealthy
		return Transition{}, false
	}
	from := l.state
	l.state = StateHealthy
	return Transition{
		TenantID: tenantID, GatewayID: gatewayID,
		From: from, To: StateHealthy, Reason: ReasonHeartbeat, At: at,
	}, true
}

// enterDown moves a link into DOWN, records an unplanned episode for flap
// detection and returns the resulting transition. Caller holds d.mu.
func (d *Detector) enterDown(l *link, tenantID, gatewayID string, reason Reason, at time.Time, planned bool) Transition {
	from := l.state
	l.state = StateDown
	l.planned = planned

	tr := Transition{
		TenantID: tenantID, GatewayID: gatewayID,
		From: from, To: StateDown, Reason: reason, At: at,
	}
	if planned {
		// Planned shutdowns are not outages and never count toward flap.
		return tr
	}
	wasBelow := len(l.flapStarts) < d.tune.FlapThreshold
	l.flapStarts = append(l.flapStarts, at)
	d.pruneFlapsLocked(l, at)
	if len(l.flapStarts) >= d.tune.FlapThreshold {
		// Alert only on the crossing; prune re-arms a detector whose
		// burst aged out of the window.
		tr.Flapping = wasBelow && !l.flapAdvised
		l.flapAdvised = true
	} else {
		l.flapAdvised = false
	}
	return tr
}

// pruneFlapTimes drops times older than the flap window, reusing the input
// slice backing array.
func pruneFlapTimes(times []time.Time, at time.Time, window time.Duration) []time.Time {
	cutoff := at.Add(-window)
	kept := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	return kept
}

// pruneFlapsLocked drops episode starts older than the flap window. Caller
// holds d.mu.
func (d *Detector) pruneFlapsLocked(l *link, at time.Time) {
	l.flapStarts = pruneFlapTimes(l.flapStarts, at, d.tune.FlapWindow)
}

// Goodbye records a shutdown notice. A planned goodbye moves to DOWN with
// reason planned_down and does not count as a flap; an unexpected goodbye is
// treated as an ungraceful down.
func (d *Detector) Goodbye(tenantID, gatewayID string, g Goodbye, at time.Time) (Transition, bool) {
	key := dkey(tenantID, gatewayID)
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.getOrCreate(key)
	if l.state == StateDown {
		return Transition{}, false
	}
	planned := g.isPlanned()
	reason := ReasonTransportDown
	if planned {
		reason = ReasonPlannedDown
	}
	return d.enterDown(l, tenantID, gatewayID, reason, at, planned), true
}

// TransportClosed records a physical MQTT disconnect. When the session is
// already down (the LWT arrives after a planned goodbye, for example) it is
// a no-op; otherwise it is an ungraceful transport down.
func (d *Detector) TransportClosed(tenantID, gatewayID string, at time.Time) (Transition, bool) {
	key := dkey(tenantID, gatewayID)
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.getOrCreate(key)
	if l.state == StateDown {
		return Transition{}, false
	}
	return d.enterDown(l, tenantID, gatewayID, ReasonTransportDown, at, false), true
}

// ObserveEdgeEpisode records one edge-reported episode start for the
// independent edge flap window (only episodes that did not match a lease
// episode are fed in, so one outage is never counted twice). It returns the
// flap-crossing transition once per burst; the burst re-arms after its
// episodes age out of the window.
func (d *Detector) ObserveEdgeEpisode(tenantID, gatewayID string, at time.Time) (Transition, bool) {
	key := dkey(tenantID, gatewayID)
	d.mu.Lock()
	defer d.mu.Unlock()
	l := d.getOrCreate(key)

	wasBelow := len(l.edgeFlapStarts) < d.tune.FlapThreshold
	l.edgeFlapStarts = append(l.edgeFlapStarts, at)
	l.edgeFlapStarts = pruneFlapTimes(l.edgeFlapStarts, at, d.tune.FlapWindow)

	if len(l.edgeFlapStarts) >= d.tune.FlapThreshold {
		if wasBelow && !l.edgeFlapAdvised {
			l.edgeFlapAdvised = true
			return Transition{
				TenantID: tenantID, GatewayID: gatewayID,
				From: StateDown, To: StateDown,
				Reason: ReasonTransportDown, At: at,
				Source: SourceEdge, EdgeFlap: true,
			}, true
		}
		return Transition{}, false
	}
	// The window slid below the threshold on a new episode: the detector is
	// re-armed without emitting a settle, because the burst never actually
	// went quiet.
	l.edgeFlapAdvised = false
	return Transition{}, false
}

// Evaluate advances every link based on elapsed silence and returns all
// transitions produced at now (SUSPECT on first missed beat, DOWN when the
// lease elapsed). It also closes an edge flap burst whose episodes have all
// aged out of the window. Invoked by the global sweep; there is no
// per-gateway timer.
func (d *Detector) Evaluate(now time.Time) []Transition {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []Transition
	for key, l := range d.links {
		tenantID, gatewayID := splitDKey(key)
		switch l.state {
		case StateHealthy:
			if !l.lastBeat.IsZero() && now.Sub(l.lastBeat) >= d.tune.SuspectAfter {
				l.state = StateSuspect
				out = append(out, Transition{
					TenantID: tenantID, GatewayID: gatewayID,
					From: StateHealthy, To: StateSuspect,
					Reason: ReasonMissedBeat, At: now,
				})
			}
		case StateSuspect:
			if !l.lastBeat.IsZero() && now.Sub(l.lastBeat) >= d.tune.DownAfter {
				out = append(out, d.enterDown(l, tenantID, gatewayID,
					ReasonLeaseExpired, now, false))
			}
		}
		if l.edgeFlapAdvised {
			l.edgeFlapStarts = pruneFlapTimes(l.edgeFlapStarts, now, d.tune.FlapWindow)
			if len(l.edgeFlapStarts) < d.tune.FlapThreshold {
				// No new episode arrived for a full window: announce the
				// settle and re-arm for the next burst.
				l.edgeFlapAdvised = false
				out = append(out, Transition{
					TenantID: tenantID, GatewayID: gatewayID,
					From: StateDown, To: StateDown,
					Reason: ReasonTransportDown, At: now,
					Source: SourceEdge, EdgeSettled: true,
				})
			}
		}
	}
	return out
}

// Remove deletes a gateway's watchdog state. It is called when a gateway is
// decommissioned or unbound so the detector does not retain stale entries.
func (d *Detector) Remove(tenantID, gatewayID string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.links, dkey(tenantID, gatewayID))
}

// State returns the current state of one gateway (Unknown when unseen).
func (d *Detector) State(tenantID, gatewayID string) State {
	d.mu.Lock()
	defer d.mu.Unlock()
	l, ok := d.links[dkey(tenantID, gatewayID)]
	if !ok {
		return StateUnknown
	}
	return l.state
}

// splitDKey splits "<tenant>.<gateway>" on the first dot. Tenant and gateway
// ids are UUIDs (no dots), so the first dot is the separator.
func splitDKey(key string) (tenantID, gatewayID string) {
	for i := 0; i < len(key); i++ {
		if key[i] == '.' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
