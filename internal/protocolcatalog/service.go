// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the catalog facade the REST handlers use. It validates and
// normalizes submitted documents, enforces the builtin delete protection and
// keeps every persisted slice in its non-null empty form. Persistence is
// abstracted behind an interface so the service can be unit-tested with an
// in-memory double without PostgreSQL.

package protocolcatalog

import (
	"context"
	"strings"
)

// repository is the persistence surface the service needs.
type repository interface {
	List(ctx context.Context, q string) ([]Family, error)
	Get(ctx context.Context, family string) (Family, error)
	Create(ctx context.Context, in SaveInput) (Family, error)
	Update(ctx context.Context, family string, in SaveInput) (Family, error)
	Delete(ctx context.Context, family string) error
}

// Service applies catalog business rules over a Store.
type Service struct {
	store repository
}

// New constructs the catalog service over a Store.
func New(store repository) *Service { return &Service{store: store} }

// List returns families matching the optional search substring.
func (s *Service) List(ctx context.Context, q string) ([]Family, error) {
	return s.store.List(ctx, strings.TrimSpace(q))
}

// Get returns one family.
func (s *Service) Get(ctx context.Context, family string) (Family, error) {
	return s.store.Get(ctx, strings.TrimSpace(family))
}

// Create validates and stores a new custom family. The primary key is
// normalized to lowercase.
func (s *Service) Create(ctx context.Context, in SaveInput) (Family, error) {
	in.Family = strings.ToLower(strings.TrimSpace(in.Family))
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	normalizeFields(in.ConnectionFields)
	normalizeFields(in.PointFields)
	if err := validate(in, true); err != nil {
		return Family{}, err
	}
	return s.store.Create(ctx, in)
}

// Update validates and replaces one family's mutable columns. A missing row
// surfaces as ErrNotFound.
func (s *Service) Update(ctx context.Context, family string, in SaveInput) (Family, error) {
	family = strings.ToLower(strings.TrimSpace(family))
	in.Family = family
	in.DisplayName = strings.TrimSpace(in.DisplayName)
	normalizeFields(in.ConnectionFields)
	normalizeFields(in.PointFields)
	if err := validate(in, false); err != nil {
		return Family{}, err
	}
	return s.store.Update(ctx, family, in)
}

// Delete removes a custom family; it refuses a builtin row with ErrBuiltin.
func (s *Service) Delete(ctx context.Context, family string) error {
	return s.store.Delete(ctx, strings.ToLower(strings.TrimSpace(family)))
}

// normalizeFields trims every string field.
func normalizeFields(fields []FieldSpec) {
	for i := range fields {
		fields[i].Key = strings.TrimSpace(fields[i].Key)
		fields[i].Label = strings.TrimSpace(fields[i].Label)
		for j := range fields[i].Options {
			fields[i].Options[j].Value = strings.TrimSpace(fields[i].Options[j].Value)
			fields[i].Options[j].Label = strings.TrimSpace(fields[i].Options[j].Label)
		}
	}
}
