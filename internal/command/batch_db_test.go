// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batch_db_test.go verifies group subtree expansion and batch execution
// progress against an opt-in PostgreSQL database.

package command

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nereidai/nereidlink/internal/rbac"
)

const (
	batchTestTenant  = "00000000-0000-0000-0000-00000000c701"
	batchGroupRoot   = "00000000-0000-0000-0000-00000000c710"
	batchGroupChild  = "00000000-0000-0000-0000-00000000c711"
	batchDeviceOne   = "00000000-0000-0000-0000-00000000c721"
	batchDeviceTwo   = "00000000-0000-0000-0000-00000000c722"
	batchDeviceThree = "00000000-0000-0000-0000-00000000c723"
)

// newBatchTestStore connects when NL_TEST_DATABASE_URL is configured.
func newBatchTestStore(t *testing.T) *BatchStore {
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
	return NewBatchStore(pool)
}

// seedBatchFixture creates a two-level group tree with three active devices
// and cleanup for every disposable row.
func seedBatchFixture(t *testing.T, store *BatchStore) {
	t.Helper()
	ctx := context.Background()
	pool := store.pool
	statements := []string{
		`INSERT INTO tenant (id, name) VALUES ($1, 'batch-test')
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO device_group (id, tenant_id, name) VALUES ($1,$2,'root')
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO device_group (id, tenant_id, parent_id, name)
		 VALUES ($1,$2,$3,'child')
		 ON CONFLICT (id) DO NOTHING`,
		`INSERT INTO device (id, tenant_id, canonical_name, group_id, state)
		 VALUES ($1,$2,'d1',$3,'ACTIVE')
		 ON CONFLICT (id) DO UPDATE SET group_id = EXCLUDED.group_id, state = 'ACTIVE'`,
		`INSERT INTO device (id, tenant_id, canonical_name, group_id, state)
		 VALUES ($1,$2,'d2',$3,'ACTIVE')
		 ON CONFLICT (id) DO UPDATE SET group_id = EXCLUDED.group_id, state = 'ACTIVE'`,
		`INSERT INTO device (id, tenant_id, canonical_name, state)
		 VALUES ($1,$2,'d3','ACTIVE')
		 ON CONFLICT (id) DO UPDATE SET group_id = NULL, state = 'ACTIVE'`,
	}
	args := [][]any{
		{batchTestTenant},
		{batchGroupRoot, batchTestTenant},
		{batchGroupChild, batchTestTenant, batchGroupRoot},
		{batchDeviceOne, batchTestTenant, batchGroupRoot},
		{batchDeviceTwo, batchTestTenant, batchGroupChild},
		{batchDeviceThree, batchTestTenant},
	}
	for index, statement := range statements {
		if _, err := pool.Exec(ctx, statement, args[index]...); err != nil {
			t.Fatalf("seed statement %d: %v", index, err)
		}
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		for _, table := range []string{
			`DELETE FROM command_task_item WHERE tenant_id = $1`,
			`DELETE FROM command_task WHERE tenant_id = $1`,
		} {
			if _, err := pool.Exec(cleanup, table, batchTestTenant); err != nil {
				t.Logf("cleanup tasks: %v", err)
			}
		}
		for _, id := range []string{batchDeviceOne, batchDeviceTwo, batchDeviceThree} {
			if _, err := pool.Exec(cleanup, `DELETE FROM device WHERE id = $1`, id); err != nil {
				t.Logf("cleanup device: %v", err)
			}
		}
		for _, id := range []string{batchGroupChild, batchGroupRoot} {
			if _, err := pool.Exec(cleanup, `DELETE FROM device_group WHERE id = $1`, id); err != nil {
				t.Logf("cleanup group: %v", err)
			}
		}
		if _, err := pool.Exec(cleanup, `DELETE FROM tenant WHERE id = $1`, batchTestTenant); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})
}

// TestExpandGroupSubtree verifies descendant groups are included.
func TestExpandGroupSubtree(t *testing.T) {
	store := newBatchTestStore(t)
	seedBatchFixture(t, store)
	devices, err := store.ExpandGroup(context.Background(), batchTestTenant, batchGroupRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 2 {
		t.Fatalf("devices = %v", devices)
	}
}

// TestCorrectLateResult verifies a timeout is corrected by a late response.
func TestCorrectLateResult(t *testing.T) {
	batchStore := newBatchTestStore(t)
	commandStore := NewStore(batchStore.pool)
	seedBatchFixture(t, batchStore)
	ctx := context.Background()
	commandID := "00000000-0000-0000-0000-00000000c760"
	if _, err := batchStore.pool.Exec(ctx, `DELETE FROM command WHERE id = $1`, commandID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := batchStore.pool.Exec(context.Background(),
			`DELETE FROM command WHERE id = $1`, commandID); err != nil {
			t.Logf("cleanup command: %v", err)
		}
	})
	c := Command{
		ID:          commandID,
		TenantID:    batchTestTenant,
		DeviceID:    batchDeviceOne,
		Name:        "reboot",
		Params:      []byte(`{}`),
		Interaction: InteractionRequestResponse,
		Timeliness:  TimelinessRealtime,
		CreatedAt:   time.Now().UTC(),
	}
	if err := commandStore.Insert(ctx, c, StatusTimeout, c.CreatedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := commandStore.Complete(ctx, commandID, StatusTimeout, nil, "response timeout"); err != nil {
		t.Fatal(err)
	}
	if err := commandStore.CorrectLateResult(ctx, Event{
		CommandID: commandID,
		TenantID:  batchTestTenant,
		DeviceID:  batchDeviceOne,
		Status:    StatusSucceeded,
		Response:  `{"ok":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := batchStore.pool.QueryRow(ctx,
		`SELECT state FROM command WHERE id = $1`, commandID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != StatusSucceeded {
		t.Fatalf("state = %s", state)
	}
}

// fakeBatchSender returns configured per-device acknowledgements.
type fakeBatchSender struct {
	acks map[string]Ack
}

// Send implements BatchSender.
func (f *fakeBatchSender) Send(_ context.Context, _, deviceID, _, _ string, _ []byte, _ int64) (Ack, error) {
	return f.acks[deviceID], nil
}

// TestBatchExecutionPartial verifies independent item outcomes are aggregated.
func TestBatchExecutionPartial(t *testing.T) {
	store := newBatchTestStore(t)
	seedBatchFixture(t, store)
	ctx := context.Background()
	now := time.Now().UTC()
	task := Task{
		ID:         "00000000-0000-0000-0000-00000000c730",
		TenantID:   batchTestTenant,
		Name:       "reboot",
		Params:     []byte(`{}`),
		TargetType: TargetDevices,
		State:      TaskStatePending,
		TotalCount: 2,
		CreatedAt:  now,
	}
	items := []TaskItem{
		{
			ID:        "00000000-0000-0000-0000-00000000c740",
			TaskID:    task.ID,
			TenantID:  batchTestTenant,
			DeviceID:  batchDeviceOne,
			State:     ItemStatePending,
			CreatedAt: now,
		},
		{
			ID:        "00000000-0000-0000-0000-00000000c741",
			TaskID:    task.ID,
			TenantID:  batchTestTenant,
			DeviceID:  batchDeviceTwo,
			State:     ItemStatePending,
			CreatedAt: now,
		},
	}
	if err := store.Create(ctx, task, items); err != nil {
		t.Fatal(err)
	}

	sender := &fakeBatchSender{
		acks: map[string]Ack{
			batchDeviceOne: {ID: "00000000-0000-0000-0000-00000000c750", Status: StatusSucceeded},
			batchDeviceTwo: {ID: "00000000-0000-0000-0000-00000000c751", Status: StatusNotConnected},
		},
	}
	manager := NewBatchManager(store, sender, nil, 65536, 1000)
	manager.execute(batchTestTenant, task.ID)

	final, err := store.GetTask(ctx, batchTestTenant, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if final.State != TaskStatePartial || final.SucceededCount != 1 || final.FailedCount != 1 {
		t.Fatalf("final = %+v", final)
	}
	result, err := store.ListItems(ctx, batchTestTenant, task.ID, 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, item := range result.Items {
		states[item.DeviceID] = item.State
	}
	if states[batchDeviceOne] != ItemStateSucceeded || states[batchDeviceTwo] != ItemStateFailed {
		t.Fatalf("states = %v", states)
	}
}

// TestListTasksRespectsDataScope proves the batch-task list narrows to the
// caller's scope, including the parent-visibility rule.
//
// A batch task has no device column of its own; it owns per-device items. That
// makes two mistakes possible and both leak: returning the task regardless of
// its targets, and returning a task because *one* item is visible while most
// belong to another customer. The tests below pin the intended behaviour —
// count and page must agree, and the parent appears only when a visible item
// exists.
func TestListTasksRespectsDataScope(t *testing.T) {
	store := newBatchTestStore(t)
	seedBatchFixture(t, store)
	ctx := context.Background()
	pool := store.pool

	// batchDeviceOne and Two sit under the group tree; Three is ungrouped.
	// Give Three a customer of its own so a CUSTOMER scope can separate them.
	customerThree := "00000000-0000-0000-0000-00000000c730"
	customerGroup := "00000000-0000-0000-0000-00000000c731"
	for _, stmt := range []struct {
		q    string
		args []any
	}{
		{`INSERT INTO customer (id, tenant_id, name)
		  VALUES ($1,$2,'cust3'), ($3,$2,'custGroup')
		  ON CONFLICT (id) DO NOTHING`,
			[]any{customerThree, batchTestTenant, customerGroup}},
		{`UPDATE device SET customer_id = $1 WHERE id = $2`,
			[]any{customerThree, batchDeviceThree}},
		{`UPDATE device SET customer_id = $1 WHERE id IN ($2, $3)`,
			[]any{customerGroup, batchDeviceOne, batchDeviceTwo}},
	} {
		if _, err := pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed ownership: %v", err)
		}
	}
	// Registered before the tasks exist, so it runs last among this test's
	// cleanups. Ownership must be released before the customers can go, and
	// the tasks before the tenant.
	t.Cleanup(func() {
		for _, id := range []string{customerThree, customerGroup} {
			if _, err := pool.Exec(ctx,
				`UPDATE device SET customer_id = NULL WHERE customer_id = $1`,
				id); err != nil {
				t.Logf("release device ownership: %v", err)
			}
			if _, err := pool.Exec(ctx,
				`DELETE FROM customer WHERE id = $1`, id); err != nil {
				t.Logf("cleanup customer: %v", err)
			}
		}
	})

	// Two tasks: one aimed only at customerThree's device, one aimed at the
	// two devices owned by customerGroup.
	taskForeign := "00000000-0000-0000-0000-00000000c740"
	taskVisible := "00000000-0000-0000-0000-00000000c741"
	seed := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO command_task (id, tenant_id, name, target_type, state, total_count)
		  VALUES ($1,$2,'foreign','devices','completed',1),
		         ($3,$2,'visible','devices','completed',2)`,
			[]any{taskForeign, batchTestTenant, taskVisible}},
		// taskForeign owns exactly one item (deviceThree); taskVisible owns
		// two (deviceOne, deviceTwo). The unique (task_id, device_id) index
		// means a repeated pair here is an error, not a duplicate row.
		{`INSERT INTO command_task_item (id, task_id, tenant_id, device_id, state)
		  VALUES (gen_random_uuid(), $1,$3,$4,'succeeded'),
		         (gen_random_uuid(), $2,$3,$5,'succeeded'),
		         (gen_random_uuid(), $2,$3,$6,'succeeded')`,
			[]any{taskForeign, taskVisible, batchTestTenant, batchDeviceThree,
				batchDeviceOne, batchDeviceTwo}},
	}
	for _, stmt := range seed {
		if _, err := pool.Exec(ctx, stmt.q, stmt.args...); err != nil {
			t.Fatalf("seed task: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, id := range []string{taskForeign, taskVisible} {
			if _, err := pool.Exec(ctx,
				`DELETE FROM command_task WHERE id = $1`, id); err != nil {
				t.Logf("cleanup task: %v", err)
			}
		}
	})

	// TENANT scope sees both tasks.
	full := rbac.ContextWithScope(ctx,
		rbac.DataScope{Level: rbac.ScopeTenant, TenantID: batchTestTenant})
	page, err := store.ListTasks(full, batchTestTenant, 1, 50)
	if err != nil {
		t.Fatalf("ListTasks TENANT: %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("TENANT scope total = %d, want 2", page.Total)
	}

	// A CUSTOMER scope sees only the task with a matching item, and its count
	// must agree with the rows returned.
	narrow := rbac.ContextWithScope(ctx, rbac.DataScope{
		Level:      rbac.ScopeCustomer,
		TenantID:   batchTestTenant,
		CustomerID: customerThree,
	})
	page, err = store.ListTasks(narrow, batchTestTenant, 1, 50)
	if err != nil {
		t.Fatalf("ListTasks CUSTOMER: %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("CUSTOMER scope total = %d, want 1", page.Total)
	}
	if len(page.Items) != 1 {
		t.Fatalf("CUSTOMER scope returned %d rows, want 1", len(page.Items))
	}
	if page.Items[0].ID != taskForeign {
		t.Fatalf("CUSTOMER scope saw task %s, want %s",
			page.Items[0].ID, taskForeign)
	}

	// The items of a visible task are narrowed too.
	itemPage, err := store.ListItems(narrow, batchTestTenant, taskForeign, 1, 50)
	if err != nil {
		t.Fatalf("ListItems CUSTOMER: %v", err)
	}
	if itemPage.Total != 1 {
		t.Fatalf("visible task item total = %d, want 1", itemPage.Total)
	}
	if len(itemPage.Items) != 1 || itemPage.Items[0].DeviceID != batchDeviceThree {
		t.Fatalf("visible task items = %+v", itemPage.Items)
	}
}
