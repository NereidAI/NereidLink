// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"errors"
	"testing"
)

func strptr(s string) *string { return &s }

func validInput() EndpointInput {
	return EndpointInput{
		CompanionID: "face-service",
		GatewayID:   "gateway-1",
		BaseURL:     "https://companion.example/",
		AuthToken:   strptr("token-1"),
	}
}

func TestAdminRegisterDefaults(t *testing.T) {
	admin := NewAdmin(newMemStore())
	ep, err := admin.Register(context.Background(), "tenant-1", validInput())
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if ep.ContractVersion != ContractV1 || !ep.Enabled {
		t.Fatalf("defaults not applied: %+v", ep)
	}
	if ep.BaseURL != "https://companion.example" {
		t.Fatalf("trailing slash not trimmed: %q", ep.BaseURL)
	}
}

func TestAdminRegisterValidation(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*EndpointInput)
	}{
		{"bad companion id", func(in *EndpointInput) { in.CompanionID = "Face Service" }},
		{"bad gateway id", func(in *EndpointInput) { in.GatewayID = "gate way" }},
		{"relative url", func(in *EndpointInput) { in.BaseURL = "/api" }},
		{"ftp url", func(in *EndpointInput) { in.BaseURL = "ftp://host" }},
		{"insecure on http", func(in *EndpointInput) {
			in.BaseURL = "http://host"
			in.InsecureSkipVerify = true
		}},
		{"unknown contract", func(in *EndpointInput) { in.ContractVersion = "companion-subscription/v9" }},
		{"missing token", func(in *EndpointInput) { in.AuthToken = nil }},
		{"empty token", func(in *EndpointInput) { in.AuthToken = strptr("  ") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			admin := NewAdmin(newMemStore())
			in := validInput()
			tc.mutate(&in)
			if _, err := admin.Register(context.Background(), "tenant-1", in); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestAdminRegisterUpsertNaturalKey(t *testing.T) {
	store := newMemStore()
	admin := NewAdmin(store)
	first, err := admin.Register(context.Background(), "tenant-1", validInput())
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	in := validInput()
	in.BaseURL = "https://companion-2.example"
	second, err := admin.Register(context.Background(), "tenant-1", in)
	if err != nil {
		t.Fatalf("second register: %v", err)
	}
	if first.ID != second.ID {
		t.Fatal("natural-key re-register must keep the surrogate id")
	}
	got, err := admin.GetEndpoint(context.Background(), "tenant-1", first.ID)
	if err != nil || got.BaseURL != "https://companion-2.example" {
		t.Fatalf("upsert did not update: %+v %v", got, err)
	}
}

func TestAdminUpdateRules(t *testing.T) {
	store := newMemStore()
	admin := NewAdmin(store)
	ep, err := admin.Register(context.Background(), "tenant-1", validInput())
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	// Natural key is immutable.
	changed := validInput()
	changed.CompanionID = "meter-service"
	if _, err := admin.Update(context.Background(), "tenant-1", ep.ID, changed); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("natural-key change err = %v, want ErrInvalidArgument", err)
	}

	// Nil token keeps the stored secret.
	keep := validInput()
	keep.AuthToken = nil
	updated, err := admin.Update(context.Background(), "tenant-1", ep.ID, keep)
	if err != nil {
		t.Fatalf("keep-token update: %v", err)
	}
	if updated.AuthToken != "token-1" {
		t.Fatalf("token = %q, want preserved token-1", updated.AuthToken)
	}

	// Explicit empty string clears the token.
	clear := validInput()
	clear.AuthToken = strptr("")
	updated, err = admin.Update(context.Background(), "tenant-1", ep.ID, clear)
	if err != nil {
		t.Fatalf("clear-token update: %v", err)
	}
	if updated.AuthToken != "" {
		t.Fatalf("token = %q, want empty", updated.AuthToken)
	}

	if _, err := admin.Update(context.Background(), "tenant-1", "missing", validInput()); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("missing update err = %v, want ErrEndpointNotFound", err)
	}
}

func TestAdminDeleteAndDesired(t *testing.T) {
	store := newMemStore()
	admin := NewAdmin(store)
	ep, err := admin.Register(context.Background(), "tenant-1", validInput())
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := store.ReplaceDesired(context.Background(), "tenant-1", ep.ID, "artifact-a", []DesiredRow{
		{RowID: "companion:gateway-1:dev:cam-1", SourceDevice: "cam-1",
			PipelineID: "p", ConclusionDeviceID: "dev", ArtifactKey: "artifact-a"},
	}); err != nil {
		t.Fatalf("seed desired: %v", err)
	}
	rows, err := admin.ListDesired(context.Background(), "tenant-1", ep.ID)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListDesired = %d, %v", len(rows), err)
	}
	if err := admin.Delete(context.Background(), "tenant-1", ep.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := admin.Delete(context.Background(), "tenant-1", ep.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("repeat delete err = %v, want ErrEndpointNotFound", err)
	}
	if _, err := admin.ListDesired(context.Background(), "tenant-1", ep.ID); !errors.Is(err, ErrEndpointNotFound) {
		t.Fatalf("desired after delete err = %v, want ErrEndpointNotFound", err)
	}
}
