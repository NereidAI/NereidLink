// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// escalation_adapters.go bridges the escalation package to the notify
// package: external channel delivery uses the configured webhook/SMTP
// settings, and fired steps are also recorded in the notify inbox.

package main

import (
	"context"
	"errors"

	"github.com/nereidai/nereidlink/internal/escalation"
	"github.com/nereidai/nereidlink/internal/notify"
)

// escalationAdminAdapter maps the gatewayapi EscalationAdmin interface
// (Create/Get verbs) onto the escalation service method names.
type escalationAdminAdapter struct {
	svc *escalation.Service
}

// List returns the chains of a tenant.
func (a escalationAdminAdapter) List(ctx context.Context, tenantID string) ([]escalation.Chain, error) {
	return a.svc.ListChains(ctx, tenantID)
}

// Get returns one chain.
func (a escalationAdminAdapter) Get(ctx context.Context, tenantID, id string) (escalation.Chain, error) {
	return a.svc.GetChain(ctx, tenantID, id)
}

// Create persists a new chain.
func (a escalationAdminAdapter) Create(ctx context.Context, tenantID string, in escalation.SaveInput) (escalation.Chain, error) {
	return a.svc.CreateChain(ctx, tenantID, in)
}

// Update replaces a chain.
func (a escalationAdminAdapter) Update(ctx context.Context, tenantID, id string, in escalation.SaveInput) (escalation.Chain, error) {
	return a.svc.UpdateChain(ctx, tenantID, id, in)
}

// Delete removes a chain.
func (a escalationAdminAdapter) Delete(ctx context.Context, tenantID, id string) error {
	return a.svc.DeleteChain(ctx, tenantID, id)
}

// escalationSender delivers fired escalations through the configured
// external channels.
type escalationSender struct {
	cfg notify.Config
}

// Send posts the webhook message when configured and emails the resolved
// role recipients when SMTP is enabled.
func (s escalationSender) Send(ctx context.Context, in escalation.SendInput) error {
	message := notify.Message{
		Subject:  in.Subject,
		Body:     in.Body,
		Severity: in.Severity,
	}

	var failures []error
	if s.cfg.WebhookURL != "" {
		if err := notify.DeliverWebhook(ctx, s.cfg.WebhookURL, message); err != nil {
			failures = append(failures, err)
		}
	}
	if s.cfg.SMTP.Enabled && len(in.Recipients) > 0 {
		if err := notify.DeliverEmail(s.cfg.SMTP, in.Recipients, message); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// escalationInbox records fired escalations as in-app notifications.
type escalationInbox struct {
	inbox *notify.Inbox
}

// Record maps the escalation record into a notify inbox record.
func (i escalationInbox) Record(ctx context.Context, in escalation.InboxRecord) error {
	_, err := i.inbox.Record(ctx, notify.RecordInput{
		TenantID: in.TenantID,
		AlarmID:  in.AlarmID,
		DeviceID: in.DeviceID,
		Severity: in.Severity,
		Title:    in.Title,
		Body:     in.Body,
	})
	return err
}
