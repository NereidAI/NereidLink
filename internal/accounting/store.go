// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists and queries the ADR-0025 accounting Top-N hypertable.
//
// Write path: the aggregator consumes per-node reports and appends their
// (already bounded) candidate rows. Read path: the diagnostic API asks
// for "one dimension, one window, top N" and gets it from a single indexed
// scan.
//
// The store is intentionally dumb: it does no heavy-hitter math. Merging
// happens in the aggregator because the merge must be per node/stream,
// and querying must be able to return raw rows for trend views without a
// lossy pre-aggregation step.

package accounting

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrUnknownDimension is returned when a caller asks for a dimension the
// accounting model does not define. It is a programming error, not a user
// input error, so it is surfaced as an error rather than an empty result.
var ErrUnknownDimension = errors.New("unknown accounting dimension")

// Store reads and writes the accounting hypertable.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore builds a Store over the pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Row is one persisted accounting candidate.
type Row struct {
	// Time is the window end the row was recorded for.
	Time time.Time
	// NodeID identifies the reporting node.
	NodeID string
	// Dimension is the accounting dimension.
	Dimension Dimension
	// DimensionValue is the accounted value.
	DimensionValue string
	// MessageCount is the estimated message count.
	MessageCount int64
	// ByteCount is the estimated byte volume.
	ByteCount int64
	// SampleRate is the 1/N divisor the estimate was scaled by.
	SampleRate int64
	// WindowSeconds is the reporting window length.
	WindowSeconds int
}

// AppendReport persists one node report. Rows are written in a single
// statement so a report is atomic: a partially stored report would show a
// node as less busy than it is, which is the direction an operator must
// never be misled in.
//
// Unsupported dimensions are skipped rather than rejected. The sampler's
// dimension set is fixed and validated in-process, so a stored row can
// only be invalid if the database constraint is stricter than the code; in
// that case dropping the offending row keeps ingest alive.
func (s *Store) AppendReport(ctx context.Context, report Report) (int, error) {
	if len(report.Dimensions) == 0 {
		return 0, nil
	}
	if report.NodeID == "" {
		return 0, errors.New("accounting: report has empty node id")
	}
	windowSeconds := int(report.WindowEnd.Sub(report.WindowStart).Seconds())
	if windowSeconds <= 0 {
		windowSeconds = 1
	}
	rowTime := report.WindowEnd

	batch := &pgx.Batch{}
	queued := 0
	for _, d := range dimensions {
		entries := report.Dimensions[string(d)]
		if len(entries) == 0 {
			continue
		}
		for _, e := range entries {
			if e.Value == "" {
				continue
			}
			batch.Queue(`
				INSERT INTO accounting_topn
					(time, node_id, dimension, dimension_value,
					 message_count, byte_count, sample_rate, window_seconds)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
				rowTime, report.NodeID, string(d), e.Value,
				clampCount(e.Count), e.Bytes, report.SampleRate, windowSeconds)
			queued++
		}
	}
	if queued == 0 {
		return 0, nil
	}
	br := s.pool.SendBatch(ctx, batch)
	defer func() { _ = br.Close() }()
	for range queued {
		if _, err := br.Exec(); err != nil {
			// A constraint violation means the value is not storable
			// (empty/oversized). Skip it rather than failing the window.
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23514" {
				continue
			}
			return 0, fmt.Errorf("append accounting report: %w", err)
		}
	}
	return queued, nil
}

// Top-N limit bounds. MaxTopLimit caps how many rows a single ranking may
// return; the values are exported because the API layer must reject an
// oversized limit rather than silently serve a smaller page than the one it
// echoes back to the caller.
const (
	// DefaultTopLimit is the page size used when none is requested.
	DefaultTopLimit = 20
	// MaxTopLimit is the largest page a caller may ask for.
	MaxTopLimit = 200
)

// TopQuery selects the diagnostic Top-N slice.
type TopQuery struct {
	// Dimension is the dimension to rank.
	Dimension Dimension
	// From is the inclusive window start.
	From time.Time
	// To is the exclusive window end.
	To time.Time
	// Limit caps the number of returned rows.
	Limit int
	// TenantID optionally restricts the window to one tenant. It is only
	// meaningful for the tenant dimension, where it lets a tenant admin
	// inspect their own volume; platform admins leave it empty.
	TenantID string
}

// TopResult is one ranked dimension value.
type TopResult struct {
	// Value is the dimension value.
	Value string
	// MessageCount is the estimated message count over the window.
	MessageCount int64
	// ByteCount is the estimated byte volume over the window.
	ByteCount int64
	// Nodes reports how many distinct nodes contributed this value.
	Nodes int
	// SampleRate is the largest divisor seen among the contributing rows.
	// A sum that mixes one exact report with one sampled report is still an
	// extrapolation, so the maximum — not the minimum — decides: only a
	// rate of 1 across every contribution means the value was measured
	// rather than sampled, and the UI can label it accordingly.
	SampleRate int64
}

// Exact reports whether the counts are measured rather than extrapolated.
func (r TopResult) Exact() bool {
	return r.SampleRate <= 1
}

// Top ranks dimension values over a window. Values are summed across
// nodes so the answer is cluster-wide, and the node count is returned
// because a value seen on many nodes is a different operational problem
// from the same volume concentrated on one node.
func (s *Store) Top(ctx context.Context, q TopQuery) ([]TopResult, error) {
	if _, ok := ParseDimension(string(q.Dimension)); !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownDimension, q.Dimension)
	}
	limit := q.Limit
	if limit <= 0 || limit > MaxTopLimit {
		limit = DefaultTopLimit
	}
	// The tenant restriction only makes sense when ranking tenants; for
	// any other dimension the value is not a tenant identifier.
	tenantFilter := q.TenantID != "" && q.Dimension == DimensionTenant
	rows, err := s.pool.Query(ctx, `
		SELECT dimension_value,
		       SUM(message_count)::BIGINT,
		       SUM(byte_count)::BIGINT,
		       COUNT(DISTINCT node_id)::INT,
		       MAX(sample_rate)::BIGINT
		FROM accounting_topn
		WHERE dimension = $1
		  AND time >= $2
		  AND time < $3
		  AND ($4::TEXT IS NULL OR dimension_value = $4)
		GROUP BY dimension_value
		ORDER BY SUM(message_count) DESC, dimension_value ASC
		LIMIT $5`,
		string(q.Dimension), q.From, q.To, nullIfEmpty(tenantFilter, q.TenantID), limit)
	if err != nil {
		return nil, fmt.Errorf("accounting top query: %w", err)
	}
	defer rows.Close()
	out := make([]TopResult, 0, limit)
	for rows.Next() {
		var r TopResult
		if err := rows.Scan(&r.Value, &r.MessageCount, &r.ByteCount, &r.Nodes, &r.SampleRate); err != nil {
			return nil, fmt.Errorf("scan accounting row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// clampCount narrows an estimated unsigned count into the signed range the
// BIGINT columns store. Saturating is the only safe direction: a wrapped
// conversion would persist a negative "messages sent", which is the one
// accounting answer an operator must never be shown.
func clampCount(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

// nullIfEmpty returns nil for an empty string so the optional filter
// parameter can be passed as a typed NULL.
func nullIfEmpty(active bool, value string) any {
	if !active || value == "" {
		return nil
	}
	return value
}

// PurgeBefore deletes accounting rows older than the cutoff. The
// TimescaleDB retention policy is the primary mechanism; this exists so an
// operator (and the test suite) can force the 24h bound deterministically.
func (s *Store) PurgeBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM accounting_topn WHERE time < $1`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge accounting: %w", err)
	}
	return tag.RowsAffected(), nil
}
