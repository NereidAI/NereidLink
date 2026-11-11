// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// invalidations.go subscribes to credential invalidation events published
// by the registry and evicts the affected entries, closing the revocation
// window instead of waiting for the TTL.

package credcache

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/nats-io/nats.go"
)

// SubjectEntryInvalidation carries one changed credentials id.
const SubjectEntryInvalidation = "nl.credcache.credential"

// SubjectEpochInvalidation carries one tenant epoch bump.
const SubjectEpochInvalidation = "nl.credcache.epoch"

// credentialPayload identifies one invalidated credential.
type credentialPayload struct {
	// CredentialsID is the globally unique credential identifier.
	CredentialsID string `json:"credentials_id"`
}

// epochPayload identifies one tenant whose epoch was bumped.
type epochPayload struct {
	// TenantID is the affected tenant.
	TenantID string `json:"tenant_id"`
}

// StartInvalidationListener subscribes to both subjects until ctx is
// cancelled. Duplicated or stale events are harmless because eviction is
// idempotent.
func (a *CachedAuthenticator) StartInvalidationListener(ctx context.Context, nc *nats.Conn) error {
	credSub, err := nc.Subscribe(SubjectEntryInvalidation, func(msg *nats.Msg) {
		var payload credentialPayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil || payload.CredentialsID == "" {
			return
		}
		a.evict(payload.CredentialsID)
	})
	if err != nil {
		return err
	}
	epochSub, err := nc.Subscribe(SubjectEpochInvalidation, func(msg *nats.Msg) {
		var payload epochPayload
		if err := json.Unmarshal(msg.Data, &payload); err != nil || payload.TenantID == "" {
			return
		}
		a.fenceTenant(payload.TenantID)
	})
	if err != nil {
		_ = credSub.Unsubscribe()
		return err
	}

	go func() {
		<-ctx.Done()
		_ = credSub.Unsubscribe()
		_ = epochSub.Unsubscribe()
	}()
	return nil
}

// logCacheWarn records a non-fatal cache tier failure without exposing
// credential material.
func logCacheWarn(reason, credentialsID string, err error) {
	slog.Warn("credential cache tier failure",
		"reason", reason, "credentials_id", credentialsID, "error", err)
}
