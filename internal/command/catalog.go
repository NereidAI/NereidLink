// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// catalog.go is the REST-facing management layer over the per-device
// command catalog (the command whitelist). It validates operator-supplied
// declarations and never creates "auto" rows: those come exclusively from
// profile materialization in materialize.go.

package command

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// MaxCatalogNameLen bounds a command name so the catalog cannot be abused
// as storage; names are short identifiers (resource/command names).
const MaxCatalogNameLen = 128

// MaxCatalogDescriptionLen bounds the optional note.
const MaxCatalogDescriptionLen = 512

// ErrInvalidCatalogEntry indicates a malformed declaration.
var ErrInvalidCatalogEntry = errors.New("invalid command catalog entry")

// CatalogAdmin validates and persists catalog declarations.
type CatalogAdmin struct {
	store *Store
}

// NewCatalogAdmin constructs the management service.
func NewCatalogAdmin(store *Store) *CatalogAdmin {
	return &CatalogAdmin{store: store}
}

// CatalogEntryInput is the create/update payload.
type CatalogEntryInput struct {
	// Name is the whitelisted command identifier.
	Name string `json:"name"`

	// Interaction is one-way or request-response.
	Interaction string `json:"interaction"`

	// Timeliness is realtime or persistent.
	Timeliness string `json:"timeliness"`

	// DefaultTimeoutMS bounds the response wait; 0 means platform default.
	DefaultTimeoutMS int64 `json:"default_timeout_ms"`

	// AllowRead / AllowWrite require at least one true.
	AllowRead  bool `json:"allow_read"`
	AllowWrite bool `json:"allow_write"`

	// Enabled switches the declaration on or off.
	Enabled *bool `json:"enabled,omitempty"`

	// Description is an optional operator note.
	Description string `json:"description,omitempty"`
}

// CatalogEntryView is the serialized catalog row.
type CatalogEntryView struct {
	Name             string `json:"name"`
	Interaction      string `json:"interaction"`
	Timeliness       string `json:"timeliness"`
	DefaultTimeoutMS int64  `json:"default_timeout_ms"`
	AllowRead        bool   `json:"allow_read"`
	AllowWrite       bool   `json:"allow_write"`
	Enabled          bool   `json:"enabled"`
	Source           string `json:"source"`
	Description      string `json:"description,omitempty"`
	CreatedAt        string `json:"created_at"`
}

func toView(entry CatalogEntry) CatalogEntryView {
	return CatalogEntryView{
		Name:             entry.Name,
		Interaction:      entry.Interaction,
		Timeliness:       entry.Timeliness,
		DefaultTimeoutMS: entry.DefaultTimeout.Milliseconds(),
		AllowRead:        entry.AllowRead,
		AllowWrite:       entry.AllowWrite,
		Enabled:          entry.Enabled,
		Source:           entry.Source,
		Description:      entry.Description,
		CreatedAt:        entry.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// validate normalizes and validates one declaration. The name argument
// comes from the request body on create and from the URL on update.
func validate(name string, in CatalogEntryInput) (CatalogInput, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return CatalogInput{}, fmt.Errorf("%w: name is required", ErrInvalidCatalogEntry)
	}
	if len(name) > MaxCatalogNameLen {
		return CatalogInput{}, fmt.Errorf("%w: name too long", ErrInvalidCatalogEntry)
	}
	switch in.Interaction {
	case InteractionOneWay, InteractionRequestResponse:
	default:
		return CatalogInput{}, fmt.Errorf("%w: interaction must be %q or %q",
			ErrInvalidCatalogEntry, InteractionOneWay, InteractionRequestResponse)
	}
	switch in.Timeliness {
	case TimelinessRealtime, TimelinessPersistent:
	default:
		return CatalogInput{}, fmt.Errorf("%w: timeliness must be %q or %q",
			ErrInvalidCatalogEntry, TimelinessRealtime, TimelinessPersistent)
	}
	if in.DefaultTimeoutMS < 0 || in.DefaultTimeoutMS > 24*60*60*1000 {
		return CatalogInput{}, fmt.Errorf("%w: default timeout out of range", ErrInvalidCatalogEntry)
	}
	if !in.AllowRead && !in.AllowWrite {
		return CatalogInput{}, fmt.Errorf("%w: at least one direction (read/write) is required", ErrInvalidCatalogEntry)
	}
	if in.Interaction == InteractionOneWay && in.AllowRead {
		return CatalogInput{}, fmt.Errorf("%w: a read command must wait for a response (request-response)",
			ErrInvalidCatalogEntry)
	}
	if len(in.Description) > MaxCatalogDescriptionLen {
		return CatalogInput{}, fmt.Errorf("%w: description too long", ErrInvalidCatalogEntry)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return CatalogInput{
		Name:             name,
		Interaction:      in.Interaction,
		Timeliness:       in.Timeliness,
		DefaultTimeoutMS: in.DefaultTimeoutMS,
		AllowRead:        in.AllowRead,
		AllowWrite:       in.AllowWrite,
		Enabled:          enabled,
		Description:      strings.TrimSpace(in.Description),
		Source:           SourceManual,
	}, nil
}

// List returns the declared commands of one device.
func (a *CatalogAdmin) List(ctx context.Context, tenantID, deviceID string) ([]CatalogEntryView, error) {
	entries, err := a.store.ListCatalog(ctx, tenantID, deviceID)
	if err != nil {
		return nil, err
	}
	views := make([]CatalogEntryView, 0, len(entries))
	for _, entry := range entries {
		views = append(views, toView(entry))
	}
	return views, nil
}

// Create declares one command.
func (a *CatalogAdmin) Create(ctx context.Context, tenantID, deviceID string, in CatalogEntryInput) (CatalogEntryView, error) {
	input, err := validate(in.Name, in)
	if err != nil {
		return CatalogEntryView{}, err
	}
	entry, err := a.store.UpsertCatalog(ctx, tenantID, deviceID, input)
	if err != nil {
		return CatalogEntryView{}, err
	}
	return toView(entry), nil
}

// Update replaces one declaration identified by the URL name.
func (a *CatalogAdmin) Update(ctx context.Context, tenantID, deviceID, name string, in CatalogEntryInput) (CatalogEntryView, error) {
	input, err := validate(name, in)
	if err != nil {
		return CatalogEntryView{}, err
	}
	entry, err := a.store.UpsertCatalog(ctx, tenantID, deviceID, input)
	if err != nil {
		return CatalogEntryView{}, err
	}
	return toView(entry), nil
}

// Delete removes one declaration.
func (a *CatalogAdmin) Delete(ctx context.Context, tenantID, deviceID, name string) error {
	return a.store.DeleteCatalog(ctx, tenantID, deviceID, strings.TrimSpace(name))
}
