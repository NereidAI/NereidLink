// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batch.go implements asynchronous batch command execution. The REST call
// returns after target expansion and persistence; execution continues in a
// background goroutine and publishes task/item progress on Core NATS for
// WebSocket delivery.

package command

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/apipage"
)

// BatchRequest is a batch command request.
type BatchRequest struct {
	// Name is the catalog command name.
	Name string

	// Direction is read, write or empty for catalog default.
	Direction string

	// Params is the shared JSON parameter document.
	Params []byte

	// TimeoutMS optionally overrides catalog timeout.
	TimeoutMS int64

	// GroupID targets a device-group subtree.
	GroupID string

	// DeviceIDs is the explicit device selector.
	DeviceIDs []string
}

// BatchSender dispatches individual commands. *API implements it.
type BatchSender interface {
	Send(ctx context.Context, tenantID, deviceID, name, direction string, params []byte, timeoutMS int64) (Ack, error)
}

// BatchManager creates and executes batch tasks.
type BatchManager struct {
	store      *BatchStore
	sender     BatchSender
	nc         *nats.Conn
	maxBody    int
	maxTargets int
	mu         sync.Mutex
	inProgress map[string]struct{}
}

// NewBatchManager constructs a batch manager.
func NewBatchManager(store *BatchStore, sender BatchSender, nc *nats.Conn, maxBody, maxTargets int) *BatchManager {
	if maxBody <= 0 {
		maxBody = DefaultMaxBodyBytes
	}
	if maxTargets <= 0 {
		maxTargets = DefaultMaxBatchTargets
	}
	return &BatchManager{
		store:      store,
		sender:     sender,
		nc:         nc,
		maxBody:    maxBody,
		maxTargets: maxTargets,
		inProgress: map[string]struct{}{},
	}
}

// TaskEvent is the progress frame published over Core NATS.
type TaskEvent struct {
	// Type is "task" or "item".
	Type string `json:"type"`

	// Task is the current aggregate state.
	Task Task `json:"task"`

	// Item is present for an item transition.
	Item *TaskItem `json:"item,omitempty"`
}

const (
	batchEventTask = "task"
	batchEventItem = "item"

	// DefaultMaxBatchTargets is applied when configuration omits the limit.
	DefaultMaxBatchTargets = 1000
)

// batchTaskSubject returns the per-tenant progress subject.
func batchTaskSubject(tenantID string) string {
	return "nl.command.task." + tenantID
}

// publishEvent sends one progress frame.
func (m *BatchManager) publishEvent(event TaskEvent) {
	if m.nc == nil {
		return
	}
	if err := m.nc.Publish(batchTaskSubject(event.Task.TenantID), mustJSON(event)); err != nil {
		slog.Error("batch command progress publish failed", "task", event.Task.ID, "error", err)
	}
}

// Create expands and persists a batch task, then starts execution.
func (m *BatchManager) Create(ctx context.Context, tenantID, createdBy string, request BatchRequest) (Task, error) {
	if request.Name == "" {
		return Task{}, fmt.Errorf("%w: name is required", ErrInvalidArgument)
	}
	params := request.Params
	if len(params) == 0 {
		params = []byte("{}")
	}
	if len(params) > m.maxBody {
		return Task{}, ErrBodyTooLarge
	}

	targetType, groupID, deviceIDs, err := m.expandTarget(ctx, tenantID, request)
	if err != nil {
		return Task{}, err
	}
	if len(deviceIDs) == 0 {
		return Task{}, fmt.Errorf("%w: no active target devices", ErrInvalidArgument)
	}
	if len(deviceIDs) > m.maxTargets {
		return Task{}, fmt.Errorf("%w: target exceeds %d devices", ErrInvalidArgument, m.maxTargets)
	}

	now := time.Now().UTC()
	task := Task{
		ID:         uuid.NewString(),
		TenantID:   tenantID,
		CreatedBy:  createdBy,
		Name:       request.Name,
		Params:     params,
		TimeoutMS:  request.TimeoutMS,
		Direction:  request.Direction,
		TargetType: targetType,
		GroupID:    groupID,
		State:      TaskStatePending,
		TotalCount: len(deviceIDs),
		CreatedAt:  now,
	}
	items := make([]TaskItem, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		items = append(items, TaskItem{
			ID:        uuid.NewString(),
			TaskID:    task.ID,
			TenantID:  tenantID,
			DeviceID:  deviceID,
			State:     ItemStatePending,
			CreatedAt: now,
		})
	}
	if err := m.store.Create(ctx, task, items); err != nil {
		return Task{}, err
	}
	m.publishEvent(TaskEvent{Type: batchEventTask, Task: task})
	m.launch(task.TenantID, task.ID)
	return task, nil
}

// expandTarget resolves exactly one requested target.
func (m *BatchManager) expandTarget(ctx context.Context, tenantID string, request BatchRequest) (string, string, []string, error) {
	hasGroup := request.GroupID != ""
	hasDevices := len(request.DeviceIDs) > 0
	if hasGroup == hasDevices {
		return "", "", nil, fmt.Errorf("%w: provide exactly one group or device selector", ErrInvalidArgument)
	}
	if hasGroup {
		if _, err := uuid.Parse(request.GroupID); err != nil {
			return "", "", nil, fmt.Errorf("%w: invalid group id", ErrInvalidArgument)
		}
		devices, err := m.store.ExpandGroup(ctx, tenantID, request.GroupID)
		if err != nil {
			return "", "", nil, err
		}
		return TargetGroup, request.GroupID, devices, nil
	}

	requested := uniqueDevices(request.DeviceIDs)
	for _, deviceID := range requested {
		if _, err := uuid.Parse(deviceID); err != nil {
			return "", "", nil, fmt.Errorf("%w: invalid device id", ErrInvalidArgument)
		}
	}
	devices, err := m.store.ResolveDevices(ctx, tenantID, requested)
	if err != nil {
		return "", "", nil, err
	}
	if len(devices) != len(requested) {
		return "", "", nil, fmt.Errorf("%w: selector contains inactive or unknown devices", ErrInvalidArgument)
	}
	return TargetDevices, "", devices, nil
}

// List returns a page of tenant tasks.
func (m *BatchManager) List(ctx context.Context, tenantID string, page, pageSize int) (apipage.Page[Task], error) {
	return m.store.ListTasks(ctx, tenantID, page, pageSize)
}

// Get returns one tenant task.
func (m *BatchManager) Get(ctx context.Context, tenantID, taskID string) (Task, error) {
	return m.store.GetTask(ctx, tenantID, taskID)
}

// ListItems returns one page of task items.
func (m *BatchManager) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[TaskItem], error) {
	return m.store.ListItems(ctx, tenantID, taskID, page, pageSize)
}

// Recover starts unfinished tasks after a process restart.
func (m *BatchManager) Recover(ctx context.Context) error {
	tasks, err := m.store.UnfinishedTasks(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		m.launch(task.TenantID, task.ID)
	}
	return nil
}

// launch starts one task unless it is already active on this process.
func (m *BatchManager) launch(tenantID, taskID string) {
	m.mu.Lock()
	_, active := m.inProgress[taskID]
	if !active {
		m.inProgress[taskID] = struct{}{}
	}
	m.mu.Unlock()
	if !active {
		go m.execute(tenantID, taskID)
	}
}

// execute processes all pending items and publishes aggregate progress.
func (m *BatchManager) execute(tenantID, taskID string) {
	defer func() {
		m.mu.Lock()
		delete(m.inProgress, taskID)
		m.mu.Unlock()
	}()
	ctx := context.Background()

	if err := m.store.FailRunningItems(ctx, taskID); err != nil {
		slog.Error("failed to recover batch items", "task", taskID, "error", err)
	}
	if _, err := m.store.MarkRunning(ctx, tenantID, taskID); err != nil {
		slog.Error("failed to start batch task", "task", taskID, "error", err)
		return
	}
	task, err := m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		slog.Error("failed to load batch task", "task", taskID, "error", err)
		return
	}
	m.publishEvent(TaskEvent{Type: batchEventTask, Task: task})

	items, err := m.store.PendingItems(ctx, tenantID, taskID)
	if err != nil {
		slog.Error("failed to list batch items", "task", taskID, "error", err)
		return
	}
	for _, queued := range items {
		item, claimed, err := m.store.ClaimItem(ctx, taskID, queued.ID)
		if err != nil || !claimed {
			continue
		}
		ack, sendErr := m.sender.Send(ctx, tenantID, item.DeviceID, task.Name, task.Direction, task.Params, task.TimeoutMS)
		if sendErr != nil {
			item.State = ItemStateFailed
			item.Error = sendErr.Error()
		} else {
			item.CommandID = ack.ID
			item.Response = ack.Response
			item.State, item.Error = itemOutcome(ack)
		}
		updated, err := m.store.ApplyItemResult(ctx, item)
		if err != nil {
			slog.Error("failed to store batch item result", "task", taskID, "error", err)
			continue
		}
		m.publishEvent(TaskEvent{Type: batchEventItem, Task: updated, Item: &item})
	}

	final, err := m.store.RecomputeTask(ctx, tenantID, taskID)
	if err != nil {
		slog.Error("failed to finish batch task", "task", taskID, "error", err)
		return
	}
	m.publishEvent(TaskEvent{Type: batchEventTask, Task: final})
}

// itemOutcome classifies an individual command ack.
func itemOutcome(ack Ack) (string, string) {
	switch ack.Status {
	case StatusSucceeded, StatusDelivered, StatusQueued:
		return ItemStateSucceeded, ""
	default:
		failure := ack.Error
		if failure == "" {
			failure = ack.Status
		}
		return ItemStateFailed, failure
	}
}

// uniqueDevices returns sorted-independent unique selector entries.
func uniqueDevices(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
