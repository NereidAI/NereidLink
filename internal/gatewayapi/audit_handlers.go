// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// audit_handlers.go exposes audit query and verification to platform
// operators. Audit is cross-tenant, so the routes sit outside a tenant
// prefix and require platform administration.

package gatewayapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/nereidai/nereidlink/internal/audit"
)

// AuditReader is the audit query/verification/write surface.
type AuditReader interface {
	// Query lists events filtered and paged.
	Query(ctx context.Context, filter audit.QueryFilter, limit, offset int) ([]audit.Event, int, error)

	// Verify recomputes the hash chain and daily seals.
	Verify(ctx context.Context) (audit.VerifyResult, error)

	// Append writes one linked event.
	Append(ctx context.Context, input audit.Input) (audit.Event, error)
}

// SetAuditReader wires the audit service.
func (h *Hub) SetAuditReader(reader AuditReader) {
	h.auditReader = reader
}

// requireAuditReader returns the service or writes a 503.
func (h *Hub) requireAuditReader(w http.ResponseWriter) AuditReader {
	if h.auditReader == nil {
		writeRESTError(w, http.StatusServiceUnavailable, "AUDIT_SERVICE_UNAVAILABLE",
			"audit log is not enabled")
		return nil
	}
	return h.auditReader
}

// handleListAudit returns filtered audit events, newest first.
func (h *Hub) handleListAudit(w http.ResponseWriter, r *http.Request) {
	reader := h.requireAuditReader(w)
	if reader == nil {
		return
	}

	query := r.URL.Query()
	filter := audit.QueryFilter{
		EventType: query.Get("type"),
		TenantID:  query.Get("tenant"),
		ActorID:   query.Get("actor"),
	}
	if value := query.Get("from"); value != "" {
		parsed, ok := parseAuditTime(value)
		if !ok {
			writeRESTError(w, http.StatusBadRequest, "AUDIT_INVALID_TIME", "invalid from parameter")
			return
		}
		filter.From = parsed
	}
	if value := query.Get("to"); value != "" {
		parsed, ok := parseAuditTime(value)
		if !ok {
			writeRESTError(w, http.StatusBadRequest, "AUDIT_INVALID_TIME", "invalid to parameter")
			return
		}
		filter.To = parsed
	}

	page := atoiDefault(query.Get("page"), 1)
	pageSize := atoiDefault(query.Get("page_size"), 50)
	if pageSize > 100 {
		pageSize = 100
	}

	events, total, err := reader.Query(r.Context(), filter, pageSize, (page-1)*pageSize)
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "AUDIT_QUERY_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, map[string]any{
		"events": events,
		"total":  total,
		"page":   page,
	})
}

// handleVerifyAudit recomputes the hash chain.
func (h *Hub) handleVerifyAudit(w http.ResponseWriter, r *http.Request) {
	reader := h.requireAuditReader(w)
	if reader == nil {
		return
	}
	result, err := reader.Verify(r.Context())
	if err != nil {
		writeRESTError(w, http.StatusInternalServerError, "AUDIT_VERIFY_FAILED", err.Error())
		return
	}
	writeRESTJSON(w, http.StatusOK, result)
}

// parseAuditTime parses a query timestamp accepting RFC3339.
func parseAuditTime(value string) (time.Time, bool) {
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed, err == nil
}

// atoiDefault parses a positive integer with fallback.
func atoiDefault(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 1 {
		return fallback
	}
	return parsed
}
