// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go orchestrates profile delivery: it reconciles the target set,
// bumps the content revision when the projection changes, publishes the full
// desired set, redrives after hello, and stores edge reports (ADR-0038).

package profilepush

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// AlarmRaiser opens and clears delivery alarms. *alarm.Service satisfies it.
type AlarmRaiser interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// deliveryStore is the persistence surface.
type deliveryStore interface {
	LibraryProfile(ctx context.Context, tenantID, profileID string) (LibraryProfile, error)
	LibraryProfileByName(ctx context.Context, tenantID, name string) (LibraryProfile, error)
	AddTarget(ctx context.Context, tenantID, profileID, gatewayID string, at time.Time) error
	RemoveTarget(ctx context.Context, tenantID, profileID, gatewayID string) error
	ListTargets(ctx context.Context, tenantID, profileID string) ([]Target, error)
	ReferencingGateways(ctx context.Context, tenantID, profileID string) ([]string, error)
	ReferencedProfiles(ctx context.Context, tenantID, gatewayID string) ([]string, error)
	TargetedProfiles(ctx context.Context, tenantID, gatewayID string) ([]string, error)
	SyncDesired(ctx context.Context, tenantID, profileID string, revision int64, gatewayIDs []string, at time.Time) error
	MarkRemoving(ctx context.Context, tenantID, profileID, gatewayID string, at time.Time) error
	DeliveriesByGateway(ctx context.Context, tenantID, gatewayID string) ([]Delivery, error)
	DeliveriesByProfile(ctx context.Context, tenantID, profileID string) ([]Delivery, error)
	MarkDispatched(ctx context.Context, tenantID, profileID, gatewayID string, revision int64, at time.Time) error
	Report(ctx context.Context, tenantID, gatewayID string, report StatusEntry, at time.Time) error
	BumpRevision(ctx context.Context, tenantID, profileID, desiredHash string, at time.Time) (int64, error)
	GatewayFormats(ctx context.Context, tenantID, gatewayID string) ([]string, error)
	ServiceNameForProtocol(ctx context.Context, protocol string) (string, error)
	ClaimPendingForAlarm(ctx context.Context, ageCutoff time.Time) ([]Delivery, error)
	ClaimFailedForRaise(ctx context.Context) ([]Delivery, error)
	ClaimFailedForEscalation(ctx context.Context, ageCutoff time.Time) ([]Delivery, error)
}

// publisher publishes frames. *nats.Conn satisfies it.
type publisher interface {
	Publish(subject string, data []byte) error
}

// Tune configures the alarm sweep (mirrors connector tuning).
type Tune struct {
	// PendingAfter is how long a fresh desired revision may stay without an
	// APPLIED report before the WARNING alarm opens.
	PendingAfter time.Duration
	// FailedEscalationAfter is how long a continuously failed delivery stays
	// MAJOR before escalation to CRITICAL.
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
	store  deliveryStore
	nc     publisher
	alarms AlarmRaiser
	tune   Tune
	log    *slog.Logger
}

// New constructs a Service.
func New(store deliveryStore, nc publisher) *Service {
	return &Service{
		store: store, nc: nc,
		tune: DefaultTune(), log: slog.Default(),
	}
}

// SetLogger overrides the sweep logger.
func (s *Service) SetLogger(log *slog.Logger) { s.log = log }

// SetAlarmRaiser wires the delivery alarm service.
func (s *Service) SetAlarmRaiser(alarms AlarmRaiser) { s.alarms = alarms }

// Distribute adds an explicit distribution edge and republishes the affected
// gateway. The library profile is referenced, never copied.
func (s *Service) Distribute(ctx context.Context, tenantID, profileID, gatewayID string) error {
	if profileID == "" || gatewayID == "" {
		return ErrInvalid
	}
	if _, err := s.store.LibraryProfile(ctx, tenantID, profileID); err != nil {
		return err
	}
	if err := s.store.AddTarget(ctx, tenantID, profileID, gatewayID, time.Now().UTC()); err != nil {
		return err
	}
	return s.ReconcileProfile(ctx, tenantID, profileID)
}

// WithdrawTarget removes an explicit distribution edge and, when the gateway
// no longer needs the profile by any route, sends a removal frame.
func (s *Service) WithdrawTarget(ctx context.Context, tenantID, profileID, gatewayID string) error {
	if profileID == "" || gatewayID == "" {
		return ErrInvalid
	}
	if err := s.store.RemoveTarget(ctx, tenantID, profileID, gatewayID); err != nil {
		return err
	}
	stillReferenced, err := s.gatewayReferencesProfile(ctx, tenantID, gatewayID, profileID)
	if err != nil {
		return err
	}
	if stillReferenced {
		return nil
	}
	if err := s.store.MarkRemoving(ctx, tenantID, profileID, gatewayID, time.Now().UTC()); err != nil {
		return err
	}
	return s.publishDesired(ctx, tenantID, gatewayID)
}

// Targets lists a profile's explicit distribution edges.
func (s *Service) Targets(ctx context.Context, tenantID, profileID string) ([]Target, error) {
	return s.store.ListTargets(ctx, tenantID, profileID)
}

// DeliveriesForGateway lists a gateway's delivery states.
func (s *Service) DeliveriesForGateway(ctx context.Context, tenantID, gatewayID string) ([]Delivery, error) {
	return s.store.DeliveriesByGateway(ctx, tenantID, gatewayID)
}

// DeliveriesForProfile lists a profile's delivery states across gateways.
func (s *Service) DeliveriesForProfile(ctx context.Context, tenantID, profileID string) ([]Delivery, error) {
	return s.store.DeliveriesByProfile(ctx, tenantID, profileID)
}

// ReconcileProfile bumps the profile revision when its rendered projection
// changed, syncs the desired rows for every target gateway (explicit ∪
// referenced), and republishes each affected gateway.
func (s *Service) ReconcileProfile(ctx context.Context, tenantID, profileID string) error {
	profile, err := s.store.LibraryProfile(ctx, tenantID, profileID)
	if err != nil {
		return err
	}
	hash, err := projectionHash(profile)
	if err != nil {
		return err
	}
	revision, err := s.store.BumpRevision(ctx, tenantID, profileID, hash, time.Now().UTC())
	if err != nil {
		return err
	}
	gateways, err := s.targetGateways(ctx, tenantID, profileID)
	if err != nil {
		return err
	}
	if err := s.store.SyncDesired(ctx, tenantID, profileID, revision, gateways, time.Now().UTC()); err != nil {
		return err
	}
	for _, gatewayID := range gateways {
		if err := s.publishDesired(ctx, tenantID, gatewayID); err != nil {
			s.log.Error("publish profile desired failed",
				"gateway", gatewayID, "profile", profileID, "error", err)
		}
	}
	s.publishEvent(EventFrame{
		Kind:        EventDesired,
		TenantID:    tenantID,
		ProfileID:   profileID,
		ProfileName: profile.Name,
		Revision:    revision,
	})
	return nil
}

// ReconcileGateway ensures every profile a gateway needs (explicit ∪
// referenced) has a delivery row, and republishes the gateway's full set.
func (s *Service) ReconcileGateway(ctx context.Context, tenantID, gatewayID string) error {
	profiles, err := s.gatewayProfiles(ctx, tenantID, gatewayID)
	if err != nil {
		return err
	}
	for _, profileID := range profiles {
		profile, err := s.store.LibraryProfile(ctx, tenantID, profileID)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return err
		}
		hash, err := projectionHash(profile)
		if err != nil {
			return err
		}
		revision, err := s.store.BumpRevision(ctx, tenantID, profileID, hash, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := s.store.SyncDesired(ctx, tenantID, profileID, revision,
			[]string{gatewayID}, time.Now().UTC()); err != nil {
			return err
		}
	}
	return s.publishDesired(ctx, tenantID, gatewayID)
}

// Redrive republishes the full desired set after a successful hello.
func (s *Service) Redrive(ctx context.Context, tenantID, gatewayID string) error {
	return s.publishDesired(ctx, tenantID, gatewayID)
}

// HandleStatus processes a complete edge status frame. Unknown deliveries are
// reported as an error for the caller to log; the uplink is fire-and-forget.
func (s *Service) HandleStatus(ctx context.Context, tenantID, gatewayID string, frame StatusFrame) error {
	var first error
	for _, entry := range frame.Reports {
		if !knownStatus(entry.Status) {
			return fmt.Errorf("%w: %s", ErrUnknownStatus, entry.Status)
		}
		if err := s.store.Report(ctx, tenantID, gatewayID, entry, time.Now().UTC()); err != nil {
			if first == nil {
				first = fmt.Errorf("report %s: %w", entry.Name, err)
			}
			continue
		}
		if entry.Status == StateApplied || entry.Status == StateRemoved {
			s.clearAlarms(ctx, tenantID, gatewayID, entry.Name)
		}
		s.publishEvent(EventFrame{
			Kind:      EventStatus,
			TenantID:  tenantID,
			GatewayID: gatewayID,
			Status:    entry.Status,
			Revision:  entry.Revision,
		})
	}
	return first
}

// MarkDispatched transitions a revision to DISPATCHED after the MQTT packet
// has been written to the live gateway session.
func (s *Service) MarkDispatched(ctx context.Context, tenantID, gatewayID, profileID string, revision int64) error {
	return s.store.MarkDispatched(ctx, tenantID, profileID, gatewayID, revision, time.Now().UTC())
}

// Run runs the pending/failed alarm sweep until ctx is canceled. Desired
// re-send on reconnect is driven by the ingress post-hello Redrive call.
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

// gatewayReferencesProfile reports whether a gateway's connector instances
// still reference a profile.
func (s *Service) gatewayReferencesProfile(ctx context.Context, tenantID, gatewayID, profileID string) (bool, error) {
	referenced, err := s.store.ReferencedProfiles(ctx, tenantID, gatewayID)
	if err != nil {
		return false, err
	}
	for _, id := range referenced {
		if id == profileID {
			return true, nil
		}
	}
	return false, nil
}

// targetGateways returns the explicit ∪ referenced gateway set for a profile.
func (s *Service) targetGateways(ctx context.Context, tenantID, profileID string) ([]string, error) {
	referencing, err := s.store.ReferencingGateways(ctx, tenantID, profileID)
	if err != nil {
		return nil, err
	}
	targets, err := s.store.ListTargets(ctx, tenantID, profileID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(referencing)+len(targets))
	for _, id := range referencing {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, t := range targets {
		if !seen[t.GatewayID] {
			seen[t.GatewayID] = true
			out = append(out, t.GatewayID)
		}
	}
	return out, nil
}

// gatewayProfiles returns the explicit ∪ referenced profile set for a gateway.
func (s *Service) gatewayProfiles(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	referenced, err := s.store.ReferencedProfiles(ctx, tenantID, gatewayID)
	if err != nil {
		return nil, err
	}
	targeted, err := s.store.TargetedProfiles(ctx, tenantID, gatewayID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(referenced)+len(targeted))
	for _, id := range append(referenced, targeted...) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out, nil
}

// publishDesired assembles the full desired set for one gateway (REMOVING rows
// included so the edge receives removals) and publishes it onto the gateway
// subject.
func (s *Service) publishDesired(ctx context.Context, tenantID, gatewayID string) error {
	deliveries, err := s.store.DeliveriesByGateway(ctx, tenantID, gatewayID)
	if err != nil {
		return err
	}
	formats, err := s.store.GatewayFormats(ctx, tenantID, gatewayID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	format := SelectFormat(formats)

	frame := PushFrame{
		TenantID:  tenantID,
		GatewayID: gatewayID,
		Entries:   make([]Entry, 0, len(deliveries)),
	}
	for _, d := range deliveries {
		entry := Entry{
			ProfileID: d.ProfileID,
			Name:      d.ProfileName,
			Revision:  d.DesiredRevision,
			Format:    format,
			Removing:  d.State == StateRemoving,
		}
		if !entry.Removing {
			doc, target, err := s.renderEntry(ctx, tenantID, d.ProfileID, format)
			if err != nil {
				return err
			}
			entry.Document = doc
			entry.Target = target
		}
		frame.Entries = append(frame.Entries, entry)
	}
	raw, err := canonicalJSON(frame)
	if err != nil {
		return err
	}
	subject := desiredSubject(tenantID, gatewayID)
	if s.nc == nil {
		return nil
	}
	return s.nc.Publish(subject, raw)
}

// renderEntry loads a library profile, renders it in the given format, and
// derives the optional opaque target hint from the protocol family.
func (s *Service) renderEntry(ctx context.Context, tenantID, profileID, format string) (map[string]any, map[string]any, error) {
	profile, err := s.store.LibraryProfile(ctx, tenantID, profileID)
	if err != nil {
		return nil, nil, err
	}
	doc, err := Render(format, ProjectionOf(profile))
	if err != nil {
		return nil, nil, err
	}
	var target map[string]any
	if service, err := s.store.ServiceNameForProtocol(ctx, profile.Protocol); err == nil && service != "" {
		target = map[string]any{"service": service}
	}
	return doc, target, nil
}

// sweep raises the pending alarm for stale open rows and escalates long-failed
// rows exactly once (the claim UPDATEs are atomic across replicas).
func (s *Service) sweep(ctx context.Context, now time.Time) {
	pending, err := s.store.ClaimPendingForAlarm(ctx, now.Add(-s.tune.PendingAfter))
	if err != nil {
		s.log.Error("claim pending profile deliveries failed", "error", err)
	} else {
		for i := range pending {
			s.raisePending(ctx, pending[i])
		}
	}

	newFailed, err := s.store.ClaimFailedForRaise(ctx)
	if err != nil {
		s.log.Error("claim new failed profile deliveries failed", "error", err)
	} else {
		for i := range newFailed {
			s.raiseFailed(ctx, newFailed[i], false)
		}
	}

	due, err := s.store.ClaimFailedForEscalation(ctx, now.Add(-s.tune.FailedEscalationAfter))
	if err != nil {
		s.log.Error("claim failed-escalation profile deliveries failed", "error", err)
		return
	}
	for i := range due {
		s.raiseFailed(ctx, due[i], true)
	}
}

// raisePending opens the WARNING not-applied alarm for a stuck delivery.
func (s *Service) raisePending(ctx context.Context, d Delivery) {
	if s.alarms == nil {
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  d.TenantID,
		DeviceID:  d.GatewayID,
		AlarmType: AlarmTypePending,
		Severity:  alarm.SeverityWarning,
		DedupeKey: DedupePendingKey(d.ProfileID),
		Details: map[string]any{
			"profile_name": d.ProfileName,
			"revision":     d.DesiredRevision,
		},
	}); err != nil {
		s.log.Error("raise profile pending alarm failed",
			"profile", d.ProfileName, "error", err)
	}
}

// raiseFailed opens (or escalates) the failed delivery alarm. escalated
// selects CRITICAL for the 1-hour sweep; the first claim is MAJOR.
func (s *Service) raiseFailed(ctx context.Context, d Delivery, escalated bool) {
	if s.alarms == nil {
		return
	}
	severity := alarm.SeverityMajor
	if escalated {
		severity = alarm.SeverityCritical
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  d.TenantID,
		DeviceID:  d.GatewayID,
		AlarmType: AlarmTypeFailed,
		Severity:  severity,
		DedupeKey: DedupeFailedKey(d.ProfileID),
		Details: map[string]any{
			"profile_name": d.ProfileName,
			"revision":     d.DesiredRevision,
			"reason":       d.Reason,
		},
	}); err != nil {
		s.log.Error("raise profile failed alarm failed",
			"profile", d.ProfileName, "error", err)
	}
}

// clearAlarms clears the pending and failed alarms for one delivery.
func (s *Service) clearAlarms(ctx context.Context, tenantID, gatewayID, profileName string) {
	if s.alarms == nil {
		return
	}
	profile, err := s.store.LibraryProfileByName(ctx, tenantID, profileName)
	if err != nil {
		return
	}
	for _, key := range []string{DedupePendingKey(profile.ID), DedupeFailedKey(profile.ID)} {
		if _, err := s.alarms.ClearAlarm(ctx, tenantID, gatewayID, key); err != nil {
			s.log.Error("clear profile delivery alarm failed",
				"profile", profileName, "error", err)
		}
	}
}

// publishEvent emits one user-plane notification. Events are advisory and
// best-effort: a publish failure only logs; the console re-fetches state.
func (s *Service) publishEvent(event EventFrame) {
	if s.nc == nil {
		return
	}
	raw, err := canonicalJSON(event)
	if err != nil {
		return
	}
	subject := SubjectEventPrefix + "." + event.TenantID
	if err := s.nc.Publish(subject, raw); err != nil {
		s.log.Error("publish profile event failed",
			"error", err, "subject", subject)
	}
}

// desiredSubject addresses one gateway's desired profile set.
func desiredSubject(tenantID, gatewayID string) string {
	return SubjectDesiredPrefix + "." + tenantID + "." + gatewayID
}

// ProjectionOf builds the edge-visible projection from a library profile.
func ProjectionOf(p LibraryProfile) Projection {
	return Projection{
		Name:        p.Name,
		Protocol:    p.Protocol,
		Description: p.Description,
		Labels:      p.Labels,
		Points:      p.Points,
		Commands:    p.Commands,
	}
}

// projectionHash renders the projection in the neutral format and hashes it, so
// the revision tracks the edge-visible content only.
func projectionHash(p LibraryProfile) (string, error) {
	doc, err := Render(FormatNeutral, ProjectionOf(p))
	if err != nil {
		return "", err
	}
	raw, err := canonicalJSON(doc)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}
