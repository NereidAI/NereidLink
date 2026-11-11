// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go verifies OTA task persistence, per-device progress state
// transitions, forward-only ordering, aggregate recompute and offline-gateway
// FailDevices against opt-in PostgreSQL (ADR-0026).

package ota

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nereidai/nereidlink/internal/rbac"
)

const otaTestTenant = "00000000-0000-0000-0000-00000000d808"

func newOTAStore(t *testing.T) (*Store, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return NewStore(pool), pool
}

func resetOTAFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`DELETE FROM ota_task_item WHERE tenant_id=$1`, otaTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM ota_task WHERE tenant_id=$1`, otaTestTenant); err != nil {
		t.Fatal(err)
	}
}

func seedOTATask(t *testing.T, store *Store, taskID string, devices map[string]string) Task {
	t.Helper()
	now := time.Now().UTC()
	task := Task{
		ID:              taskID,
		TenantID:        otaTestTenant,
		FirmwareVersion: "fw-1",
		PackageURL:      "https://x/fw-1.bin",
		PackageSHA256:   validSHA,
		PackageSize:     1024,
		State:           TaskStatePending,
		TotalCount:      len(devices),
		CreatedAt:       now,
	}
	items := make([]Item, 0, len(devices))
	for dev, gw := range devices {
		items = append(items, Item{
			ID:        uuid.NewString(),
			TaskID:    taskID,
			TenantID:  otaTestTenant,
			GatewayID: gw,
			DeviceID:  dev,
			State:     ItemStatePending,
			CreatedAt: now,
			UpdatedAt: now,
		})
	}
	if err := store.Create(context.Background(), task, items); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestOTALifecycleAndRecompute(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2 := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedOTATask(t, store, taskID, map[string]string{d1: gw, d2: gw})

	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDevices(ctx, taskID, []string{d1, d2}); err != nil {
		t.Fatal(err)
	}

	// downloading -> downloaded -> updating progress on d1 leaves it open and
	// must not change aggregate counts.
	for _, st := range []string{ItemStateDownloading, ItemStateDownloaded, ItemStateUpdating} {
		res, err := store.ApplyProgress(ctx, Event{
			TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: d1,
			State: st, ProgressPct: 40, Message: "ok",
		})
		if err != nil || !res.Changed {
			t.Fatalf("progress %s changed=%v err=%v", st, res.Changed, err)
		}
		if res.Task.SucceededCount != 0 || res.Task.State != TaskStateRunning {
			t.Fatalf("progress must not close task: %+v", res.Task)
		}
	}

	// d1 succeeds -> one success, task still running.
	var task Task
	res, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: d1,
		State: ItemStateSucceeded,
	})
	if err != nil || !res.Changed {
		t.Fatalf("succeed changed=%v err=%v", res.Changed, err)
	}
	task = res.Task
	if task.SucceededCount != 1 || task.State != TaskStateRunning {
		t.Fatalf("want 1 success/running, got %+v", task)
	}

	// A duplicate terminal frame is ignored.
	if dup, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: d1,
		State: ItemStateSucceeded,
	}); err != nil || dup.Changed {
		t.Fatalf("duplicate terminal must be ignored, changed=%v err=%v", dup.Changed, err)
	}

	// d2 fails -> partial (1 success, 1 failure) and task closes.
	res, err = store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: d2,
		State: ItemStateFailed, ResultCode: ResultIntegrityCheckFailed, Message: "checksum mismatch",
	})
	if err != nil {
		t.Fatal(err)
	}
	task = res.Task
	if task.State != TaskStatePartial || task.SucceededCount != 1 || task.FailedCount != 1 {
		t.Fatalf("want partial 1/1, got %+v", task)
	}
	if task.FinishedAt == nil {
		t.Fatal("terminal task must carry finished_at")
	}

	// Failed item message and result code persisted.
	page, err := store.ListItems(ctx, otaTestTenant, taskID, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	var sawFail bool
	for _, it := range page.Items {
		if it.DeviceID == d2 && it.Message == "checksum mismatch" &&
			it.State == ItemStateFailed && it.ResultCode == ResultIntegrityCheckFailed {
			sawFail = true
		}
	}
	if !sawFail {
		t.Fatal("failed item message/result_code not persisted")
	}

	// Timeline: d1 must have phase-boundary events ending in succeeded with a
	// monotonically increasing seq; repeated downloading pct frames must not
	// add rows.
	events, err := store.ListEvents(ctx, otaTestTenant, taskID, d1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 2 {
		t.Fatalf("want seeded + boundary events, got %d", len(events))
	}
	if events[0].Seq != 0 || events[0].Phase != ItemStatePending {
		t.Fatalf("first event must be seeded pending/seq0, got %+v", events[0])
	}
	if events[len(events)-1].Phase != ItemStateSucceeded {
		t.Fatalf("last d1 event must be succeeded, got %s", events[len(events)-1].Phase)
	}
}

func TestOTAProgressForwardOnly(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw, d := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedOTATask(t, store, taskID, map[string]string{d: gw})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimDevices(ctx, taskID, []string{d}); err != nil {
		t.Fatal(err)
	}

	// Advance to updating, then a late downloading frame must be ignored.
	if r, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d, GatewayID: gw,
		State: ItemStateUpdating, ProgressPct: 80}); err != nil || !r.Changed {
		t.Fatalf("updating changed=%v err=%v", r.Changed, err)
	}
	if r, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d, GatewayID: gw,
		State: ItemStateDownloading, ProgressPct: 10}); err != nil || r.Changed {
		t.Fatalf("backward progress must be ignored, changed=%v err=%v", r.Changed, err)
	}

	page, _ := store.ListItems(ctx, otaTestTenant, taskID, 1, 10)
	if len(page.Items) != 1 || page.Items[0].State != ItemStateUpdating || page.Items[0].ProgressPct != 80 {
		t.Fatalf("item must remain updating/80, got %+v", page.Items)
	}
}

func TestOTAFailDevicesOffline(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	offlineGW, onlineGW := uuid.NewString(), uuid.NewString()
	dOff, dOn := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedOTATask(t, store, taskID, map[string]string{dOff: offlineGW, dOn: onlineGW})
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	// Online gateway's device is claimed (in flight); offline one stays pending.
	if _, err := store.ClaimDevices(ctx, taskID, []string{dOn}); err != nil {
		t.Fatal(err)
	}

	task, _, changed, err := store.FailDevices(ctx, otaTestTenant, taskID,
		[]string{dOff, dOn}, "gateway not connected at dispatch")
	if err != nil || !changed {
		t.Fatalf("FailDevices changed=%v err=%v", changed, err)
	}
	// Only the pending offline device is failed; the claimed online device is
	// untouched so the task stays running, not failed.
	if task.FailedCount != 1 || task.State != TaskStateRunning {
		t.Fatalf("want 1 failed/running, got %+v", task)
	}

	page, _ := store.ListItems(ctx, otaTestTenant, taskID, 1, 10)
	states := map[string]string{}
	for _, it := range page.Items {
		states[it.DeviceID] = it.State
	}
	if states[dOff] != ItemStateFailed || states[dOn] != ItemStateDownloading {
		t.Fatalf("want offline=failed, online=downloading, got %+v", states)
	}
}

// seedOTAWithPolicy creates a single-device task with an explicit success
// policy, used by the confirmation/cancel tests.
func seedOTAWithPolicy(t *testing.T, store *Store, taskID, deviceID, policy string) Task {
	t.Helper()
	now := time.Now().UTC()
	task := Task{
		ID:              taskID,
		TenantID:        otaTestTenant,
		FirmwareVersion: "2.0.0",
		PackageURL:      "/v1/me/ota/" + taskID + "/package",
		PackageSHA256:   validSHA,
		PackageSize:     2048,
		State:           TaskStatePending,
		SuccessPolicy:   policy,
		TotalCount:      1,
		CreatedAt:       now,
	}
	items := []Item{{
		ID: uuid.NewString(), TaskID: taskID, TenantID: otaTestTenant,
		GatewayID: "", DeviceID: deviceID, State: ItemStatePending,
		CreatedAt: now, UpdatedAt: now,
	}}
	if err := store.Create(context.Background(), task, items); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkRunning(context.Background(), otaTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestOTAConfirmedAfterRebootPolicy(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	d := uuid.NewString()
	taskID := uuid.NewString()
	seedOTAWithPolicy(t, store, taskID, d, PolicyConfirmedAfterReboot)

	// updated must NOT finalize under the confirmation policy.
	res, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d,
		State: ItemStateUpdated, ProgressPct: 100})
	if err != nil || !res.Changed {
		t.Fatalf("updated changed=%v err=%v", res.Changed, err)
	}
	if res.Item.State != ItemStateUpdated || res.Task.State != TaskStateRunning {
		t.Fatalf("updated must stay intermediate, item=%s task=%s", res.Item.State, res.Task.State)
	}

	// rebooting remains open.
	res, err = store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d, State: ItemStateRebooting})
	if err != nil || !res.Changed || res.Item.State != ItemStateRebooting {
		t.Fatalf("rebooting transition failed: %+v err=%v", res.Item, err)
	}

	// A confirmation with the wrong version finalizes as a version-mismatch
	// failure.
	d2 := uuid.NewString()
	taskID2 := uuid.NewString()
	seedOTAWithPolicy(t, store, taskID2, d2, PolicyConfirmedAfterReboot)
	if _, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID2, TenantID: otaTestTenant, DeviceID: d2, State: ItemStateUpdated}); err != nil {
		t.Fatal(err)
	}
	bad, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID2, TenantID: otaTestTenant, DeviceID: d2,
		State: ItemStateConfirmed, FirmwareVersion: "1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if bad.Item.State != ItemStateFailed || bad.Item.ResultCode != ResultVersionMismatch {
		t.Fatalf("wrong-version confirm must fail/version_mismatch, got %s/%s",
			bad.Item.State, bad.Item.ResultCode)
	}

	// A matching confirmation finalizes success.
	ok, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d,
		State: ItemStateConfirmed, FirmwareVersion: "2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Item.State != ItemStateSucceeded || ok.Task.State != TaskStateCompleted {
		t.Fatalf("matching confirm must succeed/complete, item=%s task=%s",
			ok.Item.State, ok.Task.State)
	}
}

func TestOTADeviceReportedUpdatedFinalizes(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	d := uuid.NewString()
	taskID := uuid.NewString()
	seedOTAWithPolicy(t, store, taskID, d, PolicyDeviceReported)
	res, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, DeviceID: d, State: ItemStateUpdated})
	if err != nil {
		t.Fatal(err)
	}
	if res.Item.State != ItemStateSucceeded || res.Task.State != TaskStateCompleted {
		t.Fatalf("updated under device_reported must finalize, item=%s task=%s",
			res.Item.State, res.Task.State)
	}
}

func TestOTACancelTiers(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	dPending, dDownload, dApply := uuid.NewString(), uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedOTATask(t, store, taskID, map[string]string{
		dPending: gw, dDownload: gw, dApply: gw,
	})

	// Claim two devices, then advance one to applying.
	if _, err := store.ClaimDevices(ctx, taskID, []string{dDownload, dApply}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: dApply,
		State: ItemStateUpdating, ProgressPct: 50}); err != nil {
		t.Fatal(err)
	}

	task, events, stillApplying, err := store.CancelTask(ctx, otaTestTenant, taskID, "canceled by test")
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskStateCanceled {
		t.Fatalf("task must be canceled, got %s", task.State)
	}
	if len(stillApplying) != 1 || stillApplying[0].DeviceID != dApply {
		t.Fatalf("only the applying device should remain, got %+v", stillApplying)
	}
	states := map[string]string{}
	page, _ := store.ListItems(ctx, otaTestTenant, taskID, 1, 50)
	for _, it := range page.Items {
		states[it.DeviceID] = it.State
	}
	if states[dPending] != ItemStateSkipped || states[dDownload] != ItemStateCanceled ||
		states[dApply] != ItemStateUpdating {
		t.Fatalf("cancel tiers wrong: %+v", states)
	}
	if len(events) != 2 {
		t.Fatalf("want 2 cancel/skip timeline events, got %d", len(events))
	}

	// The applying device can still report its final result after cancel.
	res, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: dApply,
		State: ItemStateSucceeded})
	if err != nil || !res.Changed {
		t.Fatalf("post-cancel success must be accepted, changed=%v err=%v", res.Changed, err)
	}
	if res.Task.State != TaskStateCanceled {
		t.Fatalf("task stays canceled after late result, got %s", res.Task.State)
	}

	// A non-terminal frame after cancel is rejected.
	if r, err := store.ApplyProgress(ctx, Event{
		TaskID: taskID, TenantID: otaTestTenant, GatewayID: gw, DeviceID: dDownload,
		State: ItemStateDownloading}); err != nil || r.Changed {
		t.Fatalf("post-cancel non-terminal must be ignored, changed=%v err=%v", r.Changed, err)
	}
}

// TestOTAListRespectsDataScope proves an OTA rollout is invisible to a caller
// whose scope excludes every device it targets.
//
// An OTA task is the most sensitive device-derived row in the product: it
// carries the firmware package URL and SHA, so leaking another customer's
// rollout hands over the exact artefact being shipped. The task has no device
// column, so the parent-visibility rule (visible only when at least one item
// targets a visible device) is the whole defence and must be executed.
func TestOTAListRespectsDataScope(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	customerA := uuid.NewString()
	customerB := uuid.NewString()
	deviceA, deviceB := uuid.NewString(), uuid.NewString()
	gateway := uuid.NewString()

	// The shared OTA fixture tenant is not created by these tests, and
	// customer has a foreign key to it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'ota-scope-test')
		 ON CONFLICT (id) DO NOTHING`, otaTestTenant); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO customer (id, tenant_id, name)
		 VALUES ($1,$2,'ota-a'), ($3,$2,'ota-b')
		 ON CONFLICT (id) DO NOTHING`,
		customerA, otaTestTenant, customerB); err != nil {
		t.Fatalf("insert customer: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state, customer_id)
		 VALUES ($1,$2,'ota-dev-a','ACTIVE',$3),
		        ($4,$2,'ota-dev-b','ACTIVE',$5)`,
		deviceA, otaTestTenant, customerA, deviceB, customerB); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, id := range []string{customerA, customerB} {
			if _, err := pool.Exec(c,
				`UPDATE device SET customer_id = NULL WHERE customer_id = $1`,
				id); err != nil {
				t.Logf("release ownership: %v", err)
			}
			if _, err := pool.Exec(c,
				`DELETE FROM customer WHERE id = $1`, id); err != nil {
				t.Logf("cleanup customer: %v", err)
			}
		}
		if _, err := pool.Exec(c,
			`DELETE FROM device WHERE id = ANY($1)`, []string{deviceA, deviceB}); err != nil {
			t.Logf("cleanup device: %v", err)
		}
		if _, err := pool.Exec(c,
			`DELETE FROM tenant WHERE id = $1`, otaTestTenant); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})

	// Two rollouts: one aimed only at customer B's device, one at customer A's.
	taskForeign := uuid.NewString()
	taskVisible := uuid.NewString()
	seedOTATask(t, store, taskForeign, map[string]string{deviceB: gateway})
	seedOTATask(t, store, taskVisible, map[string]string{deviceA: gateway})

	full := rbac.ContextWithScope(ctx,
		rbac.DataScope{Level: rbac.ScopeTenant, TenantID: otaTestTenant})
	page, err := store.ListTasks(full, otaTestTenant, "", 1, 50)
	if err != nil {
		t.Fatalf("ListTasks TENANT: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("TENANT scope total = %d, want 2", page.Total)
	}

	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level:      rbac.ScopeCustomer,
		TenantID:   otaTestTenant,
		CustomerID: customerA,
	})
	page, err = store.ListTasks(narrow, otaTestTenant, "", 1, 50)
	if err != nil {
		t.Fatalf("ListTasks CUSTOMER: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("CUSTOMER scope total = %d, want 1", page.Total)
	}
	if len(page.Items) != 1 || page.Items[0].ID != taskVisible {
		t.Fatalf("CUSTOMER scope saw %+v, want only %s", page.Items, taskVisible)
	}

	// The package URL must not be reachable through a foreign task, and the
	// item list of a visible task must narrow to its visible device.
	itemPage, err := store.ListItems(narrow, otaTestTenant, taskVisible, 1, 50)
	if err != nil {
		t.Fatalf("ListItems CUSTOMER: %v", err)
	}
	if itemPage.Total != 1 || len(itemPage.Items) != 1 {
		t.Fatalf("visible task items = %d/%d, want 1/1",
			itemPage.Total, len(itemPage.Items))
	}
	if itemPage.Items[0].DeviceID != deviceA {
		t.Fatalf("visible task item device = %s, want %s",
			itemPage.Items[0].DeviceID, deviceA)
	}
}

func TestListItemsByDevice(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	ctx := context.Background()

	dev := uuid.NewString()
	taskA := uuid.NewString()
	taskB := uuid.NewString()
	seedOTATask(t, store, taskA, map[string]string{dev: ""})
	seedOTATask(t, store, taskB, map[string]string{dev: ""})

	// Mark taskA running and move the device to downloading so the joined
	// task state/version reflect a real rollout.
	if _, err := store.MarkRunning(ctx, otaTestTenant, taskA); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListItemsByDevice(ctx, otaTestTenant, dev, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 {
		t.Fatalf("want 2 rows for device, got total=%d items=%d", page.Total, len(page.Items))
	}
	for _, row := range page.Items {
		if row.DeviceID != dev {
			t.Fatalf("row device = %s, want %s", row.DeviceID, dev)
		}
		if row.FirmwareVersion != "fw-1" {
			t.Fatalf("joined firmware version = %q, want fw-1", row.FirmwareVersion)
		}
		if row.TaskID == taskA && row.TaskState != TaskStateRunning {
			t.Fatalf("taskA state = %q, want running", row.TaskState)
		}
	}

	// Newest first: both share created_at; the second-seeded taskB must sort
	// ahead by id DESC.
	if page.Items[0].TaskID != taskB {
		t.Fatalf("newest row task = %s, want %s", page.Items[0].TaskID, taskB)
	}

	// Pagination clamps to one row per page.
	first, err := store.ListItemsByDevice(ctx, otaTestTenant, dev, 1, 1)
	if err != nil || len(first.Items) != 1 || first.Total != 2 {
		t.Fatalf("paged result items=%d total=%d err=%v", len(first.Items), first.Total, err)
	}
}
