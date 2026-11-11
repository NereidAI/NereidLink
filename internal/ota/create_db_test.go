// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// create_db_test.go verifies Manager.Create's server-side behaviour against
// PostgreSQL: target snapshotting, model compatibility, already_current
// skipping and batch deferral (ADR-0006d §1, §3).
package ota

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeFirmware is a deterministic FirmwareResolver.
type fakeFirmware struct {
	meta FirmwareMeta
}

func (f fakeFirmware) ResolveFirmware(context.Context, string, string) (FirmwareMeta, error) {
	return f.meta, nil
}

// seedCreateFixture provisions the shared OTA test tenant, two profiles and
// direct (gateway-less) devices with explicit firmware versions, and returns
// cleanup. Device rows are returned by the requested map keyed by a label.
func seedCreateFixture(t *testing.T, pool *pgxpool.Pool, devices map[string]createDevice) func() {
	t.Helper()
	ctx := context.Background()
	profiles := map[string]struct{}{}
	for _, d := range devices {
		if d.profile != "" {
			profiles[d.profile] = struct{}{}
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'ota-create-test')
		 ON CONFLICT (id) DO NOTHING`, otaTestTenant); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	pA, pB := uuid.NewString(), uuid.NewString()
	profID := map[string]string{"A": pA, "B": pB}
	for label := range profiles {
		if _, ok := profID[label]; !ok {
			t.Fatalf("unknown profile label %q", label)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name)
		 VALUES ($1,$2,'ota-prof-a'), ($3,$2,'ota-prof-b')
		 ON CONFLICT (id) DO NOTHING`, pA, otaTestTenant, pB); err != nil {
		t.Fatalf("insert profile: %v", err)
	}
	ids := map[string]string{}
	for label, d := range devices {
		id := uuid.NewString()
		ids[label] = id
		var profileID any
		if d.profile != "" {
			profileID = profID[d.profile]
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id, tenant_id, canonical_name, state,
			                     profile_id, firmware_version)
			 VALUES ($1,$2,$3,'ACTIVE',$4,$5)`,
			id, otaTestTenant, "ota-create-"+label, profileID, d.version); err != nil {
			t.Fatalf("insert device %s: %v", label, err)
		}
	}
	return func() {
		c := context.Background()
		for _, id := range ids {
			_, _ = pool.Exec(c, `DELETE FROM device WHERE id=$1`, id)
		}
		_, _ = pool.Exec(c, `DELETE FROM device_profile WHERE id = ANY($1)`,
			[]string{pA, pB})
		_, _ = pool.Exec(c, `DELETE FROM tenant WHERE id=$1`, otaTestTenant)
	}
}

type createDevice struct {
	profile string // "A", "B" or ""
	version string
}

func newCreateManager(store *Store, meta FirmwareMeta) *Manager {
	return NewManager(store, nil, nil, fakeFirmware{meta: meta}, DefaultTuning(),
		slog.New(slog.NewTextHandler(nil, nil)))
}

func hostedReq(fwID string, ids []string, pct *int) Request {
	return Request{
		Devices:    ids,
		FirmwareID: fwID,
		Policy:     RolloutPolicy{BatchPercent: pct},
	}
}

// seedFirmwarePackage inserts a minimal repository package and returns its id.
// profileID "" creates a profile-agnostic package.
func seedFirmwarePackage(t *testing.T, pool *pgxpool.Pool, profileID, version string) string {
	t.Helper()
	id := uuid.NewString()
	var profileArg any
	if profileID != "" {
		profileArg = profileID
	}
	// Clear leftovers from an earlier aborted run (generic package versions
	// are unique per tenant).
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM ota_task WHERE firmware_id IN
		   (SELECT id FROM firmware_package WHERE tenant_id=$1)`,
		otaTestTenant); err != nil {
		t.Logf("clear stale ota tasks: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM firmware_package WHERE tenant_id=$1 AND version=$2`,
		otaTestTenant, version); err != nil {
		t.Logf("clear stale firmware: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO firmware_package
		   (id, tenant_id, profile_id, version, filename, content_type,
		    object_key, size_bytes, sha256, is_stable, name)
		 VALUES ($1,$2,$3,$4,'fw.bin','application/octet-stream',
		         $5,1024,$6,false,$7)`,
		id, otaTestTenant, profileArg, version, "ota-test-obj/"+id, validSHA,
		"ota-test-fw"); err != nil {
		t.Fatalf("insert firmware package: %v", err)
	}
	return id
}

func TestCreateAlreadyCurrentAndBatches(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	devices := map[string]createDevice{
		"d1": {profile: "A", version: "0.9.0"},
		"d2": {profile: "A", version: "0.9.0"},
		"d3": {profile: "A", version: "0.9.0"},
		"d4": {profile: "A", version: "0.9.0"},
	}
	cleanup := seedCreateFixture(t, pool, devices)
	defer cleanup()
	ctx := context.Background()

	mgr := newCreateManager(store, FirmwareMeta{
		Version: "1.0.0", SHA256: validSHA, Size: 1024,
	})
	fwID := seedFirmwarePackage(t, pool, "", "1.0.0")
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM firmware_package WHERE id=$1`, fwID)
	}()

	// 50% batches: exactly 2 devices open (batch 1) and 2 defer (batch -2),
	// independent of the id-sorted device order.
	pct50 := 50
	task, err := mgr.Create(ctx, otaTestTenant, "", Request{
		FirmwareID: fwID,
		Selector:   TargetSelector{ProfileID: profileOf(pool, t, "ota-prof-a")},
		Policy:     RolloutPolicy{BatchPercent: &pct50},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// The execute goroutine only dispatches gateway-bound items; every
	// fixture device is directly connected, so item rows are stable.
	time.Sleep(100 * time.Millisecond)

	items := listAllItems(t, store, task.ID)
	if len(items) != 4 {
		t.Fatalf("items=%d want 4", len(items))
	}
	var open, deferred int
	for _, it := range items {
		switch it.BatchNo {
		case 1:
			open++
		case -2:
			deferred++
		default:
			t.Fatalf("unexpected batch_no %d (%+v)", it.BatchNo, it)
		}
	}
	if open != 2 || deferred != 2 {
		t.Fatalf("open=%d deferred=%d want 2/2", open, deferred)
	}
}

func TestCreateAllAlreadyCurrentSettles(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	devices := map[string]createDevice{
		"d1": {profile: "A", version: "1.0.0"},
		"d2": {profile: "A", version: "1.0.0"},
	}
	cleanup := seedCreateFixture(t, pool, devices)
	defer cleanup()

	mgr := newCreateManager(store, FirmwareMeta{
		Version: "1.0.0", SHA256: validSHA, Size: 1024,
	})
	fwID := seedFirmwarePackage(t, pool, "", "1.0.0")
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM firmware_package WHERE id=$1`, fwID)
	}()

	task, err := mgr.Create(context.Background(), otaTestTenant, "", Request{
		FirmwareID: fwID,
		Selector:   TargetSelector{ProfileID: profileOf(pool, t, "ota-prof-a")},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	got, err := store.GetTask(context.Background(), otaTestTenant, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	// All items skipped: the aggregate settles as canceled (the existing
	// all-skipped aggregate), never as an in-progress rollout.
	if got.State != TaskStateCanceled {
		t.Fatalf("all already_current must settle, got %s", got.State)
	}
	items := listAllItems(t, store, task.ID)
	for _, it := range items {
		if it.State != ItemStateSkipped || it.ResultCode != ResultAlreadyCurrent {
			t.Fatalf("item must be skipped/already_current, got %+v", it)
		}
	}
}

func TestCreateRejectsIncompatibleModel(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	devices := map[string]createDevice{
		"d1": {profile: "A", version: "0.9"},
		"d2": {profile: "B", version: "0.9"}, // wrong model
	}
	cleanup := seedCreateFixture(t, pool, devices)
	defer cleanup()

	mgr := newCreateManager(store, FirmwareMeta{
		Version: "1.0", SHA256: validSHA, Size: 1,
		ProfileID: profileOf(pool, t, "ota-prof-a"),
	})
	fwID := seedFirmwarePackage(t, pool, profileOf(pool, t, "ota-prof-a"), "1.0")
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM firmware_package WHERE id=$1`, fwID)
	}()
	var ids []string
	rows, qerr := pool.Query(context.Background(),
		`SELECT id::text FROM device WHERE tenant_id=$1`, otaTestTenant)
	if qerr != nil {
		t.Fatal(qerr)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	_, err := mgr.Create(context.Background(), otaTestTenant, "", hostedReq(fwID, ids, nil))
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("incompatible model must be ErrInvalidArgument, got %v", err)
	}
}

func TestCreateRejectsDevicesAndSelector(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	mgr := newCreateManager(store, FirmwareMeta{Version: "1", SHA256: validSHA, Size: 1})
	_, err := mgr.Create(context.Background(), otaTestTenant, "", Request{
		Devices:    []string{uuid.NewString()},
		Selector:   TargetSelector{ProfileID: uuid.NewString()},
		FirmwareID: uuid.NewString(),
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("devices+selector must fail validation, got %v", err)
	}
}

func TestCreateRejectsMutuallyExclusiveBatchFields(t *testing.T) {
	store, pool := newOTAStore(t)
	resetOTAFixture(t, pool)
	t.Cleanup(func() { resetOTAFixture(t, pool) })
	mgr := newCreateManager(store, FirmwareMeta{Version: "1", SHA256: validSHA, Size: 1})
	_, err := mgr.Create(context.Background(), otaTestTenant, "", Request{
		FirmwareID: uuid.NewString(),
		Selector:   TargetSelector{ProfileID: uuid.NewString()},
		Policy: RolloutPolicy{
			BatchPercent: ptr(25),
			BatchSize:    ptr(10),
		},
	})
	if !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("batch_percent+batch_size must fail, got %v", err)
	}
}

// profileOf resolves a seeded profile name to its UUID.
func profileOf(pool *pgxpool.Pool, t *testing.T, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		`SELECT id::text FROM device_profile WHERE tenant_id=$1 AND name=$2`,
		otaTestTenant, name).Scan(&id); err != nil {
		t.Fatalf("profile %s: %v", name, err)
	}
	return id
}

// listAllItems reads the persisted items of a task directly for assertions.
func listAllItems(t *testing.T, store *Store, taskID string) []Item {
	t.Helper()
	rows, err := store.pool.Query(context.Background(),
		`SELECT `+itemColumns+` FROM ota_task_item WHERE task_id=$1 ORDER BY device_id`,
		taskID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	items, err := scanItems(rows)
	if err != nil {
		t.Fatal(err)
	}
	return items
}
