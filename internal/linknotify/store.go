// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists link-down episodes, the escalation claim and the
// once-per-day digest cursor (migration 0039).

package linknotify

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// edgeMatchTolerance widens interval-overlap matching between a server lease
// episode and an edge episode. Lease DOWN starts up to ~90s after the real
// drop, and the lease recovery lands within seconds of the edge up time, so
// two minutes covers clock and lease skew without merging distinct bursts.
const edgeMatchTolerance = 2 * time.Minute

// ErrConcurrentApply is returned when an edge episode row locked by another
// transaction cannot be applied; callers may retry.
var ErrConcurrentApply = errors.New("edge episode apply conflict")

// Store is the PostgreSQL-backed EpisodeStore and DigestStore.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a Store.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// OpenEpisode inserts an unplanned outage. A partial unique index makes a
// second open row for the same gateway a no-op, so duplicate DOWN advisories
// never create parallel episodes.
func (s *Store) OpenEpisode(ctx context.Context, ep Episode) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO gateway_link_episode
		    (tenant_id, gateway_id, started_at, down_reason, alarm_id)
		 VALUES ($1,$2,$3,$4,$5)
		 ON CONFLICT (gateway_id) WHERE ended_at IS NULL DO NOTHING`,
		ep.TenantID, ep.GatewayID, ep.StartedAt, ep.Reason, ep.AlarmID)
	if err != nil {
		return fmt.Errorf("open link episode: %w", err)
	}
	return nil
}

// CloseEpisode ends the open episode for a gateway. A missing or already
// closed episode is a no-op.
func (s *Store) CloseEpisode(ctx context.Context, gatewayID string, at time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE gateway_link_episode SET ended_at = $2
		 WHERE gateway_id = $1 AND ended_at IS NULL`,
		gatewayID, at)
	if err != nil {
		return fmt.Errorf("close link episode: %w", err)
	}
	return nil
}

// ApplyEdgeEpisode records an edge-local link watchdog episode. It runs in
// one transaction:
//
//  1. When (gateway, DownAt) already has an edge-marked row the report is a
//     duplicate; retransmissions and backlog replay cost nothing.
//  2. Otherwise the lease episode whose interval overlaps the edge interval
//     within edgeMatchTolerance is corrected in place with the edge ground
//     truth and marked source='edge'; an open row is closed at UpAt.
//  3. With no matching lease episode (typically a sub-90s flap the lease
//     could not see) a new already-closed episode is inserted with no alarm,
//     so it counts in the 24h digest without paging anyone.
func (s *Store) ApplyEdgeEpisode(ctx context.Context, ep EdgeEpisode) (ApplyResult, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ApplyResult{}, fmt.Errorf("begin edge episode: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT TRUE FROM gateway_link_episode
		 WHERE gateway_id = $1 AND edge_down_at = $2`,
		ep.GatewayID, ep.DownAt).Scan(&exists); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, fmt.Errorf("check edge episode duplicate: %w", err)
	}
	if exists {
		return ApplyResult{Status: ApplyDuplicate}, nil
	}

	var matchedID string
	err = tx.QueryRow(ctx,
		`SELECT id::text FROM gateway_link_episode
		 WHERE gateway_id = $1 AND source = $2
		   AND tstzrange(started_at, ended_at, '[]')
		       && tstzrange($3, $4, '[]')
		 ORDER BY abs(extract(epoch FROM (started_at - $5)))
		 LIMIT 1
		 FOR UPDATE`,
		ep.GatewayID, EpisodeSourceLease,
		ep.DownAt.Add(-edgeMatchTolerance), ep.UpAt.Add(edgeMatchTolerance),
		ep.DownAt).Scan(&matchedID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ApplyResult{}, fmt.Errorf("match lease episode: %w", err)
	}

	if matchedID != "" {
		cmdTag, err := tx.Exec(ctx,
			`UPDATE gateway_link_episode
			    SET started_at = $2, ended_at = $3, source = $4,
			        edge_down_at = $5, down_reason = $6
			  WHERE id = $1`,
			matchedID, ep.DownAt, ep.UpAt, EpisodeSourceEdge,
			ep.DownAt, ep.Reason)
		if err != nil {
			return ApplyResult{}, fmt.Errorf("correct lease episode: %w", err)
		}
		if cmdTag.RowsAffected() != 1 {
			return ApplyResult{}, ErrConcurrentApply
		}
		if err := tx.Commit(ctx); err != nil {
			return ApplyResult{}, fmt.Errorf("commit corrected episode: %w", err)
		}
		return ApplyResult{Status: ApplyAccepted, MatchedLease: true}, nil
	}

	cmdTag, err := tx.Exec(ctx,
		`INSERT INTO gateway_link_episode
		    (tenant_id, gateway_id, started_at, ended_at, down_reason,
		     source, edge_down_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)
		 ON CONFLICT (gateway_id, edge_down_at)
		 WHERE edge_down_at IS NOT NULL DO NOTHING`,
		ep.TenantID, ep.GatewayID, ep.DownAt, ep.UpAt, ep.Reason,
		EpisodeSourceEdge, ep.DownAt)
	if err != nil {
		return ApplyResult{}, fmt.Errorf("insert edge episode: %w", err)
	}
	if cmdTag.RowsAffected() == 0 {
		return ApplyResult{Status: ApplyDuplicate}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return ApplyResult{}, fmt.Errorf("commit edge episode: %w", err)
	}
	return ApplyResult{Status: ApplyAccepted, MatchedLease: false}, nil
}

// ClaimDueEpisodes atomically marks every open, not-yet-escalated episode
// older than cutoff as escalated and returns the claimed rows. Claiming in
// the same UPDATE that selects makes the bump at-most-once across restarts
// and queue-group failover.
func (s *Store) ClaimDueEpisodes(ctx context.Context, now, cutoff time.Time) ([]Episode, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE gateway_link_episode
		 SET escalated = TRUE, escalated_at = $1
		 WHERE id IN (
		   SELECT id FROM gateway_link_episode
		   WHERE ended_at IS NULL AND escalated = FALSE
		     AND started_at <= $2
		 )
		 RETURNING id::text, tenant_id::text, gateway_id::text,
		           started_at, down_reason, alarm_id::text`,
		now, cutoff)
	if err != nil {
		return nil, fmt.Errorf("claim due episodes: %w", err)
	}
	defer rows.Close()

	var out []Episode
	for rows.Next() {
		var ep Episode
		if err := rows.Scan(&ep.ID, &ep.TenantID, &ep.GatewayID,
			&ep.StartedAt, &ep.Reason, &ep.AlarmID); err != nil {
			return nil, fmt.Errorf("scan claimed episode: %w", err)
		}
		out = append(out, ep)
	}
	return out, rows.Err()
}

// DigestRow summarizes one gateway's outages in the digest window.
type DigestRow struct {
	GatewayID     string
	GatewayName   string
	Count         int
	TotalDowntime time.Duration
	FirstDown     time.Time
	LastUp        time.Time
}

// dayBounds returns the half-open UTC interval [day, day+24h).
func dayBounds(day time.Time) (time.Time, time.Time) {
	start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.Add(24 * time.Hour)
}

// TenantsWithEndedEpisodes lists tenants that have at least one episode that
// ended during the given UTC day, so the digest only targets affected
// tenants.
func (s *Store) TenantsWithEndedEpisodes(ctx context.Context, day time.Time) ([]string, error) {
	start, end := dayBounds(day)
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT tenant_id::text FROM gateway_link_episode
		 WHERE ended_at >= $1 AND ended_at < $2
		 ORDER BY 1`, start, end)
	if err != nil {
		return nil, fmt.Errorf("list digest tenants: %w", err)
	}
	defer rows.Close()
	var tenants []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		tenants = append(tenants, id)
	}
	return tenants, rows.Err()
}

// DigestRows aggregates one tenant's ended episodes for the UTC day by
// gateway. Episodes survive only while the gateway device exists (FK
// cascade), so the join to device is always satisfiable.
func (s *Store) DigestRows(ctx context.Context, tenantID string, day time.Time) ([]DigestRow, error) {
	start, end := dayBounds(day)
	rows, err := s.pool.Query(ctx,
		`SELECT e.gateway_id::text, d.canonical_name,
		        count(*) AS episodes,
		        coalesce(sum(extract(epoch FROM (e.ended_at - e.started_at))), 0) AS down_s,
		        min(e.started_at), max(e.ended_at)
		 FROM gateway_link_episode e
		 JOIN device d ON d.id = e.gateway_id
		 WHERE e.tenant_id = $1 AND e.ended_at >= $2 AND e.ended_at < $3
		 GROUP BY e.gateway_id, d.canonical_name
		 ORDER BY down_s DESC`, tenantID, start, end)
	if err != nil {
		return nil, fmt.Errorf("load digest rows: %w", err)
	}
	defer rows.Close()

	var out []DigestRow
	for rows.Next() {
		var r DigestRow
		var downSeconds float64
		if err := rows.Scan(&r.GatewayID, &r.GatewayName, &r.Count,
			&downSeconds, &r.FirstDown, &r.LastUp); err != nil {
			return nil, fmt.Errorf("scan digest row: %w", err)
		}
		r.TotalDowntime = time.Duration(downSeconds) * time.Second
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkDigestSent records that a tenant's digest for the UTC day was sent. It
// returns false when a row already exists, making the daily send idempotent.
func (s *Store) MarkDigestSent(ctx context.Context, tenantID string, day, at time.Time) (bool, error) {
	date := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO gateway_link_digest (tenant_id, digest_date, sent_at)
		 VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		tenantID, date, at)
	if err != nil {
		return false, fmt.Errorf("mark digest sent: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// AdminEmails resolves active tenant administrators' email addresses,
// mirroring the escalation runner's role-recipient query.
func (s *Store) AdminEmails(ctx context.Context, tenantID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT u.email
		 FROM app_user AS u
		 JOIN tenant_membership AS m ON m.user_id = u.id
		 JOIN membership_role AS r ON r.membership_id = m.id
		 WHERE m.tenant_id = $1 AND r.role = 'TENANT_ADMIN'
		   AND NOT u.disabled AND u.email <> ''`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("resolve admin emails: %w", err)
	}
	defer rows.Close()
	emails := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		emails = append(emails, email)
	}
	return emails, rows.Err()
}
