// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// credcache_wiring.go provides the publisher that turns registry credential
// changes into cache invalidation events.

package main

import (
	"context"
	"encoding/json"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/credcache"
)

// credcacheInvalidator publishes credential cache eviction events.
type credcacheInvalidator struct {
	nc *nats.Conn
}

// credentialInvalidationPayload identifies one changed credential.
type credentialInvalidationPayload struct {
	CredentialsID string `json:"credentials_id"`
}

// epochInvalidationPayload identifies one tenant epoch bump.
type epochInvalidationPayload struct {
	TenantID string `json:"tenant_id"`
}

// CredentialInvalidated publishes a single-credential eviction event.
func (p *credcacheInvalidator) CredentialInvalidated(_ context.Context, credentialsID string) error {
	data, err := json.Marshal(credentialInvalidationPayload{CredentialsID: credentialsID})
	if err != nil {
		return err
	}
	return p.nc.Publish(credcache.SubjectEntryInvalidation, data)
}

// EpochInvalidated publishes a tenant-wide fence event.
func (p *credcacheInvalidator) EpochInvalidated(_ context.Context, tenantID string) error {
	data, err := json.Marshal(epochInvalidationPayload{TenantID: tenantID})
	if err != nil {
		return err
	}
	return p.nc.Publish(credcache.SubjectEpochInvalidation, data)
}
