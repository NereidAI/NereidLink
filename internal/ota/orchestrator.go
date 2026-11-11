// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// orchestrator.go runs the rollout safety layer (ADR-0006d): it opens batches
// incrementally with an observation pause, auto-aborts on failure thresholds,
// fails stalled items via watchdogs, holds/resumes rollouts and re-dispatches
// a gateway's pending items when it comes back online. All state changes are
// conditional UPDATEs, so duplicate ticks/events across manager replicas are
// safe. Legacy tasks with NULL policy columns have no batching/thresholds and
// keep their original single-shot behaviour.

package ota

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/nereidai/nereidlink/internal/presence"
)

// presenceWildcard subscribes the manager to every gateway/device boundary.
const presenceWildcard = "nl.presence.*.*"

// orchestrateInterval is the watchdog/batch evaluation tick.
const orchestrateInterval = 15 * time.Second

// runOrchestrator subscribes to presence ONLINE events and ticks the policy
// evaluation loop until ctx is cancelled.
func (m *Manager) runOrchestrator(ctx context.Context) error {
	if m.nc == nil {
		<-ctx.Done()
		return ctx.Err()
	}
	sub, err := m.nc.Subscribe(presenceWildcard, func(msg *nats.Msg) {
		var ev presence.Event
		if json.Unmarshal(msg.Data, &ev) != nil {
			return
		}
		if ev.State != "ONLINE" {
			return
		}
		m.redispatchGateway(ev.TenantID, ev.DeviceID)
	})
	if err != nil {
		return err
	}
	defer func() { _ = sub.Unsubscribe() }()

	ticker := time.NewTicker(orchestrateInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.evaluateOpenTasks(ctx)
		}
	}
}

// redispatchGateway launches one dispatch pass per task that still has pending
// items bound to the gateway that just came online.
func (m *Manager) redispatchGateway(tenantID, gatewayID string) {
	taskIDs, err := m.store.TasksWithPendingForGateway(context.Background(), tenantID, gatewayID)
	if err != nil {
		m.log.Error("ota presence redispatch lookup failed",
			"gateway", gatewayID, "error", err)
		return
	}
	for _, taskID := range taskIDs {
		m.launch(tenantID, taskID)
	}
}

// evaluateOpenTasks applies watchdog timeouts, abort thresholds and batch
// advancement to every running task.
func (m *Manager) evaluateOpenTasks(ctx context.Context) {
	refs, err := m.store.OpenTaskRefs(ctx)
	if err != nil {
		m.log.Error("ota orchestrator open-task load failed", "error", err)
		return
	}
	for _, r := range refs {
		m.evaluateOne(ctx, r.tenant, r.task)
	}
}

// evaluateOne runs one evaluation pass for one task.
func (m *Manager) evaluateOne(ctx context.Context, tenantID, taskID string) {
	task, err := m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		return
	}
	switch task.State {
	case TaskStatePending, TaskStateRunning:
	default:
		// paused/canceled/aborted/terminal tasks do not advance batches or
		// auto-abort; canceled/aborted still collect late device results via
		// the normal progress path.
		return
	}

	now := time.Now().UTC()

	// 1. Confirmation watchdog: rebooting without confirmation too long.
	if secs := task.Policy.ConfirmTimeoutSeconds; secs != nil && *secs > 0 {
		before := now.Add(-time.Duration(*secs) * time.Second)
		t, events, err := m.store.FailStalledItems(ctx, tenantID, taskID,
			ResultRebootTimeout, "reboot confirmation timed out",
			[]string{ItemStateRebooting}, before)
		if err != nil {
			m.log.Error("ota confirm watchdog failed", "task", taskID, "error", err)
		} else if len(events) > 0 {
			m.fanOut(t, events)
		}
	}

	// 2. Stall watchdog: any in-progress item silent too long.
	if secs := task.Policy.StallTimeoutSeconds; secs != nil && *secs > 0 {
		before := now.Add(-time.Duration(*secs) * time.Second)
		phases := []string{
			ItemStateInitiated, ItemStateDownloading, ItemStateDownloaded,
			ItemStateVerified, ItemStateUpdating, ItemStateUpdated,
		}
		t, events, err := m.store.FailStalledItems(ctx, tenantID, taskID,
			ResultDownloadNoProgress, "no progress within timeout",
			phases, before)
		if err != nil {
			m.log.Error("ota stall watchdog failed", "task", taskID, "error", err)
		} else if len(events) > 0 {
			m.fanOut(t, events)
		}
	}

	// Reload after watchdog writes.
	task, err = m.store.GetTask(ctx, tenantID, taskID)
	if err != nil {
		return
	}
	prog, err := m.store.BatchProgress(ctx, tenantID, taskID)
	if err != nil {
		m.log.Error("ota batch progress failed", "task", taskID, "error", err)
		return
	}

	// 3. Abort thresholds over currently opened devices.
	if reason := abortReason(task.Policy, prog); reason != "" {
		t, events, err := m.store.AbortTask(ctx, tenantID, taskID, reason)
		if err != nil {
			m.log.Error("ota abort failed", "task", taskID, "error", err)
			return
		}
		m.fanOut(t, events)
		m.publish(TaskEvent{Type: eventTypeTask, Task: t})
		return
	}

	// 4. Batch advancement: only when the opened set is fully settled.
	if prog.Open == 0 && prog.Succeeded+prog.Failed+prog.Canceled+prog.Skipped > 0 {
		hasNext, err := m.store.HasDeferredBatch(ctx, tenantID, taskID, task.BatchNo+1)
		if err != nil {
			m.log.Error("ota next-batch check failed", "task", taskID, "error", err)
			return
		}
		if hasNext {
			// Respect the inter-batch observation window.
			if task.NextBatchAt != nil && now.Before(*task.NextBatchAt) {
				return
			}
			next := task.BatchNo + 1
			if _, err := m.store.OpenBatch(ctx, tenantID, taskID, next); err != nil {
				m.log.Error("ota open batch failed", "task", taskID, "error", err)
				return
			}
			var nextAt *time.Time
			if pause := task.Policy.BatchPauseSeconds; pause > 0 {
				t := now.Add(time.Duration(pause) * time.Second)
				nextAt = &t
			}
			if err := m.store.SetTaskBatchNo(ctx, tenantID, taskID, next, nextAt); err != nil {
				m.log.Error("ota batch bookkeeping failed", "task", taskID, "error", err)
			}
			// Dispatch the newly opened batch now.
			m.launch(tenantID, taskID)
		}
	}
}

// abortReason returns a non-empty abort message when a threshold is reached;
// nil thresholds (legacy tasks) never abort automatically.
func abortReason(p RolloutPolicy, prog BatchSettle) string {
	settled := prog.Succeeded + prog.Failed + prog.Canceled + prog.Skipped
	if settled == 0 {
		return ""
	}
	if p.FailureAbortPercent != nil && *p.FailureAbortPercent > 0 {
		failedPct := prog.Failed * 100 / settled
		if failedPct >= *p.FailureAbortPercent {
			return "aborted: failure rate threshold reached"
		}
	}
	if p.MaxConsecutiveFailures != nil && *p.MaxConsecutiveFailures > 0 &&
		prog.ConsecutiveFailures >= *p.MaxConsecutiveFailures {
		return "aborted: consecutive failure threshold reached"
	}
	return ""
}

// Pause holds a running rollout (no new batches).
func (m *Manager) Pause(ctx context.Context, tenantID, taskID, actor string) (Task, error) {
	task, err := m.store.PauseTask(ctx, tenantID, taskID)
	if err != nil {
		return Task{}, err
	}
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	return task, nil
}

// Resume releases a held rollout and dispatches its open batch.
func (m *Manager) Resume(ctx context.Context, tenantID, taskID, actor string) (Task, error) {
	task, err := m.store.ResumeTask(ctx, tenantID, taskID)
	if err != nil {
		return Task{}, err
	}
	m.publish(TaskEvent{Type: eventTypeTask, Task: task})
	m.launch(tenantID, taskID)
	return task, nil
}
