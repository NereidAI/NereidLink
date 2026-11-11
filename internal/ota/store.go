// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists OTA tasks/items and exposes the narrow state
// transitions the manager and relay need. OTA records are long-lived
// operational state and are retained (no 24h sweep); finished rollouts are
// read back for audit.

package ota

import (
	"context"
	"encoding/json"
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
var ErrNotFound = errors.New("ota task not found")

// ErrNotCancellable means the task is already terminal and cannot be canceled.
var ErrNotCancellable = errors.New("ota task is not cancellable")

// rowScanner accepts pgx.Row or pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

// Store is the PostgreSQL-backed OTA task store.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore constructs an OTA store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// TargetInfo is one resolved rollout target.
type TargetInfo struct {
	// GatewayID is empty for directly connected devices.
	GatewayID string
	// ProfileID is the device's model; used for server-side package
	// compatibility checks (ADR-0006d §3).
	ProfileID string
	// FirmwareVersion is the version the device currently reports; targets
	// already on the requested version are skipped as already_current.
	FirmwareVersion string
}

// ResolveTargets maps each requested ACTIVE/UNCLAIMED device to its gateway,
// profile and current firmware version. Directly connected devices
// (gateway_id IS NULL) map to an empty GatewayID so they are OTA targets too;
// they pull the package over the authenticated device HTTPS relay instead of
// receiving a gateway frame. Missing ids are unknown or inactive and are
// rejected by the caller.
func (s *Store) ResolveTargets(ctx context.Context, tenantID string, deviceIDs []string) (map[string]TargetInfo, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT d.id::text, COALESCE(d.gateway_id::text, ''),
		        COALESCE(d.profile_id::text, ''), COALESCE(d.firmware_version, '')
		 FROM device d
		 WHERE d.tenant_id = $1
		   AND d.id = ANY($2::uuid[])
		   AND d.state IN ('ACTIVE','UNCLAIMED')`,
		tenantID, deviceIDs)
	if err != nil {
		return nil, fmt.Errorf("resolve ota targets: %w", err)
	}
	defer rows.Close()
	out := make(map[string]TargetInfo, len(deviceIDs))
	for rows.Next() {
		var deviceID string
		var info TargetInfo
		if err := rows.Scan(&deviceID, &info.GatewayID, &info.ProfileID,
			&info.FirmwareVersion); err != nil {
			return nil, err
		}
		out[deviceID] = info
	}
	return out, rows.Err()
}

// ResolveSelectorTargets resolves a dynamic target selector (device profile
// and/or group and/or customer, all AND-combined when present) to active
// devices. The result is snapshotted into task items by the caller so later
// membership changes never move a running rollout (ADR-0006d §1).
func (s *Store) ResolveSelectorTargets(ctx context.Context, tenantID string, sel TargetSelector) (map[string]TargetInfo, error) {
	conds := []string{"d.tenant_id = $1", "d.state IN ('ACTIVE','UNCLAIMED')"}
	args := []any{tenantID}
	arg := 2
	if strings.TrimSpace(sel.ProfileID) != "" {
		conds = append(conds, fmt.Sprintf("d.profile_id = $%d::uuid", arg))
		args = append(args, sel.ProfileID)
		arg++
	}
	if strings.TrimSpace(sel.GroupID) != "" {
		conds = append(conds, fmt.Sprintf("d.group_id = $%d::uuid", arg))
		args = append(args, sel.GroupID)
		arg++
	}
	if strings.TrimSpace(sel.CustomerID) != "" {
		conds = append(conds, fmt.Sprintf("d.customer_id = $%d::uuid", arg))
		args = append(args, sel.CustomerID)
		arg++
	}
	rows, err := s.pool.Query(ctx,
		`SELECT d.id::text, COALESCE(d.gateway_id::text, ''),
		        COALESCE(d.profile_id::text, ''), COALESCE(d.firmware_version, '')
		 FROM device d
		 WHERE `+strings.Join(conds, " AND ")+`
		 ORDER BY d.created_at DESC, d.canonical_name
		 LIMIT $`+fmt.Sprint(arg),
		append(args, 1001)...)
	if err != nil {
		return nil, fmt.Errorf("resolve ota selector targets: %w", err)
	}
	defer rows.Close()
	out := map[string]TargetInfo{}
	for rows.Next() {
		var deviceID string
		var info TargetInfo
		if err := rows.Scan(&deviceID, &info.GatewayID, &info.ProfileID,
			&info.FirmwareVersion); err != nil {
			return nil, err
		}
		out[deviceID] = info
	}
	return out, rows.Err()
}

// TasksWithPendingForGateway lists distinct open tasks that still have
// pending items bound to one gateway, used to redispatch when it reconnects.
func (s *Store) TasksWithPendingForGateway(ctx context.Context, tenantID, gatewayID string) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT i.task_id::text
		 FROM ota_task_item i
		 JOIN ota_task t ON t.id = i.task_id
		 WHERE i.tenant_id=$1 AND i.gateway_id=$2
		   AND i.state='pending' AND i.batch_no >= 0
		   AND t.state IN ('pending','running')`,
		tenantID, gatewayID)
	if err != nil {
		return nil, fmt.Errorf("list pending-gateway tasks: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Create inserts a task and all its pending items in one transaction.
func (s *Store) Create(ctx context.Context, task Task, items []Item) error {
	if task.SuccessPolicy == "" {
		task.SuccessPolicy = PolicyDeviceReported
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin ota task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var createdBy any
	if task.CreatedBy != "" {
		if _, err := uuid.Parse(task.CreatedBy); err != nil {
			return fmt.Errorf("invalid creator: %w", err)
		}
		createdBy = task.CreatedBy
	}
	headers, err := json.Marshal(task.HTTPHeaders)
	if err != nil {
		return fmt.Errorf("encode ota headers: %w", err)
	}
	if len(headers) == 0 || string(headers) == "null" {
		headers = []byte("{}")
	}
	selector, err := json.Marshal(task.TargetSelector)
	if err != nil {
		return fmt.Errorf("encode ota target selector: %w", err)
	}
	if len(selector) == 0 || string(selector) == "null" {
		selector = []byte("{}")
	}
	if task.TargetMode == "" {
		task.TargetMode = TargetModeExplicit
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO ota_task
		   (id, tenant_id, created_by, firmware_id, firmware_version, package_url,
		    package_sha256, package_size, http_headers, state, success_policy,
		    total_count, succeeded_count, failed_count, created_at,
		    target_mode, target_selector,
		    batch_percent, batch_size, batch_pause_seconds,
		    failure_abort_percent, max_consecutive_failures,
		    stall_timeout_seconds, confirm_timeout_seconds)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,
		         $16,$17::jsonb,$18,$19,$20,$21,$22,$23,$24)`,
		task.ID, task.TenantID, createdBy, nullableUUID(task.FirmwareID),
		task.FirmwareVersion, task.PackageURL,
		task.PackageSHA256, task.PackageSize, string(headers), task.State,
		task.SuccessPolicy,
		task.TotalCount, 0, 0, task.CreatedAt,
		task.TargetMode, string(selector),
		task.Policy.BatchPercent, task.Policy.BatchSize,
		task.Policy.BatchPauseSeconds,
		task.Policy.FailureAbortPercent, task.Policy.MaxConsecutiveFailures,
		task.Policy.StallTimeoutSeconds, task.Policy.ConfirmTimeoutSeconds); err != nil {
		return fmt.Errorf("insert ota task: %w", err)
	}
	for _, item := range items {
		resultCode := item.ResultCode
		if resultCode == "" {
			resultCode = ResultNone
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO ota_task_item
			   (id, task_id, tenant_id, gateway_id, device_id, state,
			    result_code, batch_no, created_at, updated_at, finished_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,
			         CASE WHEN $6 IN ('succeeded','failed','canceled','skipped')
			              THEN $9::timestamptz ELSE CAST(NULL AS timestamptz) END)`,
			item.ID, item.TaskID, item.TenantID, nullableUUID(item.GatewayID),
			item.DeviceID, item.State, resultCode, item.BatchNo,
			item.CreatedAt); err != nil {
			return fmt.Errorf("insert ota task item: %w", err)
		}
		// Seed the append-only timeline with the platform event (seq 0):
		// 'pending' for a queued target, the terminal phase for an item
		// created already settled (e.g. already_current).
		if _, err := tx.Exec(ctx,
			`INSERT INTO ota_task_event
			   (id, tenant_id, task_id, device_id, gateway_id, seq, phase,
			    result_code, progress_pct, source, created_at)
			 VALUES ($1,$2,$3,$4,$5,0,$6,$7,0,'platform',$8)`,
			uuid.NewString(), item.TenantID, item.TaskID, item.DeviceID,
			nullableUUID(item.GatewayID), item.State, resultCode,
			item.CreatedAt); err != nil {
			return fmt.Errorf("insert ota task event: %w", err)
		}
	}
	// A rollout whose targets are all already settled (e.g. every device
	// already_current) is aggregated immediately.
	if allTerminal(items) {
		if _, err := recomputeInTx(ctx, tx, task.TenantID, task.ID, ""); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// allTerminal reports whether every item was created in a terminal state.
func allTerminal(items []Item) bool {
	if len(items) == 0 {
		return false
	}
	for _, it := range items {
		if !IsItemTerminal(it.State) {
			return false
		}
	}
	return true
}

// nullableUUID maps an empty UUID string to SQL NULL (e.g. direct devices
// have no owning gateway, and repository-less tasks have no firmware id).
func nullableUUID(value string) any {
	if value == "" {
		return nil
	}
	if _, err := uuid.Parse(value); err != nil {
		return nil
	}
	return value
}

// GetTask loads one tenant-owned task.
func (s *Store) GetTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx, taskSelect+` WHERE tenant_id=$1 AND id=$2`,
		tenantID, taskID)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// ListTasks returns one page of a tenant's OTA tasks, newest first. An empty
// state returns every state.
//
// Like a batch command task, an OTA task owns per-device items rather than
// being owned by a device itself. A narrowed caller sees a task only when at
// least one of its items targets a device in the caller's scope, so a rollout
// aimed entirely at another customer's devices stays invisible.
func (s *Store) ListTasks(ctx context.Context, tenantID, state string, page, pageSize int) (apipage.Page[Task], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("device_id", 2)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ota_task t
		 WHERE t.tenant_id=$1 AND ($2 = '' OR t.state = $2)
		   AND (NOT EXISTS (SELECT 1 FROM ota_task_item ti WHERE ti.task_id = t.id)
		        OR EXISTS (SELECT 1 FROM ota_task_item ti
		                   WHERE ti.task_id = t.id`+countClause+`))`,
		append([]any{tenantID, state}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("count ota tasks: %w", err)
	}
	listClause, listArgs := scope.DeviceSubqueryPredicate("device_id", 4)
	rows, err := s.pool.Query(ctx, taskSelect+`
		 WHERE tenant_id=$1 AND ($2 = '' OR state = $2)
		   AND (NOT EXISTS (SELECT 1 FROM ota_task_item ti
		                    WHERE ti.task_id = ota_task.id)
		        OR EXISTS (SELECT 1 FROM ota_task_item ti
		                   WHERE ti.task_id = ota_task.id`+listClause+`))
		 ORDER BY created_at DESC
		 LIMIT $3 OFFSET $4`,
		append([]any{tenantID, state, pageSize, (page - 1) * pageSize},
			listArgs...)...)
	if err != nil {
		return apipage.Page[Task]{}, fmt.Errorf("list ota tasks: %w", err)
	}
	defer rows.Close()
	tasks, err := scanTasks(rows)
	if err != nil {
		return apipage.Page[Task]{}, err
	}
	return apipage.New(tasks, total, page, pageSize), nil
}

// ListItems returns one page of a task's items, narrowed by the caller's
// rbac.DataScope. Per-device rollout progress is device-derived data: firmware
// versions and failure states of devices outside the caller's scope must not
// be readable.
func (s *Store) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[Item], error) {
	scope := rbac.ScopeFromContext(ctx)

	countClause, countArgs := scope.DeviceSubqueryPredicate("device_id", 2)
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ota_task_item
		 WHERE tenant_id=$1 AND task_id=$2`+countClause,
		append([]any{tenantID, taskID}, countArgs...)...).Scan(&total); err != nil {
		return apipage.Page[Item]{}, fmt.Errorf("count ota items: %w", err)
	}
	listClause, listArgs := scope.DeviceSubqueryPredicate("device_id", 4)
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id=$1 AND task_id=$2`+listClause+` ORDER BY created_at, id
		 LIMIT $3 OFFSET $4`,
		append([]any{tenantID, taskID, pageSize, (page - 1) * pageSize},
			listArgs...)...)
	if err != nil {
		return apipage.Page[Item]{}, fmt.Errorf("list ota items: %w", err)
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		return apipage.Page[Item]{}, err
	}
	return apipage.New(items, total, page, pageSize), nil
}

// deviceItemColumns is the item column list table-qualified for the
// item/task join used by ListItemsByDevice (see itemColumns for order).
const deviceItemColumns = `i.id::text, i.task_id::text, i.tenant_id::text,
       COALESCE(i.gateway_id::text,''), i.device_id::text, i.state,
       COALESCE(i.result_code,''), i.progress_pct, i.bytes_received, i.batch_no,
       COALESCE(i.message,''),
       i.created_at, i.updated_at, i.finished_at,
       t.firmware_version, t.state`

// ListItemsByDevice returns one page of rollout rows for a single device,
// newest first, joined to the parent task for its target version and
// aggregate state. The query is tenant-scoped; the device route handler
// separately proves the device belongs to the tenant and is in the caller's
// data scope.
func (s *Store) ListItemsByDevice(ctx context.Context, tenantID, deviceID string, page, pageSize int) (apipage.Page[DeviceItem], error) {
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM ota_task_item
		 WHERE tenant_id=$1 AND device_id=$2::uuid`,
		tenantID, deviceID).Scan(&total); err != nil {
		return apipage.Page[DeviceItem]{}, fmt.Errorf("count device ota items: %w", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+deviceItemColumns+`
		 FROM ota_task_item i
		 JOIN ota_task t ON t.id = i.task_id
		 WHERE i.tenant_id=$1 AND i.device_id=$2::uuid
		 ORDER BY i.created_at DESC, i.id DESC
		 LIMIT $3 OFFSET $4`,
		tenantID, deviceID, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[DeviceItem]{}, fmt.Errorf("list device ota items: %w", err)
	}
	defer rows.Close()
	out := make([]DeviceItem, 0, pageSize)
	for rows.Next() {
		var row DeviceItem
		if err := rows.Scan(&row.ID, &row.TaskID, &row.TenantID, &row.GatewayID,
			&row.DeviceID, &row.State, &row.ResultCode, &row.ProgressPct,
			&row.BytesReceived, &row.BatchNo, &row.Message,
			&row.CreatedAt, &row.UpdatedAt, &row.FinishedAt,
			&row.FirmwareVersion, &row.TaskState); err != nil {
			return apipage.Page[DeviceItem]{}, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[DeviceItem]{}, err
	}
	return apipage.New(out, total, page, pageSize), nil
}

// PendingItems loads not-yet-dispatched OPEN items of a task (batch 0 legacy
// plus the currently opened positive batch; deferred batches carry a negative
// batch number and are excluded).
func (s *Store) PendingItems(ctx context.Context, tenantID, taskID string) ([]Item, error) {
	rows, err := s.pool.Query(ctx, itemSelect+`
		 WHERE tenant_id=$1 AND task_id=$2 AND state=$3 AND batch_no >= 0
		 ORDER BY gateway_id, id`,
		tenantID, taskID, ItemStatePending)
	if err != nil {
		return nil, fmt.Errorf("list pending ota items: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// OpenBatch marks the next deferred batch (stored with batch_no = -next) as
// open (positive number) and returns the newly opened items. It is a no-op
// when no deferred batch remains.
func (s *Store) OpenBatch(ctx context.Context, tenantID, taskID string, next int) ([]Item, error) {
	rows, err := s.pool.Query(ctx,
		`UPDATE ota_task_item SET batch_no=$3
		 WHERE tenant_id=$1 AND task_id=$2 AND batch_no=(0-$3)::int
		 RETURNING `+itemColumns,
		tenantID, taskID, next)
	if err != nil {
		return nil, fmt.Errorf("open ota batch: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// HasDeferredBatch reports whether the given batch number is still waiting
// (items stored as -no).
func (s *Store) HasDeferredBatch(ctx context.Context, tenantID, taskID string, no int) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM ota_task_item
		 WHERE tenant_id=$1 AND task_id=$2 AND batch_no=(0-$3)::int)`,
		tenantID, taskID, no).Scan(&exists); err != nil {
		return false, fmt.Errorf("check deferred batch: %w", err)
	}
	return exists, nil
}

// ClaimDevices marks dispatched items as downloading (in flight) and appends
// a platform timeline event per newly claimed device. Items already past
// pending are skipped, so a duplicate dispatch is harmless. It returns the
// (device_id, event) pairs actually claimed so the manager can fan them out.
func (s *Store) ClaimDevices(ctx context.Context, taskID string, deviceIDs []string) ([]TimelineEvent, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin ota claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`UPDATE ota_task_item
		 SET state=$1, updated_at=now()
		 WHERE task_id=$2 AND device_id = ANY($3::uuid[]) AND state=$4
		 RETURNING tenant_id::text, task_id::text, COALESCE(gateway_id::text,''), device_id::text`,
		ItemStateDownloading, taskID, deviceIDs, ItemStatePending)
	if err != nil {
		return nil, fmt.Errorf("claim ota items: %w", err)
	}
	type ref struct{ tenant, task, gateway, device string }
	var claimed []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.tenant, &r.task, &r.gateway, &r.device); err != nil {
			rows.Close()
			return nil, err
		}
		claimed = append(claimed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("claim ota items: %w", err)
	}

	events := make([]TimelineEvent, 0, len(claimed))
	for _, r := range claimed {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID:     r.task,
			TenantID:   r.tenant,
			DeviceID:   r.device,
			GatewayID:  r.gateway,
			Phase:      ItemStateDownloading,
			ResultCode: ResultNone,
			Source:     SourcePlatform,
			Message:    "dispatched to gateway",
		})
		if err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit ota claim: %w", err)
	}
	return events, nil
}

// MarkRunning transitions a pending task to running.
func (s *Store) MarkRunning(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE ota_task
		 SET state=$1, started_at=COALESCE(started_at, now())
		 WHERE tenant_id=$2 AND id=$3 AND state=$4
		 RETURNING `+taskColumns,
		TaskStateRunning, tenantID, taskID, TaskStatePending)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetTask(ctx, tenantID, taskID)
	}
	return task, err
}

// ApplyProgress records one per-device progress frame under the task's success
// policy and appends timeline events at phase boundaries.
//
// Non-terminal phases update progress/bytes/message but leave the item open;
// succeeded/failed close it. Movement is forward-only (a late frame cannot
// move an item backwards), and a frame for an already-terminal item is a no-op.
// Under confirmed_after_reboot, 'updated'/'rebooting' only advance the phase;
// the manager has already normalized a version-checked 'confirmed' frame to
// succeeded (or a mismatch to failed/version_mismatch) before calling here.
//
// Frames are accepted from open items only. A canceled task still accepts
// terminal results from devices that had begun applying; non-terminal frames
// are rejected so a canceled rollout cannot be resumed. It returns the
// refreshed aggregate, the updated item and any appended timeline rows.
func (s *Store) ApplyProgress(ctx context.Context, ev Event) (ProgressResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ProgressResult{}, fmt.Errorf("begin ota progress: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := getTaskInTx(ctx, tx, ev.TenantID, ev.TaskID)
	if err != nil {
		return ProgressResult{}, err
	}

	cur, err := itemInTx(ctx, tx, ev.TenantID, ev.TaskID, ev.DeviceID)
	if err != nil {
		return ProgressResult{}, err
	}
	if IsItemTerminal(cur.State) {
		// Duplicate terminal report: idempotent no-op.
		return ProgressResult{Task: task, Item: cur}, nil
	}
	// A rollout that is fully settled (completed/partial/failed) no longer
	// accepts frames; a canceled one only accepts the final result of a device
	// that was already applying.
	if task.State != TaskStatePending && task.State != TaskStateRunning &&
		task.State != TaskStatePaused &&
		task.State != TaskStateCanceled && task.State != TaskStateAborted {
		return ProgressResult{Task: task, Item: cur}, nil
	}

	newState, resultCode, terminal := resolveFrame(task, cur, ev)
	if (task.State == TaskStateCanceled || task.State == TaskStateAborted) && !terminal {
		return ProgressResult{Task: task, Item: cur}, nil
	}

	pct := ev.ProgressPct
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	if terminal {
		if newState == ItemStateFailed {
			pct = 0
		} else {
			pct = 100
		}
	}
	bytes := ev.Bytes
	if bytes < 0 {
		bytes = 0
	}

	openList := sqlStringList(openItemStates)
	var row pgx.Row
	if terminal {
		row = tx.QueryRow(ctx,
			`UPDATE ota_task_item
			 SET state=$1, result_code=$2, progress_pct=$3, bytes_received=GREATEST(bytes_received,$4),
			     message=$5, updated_at=now(), finished_at=now()
			 WHERE task_id=$6 AND tenant_id=$7 AND device_id=$8
			   AND state IN (`+openList+`)
			 RETURNING `+itemColumns,
			newState, nullableCode(resultCode), pct, bytes, nullable(ev.Message),
			ev.TaskID, ev.TenantID, ev.DeviceID)
	} else {
		// Forward-only guard: the SQL ordinal helper prevents moving an item
		// to an earlier phase. bytes use GREATEST so a stale frame cannot
		// reduce the count.
		row = tx.QueryRow(ctx,
			`UPDATE ota_task_item
			 SET state=$1, result_code=$2, progress_pct=GREATEST(progress_pct,$3),
			     bytes_received=GREATEST(bytes_received,$4), message=$5, updated_at=now()
			 WHERE task_id=$6 AND tenant_id=$7 AND device_id=$8
			   AND state IN (`+openList+`)
			   AND ota_progress_order($1) >= ota_progress_order(state)
			 RETURNING `+itemColumns,
			newState, nullableCode(resultCode), pct, bytes, nullable(ev.Message),
			ev.TaskID, ev.TenantID, ev.DeviceID)
	}
	updated, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		// Lost the forward-only race or the item moved terminal concurrently;
		// report no change with the current aggregate.
		return ProgressResult{Task: task, Item: cur}, nil
	}
	if err != nil {
		return ProgressResult{}, fmt.Errorf("apply ota progress: %w", err)
	}

	// Append a timeline row only when the item actually crossed a phase
	// boundary or reached a terminal state; a repeated percentage frame for
	// the same phase updates the latest value but is not history.
	var timeline []TimelineEvent
	if newState != cur.State || terminal {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID:        ev.TaskID,
			TenantID:      ev.TenantID,
			DeviceID:      ev.DeviceID,
			GatewayID:     ev.GatewayID,
			Phase:         newState,
			ResultCode:    resultCode,
			ProgressPct:   pct,
			BytesReceived: bytes,
			Message:       ev.Message,
			Source:        eventSource(ev.Source),
		})
		if err != nil {
			return ProgressResult{}, err
		}
		timeline = append(timeline, e)
	}

	if !terminal {
		if err := tx.Commit(ctx); err != nil {
			return ProgressResult{}, fmt.Errorf("commit ota progress: %w", err)
		}
		return ProgressResult{Task: task, Item: updated, Events: timeline, Changed: true}, nil
	}

	recomputed, err := recomputeInTx(ctx, tx, ev.TenantID, ev.TaskID, freezeStateFor(task.State))
	if err != nil {
		return ProgressResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ProgressResult{}, fmt.Errorf("commit ota progress: %w", err)
	}
	return ProgressResult{Task: recomputed, Item: updated, Events: timeline, Changed: true}, nil
}

// resolveFrame maps an incoming frame to the item state to store, its result
// code and whether it is terminal, applying the task success policy.
func resolveFrame(task Task, cur Item, ev Event) (state, resultCode string, terminal bool) {
	resultCode = normalizeCode(ev.ResultCode)
	switch ev.State {
	case ItemStateFailed:
		return ItemStateFailed, nonNoneCode(resultCode), true
	case ItemStateSucceeded:
		return ItemStateSucceeded, resultCode, true
	case ItemStateConfirmed:
		// The post-reboot confirmation is only authoritative under the
		// confirmed_after_reboot policy with a matching running version.
		if task.SuccessPolicy == PolicyConfirmedAfterReboot {
			v := strings.TrimSpace(ev.FirmwareVersion)
			switch {
			case v == "":
				// No version evidence yet: keep waiting at the reboot phase.
				return keepPhase(cur, ItemStateRebooting), ResultNone, false
			case v != task.FirmwareVersion:
				return ItemStateFailed, ResultVersionMismatch, true
			default:
				return ItemStateSucceeded, resultCode, true
			}
		}
		return ItemStateSucceeded, resultCode, true
	case ItemStateUpdated:
		if task.SuccessPolicy == PolicyDeviceReported {
			// Immediate-success policy: the edge's "applied" report finalizes.
			return ItemStateSucceeded, resultCode, true
		}
		// Reboot-confirmation policy: stay on the intermediate phase.
		return ItemStateUpdated, ResultNone, false
	default:
		if isProgressState(ev.State) {
			return ev.State, ResultNone, false
		}
		// Unknown phase: treat as a download progress nudge without leaving
		// the current phase.
		if cur.State == ItemStatePending {
			return ItemStateDownloading, ResultNone, false
		}
		return cur.State, ResultNone, false
	}
}

// keepPhase returns target unless the item is already past it, in which case
// the current phase is retained (forward-only).
func keepPhase(cur Item, target string) string {
	if phaseOrdinal(cur.State) >= phaseOrdinal(target) {
		return cur.State
	}
	return target
}

// phaseOrdinal mirrors the ota_progress_order() SQL helper for in-process
// comparisons; keep in sync with migration 0053.
func phaseOrdinal(state string) int {
	switch state {
	case ItemStatePending:
		return 0
	case ItemStateInitiated:
		return 1
	case ItemStateDownloading:
		return 2
	case ItemStateDownloaded:
		return 3
	case ItemStateVerified:
		return 4
	case ItemStateUpdating:
		return 5
	case ItemStateUpdated:
		return 6
	case ItemStateRebooting:
		return 7
	case ItemStateConfirmed:
		return 8
	default:
		return 9
	}
}

// normalizeCode maps an empty/unknown code to the safe default ("none" for a
// normal frame).
func normalizeCode(code string) string {
	code = strings.TrimSpace(code)
	if code == "" {
		return ResultNone
	}
	if ValidResultCode(code) {
		return code
	}
	return ResultUnknown
}

// nonNoneCode ensures a failure carries a real reason code.
func nonNoneCode(code string) string {
	if code == "" || code == ResultNone {
		return ResultUnknown
	}
	return code
}

// nullableCode stores an empty/none result code as SQL NULL.
func nullableCode(code string) any {
	if code == "" || code == ResultNone {
		return nil
	}
	return code
}

// eventSource validates the reported origin of a timeline event.
func eventSource(source string) string {
	switch source {
	case SourcePlatform, SourceDevice, SourceGateway:
		return source
	default:
		return SourceDevice
	}
}

// sqlStringList renders Go state strings for an inline SQL IN (...) list. The
// values are package constants, never user input, so interpolation is safe.
func sqlStringList(values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = "'" + v + "'"
	}
	return strings.Join(quoted, ",")
}

// getTaskInTx loads a task inside an existing transaction.
func getTaskInTx(ctx context.Context, tx pgx.Tx, tenantID, taskID string) (Task, error) {
	row := tx.QueryRow(ctx, taskSelect+` WHERE tenant_id=$1 AND id=$2`,
		tenantID, taskID)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, ErrNotFound
	}
	return task, err
}

// itemInTx loads one item inside an existing transaction.
func itemInTx(ctx context.Context, tx pgx.Tx, tenantID, taskID, deviceID string) (Item, error) {
	row := tx.QueryRow(ctx,
		itemSelect+` WHERE tenant_id=$1 AND task_id=$2 AND device_id=$3`,
		tenantID, taskID, deviceID)
	item, err := scanItem(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Item{}, ErrNotTarget
	}
	return item, err
}

// insertEventInTx appends one timeline row, allocating the next per-device
// sequence atomically.
func insertEventInTx(ctx context.Context, tx pgx.Tx, e TimelineEvent) (TimelineEvent, error) {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	row := tx.QueryRow(ctx,
		`INSERT INTO ota_task_event
		   (id, tenant_id, task_id, device_id, gateway_id, seq, phase,
		    result_code, progress_pct, bytes_received, message, source, created_at)
		 VALUES ($1,$2,$3,$4,$5,
		         COALESCE((SELECT max(seq)+1 FROM ota_task_event
		                   WHERE task_id=$3 AND device_id=$4),0),
		         $6,$7,$8,$9,$10,$11, now())
		 RETURNING seq, created_at`,
		e.ID, e.TenantID, e.TaskID, e.DeviceID, nullableUUID(e.GatewayID),
		e.Phase, nullableCode(e.ResultCode), e.ProgressPct,
		nullableInt64(e.BytesReceived), nullable(e.Message), e.Source)
	if err := row.Scan(&e.Seq, &e.CreatedAt); err != nil {
		return TimelineEvent{}, fmt.Errorf("insert ota task event: %w", err)
	}
	return e, nil
}

// nullableInt64 stores a zero byte count as SQL NULL for timeline rows.
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// FailDevices terminalizes the named devices of a task that are still open
// (pending at dispatch), used when one gateway is offline or its downlink
// write fails. Items already claimed on other gateways are untouched. Each
// newly failed device gets a platform timeline event. It returns the refreshed
// task, whether anything changed and the appended timeline rows.
func (s *Store) FailDevices(ctx context.Context, tenantID, taskID string, deviceIDs []string, reason string) (Task, []TimelineEvent, bool, error) {
	if len(deviceIDs) == 0 {
		task, err := s.GetTask(ctx, tenantID, taskID)
		return task, nil, false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, nil, false, fmt.Errorf("begin fail ota devices: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`UPDATE ota_task_item
		 SET state=$1, result_code=$2, message=$3, updated_at=now(), finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5 AND device_id = ANY($6::uuid[])
		   AND state='pending'
		 RETURNING tenant_id::text, task_id::text, COALESCE(gateway_id::text,''), device_id::text`,
		ItemStateFailed, ResultUnknown, reason, taskID, tenantID, deviceIDs)
	if err != nil {
		return Task{}, nil, false, fmt.Errorf("fail ota devices: %w", err)
	}
	type ref struct{ tenant, task, gateway, device string }
	var failed []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.tenant, &r.task, &r.gateway, &r.device); err != nil {
			rows.Close()
			return Task{}, nil, false, err
		}
		failed = append(failed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Task{}, nil, false, fmt.Errorf("fail ota devices: %w", err)
	}
	if len(failed) == 0 {
		task, err := getTaskInTx(ctx, tx, tenantID, taskID)
		if err != nil {
			return Task{}, nil, false, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Task{}, nil, false, err
		}
		return task, nil, false, nil
	}

	events := make([]TimelineEvent, 0, len(failed))
	for _, r := range failed {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID:     r.task,
			TenantID:   r.tenant,
			DeviceID:   r.device,
			GatewayID:  r.gateway,
			Phase:      ItemStateFailed,
			ResultCode: ResultUnknown,
			Message:    reason,
			Source:     SourcePlatform,
		})
		if err != nil {
			return Task{}, nil, false, err
		}
		events = append(events, e)
	}
	task, err := recomputeInTx(ctx, tx, tenantID, taskID, "")
	if err != nil {
		return Task{}, nil, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, nil, false, fmt.Errorf("commit fail ota devices: %w", err)
	}
	return task, events, true, nil
}

// FailOpenItems terminalizes items still open for a task (offline dispatch or
// a deployment backstop). The task aggregate is recomputed and returned.
func (s *Store) FailOpenItems(ctx context.Context, tenantID, taskID, reason string) (Task, error) {
	if _, err := s.pool.Exec(ctx,
		`UPDATE ota_task_item
		 SET state=$1, result_code=$2, message=$3, updated_at=now(), finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5
		   AND state IN (`+sqlStringList(openItemStates)+`)`,
		ItemStateFailed, ResultUnknown, reason, taskID, tenantID); err != nil {
		return Task{}, fmt.Errorf("fail open ota items: %w", err)
	}
	return Recompute(ctx, s.pool, tenantID, taskID)
}

// CancelTask applies the safe, best-effort cancel (ADR-0006c):
//   - pending items become skipped (never dispatched / never pulled);
//   - items that had started downloading but had not begun applying
//     (initiated/downloading/downloaded/verified) become canceled, which makes
//     the relay reject any further package fetch and progress;
//   - items already applying (updating/updated/rebooting) and terminal items
//     are left untouched and their final result is still collected.
//
// The task itself is pinned to canceled. It returns the refreshed task, the
// appended per-device timeline rows and the items that were still applying
// (so the caller can report how many devices could not be stopped).
func (s *Store) CancelTask(ctx context.Context, tenantID, taskID, reason string) (Task, []TimelineEvent, []Item, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, nil, nil, fmt.Errorf("begin ota cancel: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	task, err := getTaskInTx(ctx, tx, tenantID, taskID)
	if err != nil {
		return Task{}, nil, nil, err
	}
	if task.State != TaskStatePending && task.State != TaskStateRunning &&
		task.State != TaskStatePaused && task.State != TaskStateCanceled {
		return Task{}, nil, nil, ErrNotCancellable
	}

	// Phases that must never be interrupted: the device is already writing
	// flash or rebooting and a cancel cannot be made safe.
	applying := []string{ItemStateUpdating, ItemStateUpdated, ItemStateRebooting}
	skipPhases := []string{ItemStatePending}
	cancelPhases := []string{
		ItemStateInitiated, ItemStateDownloading, ItemStateDownloaded,
		ItemStateVerified,
	}

	var changed []Item
	skipped, err := bulkCancelUpdate(ctx, tx, ItemStateSkipped,
		tenantID, taskID, reason, skipPhases)
	if err != nil {
		return Task{}, nil, nil, err
	}
	canceled, err := bulkCancelUpdate(ctx, tx, ItemStateCanceled,
		tenantID, taskID, reason, cancelPhases)
	if err != nil {
		return Task{}, nil, nil, err
	}
	changed = append(changed, skipped...)
	changed = append(changed, canceled...)

	// Append a timeline row for every item that changed phase.
	events := make([]TimelineEvent, 0, len(changed))
	for _, it := range changed {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID:     taskID,
			TenantID:   tenantID,
			DeviceID:   it.DeviceID,
			GatewayID:  it.GatewayID,
			Phase:      it.State,
			ResultCode: ResultCanceled,
			Message:    reason,
			Source:     SourcePlatform,
		})
		if err != nil {
			return Task{}, nil, nil, err
		}
		events = append(events, e)
	}

	// Items that keep applying and will still report a final result.
	applyingRows, err := tx.Query(ctx,
		itemSelect+` WHERE tenant_id=$1 AND task_id=$2 AND state IN (`+
			sqlStringList(applying)+`)`,
		tenantID, taskID)
	if err != nil {
		return Task{}, nil, nil, fmt.Errorf("list applying ota items: %w", err)
	}
	stillApplying, err := scanItems(applyingRows)
	if err != nil {
		return Task{}, nil, nil, err
	}

	// Pin the aggregate to canceled even if some devices remain open.
	if _, err := tx.Exec(ctx,
		`UPDATE ota_task SET state=$1, started_at=COALESCE(started_at,now())
		 WHERE tenant_id=$2 AND id=$3`,
		TaskStateCanceled, tenantID, taskID); err != nil {
		return Task{}, nil, nil, fmt.Errorf("cancel ota task: %w", err)
	}
	recomputed, err := recomputeInTx(ctx, tx, tenantID, taskID, TaskStateCanceled)
	if err != nil {
		return Task{}, nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, nil, nil, fmt.Errorf("commit ota cancel: %w", err)
	}
	return recomputed, events, stillApplying, nil
}

// bulkCancelUpdate moves items currently in fromStates to a terminal skip/
// canceled state and returns the updated items.
func bulkCancelUpdate(ctx context.Context, tx pgx.Tx, target, tenantID, taskID, reason string, fromStates []string) ([]Item, error) {
	rows, err := tx.Query(ctx,
		`UPDATE ota_task_item
		 SET state=$1, result_code=$2, message=$3, updated_at=now(), finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5
		   AND state IN (`+sqlStringList(fromStates)+`)
		 RETURNING `+itemColumns,
		target, ResultCanceled, reason, taskID, tenantID)
	if err != nil {
		return nil, fmt.Errorf("cancel ota items to %s: %w", target, err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// ListEvents returns the append-only phase timeline of a task, newest last,
// optionally narrowed to one device. The result is capped; timelines are short
// (phase boundaries only), so a single page suffices for the drawer.
func (s *Store) ListEvents(ctx context.Context, tenantID, taskID, deviceID string, limit int) ([]TimelineEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, task_id::text, tenant_id::text, device_id::text,
		        COALESCE(gateway_id::text,''), seq, phase,
		        COALESCE(result_code,''), progress_pct,
		        COALESCE(bytes_received,0), COALESCE(message,''), source, created_at
		 FROM ota_task_event
		 WHERE tenant_id=$1 AND task_id=$2
		   AND ($3 = '' OR device_id::text=$3)
		 ORDER BY device_id, seq
		 LIMIT $4`,
		tenantID, taskID, deviceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list ota events: %w", err)
	}
	defer rows.Close()
	out := []TimelineEvent{}
	for rows.Next() {
		var e TimelineEvent
		if err := rows.Scan(&e.ID, &e.TaskID, &e.TenantID, &e.DeviceID,
			&e.GatewayID, &e.Seq, &e.Phase, &e.ResultCode, &e.ProgressPct,
			&e.BytesReceived, &e.Message, &e.Source, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// OpenTaskRefs returns (tenant,task) pairs for tasks not yet terminal, used by
// startup recovery.
func (s *Store) OpenTaskRefs(ctx context.Context) ([]Ref, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id::text, id::text FROM ota_task
		 WHERE state IN ($1,$2,$3)`, TaskStatePending, TaskStateRunning, TaskStatePaused)
	if err != nil {
		return nil, fmt.Errorf("list open ota tasks: %w", err)
	}
	defer rows.Close()
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

// Ref identifies a (tenant, task) pair during recovery.
type Ref struct {
	tenant string
	task   string
}

// SetTaskBatchNo records the currently open batch and the earliest time the
// next batch may open.
func (s *Store) SetTaskBatchNo(ctx context.Context, tenantID, taskID string, no int, nextAt *time.Time) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE ota_task SET batch_no=$3, next_batch_at=$4
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, taskID, no, nextAt); err != nil {
		return fmt.Errorf("set ota batch no: %w", err)
	}
	return nil
}

// FailStalledItems terminalizes open items whose last update is older than
// before. Only items that actually started (phases list, never 'pending') are
// watchdog-eligible; pending items wait for a gateway ONLINE redispatch. It
// appends one platform timeline event per failed item and recomputes the
// aggregate.
func (s *Store) FailStalledItems(ctx context.Context, tenantID, taskID, reasonCode, message string, phases []string, before time.Time) (Task, []TimelineEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, nil, fmt.Errorf("begin watchdog: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`UPDATE ota_task_item
		 SET state=$1, result_code=$2, message=$3, updated_at=now(), finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5 AND batch_no >= 0
		   AND state IN (`+sqlStringList(phases)+`)
		   AND updated_at < $6
		 RETURNING tenant_id::text, task_id::text, COALESCE(gateway_id::text,''), device_id::text`,
		ItemStateFailed, reasonCode, message, taskID, tenantID, before)
	if err != nil {
		return Task{}, nil, fmt.Errorf("fail stalled items: %w", err)
	}
	type ref struct{ tenant, task, gateway, device string }
	var failed []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.tenant, &r.task, &r.gateway, &r.device); err != nil {
			rows.Close()
			return Task{}, nil, err
		}
		failed = append(failed, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Task{}, nil, err
	}
	events := make([]TimelineEvent, 0, len(failed))
	for _, r := range failed {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID: r.task, TenantID: r.tenant, DeviceID: r.device,
			GatewayID: r.gateway, Phase: ItemStateFailed, ResultCode: reasonCode,
			Message: message, Source: SourcePlatform,
		})
		if err != nil {
			return Task{}, nil, err
		}
		events = append(events, e)
	}
	task, err := getTaskInTx(ctx, tx, tenantID, taskID)
	if err != nil {
		return Task{}, nil, err
	}
	recomputed, err := recomputeInTx(ctx, tx, tenantID, taskID, freezeStateFor(task.State))
	if err != nil {
		return Task{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, nil, fmt.Errorf("commit watchdog: %w", err)
	}
	return recomputed, events, nil
}

// BatchSettle describes terminal progress over the devices opened so far.
type BatchSettle struct {
	Succeeded int
	Failed    int
	Canceled  int
	Skipped   int
	Open      int
	// ConsecutiveFailures is the trailing run of failed items in creation
	// order across the whole task.
	ConsecutiveFailures int
}

// BatchProgress returns terminal/open counts over items already opened
// (batch_no >= 0) and the trailing consecutive-failure run.
func (s *Store) BatchProgress(ctx context.Context, tenantID, taskID string) (BatchSettle, error) {
	var b BatchSettle
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state='succeeded'),
		        count(*) FILTER (WHERE state='failed'),
		        count(*) FILTER (WHERE state='canceled'),
		        count(*) FILTER (WHERE state='skipped'),
		        count(*) FILTER (WHERE state NOT IN
		            ('succeeded','failed','canceled','skipped'))
		 FROM ota_task_item
		 WHERE tenant_id=$1 AND task_id=$2 AND batch_no >= 0`,
		tenantID, taskID).
		Scan(&b.Succeeded, &b.Failed, &b.Canceled, &b.Skipped, &b.Open); err != nil {
		return b, fmt.Errorf("batch progress: %w", err)
	}
	rows, err := s.pool.Query(ctx,
		`SELECT state FROM ota_task_item
		 WHERE tenant_id=$1 AND task_id=$2 AND batch_no >= 0
		 ORDER BY created_at, id`, tenantID, taskID)
	if err != nil {
		return b, fmt.Errorf("batch failure run: %w", err)
	}
	defer rows.Close()
	var states []string
	for rows.Next() {
		var st string
		if err := rows.Scan(&st); err != nil {
			return b, err
		}
		states = append(states, st)
	}
	for i := len(states) - 1; i >= 0; i-- {
		if states[i] == ItemStateFailed {
			b.ConsecutiveFailures++
			continue
		}
		if states[i] == ItemStateSucceeded || states[i] == ItemStateCanceled ||
			states[i] == ItemStateSkipped {
			break
		}
		// open states do not break the trailing run
	}
	return b, rows.Err()
}

// AbortTask auto-aborts a rollout on a threshold: every not-started item
// (pending, including deferred negative-batch items) becomes skipped, while
// devices already downloading/applying finish undisturbed. The aggregate is
// pinned to aborted.
func (s *Store) AbortTask(ctx context.Context, tenantID, taskID, reason string) (Task, []TimelineEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, nil, fmt.Errorf("begin abort: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`UPDATE ota_task_item
		 SET state=$1, result_code=$2, message=$3, updated_at=now(), finished_at=now()
		 WHERE task_id=$4 AND tenant_id=$5 AND state='pending'
		 RETURNING tenant_id::text, task_id::text, COALESCE(gateway_id::text,''), device_id::text`,
		ItemStateSkipped, ResultNone, reason, taskID, tenantID)
	if err != nil {
		return Task{}, nil, fmt.Errorf("abort pending items: %w", err)
	}
	type ref struct{ tenant, task, gateway, device string }
	var skipped []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.tenant, &r.task, &r.gateway, &r.device); err != nil {
			rows.Close()
			return Task{}, nil, err
		}
		skipped = append(skipped, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Task{}, nil, err
	}
	events := make([]TimelineEvent, 0, len(skipped))
	for _, r := range skipped {
		e, err := insertEventInTx(ctx, tx, TimelineEvent{
			TaskID: r.task, TenantID: r.tenant, DeviceID: r.device,
			GatewayID: r.gateway, Phase: ItemStateSkipped, ResultCode: ResultNone,
			Message: reason, Source: SourcePlatform,
		})
		if err != nil {
			return Task{}, nil, err
		}
		events = append(events, e)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE ota_task SET state=$1, abort_reason=$3,
		     next_batch_at=NULL, finished_at=now()
		 WHERE tenant_id=$2 AND id=$4`,
		TaskStateAborted, tenantID, reason, taskID); err != nil {
		return Task{}, nil, fmt.Errorf("mark aborted: %w", err)
	}
	task, err := recomputeInTx(ctx, tx, tenantID, taskID, TaskStateAborted)
	if err != nil {
		return Task{}, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Task{}, nil, fmt.Errorf("commit abort: %w", err)
	}
	return task, events, nil
}

// PauseTask holds a rollout: no new batches open; applying devices continue.
func (s *Store) PauseTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE ota_task SET state=$1
		 WHERE tenant_id=$2 AND id=$3 AND state IN ($4,$5)
		 RETURNING `+taskColumns,
		TaskStatePaused, tenantID, taskID, TaskStatePending, TaskStateRunning)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetTask(ctx, tenantID, taskID)
	}
	return task, err
}

// ResumeTask releases a hold and moves the task back to running.
func (s *Store) ResumeTask(ctx context.Context, tenantID, taskID string) (Task, error) {
	row := s.pool.QueryRow(ctx,
		`UPDATE ota_task SET state=$1
		 WHERE tenant_id=$2 AND id=$3 AND state=$4
		 RETURNING `+taskColumns,
		TaskStateRunning, tenantID, taskID, TaskStatePaused)
	task, err := scanTask(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetTask(ctx, tenantID, taskID)
	}
	return task, err
}

// Recompute recalculates aggregate counts/state for a task.
func Recompute(ctx context.Context, q pgQuerier, tenantID, taskID string) (Task, error) {
	return recomputeInTx(ctx, q, tenantID, taskID, "")
}

// pgQuerier is satisfied by both *pgxpool.Pool and pgx.Tx.
type pgQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// recomputeInTx recalculates the task aggregate. freezeState non-empty pins
// the aggregate to that state (canceled by human cancel, aborted by threshold,
// paused by operator hold) even while devices that already started applying
// keep reporting terminal results and updating the counters.
func recomputeInTx(ctx context.Context, q pgQuerier, tenantID, taskID, freezeState string) (Task, error) {
	var succeeded, failed, canceled, skipped, total int
	if err := q.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE state=$1),
		        count(*) FILTER (WHERE state=$2),
		        count(*) FILTER (WHERE state=$3),
		        count(*) FILTER (WHERE state=$4),
		        count(*)
		 FROM ota_task_item WHERE task_id=$5`,
		ItemStateSucceeded, ItemStateFailed, ItemStateCanceled,
		ItemStateSkipped, taskID).
		Scan(&succeeded, &failed, &canceled, &skipped, &total); err != nil {
		return Task{}, fmt.Errorf("count ota progress: %w", err)
	}
	finished := total > 0 && succeeded+failed+canceled+skipped == total
	state := TaskStateRunning
	switch {
	case total == 0:
		state = TaskStateFailed
	case freezeState != "":
		state = freezeState
	case succeeded == total:
		state = TaskStateCompleted
	case failed == total:
		state = TaskStateFailed
	case canceled+skipped == total:
		state = TaskStateCanceled
	case finished:
		state = TaskStatePartial
	}
	finishedExpr := ""
	args := []any{succeeded, failed, canceled, skipped, state, taskID, tenantID}
	if finished {
		// A pinned task (canceled/aborted/paused) with devices still applying
		// is kept open until the last result arrives; only fully-settled
		// tasks get finished_at.
		finishedExpr = ", finished_at=COALESCE(finished_at, now())"
	}
	row := q.QueryRow(ctx,
		`UPDATE ota_task
		 SET succeeded_count=$1, failed_count=$2, canceled_count=$3,
		     skipped_count=$4, state=$5`+finishedExpr+`
		 WHERE id=$6 AND tenant_id=$7
		 RETURNING `+taskColumns, args...)
	task, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("recompute ota task: %w", err)
	}
	return task, nil
}

// nullable stores an empty message as SQL NULL.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

var taskSelect = `SELECT ` + taskColumns + ` FROM ota_task`
var itemSelect = `SELECT ` + itemColumns + ` FROM ota_task_item`

const taskColumns = `id::text, tenant_id::text, created_by::text,
       COALESCE(firmware_id::text,''), firmware_version,
       package_url, package_sha256, package_size, http_headers::text, state,
       success_policy,
       total_count, succeeded_count, failed_count, canceled_count, skipped_count,
       COALESCE(error,''), created_at, started_at, finished_at,
       target_mode, COALESCE(target_selector::text,'{}'),
       batch_percent, batch_size, batch_pause_seconds, batch_no,
       next_batch_at,
       failure_abort_percent, max_consecutive_failures,
       stall_timeout_seconds, confirm_timeout_seconds,
       COALESCE(abort_reason,'')`

const itemColumns = `id::text, task_id::text, tenant_id::text,
       COALESCE(gateway_id::text,''), device_id::text, state,
       COALESCE(result_code,''), progress_pct, bytes_received, batch_no,
       COALESCE(message,''),
       created_at, updated_at, finished_at`

func scanTask(row rowScanner) (Task, error) {
	var t Task
	var createdBy, failure, headers, firmwareID, selectorText, abortReason *string
	var startedAt, finishedAt, nextBatchAt *time.Time
	var batchPercent, batchSize, failureAbortPercent, maxConsecutiveFailures *int
	var stallTimeout, confirmTimeout *int
	var batchPause, batchNo int
	if err := row.Scan(&t.ID, &t.TenantID, &createdBy, &firmwareID,
		&t.FirmwareVersion,
		&t.PackageURL, &t.PackageSHA256, &t.PackageSize, &headers,
		&t.State, &t.SuccessPolicy,
		&t.TotalCount, &t.SucceededCount, &t.FailedCount,
		&t.CanceledCount, &t.SkippedCount, &failure,
		&t.CreatedAt, &startedAt, &finishedAt,
		&t.TargetMode, &selectorText,
		&batchPercent, &batchSize, &batchPause, &batchNo, &nextBatchAt,
		&failureAbortPercent, &maxConsecutiveFailures,
		&stallTimeout, &confirmTimeout, &abortReason); err != nil {
		return Task{}, err
	}
	if firmwareID != nil {
		t.FirmwareID = *firmwareID
	}
	if createdBy != nil {
		t.CreatedBy = *createdBy
	}
	if failure != nil {
		t.Error = *failure
	}
	if abortReason != nil {
		t.AbortReason = *abortReason
	}
	if headers != nil && *headers != "" {
		_ = json.Unmarshal([]byte(*headers), &t.HTTPHeaders)
	}
	if selectorText != nil && *selectorText != "" {
		_ = json.Unmarshal([]byte(*selectorText), &t.TargetSelector)
	}
	t.Policy = RolloutPolicy{
		BatchPercent:           batchPercent,
		BatchSize:              batchSize,
		BatchPauseSeconds:      batchPause,
		FailureAbortPercent:    failureAbortPercent,
		MaxConsecutiveFailures: maxConsecutiveFailures,
		StallTimeoutSeconds:    stallTimeout,
		ConfirmTimeoutSeconds:  confirmTimeout,
	}
	t.BatchNo = batchNo
	t.NextBatchAt = nextBatchAt
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
	var resultCode, message *string
	var finishedAt *time.Time
	if err := row.Scan(&it.ID, &it.TaskID, &it.TenantID, &it.GatewayID,
		&it.DeviceID, &it.State, &resultCode, &it.ProgressPct,
		&it.BytesReceived, &it.BatchNo, &message,
		&it.CreatedAt, &it.UpdatedAt, &finishedAt); err != nil {
		return Item{}, err
	}
	if resultCode != nil {
		it.ResultCode = *resultCode
	}
	if message != nil {
		it.Message = *message
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
