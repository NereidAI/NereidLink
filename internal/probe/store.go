// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists probe tasks/items and exposes the narrow state
// transitions the manager and relay need. Results are operational and
// retained 24h; there is no history hypertable.

package probe

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/rbac"
)

// ErrNotFound means the task does not exist for that tenant.
var ErrNotFound = errors.New("probe task not found")

// rowScanner accepts pgx.Row or pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// Store is the PostgreSQL-backed probe task store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs a probe store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// ResolveTargets maps each requested ACTIVE/UNCLAIMED sub-device to the
// gateway it is bound to. Only gateway-attached devices are probeable: the
// probe downlink goes to the gateway MQTT connection. Devices missing from
// the map are unknown, inactive, or not attached to a gateway; the caller
// treats any mismatch as a request error.
func (s *Store) ResolveTargets(ctx context.Context, tenantID string, deviceIDs []string) (map[string]string, error) {
	scopeClause, scopeArgs := rbac.ScopeFromContext(ctx).DevicePredicate("", 2)
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, gateway_id::text
		 FROM device
		 WHERE tenant_id = $1
		   AND id = ANY($2::uuid[])
		   AND state IN ('ACTIVE','UNCLAIMED')
		   AND gateway_id IS NOT NULL`+scopeClause,
		append([]any{tenantID, deviceIDs}, scopeArgs...)...)
	if err != nil {
		return nil, fmt.Errorf("resolve probe targets: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string, len(deviceIDs))
	for rows.Next() {
		var deviceID, gatewayID string
		if err := rows.Scan(&deviceID, &gatewayID); err != nil {
			return nil, err
		}
		out[deviceID] = gatewayID
	}
	return out, rows.Err()
}

// Create inserts a task and all its pending items in one transaction.
func (s *Store) Create(ctx context.Context, task Task, items []Item) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin probe task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var createdBy any
	if task.CreatedBy != "" {
		if _, err := uuid.Parse(task.CreatedBy); err != nil {
			return fmt.Errorf("invalid creator: %w", err)
		}
		createdBy = task.CreatedBy
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO probe_task
		   (id, tenant_id, created_by, mode, timeout_ms, state,
		    total_count, succeeded_count, failed_count, created_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		task.ID, task.TenantID, createdBy, task.Mode, task.TimeoutMS,
		task.State, task.TotalCount, 0, 0, task.CreatedAt); err != nil {
		return fmt.Errorf("insert probe task: %w", err)
	}
	for _, item := range items {
		if _, err := tx.Exec(ctx,
			`INSERT INTO probe_task_item
			   (id, task_id, tenant_id, gateway_id, device_id, state, created_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			item.ID, item.TaskID, item.TenantID, item.GatewayID,
			item.DeviceID, ItemStatePending, item.CreatedAt); err != nil {
			return fmt.Errorf("insert probe task item: %w", err)
		}
	}
	return tx.Commit(ctx)
}

// scopedTaskPredicate narrows a query on probe_task to tasks that have at
// least one item on a device the caller may see. The task table itself carries
// no ownership columns, so visibility is a correlated EXISTS over its items;
// an unrestricted (internal/background) scope yields no clause. existing is
// the number of bind parameters the host query already uses.
func scopedTaskPredicate(ctx context.Context, existing int) (string, []any) {
	sub, args := rbac.ScopeFromContext(ctx).DeviceSubqueryPredicate("it.device_id", existing)
	if sub == "" {
		return "", nil
	}
	return " AND EXISTS (SELECT 1 FROM probe_task_item it" +
		" WHERE it.task_id = probe_task.id" + sub + ")", args
}

// GetTask loads one tenant-owned task. A request-scoped caller only sees a
// task that probes at least one device in their data scope; internal callers
// pass a context without a scope and are not narrowed.
func (s *Store) GetTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	scopeClause, scopeArgs := scopedTaskPredicate(ctx, 2)
	row := s.pool.QueryRow(ctx, taskSelect+` WHERE tenant_id=$1 AND id=$2`+scopeClause,
		append([]any{tenantID, taskID}, scopeArgs...)...)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// FindCoalescible returns the newest open task owned by the same principal in
// the same mode where every requested device is still a non-terminal
// (pending/running) item, so a repeated request can share the in-flight task
// instead of being rejected with PROBE_COOLDOWN. It returns ErrNotFound when
// nothing covers the whole set; a partial overlap does not coalesce.
func (s *Store) FindCoalescible(ctx context.Context, tenantID, actor, mode string, deviceIDs []string) (Task, error) {
	row := s.pool.QueryRow(ctx, taskSelect+`
		 WHERE probe_task.tenant_id=$1
		   AND probe_task.created_by=$2::uuid
		   AND probe_task.state IN ($3,$4)
		   AND probe_task.mode=$5
		   AND (SELECT count(*)
		          FROM probe_task_item it
		         WHERE it.task_id = probe_task.id
		           AND it.device_id = ANY($6::uuid[])
		           AND it.state IN ($7,$8)) = $9
		 ORDER BY probe_task.created_at DESC LIMIT 1`,
		tenantID, actor, TaskStatePending, TaskStateRunning, mode,
		deviceIDs, ItemStatePending, ItemStateRunning, len(deviceIDs))
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// ListTasks returns one page of a tenant's tasks, newest first, applying
// the optional state/mode/name filter.
func (s *Store) ListTasks(ctx context.Context, tenantID string, filter TaskFilter, page, pageSize int) (apipage.Page[Task], error) {
	where := []string{"tenant_id=$1"}
	args := []any{tenantID}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if filter.State != "" {
		where = append(where, "state="+addArg(filter.State))
	}
	if filter.Mode != "" {
		where = append(where, "mode="+addArg(filter.Mode))
	}
	if filter.Query != "" {
		pattern := "%" + filter.Query + "%"
		nameRef := addArg(pattern)
		// Match the task when the gateway or any probed device's name or
		// alias contains the query. The item rows carry both gateway_id
		// and device_id, so one EXISTS covers both.
		where = append(where, `EXISTS (
			SELECT 1 FROM probe_task_item it
			JOIN device d ON d.id IN (it.gateway_id, it.device_id)
			WHERE it.task_id = probe_task.id
			  AND (d.canonical_name ILIKE `+nameRef+` OR d.alias ILIKE `+nameRef+`))`)
	}
	if scopeClause, scopeArgs := scopedTaskPredicate(ctx, len(args)); scopeClause != "" {
		where = append(where, scopeClause[len(" AND "):])
		args = append(args, scopeArgs...)
	}
	clause := " WHERE " + strings.Join(where, " AND ")

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM probe_task`+clause, args...).Scan(&total); err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("count probe tasks: %w", err)
	}
	limitRef := addArg(pageSize)
	offsetRef := addArg((page - 1) * pageSize)
	rows, err := s.pool.Query(ctx, taskSelect+clause+
		` ORDER BY created_at DESC LIMIT `+limitRef+` OFFSET `+offsetRef, args...)
	if err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("list probe tasks: %w", err)
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return apipage.Page[Task]{}, err
	}
	return apipage.New(tasks, total, page, pageSize), nil
}

// ListItems returns one page of a task's items.
func (s *Store) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[Item], error) {
	// Both statements share $1/$2 (tenant,task) and the optional scope
	// predicate at $3..$5. The list statement appends LIMIT/OFFSET after the
	// scope placeholders so the WHERE stays contiguous in both branches.
	scopeClause, scopeArgs := rbac.ScopeFromContext(ctx).
		DeviceSubqueryPredicate("device_id", 2)
	baseArgs := append([]any{tenantID, taskID}, scopeArgs...)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM probe_task_item
		 WHERE tenant_id=$1 AND task_id=$2`+scopeClause,
		baseArgs...).Scan(&total); err != nil {
		return apipage.Page[Item]{}, fmt.Errorf("count probe items: %w", err)
	}
	limitRef := fmt.Sprintf("$%d", len(baseArgs)+1)
	offsetRef := fmt.Sprintf("$%d", len(baseArgs)+2)
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id=$1 AND task_id=$2`+scopeClause+` ORDER BY created_at, id
		 LIMIT `+limitRef+` OFFSET `+offsetRef,
		append(baseArgs, pageSize, (page-1)*pageSize)...)
	if err != nil {
		return apipage.Page[Item]{}, fmt.Errorf("list probe items: %w", err)
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		return apipage.Page[Item]{}, err
	}
	return apipage.New(items, total, page, pageSize), nil
}

// PendingItems loads every not-yet-dispatched item of a task with its owning
// gateway, used when grouping one downlink frame per gateway.
func (s *Store) PendingItems(ctx context.Context, tenantID, taskID string) ([]Item, error) {
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id=$1 AND task_id=$2 AND state=$3
		 ORDER BY gateway_id, id`,
		tenantID, taskID, ItemStatePending)
	if err != nil {
		return nil, fmt.Errorf("list pending probe items: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// ClaimDevices moves the named items of a task from pending to running.
// Items already running or terminal are skipped, so a duplicate dispatch is
// harmless.
func (s *Store) ClaimDevices(ctx context.Context, taskID string, deviceIDs []string) error {
	if len(deviceIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE probe_task_item
		 SET state=$1, finished_at=NULL
		 WHERE task_id=$2 AND device_id = ANY($3::uuid[]) AND state=$4`,
		ItemStateRunning, taskID, deviceIDs, ItemStatePending)
	if err != nil {
		return fmt.Errorf("claim probe items: %w", err)
	}
	return nil
}

// ApplyEvent records one terminal per-device outcome and recomputes the task.
// Only a running item is terminalized; a late/duplicate event is ignored. It
// returns the refreshed task and whether the item transitioned.
func (s *Store) ApplyEvent(ctx context.Context, ev Event) (Task, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, false, fmt.Errorf("begin probe result: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state := ItemStateSucceeded
	if ev.Outcome != OutcomeOK {
		state = ItemStateFailed
	}
	var result, failure any
	if len(ev.Result) > 0 {
		result = string(ev.Result)
	}
	if ev.Error != "" {
		failure = ev.Error
	}
	// Accept both pending and running items. A normal edge response arrives
	// after ClaimDevices moved the item to running; an offline-gateway
	// fast-fail (or a dispatch write failure) is applied instead before
	// claiming, while the item is still pending. Already-terminal items do
	// not match, which dedupes repeated or late results.
	tag, err := tx.Exec(ctx,
		`UPDATE probe_task_item
		 SET state=$1, result=$2, error=$3, finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5 AND device_id=$6
		   AND state IN ($7,$8)`,
		state, result, failure, ev.ProbeID, ev.TenantID, ev.DeviceID,
		ItemStatePending, ItemStateRunning)
	if err != nil {
		return Task{}, false, fmt.Errorf("apply probe result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		task, err := s.GetTask(ctx, ev.TenantID, ev.ProbeID)
		return task, false, err
	}

	task, err := recomputeInTx(ctx, tx, ev.TenantID, ev.ProbeID)
	if err != nil {
		return Task{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, false, fmt.Errorf("commit probe result: %w", err)
	}
	return task, true, nil
}

// CountOpen returns how many unfinished tasks a principal owns, used for the
// per-principal concurrency cap.
func (s *Store) CountOpen(ctx context.Context, createdBy string) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM probe_task
		 WHERE created_by=$1 AND state IN ($2,$3)`,
		createdBy, TaskStatePending, TaskStateRunning).Scan(&n); err != nil {
		return 0, fmt.Errorf("count open probe tasks: %w", err)
	}
	return n, nil
}

// Ref identifies a (tenant, task) pair during recovery.
type Ref struct {
	tenant string
	task   string
}

// OpenRefs returns the distinct (tenant,task) pairs that currently have at
// least one item in the given state.
func (s *Store) OpenRefs(ctx context.Context, itemState string) ([]Ref, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT tenant_id::text, task_id::text
		 FROM probe_task_item WHERE state=$1`, itemState)
	if err != nil {
		return nil, fmt.Errorf("list open probe refs: %w", err)
	}
	defer rows.Close()
	return scanRefs(rows)
}

// OpenTaskRefs returns (tenant,task) pairs for tasks not yet terminal.
func (s *Store) OpenTaskRefs(ctx context.Context) ([]Ref, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, id::text FROM probe_task
		 WHERE state IN ($1,$2)`, TaskStatePending, TaskStateRunning)
	if err != nil {
		return nil, fmt.Errorf("list open probe tasks: %w", err)
	}
	defer rows.Close()
	return scanRefs(rows)
}

// scanRefs reads a (tenant, task) result set.
func scanRefs(rows pgx.Rows) ([]Ref, error) {
	out := []Ref{}
	for rows.Next() {
		var r Ref
		if err := rows.Scan(&r.tenant, &r.task); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MarkRunning transitions a pending task to running.
func (s *Store) MarkRunning(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE probe_task
		 SET state=$1, started_at=COALESCE(started_at, now())
		 WHERE tenant_id=$2 AND id=$3 AND state=$4
		 RETURNING `+taskReturningColumns,
		TaskStateRunning, tenantID, taskID, TaskStatePending)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetTask(ctx, tenantID, taskID)
	}
	return task, err
}

// FailOpenItems terminalizes items still pending/running for one task. It is
// the task-deadline backstop and the startup-recovery path. The affected task
// aggregate is recomputed and returned.
func (s *Store) FailOpenItems(ctx context.Context, tenantID, taskID string) (Task, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE probe_task_item
		 SET state=$1, error=$2, finished_at=now()
		 WHERE task_id=$3 AND tenant_id=$4 AND state IN ($5,$6)`,
		ItemStateFailed, "probe did not complete in time",
		taskID, tenantID, ItemStatePending, ItemStateRunning); err != nil {
		return Task{}, fmt.Errorf("fail open probe items: %w", err)
	}
	return Recompute(ctx, s.pool, tenantID, taskID)
}

// DeleteFinishedBefore removes terminal tasks (and cascade-deletes their
// items) finished before the cutoff, enforcing the 24h result retention.
func (s *Store) DeleteFinishedBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM probe_task
		 WHERE state IN ($1,$2,$3) AND finished_at IS NOT NULL AND finished_at < $4`,
		TaskStateCompleted, TaskStatePartial, TaskStateFailed, before)
	if err != nil {
		return 0, fmt.Errorf("delete expired probe tasks: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Recompute recalculates aggregate counts/state for a task.
func Recompute(ctx context.Context, q pgQuerier, tenantID, taskID string) (Task, error) {
	return recomputeInTx(ctx, q, tenantID, taskID)
}

// pgQuerier is satisfied by both *pgxpool.Pool and pgx.Tx.
type pgQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// recomputeInTx recomputes the task within the supplied query scope.
func recomputeInTx(ctx context.Context, q pgQuerier, tenantID, taskID string) (Task, error) {
	var succeeded, failed, total int
	if err := q.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state=$1),
		        count(*) FILTER (WHERE state=$2),
		        count(*)
		 FROM probe_task_item WHERE task_id=$3`,
		ItemStateSucceeded, ItemStateFailed, taskID).
		Scan(&succeeded, &failed, &total); err != nil {
		return Task{}, fmt.Errorf("count probe progress: %w", err)
	}
	state := TaskStateRunning
	switch {
	case total == 0:
		state = TaskStateFailed
	case total == succeeded:
		state = TaskStateCompleted
	case total == failed:
		state = TaskStateFailed
	case succeeded+failed == total:
		state = TaskStatePartial
	}
	finishedExpr := ""
	args := []any{succeeded, failed, state, taskID, tenantID}
	if succeeded+failed == total {
		finishedExpr = ", finished_at=now()"
	}
	row := q.QueryRow(ctx,
		`UPDATE probe_task
		 SET succeeded_count=$1, failed_count=$2, state=$3`+finishedExpr+`
		 WHERE id=$4 AND tenant_id=$5
		 RETURNING `+taskReturningColumns, args...)
	task, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("recompute probe task: %w", err)
	}
	return task, nil
}

var taskSelect = `SELECT ` + taskReturningColumns + ` FROM probe_task`
var itemSelect = `SELECT ` + itemColumns + ` FROM probe_task_item`

const taskColumns = `id::text, tenant_id::text, created_by::text, mode,
       timeout_ms, state, total_count, succeeded_count, failed_count, error,
       created_at, started_at, finished_at`

// taskReturningColumns is the scanTask-aligned projection: the 13 task
// columns plus the gateway id carried by the task's first item. Both plain
// SELECT lists and UPDATE ... RETURNING lists must use it, because a
// 13-column RETURNING scanned into 14 destinations fails at runtime with
// pgx "number of field descriptions must equal number of destinations".
const taskReturningColumns = taskColumns + `, (
        SELECT gateway_id::text FROM probe_task_item
         WHERE task_id = probe_task.id LIMIT 1)`

const itemColumns = `id::text, task_id::text, tenant_id::text,
       gateway_id::text, device_id::text, state, result::text, error,
       created_at, finished_at`

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var createdBy, failure *string
	var startedAt, finishedAt *time.Time
	if err := row.Scan(&t.ID, &t.TenantID, &createdBy, &t.Mode, &t.TimeoutMS,
		&t.State, &t.TotalCount, &t.SucceededCount, &t.FailedCount, &failure,
		&t.CreatedAt, &startedAt, &finishedAt, &t.GatewayID); err != nil {
		return Task{}, err
	}
	if createdBy != nil {
		t.CreatedBy = *createdBy
	}
	if failure != nil {
		t.Error = *failure
	}
	t.StartedAt = startedAt
	t.FinishedAt = finishedAt
	return t, nil
}

func scanTasks(rows pgx.Rows) ([]Task, error) {
	out := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanItem(row rowScanner) (Item, error) {
	var it Item
	var result, failure *string
	var finishedAt *time.Time
	if err := row.Scan(&it.ID, &it.TaskID, &it.TenantID, &it.GatewayID,
		&it.DeviceID, &it.State, &result, &failure, &it.CreatedAt, &finishedAt); err != nil {
		return Item{}, err
	}
	if result != nil {
		it.Result = []byte(*result)
	}
	if failure != nil {
		it.Error = *failure
	}
	it.FinishedAt = finishedAt
	return it, nil
}

func scanItems(rows pgx.Rows) ([]Item, error) {
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
