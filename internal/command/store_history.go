// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_history.go implements paged command history lookup for the device
// detail page and the tenant-wide command center. It reads the same
// authoritative command table that the lifecycle transitions write.

package command

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nereidai/nereidlink/internal/apipage"
)

// Record is one command audit row.
type Record struct {
	// ID is the command identifier.
	ID string `json:"id"`

	// DeviceID is the command target device.
	DeviceID string `json:"device_id"`

	// Name is the catalog command name.
	Name string `json:"name"`

	// Interaction is one-way or request-response.
	Interaction string `json:"interaction"`

	// Timeliness is realtime or persistent.
	Timeliness string `json:"timeliness"`

	// Direction is read, write or empty for legacy commands.
	Direction string `json:"direction,omitempty"`

	// Params is the raw JSON parameter document.
	Params string `json:"params"`

	// State is the lifecycle status.
	State string `json:"state"`

	// Response carries the raw JSON device response when present.
	Response string `json:"response,omitempty"`

	// Error carries the machine-readable failure detail.
	Error string `json:"error,omitempty"`

	// CreatedAt is the platform creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// DeliveredAt is the delivery time when present.
	DeliveredAt string `json:"delivered_at,omitempty"`

	// FinishedAt is the completion time when present.
	FinishedAt string `json:"finished_at,omitempty"`
}

// inflightStates are the non-terminal states represented by the special
// "INFLIGHT" filter value used by the command center.
const inflightStates = "('PENDING', 'CLAIMED', 'QUEUED')"

// History returns one page of commands for a device, optionally filtered
// by lifecycle state, newest first.
func (s *Store) History(ctx context.Context, tenantID, deviceID, state string, page, pageSize int) (apipage.Page[Record], error) {
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM command
		 WHERE tenant_id = $1 AND device_id = $2
		   AND ($3 = '' OR state = $3)`,
		tenantID, deviceID, state).Scan(&total); err != nil {
		return apipage.Page[Record]{}, fmt.Errorf("count commands: %w", err)
	}

	rows, err := s.pool.Query(ctx, commandSelect+`
		 WHERE command.tenant_id = $1 AND command.device_id = $2
		   AND ($3 = '' OR command.state = $3)
		 ORDER BY command.created_at DESC
		 LIMIT $4 OFFSET $5`,
		tenantID, deviceID, state, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[Record]{}, fmt.Errorf("list commands: %w", err)
	}
	defer rows.Close()

	records, err := scanRecords(rows)
	if err != nil {
		return apipage.Page[Record]{}, err
	}
	return apipage.New(records, total, page, pageSize), nil
}

// ListTenant returns one page of commands across every device of a tenant.
// The optional filters are a device name/alias partial match, command name
// and state. The state filter accepts the special value "INFLIGHT" matching
// every non-terminal state. Records are returned newest first.
func (s *Store) ListTenant(ctx context.Context, tenantID, deviceQuery, name, state string, page, pageSize int) (apipage.Page[Record], error) {
	// The device filter is matched by canonical name or alias, not by the
	// opaque UUID, because operators identify devices by name. Join the
	// device table and apply a case-insensitive partial predicate.
	deviceJoin := `
		 JOIN device dev ON dev.id = command.device_id`
	deviceWhere := `
		   AND ($2 = '' OR dev.canonical_name ILIKE '%' || $2 || '%'
		        OR dev.alias ILIKE '%' || $2 || '%')`

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM command`+deviceJoin+`
		 WHERE command.tenant_id = $1`+deviceWhere+`
		   AND ($3 = '' OR command.name = $3)
		   AND ($4 = '' OR ($4 = 'INFLIGHT' AND command.state IN `+inflightStates+`)
		        OR command.state = $4)`,
		tenantID, deviceQuery, name, state).Scan(&total); err != nil {
		return apipage.Page[Record]{}, fmt.Errorf("count tenant commands: %w", err)
	}

	rows, err := s.pool.Query(ctx, commandSelect+deviceJoin+`
		 WHERE command.tenant_id = $1`+deviceWhere+`
		   AND ($3 = '' OR command.name = $3)
		   AND (($4 = '' OR ($4 = 'INFLIGHT' AND command.state IN `+inflightStates+`))
		        OR command.state = $4)
		 ORDER BY command.created_at DESC
		 LIMIT $5 OFFSET $6`,
		tenantID, deviceQuery, name, state, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[Record]{}, fmt.Errorf("list tenant commands: %w", err)
	}
	defer rows.Close()

	records, err := scanRecords(rows)
	if err != nil {
		return apipage.Page[Record]{}, err
	}
	return apipage.New(records, total, page, pageSize), nil
}

// commandSelect is the shared column list used by command queries.
// Columns are qualified by the command alias because callers join the
// device table for name filtering.
var commandSelect = `
		 SELECT command.id::text, command.device_id::text, command.name,
		        command.interaction, command.timeliness,
		        command.params::text, command.state, command.response::text,
		        command.error, command.created_at,
		        command.delivered_at, command.finished_at, command.direction
		 FROM command`

// scanRecords maps command rows into audit Records.
func scanRecords(rows pgx.Rows) ([]Record, error) {
	records := []Record{}
	for rows.Next() {
		var record Record
		var params string
		var response *string
		var failure *string
		var createdAt time.Time
		var deliveredAt, finishedAt *time.Time
		if err := rows.Scan(&record.ID, &record.DeviceID, &record.Name,
			&record.Interaction, &record.Timeliness, &params, &record.State,
			&response, &failure, &createdAt, &deliveredAt, &finishedAt,
			&record.Direction); err != nil {
			return nil, fmt.Errorf("scan command row: %w", err)
		}
		record.Params = params
		if response != nil {
			record.Response = *response
		}
		if failure != nil {
			record.Error = *failure
		}
		record.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		if deliveredAt != nil {
			record.DeliveredAt = deliveredAt.UTC().Format(time.RFC3339)
		}
		if finishedAt != nil {
			record.FinishedAt = finishedAt.UTC().Format(time.RFC3339)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
