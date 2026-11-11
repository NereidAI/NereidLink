// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes.go implements device shadow attribute access (ADR-0011):
// listing attributes by scope and upserting platform-writable scopes.

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nereidai/nereidlink/internal/attributeevent"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// AttributePublisher fans a server/shared attribute change out to the
// ingress owning a live device session (Core NATS). It is optional: a nil
// publisher keeps PutAttribute persistence-only, which keeps registry unit
// tests and environments without a bus working. Delivery is best-effort and
// must not surface an error, because the row is already committed when the
// fan-out runs; an implementation logs its own publish failures.
type AttributePublisher interface {
	PublishAttributePush(frame mqttapi.AttributePushFrame)
}

// AttributeEventPublisher fans every committed attribute change to the
// console-plane WebSocket router (nl.attribute.event). It is optional and
// best-effort for the same reasons as AttributePublisher.
type AttributeEventPublisher interface {
	PublishAttributeEvent(frame attributeevent.Event)
}

// Attribute scopes accepted on the write path. CLIENT scope is owned by
// devices through rule nodes, so management writes must not overwrite it.
const (
	// ScopeServer is platform-writable server scope.
	ScopeServer = "SERVER_SCOPE"

	// ScopeShared is platform-writable scope shared back to devices.
	ScopeShared = "SHARED_SCOPE"
)

// AttributeDTO is one shadow key/value pair.
type AttributeDTO struct {
	// Scope is the shadow scope.
	Scope string `json:"scope"`

	// Key is the attribute key.
	Key string `json:"key"`

	// Value is the raw JSON value.
	Value json.RawMessage `json:"value"`

	// UpdatedAt is the last change time as RFC3339 UTC.
	UpdatedAt string `json:"updated_at"`
}

// ListAttributes returns attributes of a device, optionally restricted to
// one scope.
func (a *Admin) ListAttributes(ctx context.Context, tenantID, deviceID, scope string) ([]AttributeDTO, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return nil, err
	}

	rows, err := a.pool.Query(ctx,
		`SELECT scope, key, value::text, updated_at
		 FROM device_attribute
		 WHERE tenant_id = $1 AND device_id = $2
		   AND ($3 = '' OR scope = $3)
		 ORDER BY scope, key`,
		tenantID, deviceID, scope)
	if err != nil {
		return nil, fmt.Errorf("list attributes: %w", err)
	}
	defer rows.Close()

	attributes := []AttributeDTO{}
	for rows.Next() {
		var attribute AttributeDTO
		var updatedAt time.Time
		if err := rows.Scan(&attribute.Scope, &attribute.Key,
			&attribute.Value, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan attribute: %w", err)
		}
		attribute.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
		attributes = append(attributes, attribute)
	}
	return attributes, rows.Err()
}

// PutAttribute upserts one platform-writable attribute. CLIENT scope is
// rejected because devices own it; the upsert preserves the identity of an
// existing key while replacing only the value and timestamp.
func (a *Admin) PutAttribute(ctx context.Context, tenantID, deviceID, scope, key string, value json.RawMessage) error {
	if scope != ScopeServer && scope != ScopeShared {
		return fmt.Errorf("%w: only SERVER_SCOPE and SHARED_SCOPE are writable", ErrInvalidArgument)
	}
	if key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidArgument)
	}
	if !json.Valid(value) {
		return fmt.Errorf("%w: value must be valid JSON", ErrInvalidArgument)
	}
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return err
	}

	var updatedAt time.Time
	if err := a.pool.QueryRow(ctx,
		`INSERT INTO device_attribute (tenant_id, device_id, scope, key, value)
		 VALUES ($1,$2,$3,$4,$5::jsonb)
		 ON CONFLICT (tenant_id, device_id, scope, key)
		 DO UPDATE SET value = EXCLUDED.value, updated_at = now()
		 RETURNING updated_at`,
		tenantID, deviceID, scope, key, string(value)).Scan(&updatedAt); err != nil {
		return fmt.Errorf("put attribute: %w", mapSQLError(err))
	}
	// Fan the change out to a live session. This is a best-effort delivery:
	// persistence already succeeded, so a publish failure must not fail the
	// write or roll the row back.
	valueCopy := append(json.RawMessage(nil), value...)
	if a.attributePublisher != nil {
		a.attributePublisher.PublishAttributePush(mqttapi.AttributePushFrame{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			Scope:     scope,
			Key:       key,
			Value:     valueCopy,
			UpdatedAt: updatedAt.UTC(),
		})
	}
	// Console-plane change notification for live operator UIs.
	if a.attributeEventPublisher != nil {
		a.attributeEventPublisher.PublishAttributeEvent(attributeevent.Event{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			Scope:     scope,
			Key:       key,
			Value:     append(json.RawMessage(nil), valueCopy...),
			UpdatedAt: updatedAt.UTC(),
			Source:    attributeevent.SourceServer,
		})
	}
	return nil
}

// SetAttributePublisher wires the server/shared attribute change fan-out.
func (a *Admin) SetAttributePublisher(publisher AttributePublisher) {
	a.attributePublisher = publisher
}

// SetAttributeEventPublisher wires the console-plane attribute change
// notification (WebSocket fan-out) for every platform-write scope.
func (a *Admin) SetAttributeEventPublisher(publisher AttributeEventPublisher) {
	a.attributeEventPublisher = publisher
}

// platformAttributeScopes is the ordered default for a device-initiated
// fetch when the request names no scope. CLIENT is intentionally excluded:
// devices own that scope and the platform never pushes it back.
var platformAttributeScopes = []string{ScopeServer, ScopeShared}

// FetchAttributes returns the platform-managed attributes (SERVER_SCOPE and
// SHARED_SCOPE) of a device as one key/value map per scope, for the
// device-driven on-demand sync (v1/me/attributes/get). An empty scopes list
// means both platform scopes; an explicitly requested CLIENT or unknown
// scope is rejected.
func (a *Admin) FetchAttributes(ctx context.Context, tenantID, deviceID string, scopes []string) (map[string]map[string]json.RawMessage, error) {
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return nil, err
	}
	requested := scopes
	if len(requested) == 0 {
		requested = platformAttributeScopes
	}
	for _, scope := range requested {
		if scope != ScopeServer && scope != ScopeShared {
			return nil, fmt.Errorf("%w: only SERVER_SCOPE and SHARED_SCOPE can be fetched", ErrInvalidArgument)
		}
	}

	out := make(map[string]map[string]json.RawMessage, len(requested))
	for _, scope := range requested {
		rows, err := a.pool.Query(ctx,
			`SELECT key, value::text FROM device_attribute
			 WHERE tenant_id = $1 AND device_id = $2 AND scope = $3
			 ORDER BY key`,
			tenantID, deviceID, scope)
		if err != nil {
			return nil, fmt.Errorf("fetch attributes: %w", err)
		}
		values := map[string]json.RawMessage{}
		for rows.Next() {
			var key string
			var value json.RawMessage
			if err := rows.Scan(&key, &value); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan attribute: %w", err)
			}
			values[key] = value
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("fetch attributes: %w", err)
		}
		out[scope] = values
	}
	return out, nil
}

// DeleteAttribute removes one platform-writable attribute and fans a null
// value out so a live edge drops the key from its local cache. CLIENT scope
// is owned by devices and cannot be deleted by the platform.
func (a *Admin) DeleteAttribute(ctx context.Context, tenantID, deviceID, scope, key string) error {
	if scope != ScopeServer && scope != ScopeShared {
		return fmt.Errorf("%w: only SERVER_SCOPE and SHARED_SCOPE are deletable", ErrInvalidArgument)
	}
	if key == "" {
		return fmt.Errorf("%w: key is required", ErrInvalidArgument)
	}
	if _, err := a.GetDevice(ctx, tenantID, deviceID); err != nil {
		return err
	}
	tag, err := a.pool.Exec(ctx,
		`DELETE FROM device_attribute
		 WHERE tenant_id = $1 AND device_id = $2 AND scope = $3 AND key = $4`,
		tenantID, deviceID, scope, key)
	if err != nil {
		return fmt.Errorf("delete attribute: %w", mapSQLError(err))
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	now := time.Now().UTC()
	null := json.RawMessage("null")
	// Tell a live edge to evict the key. Best-effort after commit, exactly
	// like the upsert push; the null payload is the deletion marker.
	if a.attributePublisher != nil {
		a.attributePublisher.PublishAttributePush(mqttapi.AttributePushFrame{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			Scope:     scope,
			Key:       key,
			Value:     null,
			UpdatedAt: now,
		})
	}
	// Console-plane removal so live operator UIs drop the row.
	if a.attributeEventPublisher != nil {
		a.attributeEventPublisher.PublishAttributeEvent(attributeevent.Event{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			Scope:     scope,
			Key:       key,
			Value:     null,
			UpdatedAt: now,
			Source:    attributeevent.SourceServer,
		})
	}
	return nil
}
