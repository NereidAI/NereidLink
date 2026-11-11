// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batch_store.go persists asynchronous batch command tasks and their
// per-device items. Task rows describe the requested target and aggregate
// progress; item rows are the source for recovery and result pages.

package command

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// Target type values for batch commands.
const (
	// TargetGroup expands every active device in a group subtree.
	TargetGroup = "group"

	// TargetDevices expands an explicit device-ID selector.
	TargetDevices = "devices"
)

// Task lifecycle states.
const (
	// TaskStatePending is created before execution starts.
	TaskStatePending = "pending"

	// TaskStateRunning has at least one item being processed.
	TaskStateRunning = "running"

	// TaskStateCompleted means every item succeeded or was accepted.
	TaskStateCompleted = "completed"

	// TaskStatePartial contains both successful and failed items.
	TaskStatePartial = "partial"

	// TaskStateFailed means all items failed or the target was empty.
	TaskStateFailed = "failed"
)

// Item lifecycle states.
const (
	// ItemStatePending awaits dispatch.
	ItemStatePending = "pending"

	// ItemStateRunning means dispatch has started.
	ItemStateRunning = "running"

	// ItemStateSucceeded records an accepted or successful command.
	ItemStateSucceeded = "succeeded"

	// ItemStateFailed records a dispatch or device failure.
	ItemStateFailed = "failed"
)

// Task is an asynchronous batch command.
type Task struct {
	// ID identifies the task.
	ID string `json:"id"`

	// TenantID owns the task and target devices.
	TenantID string `json:"tenant_id"`

	// CreatedBy records the requesting user when available.
	CreatedBy string `json:"created_by,omitempty"`

	// Name is the command sent to every target.
	Name string `json:"name"`

	// Params is the shared parameter document.
	Params []byte `json:"params"`

	// TimeoutMS optionally overrides catalog timeouts.
	TimeoutMS int64 `json:"timeout_ms"`

	// Direction is read, write or empty for catalog default.
	Direction string `json:"direction,omitempty"`

	// TargetType is group or devices.
	TargetType string `json:"target_type"`

	// GroupID is set for a group target.
	GroupID string `json:"group_id,omitempty"`

	// State is the task lifecycle state.
	State string `json:"state"`

	// TotalCount is the expanded device count.
	TotalCount int `json:"total_count"`

	// SucceededCount is the number of accepted or successful items.
	SucceededCount int `json:"succeeded_count"`

	// FailedCount is the number of failed items.
	FailedCount int `json:"failed_count"`

	// Error describes a task-level failure.
	Error string `json:"error,omitempty"`

	// CreatedAt is when the request was accepted.
	CreatedAt time.Time `json:"created_at"`

	// StartedAt is when execution began.
	StartedAt *time.Time `json:"started_at,omitempty"`

	// FinishedAt is when every item reached a terminal state.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// TaskItem is one device-specific member of a batch task.
type TaskItem struct {
	// ID identifies the item.
	ID string `json:"id"`

	// TaskID owns the item.
	TaskID string `json:"task_id"`

	// TenantID owns the target device.
	TenantID string `json:"tenant_id"`

	// DeviceID is the command target.
	DeviceID string `json:"device_id"`

	// CommandID is the expanded individual command identifier.
	CommandID string `json:"command_id,omitempty"`

	// State is the item lifecycle state.
	State string `json:"state"`

	// Response carries the command response when present.
	Response []byte `json:"response,omitempty"`

	// Error carries the failure detail.
	Error string `json:"error,omitempty"`

	// CreatedAt is item creation time.
	CreatedAt time.Time `json:"created_at"`

	// FinishedAt is terminal transition time.
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// BatchStore implements task persistence.
type BatchStore struct {
	pool *pgxpool.Pool
}

// NewBatchStore constructs the batch command store.
func NewBatchStore(pool *pgxpool.Pool) *BatchStore {
	return &BatchStore{pool: pool}
}

// ExpandGroup returns active devices in the group and all descendant groups.
func (s *BatchStore) ExpandGroup(ctx context.Context, tenantID, groupID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`WITH RECURSIVE subtree AS (
		    SELECT id FROM device_group
		    WHERE tenant_id = $1::uuid AND id = $2::uuid
		    UNION
		    SELECT child.id FROM device_group child
		    JOIN subtree parent ON child.parent_id = parent.id
		 )
		 SELECT device.id::text
		 FROM device
		 JOIN subtree ON device.group_id = subtree.id
		 WHERE device.tenant_id = $1::uuid AND device.state = $3
		 ORDER BY device.id`,
		tenantID, groupID, DeviceStateActive)
	if err != nil {
		return nil, fmt.Errorf("expand group: %w", err)
	}
	defer rows.Close()
	return scanDeviceIDs(rows)
}

// ResolveDevices validates an explicit device selector and removes duplicates.
func (s *BatchStore) ResolveDevices(ctx context.Context, tenantID string, deviceIDs []string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text FROM device
		 WHERE tenant_id = $1 AND state = $2 AND id = ANY($3::uuid[])
		 ORDER BY id`,
		tenantID, DeviceStateActive, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve devices: %w", err)
	}
	defer rows.Close()
	return scanDeviceIDs(rows)
}

// Create inserts a task and all pending items in one transaction.
func (s *BatchStore) Create(ctx context.Context, task Task, items []TaskItem) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin batch task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var createdBy *uuid.UUID
	if task.CreatedBy != "" {
		value, parseErr := uuid.Parse(task.CreatedBy)
		if parseErr != nil {
			return fmt.Errorf("invalid creator: %w", parseErr)
		}
		createdBy = &value
	}
	var groupID *uuid.UUID
	if task.GroupID != "" {
		value, parseErr := uuid.Parse(task.GroupID)
		if parseErr != nil {
			return fmt.Errorf("invalid group: %w", parseErr)
		}
		groupID = &value
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO command_task
		   (id, tenant_id, created_by, name, params, timeout_ms, direction,
		    target_type, group_id, state, total_count, succeeded_count,
		    failed_count, error, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		task.ID, task.TenantID, createdBy, task.Name, string(task.Params),
		task.TimeoutMS, task.Direction, task.TargetType, groupID,
		task.State, task.TotalCount, task.SucceededCount, task.FailedCount,
		nullableText(task.Error), task.CreatedAt); err != nil {
		return fmt.Errorf("insert task: %w", err)
	}

	for _, item := range items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO command_task_item
			   (id, task_id, tenant_id, device_id, command_id, state,
			    response, error, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			item.ID, item.TaskID, item.TenantID, item.DeviceID,
			nullableUUID(item.CommandID), ItemStatePending, nil, nil,
			item.CreatedAt); err != nil {
			return fmt.Errorf("insert task item: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// GetTask loads one tenant-owned task.
func (s *BatchStore) GetTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx, taskSelect+`
		 WHERE tenant_id = $1 AND id = $2`, tenantID, taskID)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// ListTasks returns one page of tenant tasks.
//
// A batch task is not itself owned by a device: it owns items, one per target
// device. A task is therefore visible to a narrowed caller only when at least
// one of its items targets a device inside the caller's scope. EXISTS against
// the item table keeps the task list itself cheap while still hiding the
// existence of batches aimed entirely at other customers' devices.
func (s *BatchStore) ListTasks(ctx context.Context, tenantID string, page, pageSize int) (apipage.Page[Task], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("device_id", 1)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM command_task t
		 WHERE t.tenant_id = $1
		   AND (NOT EXISTS (SELECT 1 FROM command_task_item ti WHERE ti.task_id = t.id)
		        OR EXISTS (SELECT 1 FROM command_task_item ti
		                   WHERE ti.task_id = t.id`+countClause+`))`,
		append([]any{tenantID}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("count tasks: %w", err)
	}

	listClause, listArgs := scope.DeviceSubqueryPredicate("device_id", 3)
	rows, err := s.pool.Query(ctx, taskSelect+`
		 WHERE tenant_id = $1
		   AND (NOT EXISTS (SELECT 1 FROM command_task_item ti WHERE ti.task_id = command_task.id)
		        OR EXISTS (SELECT 1 FROM command_task_item ti
		                   WHERE ti.task_id = command_task.id`+listClause+`))
		 ORDER BY created_at DESC
		 LIMIT $2 OFFSET $3`,
		append([]any{tenantID, pageSize, (page - 1) * pageSize}, listArgs...)...)
	if err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return apipage.Page[Task]{}, err
	}
	return apipage.New(tasks, total, page, pageSize), nil
}

// ListItems returns one page of task items.
//
// Items are narrowed by the caller's rbac.DataScope: the per-device results of
// a batch are device-derived data, and a Customer User must not see the
// outcomes of commands issued to another customer's devices. This also keeps
// the item counts of a task honest for a narrowed caller.
func (s *BatchStore) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[TaskItem], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("device_id", 2)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM command_task_item
		 WHERE tenant_id = $1 AND task_id = $2`+countClause,
		append([]any{tenantID, taskID}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[TaskItem]{}, fmt.Errorf("count items: %w", err)
	}
	listClause, listArgs := scope.DeviceSubqueryPredicate("device_id", 4)
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id = $1 AND task_id = $2`+listClause+`
		 ORDER BY created_at, id
		 LIMIT $3 OFFSET $4`,
		append([]any{tenantID, taskID, pageSize, (page - 1) * pageSize},
			listArgs...)...)
	if err != nil {
		return apipage.Page[TaskItem]{}, fmt.Errorf("list items: %w", err)
	}
	defer rows.Close()
	items, err := scanTaskItems(rows)
	if err != nil {
		return apipage.Page[TaskItem]{}, err
	}
	return apipage.New(items, total, page, pageSize), nil
}

// PendingItems loads every unfinished item for execution or recovery.
func (s *BatchStore) PendingItems(ctx context.Context, tenantID, taskID string) ([]TaskItem, error) {
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id = $1 AND task_id = $2 AND state = $3
		 ORDER BY created_at, id`,
		tenantID, taskID, ItemStatePending)
	if err != nil {
		return nil, fmt.Errorf("list pending items: %w", err)
	}
	defer rows.Close()
	return scanTaskItems(rows)
}

// UnfinishedTasks returns tasks not yet marked terminal for process recovery.
func (s *BatchStore) UnfinishedTasks(ctx context.Context) ([]Task, error) {
	rows, err := s.pool.Query(ctx, taskSelect+`
		 WHERE state IN ($1,$2)
		 ORDER BY created_at`,
		TaskStatePending, TaskStateRunning)
	if err != nil {
		return nil, fmt.Errorf("list unfinished tasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// ClaimItem atomically moves one pending item to running.
func (s *BatchStore) ClaimItem(ctx context.Context, taskID, itemID string) (TaskItem, bool, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE command_task_item
		 SET state = $1
		 WHERE task_id = $2 AND id = $3 AND state = $4
		 RETURNING `+itemColumns,
		ItemStateRunning, taskID, itemID, ItemStatePending)
	item, err := scanTaskItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskItem{}, false, nil
	}
	if err != nil {
		return TaskItem{}, false, fmt.Errorf("claim item: %w", err)
	}
	return item, true, nil
}

// FailRunningItems terminates items left running by a previous process so
// recovery does not send a second command without caller knowledge.
func (s *BatchStore) FailRunningItems(ctx context.Context, taskID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE command_task_item
		 SET state = $1, error = $2, finished_at = now()
		 WHERE task_id = $3 AND state = $4`,
		ItemStateFailed, "interrupted by server restart", taskID, ItemStateRunning)
	if err != nil {
		return fmt.Errorf("fail stale items: %w", err)
	}
	return nil
}

// MarkRunning transitions a pending task to running.
func (s *BatchStore) MarkRunning(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE command_task
		 SET state = $1, started_at = COALESCE(started_at, now())
		 WHERE tenant_id = $2 AND id = $3 AND state = $4
		 RETURNING `+taskColumns,
		TaskStateRunning, tenantID, taskID, TaskStatePending)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetTask(ctx, tenantID, taskID)
	}
	return task, err
}

// RecomputeTask recalculates aggregate counts and terminal state.
func (s *BatchStore) RecomputeTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	var succeeded, failed, total int
	if err := s.pool.QueryRow(ctx,
		`SELECT
		    count(*) FILTER (WHERE state = $1),
		    count(*) FILTER (WHERE state = $2),
		    count(*)
		 FROM command_task_item
		 WHERE task_id = $3 AND tenant_id = $4`,
		ItemStateSucceeded, ItemStateFailed, taskID, tenantID).
		Scan(&succeeded, &failed, &total); err != nil {
		return Task{}, fmt.Errorf("count task progress: %w", err)
	}
	state := TaskStateRunning
	if total == 0 {
		state = TaskStateFailed
	} else if total == succeeded {
		state = TaskStateCompleted
	} else if total == failed {
		state = TaskStateFailed
	} else if succeeded+failed == total {
		state = TaskStatePartial
	}
	row := s.pool.QueryRow(ctx,
		`UPDATE command_task
		 SET succeeded_count = $1, failed_count = $2, state = $3,
		     finished_at = now()
		 WHERE id = $4 AND tenant_id = $5
		 RETURNING `+taskColumns,
		succeeded, failed, state, taskID, tenantID)
	return scanTask(row)
}

// ApplyItemResult stores one terminal item and recomputes task progress.
func (s *BatchStore) ApplyItemResult(ctx context.Context, item TaskItem) (Task, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, fmt.Errorf("begin item result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	finishedAt := time.Now().UTC()
	if _, err := tx.Exec(ctx,
		`UPDATE command_task_item
		 SET command_id = $2, state = $3, response = $4, error = $5,
		     finished_at = $6
		 WHERE id = $1 AND task_id = $7 AND state = $8`,
		item.ID, nullableUUID(item.CommandID), item.State,
		nullableJSON(item.Response), nullableText(item.Error), finishedAt,
		item.TaskID, ItemStateRunning); err != nil {
		return Task{}, fmt.Errorf("update item: %w", err)
	}

	var succeeded, failed, total int
	if err := tx.QueryRow(ctx,
		`SELECT
		    count(*) FILTER (WHERE state = $1),
		    count(*) FILTER (WHERE state = $2),
		    count(*)
		 FROM command_task_item
		 WHERE task_id = $3`,
		ItemStateSucceeded, ItemStateFailed, item.TaskID).
		Scan(&succeeded, &failed, &total); err != nil {
		return Task{}, fmt.Errorf("count item progress: %w", err)
	}

	state := ""
	switch total {
	case succeeded:
		state = TaskStateCompleted
	case failed:
		state = TaskStateFailed
	default:
		state = TaskStatePartial
	}
	finishedExpression := ""
	args := []any{succeeded, failed, state}
	if succeeded+failed == total {
		finishedExpression = ", finished_at = now()"
	}
	args = append(args, item.TaskID, item.TenantID)

	row := tx.QueryRow(ctx,
		`UPDATE command_task
		 SET succeeded_count = $1, failed_count = $2, state = $3`+finishedExpression+`
		 WHERE id = $4 AND tenant_id = $5
		 RETURNING `+taskColumns, args...)
	task, err := scanTask(row)
	if err != nil {
		return Task{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, fmt.Errorf("commit item result: %w", err)
	}
	return task, nil
}

var taskSelect = `SELECT ` + taskColumns + ` FROM command_task`

var itemSelect = `SELECT ` + itemColumns + ` FROM command_task_item`

const taskColumns = `id::text, tenant_id::text, created_by::text, name,
       params::text, timeout_ms, direction, target_type, group_id::text, state,
       total_count, succeeded_count, failed_count, error, created_at,
       started_at, finished_at`

const itemColumns = `id::text, task_id::text, tenant_id::text,
       device_id::text, command_id::text, state, response::text, error,
       created_at, finished_at`

// rowScanner accepts either a pgx.Row or pgx.Rows value.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanTask maps one task row.
func scanTask(row rowScanner) (Task, error) {
	var task Task
	var params string
	var createdBy, groupID, failure *string
	var startedAt, finishedAt *time.Time
	err := row.Scan(&task.ID, &task.TenantID, &createdBy, &task.Name, &params,
		&task.TimeoutMS, &task.Direction, &task.TargetType, &groupID, &task.State,
		&task.TotalCount, &task.SucceededCount, &task.FailedCount, &failure,
		&task.CreatedAt, &startedAt, &finishedAt)
	if err != nil {
		return Task{}, err
	}
	task.Params = []byte(params)
	if createdBy != nil {
		task.CreatedBy = *createdBy
	}
	if groupID != nil {
		task.GroupID = *groupID
	}
	if failure != nil {
		task.Error = *failure
	}
	task.StartedAt = startedAt
	task.FinishedAt = finishedAt
	return task, nil
}

// scanTasks maps multiple task rows.
func scanTasks(rows pgx.Rows) ([]Task, error) {
	tasks := []Task{}
	for rows.Next() {
		task, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// scanTaskItem maps one item row.
func scanTaskItem(row rowScanner) (TaskItem, error) {
	var item TaskItem
	var commandID, response, failure *string
	var finishedAt *time.Time
	if err := row.Scan(&item.ID, &item.TaskID, &item.TenantID,
		&item.DeviceID, &commandID, &item.State, &response, &failure,
		&item.CreatedAt, &finishedAt); err != nil {
		return TaskItem{}, err
	}
	if commandID != nil {
		item.CommandID = *commandID
	}
	if response != nil {
		item.Response = []byte(*response)
	}
	if failure != nil {
		item.Error = *failure
	}
	item.FinishedAt = finishedAt
	return item, nil
}

// scanTaskItems maps multiple item rows.
func scanTaskItems(rows pgx.Rows) ([]TaskItem, error) {
	items := []TaskItem{}
	for rows.Next() {
		item, err := scanTaskItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// scanDeviceIDs reads a device ID result set.
func scanDeviceIDs(rows pgx.Rows) ([]string, error) {
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// nullableUUID converts an empty UUID to SQL null.
func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	return value
}
