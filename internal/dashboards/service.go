// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package dashboards persists user-defined dashboards and their grid layout
// (ADR-0020). A dashboard is a named tenant-scoped layout descriptor; the
// service performs CRUD only and carries no business logic, so new widget
// types are a frontend change plus an optional validation entry here. It is
// kept separate from internal/dashboard, which aggregates read-only Overview
// snapshots.

package dashboards

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a dashboard id is absent for the tenant.
var ErrNotFound = errors.New("dashboard not found")

// ErrConflict is returned on a duplicate dashboard name.
var ErrConflict = errors.New("dashboard name already exists")

// ErrInvalidArgument is returned for malformed input.
var ErrInvalidArgument = errors.New("invalid dashboard argument")

// Widget types accepted by validation. Keeping the list here lets a bad
// type be rejected before it is persisted.
const (
	WidgetTimeseries = "timeseries"
	WidgetStat       = "stat"
	WidgetAlarms     = "alarms"
)

// widgetTypes is the set of valid widget types.
var widgetTypes = map[string]bool{
	WidgetTimeseries: true,
	WidgetStat:       true,
	WidgetAlarms:     true,
}

// Widget is one grid item: its layout (X/Y/W/H) plus the binding config
// that tells the frontend what data to render.
type Widget struct {
	// ID is the stable grid item key ("i" in react-grid-layout).
	ID string `json:"id"`
	// Type selects the widget renderer.
	Type string `json:"type"`
	// Title is an optional operator-supplied widget heading.
	Title string `json:"title,omitempty"`
	// DeviceID binds a data widget to one device.
	DeviceID string `json:"device_id,omitempty"`
	// MeasurementKey binds a data widget to one telemetry key.
	MeasurementKey string `json:"measurement_key,omitempty"`
	// Range selects the history window for a timeseries widget.
	Range string `json:"range,omitempty"`
	// X/Y/W/H are the grid position and size.
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// Dashboard is one persisted dashboard with its full layout.
type Dashboard struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenant_id"`
	Name      string    `json:"name"`
	SortOrder int       `json:"sort_order"`
	Layout    []Widget  `json:"layout"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Input carries the writable fields.
type Input struct {
	Name      string   `json:"name"`
	SortOrder int      `json:"sort_order"`
	Layout    []Widget `json:"layout"`
}

// Service provides dashboard persistence over one PostgreSQL pool.
type Service struct {
	pool *pgxpool.Pool
}

// New constructs a dashboard service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// List returns every dashboard of a tenant in tab order.
func (s *Service) List(ctx context.Context, tenantID string) ([]Dashboard, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, tenant_id, name, sort_order, layout, created_at, updated_at
		 FROM dashboard
		 WHERE tenant_id = $1
		 ORDER BY sort_order, name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list dashboards: %w", err)
	}
	defer rows.Close()

	dashboards := []Dashboard{}
	for rows.Next() {
		dash, err := scanDashboard(rows)
		if err != nil {
			return nil, err
		}
		dashboards = append(dashboards, dash)
	}
	return dashboards, rows.Err()
}

// Get returns one dashboard of a tenant.
func (s *Service) Get(ctx context.Context, tenantID, id string) (Dashboard, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, tenant_id, name, sort_order, layout, created_at, updated_at
		 FROM dashboard
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	dash, err := scanDashboard(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Dashboard{}, ErrNotFound
	}
	return dash, err
}

// Create validates, inserts and returns the new dashboard.
func (s *Service) Create(ctx context.Context, tenantID string, input Input) (Dashboard, error) {
	layout, err := normalizeInput(input)
	if err != nil {
		return Dashboard{}, err
	}
	id := uuid.NewString()
	now := time.Now()

	_, err = s.pool.Exec(ctx,
		`INSERT INTO dashboard (id, tenant_id, name, sort_order, layout, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $6)`,
		id, tenantID, strings.TrimSpace(input.Name), input.SortOrder, layout, now)
	if err != nil {
		return Dashboard{}, mapWriteError(err)
	}
	return s.Get(ctx, tenantID, id)
}

// Update replaces the writable fields of one dashboard.
func (s *Service) Update(ctx context.Context, tenantID, id string, input Input) (Dashboard, error) {
	layout, err := normalizeInput(input)
	if err != nil {
		return Dashboard{}, err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE dashboard
		 SET name = $3, sort_order = $4, layout = $5, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, strings.TrimSpace(input.Name), input.SortOrder, layout)
	if err != nil {
		return Dashboard{}, mapWriteError(err)
	}
	if tag.RowsAffected() == 0 {
		return Dashboard{}, ErrNotFound
	}
	return s.Get(ctx, tenantID, id)
}

// Delete removes one dashboard.
func (s *Service) Delete(ctx context.Context, tenantID, id string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM dashboard WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	if err != nil {
		return fmt.Errorf("delete dashboard: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// row abstracts pgx row scanning for reuse between Get and List.
type row interface {
	Scan(dest ...any) error
}

// scanDashboard reads one dashboard row, decoding the JSON layout.
func scanDashboard(r row) (Dashboard, error) {
	var (
		dash      Dashboard
		rawLayout []byte
	)
	if err := r.Scan(&dash.ID, &dash.TenantID, &dash.Name, &dash.SortOrder,
		&rawLayout, &dash.CreatedAt, &dash.UpdatedAt); err != nil {
		return Dashboard{}, err
	}
	if len(rawLayout) == 0 {
		dash.Layout = []Widget{}
	} else if err := json.Unmarshal(rawLayout, &dash.Layout); err != nil {
		return Dashboard{}, fmt.Errorf("decode dashboard layout: %w", err)
	}
	return dash, nil
}

// normalizeInput validates and encodes the writable fields.
func normalizeInput(input Input) ([]byte, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrInvalidArgument)
	}
	layout := input.Layout
	if layout == nil {
		layout = []Widget{}
	}
	for i := range layout {
		if !widgetTypes[layout[i].Type] {
			return nil, fmt.Errorf("%w: unknown widget type %q",
				ErrInvalidArgument, layout[i].Type)
		}
		if layout[i].ID == "" {
			return nil, fmt.Errorf("%w: widget on position %d is missing an id",
				ErrInvalidArgument, i)
		}
	}
	raw, err := json.Marshal(layout)
	if err != nil {
		return nil, fmt.Errorf("encode dashboard layout: %w", err)
	}
	return raw, nil
}

// mapWriteError converts a pg write error to the package sentinel errors.
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrConflict
	}
	return err
}
