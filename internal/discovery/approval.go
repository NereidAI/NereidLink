// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// approval.go implements the human-review branch of gateway
// auto-provisioning (ADR-0026, migration 0046). When the effective policy
// approval_mode is "review", candidates that would create a new device are
// parked in discovery_candidate as PENDING instead of being created. An
// operator later approves (creates the device through the same guarded
// registry path) or rejects; the outcome is pushed to the owning gateway on
// the normal discovery/result topic via a Core NATS dispatch so the edge can
// finish correlating its local id to a UUID asynchronously.

package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/registry"
)

// StatusPending is returned per candidate while it awaits human review.
const StatusPending = "pending"

// DecisionDispatchSubjectPrefix is nl.discovery.dispatch.<ingress-id>: an
// approval result routed to the one ingress node owning the gateway
// connection, mirroring the probe dispatch scheme.
const DecisionDispatchSubjectPrefix = "nl.discovery.dispatch."

// AdvisoryKindPending labels a frame that queued candidates for review.
const AdvisoryKindPending = "discovery.pending"

// GatewayLocation is the minimal live-location view approval routing needs.
// main.go adapts *presence.Service to it.
type GatewayLocation struct {
	Found     bool
	IngressID string
}

// LocationResolver resolves the ingress owning a gateway's live connection.
type LocationResolver interface {
	LookupGateway(ctx context.Context, tenantID, gatewayID string) (GatewayLocation, error)
}

// CandidateLister is the persistence surface for the approval queue.
// *CandidateStore satisfies it.
type CandidateLister interface {
	PendingInsert(ctx context.Context, in NewCandidate) (CandidateRecord, error)
	ListCandidates(ctx context.Context, tenantID, state, gatewayID string, limit, offset int) ([]CandidateRecord, int, error)
	GetByID(ctx context.Context, tenantID, candidateID string) (CandidateRecord, error)
	GetPendingCandidate(ctx context.Context, tenantID, candidateID string) (CandidateRecord, error)
	MarkApproved(ctx context.Context, tenantID, candidateID, deviceID, actor, reason string) error
	MarkRejected(ctx context.Context, tenantID, candidateID, actor, reason string) error
}

// ErrApprovalRejected means an approved candidate could not be created by the
// registry (for example a name conflict or quota that arose after enqueue).
var ErrApprovalRejected = errors.New("approved candidate was rejected by the registry")

// SetCandidateStore wires the approval queue and enables review mode.
func (s *Service) SetCandidateStore(store CandidateLister) {
	s.candidates = store
}

// SetLocationResolver wires gateway-presence routing for the async result
// push; without it decisions still apply but cannot be delivered live.
func (s *Service) SetLocationResolver(r LocationResolver) {
	s.locator = r
}

// reviewCandidates classifies resolved candidates in review mode, enqueuing
// the ones that would create a device. Existing matches resolve immediately;
// conflicts/invalid candidates are rejected without touching the queue.
func (s *Service) reviewCandidates(ctx context.Context, tenantID, gatewayID, source string,
	cands []registry.ProvisionCandidate, maxPerCycle int) (Result, int) {
	result := Result{Results: []ItemResult{}}
	classified, err := s.reg.ClassifyProvisionCandidates(ctx, tenantID, gatewayID, cands)
	if err != nil {
		// The gateway itself is invalid or the registry is unavailable; the
		// caller surfaces this as a frame error rather than per-candidate.
		return result, -1
	}
	queued := 0
	for _, v := range classified {
		item := ItemResult{LocalID: v.LocalID}
		switch v.State {
		case registry.CandidateStateExisting:
			item.Status = StatusExisting
			item.DeviceID = v.DeviceID
			item.CanonicalName = v.Name
			item.ProfileID = v.ProfileID
		case registry.CandidateStateFree:
			if s.candidates == nil {
				// Fail closed: review requested but the queue is unavailable.
				item.Status = StatusRejected
				item.Reason = "approval queue is not configured"
				result.Results = append(result.Results, item)
				continue
			}
			if queued >= maxPerCycle {
				item.Status = StatusRejected
				item.Reason = "auto-provision cycle cap reached"
				result.Results = append(result.Results, item)
				continue
			}
			_, insErr := s.candidates.PendingInsert(ctx, NewCandidate{
				TenantID:      tenantID,
				GatewayID:     gatewayID,
				LocalID:       v.LocalID,
				CandidateName: v.Name,
				ProfileID:     v.ProfileID,
				SourceType:    source,
			})
			switch {
			case insErr == nil:
				queued++
				item.Status = StatusPending
			case errors.Is(insErr, ErrPendingExists):
				// Repeated discovery of the same local key while under
				// review; not a new insertion, still reported as pending.
				item.Status = StatusPending
			default:
				item.Status = StatusRejected
				item.Reason = "could not queue for approval"
				s.log.Error("discovery candidate enqueue failed",
					"tenant", tenantID, "gateway", gatewayID,
					"local", v.LocalID, "error", insErr)
			}
		default: // conflict / invalid
			item.Status = StatusRejected
			item.Reason = v.Reason
		}
		result.Results = append(result.Results, item)
	}
	return result, queued
}

// ListCandidates returns a paged approval queue for the REST plane.
func (s *Service) ListCandidates(ctx context.Context, tenantID, state, gatewayID string,
	page, pageSize int) (apipage.Page[CandidateRecord], error) {
	if s.candidates == nil {
		return apipage.Page[CandidateRecord]{}, errNoQueue
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 50
	}
	items, total, err := s.candidates.ListCandidates(ctx, tenantID, state, gatewayID,
		pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[CandidateRecord]{}, err
	}
	return apipage.New(items, total, page, pageSize), nil
}

// GetCandidate reads one tenant-scoped candidate regardless of state.
func (s *Service) GetCandidate(ctx context.Context, tenantID, candidateID string) (CandidateRecord, error) {
	if s.candidates == nil {
		return CandidateRecord{}, errNoQueue
	}
	return s.candidates.GetByID(ctx, tenantID, candidateID)
}

// ApproveCandidate creates the queued device and pushes the result to the
// gateway. The candidate is marked APPROVED on success or REJECTED when the
// registry refuses it; either outcome is delivered to the edge.
func (s *Service) ApproveCandidate(ctx context.Context, tenantID, candidateID, actor string) (CandidateRecord, ItemResult, error) {
	if s.candidates == nil {
		return CandidateRecord{}, ItemResult{}, errNoQueue
	}
	cand, err := s.candidates.GetPendingCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return CandidateRecord{}, ItemResult{}, err
	}

	out, err := s.reg.AutoProvisionSubdevices(ctx, tenantID, cand.GatewayID,
		[]registry.ProvisionCandidate{{
			LocalID:   cand.LocalID,
			Name:      cand.CandidateName,
			ProfileID: cand.ProfileID,
		}}, 1, actor)
	if err != nil {
		return CandidateRecord{}, ItemResult{}, err
	}

	item := ItemResult{LocalID: cand.LocalID}
	if p, ok := findProvisioned(out, cand.LocalID); ok {
		status := StatusCreated
		if p.Existing {
			status = StatusExisting
		}
		item.Status = status
		item.DeviceID = p.DeviceID
		item.CanonicalName = p.CanonicalName
		item.ProfileID = p.ProfileID
		if mErr := s.candidates.MarkApproved(ctx, tenantID, candidateID, p.DeviceID, actor, ""); mErr != nil {
			return cand, item, mErr
		}
		cand.State = StateApproved
		cand.DeviceID = p.DeviceID
		s.dispatchDecision(ctx, tenantID, cand.GatewayID, item)
		s.log.Info("discovery candidate approved",
			"tenant", tenantID, "candidate", candidateID, "device", p.DeviceID)
		return cand, item, nil
	}

	reason := "rejected during approval"
	if len(out.Rejected) > 0 {
		reason = out.Rejected[0].Reason
	}
	item.Status = StatusRejected
	item.Reason = reason
	_ = s.candidates.MarkRejected(ctx, tenantID, candidateID, actor, reason)
	cand.State = StateRejected
	cand.DecisionReason = reason
	s.dispatchDecision(ctx, tenantID, cand.GatewayID, item)
	return cand, item, fmt.Errorf("%w: %s", ErrApprovalRejected, reason)
}

// RejectCandidate declines a queued candidate without creating a device and
// notifies the gateway.
func (s *Service) RejectCandidate(ctx context.Context, tenantID, candidateID, actor, reason string) (CandidateRecord, error) {
	if s.candidates == nil {
		return CandidateRecord{}, errNoQueue
	}
	cand, err := s.candidates.GetPendingCandidate(ctx, tenantID, candidateID)
	if err != nil {
		return CandidateRecord{}, err
	}
	if reason == "" {
		reason = "rejected by operator"
	}
	if err := s.candidates.MarkRejected(ctx, tenantID, candidateID, actor, reason); err != nil {
		return CandidateRecord{}, err
	}
	cand.State = StateRejected
	cand.DecisionReason = reason
	s.dispatchDecision(ctx, tenantID, cand.GatewayID,
		ItemResult{LocalID: cand.LocalID, Status: StatusRejected, Reason: reason})
	s.log.Info("discovery candidate rejected",
		"tenant", tenantID, "candidate", candidateID, "actor", actor)
	return cand, nil
}

// findProvisioned locates one local id in a provisioning result.
func findProvisioned(out registry.ProvisionResult, localID string) (registry.ProvisionedDevice, bool) {
	for _, p := range out.Provisioned {
		if p.LocalID == localID {
			return p, true
		}
	}
	return registry.ProvisionedDevice{}, false
}

// dispatchDecision pushes one decided item to the owning gateway over Core
// NATS. Delivery is best-effort: an offline gateway receives nothing now, but
// its next discovery frame resolves the created device as "existing".
func (s *Service) dispatchDecision(ctx context.Context, tenantID, gatewayID string, item ItemResult) {
	if s.locator == nil || s.publisher == nil {
		return
	}
	loc, err := s.locator.LookupGateway(ctx, tenantID, gatewayID)
	if err != nil || !loc.Found || loc.IngressID == "" {
		return
	}
	frame := DecisionFrame{
		TenantID:  tenantID,
		GatewayID: gatewayID,
		Result:    Result{Results: []ItemResult{item}},
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return
	}
	if pErr := s.publisher.Publish(DecisionDispatchSubjectPrefix+loc.IngressID, raw); pErr != nil {
		s.log.Warn("discovery decision dispatch failed",
			"gateway", gatewayID, "error", pErr)
	}
}

// emitPendingAdvisory publishes a summary of a frame that queued candidates.
func (s *Service) emitPendingAdvisory(ctx context.Context, tenantID, gatewayID, source string, queued int) {
	if s.publisher == nil || queued == 0 {
		return
	}
	raw, err := json.Marshal(AdvisoryProvisioned{
		Kind:      AdvisoryKindPending,
		Time:      time.Now().UTC(),
		TenantID:  tenantID,
		GatewayID: gatewayID,
		Source:    source,
		Pending:   queued,
	})
	if err != nil {
		return
	}
	if pErr := s.publisher.Publish(
		fmt.Sprintf("%s.%s.%s", SubjectPrefix, tenantID, gatewayID), raw); pErr != nil {
		s.log.Error("discovery pending advisory publish failed", "error", pErr)
	}
}

// DecisionFrame is the Core NATS payload routed to one ingress node.
type DecisionFrame struct {
	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`
	Result    Result `json:"result"`
}

// errNoQueue is returned when approval endpoints run without a store.
var errNoQueue = errors.New("discovery approval queue is not configured")
