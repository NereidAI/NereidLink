// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// API is the REST-facing façade of the command package. It resolves the
// Command Catalog, applies catalog defaults and size limits, and builds the
// command identifier before routing.
type API struct {
	router         *Router
	store          *Store
	defaultTimeout time.Duration
	maxBodyBytes   int
}

const (
	// DefaultMaxBodyBytes is applied when configuration omits the command
	// payload limit so safety limits never silently disappear.
	DefaultMaxBodyBytes = 64 * 1024
)

// NewAPI constructs the command API façade.
func NewAPI(router *Router, store *Store, defaultTimeout time.Duration, maxBodyBytes int) *API {
	if maxBodyBytes <= 0 {
		maxBodyBytes = DefaultMaxBodyBytes
	}
	return &API{
		router:         router,
		store:          store,
		defaultTimeout: defaultTimeout,
		maxBodyBytes:   maxBodyBytes,
	}
}

// Send validates one command request against the catalog and dispatches it.
// timeoutMS is optional: when zero, the catalog default applies, then the
// configured platform default. direction is "read", "write" or empty; empty
// resolves to read when the entry allows reads, otherwise write.
func (a *API) Send(ctx context.Context, tenantID, deviceID, name, direction string, params []byte, timeoutMS int64) (Ack, error) {
	if len(params) > a.maxBodyBytes {
		return Ack{}, ErrBodyTooLarge
	}

	entry, err := a.store.FindCatalog(ctx, tenantID, deviceID, name)
	if err != nil {
		return Ack{}, err
	}

	direction, err = resolveDirection(entry, direction)
	if err != nil {
		return Ack{}, err
	}

	timeout := entry.DefaultTimeout
	if timeoutMS > 0 {
		timeout = time.Duration(timeoutMS) * time.Millisecond
	}
	if timeout <= 0 {
		timeout = a.defaultTimeout
	}

	c := Command{
		ID:          uuid.NewString(),
		TenantID:    tenantID,
		DeviceID:    deviceID,
		Name:        name,
		Params:      params,
		Interaction: entry.Interaction,
		Timeliness:  entry.Timeliness,
		Timeout:     timeout,
		Direction:   direction,
	}
	return a.router.Dispatch(ctx, c)
}

// ErrDirectionDenied indicates the catalog does not allow the requested
// read/write direction for the command.
var ErrDirectionDenied = errors.New("command direction not allowed by catalog")

// resolveDirection validates the requested direction against the catalog
// mask and defaults an empty request to read when allowed, else write.
func resolveDirection(entry CatalogEntry, direction string) (string, error) {
	switch direction {
	case "", DirectionRead:
		if entry.AllowRead {
			return DirectionRead, nil
		}
		if direction == "" && entry.AllowWrite {
			return DirectionWrite, nil
		}
	case DirectionWrite:
		if entry.AllowWrite {
			return DirectionWrite, nil
		}
	}
	return "", ErrDirectionDenied
}

// ErrBodyTooLarge indicates the parameter document exceeds the configured
// hard limit (ADR-0012 defaults to 64KB).
var ErrBodyTooLarge = errors.New("command body too large")
