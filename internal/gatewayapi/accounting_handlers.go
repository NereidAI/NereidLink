// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// accounting_handlers.go exposes the ADR-0025 Top-N diagnostic API: which
// tenant, device, address, protocol, node or subject pattern is consuming
// the platform's resources right now.
//
// Data scope: the endpoint is platform-scoped on purpose. Accounting rows
// are cluster-wide and carry no tenant column, so a device- or
// address-ranked answer unavoidably spans tenants; serving it under a
// tenant path would leak one tenant's fleet topology to another. A
// tenant-scoped view would need per-candidate tenant attribution in the
// sampler, which is deliberately not guessed here.

package gatewayapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/nereidai/nereidlink/internal/accounting"
)

// AccountingReader is the Top-N diagnostic surface.
// *accounting.Store satisfies it.
type AccountingReader interface {
	// Top ranks dimension values over a window.
	Top(ctx context.Context, q accounting.TopQuery) ([]accounting.TopResult, error)
}

// SetAccounting wires the accounting store.
func (h *Hub) SetAccounting(reader AccountingReader) {
	h.accounting = reader
}

// accountingWindowPresets bounds the accepted window sizes. Accounting
// rows are retained for 24h only, so a longer window would silently return
// a partial answer; the API rejects it instead of lying.
var accountingWindowPresets = map[string]time.Duration{
	"1m":  time.Minute,
	"5m":  5 * time.Minute,
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
}

// defaultAccountingWindow is used when the caller omits one.
const defaultAccountingWindow = "5m"

// handleAccountingTopN returns the ranked dimension values for a window.
//
//	GET /v1/platform/accounting/topn?dimension=device&window=5m&limit=20
func (h *Hub) handleAccountingTopN(w http.ResponseWriter, r *http.Request) {
	if h.accounting == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "ACCOUNTING_UNAVAILABLE",
			"resource accounting is not enabled")
		return
	}
	query := r.URL.Query()

	dimensionName := query.Get("dimension")
	if dimensionName == "" {
		dimensionName = string(accounting.DimensionTenant)
	}
	dimension, ok := accounting.ParseDimension(dimensionName)
	if !ok {
		writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_DIMENSION",
			"dimension must be one of tenant, device, ip, protocol, node, topic")
		return
	}

	windowKey := query.Get("window")
	if windowKey == "" {
		windowKey = defaultAccountingWindow
	}
	window, ok := accountingWindowPresets[windowKey]
	if !ok {
		writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_WINDOW",
			"window must be one of 1m, 5m, 15m, 1h, 6h, 24h")
		return
	}

	limit := accounting.DefaultTopLimit
	if raw := query.Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_LIMIT",
				"limit must be a positive integer")
			return
		}
		// An oversized limit is rejected instead of clamped: silently
		// serving a smaller page than the echoed limit would misreport the
		// size of the ranking the caller asked for.
		if parsed > accounting.MaxTopLimit {
			writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_LIMIT",
				"limit must not exceed the maximum page size")
			return
		}
		limit = parsed
	}

	// An optional tenant filter only makes sense for the tenant dimension;
	// for the others the value is not a tenant identifier, so accepting it
	// would silently return the unfiltered ranking.
	tenantFilter := query.Get("tenant")
	if tenantFilter != "" && dimension != accounting.DimensionTenant {
		writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_TENANT_FILTER",
			"the tenant filter applies to the tenant dimension only")
		return
	}

	now := time.Now().UTC()
	results, err := h.accounting.Top(r.Context(), accounting.TopQuery{
		Dimension: dimension,
		From:      now.Add(-window),
		To:        now,
		Limit:     limit,
		TenantID:  tenantFilter,
	})
	if err != nil {
		if errors.Is(err, accounting.ErrUnknownDimension) {
			writeRESTError(w, http.StatusBadRequest, "ACCOUNTING_BAD_DIMENSION",
				err.Error())
			return
		}
		writeRESTError(w, http.StatusInternalServerError, "ACCOUNTING_QUERY_FAILED",
			err.Error())
		return
	}

	// The array contract is [] rather than null when nothing matched, so a
	// client can iterate without a nil check.
	entries := make([]accountingTopNEntry, 0, len(results))
	for _, row := range results {
		entries = append(entries, accountingTopNEntry{
			Value:        row.Value,
			MessageCount: row.MessageCount,
			ByteCount:    row.ByteCount,
			Nodes:        row.Nodes,
			SampleRate:   row.SampleRate,
			Estimated:    !row.Exact(),
		})
	}
	writeRESTJSON(w, http.StatusOK, accountingTopNResponse{
		Dimension: string(dimension),
		Window:    windowKey,
		Limit:     limit,
		Entries:   entries,
	})
}

// accountingTopNEntry is one ranked dimension value.
type accountingTopNEntry struct {
	// Value is the accounted dimension value.
	Value string `json:"value"`
	// MessageCount is the estimated message count over the window.
	MessageCount int64 `json:"message_count"`
	// ByteCount is the estimated byte volume over the window.
	ByteCount int64 `json:"byte_count"`
	// Nodes is how many distinct nodes reported this value.
	Nodes int `json:"nodes"`
	// SampleRate is the largest 1/N divisor among the contributions.
	SampleRate int64 `json:"sample_rate"`
	// Estimated is true when the counts were extrapolated from a sample
	// rather than measured.
	Estimated bool `json:"estimated"`
}

// accountingTopNResponse is the Top-N diagnostic payload.
type accountingTopNResponse struct {
	// Dimension is the ranked dimension.
	Dimension string `json:"dimension"`
	// Window is the requested window key.
	Window string `json:"window"`
	// Limit is the applied row cap.
	Limit int `json:"limit"`
	// Entries is the ranking, ordered by descending message count.
	Entries []accountingTopNEntry `json:"entries"`
}
