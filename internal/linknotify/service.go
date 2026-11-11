// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go consumes link-health advisories, raises/clears alarms and runs
// the escalation and digest loops.

package linknotify

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/alarm"
)

// AlarmRaiser opens and clears the deduplicated outage alarm. It is
// satisfied by *alarm.Service.
type AlarmRaiser interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// DigestSink delivers the daily summary: an in-app inbox row plus the
// configured external channels. Implemented by the cmd adapter over
// internal/notify.
type DigestSink interface {
	// RecordInbox records one tenant-scoped in-app notification.
	RecordInbox(ctx context.Context, tenantID, deviceID, severity, title, body string) error
	// Deliver pushes the summary through webhook/email to admin recipients.
	Deliver(ctx context.Context, subject, body, severity string, recipients []string) error
}

// EpisodeStore is the persistence surface the worker needs. It is satisfied
// by *Store and narrowed here so the advisory/escalation logic is testable
// without a database.
type EpisodeStore interface {
	OpenEpisode(ctx context.Context, ep Episode) error
	CloseEpisode(ctx context.Context, gatewayID string, at time.Time) error
	ClaimDueEpisodes(ctx context.Context, now, cutoff time.Time) ([]Episode, error)
	TenantsWithEndedEpisodes(ctx context.Context, day time.Time) ([]string, error)
	DigestRows(ctx context.Context, tenantID string, day time.Time) ([]DigestRow, error)
	MarkDigestSent(ctx context.Context, tenantID string, day, at time.Time) (bool, error)
	AdminEmails(ctx context.Context, tenantID string) ([]string, error)
}

// Service wires advisories to alarms and durable escalation.
type Service struct {
	nc     *nats.Conn
	store  EpisodeStore
	alarms AlarmRaiser
	sink   DigestSink
	tune   Tuning
	log    *slog.Logger
}

// NewService constructs the link-notification service.
func NewService(nc *nats.Conn, store EpisodeStore, alarms AlarmRaiser, sink DigestSink,
	tune Tuning, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		nc:     nc,
		store:  store,
		alarms: alarms,
		sink:   sink,
		tune:   tune,
		log:    log,
	}
}

// Run subscribes to advisories and runs the escalation and digest loops
// until ctx is canceled.
func (s *Service) Run(ctx context.Context) error {
	sub, err := s.nc.QueueSubscribe(SubjectWildcard, QueueGroup,
		func(msg *nats.Msg) { s.handle(ctx, msg.Data) })
	if err != nil {
		return fmt.Errorf("subscribe link advisories: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	escalate := time.NewTicker(s.tune.EscalationSweepInterval)
	defer escalate.Stop()
	digest := time.NewTicker(s.tune.DigestSweepInterval)
	defer digest.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-escalate.C:
			s.escalationSweep(ctx, time.Now().UTC())
		case <-digest.C:
			s.digestTick(ctx, time.Now().UTC())
		}
	}
}

// handle processes one link-health advisory.
func (s *Service) handle(ctx context.Context, raw []byte) {
	var adv advisory
	if err := json.Unmarshal(raw, &adv); err != nil {
		s.log.Warn("bad link-health advisory", "error", err)
		return
	}
	switch {
	case adv.Kind == kindPlanned:
		// Planned maintenance never alarms and never opens an episode.
		return
	case adv.Kind == kindFlapSettled && adv.Source == sourceEdge:
		s.settleEdgeFlap(ctx, adv)
	case adv.Kind == kindFlapping && adv.Source == sourceEdge:
		s.edgeFlapDown(ctx, adv)
	case adv.Kind == kindRecovered:
		s.recover(ctx, adv)
	case adv.isDown():
		s.down(ctx, adv)
	}
	// SUSPECT advisories are observational (no alarm until DOWN).
}

// edgeFlapDown raises the deduplicated MAJOR alarm for an edge-detected
// micro-flap burst. No episode is opened: the individual edge episodes are
// already recorded as closed rows. settleEdgeFlap clears the alarm once the
// burst ages out.
func (s *Service) edgeFlapDown(ctx context.Context, adv advisory) {
	_, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  adv.TenantID,
		DeviceID:  adv.GatewayID,
		AlarmType: EdgeFlapAlarmType,
		Severity:  InitialSeverity,
		DedupeKey: edgeFlapDedupeKey(adv.GatewayID),
		Details: map[string]any{
			"flapping": true,
			"source":   sourceEdge,
		},
	})
	if err != nil {
		s.log.Error("raise edge flap alarm failed",
			"gateway", adv.GatewayID, "error", err)
	}
}

// settleEdgeFlap clears the edge flap alarm after the burst has been quiet
// for a full flap window. It never closes an episode: edge episodes are
// already closed and a lease episode for the gateway must stay untouched.
func (s *Service) settleEdgeFlap(ctx context.Context, adv advisory) {
	if _, err := s.alarms.ClearAlarm(ctx,
		adv.TenantID, adv.GatewayID, edgeFlapDedupeKey(adv.GatewayID)); err != nil {
		s.log.Error("clear edge flap alarm failed",
			"gateway", adv.GatewayID, "error", err)
	}
}

// down opens the deduplicated MAJOR alarm and records the episode.
func (s *Service) down(ctx context.Context, adv advisory) {
	key := dedupeKey(adv.GatewayID)
	details := map[string]any{
		"link_reason": adv.Reason,
		"from":        adv.From,
		"to":          adv.To,
	}
	if adv.Kind == kindFlapping {
		details["flapping"] = true
	}
	outcome, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
		TenantID:  adv.TenantID,
		DeviceID:  adv.GatewayID,
		AlarmType: AlarmType,
		Severity:  InitialSeverity,
		DedupeKey: key,
		Details:   details,
	})
	if err != nil {
		s.log.Error("raise gateway link alarm failed",
			"gateway", adv.GatewayID, "error", err)
		return
	}
	startedAt := adv.Time
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}
	if err := s.store.OpenEpisode(ctx, Episode{
		TenantID:  adv.TenantID,
		GatewayID: adv.GatewayID,
		StartedAt: startedAt,
		Reason:    adv.Reason,
		AlarmID:   outcome.AlarmID,
	}); err != nil {
		s.log.Error("open link episode failed", "gateway", adv.GatewayID, "error", err)
	}
}

// recover clears the alarm and closes the episode.
func (s *Service) recover(ctx context.Context, adv advisory) {
	if _, err := s.alarms.ClearAlarm(ctx,
		adv.TenantID, adv.GatewayID, dedupeKey(adv.GatewayID)); err != nil {
		s.log.Error("clear gateway link alarm failed",
			"gateway", adv.GatewayID, "error", err)
	}
	at := adv.Time
	if at.IsZero() {
		at = time.Now().UTC()
	}
	if err := s.store.CloseEpisode(ctx, adv.GatewayID, at); err != nil {
		s.log.Error("close link episode failed", "gateway", adv.GatewayID, "error", err)
	}
}

// escalationSweep bumps long-open MAJOR episodes to CRITICAL exactly once.
func (s *Service) escalationSweep(ctx context.Context, now time.Time) {
	cutoff := now.Add(-s.tune.EscalationAfter)
	due, err := s.store.ClaimDueEpisodes(ctx, now, cutoff)
	if err != nil {
		s.log.Error("claim due link episodes failed", "error", err)
		return
	}
	for _, ep := range due {
		if _, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  ep.TenantID,
			DeviceID:  ep.GatewayID,
			AlarmType: AlarmType,
			Severity:  EscalatedSeverity,
			DedupeKey: dedupeKey(ep.GatewayID),
			Details: map[string]any{
				"escalated_after_minutes": int(s.tune.EscalationAfter.Minutes()),
			},
		}); err != nil {
			s.log.Error("escalate gateway link alarm failed",
				"gateway", ep.GatewayID, "error", err)
		}
	}
}

// digestTick sends the previous-UTC-day summary once, at the configured
// hour, for every affected tenant.
func (s *Service) digestTick(ctx context.Context, now time.Time) {
	if !s.tune.DigestEnabled || now.Hour() != s.tune.DigestHourUTC || s.sink == nil {
		return
	}
	yesterday := now.AddDate(0, 0, -1)
	tenants, err := s.store.TenantsWithEndedEpisodes(ctx, yesterday)
	if err != nil {
		s.log.Error("list digest tenants failed", "error", err)
		return
	}
	for _, tenantID := range tenants {
		s.sendDigest(ctx, tenantID, yesterday, now)
	}
}

// sendDigest composes and delivers one tenant digest.
func (s *Service) sendDigest(ctx context.Context, tenantID string, day, now time.Time) {
	rows, err := s.store.DigestRows(ctx, tenantID, day)
	if err != nil {
		s.log.Error("load digest rows failed", "tenant", tenantID, "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	ok, err := s.store.MarkDigestSent(ctx, tenantID, day, now)
	if err != nil {
		s.log.Error("mark digest sent failed", "tenant", tenantID, "error", err)
		return
	}
	if !ok {
		return
	}

	title := fmt.Sprintf("Gateway link digest %s", day.Format("2006-01-02"))
	var b strings.Builder
	fmt.Fprintf(&b, "Gateway link outages for %s UTC:\n", day.Format("2006-01-02"))
	for _, r := range rows {
		fmt.Fprintf(&b, "- %s: %d outage(s), total downtime %s\n",
			r.GatewayName, r.Count, formatDur(r.TotalDowntime))
	}
	body := b.String()

	if err := s.sink.RecordInbox(ctx, tenantID, "", alarm.SeverityMinor, title, body); err != nil {
		s.log.Error("record digest inbox failed", "tenant", tenantID, "error", err)
	}
	emails, err := s.store.AdminEmails(ctx, tenantID)
	if err != nil {
		s.log.Error("resolve digest recipients failed", "tenant", tenantID, "error", err)
		return
	}
	if err := s.sink.Deliver(ctx, title, body, alarm.SeverityMinor, emails); err != nil {
		s.log.Error("deliver digest failed", "tenant", tenantID, "error", err)
	}
}

// formatDur renders a compact human duration for the digest body.
func formatDur(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
