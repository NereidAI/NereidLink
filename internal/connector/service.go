// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the connector-instance lifecycle orchestrator. It writes
// desired specs, publishes downlink PushFrames on Core NATS for the MQTT
// ingress to deliver, re-sends the full desired set when a gateway reports
// ONLINE, consumes edge-reported status, and runs the not-applied /
// failed-escalation alarm sweeps.
package connector

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/secrets"
)

// Store is the persistence surface the service needs, satisfied by *Store.
type Store interface {
	GatewayExists(ctx context.Context, tenantID, gatewayID string) (bool, error)
	DeviceBelongsToGateway(ctx context.Context, tenantID, gatewayID, deviceID string) (bool, error)
	FetchTemplates(ctx context.Context, tenantID string, profileIDs []string) (map[string]Template, error)
	Create(ctx context.Context, tenantID, gatewayID string, in SaveInput, connectorType, typeVersion string) (Spec, error)
	Update(ctx context.Context, tenantID, specID string, in SaveInput, connectorType, typeVersion string) (Spec, error)
	Delete(ctx context.Context, tenantID, specID string) (Spec, error)
	Get(ctx context.Context, tenantID, specID string) (Spec, error)
	ListByGateway(ctx context.Context, tenantID, gatewayID string) ([]Spec, error)
	ListAllForGateway(ctx context.Context, gatewayID string) ([]Spec, error)
	ListDeleteTombstones(ctx context.Context, gatewayID string, limit int) ([]DeleteTombstone, error)
	MarkDeleteAttempt(ctx context.Context, tombstone DeleteTombstone, at time.Time, lastError string) error
	MarkDispatched(ctx context.Context, tenantID, specID string, at time.Time) error
	ApplyStatus(ctx context.Context, tenantID string, f StatusFrame, at time.Time) (Spec, error)
	StampManifest(ctx context.Context, tenantID, gatewayID string, reported []ReportedInstance, at time.Time) ([]Spec, error)
	GetWatermark(ctx context.Context, tenantID, gatewayID string) (ManifestWatermark, error)
	BumpRevision(ctx context.Context, tenantID, specID string) (Spec, error)
	ClaimPendingForAlarm(ctx context.Context, now, ageCutoff time.Time) ([]Spec, error)
	ClaimFailedForRaise(ctx context.Context, now time.Time) ([]Spec, error)
	ClaimFailedForEscalation(ctx context.Context, now, ageCutoff time.Time) ([]Spec, error)
}

// AlarmRaiser opens and clears the deduplicated connector alarms. Satisfied
// by *alarm.Service; nil disables alarm actions.
type AlarmRaiser interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// Tuning controls the alarm sweeps.
type Tuning struct {
	// PendingAfter is how long a fresh desired revision may stay without an
	// APPLIED report before the WARNING event/alarm is raised.
	PendingAfter time.Duration

	// FailedEscalationAfter is how long a continuously FAILED connector stays
	// at MAJOR before being escalated to CRITICAL.
	FailedEscalationAfter time.Duration

	// SweepInterval is how often both sweeps run.
	SweepInterval time.Duration

	// StrictReconcile makes desired win on a manifest drift: re-push missing
	// and stale specs, bump+re-push same-revision content drift, and delete
	// edge instances NL no longer desires. When false (default) drift only
	// raises the per-gateway GATEWAY_CONNECTOR_DRIFT alarm.
	StrictReconcile bool
}

// deleteReplayBatchSize bounds one post-hello or periodic tombstone replay
// pass. Tombstones remain retained until an explicit delete-ACK contract; the
// never-attempted/least-recently-attempted ordering lets later passes rotate
// through a large backlog without loading or fanning it out all at once.
const deleteReplayBatchSize = 500

// DefaultTuning are the production ADR-0026 thresholds: 5 minutes pending,
// 1 hour failed escalation, 1 minute sweep.
func DefaultTuning() Tuning {
	return Tuning{
		PendingAfter:          5 * time.Minute,
		FailedEscalationAfter: time.Hour,
		SweepInterval:         time.Minute,
	}
}

// ConfigValidator validates connector config documents against the
// edge-registered schema for the tenant's type/version. ValidateConfig checks
// a complete document (the profile template); ValidateOverride checks a
// partial patch (a per-instance override merged over the template). Satisfied
// by *connectorcatalog.Service; nil disables schema validation (config then
// stays opaque, the pre-schema behaviour).
type ConfigValidator interface {
	ValidateConfig(ctx context.Context, tenantID, connectorType, version string, config json.RawMessage) error
	ValidateOverride(ctx context.Context, tenantID, connectorType, version string, override json.RawMessage) error
	// ResolveVersion performs NL-side unified plugin-version selection: an
	// explicit version is returned unchanged; an empty version is resolved to
	// the highest registered one ("" when the type has no versioned schema).
	ResolveVersion(ctx context.Context, tenantID, connectorType, version string) (string, error)
}

// ProtocolFamilyReader reports the declared protocol family for a connector
// type. Satisfied by *connectorcatalog.Service; nil disables family checks
// (fail-open). A blank family for an unregistered/undeclared type also fails
// open (ADR-0032 §6).
type ProtocolFamilyReader interface {
	Family(ctx context.Context, tenantID, connectorType string) (string, error)
}

// SecretResolver resolves secret:// references in connector config to
// plaintext at frame-build / hash time (migration 0047). Satisfied by
// *secrets.Store; nil leaves references verbatim. Plaintext is only ever put
// on the internal Core NATS frame, never persisted or returned over REST.
type SecretResolver interface {
	Resolve(ctx context.Context, tenantID string, names []string) (map[string]string, error)
}

// Service orchestrates the desired/reported lifecycle.
type Service struct {
	nc           *nats.Conn
	store        Store
	alarms       AlarmRaiser
	validator    ConfigValidator
	familyReader ProtocolFamilyReader
	secrets      SecretResolver
	tune         Tuning
	log          *slog.Logger
	// publishFunc is a test seam; production uses nc on the unchanged subject.
	publishFunc func(context.Context, PushFrame) error
}

// SetProtocolFamilyReader wires the connector catalog used for protocol-family
// compatibility checks between a profile and its connector type. When unset,
// family checks fail open (ADR-0032 §6).
func (s *Service) SetProtocolFamilyReader(r ProtocolFamilyReader) {
	s.familyReader = r
}

// SetSecretResolver wires secret:// resolution into dispatched config.
func (s *Service) SetSecretResolver(r SecretResolver) {
	s.secrets = r
}

// resolveSecrets substitutes secret:// references in the effective config for
// one dispatch/hash. A resolver or frame with no references is a no-op. A
// resolution error is logged and the frame keeps its references rather than
// dropping the push, so the edge can still surface the misconfiguration.
func (s *Service) resolveSecrets(ctx context.Context, sp Spec, raw json.RawMessage) json.RawMessage {
	if s.secrets == nil || len(raw) == 0 {
		return raw
	}
	names := secrets.FindReferences(raw)
	if len(names) == 0 {
		return raw
	}
	values, err := s.secrets.Resolve(ctx, sp.TenantID, names)
	if err != nil {
		s.log.Error("resolve connector secrets failed", "spec", sp.ID, "error", err)
		return raw
	}
	resolved, missing := secrets.ResolveDocument(raw, values)
	if len(missing) > 0 {
		s.log.Warn("connector config references unresolved secrets",
			"spec", sp.ID, "names", missing)
	}
	return resolved
}

// NewService constructs the connector service.
func NewService(nc *nats.Conn, store Store, alarms AlarmRaiser, tune Tuning, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	if tune.SweepInterval <= 0 {
		tune = DefaultTuning()
	}
	return &Service{nc: nc, store: store, alarms: alarms, tune: tune, log: log}
}

// SetConfigValidator wires edge-registered schema validation of config.
func (s *Service) SetConfigValidator(v ConfigValidator) {
	s.validator = v
}

// Admin returns the REST administration surface (see admin.go).

// prepare validates one desired write, verifies the sub-device binding and
// resolves the profile template, returning the connector type/version to// persist (the connector_type column is NOT NULL). The raw override
// config/mappings are stored unchanged and merged with the template only at
// dispatch, so a later profile edit flows on the next push.
func (s *Service) prepare(ctx context.Context, tenantID, gatewayID string, in SaveInput) (string, string, error) {
	if err := validateOverrideShape(in); err != nil {
		return "", "", err
	}
	if in.DeviceID != "" {
		ok, err := s.store.DeviceBelongsToGateway(ctx, tenantID, gatewayID, in.DeviceID)
		if err != nil {
			return "", "", err
		}
		if !ok {
			return "", "", fmt.Errorf("%w: device %s is not a sub-device of gateway %s",
				ErrInvalid, in.DeviceID, gatewayID)
		}
	}
	connectorType := in.ConnectorType
	typeVersion := in.TypeVersion
	var templateConfig json.RawMessage
	var profileFamily string
	if in.ProfileID != "" {
		tpls, err := s.store.FetchTemplates(ctx, tenantID, []string{in.ProfileID})
		if err != nil {
			return "", "", err
		}
		tpl, ok := tpls[in.ProfileID]
		if !ok {
			return "", "", fmt.Errorf("%w: profile %s not found", ErrInvalid, in.ProfileID)
		}
		if tpl.ConnectorType == "" {
			return "", "", fmt.Errorf("%w: profile %s has no connector template",
				ErrInvalid, in.ProfileID)
		}
		connectorType = resolveType(in.ConnectorType, tpl.ConnectorType)
		typeVersion = resolveType(in.TypeVersion, tpl.TypeVersion)
		templateConfig = tpl.Config
		profileFamily = tpl.Protocol
	}
	if connectorType == "" {
		return "", "", fmt.Errorf("%w: connector_type is required when no profile template is used",
			ErrInvalid)
	}
	// Protocol-family compatibility (ADR-0032 §6): only enforced when BOTH
	// the profile and the connector type declare a family; either side unknown
	// fails open so legacy/third-party edges are not blocked.
	if err := s.checkFamily(ctx, tenantID, connectorType, profileFamily); err != nil {
		return "", "", err
	}
	// Unified plugin-version selection (ADR-0026): a blank type_version is
	// resolved by NL to the highest version the tenant's gateways registered,
	// rather than trusting the edge or the operator. With no versioned schema
	// registered it stays "" (unversioned "any" schema / fail-open).
	if typeVersion == "" && s.validator != nil {
		resolved, err := s.validator.ResolveVersion(ctx, tenantID, connectorType, typeVersion)
		if err != nil {
			return "", "", err
		}
		typeVersion = resolved
	}
	// Edge-registered schema validation (best-effort: a type with no
	// registered schema passes). BOTH the profile template and the
	// per-instance override are partial documents validated against the
	// presence-relaxed schema: the template is reusable and intentionally
	// omits per-instance keys the console injects at create time
	// (e.g. EdgeX device_name), while the override is a deep-merge patch.
	// Type/shape constraints on the keys each document actually sets are
	// still enforced; completeness of the final merged document is checked
	// at the edge apply path (which reports FAILED with a precise reason).
	// A self-contained instance (no template) carries the full config and is
	// therefore validated against the full schema.
	if s.validator != nil {
		if in.ProfileID != "" {
			if err := s.validator.ValidateOverride(ctx, tenantID, connectorType, typeVersion, templateConfig); err != nil {
				return "", "", fmt.Errorf("%w: profile template config: %v", ErrInvalid, err)
			}
		}
		if len(in.Config) > 0 && string(in.Config) != "{}" {
			validate := s.validator.ValidateOverride
			if in.ProfileID == "" {
				// Without a template the instance config IS the full config.
				validate = s.validator.ValidateConfig
			}
			if err := validate(ctx, tenantID, connectorType, typeVersion, in.Config); err != nil {
				return "", "", fmt.Errorf("%w: instance config: %v", ErrInvalid, err)
			}
		}
	}
	return connectorType, typeVersion, nil
}

// checkFamily enforces that the resolved connector type belongs to the same
// open-vocabulary protocol family the profile declares. A blank family on
// either side disables the check (fail-open); a lookup error also fails open so
// a catalog hiccup cannot block connector creation.
func (s *Service) checkFamily(ctx context.Context, tenantID, connectorType, profileFamily string) error {
	if s.familyReader == nil || profileFamily == "" {
		return nil
	}
	connectorFamily, err := s.familyReader.Family(ctx, tenantID, connectorType)
	if err != nil {
		s.log.Warn("protocol family lookup failed; allowing connector",
			"connector_type", connectorType, "error", err)
		return nil
	}
	if connectorFamily == "" {
		return nil
	}
	if !strings.EqualFold(strings.TrimSpace(connectorFamily), strings.TrimSpace(profileFamily)) {
		return fmt.Errorf("%w: profile protocol %q does not match connector type protocol %q",
			ErrInvalid, profileFamily, connectorFamily)
	}
	return nil
}

// CreateSpec persists a new desired spec, pushes it and returns the row.
func (s *Service) CreateSpec(ctx context.Context, tenantID, gatewayID string, in SaveInput) (Spec, error) {
	if hc, err := normalizeHealthCheck(in.HealthCheck); err != nil {
		return Spec{}, err
	} else if hc != nil && in.DeviceID == "" {
		return Spec{}, fmt.Errorf("%w: health_check requires an instance bound to one sub-device", ErrInvalid)
	}
	ok, err := s.store.GatewayExists(ctx, tenantID, gatewayID)
	if err != nil {
		return Spec{}, err
	}
	if !ok {
		return Spec{}, ErrGatewayNotFound
	}
	connectorType, typeVersion, err := s.prepare(ctx, tenantID, gatewayID, in)
	if err != nil {
		return Spec{}, err
	}
	sp, err := s.store.Create(ctx, tenantID, gatewayID, in, connectorType, typeVersion)
	if err != nil {
		return Spec{}, err
	}
	s.pushSpec(ctx, sp)
	s.publishEvent(EventDesired, sp.TenantID, sp.GatewayID, sp.ID, "", sp.Revision)
	return sp, nil
}

// UpdateSpec overwrites desired, bumps revision and pushes.
func (s *Service) UpdateSpec(ctx context.Context, tenantID, specID string, in SaveInput) (Spec, error) {
	if hc, err := normalizeHealthCheck(in.HealthCheck); err != nil {
		return Spec{}, err
	} else if hc != nil && in.DeviceID == "" {
		return Spec{}, fmt.Errorf("%w: health_check requires an instance bound to one sub-device", ErrInvalid)
	}
	existing, err := s.store.Get(ctx, tenantID, specID)
	if err != nil {
		return Spec{}, err
	}
	connectorType, typeVersion, err := s.prepare(ctx, tenantID, existing.GatewayID, in)
	if err != nil {
		return Spec{}, err
	}
	sp, err := s.store.Update(ctx, tenantID, specID, in, connectorType, typeVersion)
	if err != nil {
		return Spec{}, err
	}
	s.pushSpec(ctx, sp)
	s.publishEvent(EventDesired, sp.TenantID, sp.GatewayID, sp.ID, "", sp.Revision)
	return sp, nil
}

// DeleteSpec commits desired removal and its durable tombstone before any
// downlink attempt. Transport success is not an acknowledgement: replay keeps
// the tombstone until a future explicit revision-matched delete-ack contract.
func (s *Service) DeleteSpec(ctx context.Context, tenantID, specID string) error {
	sp, err := s.store.Delete(ctx, tenantID, specID)
	if err != nil {
		return err
	}
	tombstone := DeleteTombstone{
		TenantID: sp.TenantID, GatewayID: sp.GatewayID, SpecID: sp.ID,
		DeviceID: sp.DeviceID, ConnectorType: sp.ConnectorType,
		TypeVersion: sp.TypeVersion, DeleteRevision: sp.Revision,
	}
	s.publishDelete(ctx, tombstone)
	s.clearAlarms(ctx, sp)
	s.publishEvent(EventDeleted, sp.TenantID, sp.GatewayID, sp.ID, "", sp.Revision)
	return nil
}

func (s *Service) publishDelete(ctx context.Context, tombstone DeleteTombstone) {
	var lastError string
	if err := s.publish(ctx, tombstone.Frame()); err != nil {
		lastError = err.Error()
	}
	if err := s.store.MarkDeleteAttempt(ctx, tombstone, time.Now().UTC(), lastError); err != nil {
		s.log.Warn("record connector delete attempt failed", "spec", tombstone.SpecID, "error", err)
	}
}

// ListSpecs returns a gateway's specs for REST.
func (s *Service) ListSpecs(ctx context.Context, tenantID, gatewayID string) ([]Spec, error) {
	return s.store.ListByGateway(ctx, tenantID, gatewayID)
}

// GetSpec returns one spec scoped by tenant.
func (s *Service) GetSpec(ctx context.Context, tenantID, specID string) (Spec, error) {
	return s.store.Get(ctx, tenantID, specID)
}

// JobMetrics exposes only actually reported optional stats. Zero is available;
// an absent/empty stats object is not. No events-per-second source exists yet.
func (s *Service) JobMetrics(ctx context.Context, tenantID, specID string) (JobMetrics, bool, error) {
	sp, err := s.store.Get(ctx, tenantID, specID)
	if err != nil {
		return JobMetrics{}, false, err
	}
	if sp.Stats == nil || (sp.Stats.SuccessTotal == nil && sp.Stats.ErrorTotal == nil &&
		sp.Stats.P50MS == nil && sp.Stats.P95MS == nil) {
		return JobMetrics{}, false, nil
	}
	return JobMetrics{
		SuccessTotal: sp.Stats.SuccessTotal, ErrorTotal: sp.Stats.ErrorTotal,
		P50MS: sp.Stats.P50MS, P95MS: sp.Stats.P95MS,
	}, true, nil
}

// pushSpec marks the spec dispatched best-effort, publishes the merged
// desired frame and clears any stale failed alarm for a fresh revision.
func (s *Service) pushSpec(ctx context.Context, sp Spec) {
	frame, err := s.frameFor(ctx, sp)
	if err != nil {
		s.log.Error("build connector frame failed", "spec", sp.ID, "error", err)
		return
	}
	now := time.Now().UTC()
	if err := s.store.MarkDispatched(ctx, sp.TenantID, sp.ID, now); err != nil {
		s.log.Warn("mark connector dispatched failed", "spec", sp.ID, "error", err)
	}
	_ = s.publish(ctx, frame) // Desired rows remain authoritative for post-hello redrive.
}

// frameFor resolves the effective spec (profile template merged under the
// instance overrides) into the downlink PushFrame.
func (s *Service) frameFor(ctx context.Context, sp Spec) (PushFrame, error) {
	if sp.ProfileID == "" {
		return s.frameForWith(sp, Template{})
	}
	tpls, err := s.store.FetchTemplates(ctx, sp.TenantID, []string{sp.ProfileID})
	if err != nil {
		return PushFrame{}, err
	}
	// A missing map entry (e.g. template stripped after binding) yields a
	// zero Template; frameForWith then sends the stored spec verbatim.
	return s.frameForWith(sp, tpls[sp.ProfileID])
}

// publish emits one downlink frame onto the per-gateway Core NATS subject.
func (s *Service) publish(ctx context.Context, f PushFrame) error {
	if s.publishFunc != nil {
		return s.publishFunc(ctx, f)
	}
	if s.nc == nil {
		return nil
	}
	data, err := json.Marshal(f)
	if err != nil {
		s.log.Error("marshal connector frame", "error", err)
		return err
	}
	subject := fmt.Sprintf("%s.%s.%s", SubjectDesiredPrefix, f.TenantID, f.GatewayID)
	if err := s.nc.Publish(subject, data); err != nil {
		s.log.Error("publish connector frame", "error", err, "subject", subject)
		return err
	}
	return nil
}

// publishEvent emits a best-effort user-plane lifecycle event that lets an
// open console refresh the gateway's connector view. A failure is logged and
// never affects the desired/status write path: the frame is a notification,
// not part of the state machine, and the console also re-reads on mount.
func (s *Service) publishEvent(kind, tenantID, gatewayID, specID, state string, revision int64) {
	if s.nc == nil {
		return
	}
	frame := EventFrame{
		Kind: kind, TenantID: tenantID, GatewayID: gatewayID,
		SpecID: specID, State: state, Revision: revision,
	}
	data, err := json.Marshal(frame)
	if err != nil {
		s.log.Error("marshal connector event", "error", err)
		return
	}
	subject := fmt.Sprintf("%s.%s", SubjectEventPrefix, tenantID)
	if err := s.nc.Publish(subject, data); err != nil {
		s.log.Error("publish connector event", "error", err, "subject", subject)
	}
}

// HandleStatus consumes one edge-reported status frame. APPLIED/DEGRADED
// clear the connector alarms; FAILED immediately opens the MAJOR failed
// alarm (idempotent via its dedupe key) for the 1-hour sweep to escalate.
func (s *Service) HandleStatus(ctx context.Context, tenantID string, f StatusFrame) (Spec, error) {
	if f.SpecID == "" {
		return Spec{}, fmt.Errorf("%w: spec_id is required", ErrInvalid)
	}
	sp, err := s.store.ApplyStatus(ctx, tenantID, f, time.Now().UTC())
	if err != nil {
		return Spec{}, err
	}
	if f.Revision != sp.Revision {
		// ApplyStatus returns the current desired row for a revision-fenced
		// no-op. Delayed/old/future reports must not mutate alarms or events.
		return sp, nil
	}
	// This check fences old and unknown-future status frames, but the external
	// alarm calls below are not transactional with ApplyStatus. A desired
	// revision committed after that UPDATE still needs an alarm revision
	// predicate/outbox for a fully linearizable fence (see L-144).
	switch f.State {
	case StateApplied:
		s.clearAlarms(ctx, sp)
	case StateDegraded:
		// DEGRADED is its own binding-plane event (ADR-0039: e.g. an
		// edge-inference binding reporting idle-no-inference). Clear the
		// pending/failed alarms and open the generic degraded WARNING.
		s.clearKey(ctx, sp, DedupePendingKey(sp.ID), DedupeFailedKey(sp.ID))
		s.raiseDegraded(ctx, sp)
	}
	s.publishEvent(EventStatus, sp.TenantID, sp.GatewayID, sp.ID, f.State, sp.Revision)
	// FAILED does not raise inline: the periodic sweep claims newly failed
	// specs and opens the MAJOR alarm, which keeps raising durable across a
	// crash between the report and the alarm write and gives the 1-hour
	// MAJOR→CRITICAL escalation a single, restart-safe owner.
	return sp, nil
}

func (s *Service) clearAlarms(ctx context.Context, sp Spec) {
	if s.alarms == nil {
		return
	}
	s.clearKey(ctx, sp,
		DedupePendingKey(sp.ID), DedupeFailedKey(sp.ID), DedupeDegradedKey(sp.ID))
}

// clearKey clears the given dedupe keys for a spec, logging per-key failures.
func (s *Service) clearKey(ctx context.Context, sp Spec, keys ...string) {
	if s.alarms == nil {
		return
	}
	for _, key := range keys {
		if _, err := s.alarms.ClearAlarm(ctx, sp.TenantID, sp.GatewayID, key); err != nil {
			s.log.Error("clear connector alarm failed", "spec", sp.ID, "error", err)
		}
	}
}

// raiseDegraded opens the generic per-spec DEGRADED WARNING carrying the
// edge-reported reason verbatim. It is generic and fires for every connector
// type, not only edge-inference.
func (s *Service) raiseDegraded(ctx context.Context, sp Spec) {
	if s.alarms == nil {
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  sp.TenantID,
		DeviceID:  sp.GatewayID,
		AlarmType: AlarmTypeDegraded,
		Severity:  alarm.SeverityWarning,
		DedupeKey: DedupeDegradedKey(sp.ID),
		Details: map[string]any{
			"spec_id":        sp.ID,
			"connector_type": sp.ConnectorType,
			"revision":       sp.Revision,
			"reason":         sp.ReportReason,
		},
	}); err != nil {
		s.log.Error("raise connector degraded alarm failed", "spec", sp.ID, "error", err)
	}
}

// raiseFailed opens (or escalates) the failed connector alarm. escalated
// selects CRITICAL for the 1-hour sweep; the first report is MAJOR.
func (s *Service) raiseFailed(ctx context.Context, sp Spec, escalated bool) {
	if s.alarms == nil {
		return
	}
	// A DEGRADED→FAILED transition must not leave the degraded alarm open
	// alongside the failed one; ClearAlarm is a no-op if it was not open.
	if _, err := s.alarms.ClearAlarm(ctx, sp.TenantID, sp.GatewayID, DedupeDegradedKey(sp.ID)); err != nil {
		s.log.Error("clear connector degraded alarm on failure failed", "spec", sp.ID, "error", err)
	}
	severity := alarm.SeverityMajor
	if escalated {
		severity = alarm.SeverityCritical
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  sp.TenantID,
		DeviceID:  sp.GatewayID,
		AlarmType: AlarmTypeFailed,
		Severity:  severity,
		DedupeKey: DedupeFailedKey(sp.ID),
		Details: map[string]any{
			"spec_id":        sp.ID,
			"connector_type": sp.ConnectorType,
			"revision":       sp.Revision,
			"reason":         sp.ReportReason,
		},
	}); err != nil {
		s.log.Error("raise connector failed alarm failed", "spec", sp.ID, "error", err)
	}
}

// raisePending opens the WARNING not-applied alarm for a stuck spec.
func (s *Service) raisePending(ctx context.Context, sp Spec) {
	if s.alarms == nil {
		return
	}
	if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  sp.TenantID,
		DeviceID:  sp.GatewayID,
		AlarmType: AlarmTypePending,
		Severity:  alarm.SeverityWarning,
		DedupeKey: DedupePendingKey(sp.ID),
		Details: map[string]any{
			"spec_id":        sp.ID,
			"connector_type": sp.ConnectorType,
			"revision":       sp.Revision,
		},
	}); err != nil {
		s.log.Error("raise connector pending alarm failed", "spec", sp.ID, "error", err)
	}
}

// Redrive pushes the full desired spec set for one gateway. It is invoked by
// the MQTT ingress immediately after a gateway completes hello, at which
// point the edge is subscribed to the downlink filter. This is the ADR-0026
// reconnect guarantee: desired never disappears and delivery is retried
// idempotently without per-gateway timers in the kernel.
func (s *Service) Redrive(ctx context.Context, tenantID, gatewayID string) {
	s.redriveDeletes(ctx, gatewayID)
	specs, err := s.store.ListAllForGateway(ctx, gatewayID)
	if err != nil {
		s.log.Error("redrive list connector specs failed", "gateway", gatewayID, "error", err)
		return
	}
	// Batch-fetch every referenced profile template in one query so a
	// template edit reaches the edge on reconnect without N lookups.
	templates := map[string]Template{}
	byTenant := map[string][]string{}
	for i := range specs {
		if specs[i].ProfileID != "" {
			byTenant[specs[i].TenantID] = append(byTenant[specs[i].TenantID], specs[i].ProfileID)
		}
	}
	for t, ids := range byTenant {
		found, err := s.store.FetchTemplates(ctx, t, ids)
		if err != nil {
			s.log.Error("redrive fetch connector templates failed", "error", err)
			continue
		}
		for k, v := range found {
			templates[k] = v
		}
	}
	for i := range specs {
		sp := specs[i]
		frame, err := s.frameForWith(sp, templates[sp.ProfileID])
		if err != nil {
			s.log.Error("redrive build connector frame failed", "spec", sp.ID, "error", err)
			continue
		}
		now := time.Now().UTC()
		if err := s.store.MarkDispatched(ctx, sp.TenantID, sp.ID, now); err != nil {
			s.log.Warn("mark connector dispatched failed", "spec", sp.ID, "error", err)
		}
		_ = s.publish(ctx, frame) // Desired rows remain authoritative for post-hello redrive.
	}
}

// redriveDeletes replays one bounded, fair batch of unacknowledged explicit
// deletes: never-attempted tombstones first, then the least recently attempted.
// An empty gateway is the periodic all-gateway sweep;
// post-hello passes one gateway. Retention is unchanged: no transport result
// retires a tombstone without the future explicit delete-ACK contract.
func (s *Service) redriveDeletes(ctx context.Context, gatewayID string) {
	tombstones, err := s.store.ListDeleteTombstones(ctx, gatewayID, deleteReplayBatchSize)
	if err != nil {
		s.log.Error("list connector delete tombstones failed", "gateway", gatewayID, "error", err)
		return
	}
	for _, tombstone := range tombstones {
		s.publishDelete(ctx, tombstone)
	}
}

// frameForWith builds the downlink frame using an already-resolved template
// (zero value for an unbound or missing template), sharing the merge between
// single-push and the batched redrive path.
func (s *Service) frameForWith(sp Spec, tpl Template) (PushFrame, error) {
	var ref *Template
	if sp.ProfileID != "" && tpl.ProfileID != "" {
		copy := tpl
		ref = &copy
	}
	effective, err := effectiveSpec(sp, ref)
	if err != nil {
		return PushFrame{}, err
	}
	var health *HealthCheck
	if effective.HealthCheck.Enabled() {
		hc := effective.HealthCheck
		health = &hc
	}
	return PushFrame{
		Kind:          KindSpec,
		TenantID:      effective.TenantID,
		GatewayID:     effective.GatewayID,
		SpecID:        effective.ID,
		DeviceID:      effective.DeviceID,
		ConnectorType: effective.ConnectorType,
		TypeVersion:   effective.TypeVersion,
		Config:        effective.Config,
		Mappings:      effective.Mappings,
		HealthCheck:   health,
		Revision:      effective.Revision,
	}, nil
}

// Run runs the pending/failed alarm sweeps until ctx is canceled. Desired
// re-send on reconnect is driven by the ingress post-hello Redrive call, not
// a presence subscription, so the edge's downlink subscription always
// exists before the frames are written.
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

// sweep raises the pending alarm for stale open specs and escalates long
// FAILED specs exactly once (the claim UPDATEs are atomic across replicas).
func (s *Service) sweep(ctx context.Context, now time.Time) {
	s.redriveDeletes(ctx, "")
	pending, err := s.store.ClaimPendingForAlarm(ctx, now, now.Add(-s.tune.PendingAfter))
	if err != nil {
		s.log.Error("claim pending connector specs failed", "error", err)
	} else {
		for i := range pending {
			s.raisePending(ctx, pending[i])
		}
	}

	newFailed, err := s.store.ClaimFailedForRaise(ctx, now)
	if err != nil {
		s.log.Error("claim new failed connector specs failed", "error", err)
	} else {
		for i := range newFailed {
			s.raiseFailed(ctx, newFailed[i], false)
		}
	}

	due, err := s.store.ClaimFailedForEscalation(ctx, now, now.Add(-s.tune.FailedEscalationAfter))
	if err != nil {
		s.log.Error("claim failed-escalation connector specs failed", "error", err)
		return
	}
	for i := range due {
		s.raiseFailed(ctx, due[i], true)
	}
}
