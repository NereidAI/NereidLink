// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"sort"
	"sync"
)

// memStore is the in-memory Store double used by owner tests. It mirrors the
// PGStore transaction semantics at the granularity the owner relies on.
type memStore struct {
	mu        sync.Mutex
	endpoints map[string]Endpoint
	rows      map[string]map[string]DesiredRow // endpointID -> rowID -> row
}

func newMemStore() *memStore {
	return &memStore{
		endpoints: map[string]Endpoint{},
		rows:      map[string]map[string]DesiredRow{},
	}
}

func (m *memStore) GetEndpoint(_ context.Context, tenantID, companionID, gatewayID string) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ep := range m.endpoints {
		if ep.TenantID == tenantID && ep.CompanionID == companionID && ep.GatewayID == gatewayID {
			return ep, nil
		}
	}
	return Endpoint{}, ErrStoreNotFound
}

func (m *memStore) GetEndpointByID(_ context.Context, tenantID, endpointID string) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.endpoints[endpointID]
	if !ok || ep.TenantID != tenantID {
		return Endpoint{}, ErrStoreNotFound
	}
	return ep, nil
}

func (m *memStore) UpsertEndpoint(_ context.Context, endpoint Endpoint) (Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, ep := range m.endpoints {
		if ep.TenantID == endpoint.TenantID &&
			ep.CompanionID == endpoint.CompanionID &&
			ep.GatewayID == endpoint.GatewayID {
			endpoint.ID = ep.ID
			endpoint.CreatedAt = ep.CreatedAt
			m.endpoints[ep.ID] = endpoint
			return endpoint, nil
		}
	}
	if endpoint.ID == "" {
		endpoint.ID = "ep-" + endpoint.CompanionID + "-" + endpoint.GatewayID
	}
	m.endpoints[endpoint.ID] = endpoint
	m.rows[endpoint.ID] = map[string]DesiredRow{}
	return endpoint, nil
}

func (m *memStore) ListEndpoints(_ context.Context, tenantID string) ([]Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Endpoint, 0)
	for _, ep := range m.endpoints {
		if ep.TenantID == tenantID {
			out = append(out, ep)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CompanionID != out[j].CompanionID {
			return out[i].CompanionID < out[j].CompanionID
		}
		return out[i].GatewayID < out[j].GatewayID
	})
	return out, nil
}

func (m *memStore) DeleteEndpoint(_ context.Context, tenantID, endpointID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.endpoints[endpointID]
	if !ok || ep.TenantID != tenantID {
		return ErrStoreNotFound
	}
	delete(m.endpoints, endpointID)
	delete(m.rows, endpointID)
	return nil
}

func (m *memStore) ReplaceDesired(_ context.Context, tenantID, endpointID, artifactKey string, rows []DesiredRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.endpoints[endpointID]
	if !ok || ep.TenantID != tenantID {
		return ErrStoreNotFound
	}
	set := m.rows[endpointID]
	for rowID, row := range set {
		if row.ArtifactKey == artifactKey {
			delete(set, rowID)
		}
	}
	for _, row := range rows {
		set[row.RowID] = row
	}
	return nil
}

func (m *memStore) DeleteDesiredArtifact(_ context.Context, tenantID, artifactKey string) (string, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpointID := ""
	for epID, set := range m.rows {
		ep, ok := m.endpoints[epID]
		if !ok || ep.TenantID != tenantID {
			continue
		}
		for rowID, row := range set {
			if row.ArtifactKey == artifactKey {
				endpointID = epID
				delete(set, rowID)
			}
		}
	}
	if endpointID == "" {
		return "", 0, nil
	}
	return endpointID, len(m.rows[endpointID]), nil
}

func (m *memStore) ListDesired(_ context.Context, tenantID, endpointID string) ([]DesiredRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.endpoints[endpointID]
	if !ok || ep.TenantID != tenantID {
		return nil, ErrStoreNotFound
	}
	set := m.rows[endpointID]
	out := make([]DesiredRow, 0, len(set))
	for _, row := range set {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SourceDevice != out[j].SourceDevice {
			return out[i].SourceDevice < out[j].SourceDevice
		}
		return out[i].RowID < out[j].RowID
	})
	return out, nil
}

func (m *memStore) ApplyReadback(_ context.Context, tenantID, endpointID string, readback []ReadbackRow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	ep, ok := m.endpoints[endpointID]
	if !ok || ep.TenantID != tenantID {
		return ErrStoreNotFound
	}
	set := m.rows[endpointID]
	for _, rb := range readback {
		row, exists := set[rb.ID]
		if !exists {
			continue
		}
		row.Revision = rb.Revision
		row.Status = rb.Status
		row.Reason = rb.Reason
		set[rb.ID] = row
	}
	return nil
}

// snapshotRows returns a copy of the desired rows for test assertions.
func (m *memStore) snapshotRows(endpointID string) []DesiredRow {
	m.mu.Lock()
	defer m.mu.Unlock()
	set := m.rows[endpointID]
	out := make([]DesiredRow, 0, len(set))
	for _, row := range set {
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RowID < out[j].RowID })
	return out
}
