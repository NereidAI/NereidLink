// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// linknotify_wiring.go bridges the link-notification worker to the notify
// package (inbox + configured webhook/SMTP) and maps its config.

package main

import (
	"context"
	"errors"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/linknotify"
	"github.com/nereidai/nereidlink/internal/notify"
)

// linkDigestSink delivers the daily gateway link digest via the in-app
// inbox and the platform-configured external channels.
type linkDigestSink struct {
	inbox *notify.Inbox
	cfg   notify.Config
}

// RecordInbox stores one tenant-scoped in-app digest notification.
func (s linkDigestSink) RecordInbox(ctx context.Context, tenantID, _, severity, title, body string) error {
	_, err := s.inbox.Record(ctx, notify.RecordInput{
		TenantID: tenantID,
		Severity: severity,
		Title:    title,
		Body:     body,
	})
	return err
}

// Deliver posts the digest to the configured webhook and emails the
// resolved tenant administrators when SMTP is enabled.
func (s linkDigestSink) Deliver(ctx context.Context, subject, body, severity string, recipients []string) error {
	message := notify.Message{
		Subject:  subject,
		Body:     body,
		Severity: severity,
	}
	var failures []error
	if s.cfg.WebhookURL != "" {
		if err := notify.DeliverWebhook(ctx, s.cfg.WebhookURL, message); err != nil {
			failures = append(failures, err)
		}
	}
	if s.cfg.SMTP.Enabled && len(recipients) > 0 {
		if err := notify.DeliverEmail(s.cfg.SMTP, recipients, message); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// linkNotifyTuning maps declarative configuration, leaving empty fields at
// the ADR defaults (escalate after 30m, digest at 08:00 UTC).
func linkNotifyTuning(cfg *config.Config) linknotify.Tuning {
	tune := linknotify.DefaultTuning()
	if d, ok := optionalDuration(cfg.LinkNotify.EscalationAfter); ok && d > 0 {
		tune.EscalationAfter = d
	}
	if d, ok := optionalDuration(cfg.LinkNotify.EscalationSweepInterval); ok && d > 0 {
		tune.EscalationSweepInterval = d
	}
	if d, ok := optionalDuration(cfg.LinkNotify.DigestSweepInterval); ok && d > 0 {
		tune.DigestSweepInterval = d
	}
	if cfg.LinkNotify.DigestEnabled != nil {
		tune.DigestEnabled = *cfg.LinkNotify.DigestEnabled
	}
	if h := cfg.LinkNotify.DigestHourUTC; h > 0 && h < 24 {
		tune.DigestHourUTC = h
	}
	return tune
}
