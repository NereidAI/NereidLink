// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// autoprovision.go implements the shared gateway auto-provisioning core
// (ADR-0026 / ADR-0013). It is the single pipeline the edge discovery uplink
// uses today and a future direct first-connect source can reuse. The registry
// owns the transaction boundary, the gateway/tenant guard, the per-cycle cap,
// idempotent name-based dedupe and the normal CreateDevice guardrails (quota,
// profile, ownership, registered history).
//
// Auto-provisioning is the only exception to "an unregistered device is
// rejected". It stays safe because every candidate is bound to exactly one
// gateway, receives an operator-policy profile, and is name-idempotent: a
// re-discovery of the same gateway-local key resolves to the existing device
// instead of creating a duplicate.

package registry

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/nereidai/nereidlink/internal/audit"
)

// ProvisionCandidate is one gateway-reported device to auto-create.
type ProvisionCandidate struct {
	// LocalID is the stable key the edge uses for the device inside its own
	// southbound domain (Modbus unit id, BLE address, OPC node id, ...). It is
	// combined with the gateway name to form the canonical name, which makes
	// repeated discovery idempotent without the edge knowing the NL UUID.
	LocalID string

	// ProfileHint optionally names a tenant profile to bind. The discovery
	// policy resolves it before calling the registry; an empty hint uses the
	// policy default.
	ProfileID string

	// Name optionally overrides the generated canonical name. When empty the
	// registry builds "<gateway>/<local>".
	Name string
}

// ProvisionedDevice reports one successfully materialised (or already
// present) device back to the caller, which maps it to the edge local id.
type ProvisionedDevice struct {
	LocalID       string `json:"local_id"`
	DeviceID      string `json:"device_id"`
	CanonicalName string `json:"canonical_name"`
	ProfileID     string `json:"profile_id,omitempty"`
	// Existing is true when a device with the deterministic canonical name
	// was already bound to the gateway, so no row was inserted.
	Existing bool `json:"existing"`
}

// ProvisionReject reports one candidate that could not be provisioned. Like
// bulk import, one bad candidate never blocks the rest of the batch.
type ProvisionReject struct {
	LocalID string `json:"local_id"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"`
}

// ProvisionResult is the partial-success outcome of one discovery batch.
type ProvisionResult struct {
	Provisioned []ProvisionedDevice `json:"provisioned"`
	Rejected    []ProvisionReject   `json:"rejected"`
}

// CandidateState classifies one candidate against the current registry
// without creating anything, used by the human-review approval flow
// (migration 0046).
const (
	// CandidateStateExisting means the deterministic name is already a
	// device bound to this gateway.
	CandidateStateExisting = "existing"
	// CandidateStateFree means no device owns the deterministic name; the
	// candidate may be created on approval.
	CandidateStateFree = "free"
	// CandidateStateConflict means the name is bound elsewhere.
	CandidateStateConflict = "conflict"
	// CandidateStateInvalid means the candidate itself is malformed.
	CandidateStateInvalid = "invalid"
)

// CandidateClassification is the non-mutating view of one candidate.
type CandidateClassification struct {
	LocalID   string `json:"local_id"`
	Name      string `json:"name"`
	ProfileID string `json:"profile_id,omitempty"`
	State     string `json:"state"`
	DeviceID  string `json:"device_id,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ClassifyProvisionCandidates resolves each candidate's deterministic name
// and reports whether it is already a device of this gateway, free to create,
// or conflicting with a device bound elsewhere. It never writes and is the
// classification half of AutoProvisionSubdevices used by the review queue.
func (a *Admin) ClassifyProvisionCandidates(ctx context.Context, tenantID, gatewayID string,
	candidates []ProvisionCandidate) ([]CandidateClassification, error) {
	var gatewayName string
	err := a.pool.QueryRow(ctx,
		`SELECT canonical_name FROM device
		 WHERE id = $1 AND tenant_id = $2 AND is_gateway AND state = $3`,
		gatewayID, tenantID, StateActive).Scan(&gatewayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid("auto-provision target is not an ACTIVE gateway of the tenant")
	}
	if err != nil {
		return nil, mapSQLError(err)
	}

	out := make([]CandidateClassification, 0, len(candidates))
	for _, cand := range candidates {
		local := strings.TrimSpace(cand.LocalID)
		if local == "" {
			out = append(out, CandidateClassification{
				Reason: "local_id is required", State: CandidateStateInvalid})
			continue
		}
		name := strings.TrimSpace(cand.Name)
		if name == "" {
			name = gatewayName + "/" + local
		}
		view := CandidateClassification{
			LocalID:   local,
			Name:      name,
			ProfileID: cand.ProfileID,
			State:     CandidateStateFree,
		}
		if len(name) > 255 {
			view.State = CandidateStateInvalid
			view.Reason = "canonical name is too long"
			out = append(out, view)
			continue
		}
		existing, bound, lerr := a.findProvisioned(ctx, tenantID, gatewayID, name)
		if lerr != nil {
			view.State = CandidateStateInvalid
			view.Reason = "lookup failed"
			out = append(out, view)
			continue
		}
		switch {
		case bound:
			view.State = CandidateStateExisting
			view.DeviceID = existing.DeviceID
			view.ProfileID = existing.ProfileID
		case existing.DeviceID != "":
			view.State = CandidateStateConflict
			view.Reason = "canonical name already used by another device"
		}
		out = append(out, view)
	}
	return out, nil
}

// ResolveProfileID maps a tenant-scoped profile identifier to its UUID. It
// accepts either a profile UUID or a profile canonical name, both scoped to
// the tenant so an edge hint can never select another tenant's profile.
func (a *Admin) ResolveProfileID(ctx context.Context, tenantID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	var id string
	err := a.pool.QueryRow(ctx,
		`SELECT id::text FROM device_profile
		 WHERE tenant_id = $1 AND (id::text = $2 OR name = $2)`,
		tenantID, ref).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", mapSQLError(err)
	}
	return id, nil
}

// AutoProvisionSubdevices creates (or idempotently resolves) gateway-bound
// sub-devices for the supplied candidates. maxPerCycle caps newly created
// rows in this call; candidates beyond the cap are reported as rejected so an
// edge cannot bypass a tenant device quota by spraying thousands of
// candidates. The method never fails the whole batch for a per-candidate
// error.
func (a *Admin) AutoProvisionSubdevices(ctx context.Context, tenantID, gatewayID string,
	candidates []ProvisionCandidate, maxPerCycle int, actorID string) (ProvisionResult, error) {
	result := ProvisionResult{
		Provisioned: []ProvisionedDevice{},
		Rejected:    []ProvisionReject{},
	}

	var gatewayName string
	err := a.pool.QueryRow(ctx,
		`SELECT canonical_name FROM device
		 WHERE id = $1 AND tenant_id = $2 AND is_gateway AND state = $3`,
		gatewayID, tenantID, StateActive).Scan(&gatewayName)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, invalid("auto-provision target is not an ACTIVE gateway of the tenant")
	}
	if err != nil {
		return result, mapSQLError(err)
	}

	created := 0
	for _, cand := range candidates {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		local := strings.TrimSpace(cand.LocalID)
		if local == "" {
			result.Rejected = append(result.Rejected, ProvisionReject{
				Reason: "local_id is required",
			})
			continue
		}

		name := strings.TrimSpace(cand.Name)
		if name == "" {
			name = gatewayName + "/" + local
		}
		if len(name) > 255 {
			result.Rejected = append(result.Rejected, ProvisionReject{
				LocalID: local, Name: name, Reason: "canonical name is too long",
			})
			continue
		}

		// Idempotency: a device already carrying the deterministic name and
		// bound to this gateway resolves to its UUID. A name owned elsewhere
		// (different gateway or unbound) is a rejection rather than a steal.
		existing, bound, err := a.findProvisioned(ctx, tenantID, gatewayID, name)
		if err != nil {
			result.Rejected = append(result.Rejected, ProvisionReject{
				LocalID: local, Name: name, Reason: "lookup failed",
			})
			continue
		}
		switch {
		case bound:
			existing.LocalID = local
			result.Provisioned = append(result.Provisioned, existing)
			continue
		case existing.DeviceID != "":
			result.Rejected = append(result.Rejected, ProvisionReject{
				LocalID: local, Name: name,
				Reason: "canonical name already used by another device",
			})
			continue
		}

		if maxPerCycle > 0 && created >= maxPerCycle {
			result.Rejected = append(result.Rejected, ProvisionReject{
				LocalID: local, Name: name,
				Reason: "auto-provision cycle cap reached",
			})
			continue
		}

		dev, err := a.CreateDevice(ctx, tenantID, DeviceDTO{
			CanonicalName: name,
			ProfileID:     cand.ProfileID,
			GatewayID:     gatewayID,
			State:         StateActive,
		})
		if err != nil {
			result.Rejected = append(result.Rejected, ProvisionReject{
				LocalID: local, Name: name, Reason: bulkImportReason(err),
			})
			continue
		}
		created++
		result.Provisioned = append(result.Provisioned, ProvisionedDevice{
			LocalID:       local,
			DeviceID:      dev.ID,
			CanonicalName: dev.CanonicalName,
			ProfileID:     dev.ProfileID,
		})
	}

	if a.auditor != nil && created > 0 {
		if _, err := a.auditor.Append(ctx, audit.Input{
			TenantID:     tenantID,
			ActorID:      actorID,
			EventType:    audit.EventConfigChange,
			Action:       "auto-provision",
			ResourceType: "device",
			Details: map[string]any{
				"gateway_id": gatewayID,
				"created":    created,
				"existing":   len(result.Provisioned) - created,
				"rejected":   len(result.Rejected),
			},
		}); err != nil {
			slog.Warn("auto-provision audit append failed",
				"tenant", tenantID, "gateway", gatewayID, "error", err)
		}
	}
	return result, nil
}

// findProvisioned resolves a deterministic canonical name during
// auto-provisioning. The middle return distinguishes:
//   - (dev, true, nil):  name exists and is bound to this gateway -> reuse.
//   - (dev, false, nil): name exists but is bound elsewhere -> reject.
//     When dev.DeviceID == "" the name is free -> create.
func (a *Admin) findProvisioned(ctx context.Context, tenantID, gatewayID, name string) (ProvisionedDevice, bool, error) {
	var deviceID, boundGateway, profileID string
	err := a.pool.QueryRow(ctx,
		`SELECT id::text, COALESCE(gateway_id::text,''), COALESCE(profile_id::text,'')
		 FROM device WHERE tenant_id = $1 AND canonical_name = $2`,
		tenantID, name).Scan(&deviceID, &boundGateway, &profileID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProvisionedDevice{}, false, nil
	}
	if err != nil {
		return ProvisionedDevice{}, false, mapSQLError(err)
	}
	if boundGateway == gatewayID {
		return ProvisionedDevice{
			DeviceID:      deviceID,
			CanonicalName: name,
			ProfileID:     profileID,
			Existing:      true,
		}, true, nil
	}
	return ProvisionedDevice{DeviceID: deviceID}, false, nil
}
