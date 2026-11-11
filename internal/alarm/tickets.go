// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// tickets.go implements alarm assignment and comments (ADR-0019). It is the
// deliberately small ticket loop the ADR permits: an alarm can be assigned to
// one tenant member and carry an append-only text timeline, with no file
// attachments or standalone work-order workflow.

package alarm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidAssignee means the requested assignee is not an active member of
// the alarm's tenant. The REST layer maps it to 400.
var ErrInvalidAssignee = errors.New("invalid alarm assignee")

// CommentRow is one typed alarm comment.
type CommentRow struct {
	// CommentID identifies the comment.
	CommentID string `json:"comment_id"`

	// AlarmID identifies the parent alarm.
	AlarmID string `json:"alarm_id"`

	// AuthorID identifies the user that wrote the comment.
	AuthorID string `json:"author_id"`

	// AuthorName is the display snapshot used by the UI.
	AuthorName string `json:"author_name"`

	// Body is the plain-text comment.
	Body string `json:"body"`

	// CreatedAt is the timestamp as RFC3339 UTC.
	CreatedAt string `json:"created_at"`
}

// AssignAlarm assigns one alarm to a tenant user.
//
// An empty assigneeID clears the current assignment. The assignee is verified
// inside the same tenant and must not be disabled, so a caller cannot point an
// alarm at a platform admin or a removed account and silently lose the
// assignment's accountability.
func (s *Service) AssignAlarm(ctx context.Context, tenantID, alarmID, assigneeID string) error {
	if assigneeID != "" {
		var valid bool
		if err := s.pool.QueryRow(ctx,
			`SELECT EXISTS(
				SELECT 1 FROM app_user
				WHERE id = $1 AND tenant_id = $2 AND disabled = false)`,
			assigneeID, tenantID).Scan(&valid); err != nil {
			return fmt.Errorf("validate assignee: %w", err)
		}
		if !valid {
			return ErrInvalidAssignee
		}
	}

	// Narrow the update to alarms whose device is visible; assigning an
	// out-of-scope alarm must not be possible even when its id is known.
	asClause, asArgs := s.scopedAlarmPredicate(ctx, 3)
	tag, err := s.pool.Exec(ctx,
		`UPDATE alarm
		 SET assignee_id = NULLIF($3, '')::uuid
		 WHERE tenant_id = $1 AND id = $2`+asClause,
		append([]any{tenantID, alarmID, assigneeID}, asArgs...)...)
	if err != nil {
		return fmt.Errorf("assign alarm: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddComment appends a comment to one alarm.
func (s *Service) AddComment(ctx context.Context, tenantID, alarmID, authorID, body string) (CommentRow, error) {
	body = strings.TrimSpace(body)
	if body == "" || len(body) > 2000 {
		return CommentRow{}, fmt.Errorf("comment body must contain 1-2000 characters")
	}

	// Refuse to comment on an alarm whose device is outside the caller's
	// scope; the predicate makes it look missing rather than forbidden.
	var exists bool
	acClause, acArgs := s.scopedAlarmPredicate(ctx, 2)
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM alarm WHERE tenant_id = $1 AND id = $2`+acClause+`)`,
		append([]any{tenantID, alarmID}, acArgs...)...).Scan(&exists); err != nil {
		return CommentRow{}, fmt.Errorf("check alarm: %w", err)
	}
	if !exists {
		return CommentRow{}, ErrNotFound
	}

	commentID := uuid.NewString()
	now := time.Now().UTC()
	var authorName string
	if err := s.pool.QueryRow(ctx,
		`INSERT INTO alarm_comment
		   (id, tenant_id, alarm_id, author_id, body, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 RETURNING (SELECT display_name FROM app_user WHERE id = $4)`,
		commentID, tenantID, alarmID, authorID, body, now,
	).Scan(&authorName); err != nil {
		return CommentRow{}, fmt.Errorf("insert alarm comment: %w", err)
	}

	return CommentRow{
		CommentID:  commentID,
		AlarmID:    alarmID,
		AuthorID:   authorID,
		AuthorName: authorName,
		Body:       body,
		CreatedAt:  now.Format(time.RFC3339),
	}, nil
}

// ListComments returns the comment timeline of one alarm oldest first.
func (s *Service) ListComments(ctx context.Context, tenantID, alarmID string) ([]CommentRow, error) {
	// Reading another customer's alarm comments must look like a missing
	// alarm, not an empty thread.
	lcClause, lcArgs := s.scopedAlarmPredicate(ctx, 2)
	var visible bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM alarm WHERE tenant_id = $1 AND id = $2`+lcClause+`)`,
		append([]any{tenantID, alarmID}, lcArgs...)...).Scan(&visible); err != nil {
		return nil, fmt.Errorf("check alarm: %w", err)
	}
	if !visible {
		return nil, ErrNotFound
	}

	rows, err := s.pool.Query(ctx,
		`SELECT c.id::text, c.alarm_id::text, c.author_id::text,
		        COALESCE(u.display_name, u.email), c.body, c.created_at
		 FROM alarm_comment c
		 JOIN app_user u ON u.id = c.author_id
		 WHERE c.tenant_id = $1 AND c.alarm_id = $2
		 ORDER BY c.created_at ASC`,
		tenantID, alarmID)
	if err != nil {
		return nil, fmt.Errorf("list alarm comments: %w", err)
	}
	defer rows.Close()

	result := []CommentRow{}
	for rows.Next() {
		var row CommentRow
		var createdAt time.Time
		if err := rows.Scan(&row.CommentID, &row.AlarmID, &row.AuthorID,
			&row.AuthorName, &row.Body, &createdAt); err != nil {
			return nil, fmt.Errorf("scan alarm comment: %w", err)
		}
		row.CreatedAt = createdAt.UTC().Format(time.RFC3339)
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}
