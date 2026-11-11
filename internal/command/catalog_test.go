// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"errors"
	"testing"
)

// TestValidateCatalogEntry covers the direction/interaction constraints an
// operator declaration must satisfy.
func TestValidateCatalogEntry(t *testing.T) {
	enabled := true
	cases := []struct {
		name    string
		in      CatalogEntryInput
		wantErr bool
	}{
		{
			name: "read request-response realtime",
			in: CatalogEntryInput{
				Name: "DeviceInformation", Interaction: InteractionRequestResponse,
				Timeliness: TimelinessRealtime, AllowRead: true, Enabled: &enabled,
			},
		},
		{
			name: "write one-way persistent",
			in: CatalogEntryInput{
				Name: "reboot", Interaction: InteractionOneWay,
				Timeliness: TimelinessPersistent, AllowWrite: true, Enabled: &enabled,
			},
		},
		{
			name: "read+write request-response",
			in: CatalogEntryInput{
				Name: "setpoint", Interaction: InteractionRequestResponse,
				Timeliness: TimelinessRealtime, AllowRead: true, AllowWrite: true,
			},
		},
		{
			name: "missing name",
			in: CatalogEntryInput{
				Interaction: InteractionRequestResponse, Timeliness: TimelinessRealtime,
				AllowRead: true,
			},
			wantErr: true,
		},
		{
			name: "no direction",
			in: CatalogEntryInput{
				Name: "x", Interaction: InteractionRequestResponse,
				Timeliness: TimelinessRealtime,
			},
			wantErr: true,
		},
		{
			name: "read cannot be one-way",
			in: CatalogEntryInput{
				Name: "x", Interaction: InteractionOneWay,
				Timeliness: TimelinessRealtime, AllowRead: true,
			},
			wantErr: true,
		},
		{
			name: "bad interaction",
			in: CatalogEntryInput{
				Name: "x", Interaction: "nope", Timeliness: TimelinessRealtime,
				AllowRead: true,
			},
			wantErr: true,
		},
		{
			name: "bad timeliness",
			in: CatalogEntryInput{
				Name: "x", Interaction: InteractionRequestResponse, Timeliness: "later",
				AllowRead: true,
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := validate(tc.in.Name, tc.in)
			if tc.wantErr && !errors.Is(err, ErrInvalidCatalogEntry) {
				t.Fatalf("want ErrInvalidCatalogEntry, got %v", err)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

// TestResolveDirection checks the catalog read/write mask enforcement and
// the empty-request default.
func TestResolveDirection(t *testing.T) {
	entry := CatalogEntry{AllowRead: true, AllowWrite: false}
	if got, err := resolveDirection(entry, ""); err != nil || got != DirectionRead {
		t.Fatalf("empty default = %q, %v; want read", got, err)
	}
	if got, err := resolveDirection(entry, DirectionRead); err != nil || got != DirectionRead {
		t.Fatalf("explicit read = %q, %v", got, err)
	}
	if _, err := resolveDirection(entry, DirectionWrite); !errors.Is(err, ErrDirectionDenied) {
		t.Fatalf("write on read-only entry err = %v, want ErrDirectionDenied", err)
	}

	writeOnly := CatalogEntry{AllowWrite: true}
	if got, err := resolveDirection(writeOnly, ""); err != nil || got != DirectionWrite {
		t.Fatalf("write-only default = %q, %v; want write", got, err)
	}
	if _, err := resolveDirection(writeOnly, DirectionRead); !errors.Is(err, ErrDirectionDenied) {
		t.Fatalf("read on write-only entry err = %v, want ErrDirectionDenied", err)
	}
}
