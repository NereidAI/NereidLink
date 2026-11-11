// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package edgeprofile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/nereidai/nereidlink/internal/registry"
)

// ErrInvalid means an adopt request was malformed.
var ErrInvalid = errors.New("edge profile adopt request invalid")

// ProfileStore is the native-profile subset edgeprofile adopts into and auto
// ingests to. It is satisfied by *registry.Admin and kept as an interface so
// the translation/ingest logic can be tested without a database.
type ProfileStore interface {
	GetProfileByName(ctx context.Context, tenantID, name string) (registry.ProfileDTO, error)
	CreateProfile(ctx context.Context, tenantID string, input registry.ProfileDTO) (registry.ProfileDTO, error)
	UpdateProfile(ctx context.Context, tenantID, profileID string, input registry.ProfileDTO) (registry.ProfileDTO, error)
	UpsertEdgeProfile(ctx context.Context, tenantID, gatewayID string, in registry.EdgeProfileUpsert) (registry.EdgeProfileUpsertResult, error)
	ImportEdgeProfile(ctx context.Context, tenantID string, in registry.EdgeProfileUpsert) (registry.EdgeProfileUpsertResult, error)
}

// CacheStore is the persistence subset the Service needs. It is satisfied by
// *Store and kept as an interface so the conflict policy can be unit tested
// without a database.
type CacheStore interface {
	Report(ctx context.Context, tenantID, gatewayID string, profile Profile, hash string) error
	ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]CachedProfile, error)
	Get(ctx context.Context, tenantID, gatewayID, name string) (Profile, CachedProfile, error)
	SetAdoption(ctx context.Context, tenantID, gatewayID, name, nativeProfileID string) error
}

// Service caches reported edge profiles and adopts them into native profiles.
type Service struct {
	store    CacheStore
	profiles ProfileStore
	drift    DriftSink
}

// DriftSink receives profile drift/collision events. It is optional and
// satisfied by the Core NATS-backed advisory publisher; when nil, drift is
// still reflected in storage but not announced.
type DriftSink interface {
	// EmitProfileDrift publishes one advisory. Implementations must not fail
	// ingestion; the caller only logs a returned error.
	EmitProfileDrift(ctx context.Context, event DriftEvent) error
}

// DriftEvent reports a same-name profile whose reported content differs from
// the stored library version, or an edge report colliding with an
// operator-owned profile.
type DriftEvent struct {
	// Kind is "profile.drift" or "profile.collision".
	Kind string
	// TenantID owns the profile.
	TenantID string
	// GatewayID reported the new content.
	GatewayID string
	// ProfileName is the tenant-unique name.
	ProfileName string
	// ProfileID is the existing library profile.
	ProfileID string
}

// Drift/collision advisory kinds.
const (
	DriftKindProfile   = "profile.drift"
	DriftKindCollision = "profile.collision"
)

// NewService wires the cache store to the native profile store.
func NewService(store *Store, profiles ProfileStore) *Service {
	return &Service{store: store, profiles: profiles}
}

// SetDriftSink attaches the advisory publisher for profile drift/collision.
func (s *Service) SetDriftSink(sink DriftSink) {
	s.drift = sink
}

// Ingest persists one reported batch. Identity and the negotiated gateway are
// established by the MQTT layer; this method never trusts frame-embedded
// tenant or gateway fields. Profiles with a blank name are skipped rather than
// rejecting the whole batch, so one malformed entry cannot suppress the rest.
//
// Besides the per-gateway raw cache (0056) every profile is auto-ingested into
// the tenant profile library (ADR-0032). A cache write or ingest error fails
// the batch so the post-hello replay retries; drift/collision is not an error
// and is announced via the optional DriftSink.
func (s *Service) Ingest(ctx context.Context, tenantID, gatewayID string, frame Frame) error {
	for i := range frame.Profiles {
		profile := frame.Profiles[i]
		if strings.TrimSpace(profile.Name) == "" {
			continue
		}
		hash, err := CanonicalHash(profile)
		if err != nil {
			return fmt.Errorf("hash edge profile %q: %w", profile.Name, err)
		}
		if err := s.store.Report(ctx, tenantID, gatewayID, profile, hash); err != nil {
			return err
		}

		payload, err := json.Marshal(profile)
		if err != nil {
			return fmt.Errorf("encode edge profile %q: %w", profile.Name, err)
		}
		result, err := s.profiles.UpsertEdgeProfile(
			ctx, tenantID, gatewayID, toUpsert(profile, hash, payload))
		if err != nil {
			return fmt.Errorf("ingest edge profile %q: %w", profile.Name, err)
		}
		// First sight auto-creates the tenant library profile (ADR-0032 §5);
		// link this gateway cache row to it so the per-gateway detail/adopt
		// view resolves to the library object. Drift and collision outcomes
		// are deliberately NOT linked: they await an operator decision.
		if result.ProfileID != "" && (result.Created || result.Unchanged) {
			if err := s.store.SetAdoption(ctx, tenantID, gatewayID, profile.Name, result.ProfileID); err != nil {
				return fmt.Errorf("link edge profile %q: %w", profile.Name, err)
			}
		}
		s.announceDrift(ctx, result, tenantID, gatewayID, profile.Name)
	}
	return nil
}

// announceDrift emits a drift/collision advisory when configured. A publish
// failure never blocks the uplink path, so the error is intentionally dropped.
func (s *Service) announceDrift(ctx context.Context, result registry.EdgeProfileUpsertResult, tenantID, gatewayID, name string) {
	if s.drift == nil || (!result.Drift && !result.Collision) {
		return
	}
	kind := DriftKindProfile
	if result.Collision {
		kind = DriftKindCollision
	}
	_ = s.drift.EmitProfileDrift(ctx, DriftEvent{
		Kind: kind, TenantID: tenantID, GatewayID: gatewayID,
		ProfileName: name, ProfileID: result.ProfileID,
	})
}

// ListForGateway returns cached edge profile metadata for one gateway.
func (s *Service) ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]CachedProfile, error) {
	return s.store.ListForGateway(ctx, tenantID, gatewayID)
}

// Get returns one cached edge profile.
func (s *Service) Get(ctx context.Context, tenantID, gatewayID, name string) (Profile, CachedProfile, error) {
	return s.store.Get(ctx, tenantID, gatewayID, name)
}

// ImportResult is the outcome of a profile-document import or preview.
type ImportResult struct {
	// Name is the parsed profile name.
	Name string
	// Protocol is the declared/inferred protocol family ("" unknown).
	Protocol string
	// ProfileID is the created library profile (empty for preview).
	ProfileID string
	// PointCount is the number of points imported.
	PointCount int
	// Warnings lists non-fatal parse notices.
	Warnings []string
}

// ImportDocument parses and stores one pasted neutral/EdgeX profile document
// as a source=import library profile. It performs no gateway provenance.
func (s *Service) ImportDocument(ctx context.Context, tenantID string, raw []byte) (ImportResult, error) {
	parsed, err := ParseDocument(raw)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	parsed.Upsert.ContentHash = contentHashOf(raw)
	res, err := s.profiles.ImportEdgeProfile(ctx, tenantID, parsed.Upsert)
	if err != nil {
		return ImportResult{}, err
	}
	return ImportResult{
		ProfileID: res.ProfileID, PointCount: res.PointCount,
		Warnings: parsed.Warnings,
	}, nil
}

// PreviewDocument parses a document without persisting, returning the name,
// inferred protocol, point count and warnings so the console can show an
// import confirmation screen.
func (s *Service) PreviewDocument(raw []byte) (ImportResult, error) {
	parsed, err := ParseDocument(raw)
	if err != nil {
		return ImportResult{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return ImportResult{
		Name: parsed.Name, Protocol: parsed.Protocol,
		PointCount: len(parsed.Upsert.Points),
		Warnings:   parsed.Warnings,
	}, nil
}

// Detail returns one cached edge profile together with the adopt// classification of every reported resource, backing the read-only content
// view. It performs no adoption.
func (s *Service) Detail(ctx context.Context, tenantID, gatewayID, name string) (Detail, error) {
	profile, cached, err := s.store.Get(ctx, tenantID, gatewayID, name)
	if err != nil {
		return Detail{}, err
	}
	return Detail{
		Cached:    cached,
		Resources: ClassifyResources(profile),
	}, nil
}

// Adopt resolves one gateway-cached edge profile onto its tenant library
// profile and links the cache row to it.
//
// Since ADR-0032 the first uplink already auto-creates the library profile
// (ingest), so Adopt no longer creates descriptors or takes a conflict
// decision: it is an idempotent "show/link" action in the default mode, and
// with mode=update it force-syncs this gateway's cached content over a
// detected drift. TargetName is retained on the request for backward
// compatibility but the library profile name always equals the edge name.
func (s *Service) Adopt(ctx context.Context, tenantID string, req AdoptRequest) (AdoptResult, error) {
	if req.Mode != AdoptDefault && req.Mode != AdoptUpdate {
		return AdoptResult{}, fmt.Errorf("%w: mode must be empty or update", ErrInvalid)
	}
	if strings.TrimSpace(req.GatewayID) == "" || strings.TrimSpace(req.Name) == "" {
		return AdoptResult{}, fmt.Errorf("%w: gateway and profile name are required", ErrInvalid)
	}

	edge, _, err := s.store.Get(ctx, tenantID, req.GatewayID, req.Name)
	if err != nil {
		return AdoptResult{}, err
	}
	_, skipped := Translate(edge)

	profileID := ""
	if req.Mode == AdoptUpdate {
		hash, err := CanonicalHash(edge)
		if err != nil {
			return AdoptResult{}, fmt.Errorf("hash edge profile %q: %w", edge.Name, err)
		}
		payload, err := json.Marshal(edge)
		if err != nil {
			return AdoptResult{}, fmt.Errorf("encode edge profile %q: %w", edge.Name, err)
		}
		upsert := toUpsert(edge, hash, payload)
		upsert.Force = true
		res, err := s.profiles.UpsertEdgeProfile(ctx, tenantID, req.GatewayID, upsert)
		if err != nil {
			return AdoptResult{}, err
		}
		profileID = res.ProfileID
	} else {
		existing, err := s.profiles.GetProfileByName(ctx, tenantID, edge.Name)
		if err != nil {
			return AdoptResult{}, err
		}
		profileID = existing.ID
	}

	if err := s.store.SetAdoption(ctx, tenantID, req.GatewayID, req.Name, profileID); err != nil {
		return AdoptResult{}, err
	}
	updated := req.Mode == AdoptUpdate
	return AdoptResult{
		ProfileID:    profileID,
		ProfileName:  edge.Name,
		Updated:      updated,
		AdoptedCount: len(edge.DeviceResources) - len(skipped),
		Skipped:      skipped,
	}, nil
}
