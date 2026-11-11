// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service_test.go covers catalog validation, normalization and the builtin
// delete protection using an in-memory repository, so these rules are checked
// without PostgreSQL.

package protocolcatalog

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// memRepo is an in-memory repository double.
type memRepo struct {
	rows    map[string]Family
	builtin map[string]bool
}

func newMemRepo() *memRepo {
	return &memRepo{rows: map[string]Family{}, builtin: map[string]bool{}}
}

func (m *memRepo) List(_ context.Context, q string) ([]Family, error) {
	out := []Family{}
	for _, f := range m.rows {
		q = strings.ToLower(q)
		if q == "" || strings.Contains(strings.ToLower(f.Family), q) ||
			strings.Contains(strings.ToLower(f.DisplayName), q) ||
			strings.Contains(strings.ToLower(f.ServiceName), q) {
			out = append(out, f)
		}
	}
	return out, nil
}

func (m *memRepo) Get(_ context.Context, family string) (Family, error) {
	f, ok := m.rows[family]
	if !ok {
		return Family{}, ErrNotFound
	}
	return f, nil
}

func (m *memRepo) Create(_ context.Context, in SaveInput) (Family, error) {
	if _, ok := m.rows[in.Family]; ok {
		return Family{}, ErrConflict
	}
	f := Family{Family: in.Family, DisplayName: in.DisplayName,
		ServiceName: in.ServiceName, ProtocolKey: in.ProtocolKey,
		Transports:       orTransports(in.Transports),
		ConnectionFields: orFields(in.ConnectionFields),
		PointFields:      orFields(in.PointFields),
		Builtin:          false}
	m.rows[in.Family] = f
	return f, nil
}

func (m *memRepo) Update(_ context.Context, family string, in SaveInput) (Family, error) {
	f, ok := m.rows[family]
	if !ok {
		return Family{}, ErrNotFound
	}
	f.DisplayName = in.DisplayName
	f.ServiceName = in.ServiceName
	f.ProtocolKey = in.ProtocolKey
	f.Transports = orTransports(in.Transports)
	f.ConnectionFields = orFields(in.ConnectionFields)
	f.PointFields = orFields(in.PointFields)
	m.rows[family] = f
	return f, nil
}

func (m *memRepo) Delete(_ context.Context, family string) error {
	if m.builtin[family] {
		return ErrBuiltin
	}
	if _, ok := m.rows[family]; !ok {
		return ErrNotFound
	}
	delete(m.rows, family)
	return nil
}

func orTransports(v []Transport) []Transport {
	if v == nil {
		return []Transport{}
	}
	return v
}

func orFields(v []FieldSpec) []FieldSpec {
	if v == nil {
		return []FieldSpec{}
	}
	return v
}

func TestCreateValidatesAndNormalizes(t *testing.T) {
	svc := New(newMemRepo())
	ctx := context.Background()

	created, err := svc.Create(ctx, SaveInput{
		Family:      "My-Fam",
		DisplayName: "  My Family  ",
		Transports:  []Transport{{Value: "x", Label: "X"}},
		ConnectionFields: []FieldSpec{
			{Key: "Host", Label: "Host", Kind: "string", Required: true},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Family != "my-fam" {
		t.Fatalf("family key must be lowercased/trimmed, got %q", created.Family)
	}
	if created.DisplayName != "My Family" {
		t.Fatalf("display name not trimmed: %q", created.DisplayName)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	svc := New(newMemRepo())
	ctx := context.Background()

	cases := []SaveInput{
		{Family: "bad key", DisplayName: "X"}, // invalid key
		{Family: "ok", DisplayName: ""},       // missing display name
		{Family: "ok", DisplayName: "X", // unknown kind
			ConnectionFields: []FieldSpec{{Key: "a", Label: "A", Kind: "nope"}}},
		{Family: "ok", DisplayName: "X", // select without options
			PointFields: []FieldSpec{{Key: "a", Label: "A", Kind: "select"}}},
		{Family: "ok", DisplayName: "X", // duplicate keys
			ConnectionFields: []FieldSpec{
				{Key: "a", Label: "A", Kind: "string"},
				{Key: "a", Label: "A2", Kind: "string"},
			}},
	}
	for i, in := range cases {
		if _, err := svc.Create(ctx, in); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}

func TestCreateConflict(t *testing.T) {
	repo := newMemRepo()
	svc := New(repo)
	ctx := context.Background()
	in := SaveInput{Family: "dup", DisplayName: "Dup"}
	if _, err := svc.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(ctx, in); !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
}

func TestDeleteBuiltinProtected(t *testing.T) {
	repo := newMemRepo()
	repo.builtin["modbus"] = true
	repo.rows["modbus"] = Family{Family: "modbus", Builtin: true}
	svc := New(repo)
	ctx := context.Background()

	if err := svc.Delete(ctx, "modbus"); !errors.Is(err, ErrBuiltin) {
		t.Fatalf("want ErrBuiltin, got %v", err)
	}
	if err := svc.Delete(ctx, "missing"); err == nil {
		t.Fatal("deleting a missing family must error")
	}
}

func TestUpdateMissingAndEdit(t *testing.T) {
	repo := newMemRepo()
	svc := New(repo)
	ctx := context.Background()
	if _, err := svc.Update(ctx, "ghost", SaveInput{DisplayName: "G"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	repo.rows["s7"] = Family{Family: "s7", Builtin: true}
	updated, err := svc.Update(ctx, "s7", SaveInput{DisplayName: "Siemens S7",
		ConnectionFields: []FieldSpec{{Key: "Host", Label: "Host", Kind: "string"}}})
	if err != nil {
		t.Fatal(err)
	}
	if updated.DisplayName != "Siemens S7" || !updated.Builtin {
		t.Fatalf("builtin row must remain editable but stay builtin: %+v", updated)
	}
}
