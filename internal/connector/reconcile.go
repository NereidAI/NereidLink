// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// reconcile.go implements ADR-0026 connector manifest reconciliation. A
// negotiated gateway periodically publishes the full list of connector
// instances it has applied (spec_id, applied revision, optional config hash).
// NL partitions that reported set against the desired set into in_sync /
// missing / stale / drifted / extra. By default the result is observational:
// a single per-gateway GATEWAY_CONNECTOR_DRIFT alarm summarizes any
// divergence and is cleared once converged. When strict reconciliation is
// enabled NL makes desired win: it re-pushes missing/stale specs, bumps the
// desired revision and re-pushes a same-revision content-mutated spec (the
// edge otherwise dedupes an identical revision), and deletes extra instances.
package connector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// canonicalHash is NL's expected value of an edge's reported config_hash. It
// is the lowercase-hex sha256 over the compact, key-sorted JSON document
// {"config": <effective config>, "mappings": <effective mappings>}. Both
// sides canonicalize the same effective document, so whitespace and object
// key order never cause a false drift. The edge protocol pins this algorithm
// so implementations in other languages reproduce it byte for byte.
func canonicalHash(config, mappings json.RawMessage) string {
	var c, m any
	if len(config) > 0 {
		if err := json.Unmarshal(config, &c); err != nil {
			c = nil
		}
	}
	if len(mappings) > 0 {
		if err := json.Unmarshal(mappings, &m); err != nil {
			m = []any{}
		}
	}
	// encoding/json marshals maps with sorted keys, making this deterministic
	// without an external canonicalization dependency.
	b, err := json.Marshal(struct {
		Config any `json:"config"`
		Maps   any `json:"mappings"`
	}{c, m})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ReconcileReported handles one connector segment of a gateway manifest. It
// records the full-list watermark, classifies desired versus reported, raises
// or clears the drift alarm, and — when strict mode is enabled — converges
// the edge to desired. reported is tenant/gateway derived from the
// authenticated connection by the ingress, never from the frame.
func (s *Service) ReconcileReported(ctx context.Context, tenantID, gatewayID string, reported []ReportedInstance) (ReconcileReport, error) {
	now := time.Now().UTC()

	// Record the watermark and read back the tenant's desired set in one tx.
	desired, err := s.store.StampManifest(ctx, tenantID, gatewayID, reported, now)
	if err != nil {
		return ReconcileReport{}, err
	}

	reportedByID := make(map[string]ReportedInstance, len(reported))
	for _, r := range reported {
		if r.SpecID != "" {
			reportedByID[r.SpecID] = r
		}
	}
	desiredByID := make(map[string]Spec, len(desired))
	for _, sp := range desired {
		desiredByID[sp.ID] = sp
	}

	// Batch-resolve profile templates so the expected hash is computed over
	// the effective (merged) document, mirroring redrive.
	templates := s.templatesFor(ctx, desired)

	rep := ReconcileReport{
		TenantID:  tenantID,
		GatewayID: gatewayID,
		InSync:    []string{},
		Missing:   []string{},
		Stale:     []string{},
		Drifted:   []string{},
		Extra:     []string{},
	}

	// Index effective hashes for desired specs that reference a template.
	effective := make(map[string]Spec, len(desired))
	hashConfig := make(map[string]json.RawMessage, len(desired))
	for _, sp := range desired {
		eff, err := effectiveSpec(sp, templatePtr(templates[sp.ProfileID]))
		if err != nil {
			// A bound profile lost its template: reconcile on revision only
			// rather than failing the whole gateway cycle.
			eff = sp
		}
		effective[sp.ID] = eff
		// The edge applies plaintext after the relay resolves secret://
		// references, so the expected hash must resolve them too; otherwise
		// every secret-bearing spec would false-positive as drifted.
		hashConfig[sp.ID] = s.resolveSecrets(ctx, eff, eff.Config)
	}

	for _, sp := range desired {
		got, present := reportedByID[sp.ID]
		switch {
		case !present:
			rep.Missing = append(rep.Missing, sp.ID)
		case got.Revision < sp.Revision:
			rep.Stale = append(rep.Stale, sp.ID)
		case got.Revision == sp.Revision && got.ConfigHash != "" &&
			got.ConfigHash != canonicalHash(hashConfig[sp.ID], effective[sp.ID].Mappings):
			rep.Drifted = append(rep.Drifted, sp.ID)
		default:
			rep.InSync = append(rep.InSync, sp.ID)
		}
	}
	for id := range reportedByID {
		if _, isDesired := desiredByID[id]; !isDesired {
			rep.Extra = append(rep.Extra, id)
		}
	}

	s.applyDriftAlarm(ctx, rep)
	if s.tune.StrictReconcile {
		s.enforceDesired(ctx, rep, desiredByID)
	}
	s.publishEvent(EventManifest, tenantID, gatewayID, "", "", 0)
	return rep, nil
}

// ReconcileView computes the current desired-vs-applied classification for one
// gateway on demand, purely from stored state: the desired rows plus the last
// manifest watermark (migration 0055). Unlike ReconcileReported it accepts no
// manifest, records no watermark, raises/clears no alarm and does not converge
// in strict mode, so the console can poll it freely. A gateway that has never
// sent a connector manifest yields a nil watermark with every desired spec
// left unclassified (the caller treats them as "unknown/pending" rather than
// "missing", since no full list has ever been observed).
func (s *Service) ReconcileView(ctx context.Context, tenantID, gatewayID string) (ReconcileView, error) {
	desired, err := s.store.ListByGateway(ctx, tenantID, gatewayID)
	if err != nil {
		return ReconcileView{}, err
	}

	view := ReconcileView{
		TenantID:   tenantID,
		GatewayID:  gatewayID,
		InSync:     []string{},
		Missing:    []string{},
		Stale:      []string{},
		Drifted:    []string{},
		Extra:      []string{},
		StrictMode: s.tune.StrictReconcile,
	}

	wm, err := s.store.GetWatermark(ctx, tenantID, gatewayID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return ReconcileView{}, err
	}
	if err == nil {
		wmCopy := wm
		view.Watermark = &wmCopy
	}

	// Without an observed full list there is nothing to diff against: leave
	// every spec unclassified rather than reporting the whole fleet missing.
	if view.Watermark == nil {
		return view, nil
	}

	reportedByID := make(map[string]struct{}, len(wm.SpecIDs))
	for _, id := range wm.SpecIDs {
		if id != "" {
			reportedByID[id] = struct{}{}
		}
	}

	templates := s.templatesFor(ctx, desired)
	for _, sp := range desired {
		if _, present := reportedByID[sp.ID]; !present {
			view.Missing = append(view.Missing, sp.ID)
			continue
		}
		switch {
		case sp.ManifestRevision < sp.Revision:
			view.Stale = append(view.Stale, sp.ID)
		case sp.ManifestRevision == sp.Revision && sp.ManifestHash != "":
			eff, effErr := effectiveSpec(sp, templatePtr(templates[sp.ProfileID]))
			if effErr != nil {
				eff = sp
			}
			plain := s.resolveSecrets(ctx, eff, eff.Config)
			if sp.ManifestHash != canonicalHash(plain, eff.Mappings) {
				view.Drifted = append(view.Drifted, sp.ID)
			} else {
				view.InSync = append(view.InSync, sp.ID)
			}
		default:
			view.InSync = append(view.InSync, sp.ID)
		}
	}

	desiredByID := make(map[string]struct{}, len(desired))
	for _, sp := range desired {
		desiredByID[sp.ID] = struct{}{}
	}
	for id := range reportedByID {
		if _, isDesired := desiredByID[id]; !isDesired {
			view.Extra = append(view.Extra, id)
		}
	}
	sort.Strings(view.Extra)
	return view, nil
}

// templatePtr returns a pointer to a non-empty template or nil, matching the
// effectiveSpec contract (nil means "instance carries its full config").
func templatePtr(tpl Template) *Template {
	if tpl.ProfileID == "" {
		return nil
	}
	copy := tpl
	return &copy
}

// templatesFor batch-fetches every referenced profile template for a desired
// set in one query per tenant (the same N-safe batching redrive uses).
func (s *Service) templatesFor(ctx context.Context, desired []Spec) map[string]Template {
	out := map[string]Template{}
	byTenant := map[string][]string{}
	for i := range desired {
		if desired[i].ProfileID != "" {
			byTenant[desired[i].TenantID] = append(byTenant[desired[i].TenantID], desired[i].ProfileID)
		}
	}
	for tenant, ids := range byTenant {
		found, err := s.store.FetchTemplates(ctx, tenant, ids)
		if err != nil {
			s.log.Error("reconcile fetch connector templates failed", "error", err)
			continue
		}
		for k, v := range found {
			out[k] = v
		}
	}
	return out
}

// applyDriftAlarm opens one summarizing WARNING per gateway while any class
// diverges and clears it the moment the whole set is back in sync. The
// per-gateway dedupe key keeps exactly one open alarm regardless of how many
// consecutive cycles drift or how many specs are involved.
func (s *Service) applyDriftAlarm(ctx context.Context, rep ReconcileReport) {
	if s.alarms == nil {
		return
	}
	key := DedupeDriftKey(rep.GatewayID)
	if !rep.Drift() {
		if _, err := s.alarms.ClearAlarm(ctx, rep.TenantID, rep.GatewayID, key); err != nil {
			s.log.Error("clear connector drift alarm failed", "gateway", rep.GatewayID, "error", err)
		}
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  rep.TenantID,
		DeviceID:  rep.GatewayID,
		AlarmType: AlarmTypeDrift,
		Severity:  alarm.SeverityWarning,
		DedupeKey: key,
		Details: map[string]any{
			"missing": rep.Missing,
			"stale":   rep.Stale,
			"drifted": rep.Drifted,
			"extra":   rep.Extra,
		},
	}); err != nil {
		s.log.Error("raise connector drift alarm failed", "gateway", rep.GatewayID, "error", err)
	}
}

// enforceDesired makes desired win in strict mode. stored maps spec id to
// the raw stored desired row (pushSpec re-merges its current template).
func (s *Service) enforceDesired(ctx context.Context, rep ReconcileReport, stored map[string]Spec) {
	// Missing and stale converge by re-sending the current desired frame: the
	// edge either lacks the instance entirely or runs an older revision, so a
	// fresh push is accepted rather than deduped.
	for _, id := range append(append([]string{}, rep.Missing...), rep.Stale...) {
		if sp, ok := stored[id]; ok {
			s.pushSpec(ctx, sp)
		}
	}
	// Same-revision content drift requires a revision bump, otherwise the
	// edge's (spec_id, revision) dedupe would discard the corrective push.
	for _, id := range rep.Drifted {
		desired, ok := stored[id]
		if !ok {
			continue
		}
		bumped, err := s.store.BumpRevision(ctx, desired.TenantID, id)
		if err != nil {
			s.log.Error("strict reconcile bump failed", "spec", id, "error", err)
			continue
		}
		// pushSpec re-merges the bumped row with its current template.
		s.pushSpec(ctx, bumped)
	}
	// Extra remains observational even in strict mode. A manifest identity has
	// no trustworthy connector type/device identity and absence is not a delete
	// acknowledgement; only an explicit durable delete may remove manager,
	// northbound-filter, or legacy resources.
}
