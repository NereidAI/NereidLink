// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_page.go provides the typed paged alarm query used by the alarm
// center. It filters by device, alarm type and assignee, and supports a
// severity filter plus a compound "LIVE" state matching ACTIVE and ACK.

package alarm

import (
	"context"
	"fmt"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// Row is one typed alarm audit row.
type Row struct {
	// AlarmID is the alarm identifier.
	AlarmID string `json:"alarm_id"`

	// DeviceID is the alarm originator.
	DeviceID string `json:"device_id"`

	// DeviceName is the display name (alias, else canonical name) of the
	// originator, so the alarm center can show a readable device column.
	DeviceName string `json:"device_name,omitempty"`

	// AlarmType classifies the alarm.
	AlarmType string `json:"alarm_type"`

	// Severity is CRITICAL, MAJOR, MINOR or WARNING.
	Severity string `json:"severity"`

	// State is ACTIVE, ACK or CLEARED.
	State string `json:"state"`

	// DedupeKey identifies the open alarm.
	DedupeKey string `json:"dedupe_key"`

	// Details is the raw JSON detail document when present.
	Details string `json:"details,omitempty"`

	// StartedAt is the creation time as RFC3339 UTC.
	StartedAt string `json:"started_at"`

	// AckedAt is the acknowledgment time when present.
	AckedAt string `json:"acked_at,omitempty"`

	// ClearedAt is the clearance time when present.
	ClearedAt string `json:"cleared_at,omitempty"`

	// AssigneeID is the currently responsible user when present.
	AssigneeID string `json:"assignee_id,omitempty"`

	// AssigneeName is the display snapshot used by the UI.
	AssigneeName string `json:"assignee_name,omitempty"`
}

// matchState renders the state predicate for the bound state parameter.
// The compound values LIVE and OPEN match ACTIVE and ACK; an empty value
// disables filtering. stateCol is the qualified alarm state column; it must be
// qualified because ListPage joins the device table, which also has a state
// column, leaving a bare "state" reference ambiguous.
func matchState(param, stateCol string) string {
	return fmt.Sprintf(
		`(%[1]s = '' OR ((%[1]s IN ('LIVE','OPEN')) AND %[2]s IN ('ACTIVE','ACK'))
		 OR %[2]s = %[1]s)`, param, stateCol)
}

// assigneePredicate renders the assignee filter for the bound parameter. An
// empty value disables the axis; the literal "none" matches unassigned alarms;
// any other value is treated as an assignee user UUID. The column is qualified
// because the query joins several tables.
func assigneePredicate(param string) string {
	return fmt.Sprintf(
		`(%[1]s = '' OR (%[1]s = 'none' AND a.assignee_id IS NULL)
		 OR (%[1]s <> 'none' AND a.assignee_id = %[1]s::uuid))`, param)
}

// Filter is the alarm-center query filter. Empty fields disable that axis.
type Filter struct {
	// DeviceID narrows to one originator.
	DeviceID string
	// AlarmType narrows to one alarm type (exact match).
	AlarmType string
	// AssigneeID narrows by current assignee; the special value "none"
	// matches unassigned alarms.
	AssigneeID string
	// State accepts ACTIVE, ACK, CLEARED, or the compound LIVE.
	State string
	// Severity accepts CRITICAL, MAJOR, MINOR or WARNING.
	Severity string
}

// ListPage returns one page of alarm rows, newest first.
//
// An alarm carries a device_id but no ownership columns, so it is narrowed by
// the caller's rbac.DataScope through device membership: a scoped member must
// not learn about alarms raised by devices it cannot see. The count query is
// narrowed identically, or the total alone would leak the tenant-wide number.
func (s *Service) ListPage(ctx context.Context, tenantID string, filter Filter, page, pageSize int) (apipage.Page[Row], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("a.device_id", 6)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM alarm a
		 WHERE a.tenant_id = $1
		   AND ($2 = '' OR a.device_id = $2::uuid)
		   AND `+matchState("$3", "a.state")+`
		   AND ($4 = '' OR a.severity = $4)
		   AND ($5 = '' OR a.type = $5)
		   AND `+assigneePredicate("$6")+countClause,
		append([]any{tenantID, filter.DeviceID, filter.State, filter.Severity,
			filter.AlarmType, filter.AssigneeID}, countArgs...)...).
		Scan(&total); err != nil {
		return apipage.Page[Row]{}, fmt.Errorf("count alarms: %w", err)
	}

	listClause, listArgs := scope.DeviceSubqueryPredicate("a.device_id", 8)
	rows, err := s.pool.Query(ctx,
		`SELECT a.id::text, a.device_id::text, a.type, a.severity, a.state,
		        a.dedupe_key, a.details::text, a.started_at, a.acked_at,
		        a.cleared_at, a.assignee_id::text,
		        COALESCE(assignee.display_name, assignee.email),
		        COALESCE(NULLIF(d.alias, ''), d.canonical_name)
		 FROM alarm a
		 LEFT JOIN app_user assignee ON assignee.id = a.assignee_id
		 LEFT JOIN device d ON d.id = a.device_id
		 WHERE a.tenant_id = $1
		   AND ($2 = '' OR a.device_id = $2::uuid)
		   AND `+matchState("$3", "a.state")+`
		   AND ($4 = '' OR a.severity = $4)
		   AND ($5 = '' OR a.type = $5)
		   AND `+assigneePredicate("$6")+listClause+`
		 ORDER BY a.started_at DESC
		 LIMIT $7 OFFSET $8`,
		append([]any{tenantID, filter.DeviceID, filter.State, filter.Severity,
			filter.AlarmType, filter.AssigneeID,
			pageSize, (page - 1) * pageSize}, listArgs...)...)
	if err != nil {
		return apipage.Page[Row]{}, fmt.Errorf("list alarms: %w", err)
	}
	defer rows.Close()

	result := []Row{}
	for rows.Next() {
		var row Row
		var details *string
		var startedAt time.Time
		var ackedAt, clearedAt *time.Time
		var assigneeID, assigneeName, deviceName *string
		if err := rows.Scan(&row.AlarmID, &row.DeviceID, &row.AlarmType,
			&row.Severity, &row.State, &row.DedupeKey, &details,
			&startedAt, &ackedAt, &clearedAt, &assigneeID, &assigneeName,
			&deviceName); err != nil {
			return apipage.Page[Row]{}, fmt.Errorf("scan alarm row: %w", err)
		}
		if deviceName != nil {
			row.DeviceName = *deviceName
		}
		row.StartedAt = startedAt.UTC().Format(time.RFC3339)
		if details != nil {
			row.Details = *details
		}
		if ackedAt != nil {
			row.AckedAt = ackedAt.UTC().Format(time.RFC3339)
		}
		if clearedAt != nil {
			row.ClearedAt = clearedAt.UTC().Format(time.RFC3339)
		}
		if assigneeID != nil {
			row.AssigneeID = *assigneeID
		}
		if assigneeName != nil {
			row.AssigneeName = *assigneeName
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[Row]{}, err
	}
	return apipage.New(result, total, page, pageSize), nil
}
