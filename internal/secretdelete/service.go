// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates forced secret deletion: withdraw all delivered
// copies, record the pending marker, and hard-delete the vault original
// once every binding is gone (hook or sweep).

package secretdelete

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/secretbinding"
)

// pendingStore is the persistence surface. *Store satisfies it.
type pendingStore interface {
	Upsert(ctx context.Context, p PendingDelete) error
	List(ctx context.Context) ([]PendingDelete, error)
	Exists(ctx context.Context, tenantID, secretName string) (bool, error)
	Delete(ctx context.Context, tenantID, secretName string) error
}

// BindingAdmin withdraws and lists delivered copies. *secretbinding.Service
// satisfies it.
type BindingAdmin interface {
	References(ctx context.Context, tenantID, secretName string) ([]secretbinding.Reference, error)
	Withdraw(ctx context.Context, tenantID, gatewayID, secretName string) error
}

// VaultStore hard-deletes the vault original. *secrets.Store satisfies it.
type VaultStore interface {
	Delete(ctx context.Context, tenantID, name string) error
}

// Auditor appends chained events. *audit.Service satisfies it.
type Auditor interface {
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// Service orchestrates deletion protection.
type Service struct {
	store         pendingStore
	bindings      BindingAdmin
	vault         VaultStore
	auditor       Auditor
	sweepInterval time.Duration
	log           *slog.Logger
}

// New constructs a Service.
func New(store pendingStore, bindings BindingAdmin, vault VaultStore) *Service {
	return &Service{
		store:         store,
		bindings:      bindings,
		vault:         vault,
		sweepInterval: DefaultSweepInterval,
		log:           slog.Default(),
	}
}

// SetAuditor wires the chained audit appender.
func (s *Service) SetAuditor(auditor Auditor) {
	s.auditor = auditor
}

// References returns the gateways still holding a copy (proxied for the
// REST layer, which then depends only on this service).
func (s *Service) References(ctx context.Context, tenantID, secretName string) ([]secretbinding.Reference, error) {
	return s.bindings.References(ctx, tenantID, secretName)
}

// ForceDelete withdraws every delivered copy and records the pending
// marker. Withdrawal failures on individual gateways do not abort: an
// offline gateway's REMOVING row is durable and confirmed on reconnect.
// The vault original is untouched until finalizeIfDone sees zero refs.
func (s *Service) ForceDelete(ctx context.Context, tenantID, secretName string, actor Actor) (PendingDelete, error) {
	refs, err := s.bindings.References(ctx, tenantID, secretName)
	if err != nil {
		return PendingDelete{}, fmt.Errorf("load references: %w", err)
	}

	// Convergent fast path: nothing references the secret anymore.
	if len(refs) == 0 {
		if err := s.vault.Delete(ctx, tenantID, secretName); err != nil {
			return PendingDelete{}, fmt.Errorf("delete vault original: %w", err)
		}
		s.appendAudit(ctx, tenantID, secretName, actor, "force-delete-complete", 0)
		return PendingDelete{}, nil
	}

	for _, ref := range refs {
		// ErrNotFound means a concurrent withdrawal already moved the
		// binding; that is an expected race, not a failure.
		if err := s.bindings.Withdraw(ctx, tenantID, ref.GatewayID, secretName); err != nil &&
			!errors.Is(err, secretbinding.ErrNotFound) {
			return PendingDelete{}, fmt.Errorf("withdraw from %s: %w", ref.GatewayID, err)
		}
	}

	pending := PendingDelete{
		TenantID:    tenantID,
		SecretName:  secretName,
		InitiatedBy: actor.UserID,
		InitiatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if err := s.store.Upsert(ctx, pending); err != nil {
		return PendingDelete{}, err
	}
	s.appendAudit(ctx, tenantID, secretName, actor, "force-delete", len(refs))

	// Normally the REMOVING rows cannot be terminal yet, but if every
	// gateway already answered between the withdraw and the marker write,
	// finalize immediately.
	if _, err := s.finalizeIfDone(ctx, tenantID, secretName); err != nil {
		return pending, err
	}
	return pending, nil
}

// OnRemoval is the secretbinding post-REMOVED hook. It finalizes the
// pending deletion when the last referencing binding disappears.
func (s *Service) OnRemoval(ctx context.Context, tenantID, secretName string) {
	if _, err := s.finalizeIfDone(ctx, tenantID, secretName); err != nil {
		s.log.Error("finalize forced secret delete after removal failed",
			"secret", secretName, "error", err)
	}
}

// finalizeIfDone deletes the vault original and the marker when a pending
// deletion exists and no binding references the secret. It returns whether
// the deletion completed.
func (s *Service) finalizeIfDone(ctx context.Context, tenantID, secretName string) (bool, error) {
	exists, err := s.store.Exists(ctx, tenantID, secretName)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, nil
	}
	refs, err := s.bindings.References(ctx, tenantID, secretName)
	if err != nil {
		return false, fmt.Errorf("recheck references: %w", err)
	}
	if len(refs) > 0 {
		return false, nil
	}
	if err := s.vault.Delete(ctx, tenantID, secretName); err != nil {
		return false, fmt.Errorf("delete vault original: %w", err)
	}
	if err := s.store.Delete(ctx, tenantID, secretName); err != nil {
		return false, err
	}
	s.appendAudit(ctx, tenantID, secretName, Actor{}, "force-delete-complete", 0)
	return true, nil
}

// Run re-checks pending rows periodically until ctx is canceled. It
// re-issues withdrawals for bindings that are somehow still active and
// finalizes rows whose references are gone, covering missed hook
// notifications.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.sweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx)
		}
	}
}

// sweep iterates all pending rows and converges them.
func (s *Service) sweep(ctx context.Context) {
	pending, err := s.store.List(ctx)
	if err != nil {
		s.log.Error("list pending secret deletes failed", "error", err)
		return
	}
	for _, p := range pending {
		refs, err := s.bindings.References(ctx, p.TenantID, p.SecretName)
		if err != nil {
			s.log.Error("recheck pending delete references failed",
				"secret", p.SecretName, "error", err)
			continue
		}
		for _, ref := range refs {
			if ref.Status == secretbinding.StatusRemoving {
				// Withdrawal already declared; wait for the edge
				// confirmation.
				continue
			}
			if err := s.bindings.Withdraw(ctx, p.TenantID, ref.GatewayID, p.SecretName); err != nil &&
				!errors.Is(err, secretbinding.ErrNotFound) {
				s.log.Error("retry withdrawal failed",
					"secret", p.SecretName, "gateway", ref.GatewayID, "error", err)
			}
		}
		if _, err := s.finalizeIfDone(ctx, p.TenantID, p.SecretName); err != nil {
			s.log.Error("finalize pending secret delete failed",
				"secret", p.SecretName, "error", err)
		}
	}
}

// appendAudit records one chained vault event. Audit failure is logged and
// never blocks the deletion path; no secret content is ever included.
func (s *Service) appendAudit(ctx context.Context, tenantID, secretName string, actor Actor, action string, gatewayCount int) {
	if s.auditor == nil {
		return
	}
	event := audit.Input{
		TenantID:     tenantID,
		ActorID:      actor.UserID,
		ActorName:    actor.Email,
		EventType:    audit.EventCredential,
		Action:       action,
		ResourceType: "tenant_secret",
		ResourceID:   secretName,
		Details: map[string]any{
			"name":          secretName,
			"gateway_count": gatewayCount,
		},
	}
	if _, err := s.auditor.Append(ctx, event); err != nil {
		s.log.Error("audit forced secret delete failed", "action", action, "error", err)
	}
}
