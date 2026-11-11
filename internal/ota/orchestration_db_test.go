// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// orchestration_db_test.go verifies the ADR-0006d rollout orchestration
// persistence: deferred (negative) batch numbers, batch opening, watchdog
// failure, threshold abort and pause/resume.
package ota

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

// seedBatchedTask creates a task whose items carry explicit batch numbers:
// positive/open or negative/deferred, all pending.
func seedBatchedTask(t *testing.T, store *Store, taskID string, gw string, batches map[int][]string) {
	t.Helper()
	now := time.Now().UTC()
	task := Task{
		ID:              taskID,
		TenantID:        otaTestTenant,
		FirmwareVersion: "fw-2",
		PackageURL:      "/v1/me/ota/" + taskID + "/package",
		PackageSHA256:   validSHA,
		PackageSize:     1024,
		State:           TaskStatePending,
		SuccessPolicy:   PolicyDeviceReported,
		Policy: RolloutPolicy{
			BatchPercent:           ptr(50),
			FailureAbortPercent:    ptr(20),
			MaxConsecutiveFailures: ptr(5),
			StallTimeoutSeconds:    ptr(1800),
		},
		CreatedAt: now,
	}
	var items []Item
	for no, devs := range batches {
		for _, dev := range devs {
			items = append(items, Item{
				ID: uuid.NewString(), TaskID: taskID, TenantID: otaTestTenant,
				GatewayID: gw, DeviceID: dev, State: ItemStatePending,
				BatchNo: no, CreatedAt: now, UpdatedAt: now,
			})
		}
	}
	if err := store.Create(context.Background(), task, items); err != nil {
		t.Fatal(err)
	}
}

func TestBatchDeferralAndOpen(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2, d3, d4 := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedBatchedTask(t, store, taskID, gw, map[int][]string{
		1:  {d1, d2},
		-2: {d3, d4},
	})

	pending, err := store.PendingItems(ctx, otaTestTenant, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("first batch pending=%d want 2 (deferred batch excluded)", len(pending))
	}

	has, err := store.HasDeferredBatch(ctx, otaTestTenant, taskID, 2)
	if err != nil || !has {
		t.Fatalf("deferred batch 2 should exist: has=%v err=%v", has, err)
	}

	opened, err := store.OpenBatch(ctx, otaTestTenant, taskID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 2 {
		t.Fatalf("opened batch size=%d want 2", len(opened))
	}
	for _, it := range opened {
		if it.BatchNo != 2 {
			t.Fatalf("opened item batch_no=%d want 2", it.BatchNo)
		}
	}

	pending, _ = store.PendingItems(ctx, otaTestTenant, taskID)
	if len(pending) != 4 {
		t.Fatalf("after open, pending=%d want 4", len(pending))
	}
}

func TestWatchdogFailsStalledItems(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2 := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedBatchedTask(t, store, taskID, gw, map[int][]string{1: {d1, d2}})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDevices(ctx, taskID, []string{d1, d2}); err != nil {
		t.Fatal(err)
	}

	// Nothing stalled yet: a cutoff in the past must not match freshly
	// claimed items (updated_at = now).
	task, events, err := store.FailStalledItems(ctx, otaTestTenant, taskID,
		ResultDownloadNoProgress, "stalled", []string{ItemStateDownloading},
		time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("fresh items must not be watchdogged, got %d", len(events))
	}

	// Backdate one item well beyond the timeout.
	if _, err := pool.Exec(ctx,
		`UPDATE ota_task_item SET updated_at=now() - interval '1 hour'
		 WHERE device_id=$1`, d1); err != nil {
		t.Fatal(err)
	}
	task, events, err = store.FailStalledItems(ctx, otaTestTenant, taskID,
		ResultDownloadNoProgress, "stalled",
		[]string{ItemStateDownloading, ItemStateDownloaded, ItemStateUpdating},
		time.Now().Add(-30*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("watchdog events=%d want 1", len(events))
	}
	if events[0].ResultCode != ResultDownloadNoProgress {
		t.Fatalf("watchdog result=%s want %s", events[0].ResultCode, ResultDownloadNoProgress)
	}
	if task.FailedCount != 1 {
		t.Fatalf("failed count=%d want 1", task.FailedCount)
	}
}

func TestAbortSkipsPendingAndPins(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2, d3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedBatchedTask(t, store, taskID, gw, map[int][]string{
		1:  {d1, d2},
		-2: {d3},
	})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	// d1 is already downloading (applying) and must be left undisturbed;
	// pending d2 and deferred d3 become skipped.
	if _, err := store.ClaimDevices(ctx, taskID, []string{d1}); err != nil {
		t.Fatal(err)
	}

	task, events, err := store.AbortTask(ctx, otaTestTenant, taskID, "threshold")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskStateAborted {
		t.Fatalf("state=%s want aborted", task.State)
	}
	if len(events) != 2 {
		t.Fatalf("abort events=%d want 2 pending->skipped", len(events))
	}
	// Late success from the still-applying d1 must be collected while the
	// aggregate stays pinned to aborted.
	res, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: d1,
		State: ItemStateSucceeded,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || res.Task.State != TaskStateAborted {
		t.Fatalf("late result changed=%v state=%s: must collect but stay aborted",
			res.Changed, res.Task.State)
	}
}

func TestPauseResume(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1 := uuid.NewString()
	taskID := uuid.NewString()
	seedBatchedTask(t, store, taskID, gw, map[int][]string{1: {d1}})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}

	paused, err := store.PauseTask(ctx, otaTestTenant, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if paused.State != TaskStatePaused {
		t.Fatalf("state=%s want paused", paused.State)
	}
	// Cancel is allowed from paused.
	if _, _, _, err := store.CancelTask(ctx, otaTestTenant, taskID, "cancel after pause"); err != nil {
		t.Fatalf("cancel from paused must work: %v", err)
	}

	// A second task exercises resume.
	taskID2 := uuid.NewString()
	d2 := uuid.NewString()
	seedBatchedTask(t, store, taskID2, gw, map[int][]string{1: {d2}})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID2); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PauseTask(ctx, otaTestTenant, taskID2); err != nil {
		t.Fatal(err)
	}
	resumed, err := store.ResumeTask(ctx, otaTestTenant, taskID2)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.State != TaskStateRunning {
		t.Fatalf("state=%s want running", resumed.State)
	}
}

func TestPendingRediscoveryByGateway(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2 := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedBatchedTask(t, store, taskID, gw, map[int][]string{1: {d1, d2}})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}

	found, err := store.TasksWithPendingForGateway(ctx, otaTestTenant, gw)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0] != taskID {
		t.Fatalf("rediscovery found=%v want [%s]", found, taskID)
	}
}
