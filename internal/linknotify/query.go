// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// query.go exposes the read side of the link episode history: a typed
// paged query backing the gateway detail Link Health tab. The write side
// (OpenEpisode/CloseEpisode/ApplyEdgeEpisode) intentionally stays in
// store.go; this file only assembles operator-facing rows.

package linknotify

import (
	"context"
	"fmt"
	"time"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// EpisodeRow is one link outage row in the history view.
type EpisodeRow struct {
	// EpisodeID is the persisted episode identifier.
	EpisodeID string `json:"episode_id"`

	// GatewayID is the gateway whose link dropped.
	GatewayID string `json:"gateway_id"`

	// GatewayName is the canonical-name snapshot so the UI need not
	// resolve the identifier separately.
	GatewayName string `json:"gateway_name"`

	// Source is lease (server lease detector) or edge (edge watchdog,
	// whether it corrected a lease row or was inserted alone).
	Source string `json:"source"`

	// StartedAt is the outage start as RFC3339 UTC.
	StartedAt string `json:"started_at"`

	// EndedAt is the recovery time as RFC3339 UTC; empty for an
	// outage still open.
	EndedAt string `json:"ended_at,omitempty"`

	// DownReason is the recorded reason text (lease expiry, edge
	// reason, etc.).
	DownReason string `json:"down_reason"`

	// EdgeDownAt is the edge ground-truth down time when present.
	EdgeDownAt string `json:"edge_down_at,omitempty"`

	// AlarmID links to the alarm ledger; empty for alarm-free
	// sub-lease micro-flaps.
	AlarmID string `json:"alarm_id,omitempty"`
}

// EpisodeFilter narrows the episode history query. Zero-valued fields
// disable the corresponding predicate.
type EpisodeFilter struct {
	// GatewayID limits rows to one gateway.
	GatewayID string

	// Source limits rows to lease or edge.
	Source string

	// Start matches episodes started at or after this instant.
	Start time.Time

	// End matches episodes started before this instant.
	End time.Time
}

// ListEpisodes returns one page of episode rows, newest first.
//
// Episodes carry a gateway_id but no ownership columns, so they are narrowed
// by the caller's rbac.DataScope exactly like alarm rows: a scoped member
// must not learn about outages of gateways it cannot see. The count query is
// narrowed identically, or the total alone would leak the tenant-wide number.
func (s *Store) ListEpisodes(ctx context.Context, tenantID string, filter EpisodeFilter, page, pageSize int) (apipage.Page[EpisodeRow], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("gateway_id", 6)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*)
		 FROM gateway_link_episode
		 WHERE tenant_id = $1
		   AND ($2 = '' OR gateway_id = $2::uuid)
		   AND ($3 = '' OR source = $3)
		   AND ($4::timestamptz IS NULL OR started_at >= $4)
		   AND ($5::timestamptz IS NULL OR started_at < $5)`+countClause,
		append([]any{
			tenantID, filter.GatewayID, filter.Source,
			nullableTime(filter.Start), nullableTime(filter.End),
		}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[EpisodeRow]{}, fmt.Errorf("count link episodes: %w", err)
	}

	listClause, listArgs := scope.DeviceSubqueryPredicate("e.gateway_id", 8)
	rows, err := s.pool.Query(ctx,
		`SELECT e.id::text, e.gateway_id::text, d.canonical_name, e.source,
		        e.started_at, e.ended_at, e.down_reason, e.edge_down_at,
		        e.alarm_id::text
		 FROM gateway_link_episode e
		 JOIN device d ON d.id = e.gateway_id
		 WHERE e.tenant_id = $1
		   AND ($2 = '' OR e.gateway_id = $2::uuid)
		   AND ($3 = '' OR e.source = $3)
		   AND ($4::timestamptz IS NULL OR e.started_at >= $4)
		   AND ($5::timestamptz IS NULL OR e.started_at < $5)`+listClause+`
		 ORDER BY e.started_at DESC, e.id
		 LIMIT $6 OFFSET $7`,
		append([]any{
			tenantID, filter.GatewayID, filter.Source,
			nullableTime(filter.Start), nullableTime(filter.End),
			pageSize, (page - 1) * pageSize,
		}, listArgs...)...)
	if err != nil {
		return apipage.Page[EpisodeRow]{}, fmt.Errorf("list link episodes: %w", err)
	}
	defer rows.Close()

	result := []EpisodeRow{}
	for rows.Next() {
		var row EpisodeRow
		var startedAt time.Time
		var endedAt, edgeDownAt *time.Time
		var alarmID *string
		if err := rows.Scan(&row.EpisodeID, &row.GatewayID, &row.GatewayName,
			&row.Source, &startedAt, &endedAt, &row.DownReason,
			&edgeDownAt, &alarmID); err != nil {
			return apipage.Page[EpisodeRow]{}, fmt.Errorf("scan link episode row: %w", err)
		}
		row.StartedAt = startedAt.UTC().Format(time.RFC3339)
		if endedAt != nil {
			row.EndedAt = endedAt.UTC().Format(time.RFC3339)
		}
		if edgeDownAt != nil {
			row.EdgeDownAt = edgeDownAt.UTC().Format(time.RFC3339)
		}
		if alarmID != nil {
			row.AlarmID = *alarmID
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[EpisodeRow]{}, err
	}
	return apipage.New(result, total, page, pageSize), nil
}

// nullableTime maps a zero time to a typed NULL so the SQL predicate can
// treat it as "window disabled" without pgx rejecting an untyped nil.
func nullableTime(value time.Time) *time.Time {
	if value.IsZero() {
		return nil
	}
	return &value
}
