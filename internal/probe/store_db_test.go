// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go verifies probe task persistence, per-device outcome
// transitions, aggregate recompute and retention against opt-in PostgreSQL
// (ADR-0026 section 9).

package probe

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

const probeTestTenant = "00000000-0000-0000-0000-00000000d801"

func newProbeStore(t *testing.T) (*Store, *pgxpool.Pool) {
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

func resetProbeFixture(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`DELETE FROM probe_task_item WHERE tenant_id=$1`, probeTestTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM probe_task WHERE tenant_id=$1`, probeTestTenant); err != nil {
		t.Fatal(err)
	}
}

func seedProbeTask(t *testing.T, store *Store, taskID string, gateways, devices map[string]string) Task {
	t.Helper()
	now := time.Now().UTC()
	task := Task{
		ID:         taskID,
		TenantID:   probeTestTenant,
		Mode:       ModeFresh,
		TimeoutMS:  10000,
		State:      TaskStatePending,
		TotalCount: len(devices),
		CreatedAt:  now,
	}
	items := make([]Item, 0, len(devices))
	for dev, gw := range devices {
		items = append(items, Item{
			ID:        uuid.NewString(),
			TaskID:    taskID,
			TenantID:  probeTestTenant,
			GatewayID: gw,
			DeviceID:  dev,
			State:     ItemStatePending,
			CreatedAt: now,
		})
	}
	if err := store.Create(context.Background(), task, items); err != nil {
		t.Fatal(err)
	}
	return task
}

func TestProbeLifecycleAndRecompute(t *testing.T) {
	store, pool := newProbeStore(t)
	resetProbeFixture(t, pool)
	t.Cleanup(func() { resetProbeFixture(t, pool) })
	ctx := context.Background()

	gw1 := uuid.NewString()
	d1, d2 := uuid.NewString(), uuid.NewString()
	taskID := uuid.NewString()
	seedProbeTask(t, store, taskID,
		nil, map[string]string{d1: gw1, d2: gw1})

	if _, err := store.MarkRunning(ctx, probeTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDevices(ctx, taskID, []string{d1, d2}); err != nil {
		t.Fatal(err)
	}

	// First device succeeds -> task stays running/partial-in-progress.
	task, changed, err := store.ApplyEvent(ctx, Event{
		ProbeID: taskID, TenantID: probeTestTenant, GatewayID: gw1,
		DeviceID: d1, Outcome: OutcomeOK, Result: json.RawMessage(`{"rtt_ms":4}`),
	})
	if err != nil || !changed {
		t.Fatalf("apply d1: changed=%v err=%v", changed, err)
	}
	if task.SucceededCount != 1 || task.State != TaskStateRunning {
		t.Fatalf("after 1/2 ok: succ=%d state=%s", task.SucceededCount, task.State)
	}

	// A duplicate/late event for the same device does not double-count.
	if _, changed, err := store.ApplyEvent(ctx, Event{
		ProbeID: taskID, TenantID: probeTestTenant, GatewayID: gw1,
		DeviceID: d1, Outcome: OutcomeOK,
	}); err != nil || changed {
		t.Fatalf("duplicate event must be ignored: changed=%v err=%v", changed, err)
	}

	// Second device fails -> task terminal partial.
	task, changed, err = store.ApplyEvent(ctx, Event{
		ProbeID: taskID, TenantID: probeTestTenant, GatewayID: gw1,
		DeviceID: d2, Outcome: OutcomeTimeout, Error: "no response",
	})
	if err != nil || !changed {
		t.Fatalf("apply d2: changed=%v err=%v", changed, err)
	}
	if task.State != TaskStatePartial || task.SucceededCount != 1 || task.FailedCount != 1 {
		t.Fatalf("terminal: %+v", task)
	}
	if task.FinishedAt == nil {
		t.Fatal("terminal task must carry finished_at")
	}
}

// TestProbeFastFailBeforeClaim covers the offline-gateway path: an outcome
// is applied while the item is still pending (ClaimDevices is skipped for an
// offline gateway), and must terminalize the item and its task just like a
// running-item result.
func TestProbeFastFailBeforeClaim(t *testing.T) {
	store, pool := newProbeStore(t)
	resetProbeFixture(t, pool)
	t.Cleanup(func() { resetProbeFixture(t, pool) })
	ctx := context.Background()

	gw1 := uuid.NewString()
	d1 := uuid.NewString()
	taskID := uuid.NewString()
	seedProbeTask(t, store, taskID, nil, map[string]string{d1: gw1})
	if _, err := store.MarkRunning(ctx, probeTestTenant, taskID); err != nil {
		t.Fatal(err)
	}

	// Apply not-connected without claiming: item stays pending.
	task, changed, err := store.ApplyEvent(ctx, Event{
		ProbeID: taskID, TenantID: probeTestTenant, GatewayID: gw1,
		DeviceID: d1, Outcome: OutcomeNotConnected, Error: "gateway not connected",
	})
	if err != nil || !changed {
		t.Fatalf("pending fast-fail: changed=%v err=%v", changed, err)
	}
	if task.State != TaskStateFailed || task.FailedCount != 1 || task.FinishedAt == nil {
		t.Fatalf("task must be failed after pre-claim fast-fail: %+v", task)
	}
	items, err := store.ListItems(ctx, probeTestTenant, taskID, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(items.Items) != 1 || items.Items[0].State != ItemStateFailed {
		t.Fatalf("item must be failed: %+v", items.Items)
	}

	// A second event for the now-terminal item is ignored.
	if _, changed, err := store.ApplyEvent(ctx, Event{
		ProbeID: taskID, TenantID: probeTestTenant, GatewayID: gw1,
		DeviceID: d1, Outcome: OutcomeOK,
	}); err != nil || changed {
		t.Fatalf("late event must be ignored: changed=%v err=%v", changed, err)
	}
}

func TestProbeFailOpenItemsAndRetention(t *testing.T) {
	store, pool := newProbeStore(t)
	resetProbeFixture(t, pool)
	t.Cleanup(func() { resetProbeFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1 := uuid.NewString()
	taskID := uuid.NewString()
	seedProbeTask(t, store, taskID, nil, map[string]string{d1: gw})
	if _, err := store.MarkRunning(ctx, probeTestTenant, taskID); err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimDevices(ctx, taskID, []string{d1}); err != nil {
		t.Fatal(err)
	}

	task, err := store.FailOpenItems(ctx, probeTestTenant, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if task.State != TaskStateFailed || task.FailedCount != 1 {
		t.Fatalf("fail-open: %+v", task)
	}

	// A task finished far in the future is retained; backdating finished_at
	// makes the retention sweep remove it.
	if _, err := pool.Exec(ctx,
		`UPDATE probe_task SET finished_at = now() - interval '25 hours' WHERE id=$1`,
		taskID); err != nil {
		t.Fatal(err)
	}
	n, err := store.DeleteFinishedBefore(ctx, time.Now().UTC().Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 deleted, got %d", n)
	}
	if _, err := store.GetTask(ctx, probeTestTenant, taskID); err == nil {
		t.Fatal("retention should have removed the task")
	}
}

func TestProbeResolveTargets(t *testing.T) {
	store, pool := newProbeStore(t)
	ctx := context.Background()

	gw := "00000000-0000-0000-0000-00000000d810"
	sub := "00000000-0000-0000-0000-00000000d811"
	plain := "00000000-0000-0000-0000-00000000d812"
	stmts := []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenant (id,name) VALUES ($1,'probe-test') ON CONFLICT (id) DO NOTHING`,
			[]any{probeTestTenant}},
		{`INSERT INTO device (id,tenant_id,canonical_name,is_gateway,state)
		 VALUES ($1,$2,'probe-gw',true,'ACTIVE') ON CONFLICT (id) DO UPDATE SET state='ACTIVE'`,
			[]any{gw, probeTestTenant}},
		{`INSERT INTO device (id,tenant_id,canonical_name,gateway_id,state)
		 VALUES ($1,$2,'probe-sub',$3,'ACTIVE') ON CONFLICT (id) DO UPDATE SET gateway_id=$3,state='ACTIVE'`,
			[]any{sub, probeTestTenant, gw}},
		{`INSERT INTO device (id,tenant_id,canonical_name,state)
		 VALUES ($1,$2,'probe-plain','ACTIVE') ON CONFLICT (id) DO UPDATE SET state='ACTIVE'`,
			[]any{plain, probeTestTenant}},
	}
	for _, st := range stmts {
		if _, err := pool.Exec(ctx, st.sql, st.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, id := range []string{sub, plain, gw} {
			_, _ = pool.Exec(c, `DELETE FROM device_ownership_event WHERE device_id=$1`, id)
			_, _ = pool.Exec(c, `DELETE FROM device WHERE id=$1`, id)
		}
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, probeTestTenant)
	})

	targets, err := store.ResolveTargets(ctx, probeTestTenant, []string{sub, plain, gw})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[sub] != gw {
		t.Fatalf("only gateway-bound sub-device resolves, got %+v", targets)
	}
}

// TestProbeDataScopeNarrowing proves a CUSTOMER-scoped caller only resolves
// and reads probe targets/items for devices their scope covers, while an
// unscoped (internal) caller sees the whole tenant.
func TestProbeDataScopeNarrowing(t *testing.T) {
	store, pool := newProbeStore(t)
	resetProbeFixture(t, pool)
	t.Cleanup(func() { resetProbeFixture(t, pool) })
	ctx := context.Background()

	gw := "00000000-0000-0000-0000-00000000d820"
	devA := "00000000-0000-0000-0000-00000000d821"
	devB := "00000000-0000-0000-0000-00000000d822"
	custA := "00000000-0000-0000-0000-00000000d823"
	custB := "00000000-0000-0000-0000-00000000d824"
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO tenant (id,name) VALUES ($1,'probe-scope')
		  ON CONFLICT (id) DO NOTHING`, []any{probeTestTenant}},
		{`INSERT INTO customer (id,tenant_id,name) VALUES ($1,$2,'a'),($3,$2,'b')
		  ON CONFLICT (id) DO NOTHING`, []any{custA, probeTestTenant, custB}},
		{`INSERT INTO device (id,tenant_id,canonical_name,is_gateway,state)
		  VALUES ($1,$2,'pscope-gw',true,'ACTIVE')
		  ON CONFLICT (id) DO UPDATE SET state='ACTIVE'`, []any{gw, probeTestTenant}},
		{`INSERT INTO device (id,tenant_id,canonical_name,gateway_id,customer_id,state)
		  VALUES ($1,$2,'pscope-a',$3,$4,'ACTIVE'),($5,$2,'pscope-b',$3,$6,'ACTIVE')
		  ON CONFLICT (id) DO UPDATE SET gateway_id=$3,customer_id=EXCLUDED.customer_id,state='ACTIVE'`,
			[]any{devA, probeTestTenant, gw, custA, devB, custB}},
	}
	for _, st := range stmts {
		if _, err := pool.Exec(ctx, st.q, st.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		c := context.Background()
		for _, id := range []string{devA, devB, gw} {
			_, _ = pool.Exec(c, `DELETE FROM device_ownership_event WHERE device_id=$1`, id)
			_, _ = pool.Exec(c, `DELETE FROM device WHERE id=$1`, id)
		}
		_, _ = pool.Exec(c, `DELETE FROM customer WHERE id=ANY($1::uuid[])`, []string{custA, custB})
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, probeTestTenant)
	})

	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		TenantID: probeTestTenant, Level: rbac.ScopeCustomer, CustomerID: custA,
	})

	// ResolveTargets only returns the in-scope gateway-bound device.
	targets, err := store.ResolveTargets(narrow, probeTestTenant, []string{devA, devB})
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 1 || targets[devA] != gw {
		t.Fatalf("customer scope must resolve only devA, got %+v", targets)
	}

	// Seed one task spanning both devices, then prove read narrowing.
	taskID := uuid.NewString()
	now := time.Now().UTC()
	task := Task{ID: taskID, TenantID: probeTestTenant, Mode: ModeFresh,
		TimeoutMS: 10000, State: TaskStateRunning, TotalCount: 2, CreatedAt: now}
	items := []Item{
		{ID: uuid.NewString(), TaskID: taskID, TenantID: probeTestTenant,
			GatewayID: gw, DeviceID: devA, State: ItemStateRunning, CreatedAt: now},
		{ID: uuid.NewString(), TaskID: taskID, TenantID: probeTestTenant,
			GatewayID: gw, DeviceID: devB, State: ItemStateRunning, CreatedAt: now},
	}
	if err := store.Create(ctx, task, items); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetTask(narrow, probeTestTenant, taskID); err != nil {
		t.Fatalf("task with an in-scope item stays visible: %v", err)
	}
	page, err := store.ListItems(narrow, probeTestTenant, taskID, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].DeviceID != devA {
		t.Fatalf("items narrow to devA: total=%d n=%d", page.Total, len(page.Items))
	}

	// A second task probing only the out-of-scope device must be hidden from
	// the narrowed list, while the spanning task stays visible.
	otherTask := uuid.NewString()
	if err := store.Create(ctx, Task{ID: otherTask, TenantID: probeTestTenant,
		Mode: ModeFresh, TimeoutMS: 10000, State: TaskStateRunning,
		TotalCount: 1, CreatedAt: now.Add(time.Second)}, []Item{
		{ID: uuid.NewString(), TaskID: otherTask, TenantID: probeTestTenant,
			GatewayID: gw, DeviceID: devB, State: ItemStateRunning,
			CreatedAt: now.Add(time.Second)},
	}); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListTasks(narrow, probeTestTenant, TaskFilter{}, 1, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range list.Items {
		if tk.ID == otherTask {
			t.Fatal("out-of-scope-only task must not appear in narrowed list")
		}
	}
	found := false
	for _, tk := range list.Items {
		if tk.ID == taskID {
			found = true
		}
	}
	if !found {
		t.Fatal("task with an in-scope item must appear in narrowed list")
	}
}

// TestProbeFindCoalescible proves a repeated request shares an open task only
// when every requested device is still pending/running in one same-actor,
// same-mode task, and not after that item terminalizes or for a foreign actor.
func TestProbeFindCoalescible(t *testing.T) {
	store, pool := newProbeStore(t)
	resetProbeFixture(t, pool)
	t.Cleanup(func() { resetProbeFixture(t, pool) })
	ctx := context.Background()

	gw := uuid.NewString()
	d1, d2, d3 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	actor := uuid.NewString()
	taskID := uuid.NewString()
	now := time.Now().UTC()
	task := Task{ID: taskID, TenantID: probeTestTenant, CreatedBy: actor,
		Mode: ModeFresh, TimeoutMS: 10000, State: TaskStateRunning,
		TotalCount: 2, CreatedAt: now}
	items := []Item{
		{ID: uuid.NewString(), TaskID: taskID, TenantID: probeTestTenant,
			GatewayID: gw, DeviceID: d1, State: ItemStateRunning, CreatedAt: now},
		{ID: uuid.NewString(), TaskID: taskID, TenantID: probeTestTenant,
			GatewayID: gw, DeviceID: d2, State: ItemStatePending, CreatedAt: now},
	}
	if err := store.Create(ctx, task, items); err != nil {
		t.Fatal(err)
	}

	got, err := store.FindCoalescible(ctx, probeTestTenant, actor, ModeFresh, []string{d1, d2})
	if err != nil || got.ID != taskID {
		t.Fatalf("covered set coalesces onto task: id=%s err=%v", got.ID, err)
	}
	if _, err := store.FindCoalescible(ctx, probeTestTenant, actor, ModeFresh, []string{d1, d3}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a set with an uncovered device must not coalesce, got %v", err)
	}
	if _, err := store.FindCoalescible(ctx, probeTestTenant, actor, ModeStrict, []string{d1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a different mode must not coalesce, got %v", err)
	}
	if _, err := store.FindCoalescible(ctx, probeTestTenant, uuid.NewString(), ModeFresh, []string{d1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a foreign actor must not coalesce, got %v", err)
	}

	// Once every requested item is terminal, the task no longer coalesces.
	if _, err := pool.Exec(ctx,
		`UPDATE probe_task_item SET state='succeeded', finished_at=now()
		 WHERE task_id=$1 AND device_id=ANY($2::uuid[])`,
		taskID, []string{d1, d2}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindCoalescible(ctx, probeTestTenant, actor, ModeFresh, []string{d1, d2}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("terminal items must not coalesce, got %v", err)
	}
}
