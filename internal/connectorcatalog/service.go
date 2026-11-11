// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the catalog facade the hello path and the desired-write path
// use: it compiles and upserts schemas advertised by an edge, and validates a
// connector config document against the registered schema for its type.

package connectorcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
)

// registry is the persistence surface the service needs.
type registry interface {
	Upsert(ctx context.Context, reg Registration, tenantID, registeredBy, source string) (Schema, error)
	Get(ctx context.Context, tenantID, connectorType, version string) (Schema, error)
	HighestVersion(ctx context.Context, tenantID, connectorType string) (string, error)
	List(ctx context.Context, tenantID string) ([]Schema, error)
	Family(ctx context.Context, tenantID, connectorType string) (string, error)
}

// Service compiles, stores and applies connector config schemas.
type Service struct {
	store registry
	cache *cache
	log   *slog.Logger
}

// Option configures a Service.
type Option func(*Service)

// WithLogger sets the logger.
func WithLogger(log *slog.Logger) Option {
	return func(s *Service) { s.log = log }
}

// New constructs the catalog service over a Store.
func New(store registry, opts ...Option) *Service {
	s := &Service{store: store, cache: newCache()}
	for _, opt := range opts {
		opt(s)
	}
	if s.log == nil {
		s.log = slog.Default()
	}
	return s
}

// RegisterFromHello compiles and upserts every inline schema advertised by
// one negotiated gateway. Each entry is compiled BEFORE writing, so a
// malformed schema is rejected and does not poison the catalog. Registration
// is partial-success: a bad entry is reported rejected and the rest are still
// stored. tenant/gateway come from the authenticated connection, never the
// frame. An entry missing a connector_type or schema is rejected.
func (s *Service) RegisterFromHello(ctx context.Context, tenantID, gatewayID string, regs []Registration) []SchemaResult {
	results := make([]SchemaResult, 0, len(regs))
	for _, reg := range regs {
		res := SchemaResult{
			ConnectorType:        reg.ConnectorType,
			ConnectorTypeVersion: reg.ConnectorTypeVersion,
		}
		if reg.ConnectorType == "" || len(reg.ConfigSchema) == 0 {
			res.Reason = "type and config_schema are required"
			results = append(results, res)
			continue
		}
		// Compile first so an invalid document never reaches the store.
		if _, err := s.cache.compiled(reg.ConfigSchema); err != nil {
			res.Reason = err.Error()
			results = append(results, res)
			continue
		}
		if _, err := s.store.Upsert(ctx, reg, tenantID, gatewayID, SourceEdge); err != nil {
			res.Reason = err.Error()
			results = append(results, res)
			continue
		}
		res.Accepted = true
		results = append(results, res)
	}
	return results
}

// ValidateConfig validates one config document against the registered schema
// for the tenant's connector type/version. When no schema is registered the
// config is accepted (best-effort validation keeps unmanaged types working).
// A schema that exists but is violated returns an error wrapping
// ErrConfigViolation. Empty/absent config is normalized to {} before
// validation so an object-typed schema can require fields.
func (s *Service) ValidateConfig(ctx context.Context, tenantID, connectorType, version string, config json.RawMessage) error {
	if connectorType == "" {
		return nil
	}
	sch, err := s.store.Get(ctx, tenantID, connectorType, version)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.validateAgainst(sch.ConfigSchema, config)
}

// ValidateOverride validates a per-instance override (patch) document against
// a patch-relaxed variant of the registered schema: presence constraints are
// stripped at every level because the template is expected to supply required
// fields, while type/shape constraints on the keys the override actually sets
// are still enforced. Like ValidateConfig it is best-effort when the type has
// no registered schema.
func (s *Service) ValidateOverride(ctx context.Context, tenantID, connectorType, version string, override json.RawMessage) error {
	if connectorType == "" {
		return nil
	}
	sch, err := s.store.Get(ctx, tenantID, connectorType, version)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	relaxed, err := relaxForPatch(sch.ConfigSchema)
	if err != nil {
		return err
	}
	return s.validateAgainst(relaxed, override)
}

// ListSchemas returns every catalog schema registered for one tenant (the
// schemas an edge advertised during hello). It backs the console catalog
// read that drives schema-driven connector config forms.
func (s *Service) ListSchemas(ctx context.Context, tenantID string) ([]Schema, error) {
	return s.store.List(ctx, tenantID)
}

// HasSchema reports whether a schema is registered for the type/version.
func (s *Service) HasSchema(ctx context.Context, tenantID, connectorType, version string) bool {
	_, err := s.store.Get(ctx, tenantID, connectorType, version)
	return err == nil
}

// Family returns the declared protocol family for a connector type, resolved
// across its registered versions. It returns "" (unknown) when no schema is
// registered for the type or no version declares a family, so callers fail
// open rather than rejecting unmanaged connector types (ADR-0032 §6).
func (s *Service) Family(ctx context.Context, tenantID, connectorType string) (string, error) {
	return s.store.Family(ctx, tenantID, connectorType)
}

// ResolveVersion implements NL's unified plugin-version selection
// (ADR-0026): when the caller already pinned an explicit type_version it is
// returned unchanged; when version is empty the platform selects the highest
// registered version for that connector type. With no versioned registration
// "" is returned with no error — callers then fall back to the unversioned
// ("any") schema / validation fail-open exactly as before.
func (s *Service) ResolveVersion(ctx context.Context, tenantID, connectorType, version string) (string, error) {
	if version != "" || connectorType == "" {
		return version, nil
	}
	high, err := s.store.HighestVersion(ctx, tenantID, connectorType)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return high, nil
}
