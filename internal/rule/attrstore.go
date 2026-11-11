// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attrstore.go implements the AttributeSaver side-effect against the
// device_attribute table.

package rule

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/attributeevent"
)

// AttributeEventPublisher fans a committed device/rule attribute change to
// the console-plane WebSocket router. It is optional; a nil store publisher
// keeps SaveAttributes persistence-only (rule package stays free of a NATS
// dependency, exactly like registry's publisher hook).
type AttributeEventPublisher interface {
	PublishAttributeEvent(frame attributeevent.Event)
}

// AttrStore persists device attributes.
type AttrStore struct {
	pool      *pgxpool.Pool
	publisher AttributeEventPublisher
}

// NewAttrStore constructs the attribute store.
func NewAttrStore(pool *pgxpool.Pool) *AttrStore {
	return &AttrStore{pool: pool}
}

// SetAttributeEventPublisher wires the console-plane change notification
// fired after each committed device/rule attribute upsert.
func (s *AttrStore) SetAttributeEventPublisher(publisher AttributeEventPublisher) {
	s.publisher = publisher
}

// SaveAttributes upserts every attribute for one device scope. Each
// successful upsert emits one best-effort attribute event after commit so
// operator UIs see device-reported (CLIENT) and rule-produced changes live.
func (s *AttrStore) SaveAttributes(ctx context.Context, tenantID, deviceID, scope string, attrs map[string]any) error {
	for key, value := range attrs {
		raw := toJSON(value)
		var updatedAt time.Time
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO device_attribute
			   (tenant_id, device_id, scope, key, value, updated_at)
			 VALUES ($1,$2,$3,$4,$5::jsonb, now())
			 ON CONFLICT (tenant_id, device_id, scope, key)
			 DO UPDATE SET value = EXCLUDED.value, updated_at = now()
			 RETURNING updated_at`,
			tenantID, deviceID, scope, key, raw).Scan(&updatedAt); err != nil {
			return fmt.Errorf("save attribute %s: %w", key, err)
		}
		if s.publisher != nil {
			s.publisher.PublishAttributeEvent(attributeevent.Event{
				TenantID:  tenantID,
				DeviceID:  deviceID,
				Scope:     scope,
				Key:       key,
				Value:     json.RawMessage(raw),
				UpdatedAt: updatedAt.UTC(),
				Source:    attributeevent.SourceDevice,
			})
		}
	}
	return nil
}

// toJSON encodes an attribute value into JSON text.
func toJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}
