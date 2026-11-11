// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// manager.go is the transport-independent OTA orchestrator. It validates and
// persists a firmware rollout, resolves target devices to their gateways,
// dispatches one firmware frame per online gateway, fails targets on offline
// gateways immediately, and correlates per-device progress frames arriving on
// Core NATS. Unlike probes an OTA task is long-lived: there is no short
// deadline or 24h retention — a target stays open until the edge reports a
// terminal state.

package ota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/apipage"
	"github.com/nereidai/nereidlink/internal/presence"
)

// ErrInvalidArgument marks a malformed OTA request.
var ErrInvalidArgument = errors.New("invalid ota argument")

// ErrTooManyDevices is returned when a rollout exceeds the device cap.
var ErrTooManyDevices = errors.New("ota task exceeds device limit")

// DefaultMaxDevices bounds one rollout's device count.
const DefaultMaxDevices = 1000

// FirmwareMeta is the repository metadata the manager needs to build a
// platform-hosted task. Defined here (instead of importing internal/firmware)
// to keep the dependency direction one-way; main.go adapts the firmware
// service to this interface.
type FirmwareMeta struct {
	Version string
	SHA256  string
	Size    int64
	// ProfileID is the model the package is bound to ("" = profile-agnostic).
	// It drives the server-side compatibility check (ADR-0006d §3).
	ProfileID string
}

// FirmwareResolver loads one repository package for task creation.
type FirmwareResolver interface {
	ResolveFirmware(ctx context.Context, tenantID, packageID string) (FirmwareMeta, error)
}

// Locator resolves the live location of a gateway. *presence.Service
// satisfies it.
type Locator interface {
	Lookup(ctx context.Context, id presence.Identity) (presence.Location, error)
}

// SetFirmwareResolver wires the repository resolver after construction. It is
// used when the OTA manager is created before the blob-backed firmware service.
func (m *Manager) SetFirmwareResolver(resolver FirmwareResolver) {
	m.mu.Lock()
	m.firmware = resolver
	m.mu.Unlock()
}

// Tuning holds the dispatch limits and default rollout safety policy.
type Tuning struct {
	// MaxDevices bounds a single rollout.
	MaxDevices int
	// DefaultPolicy fills policy fields a request leaves unset (ADR-0006d §5).
	DefaultPolicy RolloutPolicy
}

// intPtr returns a pointer to v.
func intPtr(v int) *int { return &v }

// DefaultTuning returns the documented defaults: conservative abort
// thresholds and watchdogs, single full batch (batch fields nil).
func DefaultTuning() Tuning {
	return Tuning{
		MaxDevices: DefaultMaxDevices,
		DefaultPolicy: RolloutPolicy{
			FailureAbortPercent:    intPtr(20),
			MaxConsecutiveFailures: intPtr(5),
			StallTimeoutSeconds:    intPtr(1800),
			ConfirmTimeoutSeconds:  intPtr(7200),
		},
	}
}

// Manager orchestrates OTA tasks.
type Manager struct {
	store    *Store
	nc       *nats.Conn
	locator  Locator
	firmware FirmwareResolver
	tune     Tuning
	log      *slog.Logger
	instance string

	mu      sync.Mutex
	started map[string]struct{}
}

// NewManager constructs the manager. firmware may be nil when the firmware
// repository (object storage) is disabled; repository-hosted task creation
// then fails, but external-URL tasks keep working.
func NewManager(store *Store, nc *nats.Conn, locator Locator, firmware FirmwareResolver, tune Tuning, log *slog.Logger) *Manager {
	if log == nil {
		log = slog.Default()
	}
	if tune.MaxDevices <= 0 {
		tune = DefaultTuning()
	}
	return &Manager{
		store:    store,
		nc:       nc,
		locator:  locator,
		firmware: firmware,
		tune:     tune,
		log:      log,
		instance: "ota-" + uuid.NewString(),
		started:  map[string]struct{}{},
	}
}

// Create validates a rollout, resolves the target devices, persists a pending
// task and dispatches it. actor is the principal id for audit. A request with
// FirmwareID creates a platform-hosted task (version/sha/size come from the
// firmware repository; devices download via the authenticated relay);
// otherwise it is a legacy external-URL task.
func (m *Manager) Create(ctx context.Context, tenantID, actor string, req Request) (Task, error) {
	policy, err := m.resolvePolicy(req.Policy)
	if err != nil {
		return Task{}, err
	}

	hosted := strings.TrimSpace(req.FirmwareID) != ""
	if hosted {
		if m.firmware == nil {
			return Task{}, fmt.Errorf("%w: firmware repository unavailable", ErrInvalidArgument)
		}
	} else if err := validateExternalPackage(req.Package); err != nil {
		return Task{}, err
	}
	policyName := strings.TrimSpace(req.SuccessPolicy)
	if policyName == "" {
		policyName = PolicyDeviceReported
	}
	if !ValidSuccessPolicy(policyName) {
		return Task{}, fmt.Errorf("%w: invalid success_policy %q", ErrInvalidArgument, policyName)
	}

	targetMode := TargetModeExplicit
	var meta FirmwareMeta
	if hosted {
		meta, err = m.firmware.ResolveFirmware(ctx, tenantID, strings.TrimSpace(req.FirmwareID))
		if err != nil {
			return Task{}, err
		}
	}

	// Resolve targets either from the explicit UUID list or the dynamic
	// selector; the result is snapshotted into items immediately.
	targets, err := m.resolveTargets(ctx, tenantID, req, meta, hosted)
	if err != nil {
		return Task{}, err
	}
	if len(targets) == 0 {
		return Task{}, fmt.Errorf("%w: no matching active devices", ErrInvalidArgument)
	}
	if len(targets) > m.tune.MaxDevices {
		return Task{}, fmt.Errorf("%w: max %d devices per task", ErrTooManyDevices, m.tune.MaxDevices)
	}

	now := time.Now().UTC()
	task := Task{
		ID:              uuid.NewString(),
		TenantID:        tenantID,
		CreatedBy:       actor,
		FirmwareVersion: req.Package.Version,
		PackageURL:      req.Package.URL,
		PackageSHA256:   strings.ToLower(req.Package.SHA256),
		PackageSize:     req.Package.Size,
		HTTPHeaders:     req.Package.Headers,
		State:           TaskStatePending,
		SuccessPolicy:   policyName,
		TargetMode:      targetMode,
		TargetSelector:  req.Selector,
		Policy:          policy,
		TotalCount:      len(targets),
		CreatedAt:       now,
	}
	targetVersion := ""
	if hosted {
		task.FirmwareID = strings.TrimSpace(req.FirmwareID)
		task.FirmwareVersion = meta.Version
		targetVersion = meta.Version
		task.PackageSHA256 = strings.ToLower(meta.SHA256)
		task.PackageSize = meta.Size
		task.HTTPHeaders = nil
		// Relative, authenticated path served by this server.
		task.PackageURL = DevicePackagePath(task.ID)
	} else {
		targetVersion = req.Package.Version
	}

	// Assign stable sequence numbers to targets and open the first batch now;
	// already-current devices are skipped terminal. Batch numbers are 1-based
	// for batched tasks and 0 for the legacy one-shot/full dispatch; a
	// not-yet-opened future batch is stored with the NEGATIVE of its number so
	// dispatch, pull assignments and counts can tell it apart from batch 0
	// without adding a new item state.
	batched := policy.BatchPercent != nil || policy.BatchSize != nil
	ordered := make([]string, 0, len(targets))
	for id := range targets {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)

	// Determine each target's batch. Non-current items of batch 1 are opened
	// immediately; later batches are stored with a NEGATIVE batch number until
	// the orchestrator opens them. Already-current devices are skipped and
	// keep the positive (informational) batch number.
	batchPlan := buildBatches(len(ordered), policy, batched)
	items := make([]Item, 0, len(targets))
	for i, deviceID := range ordered {
		info := targets[deviceID]
		current := targetVersion != "" && info.FirmwareVersion == targetVersion
		item := Item{
			ID:        uuid.NewString(),
			TaskID:    task.ID,
			TenantID:  tenantID,
			GatewayID: info.GatewayID, // empty for directly connected devices
			DeviceID:  deviceID,
			State:     ItemStatePending,
			BatchNo:   batchPlan[i],
			CreatedAt: now,
			UpdatedAt: now,
		}
		if batchPlan[i] > 1 {
			// Defer open batches until the previous ones settle.
			item.BatchNo = -batchPlan[i]
		}
		if current {
			// already_current: never re-flash a device already reporting the
			// requested version (ADR-0006d §3).
			item.BatchNo = batchPlan[i]
			item.State = ItemStateSkipped
			item.ResultCode = ResultAlreadyCurrent
			item.Message = "device already reports target version"
			item.FinishedAt = &now
		}
		items = append(items, item)
	}

	if err := m.store.Create(ctx, task, items); err != nil {
		return Task{}, err
	}
	created, err := m.store.GetTask(ctx, tenantID, task.ID)
	if err == nil {
		task = created
	}
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	m.launch(tenantID, task.ID)
	return task, nil
}

// resolvePolicy overlays the per-request policy on top of the configured
// defaults: absent pointer fields take the default, present fields override.
func (m *Manager) resolvePolicy(in RolloutPolicy) (RolloutPolicy, error) {
	d := m.tune.DefaultPolicy
	out := RolloutPolicy{
		BatchPercent:           in.BatchPercent,
		BatchSize:              in.BatchSize,
		BatchPauseSeconds:      in.BatchPauseSeconds,
		FailureAbortPercent:    in.FailureAbortPercent,
		MaxConsecutiveFailures: in.MaxConsecutiveFailures,
		StallTimeoutSeconds:    in.StallTimeoutSeconds,
		ConfirmTimeoutSeconds:  in.ConfirmTimeoutSeconds,
	}
	if out.BatchPercent == nil {
		out.BatchPercent = d.BatchPercent
	}
	if out.BatchSize == nil {
		out.BatchSize = d.BatchSize
	}
	if out.FailureAbortPercent == nil {
		out.FailureAbortPercent = d.FailureAbortPercent
	}
	if out.MaxConsecutiveFailures == nil {
		out.MaxConsecutiveFailures = d.MaxConsecutiveFailures
	}
	if out.StallTimeoutSeconds == nil {
		out.StallTimeoutSeconds = d.StallTimeoutSeconds
	}
	if out.ConfirmTimeoutSeconds == nil {
		out.ConfirmTimeoutSeconds = d.ConfirmTimeoutSeconds
	}
	if out.BatchPercent != nil && out.BatchSize != nil {
		return out, fmt.Errorf("%w: batch_percent and batch_size are mutually exclusive", ErrInvalidArgument)
	}
	if p := out.BatchPercent; p != nil && (*p < 1 || *p > 100) {
		return out, fmt.Errorf("%w: batch_percent must be 1..100", ErrInvalidArgument)
	}
	if s := out.BatchSize; s != nil && *s < 1 {
		return out, fmt.Errorf("%w: batch_size must be positive", ErrInvalidArgument)
	}
	return out, nil
}

// cumulativeOpened returns how many of total devices are opened through
// batch n (1-based).
func cumulativeOpened(total int, p RolloutPolicy, n int) int {
	if n < 1 {
		return 0
	}
	if p.BatchSize != nil {
		cum := n * *p.BatchSize
		if cum > total {
			return total
		}
		return cum
	}
	if p.BatchPercent != nil {
		cum := (total*(*p.BatchPercent)*n + 99) / 100
		if cum > total {
			return total
		}
		return cum
	}
	return total
}

// buildBatches assigns a 1-based batch number to every target index
// deterministically. Non-batched policies put everything in batch 0.
func buildBatches(total int, p RolloutPolicy, batched bool) []int {
	plan := make([]int, total)
	if !batched {
		for i := range plan {
			plan[i] = 0
		}
		return plan
	}
	n := 1
	for i := 0; i < total; i++ {
		for cumulativeOpened(total, p, n) <= i {
			n++
		}
		plan[i] = n
	}
	return plan
}

// resolveTargets validates the target selection, enforces server-side
// profile compatibility for hosted packages and returns resolved targets.
func (m *Manager) resolveTargets(ctx context.Context, tenantID string, req Request, meta FirmwareMeta, hosted bool) (map[string]TargetInfo, error) {
	if len(req.Devices) > 0 && !req.Selector.Empty() {
		return nil, fmt.Errorf("%w: provide either devices or selector, not both", ErrInvalidArgument)
	}
	var (
		targets map[string]TargetInfo
		err     error
	)
	if !req.Selector.Empty() {
		targets, err = m.store.ResolveSelectorTargets(ctx, tenantID, req.Selector)
		if err != nil {
			return nil, err
		}
	} else {
		devices := dedupe(req.Devices)
		if err := validateDevices(devices, m.tune); err != nil {
			return nil, err
		}
		targets, err = m.store.ResolveTargets(ctx, tenantID, devices)
		if err != nil {
			return nil, err
		}
		for _, id := range devices {
			if _, ok := targets[id]; !ok {
				return nil, fmt.Errorf("%w: device %s is unknown or inactive",
					ErrInvalidArgument, id)
			}
		}
	}
	// Server-side model compatibility (ADR-0006d §3): a profile-bound package
	// only targets devices of that model; a profile-agnostic package matches
	// any model. External-URL tasks have no binding to enforce.
	if hosted && meta.ProfileID != "" {
		for id, info := range targets {
			if info.ProfileID != meta.ProfileID {
				return nil, fmt.Errorf("%w: device %s model is incompatible with this firmware",
					ErrInvalidArgument, id)
			}
		}
	}
	return targets, nil
}

// launch runs one task's dispatch on a background goroutine. It is a named
// method (rather than an inline closure over the request context) because
// dispatch must outlive the HTTP request; execute derives its own background
// context. A duplicate launch for an already-running task is skipped.
func (m *Manager) launch(tenantID, taskID string) {
	go m.execute(tenantID, taskID)
}

// validateDevices checks the target id list.
func validateDevices(devices []string, tune Tuning) error {
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
	return nil
}

// validateExternalPackage checks an external-URL firmware descriptor.
func validateExternalPackage(pkg Package) error {
	if strings.TrimSpace(pkg.Version) == "" {
		return fmt.Errorf("%w: package.version is required", ErrInvalidArgument)
	}
	u, err := url.Parse(pkg.URL)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("%w: package.url must be an absolute http(s) URL", ErrInvalidArgument)
	}
	if !ValidSHA256(pkg.SHA256) {
		return fmt.Errorf("%w: package.sha256 must be 64 hex chars", ErrInvalidArgument)
	}
	if pkg.Size < 0 {
		return fmt.Errorf("%w: package.size must be non-negative", ErrInvalidArgument)
	}
	return nil
}

// Get returns one tenant task.
func (m *Manager) Get(ctx context.Context, tenantID, taskID string) (Task, error) {
	return m.store.GetTask(ctx, tenantID, taskID)
}

// List returns a page of tenant tasks, optionally filtered by aggregate state.
func (m *Manager) List(ctx context.Context, tenantID, state string, page, pageSize int) (apipage.Page[Task], error) {
	return m.store.ListTasks(ctx, tenantID, state, page, pageSize)
}

// ListItems returns a page of task items.
func (m *Manager) ListItems(ctx context.Context, tenantID, taskID string, page, pageSize int) (apipage.Page[Item], error) {
	return m.store.ListItems(ctx, tenantID, taskID, page, pageSize)
}

// ListItemsByDevice returns a page of rollout rows for one device, joined to
// the parent task for its target version and aggregate state.
func (m *Manager) ListItemsByDevice(ctx context.Context, tenantID, deviceID string, page, pageSize int) (apipage.Page[DeviceItem], error) {
	return m.store.ListItemsByDevice(ctx, tenantID, deviceID, page, pageSize)
}

// OpenAssignments returns the open repository-hosted jobs for one direct
// device so it can poll for firmware work.
func (m *Manager) OpenAssignments(ctx context.Context, tenantID, deviceID string) ([]DirectAssignment, error) {
	return m.store.OpenAssignments(ctx, tenantID, deviceID)
}

// AuthorizeDownload validates the caller and returns the backing blob for the
// authenticated package relay.
func (m *Manager) AuthorizeDownload(ctx context.Context, tenantID, callerID, taskID string) (DownloadGrant, error) {
	return m.store.AuthorizeDownload(ctx, tenantID, callerID, taskID)
}

// ApplyDeviceProgress records one direct-device progress frame and writes the
// firmware version back on success.
func (m *Manager) ApplyDeviceProgress(ctx context.Context, ev Event) (Task, bool, error) {
	if ev.Source == "" {
		ev.Source = SourceDevice
	}
	res, err := m.store.ApplyProgress(ctx, ev)
	if err != nil {
		return Task{}, false, err
	}
	if !res.Changed {
		return res.Task, false, nil
	}
	m.finalizeProgress(ev, res)
	return res.Task, true, nil
}

// finalizeProgress performs the post-commit side effects shared by direct and
// gateway-reported frames: version write-back on success and WebSocket
// fan-out of the updated item, any new timeline rows and the aggregate when it
// reaches a terminal state.
func (m *Manager) finalizeProgress(ev Event, res ProgressResult) {
	if res.Item.State == ItemStateSucceeded {
		if fwErr := m.store.SetDeviceFirmwareVersion(context.Background(),
			ev.TenantID, ev.DeviceID, res.Task.FirmwareVersion); fwErr != nil {
			m.log.Warn("write back device firmware version failed",
				"device", ev.DeviceID, "task", ev.TaskID, "error", fwErr)
		}
	}
	item := res.Item
	m.publish(TaskEvent{Type: eventTypeItem, Task: res.Task, Item: &item})
	for i := range res.Events {
		e := res.Events[i]
		m.publish(TaskEvent{Type: eventTypeEvent, Task: res.Task, Event: &e})
	}
	if isAggregateTerminal(res.Task.State) {
		m.publish(TaskEvent{Type: eventTypeTask, Task: res.Task})
	}
}

// isAggregateTerminal reports a settled task aggregate state.
func isAggregateTerminal(state string) bool {
	switch state {
	case TaskStateCompleted, TaskStatePartial, TaskStateFailed,
		TaskStateCanceled, TaskStateAborted:
		return true
	}
	return false
}

// execute groups pending items by gateway, fails devices on offline gateways
// immediately and dispatches one firmware frame per online gateway.
func (m *Manager) execute(tenantID, taskID string) {
	ctx := context.Background()
	m.mu.Lock()
	if _, running := m.started[taskID]; running {
		m.mu.Unlock()
		return
	}
	m.started[taskID] = struct{}{}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.started, taskID)
		m.mu.Unlock()
	}()

	task, err := m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("ota task load failed", "task", taskID, "error", err)
		return
	}
	if _, err := m.store.MarkRunning(ctx, tenantID, taskID); err != nil {
		m.log.Error("ota task start failed", "task", taskID, "error", err)
		return
	}
	task, err = m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("ota task reload failed", "task", taskID, "error", err)
		return
	}
	// A hold (paused) or auto-abort/cancel must never dispatch new devices.
	if task.State != TaskStatePending && task.State != TaskStateRunning {
		return
	}
	items, err := m.store.PendingItems(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("ota items load failed", "task", taskID, "error", err)
		return
	}
	byGateway := map[string][]string{}
	for _, it := range items {
		if it.GatewayID == "" {
			// Directly connected devices are pull-based: they discover the
			// assignment over the device HTTPS endpoint and download through
			// the relay, so they are not dispatched (or failed) here.
			continue
		}
		byGateway[it.GatewayID] = append(byGateway[it.GatewayID], it.DeviceID)
	}
	for gatewayID, deviceIDs := range byGateway {
		loc, lerr := m.locator.Lookup(ctx, presence.Identity{TenantID: tenantID, DeviceID: gatewayID})
		if lerr != nil || !loc.Found || loc.IngressID == "" {
			// Offline gateways no longer fail their targets (ADR-0006d §4):
			// items stay pending and are dispatched on the gateway's next
			// ONLINE presence event or by the orchestrator tick.
			m.log.Debug("ota dispatch deferred: gateway offline",
				"task", taskID, "gateway", gatewayID, "devices", len(deviceIDs))
			continue
		}
		claimed, err := m.store.ClaimDevices(ctx, taskID, deviceIDs)
		if err != nil {
			m.log.Error("ota claim failed", "task", taskID, "error", err)
			continue
		}
		frame := DispatchFrame{
			TaskID:        task.ID,
			TenantID:      tenantID,
			GatewayID:     gatewayID,
			Devices:       deviceIDs,
			Version:       task.FirmwareVersion,
			URL:           task.PackageURL,
			SHA256:        task.PackageSHA256,
			Size:          task.PackageSize,
			Headers:       task.HTTPHeaders,
			SuccessPolicy: task.SuccessPolicy,
		}
		if err := m.dispatch(loc.IngressID, frame); err != nil {
			m.log.Error("ota dispatch failed", "task", taskID, "gateway", gatewayID, "error", err)
			t, events, changed, ferr := m.store.FailDevices(ctx, tenantID, taskID, deviceIDs,
				"dispatch write failed: "+err.Error())
			if ferr != nil {
				m.log.Error("ota dispatch-fail terminalize failed", "task", taskID, "error", ferr)
			} else if changed {
				m.fanOut(t, events)
			}
			continue
		}
		// Dispatch succeeded: surface the platform 'downloading' (claimed)
		// timeline rows over WebSocket.
		if len(claimed) > 0 {
			m.fanOut(task, claimed)
		}
	}
}

// fanOut publishes one item refresh and the appended timeline rows for a
// platform-driven transition (claim or offline fail), plus the aggregate when
// it settles.
func (m *Manager) fanOut(task Task, events []TimelineEvent) {
	for i := range events {
		e := events[i]
		item := Item{
			ID:            "",
			TaskID:        e.TaskID,
			TenantID:      e.TenantID,
			GatewayID:     e.GatewayID,
			DeviceID:      e.DeviceID,
			State:         e.Phase,
			ResultCode:    e.ResultCode,
			ProgressPct:   e.ProgressPct,
			BytesReceived: e.BytesReceived,
			Message:       e.Message,
		}
		m.publish(TaskEvent{Type: eventTypeItem, Task: task, Item: &item})
		m.publish(TaskEvent{Type: eventTypeEvent, Task: task, Event: &events[i]})
	}
	if isAggregateTerminal(task.State) {
		m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	}
}

// dispatch publishes one gateway frame to the owning ingress.
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

// applyEvent records one per-device progress frame reported by a gateway.
func (m *Manager) applyEvent(ev Event) {
	if ev.Source == "" {
		ev.Source = SourceGateway
	}
	res, err := m.store.ApplyProgress(context.Background(), ev)
	if err != nil {
		m.log.Error("ota progress apply failed", "task", ev.TaskID, "device", ev.DeviceID, "error", err)
		return
	}
	if !res.Changed {
		return
	}
	m.finalizeProgress(ev, res)
}

// ListEvents returns the append-only phase timeline of a task, optionally
// narrowed to one device.
func (m *Manager) ListEvents(ctx context.Context, tenantID, taskID, deviceID string, limit int) ([]TimelineEvent, error) {
	return m.store.ListEvents(ctx, tenantID, taskID, deviceID, limit)
}

// Cancel applies the safe best-effort cancel of a rollout (ADR-0006c): targets
// that never started are skipped and targets still short of applying are
// canceled (the package relay then refuses them); devices already applying
// finish undisturbed and their result is still collected. No cancel frame is
// pushed to gateways in this slice. It returns the refreshed task and the
// number of devices that could not be stopped because they were applying.
func (m *Manager) Cancel(ctx context.Context, tenantID, taskID, actor string) (Task, int, error) {
	reason := "canceled by " + actor
	task, events, stillApplying, err := m.store.CancelTask(ctx, tenantID, taskID, reason)
	if err != nil {
		return Task{}, 0, err
	}
	m.fanOut(task, events)
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	return task, len(stillApplying), nil
}

// Run subscribes to per-device progress and re-dispatches open tasks after a
// restart. It blocks until ctx is cancelled.
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
		return fmt.Errorf("subscribe ota events: %w", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	if err := m.Recover(context.Background()); err != nil {
		m.log.Error("ota recovery failed", "error", err)
	}

	// Batch/canary orchestrator: presence-driven redispatch, watchdogs,
	// thresholds and batch advancement (ADR-0006d §4-5).
	orchErr := make(chan error, 1)
	go func() { orchErr <- m.runOrchestrator(ctx) }()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-orchErr:
		return err
	}
}

// Recover re-launches tasks left open by a previous process so their still
// pending items get dispatched to gateways that are now online. Items already
// claimed (downloading/...) are left open awaiting the edge's next progress
// frame; OTA has no platform-side timeout.
func (m *Manager) Recover(ctx context.Context) error {
	open, err := m.store.OpenTaskRefs(ctx)
	if err != nil {
		return err
	}
	for _, r := range open {
		m.launch(r.tenant, r.task)
	}
	return nil
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
		m.log.Error("ota progress publish failed", "task", ev.Task.ID, "error", err)
	}
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
