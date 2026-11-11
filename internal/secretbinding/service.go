// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates the secret delivery desired state: it merges
// changes, builds the Core NATS push frame with secret fields replaced by
// references, redrives the full set after hello, and stores edge reports.

package secretbinding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/secrets"
)

// SecretMetaReader reads vault metadata for a secret being delivered.
type SecretMetaReader interface {
	Get(ctx context.Context, tenantID, name string) (secrets.Metadata, error)
}

// AlarmRaiser opens and clears delivery alarms (ADR-0034). *alarm.Service
// satisfies it.
type AlarmRaiser interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// SubjectDesiredPrefix starts the per-gateway desired-state subjects.
const SubjectDesiredPrefix = "nl.edge.secret.desired"

// desiredSubject addresses one gateway.
func desiredSubject(tenantID, gatewayID string) string {
	return SubjectDesiredPrefix + "." + tenantID + "." + gatewayID
}

// bindingStore is the persistence surface.
type bindingStore interface {
	Get(ctx context.Context, tenantID, gatewayID, secretName string) (Binding, error)
	ListByGateway(ctx context.Context, tenantID, gatewayID string, includeRemoved bool) ([]Binding, error)
	ListBySecret(ctx context.Context, tenantID, secretName string) ([]Binding, error)
	ActiveGatewayIDs(ctx context.Context, tenantID, secretName string) ([]string, error)
	References(ctx context.Context, tenantID, secretName string) ([]Reference, error)
	Upsert(ctx context.Context, tenantID, gatewayID string, change DesiredChange, at time.Time) (Binding, error)
	Withdraw(ctx context.Context, tenantID, gatewayID, secretName string, at time.Time) (Binding, error)
	MarkDispatched(ctx context.Context, tenantID, gatewayID string, revision int64, at time.Time) error
	Report(ctx context.Context, tenantID, gatewayID string, report Report, at time.Time) error
	MarkRemoved(ctx context.Context, tenantID, gatewayID, secretName string, at time.Time) error
	ClaimPendingForAlarm(ctx context.Context, ageCutoff time.Time) ([]Binding, error)
	ClaimFailedForRaise(ctx context.Context) ([]Binding, error)
	ClaimFailedForEscalation(ctx context.Context, ageCutoff time.Time) ([]Binding, error)
}

// DocumentOpener returns stored typed documents keyed by secret name.
type DocumentOpener interface {
	ResolveDocuments(ctx context.Context, tenantID string, names []string) (map[string]map[string]any, error)
}

// SecretPathResolver reports which document paths are secret for a type.
type SecretPathResolver interface {
	SecretPaths(ctx context.Context, tenantID, secretType, typeVersion string) ([]string, error)
}

// Entry is one secret in a delivery frame.
type Entry struct {
	Name      string         `json:"name"`
	Type      string         `json:"type,omitempty"`
	Version   string         `json:"version,omitempty"`
	Revision  int64          `json:"revision"`
	Removing  bool           `json:"removing,omitempty"`
	Selectors []Selector     `json:"selectors,omitempty"`
	Document  map[string]any `json:"document,omitempty"`
}

// PushFrame is the full desired set for one gateway.
type PushFrame struct {
	TenantID  string  `json:"tenant_id"`
	GatewayID string  `json:"gateway_id"`
	Entries   []Entry `json:"entries"`
}

// publisher publishes frames. *nats.Conn satisfies it.
type publisher interface {
	Publish(subject string, data []byte) error
}

// Tune configures the alarm sweep (mirrors connector tuning).
type Tune struct {
	// PendingAfter is how long a fresh desired revision may stay without
	// an APPLIED report before the WARNING alarm opens.
	PendingAfter time.Duration
	// FailedEscalationAfter is how long a continuously FAILED binding
	// stays MAJOR before escalation to CRITICAL.
	FailedEscalationAfter time.Duration
	// SweepInterval is how often the sweep runs.
	SweepInterval time.Duration
}

// DefaultTune returns the v1 defaults.
func DefaultTune() Tune {
	return Tune{
		PendingAfter:          5 * time.Minute,
		FailedEscalationAfter: time.Hour,
		SweepInterval:         time.Minute,
	}
}

// Service orchestrates delivery.
type Service struct {
	store     bindingStore
	nc        publisher
	docs      DocumentOpener
	paths     SecretPathResolver
	meta      SecretMetaReader
	alarms    AlarmRaiser
	tune      Tune
	log       *slog.Logger
	onRemoval RemovalHook
}

// RemovalHook is invoked after a binding reaches REMOVED so a deletion
// reconciler can finalize a pending vault delete once the last gateway
// confirms. It runs asynchronously: hook errors must never block or fail
// the edge status path.
type RemovalHook func(ctx context.Context, tenantID, secretName string)

// New constructs a Service.
func New(store bindingStore, nc publisher, docs DocumentOpener, paths SecretPathResolver) *Service {
	return &Service{
		store: store, nc: nc, docs: docs, paths: paths,
		tune: DefaultTune(), log: slog.Default(),
	}
}

// SetLogger overrides the sweep logger.
func (s *Service) SetLogger(log *slog.Logger) {
	s.log = log
}

// SetAlarmRaiser wires the delivery alarm service.
func (s *Service) SetAlarmRaiser(alarms AlarmRaiser) {
	s.alarms = alarms
}

// SetSecretMetaReader wires vault metadata lookup used by Deliver to fill
// the binding type/version from the stored secret.
func (s *Service) SetSecretMetaReader(meta SecretMetaReader) {
	s.meta = meta
}

// SetRemovalHook registers the post-REMOVED notifier (deletion finalizer).
func (s *Service) SetRemovalHook(hook RemovalHook) {
	s.onRemoval = hook
}

// References returns the gateways still holding a delivered copy (REST
// deletion-protection surface).
func (s *Service) References(ctx context.Context, tenantID, secretName string) ([]Reference, error) {
	return s.store.References(ctx, tenantID, secretName)
}

// Deliver declares a typed secret should be present on one gateway, fills
// type/version from the vault row, and publishes the full desired set.
func (s *Service) Deliver(ctx context.Context, tenantID, gatewayID, secretName string, selectors []Selector) (Binding, error) {
	if secretName == "" {
		return Binding{}, ErrEmptySecret
	}
	if s.meta == nil {
		return Binding{}, errors.New("secret meta reader not wired")
	}
	md, err := s.meta.Get(ctx, tenantID, secretName)
	if errors.Is(err, secrets.ErrNotFound) {
		return Binding{}, ErrSecretNotFound
	}
	if err != nil {
		return Binding{}, err
	}
	if md.Kind != secrets.KindDocument {
		return Binding{}, ErrSecretNotDeliverable
	}
	return s.Apply(ctx, tenantID, gatewayID, DesiredChange{
		SecretName:  secretName,
		SecretType:  md.SecretType,
		TypeVersion: md.TypeVersion,
		Selectors:   selectors,
	})
}

// Apply upserts one desired change and publishes the full set.
func (s *Service) Apply(ctx context.Context, tenantID, gatewayID string, change DesiredChange) (Binding, error) {
	if change.SecretName == "" {
		return Binding{}, ErrEmptySecret
	}
	if err := validateSelectors(change.Selectors); err != nil {
		return Binding{}, err
	}
	binding, err := s.store.Upsert(ctx, tenantID, gatewayID, change, time.Now())
	if err != nil {
		return Binding{}, err
	}
	if err := s.publishDesired(ctx, tenantID, gatewayID); err != nil {
		return binding, err
	}
	s.publishEvent(EventFrame{
		Kind:       EventDesired,
		TenantID:   tenantID,
		GatewayID:  gatewayID,
		SecretName: binding.SecretName,
		Revision:   binding.Revision,
	})
	return binding, nil
}

// Withdraw declares removal and republishes.
func (s *Service) Withdraw(ctx context.Context, tenantID, gatewayID, secretName string) error {
	binding, err := s.store.Withdraw(ctx, tenantID, gatewayID, secretName, time.Now())
	if err != nil {
		return err
	}
	if err := s.publishDesired(ctx, tenantID, gatewayID); err != nil {
		return err
	}
	s.publishEvent(EventFrame{
		Kind:       EventWithdrawn,
		TenantID:   tenantID,
		GatewayID:  gatewayID,
		SecretName: secretName,
		Revision:   binding.Revision,
	})
	return nil
}

// Redrive rebuilds and publishes the full desired set after a successful
// hello (idempotent re-delivery).
func (s *Service) Redrive(ctx context.Context, tenantID, gatewayID string) error {
	return s.publishDesired(ctx, tenantID, gatewayID)
}

// Report stores one edge status uplink.
func (s *Service) Report(ctx context.Context, tenantID, gatewayID string, report Report) error {
	return s.store.Report(ctx, tenantID, gatewayID, report, time.Now())
}

// HandleStatus processes a complete status frame, persisting each report.
// Unknown bindings are reported as an error for the caller to log; the
// uplink topic is fire-and-forget.
func (s *Service) HandleStatus(ctx context.Context, tenantID, gatewayID string, frame StatusFrame) error {
	var first error
	// removedNames collects entries confirmed REMOVED in this frame; after
	// processing, the removal hook is notified once per name.
	removedNames := map[string]bool{}
	for _, entry := range frame.Reports {
		if !knownStatus(entry.Status) {
			return fmt.Errorf("%w: %s", ErrUnknownStatus, entry.Status)
		}
		report := Report{
			SecretName: entry.Name,
			Status:     entry.Status,
			Reason:     entry.Reason,
		}
		if err := s.store.Report(ctx, tenantID, gatewayID, report, time.Now()); err != nil {
			if first == nil {
				first = fmt.Errorf("report %s: %w", entry.Name, err)
			}
			continue
		}
		// APPLIED or a confirmed REMOVED clears the delivery alarms. FAILED
		// does not raise inline: the periodic sweep claims newly failed
		// bindings, which keeps raising durable across a crash between the
		// report and the alarm write.
		if entry.Status == StatusApplied || entry.Status == StatusRemoved {
			s.clearAlarms(ctx, tenantID, gatewayID, entry.Name)
			if entry.Status == StatusRemoved {
				removedNames[entry.Name] = true
			}
		}
		s.publishEvent(EventFrame{
			Kind:       EventStatus,
			TenantID:   tenantID,
			GatewayID:  gatewayID,
			SecretName: entry.Name,
			Status:     entry.Status,
			Revision:   entry.Revision,
		})
	}
	if s.onRemoval != nil {
		for name := range removedNames {
			// Detach cancellation/deadline from the uplink while retaining
			// its values: status handling is fire-and-forget and must not
			// wait on vault finalization.
			go s.onRemoval(context.WithoutCancel(ctx), tenantID, name)
		}
	}
	return first
}

// clearAlarms clears the pending and failed alarms for one binding.
func (s *Service) clearAlarms(ctx context.Context, tenantID, gatewayID, secretName string) {
	if s.alarms == nil {
		return
	}
	for _, key := range []string{DedupePendingKey(secretName), DedupeFailedKey(secretName)} {
		if _, err := s.alarms.ClearAlarm(ctx, tenantID, gatewayID, key); err != nil {
			s.log.Error("clear secret binding alarm failed",
				"secret", secretName, "error", err)
		}
	}
}

// Run runs the pending/failed alarm sweep until ctx is canceled. Desired
// re-send on reconnect is driven by the ingress post-hello Redrive call,
// not a presence subscription.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.tune.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			s.sweep(ctx, time.Now().UTC())
		}
	}
}

// sweep raises the pending alarm for stale open bindings and escalates
// long-FAILED bindings exactly once (the claim UPDATEs are atomic across
// replicas).
func (s *Service) sweep(ctx context.Context, now time.Time) {
	pending, err := s.store.ClaimPendingForAlarm(ctx, now.Add(-s.tune.PendingAfter))
	if err != nil {
		s.log.Error("claim pending secret bindings failed", "error", err)
	} else {
		for i := range pending {
			s.raisePending(ctx, pending[i])
		}
	}

	newFailed, err := s.store.ClaimFailedForRaise(ctx)
	if err != nil {
		s.log.Error("claim new failed secret bindings failed", "error", err)
	} else {
		for i := range newFailed {
			s.raiseFailed(ctx, newFailed[i], false)
		}
	}

	due, err := s.store.ClaimFailedForEscalation(ctx, now.Add(-s.tune.FailedEscalationAfter))
	if err != nil {
		s.log.Error("claim failed-escalation secret bindings failed", "error", err)
		return
	}
	for i := range due {
		s.raiseFailed(ctx, due[i], true)
	}
}

// raisePending opens the WARNING not-applied alarm for a stuck binding.
func (s *Service) raisePending(ctx context.Context, binding Binding) {
	if s.alarms == nil {
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  binding.TenantID,
		DeviceID:  binding.GatewayID,
		AlarmType: AlarmTypePending,
		Severity:  alarm.SeverityWarning,
		DedupeKey: DedupePendingKey(binding.SecretName),
		Details: map[string]any{
			"secret_name": binding.SecretName,
			"secret_type": binding.SecretType,
			"revision":    binding.Revision,
		},
	}); err != nil {
		s.log.Error("raise secret binding pending alarm failed",
			"secret", binding.SecretName, "error", err)
	}
}

// raiseFailed opens (or escalates) the failed binding alarm. escalated
// selects CRITICAL for the 1-hour sweep; the first claim is MAJOR.
func (s *Service) raiseFailed(ctx context.Context, binding Binding, escalated bool) {
	if s.alarms == nil {
		return
	}
	severity := alarm.SeverityMajor
	if escalated {
		severity = alarm.SeverityCritical
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  binding.TenantID,
		DeviceID:  binding.GatewayID,
		AlarmType: AlarmTypeFailed,
		Severity:  severity,
		DedupeKey: DedupeFailedKey(binding.SecretName),
		Details: map[string]any{
			"secret_name": binding.SecretName,
			"secret_type": binding.SecretType,
			"revision":    binding.Revision,
			"reason":      binding.Reason,
		},
	}); err != nil {
		s.log.Error("raise secret binding failed alarm failed",
			"secret", binding.SecretName, "error", err)
	}
}

// knownStatus reports whether the edge status value is a recognized uplink
// state. PENDING/DISPATCHED/REMOVING are platform-side only and may not be
// reported by the edge.
func knownStatus(status string) bool {
	switch status {
	case StatusApplied, StatusFailed, StatusRemoved:
		return true
	default:
		return false
	}
}

// Bindings returns the gateway's non-removed bindings (REST surface).
func (s *Service) Bindings(ctx context.Context, tenantID, gatewayID string) ([]Binding, error) {
	return s.store.ListByGateway(ctx, tenantID, gatewayID, false)
}

// MarkDispatched transitions the given revisions to DISPATCHED after the
// MQTT packet has been written to the live gateway session.
func (s *Service) MarkDispatched(ctx context.Context, tenantID, gatewayID string, revisions []int64) error {
	for _, revision := range revisions {
		if err := s.store.MarkDispatched(ctx, tenantID, gatewayID, revision, time.Now()); err != nil {
			return err
		}
	}
	return nil
}

// publishDesired assembles the frame from non-removed bindings (REMOVING
// rows included so the edge receives the removal), substitutes secret
// references, and publishes onto the gateway subject.
func (s *Service) publishDesired(ctx context.Context, tenantID, gatewayID string) error {
	bindings, err := s.store.ListByGateway(ctx, tenantID, gatewayID, false)
	if err != nil {
		return err
	}
	frame := PushFrame{
		TenantID:  tenantID,
		GatewayID: gatewayID,
		Entries:   make([]Entry, 0, len(bindings)),
	}
	names := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Status != StatusRemoving {
			names = append(names, binding.SecretName)
		}
	}
	documents, err := s.docs.ResolveDocuments(ctx, tenantID, names)
	if err != nil {
		return fmt.Errorf("open delivery documents: %w", err)
	}
	for _, binding := range bindings {
		entry := Entry{
			Name:      binding.SecretName,
			Type:      binding.SecretType,
			Version:   binding.TypeVersion,
			Revision:  binding.Revision,
			Removing:  binding.Status == StatusRemoving,
			Selectors: binding.Selectors,
		}
		if !entry.Removing {
			doc := documents[binding.SecretName]
			masked, err := s.maskSecretFields(ctx, tenantID, binding, doc)
			if err != nil {
				return err
			}
			entry.Document = masked
		}
		frame.Entries = append(frame.Entries, entry)
	}
	raw, err := jsonMarshal(frame)
	if err != nil {
		return err
	}
	return s.nc.Publish(desiredSubject(tenantID, gatewayID), raw)
}

// maskSecretFields replaces each secret path in doc with a
// secret://name#field reference for transport over Core NATS.
func (s *Service) maskSecretFields(ctx context.Context, tenantID string, binding Binding, doc map[string]any) (map[string]any, error) {
	secretPaths, err := s.paths.SecretPaths(ctx, tenantID, binding.SecretType, binding.TypeVersion)
	if err != nil {
		return nil, err
	}
	out := cloneMap(doc)
	for _, path := range secretPaths {
		if _, ok := valueAtPath(out, path); ok {
			setPath(out, path, fmt.Sprintf("secret://%s#%s", binding.SecretName, path))
		}
	}
	return out, nil
}

// validateSelectors checks every selector row.
func validateSelectors(selectors []Selector) error {
	for _, selector := range selectors {
		if selector.Kind == "" || selector.Value == "" {
			return ErrInvalidSelector
		}
	}
	return nil
}

// publishEvent emits one user-plane notification. Events are advisory and
// best-effort: a publish failure only logs; the console re-fetches state.
func (s *Service) publishEvent(event EventFrame) {
	raw, err := jsonMarshal(event)
	if err != nil {
		return
	}
	subject := SubjectEventPrefix + "." + event.TenantID
	if err := s.nc.Publish(subject, raw); err != nil {
		s.log.Error("publish secret binding event failed",
			"error", err, "subject", subject)
	}
}
