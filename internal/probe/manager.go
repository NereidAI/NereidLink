// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// manager.go is the transport-independent probe orchestrator. It validates
// and persists an asynchronous probe task, enforces per-principal admission
// limits and a per-device cooldown, groups target devices by owning gateway,
// dispatches one aggregated frame to the ingress node of each online
// gateway, correlates per-device results arriving on Core NATS, times out
// outstanding devices at the task deadline, and publishes progress events
// for WebSocket fan-out.

package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/presence"
)

// ErrInvalidArgument marks a malformed probe request.
var ErrInvalidArgument = errors.New("invalid probe argument")

// ErrTooManyDevices is returned when a task exceeds the device cap.
var ErrTooManyDevices = errors.New("probe task exceeds device limit")

// ErrRateLimited is returned when a principal exceeds the creation QPS.
var ErrRateLimited = errors.New("probe creation rate limit exceeded")

// ErrTooManyConcurrent is returned when a principal has too many open tasks.
var ErrTooManyConcurrent = errors.New("too many concurrent probe tasks")

// ErrCooldown is returned when one of the devices was probed too recently.
var ErrCooldown = errors.New("device probe cooldown active")

// Locator resolves the live location of a gateway. *presence.Service
// satisfies it.
type Locator interface {
	Lookup(ctx context.Context, id presence.Identity) (presence.Location, error)
}

// Tuning holds the admission and timing limits.
type Tuning struct {
	MaxDevices     int
	CreateRate     time.Duration
	MaxConcurrent  int
	Cooldown       time.Duration
	DefaultTimeout time.Duration
	TaskDeadline   time.Duration
	Retention      time.Duration
	SweepInterval  time.Duration
}

// DefaultTuning returns the documented ADR defaults.
func DefaultTuning() Tuning {
	return Tuning{
		MaxDevices:     DefaultMaxDevices,
		CreateRate:     DefaultCreateRate,
		MaxConcurrent:  DefaultMaxConcurrent,
		Cooldown:       DefaultCooldown,
		DefaultTimeout: DefaultTimeoutMS * time.Millisecond,
		TaskDeadline:   DefaultTaskDeadline,
		Retention:      DefaultRetention,
		SweepInterval:  DefaultSweepInterval,
	}
}

// Manager orchestrates probe tasks.
type Manager struct {
	store    *Store
	nc       *nats.Conn
	locator  Locator
	tune     Tuning
	log      *slog.Logger
	instance string

	mu         sync.Mutex
	lastCreate map[string]time.Time
	lastProbe  map[string]time.Time
	deadlines  map[string]*time.Timer
}

// NewManager constructs the manager. instance is this node's id used to tag
// recovery ownership; a random suffix keeps two processes from colliding.
func NewManager(store *Store, nc *nats.Conn, locator Locator, tune Tuning, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if tune.MaxDevices <= 0 {
		tune = DefaultTuning()
	}
	return &Manager{
		store:      store,
		nc:         nc,
		locator:    locator,
		tune:       tune,
		log:        log,
		instance:   "probe-" + uuid.NewString(),
		lastCreate: map[string]time.Time{},
		lastProbe:  map[string]time.Time{},
		deadlines:  map[string]*time.Timer{},
	}
}

// Create validates a request, applies admission control, persists a pending
// task and launches dispatch. actor is the principal key used for the QPS and
// concurrency limits (user id, or tenant for a service account).
func (m *Manager) Create(ctx context.Context, tenantID, actor string, req Request) (Task, error) {
	if err := validateRequest(req, m.tune); err != nil {
		return Task{}, err
	}

	devices := dedupe(req.Devices)
	mode := req.Mode
	if mode == "" {
		mode = ModeFresh
	}

	// Resolve targets before admission so a request fully covered by an
	// existing open task can share it without spending QPS/cooldown quota.
	// ResolveTargets also enforces the caller's data scope: an out-of-scope
	// device is absent from the map and rejected like any invalid target.
	targets, err := m.store.ResolveTargets(ctx, tenantID, devices)
	if err != nil {
		return Task{}, err
	}
	for _, id := range devices {
		if _, ok := targets[id]; !ok {
			return Task{}, fmt.Errorf("%w: device %s is unknown, inactive, not attached to a gateway, or outside your data scope",
				ErrInvalidArgument, id)
		}
	}

	// Cross-request coalescing: when every requested device is already open
	// (pending/running) in one task this principal created in the same mode,
	// replay that task instead of dispatching a duplicate or failing the
	// per-device cooldown. A principal without a UUID subject cannot own a
	// persisted task, so it never coalesces.
	if _, parseErr := uuid.Parse(actor); parseErr == nil {
		if existing, cerr := m.store.FindCoalescible(ctx, tenantID, actor, mode, devices); cerr == nil {
			return existing, nil
		}
	}

	if err := m.admit(ctx, tenantID, actor, devices); err != nil {
		return Task{}, err
	}

	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = m.tune.DefaultTimeout
	}

	now := time.Now().UTC()
	task := Task{
		ID:         uuid.NewString(),
		TenantID:   tenantID,
		CreatedBy:  actor,
		Mode:       mode,
		TimeoutMS:  timeout.Milliseconds(),
		State:      TaskStatePending,
		TotalCount: len(targets),
		CreatedAt:  now,
	}
	items := make([]Item, 0, len(targets))
	for deviceID, gatewayID := range targets {
		items = append(items, Item{
			ID:        uuid.NewString(),
			TaskID:    task.ID,
			TenantID:  tenantID,
			GatewayID: gatewayID,
			DeviceID:  deviceID,
			State:     ItemStatePending,
			CreatedAt: now,
		})
	}
	if err := m.store.Create(ctx, task, items); err != nil {
		return Task{}, err
	}
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	m.launch(tenantID, task.ID)
	return task, nil
}

// validateRequest checks mode, non-empty unique devices and the cap.
func validateRequest(req Request, tune Tuning) error {
	switch req.Mode {
	case "", ModeCached, ModeFresh, ModeStrict:
	default:
		return fmt.Errorf("%w: mode must be cached, fresh or strict", ErrInvalidArgument)
	}
	devices := dedupe(req.Devices)
	if len(devices) == 0 {
		return fmt.Errorf("%w: devices is required", ErrInvalidArgument)
	}
	for _, id := range devices {
		if _, err := uuid.Parse(id); err != nil {
			return fmt.Errorf("%w: invalid device id %s", ErrInvalidArgument, id)
		}
	}
	if len(devices) > tune.MaxDevices {
		return fmt.Errorf("%w: max %d devices per task", ErrTooManyDevices, tune.MaxDevices)
	}
	if req.TimeoutMS < 0 {
		return fmt.Errorf("%w: timeout_ms must be positive", ErrInvalidArgument)
	}
	return nil
}

// admit enforces the per-principal creation QPS, concurrency cap and the
// per-device cooldown. Limits are in-process (no Redis, per architecture): in
// a multi-node deployment each node enforces its own share, which is the same
// trade-off the ingress token buckets use.
func (m *Manager) admit(ctx context.Context, tenantID, actor string, devices []string) error {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()

	key := tenantID + "." + actor
	if last, ok := m.lastCreate[key]; ok && now.Sub(last) < m.tune.CreateRate {
		return ErrRateLimited
	}
	// The DB is authoritative for concurrency so a restart does not reset it.
	if open, err := m.store.CountOpen(ctx, actor); err == nil && open >= m.tune.MaxConcurrent {
		return ErrTooManyConcurrent
	}
	if m.tune.Cooldown > 0 {
		for _, id := range dedupe(devices) {
			if last, ok := m.lastProbe[tenantID+"."+id]; ok && now.Sub(last) < m.tune.Cooldown {
				return fmt.Errorf("%w: device %s", ErrCooldown, id)
			}
		}
	}
	m.lastCreate[key] = now
	for _, id := range dedupe(devices) {
		m.lastProbe[tenantID+"."+id] = now
	}
	return nil
}

// Get returns one tenant task.
func (m *Manager) Get(ctx context.Context, tenantID, taskID string) (Task, error) {
	return m.store.GetTask(ctx, tenantID, taskID)
}

// List returns a page of tenant tasks.
func (m *Manager) List(ctx context.Context, tenantID string, filter TaskFilter, page, pageSize int) (apipage.Page[Task], error) {
	return m.store.ListTasks(ctx, tenantID, filter, page, pageSize)
}

// ListItems returns a page of task items.
func (m *Manager) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[Item], error) {
	return m.store.ListItems(ctx, tenantID, taskID, page, pageSize)
}

// launch runs one task unless a deadline goroutine is already armed.
func (m *Manager) launch(tenantID, taskID string) {
	m.mu.Lock()
	if _, armed := m.deadlines[taskID]; armed {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	go m.execute(tenantID, taskID)
}

// execute groups pending items by gateway, fails devices on offline gateways
// immediately, dispatches one aggregated frame per online gateway and arms a
// deadline that terminalizes any device still outstanding.
func (m *Manager) execute(tenantID, taskID string) {
	ctx := context.Background()

	task, err := m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("probe task load failed", "task", taskID, "error", err)
		return
	}
	if _, err := m.store.MarkRunning(ctx, tenantID, taskID); err != nil {
		m.log.Error("probe task start failed", "task", taskID, "error", err)
		return
	}

	items, err := m.store.PendingItems(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("probe items load failed", "task", taskID, "error", err)
		return
	}

	byGateway := map[string][]string{}
	for _, it := range items {
		byGateway[it.GatewayID] = append(byGateway[it.GatewayID], it.DeviceID)
	}

	timeout := time.Duration(task.TimeoutMS) * time.Millisecond
	dispatched := false
	for gatewayID, deviceIDs := range byGateway {
		loc, lerr := m.locator.Lookup(ctx, presence.Identity{TenantID: tenantID, DeviceID: gatewayID})
		if lerr != nil || !loc.Found || loc.IngressID == "" {
			m.failDevices(tenantID, taskID, gatewayID, deviceIDs, OutcomeNotConnected, "gateway not connected")
			continue
		}
		if err := m.store.ClaimDevices(ctx, taskID, deviceIDs); err != nil {
			m.log.Error("probe claim failed", "task", taskID, "error", err)
			continue
		}
		frame := DispatchFrame{
			ProbeID:   taskID,
			TenantID:  tenantID,
			GatewayID: gatewayID,
			Devices:   deviceIDs,
			Mode:      task.Mode,
			TimeoutMS: task.TimeoutMS,
		}
		if err := m.dispatch(loc.IngressID, frame); err != nil {
			m.failDevices(tenantID, taskID, gatewayID, deviceIDs, OutcomeTransportError, err.Error())
			continue
		}
		dispatched = true
	}
	_ = timeout

	// If every device already reached a terminal state (e.g. all gateways
	// offline), the completion event was emitted while applying them; do not
	// arm a deadline that would publish a duplicate terminal event.
	current, err := m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("probe task reload failed", "task", taskID, "error", err)
		return
	}
	if isTerminal(current.State) || !dispatched {
		return
	}

	// Arm the deadline using the per-device hint plus a small delivery slack,
	// bounded below by the configured task deadline. The edge already receives
	// the per-device timeout_ms hint, so the platform deadline only needs to
	// outlast the slowest accepted response plus one network round-trip.
	deadline := m.tune.TaskDeadline
	if edge := timeout + deadlineSlack; edge > deadline {
		deadline = edge
	}
	timer := time.AfterFunc(deadline, func() { m.expire(tenantID, taskID) })
	m.mu.Lock()
	m.deadlines[taskID] = timer
	m.mu.Unlock()
}

// dispatch publishes one aggregated gateway frame to the owning ingress.
func (m *Manager) dispatch(ingressID string, frame DispatchFrame) error {
	if m.nc == nil {
		return errors.New("messaging unavailable")
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return m.nc.Publish(dispatchSubject(ingressID), raw)
}

// failDevices emits a not-connected/transport failure outcome for each device
// of an unreachable gateway, exactly as if the gateway had reported it.
func (m *Manager) failDevices(tenantID, taskID, gatewayID string, deviceIDs []string, outcome, reason string) {
	for _, deviceID := range deviceIDs {
		m.applyEvent(Event{
			ProbeID:   taskID,
			TenantID:  tenantID,
			GatewayID: gatewayID,
			DeviceID:  deviceID,
			Outcome:   outcome,
			Error:     reason,
		})
	}
}

// applyEvent records one per-device outcome and publishes item + (when
// terminal) task progress events.
func (m *Manager) applyEvent(ev Event) {
	task, changed, err := m.store.ApplyEvent(context.Background(), ev)
	if err != nil {
		m.log.Error("probe result apply failed", "task", ev.ProbeID, "device", ev.DeviceID, "error", err)
		return
	}
	if !changed {
		return
	}
	item := Item{
		TaskID:    ev.ProbeID,
		TenantID:  ev.TenantID,
		GatewayID: ev.GatewayID,
		DeviceID:  ev.DeviceID,
		State:     ItemStateSucceeded,
		Result:    ev.Result,
	}
	if ev.Outcome != OutcomeOK {
		item.State = ItemStateFailed
		item.Error = ev.Error
	}
	m.publish(TaskEvent{Type: eventTypeItem, Task: task, Item: &item})
	if isTerminal(task.State) {
		m.finish(task)
	}
}

// finish disarms the deadline timer once every item is terminal and publishes
// the final aggregate event.
func (m *Manager) finish(task Task) {
	m.mu.Lock()
	if timer, ok := m.deadlines[task.ID]; ok {
		timer.Stop()
		delete(m.deadlines, task.ID)
	}
	m.mu.Unlock()
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
}

// expire terminalizes any device still pending/running at the deadline.
func (m *Manager) expire(tenantID, taskID string) {
	ctx := context.Background()
	task, err := m.store.FailOpenItems(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("probe deadline sweep failed", "task", taskID, "error", err)
		return
	}
	m.mu.Lock()
	delete(m.deadlines, taskID)
	m.mu.Unlock()
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
}

// Run subscribes to per-device outcomes and runs the retention/deadline
// sweeper. It blocks until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	if m.nc == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	sub, err := m.nc.Subscribe(EventsWildcard, func(msg *nats.Msg) {
		var ev Event
		if json.Unmarshal(msg.Data, &ev) != nil {
			return
		}
		m.applyEvent(ev)
	})
	if err != nil {
		return fmt.Errorf("subscribe probe events: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := m.Recover(context.Background()); err != nil {
		m.log.Error("probe recovery failed", "error", err)
	}

	ticker := time.NewTicker(m.tune.SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.sweepRetention(ctx)
		}
	}
}

// Recover re-arms unfinished tasks after a restart and fails items a previous
// process had dispatched but can no longer answer.
func (m *Manager) Recover(ctx context.Context) error {
	running, err := m.store.OpenRefs(ctx, ItemStateRunning)
	if err != nil {
		return err
	}
	for _, r := range running {
		if _, err := m.store.FailOpenItems(ctx, r.tenant, r.task); err != nil {
			m.log.Error("probe recover fail items failed", "task", r.task, "error", err)
		}
	}
	open, err := m.store.OpenTaskRefs(ctx)
	if err != nil {
		return err
	}
	for _, r := range open {
		m.launch(r.tenant, r.task)
	}
	return nil
}

// sweepRetention deletes task results older than the retention window.
func (m *Manager) sweepRetention(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-m.tune.Retention)
	n, err := m.store.DeleteFinishedBefore(ctx, cutoff)
	if err != nil {
		m.log.Error("probe retention sweep failed", "error", err)
		return
	}
	if n > 0 {
		m.log.Info("probe retention sweep removed tasks", "count", n)
	}
}

// publish emits one WebSocket progress frame to the tenant subject.
func (m *Manager) publish(ev TaskEvent) {
	if m.nc == nil {
		return
	}
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := m.nc.Publish(taskSubject(ev.Task.TenantID), raw); err != nil {
		m.log.Error("probe progress publish failed", "task", ev.Task.ID, "error", err)
	}
}

// isTerminal reports a task aggregate state with no outstanding items.
func isTerminal(state string) bool {
	return state == TaskStateCompleted || state == TaskStatePartial || state == TaskStateFailed
}

// dedupe returns the input ids with duplicates removed, first-seen order.
func dedupe(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
