// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// negotiator.go holds the stateful Negotiator: it runs one hello
// exchange, records the resulting session and answers session lookups.

package edge

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"sync"
	"time"
)

// Negotiator performs hello negotiation and keeps the negotiated
// sessions for the lifetime of the process. Session state is local to
// the node that terminated the transport connection; a reconnecting
// edge lands on the same cluster and resumes by session id later.
type Negotiator struct {
	cfg Config

	mu       sync.RWMutex
	sessions map[string]Session
}

// NewNegotiator constructs a Negotiator with the given policy.
func NewNegotiator(cfg Config) *Negotiator {
	return &Negotiator{
		cfg:      cfg,
		sessions: map[string]Session{},
	}
}

// intersectFormats keeps the gateway's preferred formats that the server can
// render, preserving the gateway's order. When the gateway declared none, the
// server's full set is offered so the edge learns what it may request.
func intersectFormats(preferred, supported []string) []string {
	if len(supported) == 0 {
		return nil
	}
	if len(preferred) == 0 {
		return slices.Clone(supported)
	}
	out := make([]string, 0, len(preferred))
	for _, want := range preferred {
		if slices.Contains(supported, want) {
			out = append(out, want)
		}
	}
	return out
}

// negotiateInference computes the accepted edge model-inference capability
// (ADR-0039). It returns nil when the edge did not advertise a block, the
// cloud is configured to accept none, the contract version differs, or no
// backend protocol family intersects — nil is the cloud's "do not enable"
// answer and keeps the inference key off the ack. The gateway's backend
// preference order is preserved.
func negotiateInference(advert *InferenceAdvert, cfg Config) *InferenceAdvert {
	if advert == nil {
		return nil
	}
	if cfg.SupportedInferenceContract == "" || advert.Contract != cfg.SupportedInferenceContract {
		return nil
	}
	backends := intersectFormats(advert.Backends, cfg.SupportedInferenceBackends)
	if len(backends) == 0 {
		return nil
	}
	return &InferenceAdvert{Contract: advert.Contract, Backends: backends}
}

// Negotiate validates hello and returns the wire ack. On success the
// returned Session is already recorded and IsNegotiated will report it.
// A second hello for the same connection is rejected by the caller;
// here an already-known edge simply receives a fresh session.
func (n *Negotiator) Negotiate(tenantID string, hello Hello) (Ack, Session, error) {
	if hello.V != 1 {
		return Ack{}, Session{}, ErrInvalidHello
	}
	if hello.EdgeID == "" {
		return Ack{}, Session{}, ErrInvalidHello
	}

	selected, compatible := selectVersion(hello.Proto, n.cfg.Proto)
	now := time.Now().UTC()

	ack := Ack{
		Server: ServerInfo{
			Min:     n.cfg.Proto.Min,
			Max:     n.cfg.Proto.Max,
			Version: n.cfg.ServerVersion,
		},
		Limits: Limits{
			HeartbeatS:    int(n.cfg.Heartbeat.Seconds()),
			HelloTimeoutS: int(n.cfg.HelloTimeout.Seconds()),
			Upload: UploadLimits{
				MaxMB: n.cfg.MaxUploadMB,
				QPS:   n.cfg.UploadQPS,
			},
		},
		Features: []string{},
	}

	if !compatible {
		ack.Status = StatusIncompatibleVersion
		ack.Reason = "no overlapping protocol major version"
		return ack, Session{}, nil
	}

	enabled, missing := featureIntersection(hello.Features, n.cfg.SupportedFeatures)
	if missing != "" {
		ack.Status = StatusIncompatibleFeatures
		ack.Reason = "required feature unsupported: " + missing
		ack.Selected = selected
		return ack, Session{}, nil
	}

	// Device-profile downlink (ADR-0038): enable only when the profiles
	// feature was negotiated, and echo the formats this session may render.
	// The gateway's order is preserved; an unknown preference is dropped so
	// the edge never receives a format it cannot apply.
	if slices.Contains(enabled, FeatureProfiles) {
		ack.ProfileFormats = intersectFormats(hello.ProfileFormats, n.cfg.SupportedProfileFormats)
	}

	// Edge model inference (ADR-0039): enable only when the inference
	// feature was negotiated and the edge's advertisement is acceptable;
	// echo the recognized contract and the backend intersection. A nil
	// result is the cloud's "not accepted" signal, so the edge stays on
	// the generic path (no models segment, no edge-inference specs).
	if slices.Contains(enabled, FeatureInference) {
		ack.Inference = negotiateInference(hello.Inference, n.cfg)
	}

	sessionID, err := newSessionID()
	if err != nil {
		return Ack{}, Session{}, err
	}

	resumeAccepted := false
	var resumed Session
	// Session resume (ADR-0026 deferred slice): a reconnecting edge may name
	// its prior session. Sessions are in-process on the node that served the
	// transport, so resume only succeeds on the same node, for the same
	// tenant and edge identity, within the TTL. Success reuses the prior
	// session id (so the edge correlates state) and marks accepted=true. Any
	// mismatch/expiry/unknown id silently falls through to a fresh session
	// with accepted=false; resume is an optimization, never a gate.
	// Downlink catch-up is independent: the post-hello redrive resends the
	// full desired connector set idempotently.
	n.mu.RLock()
	if hello.Resume.SessionID != "" && n.cfg.SessionTTL >= 0 {
		if prior, ok := n.sessions[hello.Resume.SessionID]; ok &&
			prior.TenantID == tenantID && prior.EdgeID == hello.EdgeID &&
			now.Sub(prior.LastSeen) <= n.cfg.SessionTTL {
			resumeAccepted = true
			resumed = prior
			sessionID = prior.ID
		}
	}
	n.mu.RUnlock()

	offsetMS := now.UnixMilli() - hello.ClientTime

	ack.SessionID = sessionID
	ack.Status = StatusOK
	ack.Selected = selected
	ack.Features = enabled
	ack.Time = TimeInfo{
		ServerTime:   now.UnixMilli(),
		OffsetMS:     offsetMS,
		ClockWarning: abs64(offsetMS) > n.cfg.ClockSkewWarn.Milliseconds(),
	}
	ack.Resume = ResumeResult{Accepted: resumeAccepted}

	session := Session{
		ID:           sessionID,
		EdgeID:       hello.EdgeID,
		TenantID:     tenantID,
		Proto:        selected,
		Features:     enabled,
		NegotiatedAt: now,
		LastSeen:     now,
	}
	if resumeAccepted {
		// Preserve the original negotiation timestamp while advancing the
		// activity anchor so each accepted resume extends the TTL window.
		session.NegotiatedAt = resumed.NegotiatedAt
	}

	n.mu.Lock()
	// Opportunistically drop expired sessions so a long-lived node cannot
	// accumulate tokens for edges that never reconnect. This is O(n) but
	// negotiation is low-frequency and the map is bounded by live/recent
	// gateways.
	n.evictExpiredLocked(now)
	n.sessions[sessionID] = session
	n.mu.Unlock()

	return ack, session, nil
}

// evictExpiredLocked removes sessions older than the resume TTL. Callers must
// hold n.mu. With TTL disabled (negative) nothing is evicted.
func (n *Negotiator) evictExpiredLocked(now time.Time) {
	if n.cfg.SessionTTL < 0 {
		return
	}
	for id, s := range n.sessions {
		if now.Sub(s.LastSeen) > n.cfg.SessionTTL {
			delete(n.sessions, id)
		}
	}
}

// Session returns one recorded session by id.
func (n *Negotiator) Session(id string) (Session, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	session, ok := n.sessions[id]
	return session, ok
}

// RemoveSession drops a session, used on connection close.
func (n *Negotiator) RemoveSession(id string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	delete(n.sessions, id)
}

// abs64 returns the absolute value of x without importing math.
func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// newSessionID returns 16 random bytes rendered as an s_-prefixed hex
// token, matching the session-id shape shown in the protocol examples.
func newSessionID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return "s_" + hex.EncodeToString(buf), nil
}
