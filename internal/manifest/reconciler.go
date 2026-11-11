// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// reconciler.go wires manifest reconciliation to its collaborators and
// owns the gateway manifest lease bucket. Reconciliation is event-driven
// (one Reconcile call per received frame); the only background work is a
// lease watch and a global sweep that detects gateways which stopped
// reporting, with no per-gateway timer.

package manifest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/nereidai/nereidlink/internal/presence"
)

// BucketManifest stores one live lease key per connected gateway. The
// bucket TTL silently expires keys that stop refreshing; key-level TTL is
// unavailable on KV so the lifetime is set on the bucket.
const BucketManifest = "gw-manifest"

// SubjectManifestPrefix is the Core NATS root for drift advisories,
// suffixed by "<tenant>.<gateway>".
const SubjectManifestPrefix = "nl.manifest"

// DesiredLister returns the bound, serviceable sub-devices of one gateway.
// It is implemented by *registry.Admin.
type DesiredLister interface {
	ListGatewaySubdevices(ctx context.Context, tenantID, gatewayID string) ([]string, error)
}

// Presence is the subset of the presence service reconciliation drives.
// It is implemented by *presence.Service.
type Presence interface {
	EnsureGatewayOnline(ctx context.Context, id presence.Identity, gatewayID string) (bool, error)
	EnsureGatewayOffline(ctx context.Context, id presence.Identity, gatewayID, reason string) (bool, error)
	DeriveGatewayOffline(ctx context.Context, tenantID, gatewayID, reason string) (int, error)
}

// Tuning configures the lease bucket and its backstop sweep.
type Tuning struct {
	// JSReplicas is the JetStream Raft factor for the lease KV (1/3/5).
	JSReplicas int

	// LeaseTTL is how long a gateway's last manifest keeps its lease
	// alive. It should span several manifest intervals.
	LeaseTTL time.Duration

	// SweepInterval paces the global sweep that catches silent TTL
	// expiry, which raises no KV watch event.
	SweepInterval time.Duration
}

// leaseValue is the JSON stored per gateway.
type leaseValue struct {
	TenantID  string    `json:"tenant_id"`
	GatewayID string    `json:"gateway_id"`
	At        time.Time `json:"at"`
	Revision  int64     `json:"revision"`
}

// leaseWriter is the small KV surface reconciliation needs per frame. The
// full jetstream.KeyValue (used by Run's watcher and sweep) satisfies it;
// narrowing it keeps Reconcile unit-testable without an embedded broker.
type leaseWriter interface {
	Put(ctx context.Context, key string, value []byte) (uint64, error)
}

// Reconciler reconciles gateway manifests and owns their leases.
type Reconciler struct {
	js        jetstream.JetStream
	publisher advisoryPublisher
	pool      *pgxpool.Pool
	desired   DesiredLister
	presence  Presence
	tune      Tuning
	log       *slog.Logger

	bucket  jetstream.KeyValue
	leases  leaseWriter
	tracker *leaseTracker

	// leaseExpirySinks are notified after a gateway's manifest lease is
	// derived expired (ADR-0039 model inventory staleness). Wired once at
	// construction before Run, so no lock is required.
	leaseExpirySinks []LeaseExpirySink

	// initMu serializes lazy bucket creation so a manifest that arrives
	// before Run starts the watcher cannot observe a nil lease store.
	initMu sync.Mutex
}

// LeaseExpirySink is notified when a gateway's gw-manifest lease expires. It
// lets sibling owners (ADR-0039 model inventory) mark their gateway-scoped
// state stale without the manifest package importing them. A sink error is
// the sink's own concern; it never affects device offline derivation.
type LeaseExpirySink interface {
	OnManifestLeaseExpired(ctx context.Context, tenantID, gatewayID string)
}

// AddLeaseExpirySink registers a sink notified on gw-manifest lease expiry.
// Call before Run.
func (r *Reconciler) AddLeaseExpirySink(sink LeaseExpirySink) {
	if sink != nil {
		r.leaseExpirySinks = append(r.leaseExpirySinks, sink)
	}
}

// New constructs a Reconciler. Run must be called to create the lease
// bucket and start the watch and sweep before Reconcile is used.
func New(js jetstream.JetStream, nc *nats.Conn, pool *pgxpool.Pool,
	desired DesiredLister, presence Presence, tune Tuning, log *slog.Logger) *Reconciler {
	if log == nil {
		log = slog.Default()
	}
	return &Reconciler{
		js:        js,
		publisher: nc,
		pool:      pool,
		desired:   desired,
		presence:  presence,
		tune:      tune,
		log:       log,
		tracker:   newLeaseTracker(),
	}
}

// Run ensures the lease bucket and runs the watch and sweep until ctx is
// canceled.
func (r *Reconciler) Run(ctx context.Context) error {
	if err := r.ensureBucket(ctx); err != nil {
		return err
	}

	ticker := time.NewTicker(r.tune.SweepInterval)
	defer ticker.Stop()

	watcher, err := r.bucket.WatchAll(ctx)
	if err != nil {
		return fmt.Errorf("watch manifest leases: %w", err)
	}
	stop := func() { _ = watcher.Stop() }
	defer stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case entry, ok := <-watcher.Updates():
			if !ok {
				return nil
			}
			r.handleLeaseWatch(ctx, entry)
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

// ensureBucket creates the lease bucket exactly once. It is safe to call
// from both Run (before the watcher starts) and Reconcile (an early manifest
// that arrives before Run has created the bucket), so a frame is never
// dropped with "lease store not initialized" during startup.
func (r *Reconciler) ensureBucket(ctx context.Context) error {
	r.initMu.Lock()
	defer r.initMu.Unlock()
	if r.bucket != nil {
		return nil
	}
	if r.js == nil {
		return errors.New("manifest jetstream unavailable")
	}
	bucket, err := r.js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   BucketManifest,
		TTL:      r.tune.LeaseTTL,
		Replicas: jsReplicas(r.tune.JSReplicas),
	})
	if err != nil {
		return fmt.Errorf("ensure manifest KV: %w", err)
	}
	r.bucket = bucket
	r.leases = bucket
	return nil
}

// Diff partitions devices into the in-sync, missing and unregistered sets.
// desired is the bound set from the registry; reported is the gateway's
// full online list. Each output slice is sorted and de-duplicated.
func Diff(desired, reported []string) Class {
	desiredSet := toSet(desired)
	reportedSet := toSet(reported)

	c := Class{}
	for id := range reportedSet {
		if _, ok := desiredSet[id]; ok {
			c.InSync = append(c.InSync, id)
		} else {
			c.Unregistered = append(c.Unregistered, id)
		}
	}
	for id := range desiredSet {
		if _, ok := reportedSet[id]; !ok {
			c.Missing = append(c.Missing, id)
		}
	}
	sort.Strings(c.InSync)
	sort.Strings(c.Missing)
	sort.Strings(c.Unregistered)
	return c
}

// toSet de-duplicates a device list into a set.
func toSet(ids []string) map[string]struct{} {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		set[id] = struct{}{}
	}
	return set
}

// Reconcile applies one manifest frame: it converges presence toward the
// reported list, refreshes the gateway lease, emits a drift advisory on
// mismatch and records a history row. Presence convergence and the advisory
// are authoritative; a history-write failure is logged but does not fail
// reconciliation, since the operational state has already converged.
func (r *Reconciler) Reconcile(ctx context.Context, tenantID, gatewayID string, frame Frame) (Report, error) {
	now := time.Now().UTC()
	report := Report{
		TenantID:     tenantID,
		GatewayID:    gatewayID,
		At:           now,
		Revision:     frame.Revision,
		MissedCycles: frame.MissedCycles,
	}

	desired, err := r.desired.ListGatewaySubdevices(ctx, tenantID, gatewayID)
	if err != nil {
		return report, fmt.Errorf("list desired sub-devices: %w", err)
	}

	report.Class = Diff(desired, frame.Devices)

	for _, deviceID := range report.Class.InSync {
		promoted, err := r.presence.EnsureGatewayOnline(ctx,
			presence.Identity{TenantID: tenantID, DeviceID: deviceID}, gatewayID)
		if err != nil {
			return report, fmt.Errorf("ensure online %s: %w", deviceID, err)
		}
		if promoted {
			report.OnlinePromoted++
		}
	}
	for _, deviceID := range report.Class.Missing {
		derived, err := r.presence.EnsureGatewayOffline(ctx,
			presence.Identity{TenantID: tenantID, DeviceID: deviceID},
			gatewayID, presence.ReasonManifest)
		if err != nil {
			return report, fmt.Errorf("ensure offline %s: %w", deviceID, err)
		}
		if derived {
			report.OfflineDerived++
		}
	}

	if err := r.refreshLease(ctx, tenantID, gatewayID, now, frame.Revision); err != nil {
		// A failed lease refresh can make the sweep wrongly derive offline,
		// so surface it rather than silently treating the cycle as healthy.
		return report, fmt.Errorf("refresh manifest lease: %w", err)
	}

	if report.Class.Drift() {
		r.emitAdvisory(ctx, report)
	}
	if err := r.recordHistory(ctx, report); err != nil {
		r.log.Warn("manifest history insert failed",
			"gateway", gatewayID, "error", err)
	}
	return report, nil
}

// refreshLease writes the per-gateway lease, resetting its TTL.
func (r *Reconciler) refreshLease(ctx context.Context, tenantID, gatewayID string,
	at time.Time, revision int64) error {
	if r.leases == nil {
		// Run has not created the bucket yet (a frame arrived during
		// startup). Lazily ensure it so the frame is still applied.
		if err := r.ensureBucket(ctx); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(leaseValue{
		TenantID:  tenantID,
		GatewayID: gatewayID,
		At:        at,
		Revision:  revision,
	})
	if err != nil {
		return err
	}
	_, err = r.leases.Put(ctx, leaseKey(tenantID, gatewayID), raw)
	return err
}

// leaseKey builds the manifest lease key.
func leaseKey(tenantID, gatewayID string) string {
	return tenantID + "." + gatewayID
}

// jsReplicas normalizes the configured JetStream Raft factor.
func jsReplicas(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
