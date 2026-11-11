// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// inbox.go implements in-app notification persistence, real-time Core NATS
// publication and inbox queries (ADR-0019/ADR-0030). One notification is
// recorded per alarm event independently of external delivery channels.

package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/apipage"
)

// SubjectPrefix carries in-app notifications per tenant.
const SubjectPrefix = "nl.notification"

// Notification states.
const (
	// StateNew is an unread notification.
	StateNew = "NEW"

	// StateRead is a read notification.
	StateRead = "READ"
)

// Inbox persists and publishes in-app notifications.
type Inbox struct {
	pool *pgxpool.Pool
	nc   *nats.Conn
}

// NewInbox constructs the notification inbox.
func NewInbox(pool *pgxpool.Pool, nc *nats.Conn) *Inbox {
	return &Inbox{pool: pool, nc: nc}
}

// Notification is one in-app notification row.
type Notification struct {
	// ID is the notification identifier.
	ID string `json:"notification_id"`

	// TenantID owns the notification.
	TenantID string `json:"tenant_id"`

	// AlarmID links the originating alarm when present.
	AlarmID string `json:"alarm_id,omitempty"`

	// DeviceID links the originating device when present.
	DeviceID string `json:"device_id,omitempty"`

	// Severity mirrors the alarm severity.
	Severity string `json:"severity"`

	// Title is the short notification title.
	Title string `json:"title"`

	// Body is the optional longer text.
	Body string `json:"body,omitempty"`

	// State is NEW or READ.
	State string `json:"state"`

	// CreatedAt is the recording time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// ReadAt is the read time as RFC3339 UTC when present.
	ReadAt string `json:"read_at,omitempty"`
}

// RecordInput carries the fields needed to record one notification.
type RecordInput struct {
	// TenantID owns the notification.
	TenantID string

	// AlarmID links the originating alarm.
	AlarmID string

	// DeviceID links the originating device.
	DeviceID string

	// Severity mirrors the alarm severity.
	Severity string

	// Title is the rendered title.
	Title string

	// Body is the rendered body.
	Body string
}

// Record inserts one notification, publishes it to the per-tenant subject
// and returns the stored row. Insert and publish are not transactional:
// inbox reads back from PostgreSQL while WS push is best-effort, matching
// the alarm publication model.
func (i *Inbox) Record(ctx context.Context, input RecordInput) (Notification, error) {
	now := time.Now().UTC()
	notification := Notification{
		ID:        uuid.NewString(),
		TenantID:  input.TenantID,
		AlarmID:   input.AlarmID,
		DeviceID:  input.DeviceID,
		Severity:  input.Severity,
		Title:     input.Title,
		Body:      input.Body,
		State:     StateNew,
		CreatedAt: now.Format(time.RFC3339),
	}

	if _, err := i.pool.Exec(ctx,
		`INSERT INTO notification (id, tenant_id, alarm_id, device_id,
		    severity, title, body, state, created_at)
		 VALUES ($1,$2,NULLIF($3,'')::uuid,NULLIF($4,'')::uuid,
		         $5,$6,$7,$8,$9)`,
		notification.ID, notification.TenantID, notification.AlarmID,
		notification.DeviceID, notification.Severity, notification.Title,
		notification.Body, notification.State, now); err != nil {
		return Notification{}, fmt.Errorf("record notification: %w", err)
	}

	subject := SubjectPrefix + "." + input.TenantID
	_ = i.nc.Publish(subject, mustMarshal(notification))
	return notification, nil
}

// List returns one page of notifications of a tenant, optionally restricted
// to one state, newest first.
func (i *Inbox) List(ctx context.Context, tenantID, state string, page, pageSize int) (apipage.Page[Notification], error) {
	var total int
	if err := i.pool.QueryRow(ctx,
		`SELECT count(*) FROM notification
		 WHERE tenant_id = $1 AND ($2 = '' OR state = $2)`,
		tenantID, state).Scan(&total); err != nil {
		return apipage.Page[Notification]{}, fmt.Errorf("count notifications: %w", err)
	}

	rows, err := i.pool.Query(ctx,
		`SELECT id::text, tenant_id::text, alarm_id::text, device_id::text,
		        severity, title, body, state, created_at, read_at
		 FROM notification
		 WHERE tenant_id = $1 AND ($2 = '' OR state = $2)
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		tenantID, state, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[Notification]{}, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()

	notifications := []Notification{}
	for rows.Next() {
		var notification Notification
		var alarmID, deviceID *string
		var createdAt time.Time
		var readAt *time.Time
		if err := rows.Scan(&notification.ID, &notification.TenantID, &alarmID,
			&deviceID, &notification.Severity, &notification.Title,
			&notification.Body, &notification.State, &createdAt, &readAt); err != nil {
			return apipage.Page[Notification]{}, fmt.Errorf("scan notification: %w", err)
		}
		if alarmID != nil {
			notification.AlarmID = *alarmID
		}
		if deviceID != nil {
			notification.DeviceID = *deviceID
		}
		notification.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if readAt != nil {
			notification.ReadAt = readAt.UTC().Format(time.RFC3339)
		}
		notifications = append(notifications, notification)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[Notification]{}, err
	}
	return apipage.New(notifications, total, page, pageSize), nil
}

// MarkRead marks the given tenant-owned notifications as read. Rows already
// read keep their original read_at timestamp.
func (i *Inbox) MarkRead(ctx context.Context, tenantID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	if _, err := i.pool.Exec(ctx,
		`UPDATE notification
		 SET state = $1, read_at = COALESCE(read_at, now())
		 WHERE tenant_id = $2 AND id = ANY($3)`,
		StateRead, tenantID, ids); err != nil {
		return fmt.Errorf("mark notifications read: %w", err)
	}
	return nil
}

// mustMarshal encodes a notification for NATS publication.
func mustMarshal(notification Notification) []byte {
	raw, err := json.Marshal(notification)
	if err != nil {
		return []byte("{}")
	}
	return raw
}
