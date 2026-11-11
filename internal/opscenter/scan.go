// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// scan.go maps ops_order rows to the Order type for both single-row
// and multi-row queries, handling nullable scope, identity and
// timestamp columns.

package opscenter

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// rowScanner is satisfied by pgx Row and Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanOrder reads one row in orderColumns order.
func scanOrder(row rowScanner) (Order, error) {
	var order Order
	var tenantID, initiatorID, reviewerID *string
	var reviewedAt, executedAt *time.Time
	var expiresAt, createdAt time.Time
	err := row.Scan(
		&order.ID, &tenantID, &order.Action, &order.Summary, &order.Payload,
		&order.ContentHash, &initiatorID, &order.InitiatorName,
		&order.InitiatorChannel, &order.Status, &reviewerID,
		&order.ReviewerName, &order.ReviewerChannel, &order.ReviewReason,
		&reviewedAt, &executedAt, &order.ExecResult, &expiresAt,
		&createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Order{}, ErrNotFound
	}
	if err != nil {
		return Order{}, fmt.Errorf("scan ops order: %w", err)
	}

	if tenantID != nil {
		order.TenantID = *tenantID
	}
	if initiatorID != nil {
		order.InitiatorID = *initiatorID
	}
	if reviewerID != nil {
		order.ReviewerID = *reviewerID
	}
	if reviewedAt != nil {
		order.ReviewedAt = reviewedAt.UTC().Format(time.RFC3339)
	}
	if executedAt != nil {
		order.ExecutedAt = executedAt.UTC().Format(time.RFC3339)
	}
	order.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	order.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	if len(order.ExecResult) == 0 {
		order.ExecResult = json.RawMessage(`{}`)
	}
	if len(order.Payload) == 0 {
		order.Payload = json.RawMessage(`{}`)
	}
	return order, nil
}

// jsonObject reports whether raw is a JSON object document.
func jsonObject(raw []byte) bool {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	_, ok := value.(map[string]any)
	return ok
}
