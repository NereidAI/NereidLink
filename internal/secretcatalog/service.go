// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the descriptor facade the hello path, the credential write
// path and the management REST use: it compiles and stores descriptors
// advertised by an edge, manages tenant-defined descriptors and validates
// credential documents.

package secretcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SourceBlank marks the non-persisted, fully permissive descriptor returned
// for an unregistered type.
const SourceBlank = "blank"

// registry is the persistence surface the service needs.
type registry interface {
	Upsert(ctx context.Context, reg Registration, tenantID, registeredBy string) (SecretType, error)
	Create(ctx context.Context, reg Registration, tenantID string) (SecretType, error)
	Update(ctx context.Context, tenantID, secretType, version string, reg Registration) (SecretType, error)
	Delete(ctx context.Context, tenantID, secretType, version string) error
	Get(ctx context.Context, tenantID, secretType, version string) (SecretType, error)
	List(ctx context.Context, tenantID string) ([]SecretType, error)
}

// Service compiles, stores and applies edge secret type descriptors.
type Service struct {
	store registry
	cache *schemaCache
}

// New constructs the descriptor service over a Store.
func New(store registry) *Service {
	return &Service{store: store, cache: newSchemaCache()}
}

// RegisterFromHello compiles and upserts every descriptor advertised by
// one negotiated gateway. Each entry is compiled BEFORE writing, so a
// malformed schema is rejected and does not poison the catalog. Registration
// is partial-success: a bad entry is reported rejected and the rest are
// still stored. tenant/gateway come from the authenticated connection,
// never the frame. An entry missing a type or schema is rejected.
func (s *Service) RegisterFromHello(ctx context.Context, tenantID, gatewayID string, regs []Registration) []TypeResult {
	results := make([]TypeResult, 0, len(regs))
	for _, reg := range regs {
		res := TypeResult{SecretType: reg.SecretType, TypeVersion: reg.TypeVersion}
		if reg.SecretType == "" || len(reg.FieldsSchema) == 0 {
			res.Reason = "type and fields_schema are required"
			results = append(results, res)
			continue
		}
		// Compile first so an invalid document never reaches the store.
		if _, err := s.cache.compiled(reg.FieldsSchema); err != nil {
			res.Reason = err.Error()
			results = append(results, res)
			continue
		}
		if _, err := s.store.Upsert(ctx, reg, tenantID, gatewayID); err != nil {
			res.Reason = err.Error()
			results = append(results, res)
			continue
		}
		res.Accepted = true
		results = append(results, res)
	}
	return results
}

// CreateManual validates and stores one tenant-defined descriptor, mapping a
// duplicate key to ErrConflict.
func (s *Service) CreateManual(ctx context.Context, tenantID string, reg Registration) (SecretType, error) {
	if reg.SecretType == "" || len(reg.FieldsSchema) == 0 {
		return SecretType{}, fmt.Errorf("%w: type and fields_schema are required", ErrInvalidSchema)
	}
	if _, err := s.cache.compiled(reg.FieldsSchema); err != nil {
		return SecretType{}, err
	}
	return s.store.Create(ctx, reg, tenantID)
}

// UpdateManual validates and replaces one tenant-defined descriptor.
func (s *Service) UpdateManual(ctx context.Context, tenantID, secretType, version string, reg Registration) (SecretType, error) {
	if reg.SecretType == "" || len(reg.FieldsSchema) == 0 {
		return SecretType{}, fmt.Errorf("%w: type and fields_schema are required", ErrInvalidSchema)
	}
	if _, err := s.cache.compiled(reg.FieldsSchema); err != nil {
		return SecretType{}, err
	}
	return s.store.Update(ctx, tenantID, secretType, version, reg)
}

// DeleteManual removes one tenant-defined descriptor.
func (s *Service) DeleteManual(ctx context.Context, tenantID, secretType, version string) error {
	return s.store.Delete(ctx, tenantID, secretType, version)
}

// CloneToManual copies an edge-advertised descriptor into a new
// tenant-defined descriptor keyed by newType, so an operator can edit a copy
// without touching the original.
func (s *Service) CloneToManual(ctx context.Context, tenantID, secretType, version, newType string) (SecretType, error) {
	origin, err := s.store.Get(ctx, tenantID, secretType, version)
	if err != nil {
		return SecretType{}, err
	}
	if newType == "" {
		newType = origin.SecretType + "-copy"
	}
	reg := Registration{
		SecretType:   newType,
		TypeVersion:  origin.TypeVersion,
		FieldsSchema: origin.FieldsSchema,
		DisplayName:  origin.DisplayName,
	}
	return s.CreateManual(ctx, tenantID, reg)
}

// List returns every descriptor for one tenant.
func (s *Service) List(ctx context.Context, tenantID string) ([]SecretType, error) {
	return s.store.List(ctx, tenantID)
}

// Get returns the descriptor for one type/version.
func (s *Service) Get(ctx context.Context, tenantID, secretType, version string) (SecretType, error) {
	return s.store.Get(ctx, tenantID, secretType, version)
}

// blankSchema is the permissive object schema used for ad-hoc types.
var blankSchema = json.RawMessage(`{"type":"object"}`)

// Descriptor resolves the descriptor for one type/version, returning a
// non-persisted permissive blank descriptor when nothing is registered.
// Credential forms therefore always render: registered schemas drive typed
// fields, blank descriptors drive the generic key/value editor.
func (s *Service) Descriptor(ctx context.Context, tenantID, secretType, version string) (SecretType, error) {
	st, err := s.store.Get(ctx, tenantID, secretType, version)
	switch {
	case err == nil:
		return st, nil
	case errors.Is(err, ErrNotFound):
		return SecretType{
			TenantID:     tenantID,
			SecretType:   secretType,
			TypeVersion:  version,
			FieldsSchema: blankSchema,
			Source:       SourceBlank,
		}, nil
	default:
		return SecretType{}, err
	}
}

// Validate validates one credential document against the registered
// descriptor. When no descriptor is registered the document is accepted
// (blank types carry arbitrary key/value pairs).
func (s *Service) Validate(ctx context.Context, tenantID, secretType, version string, doc json.RawMessage) error {
	st, err := s.store.Get(ctx, tenantID, secretType, version)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return validateAgainst(s.cache, st.FieldsSchema, doc)
}
