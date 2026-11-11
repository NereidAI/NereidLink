// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// history.go implements paged historical telemetry lookup for the device
// detail page. Rows come from the same TimescaleDB hypertable the writer
// populates; the query uses the device/time index and a bounded time window.

package telemetry

import (
	"context"
	"fmt"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// HistoryPoint is one stored telemetry row.
type HistoryPoint struct {
	// Time is the event time as RFC3339 UTC.
	Time string `json:"time"`

	// MeasurementKey identifies the measurement.
	MeasurementKey string `json:"measurement_key"`

	// VDouble is the engineering value.
	VDouble *float64 `json:"v_double"`

	// RawDouble is the unconverted value when present.
	RawDouble *float64 `json:"raw_value"`

	// Unit is the engineering unit.
	Unit string `json:"unit"`

	// DescriptorVersion is the descriptor version applied to the row.
	DescriptorVersion *int `json:"descriptor_version"`

	// VString carries string-valued measurements.
	VString string `json:"v_string,omitempty"`

	// VBool carries boolean-valued measurements.
	VBool *bool `json:"v_bool,omitempty"`

	// Quality is the measurement quality.
	Quality string `json:"quality"`
}

// History returns one page of telemetry of a device within a time window,
// optionally restricted to one measurement key, newest first.
//
// The requested device is checked against the caller's rbac.DataScope before
// any row is read. Telemetry is the most sensitive device-derived data in the
// platform, so it is narrowed on the device itself rather than on each
// measurement row.
func (s *Server) History(ctx context.Context, tenantID, deviceID, key string, start, end time.Time, page, pageSize int) (apipage.Page[HistoryPoint], error) {
	filter := `tenant_id = $1 AND device_id = $2
	           AND ($3 = '' OR measurement_key = $3)
	           AND time >= $4 AND time <= $5`

	// The device visibility gate is a bound predicate rather than a separate
	// round trip: it keeps the check in the same statement as the data, so a
	// device that is moved out of the caller's scope cannot be read in the
	// window between an existence check and the read.
	scope := rbac.ScopeFromContext(ctx)
	gate, gateArgs := scope.DeviceSubqueryPredicate("device_id", 5)

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ts_telemetry WHERE `+filter+gate,
		append([]any{tenantID, deviceID, key, start, end}, gateArgs...)...).
		Scan(&total); err != nil {
		return apipage.Page[HistoryPoint]{}, fmt.Errorf("count telemetry: %w", err)
	}

	listGate, listArgs := scope.DeviceSubqueryPredicate("device_id", 7)
	rows, err := s.pool.Query(ctx,
		`SELECT time, measurement_key, v_double, raw_value, unit,
		        descriptor_version, v_string, v_bool, quality
		 FROM ts_telemetry
		 WHERE `+filter+listGate+`
		 ORDER BY time DESC
		 LIMIT $6 OFFSET $7`,
		append([]any{tenantID, deviceID, key, start, end,
			pageSize, (page - 1) * pageSize}, listArgs...)...)
	if err != nil {
		return apipage.Page[HistoryPoint]{}, fmt.Errorf("list telemetry: %w", err)
	}
	defer rows.Close()

	points := []HistoryPoint{}
	for rows.Next() {
		var point HistoryPoint
		var pointTime time.Time
		var vDouble, rawDouble *float64
		var descriptor *int
		var vString *string
		var vBool *bool
		if err := rows.Scan(&pointTime, &point.MeasurementKey, &vDouble,
			&rawDouble, &point.Unit, &descriptor, &vString, &vBool,
			&point.Quality); err != nil {
			return apipage.Page[HistoryPoint]{}, fmt.Errorf("scan telemetry row: %w", err)
		}
		point.Time = pointTime.UTC().Format(time.RFC3339)
		point.VDouble = vDouble
		point.RawDouble = rawDouble
		point.DescriptorVersion = descriptor
		if vString != nil {
			point.VString = *vString
		}
		point.VBool = vBool
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[HistoryPoint]{}, err
	}
	return apipage.New(points, total, page, pageSize), nil
}

// KeySummary describes the most recent value of one measurement key.
type KeySummary struct {
	// MeasurementKey identifies the measurement.
	MeasurementKey string `json:"measurement_key"`

	// ValueType is the carrier of the latest value: number|boolean|string.
	ValueType string `json:"value_type"`

	// Unit is the engineering unit when present.
	Unit string `json:"unit,omitempty"`

	// Time of the latest value as RFC3339 UTC.
	Time string `json:"time"`

	// VDouble carries the latest numeric value.
	VDouble *float64 `json:"v_double,omitempty"`

	// VBool carries the latest boolean value.
	VBool *bool `json:"v_bool,omitempty"`

	// VString carries the latest string value.
	VString string `json:"v_string,omitempty"`

	// Quality is the measurement quality of the latest row.
	Quality string `json:"quality"`
}

// Keys returns the most recent row of every measurement key a device
// reported within the window, newest key first. It backs both the
// measurement selector and the latest-telemetry strip on device detail.
func (s *Server) Keys(ctx context.Context, tenantID, deviceID string, start, end time.Time) ([]KeySummary, error) {
	// The key strip is device-derived, so it inherits device visibility even
	// though it returns measurement names rather than a device row. Without
	// this a crafted device id still leaks which keys another customer's
	// device reports.
	keyScope := rbac.ScopeFromContext(ctx)
	keyGate, keyGateArgs := keyScope.DeviceSubqueryPredicate("device_id", 4)
	rows, err := s.pool.Query(ctx,
		`SELECT measurement_key, time, v_double, v_bool, v_string, unit, quality
		 FROM (
		     SELECT DISTINCT ON (measurement_key)
		            measurement_key, time, v_double, v_bool, v_string, unit, quality
		     FROM ts_telemetry
		     WHERE tenant_id = $1 AND device_id = $2
		       AND time >= $3 AND time <= $4`+keyGate+`
		     ORDER BY measurement_key, time DESC
		 ) latest
		 ORDER BY time DESC`,
		append([]any{tenantID, deviceID, start, end}, keyGateArgs...)...)
	if err != nil {
		return nil, fmt.Errorf("list telemetry keys: %w", err)
	}
	defer rows.Close()

	keys := []KeySummary{}
	for rows.Next() {
		var summary KeySummary
		var pointTime time.Time
		var vDouble *float64
		var vBool *bool
		var vString *string
		if err := rows.Scan(&summary.MeasurementKey, &pointTime, &vDouble,
			&vBool, &vString, &summary.Unit, &summary.Quality); err != nil {
			return nil, fmt.Errorf("scan telemetry key: %w", err)
		}
		summary.Time = pointTime.UTC().Format(time.RFC3339)
		switch {
		case vBool != nil:
			summary.ValueType = "boolean"
			summary.VBool = vBool
		case vString != nil && *vString != "":
			summary.ValueType = "string"
			summary.VString = *vString
		default:
			summary.ValueType = "number"
			summary.VDouble = vDouble
		}
		keys = append(keys, summary)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}
