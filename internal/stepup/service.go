// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates the step-up lifecycle: Start (bind action to a
// fresh challenge), Complete (verify a fresh TOTP code) and Consume (one-time
// use by the protected endpoint). Every attempt is written to the chained
// audit log without the TOTP code or any secret content.

package stepup

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nereidai/nereidlink/internal/audit"
)

// TOTPVerifier validates a current TOTP code for one user, applying the
// existing failure-lockout policy. identity.Service implements it.
type TOTPVerifier interface {
	VerifyTOTP(ctx context.Context, userID, code string) error
}

// Service is the step-up orchestrator.
type Service struct {
	store    Store
	verifier TOTPVerifier
	key      []byte
	ttl      time.Duration

	// audit records challenge attempts; nil is tolerated in tests.
	audit *audit.Service
}

// New constructs a Service. signingKey is the HMAC key for signed QR payloads.
func New(store Store, verifier TOTPVerifier, signingKey []byte) *Service {
	return &Service{
		store:    store,
		verifier: verifier,
		key:      append([]byte(nil), signingKey...),
		ttl:      TTL,
	}
}

// SetAuditService wires the chained audit log.
func (s *Service) SetAuditService(service *audit.Service) {
	s.audit = service
}

// recordAudit appends an event, surfacing failures loudly without blocking
// the protected operation (audit.Append failure is already alerted inside
// audit, matching identity's behavior).
func (s *Service) recordAudit(ctx context.Context, in audit.Input) {
	if s.audit == nil {
		return
	}
	if _, err := s.audit.Append(ctx, in); err != nil {
		slog.Error("failed to write step-up audit event", "error", err, "action", in.Action)
	}
}

// Start creates a pending challenge for in.Action, returning it with the
// signed QR payload. The caller (HTTP layer) must already have refused
// machine principals and authorized the action's RBAC point.
func (s *Service) Start(ctx context.Context, in StartInput) (Challenge, error) {
	if in.Action == "" {
		return Challenge{}, errors.New("step-up action is required")
	}
	detailsSHA, err := hashDetails(in.Details)
	if err != nil {
		return Challenge{}, err
	}
	now := time.Now()
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return Challenge{}, fmt.Errorf("generate step-up id: %w", err)
	}
	challenge := Challenge{
		ID:            uuidString(id),
		TenantID:      in.TenantID,
		UserID:        in.UserID,
		Action:        in.Action,
		Details:       in.Details,
		DetailsSHA256: detailsSHA,
		Status:        StatusPending,
		ExpiresAt:     now.Add(s.ttl),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if err := s.store.Insert(ctx, challenge); err != nil {
		return Challenge{}, err
	}
	challenge.SignedPayload = signPayload(s.key, challenge.Action, detailsSHA, challenge.ExpiresAt)
	s.recordAudit(ctx, audit.Input{
		TenantID: in.TenantID, ActorID: in.UserID,
		EventType: audit.EventStepUp, Action: "start",
		ResourceType: "step_up_challenge", ResourceID: challenge.ID,
		Details: map[string]any{"step_up_action": in.Action},
	})
	return challenge, nil
}

// Complete verifies code against the user's current TOTP and advances the
// challenge to verified. The TOTP code is never logged or audited.
func (s *Service) Complete(ctx context.Context, tenantID, userID, challengeID, code, ip string) (Challenge, error) {
	if err := s.verifier.VerifyTOTP(ctx, userID, code); err != nil {
		s.recordAttempt(ctx, tenantID, userID, challengeID, "verify_failed", ip)
		return Challenge{}, err
	}
	now := time.Now()
	challenge, err := s.store.MarkVerified(ctx, tenantID, userID, challengeID, now)
	if err != nil {
		s.recordAttempt(ctx, tenantID, userID, challengeID, auditAction(err), ip)
		return Challenge{}, err
	}
	challenge.SignedPayload = signPayload(s.key, challenge.Action, challenge.DetailsSHA256, challenge.ExpiresAt)
	s.recordAudit(ctx, audit.Input{
		TenantID: tenantID, ActorID: userID,
		EventType: audit.EventStepUp, Action: "verify",
		ResourceType: "step_up_challenge", ResourceID: challengeID, IP: ip,
		Details: map[string]any{"step_up_action": challenge.Action},
	})
	return challenge, nil
}

// Consume atomically presents a verified challenge to the protected endpoint
// for exactly the action it was started for. Success destroys the
// challenge's usability; replay, expiry and action mismatch return distinct
// errors and are audited.
func (s *Service) Consume(ctx context.Context, tenantID, userID, challengeID, action, ip string) error {
	challenge, err := s.store.Get(ctx, tenantID, userID, challengeID)
	if err == nil && challenge.Action != action {
		s.recordAttempt(ctx, tenantID, userID, challengeID, "action_mismatch", ip)
		return ErrActionMismatch
	}
	if err != nil {
		s.recordAttempt(ctx, tenantID, userID, challengeID, auditAction(err), ip)
		return err
	}
	if _, err := s.store.Consume(ctx, tenantID, userID, challengeID, time.Now()); err != nil {
		s.recordAttempt(ctx, tenantID, userID, challengeID, auditAction(err), ip)
		return err
	}
	s.recordAudit(ctx, audit.Input{
		TenantID: tenantID, ActorID: userID,
		EventType: audit.EventStepUp, Action: "consume",
		ResourceType: "step_up_challenge", ResourceID: challengeID, IP: ip,
		Details: map[string]any{"step_up_action": action},
	})
	return nil
}

// recordAttempt audits a rejected challenge presentation without details of
// the submitted code.
func (s *Service) recordAttempt(ctx context.Context, tenantID, userID, challengeID, action, ip string) {
	s.recordAudit(ctx, audit.Input{
		TenantID: tenantID, ActorID: userID,
		EventType: audit.EventStepUp, Action: action,
		ResourceType: "step_up_challenge", ResourceID: challengeID, IP: ip,
	})
}

// auditAction maps a service/store error to the audit action label.
func auditAction(err error) string {
	switch {
	case errors.Is(err, ErrReplay):
		return "replay"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrActionMismatch):
		return "action_mismatch"
	case errors.Is(err, ErrNotVerified):
		return "not_verified"
	case errors.Is(err, ErrNotPending):
		return "not_pending"
	default:
		return "failed"
	}
}

// Sweep deletes expired challenges.
func (s *Service) Sweep(ctx context.Context) error {
	return s.store.DeleteExpired(ctx, time.Now())
}

// Run periodically sweeps expired rows until stopped.
func (s *Service) Run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.Sweep(ctx); err != nil {
				slog.Error("failed to sweep step-up challenges", "error", err)
			}
		}
	}
}

// uuidString renders random bytes as a version-4 UUID without a uuid
// dependency.
func uuidString(raw []byte) string {
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	h := fmt.Sprintf("%x", raw)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}
