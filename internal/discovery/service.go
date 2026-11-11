// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go applies a discovery Frame against the registry under operator
// policy. Policy decides whether auto-provisioning is enabled at all, caps how
// many new devices one frame may create and supplies a default profile when a
// candidate names none. The service resolves profile references, calls the
// registry batch (partial success), assembles the edge-facing Result and emits
// one Core NATS advisory describing what changed.

package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nereidai/nereidlink/internal/registry"
)

// Registry is the narrow registry surface provisioning needs. *registry.Admin
// satisfies it; tests use a fake.
type Registry interface {
	// ResolveProfileID maps a tenant profile UUID or name to a UUID.
	ResolveProfileID(ctx context.Context, tenantID, ref string) (string, error)
	// AutoProvisionSubdevices performs the guarded, idempotent batch create.
	AutoProvisionSubdevices(ctx context.Context, tenantID, gatewayID string,
		candidates []registry.ProvisionCandidate, maxPerCycle int, actorID string) (registry.ProvisionResult, error)
	// ClassifyProvisionCandidates is the non-mutating view used by the
	// human-review approval queue (migration 0046).
	ClassifyProvisionCandidates(ctx context.Context, tenantID, gatewayID string,
		candidates []registry.ProvisionCandidate) ([]registry.CandidateClassification, error)
}

// Publisher is the Core NATS surface used for advisories.
type Publisher interface {
	Publish(subject string, data []byte) error
}

// Policy is the operator-controlled auto-provisioning configuration for a
// deployment. It is not tenant-editable in this slice.
type Policy struct {
	// Enabled gates the whole feature. When false every candidate is rejected
	// with a stable reason and nothing is created.
	Enabled bool

	// MaxPerCycle bounds how many NEW devices a single frame may insert;
	// already-existing matches do not count toward it.
	MaxPerCycle int

	// MaxCandidates bounds the number of candidates accepted in one frame,
	// protecting the transport and database from an oversized uplink.
	MaxCandidates int

	// DefaultProfile is a tenant profile UUID or name applied to candidates
	// that do not name their own profile.
	DefaultProfile string

	// ApprovalMode selects immediate create (ApprovalAuto, default) or a
	// human-review pending queue (ApprovalReview, migration 0046).
	ApprovalMode string
}

// Service is the transport-independent provisioning entry point.
type Service struct {
	reg       Registry
	publisher Publisher
	// fallback is the deployment-level policy used unless a tenant/gateway
	// override row resolves a more-specific policy for the frame.
	fallback Policy
	// reader supplies tenant/gateway overrides (migration 0045); nil keeps
	// the service on the single deployment-level policy (tests/older wiring).
	reader PolicyReader
	// admin manages override rows; nil disables the REST policy surface.
	admin PolicyAdmin
	// candidates backs the human-review approval queue (migration 0046);
	// nil keeps auto-provisioning on immediate create/reject only.
	candidates CandidateLister
	// locator resolves the ingress owning a gateway for the async decision
	// push; nil makes delivery best-effort on the next discovery frame.
	locator LocationResolver
	log     *slog.Logger
}

// New builds a Service. A nil logger defaults to slog.Default().
func New(reg Registry, publisher Publisher, policy Policy, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if policy.MaxCandidates <= 0 {
		policy.MaxCandidates = 1000
	}
	if policy.MaxPerCycle <= 0 {
		policy.MaxPerCycle = 100
	}
	if policy.ApprovalMode != ApprovalReview {
		policy.ApprovalMode = ApprovalAuto
	}
	return &Service{reg: reg, publisher: publisher, fallback: policy, log: log}
}

// SetPolicyReader wires per-tenant/per-gateway policy overrides.
func (s *Service) SetPolicyReader(reader PolicyReader) {
	s.reader = reader
}

// policyFor resolves the concrete policy for one authenticated gateway frame.
func (s *Service) policyFor(ctx context.Context, tenantID, gatewayID string) (Policy, error) {
	return EffectivePolicy(ctx, s.reader, s.fallback, tenantID, gatewayID)
}

// AdvisoryProvisioned is published after a frame that created at least one
// device. It drives dashboards and a future approval workflow.
type AdvisoryProvisioned struct {
	Kind      string    `json:"kind"`
	Time      time.Time `json:"time"`
	TenantID  string    `json:"tenant_id"`
	GatewayID string    `json:"gateway_id"`
	Source    string    `json:"source,omitempty"`
	Created   int       `json:"created"`
	Existing  int       `json:"existing"`
	Rejected  int       `json:"rejected"`
	Pending   int       `json:"pending,omitempty"`
}

const (
	// SubjectPrefix is the Core NATS root for discovery advisories.
	SubjectPrefix = "nl.discovery"

	// AdvisoryKindProvisioned labels a successful provisioning frame.
	AdvisoryKindProvisioned = "discovery.provisioned"
)

// Handle converts one uplink frame into registry provisioning calls and an
// edge-facing result. tenantID and gatewayID come from the authenticated
// gateway session, never from the frame body. The returned Result is never nil
// and always carries a non-nil Results slice. A non-nil error means the batch
// could not be attempted (for example a missing gateway); in that case the
// caller should return a rejection result to the edge rather than retry.
func (s *Service) HandleDiscovery(ctx context.Context, tenantID, gatewayID string, frame Frame) (Result, error) {
	result := Result{Results: []ItemResult{}}

	policy, err := s.policyFor(ctx, tenantID, gatewayID)
	if err != nil {
		return result, err
	}

	if !policy.Enabled {
		for _, c := range frame.Candidates {
			result.Results = append(result.Results, ItemResult{
				LocalID: c.LocalID,
				Status:  StatusRejected,
				Reason:  "auto-provisioning is disabled",
			})
		}
		return result, nil
	}

	if len(frame.Candidates) > policy.MaxCandidates {
		return result, fmt.Errorf("discovery frame has %d candidates, max %d",
			len(frame.Candidates), policy.MaxCandidates)
	}

	// Resolve profiles per tenant before touching devices, so a bad profile
	// reference rejects only that candidate and is never cached as a device.
	defaultProfileID, err := s.resolveDefaultProfile(ctx, tenantID, policy.DefaultProfile)
	if err != nil {
		return result, err
	}

	cands := make([]registry.ProvisionCandidate, 0, len(frame.Candidates))
	for _, c := range frame.Candidates {
		local := strings.TrimSpace(c.LocalID)
		item := ItemResult{LocalID: local}
		if local == "" {
			item.Status = StatusRejected
			item.Reason = "local_id is required"
			result.Results = append(result.Results, item)
			continue
		}
		profileID := defaultProfileID
		if ref := strings.TrimSpace(c.Profile); ref != "" {
			id, perr := s.reg.ResolveProfileID(ctx, tenantID, ref)
			if perr != nil {
				item.Status = StatusRejected
				item.Reason = "profile not found: " + ref
				result.Results = append(result.Results, item)
				continue
			}
			profileID = id
		}
		cands = append(cands, registry.ProvisionCandidate{
			LocalID:   local,
			Name:      c.Name,
			ProfileID: profileID,
		})
	}

	if len(cands) == 0 {
		return result, nil
	}

	// Human-review mode (migration 0046): free candidates are parked as
	// PENDING rather than created; existing matches still resolve now.
	if policy.ApprovalMode == ApprovalReview {
		reviewResult, queued := s.reviewCandidates(ctx, tenantID, gatewayID,
			frame.Type, cands, policy.MaxPerCycle)
		if queued < 0 {
			return result, fmt.Errorf("discovery review classification failed")
		}
		s.emitPendingAdvisory(ctx, tenantID, gatewayID, frame.Type, queued)
		s.log.Info("gateway discovery queued for approval",
			"tenant", tenantID, "gateway", gatewayID, "queued", queued)
		return reviewResult, nil
	}

	out, err := s.reg.AutoProvisionSubdevices(ctx, tenantID, gatewayID,
		cands, policy.MaxPerCycle, gatewayID)
	if err != nil {
		return result, err
	}

	// Registry results are keyed by local id; rebuild in request order.
	created := 0
	for _, p := range out.Provisioned {
		status := StatusExisting
		if !p.Existing {
			status = StatusCreated
			created++
		}
		result.Results = append(result.Results, ItemResult{
			LocalID:       p.LocalID,
			DeviceID:      p.DeviceID,
			CanonicalName: p.CanonicalName,
			ProfileID:     p.ProfileID,
			Status:        status,
		})
	}
	existing := len(out.Provisioned) - created
	for _, r := range out.Rejected {
		result.Results = append(result.Results, ItemResult{
			LocalID: r.LocalID,
			Status:  StatusRejected,
			Reason:  r.Reason,
		})
	}

	if created > 0 {
		s.emitAdvisory(ctx, tenantID, gatewayID, frame.Type, created, existing, len(out.Rejected))
	}
	s.log.Info("gateway discovery provisioned",
		"tenant", tenantID, "gateway", gatewayID,
		"created", created, "existing", existing, "rejected", len(out.Rejected))
	return result, nil
}

// resolveDefaultProfile resolves the policy default once per frame. An empty
// default is valid (devices get no profile). A configured-but-missing profile
// is a deployment error and fails the whole frame rather than silently
// creating profile-less devices.
func (s *Service) resolveDefaultProfile(ctx context.Context, tenantID, defaultProfile string) (string, error) {
	if strings.TrimSpace(defaultProfile) == "" {
		return "", nil
	}
	id, err := s.reg.ResolveProfileID(ctx, tenantID, defaultProfile)
	if err != nil {
		return "", fmt.Errorf("auto-provision default profile unavailable: %w", err)
	}
	return id, nil
}

// emitAdvisory publishes one provisioning summary. NATS failures are logged
// only; they must not roll back accepted devices.
func (s *Service) emitAdvisory(ctx context.Context, tenantID, gatewayID, source string,
	created, existing, rejected int) {
	if s.publisher == nil {
		return
	}
	raw, err := json.Marshal(AdvisoryProvisioned{
		Kind:      AdvisoryKindProvisioned,
		Time:      time.Now().UTC(),
		TenantID:  tenantID,
		GatewayID: gatewayID,
		Source:    source,
		Created:   created,
		Existing:  existing,
		Rejected:  rejected,
	})
	if err != nil {
		s.log.Error("discovery advisory marshal failed", "error", err)
		return
	}
	subject := fmt.Sprintf("%s.%s.%s", SubjectPrefix, tenantID, gatewayID)
	if err := s.publisher.Publish(subject, raw); err != nil {
		s.log.Error("discovery advisory publish failed", "error", err)
	}
}
