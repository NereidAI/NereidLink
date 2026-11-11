// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package linknotify turns ephemeral ADR-0026 gateway link-health
// advisories into operator-facing notifications and durable escalation.
//
// It is the missing subscriber for `nl.linkhealth.*.*`:
//
//   - An unplanned DOWN (lease expiry, transport crash, or a flap crossing)
//     opens one deduplicated MAJOR alarm, which rides the existing ADR-0019
//     notify pipeline (inbox, webhook, SMTP) with no parallel sender.
//   - A RECOVERED advisory clears that alarm and closes the episode.
//   - A PLANNED_DOWN advisory is ignored for alarming: scheduled maintenance
//     must not page anyone and must not appear in the outage digest.
//   - An episode still open after the escalation threshold is bumped once to
//     CRITICAL (ADR: "escalate after 30 minutes without recovery").
//   - Once per UTC calendar day each affected tenant gets a 24-hour outage
//     digest (ADR: "24-hour summary").
//
// Episodes and the once-per-day digest cursor are persisted
// (migration 0039) so escalation and digest behavior survive restarts and
// are at-most-once across a queue-group failover.
//
// Migration 0075 adds edge-reported episodes: ApplyEdgeEpisode corrects the
// matching lease row with the edge ground truth or inserts a closed
// alarm-free episode for outages the lease never saw; edge flap bursts raise
// a separate GATEWAY_LINK_FLAPPING alarm that flap_settled clears.
package linknotify

import "time"

// SubjectWildcard matches every per-gateway link-health advisory.
const SubjectWildcard = "nl.linkhealth.*.*"

// QueueGroup load-balances advisory consumption so a cluster sends each
// escalation/digest once.
const QueueGroup = "nl-linknotify"

// Advisory kind values, matching internal/linkhealth.
const (
	kindSuspect   = "suspect"
	kindDown      = "down"
	kindPlanned   = "planned_down"
	kindRecovered = "recovered"
	kindFlapping  = "flapping"
)

// AlarmType classifies gateway link outages in the alarm ledger.
const AlarmType = "GATEWAY_LINK_DOWN"

// EdgeFlapAlarmType classifies flapping detected from edge-reported episodes
// that the server lease never saw; it is a distinct alarm type so operators
// can distinguish edge-detected micro-flap bursts.
const EdgeFlapAlarmType = "GATEWAY_LINK_FLAPPING"

// source values carried by advisories and used to branch handling.
const (
	sourceEdge = "edge"
)

// kindFlapSettled is the advisory that closes an edge flap alarm once the
// burst ages out of the flap window with no new episode.
const kindFlapSettled = "flap_settled"

// edgeFlapDedupeKey builds the stable alarm dedupe key for one gateway's
// edge flap burst, so repeated crossing signals while the burst is active
// update one alarm instead of opening many.
func edgeFlapDedupeKey(gatewayID string) string {
	return "gateway-link-flap:" + gatewayID
}

// InitialSeverity is assigned when an unplanned outage opens; the
// escalation sweep later raises a long episode to CRITICAL.
const (
	InitialSeverity   = "MAJOR"
	EscalatedSeverity = "CRITICAL"
)

// dedupeKey builds the stable alarm dedupe key for one gateway, so repeated
// DOWN advisories update one open alarm instead of opening many.
func dedupeKey(gatewayID string) string {
	return "gateway-link-down:" + gatewayID
}

// advisory is the decoded nl.linkhealth payload (a subset of
// linkhealth.Advisory; only fields this worker acts on).
type advisory struct {
	Kind      string    `json:"kind"`
	Time      time.Time `json:"time"`
	TenantID  string    `json:"tenant_id"`
	GatewayID string    `json:"gateway_id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Reason    string    `json:"reason"`

	// Source distinguishes edge-originated advisories (edge episode flap
	// crossing and its settle) from lease-driven ones.
	Source string `json:"source,omitempty"`
}

// isDown reports whether the advisory denotes an unplanned DOWN edge. A
// flapping crossing carries its own kind but is still a DOWN transition.
func (a advisory) isDown() bool {
	return a.Kind == kindDown || a.Kind == kindFlapping
}

// Tuning configures escalation and digest timing.
type Tuning struct {
	// EscalationAfter bounds how long an episode may stay MAJOR before it
	// is bumped once to CRITICAL.
	EscalationAfter time.Duration

	// EscalationSweepInterval paces the open-episode scan.
	EscalationSweepInterval time.Duration

	// DigestEnabled turns the 24h summary on.
	DigestEnabled bool

	// DigestHourUTC is the UTC hour (0-23) at which the previous-UTC-day
	// digest is sent.
	DigestHourUTC int

	// DigestSweepInterval paces the clock check for the daily send.
	DigestSweepInterval time.Duration
}

// DefaultTuning returns the ADR defaults: escalate after 30 minutes, digest
// at 08:00 UTC.
func DefaultTuning() Tuning {
	return Tuning{
		EscalationAfter:         30 * time.Minute,
		EscalationSweepInterval: time.Minute,
		DigestEnabled:           true,
		DigestHourUTC:           8,
		DigestSweepInterval:     time.Minute,
	}
}

// Episode is one persisted unplanned gateway outage.
type Episode struct {
	ID        string
	TenantID  string
	GatewayID string
	StartedAt time.Time
	Reason    string
	AlarmID   string
}

// Episode source values recorded in gateway_link_episode.source (migration
// 0075).
const (
	EpisodeSourceLease = "lease"
	EpisodeSourceEdge  = "edge"
)

// EdgeEpisode is a validated edge-local link watchdog episode to apply.
// DownAt/UpAt are the edge local ground truth.
type EdgeEpisode struct {
	TenantID  string
	GatewayID string
	DownAt    time.Time
	UpAt      time.Time
	Reason    string
}

// Apply outcome status values.
const (
	// ApplyAccepted means a lease row was corrected or a new closed episode
	// inserted.
	ApplyAccepted = "accepted"

	// ApplyDuplicate means (gateway, DownAt) is already recorded.
	ApplyDuplicate = "duplicate"
)

// ApplyResult reports what ApplyEdgeEpisode did. MatchedLease is true when a
// server lease episode was corrected with the edge boundaries; false when a
// new closed edge-only episode was inserted. Callers count only unmatched
// episodes toward the edge flap window, so one outage is never counted twice.
type ApplyResult struct {
	Status       string
	MatchedLease bool
}
