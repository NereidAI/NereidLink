// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// lease.go detects gateways that stop sending manifests. KV key TTL expiry is
// silent (no watch event, no tombstone), so two complementary mechanisms run
// without per-gateway timers: a watcher that reacts to explicit lease
// deletion, and a global sweep that tracks each lease's last-seen time and
// derives sub-device OFFLINE once a missing lease is older than LeaseTTL.

package manifest

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/internal/presence"
)

// leaseTracker records the last time each gateway lease was observed and
// which expired leases have already been derived, so an expiry derives
// offline exactly once even though both the watcher and sweep can notice it.
type leaseTracker struct {
	mu       sync.Mutex
	lastBeat map[string]time.Time
	derived  map[string]struct{}
}

func newLeaseTracker() *leaseTracker {
	return &leaseTracker{
		lastBeat: map[string]time.Time{},
		derived:  map[string]struct{}{},
	}
}

// observe records a live lease sighting and clears any "already derived"
// marker so a gateway that resumes and expires again is handled anew.
func (t *leaseTracker) observe(key string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if prev, ok := t.lastBeat[key]; !ok || at.After(prev) {
		t.lastBeat[key] = at
	}
	delete(t.derived, key)
}

// shouldDerive reports whether a tracked-but-absent key is now older than
// ttl and has not been derived yet.
func (t *leaseTracker) shouldDerive(key string, now time.Time, ttl time.Duration) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	beat, ok := t.lastBeat[key]
	if !ok || !now.After(beat.Add(ttl)) {
		return false
	}
	if _, done := t.derived[key]; done {
		return false
	}
	t.derived[key] = struct{}{}
	return true
}

// markDerived records that an absent key has already been derived, so the
// sweep does not re-derive (and re-advertise) an explicit deletion once the
// TTL clock also passes.
func (t *leaseTracker) markDerived(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.derived[key] = struct{}{}
}

// handleLeaseWatch reacts to explicit lease deletion. Silent TTL expiry is
// covered by sweep; Put events refresh the tracker.
func (r *Reconciler) handleLeaseWatch(ctx context.Context, entry jetstream.KeyValueEntry) {
	if entry == nil {
		return
	}
	key := entry.Key()
	switch entry.Operation() {
	case jetstream.KeyValuePut:
		var value leaseValue
		if err := json.Unmarshal(entry.Value(), &value); err != nil {
			return
		}
		r.tracker.observe(key, value.At)
	case jetstream.KeyValueDelete, jetstream.KeyValuePurge:
		if r.deriveExpired(ctx, key, "explicit lease deletion") {
			// Prevent the sweep from deriving the same deletion again
			// once the TTL window passes.
			r.tracker.markDerived(key)
		}
	}
}

// sweep lists live leases, refreshes observed times, and derives offline for
// tracked gateways whose lease has silently expired.
func (r *Reconciler) sweep(ctx context.Context) {
	if r.bucket == nil {
		return
	}
	lister, err := r.bucket.ListKeys(ctx)
	if err != nil {
		// An empty bucket surfaces as a stream error in the NATS client;
		// nothing is tracked yet, so there is nothing to sweep.
		r.log.Debug("manifest lease list failed", "error", err)
		return
	}
	seen := map[string]struct{}{}
	now := time.Now().UTC()
	for key := range lister.Keys() {
		entry, err := r.bucket.Get(ctx, key)
		if err != nil {
			continue
		}
		var value leaseValue
		if err := json.Unmarshal(entry.Value(), &value); err != nil {
			continue
		}
		seen[key] = struct{}{}
		r.tracker.observe(key, value.At)
	}

	for _, key := range r.tracker.keys() {
		if _, live := seen[key]; live {
			continue
		}
		if r.tracker.shouldDerive(key, now, r.tune.LeaseTTL) {
			r.deriveExpired(ctx, key, "manifest lease sweep")
		}
	}
}

// deriveExpired parses a lease key and drives its attached sub-devices
// offline. It reports whether the expiry was handled, so callers can mark
// the key derived and avoid duplicate advisories. A failure clears the
// derived marker so the next sweep retries.
func (r *Reconciler) deriveExpired(ctx context.Context, key, why string) bool {
	tenantID, gatewayID, ok := splitLeaseKey(key)
	if !ok {
		return false
	}
	n, err := r.presence.DeriveGatewayOffline(ctx, tenantID, gatewayID,
		presence.ReasonManifestLeaseExpired)
	if err != nil {
		r.log.Error("manifest lease expiry derive offline failed",
			"gateway", gatewayID, "error", err)
		r.tracker.retry(key)
		return false
	}
	r.emitLeaseExpiredAdvisory(ctx, tenantID, gatewayID, n)
	// Notify sibling owners (ADR-0039 model inventory) to mark their
	// gateway-scoped state stale. Best-effort and independent of device
	// offline derivation; each sink logs its own failures.
	for _, sink := range r.leaseExpirySinks {
		sink.OnManifestLeaseExpired(ctx, tenantID, gatewayID)
	}
	if n > 0 {
		r.log.Warn("manifest lease expired, sub-devices derived offline",
			"gateway", gatewayID, "count", n, "why", why)
	} else {
		r.log.Info("manifest lease expired with no attached sub-devices",
			"gateway", gatewayID, "why", why)
	}
	return true
}

// keys returns a snapshot of tracked lease keys.
func (t *leaseTracker) keys() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.lastBeat))
	for key := range t.lastBeat {
		out = append(out, key)
	}
	return out
}

// retry clears the derived marker so a failed derivation is retried.
func (t *leaseTracker) retry(key string) {
	t.mu.Lock()
	delete(t.derived, key)
	t.mu.Unlock()
}

// splitLeaseKey splits "<tenant-uuid>.<gateway-uuid>" on the first dot.
func splitLeaseKey(key string) (tenantID, gatewayID string, ok bool) {
	tenantID, gatewayID, found := strings.Cut(key, ".")
	if !found || tenantID == "" || gatewayID == "" {
		return "", "", false
	}
	return tenantID, gatewayID, true
}
