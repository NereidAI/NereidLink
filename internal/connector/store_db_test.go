// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package connector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const dbTestTenant = "00000000-0000-0000-0000-00000000d704"

// TestStoreConnectorLifecycle exercises migration 0040 against a real
// database: gateway guard, create/update revision, status reporting,
// dispatch state, tenant isolation and the at-most-once alarm claims.
func TestStoreConnectorLifecycle(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM connector_delete_outbox WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM connector_instance WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name = 'cn-gw-a'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'connector-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gw string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES (gen_random_uuid(), $1, 'cn-gw-a', TRUE) RETURNING id::text`,
		dbTestTenant).Scan(&gw); err != nil {
		t.Fatalf("create gateway: %v", err)
	}

	// A plain, non-existent / non-gateway target must fail the guard.
	ok, err := store.GatewayExists(ctx, dbTestTenant, "00000000-0000-0000-0000-0000000000ff")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("unknown gateway must not pass the guard")
	}
	ok, err = store.GatewayExists(ctx, dbTestTenant, gw)
	if err != nil || !ok {
		t.Fatalf("created gateway must pass guard: ok=%v err=%v", ok, err)
	}

	in := SaveInput{
		ConnectorType: "modbus-tcp",
		TypeVersion:   "1.0.0",
		Config:        json.RawMessage(`{"host":"10.0.0.5"}`),
		Mappings:      json.RawMessage(`[{"point":"temp"}]`),
	}
	sp, err := store.Create(ctx, dbTestTenant, gw, in, in.ConnectorType, in.TypeVersion)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if sp.Revision != 1 || sp.State != StatePending || string(sp.Config) == "" {
		t.Fatalf("unexpected created spec: %+v", sp)
	}

	now := time.Now().UTC()
	if err := store.MarkDispatched(ctx, dbTestTenant, sp.ID, now); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, dbTestTenant, sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateDispatched || got.DispatchedAt == nil {
		t.Fatalf("expected DISPATCHED with timestamp, got %+v", got)
	}

	// Edge reports APPLIED for revision 1.
	applied, err := store.ApplyStatus(ctx, dbTestTenant,
		StatusFrame{SpecID: sp.ID, State: StateApplied, Revision: 1}, now)
	if err != nil {
		t.Fatal(err)
	}
	if applied.State != StateApplied || applied.ReportedRevision != 1 ||
		applied.ReportedAt == nil {
		t.Fatalf("unexpected APPLIED row: %+v", applied)
	}

	// Updating desired bumps revision and resets the reported half.
	upd, err := store.Update(ctx, dbTestTenant, sp.ID, SaveInput{ConnectorType: "modbus-tcp"},
		"modbus-tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	if upd.Revision != 2 || upd.State != StatePending || upd.ReportedRevision != 0 ||
		upd.DispatchedAt != nil || upd.ReportedAt != nil {
		t.Fatalf("update must reset lifecycle: %+v", upd)
	}
	if string(upd.Config) != "{}" || string(upd.Mappings) != "[]" {
		t.Fatalf("empty config/mappings must default to {} / [], got %s %s", upd.Config, upd.Mappings)
	}

	// List by gateway sees exactly the one spec.
	list, err := store.ListByGateway(ctx, dbTestTenant, gw)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %d specs, err=%v", len(list), err)
	}

	// Tenant isolation: another tenant cannot see or update it.
	if _, err := store.Get(ctx, "00000000-0000-0000-0000-00000000d999", sp.ID); err != ErrNotFound {
		t.Fatalf("cross-tenant get must be ErrNotFound, got %v", err)
	}

	// A fresh FAILED spec is claimed once for the MAJOR raise.
	failed, err := store.Create(ctx, dbTestTenant, gw,
		SaveInput{ConnectorType: "opcua"}, "opcua", "")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().UTC().Add(-2 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE connector_instance SET desired_at=$2, failed_at=$2
		 WHERE id=$1`, failed.ID, base); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyStatus(ctx, dbTestTenant,
		StatusFrame{SpecID: failed.ID, State: StateFailed, Revision: 1,
			Reason: "refused"}, now); err != nil {
		t.Fatal(err)
	}
	newFailed, err := store.ClaimFailedForRaise(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if !containsSpec(newFailed, failed.ID) {
		t.Fatal("new FAILED spec must be claimed for MAJOR raise")
	}
	again, _ := store.ClaimFailedForRaise(ctx, now)
	if containsSpec(again, failed.ID) {
		t.Fatal("FAILED raise claim must be at-most-once")
	}
	// It is immediately eligible for the 1-hour CRITICAL escalation.
	due, err := store.ClaimFailedForEscalation(ctx, now, now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !containsSpec(due, failed.ID) {
		t.Fatal("hour-old FAILED spec must be claimed for CRITICAL escalation")
	}
	if d, _ := store.ClaimFailedForEscalation(ctx, now, now.Add(-time.Hour)); containsSpec(d, failed.ID) {
		t.Fatal("CRITICAL escalation claim must be at-most-once")
	}
	// FAILED rows are not pending-claimable (only PENDING/DISPATCHED/CREATING).
	pendingFailed, err := store.ClaimPendingForAlarm(ctx, now, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if containsSpec(pendingFailed, failed.ID) {
		t.Fatal("FAILED spec must not be pending-claimable")
	}

	// Age the updated (PENDING, revision 2) modbus spec so it is pending
	// claimable once, then at-most-once.
	if _, err := pool.Exec(ctx,
		`UPDATE connector_instance SET desired_at=$2 WHERE id=$1`,
		sp.ID, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ClaimPendingForAlarm(ctx, now, now.Add(-5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !containsSpec(pending, sp.ID) {
		t.Fatal("stale PENDING spec must be pending-claimable")
	}
	if p, _ := store.ClaimPendingForAlarm(ctx, now, now.Add(-5*time.Minute)); containsSpec(p, sp.ID) {
		t.Fatal("pending claim must be at-most-once")
	}

	// APPLIED clears the escalation flags, so a later report re-arms claims.
	if _, err := store.ApplyStatus(ctx, dbTestTenant,
		StatusFrame{SpecID: failed.ID, State: StateApplied, Revision: 1}, now); err != nil {
		t.Fatal(err)
	}
	row, _ := store.Get(ctx, dbTestTenant, failed.ID)
	if row.NotAppliedAlarm || row.FailedAlarm || row.FailedCritical {
		t.Fatalf("APPLIED must clear escalation flags: %+v", row)
	}

	// Delete removes the row and is tenant scoped.
	if _, err := store.Delete(ctx, dbTestTenant, sp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Delete(ctx, dbTestTenant, sp.ID); err != ErrNotFound {
		t.Fatalf("second delete must be ErrNotFound, got %v", err)
	}
}

func containsSpec(specs []Spec, id string) bool {
	for _, sp := range specs {
		if sp.ID == id {
			return true
		}
	}
	return false
}

// TestStoreConnectorTemplateAndBinding exercises migration 0042: fetching a
// profile connector template, the sub-device ownership guard, persisting the
// binding/profile reference, and the one-instance-per-sub-device unique index.
func TestStoreConnectorTemplateAndBinding(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM connector_delete_outbox WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM connector_instance WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name IN ('cn-gw-b','cn-sub-b')`)
		_, _ = pool.Exec(ctx, `DELETE FROM device_profile WHERE id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name = 'cn-gw-tpl'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'connector-template-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gw, sub string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway, state)
		 VALUES (gen_random_uuid(),$1,'cn-gw-tpl',TRUE,'ACTIVE') RETURNING id::text`,
		dbTestTenant).Scan(&gw); err != nil {
		t.Fatalf("gateway: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway, gateway_id, state)
		 VALUES (gen_random_uuid(),$1,'cn-sub-b',FALSE,$2,'ACTIVE') RETURNING id::text`,
		dbTestTenant, gw).Scan(&sub); err != nil {
		t.Fatalf("sub device: %v", err)
	}
	// Profile carrying a connector template.
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile
		    (id, tenant_id, name, connector_type, connector_type_version,
		     connector_config_template, connector_mappings_template)
		 VALUES ($1,$1,'cn-tpl','modbus-tcp','2.1',
		         '{"host":"tpl","port":502}'::jsonb,
		         '[{"register":"40001"}]'::jsonb)`, dbTestTenant); err != nil {
		t.Fatalf("profile: %v", err)
	}

	// Ownership guard: the sub-device belongs to the gateway.
	ok, err := store.DeviceBelongsToGateway(ctx, dbTestTenant, gw, sub)
	if err != nil || !ok {
		t.Fatalf("sub-device guard: ok=%v err=%v", ok, err)
	}
	ok, err = store.DeviceBelongsToGateway(ctx, dbTestTenant, gw, gw)
	if err != nil || ok {
		t.Fatalf("a gateway must not be treated as its own sub-device: ok=%v", ok)
	}

	// Template fetch.
	tpls, err := store.FetchTemplates(ctx, dbTestTenant, []string{dbTestTenant})
	if err != nil {
		t.Fatal(err)
	}
	tpl, ok := tpls[dbTestTenant]
	if !ok || tpl.ConnectorType != "modbus-tcp" || string(tpl.Config) == "" {
		t.Fatalf("template not fetched correctly: %+v ok=%v", tpl, ok)
	}

	// Persist a bound instance: raw overrides and the bindings are stored.
	sp, err := store.Create(ctx, dbTestTenant, gw, SaveInput{
		DeviceID: sub, ProfileID: dbTestTenant,
		Config: json.RawMessage(`{"host":"dev"}`),
	}, "modbus-tcp", "2.2")
	if err != nil {
		t.Fatalf("create bound: %v", err)
	}
	if sp.DeviceID != sub || sp.ProfileID != dbTestTenant {
		t.Fatalf("bindings not persisted: %+v", sp)
	}
	var cfg string
	if err := pool.QueryRow(ctx,
		`SELECT config::text FROM connector_instance WHERE id=$1`, sp.ID).Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	if cfg != `{"host": "dev"}` {
		t.Fatalf("stored config must be raw overrides, got %s", cfg)
	}

	// A second instance bound to the same sub-device hits the unique index.
	if _, err := store.Create(ctx, dbTestTenant, gw, SaveInput{
		ConnectorType: "x", DeviceID: sub,
	}, "x", ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate per-device binding must be ErrConflict, got %v", err)
	}
}

// TestStoreConnectorManifest exercises migration 0044: the full-list
// watermark stamp (reported specs updated, omitted specs left NULL so they
// read as missing) and the content-preserving revision bump used by strict
// reconciliation.
func TestStoreConnectorManifest(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)

	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM connector_delete_outbox WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM connector_instance WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name='cn-gw-m'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1,'connector-manifest-test')
		 ON CONFLICT (id) DO NOTHING`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gw string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, is_gateway)
		 VALUES (gen_random_uuid(),$1,'cn-gw-m',TRUE) RETURNING id::text`,
		dbTestTenant).Scan(&gw); err != nil {
		t.Fatalf("gateway: %v", err)
	}

	reported, err := store.Create(ctx, dbTestTenant, gw,
		SaveInput{ConnectorType: "modbus-tcp", Config: json.RawMessage(`{"host":"a"}`)},
		"modbus-tcp", "")
	if err != nil {
		t.Fatal(err)
	}
	omitted, err := store.Create(ctx, dbTestTenant, gw,
		SaveInput{ConnectorType: "onvif", Config: json.RawMessage(`{"host":"b"}`)},
		"onvif", "")
	if err != nil {
		t.Fatal(err)
	}

	at := time.Now().UTC()
	rows, err := store.StampManifest(ctx, dbTestTenant, gw, []ReportedInstance{
		{SpecID: reported.ID, Revision: 1, ConfigHash: "abc"},
	}, at)
	if err != nil {
		t.Fatalf("stamp: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("stamp must return all desired specs, got %d", len(rows))
	}
	byID := map[string]Spec{}
	for _, sp := range rows {
		byID[sp.ID] = sp
	}
	r := byID[reported.ID]
	if r.ManifestRevision != 1 || r.ManifestHash != "abc" || r.LastManifestAt == nil {
		t.Fatalf("reported spec watermark not stamped: %+v", r)
	}
	o := byID[omitted.ID]
	if o.ManifestRevision != 0 || o.ManifestHash != "" || o.LastManifestAt != nil {
		t.Fatalf("omitted spec must keep a NULL watermark (missing): %+v", o)
	}

	// A report carrying another gateway's/tenant's spec id must not stamp it.
	foreign, err := store.StampManifest(ctx, dbTestTenant, gw, []ReportedInstance{
		{SpecID: "00000000-0000-0000-0000-0000000000ff", Revision: 9},
	}, at)
	if err != nil {
		t.Fatalf("foreign stamp must be a no-op, got %v", err)
	}
	if len(foreign) != 2 {
		t.Fatalf("foreign report must not change desired set, got %d", len(foreign))
	}

	// Bump advances revision without changing content and resets lifecycle.
	bumped, err := store.BumpRevision(ctx, dbTestTenant, reported.ID)
	if err != nil {
		t.Fatalf("bump: %v", err)
	}
	if bumped.Revision != 2 || bumped.State != StatePending {
		t.Fatalf("bump must advance to rev2 PENDING, got rev=%d state=%s",
			bumped.Revision, bumped.State)
	}
	// jsonb is returned re-spaced; compare semantically rather than bytes.
	var bumpCfg map[string]any
	if err := json.Unmarshal(bumped.Config, &bumpCfg); err != nil || bumpCfg["host"] != "a" {
		t.Fatalf("bump must preserve desired config, got %s", bumped.Config)
	}
	if _, err := store.BumpRevision(ctx, dbTestTenant,
		"00000000-0000-0000-0000-0000000000ff"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bump unknown spec must be ErrNotFound, got %v", err)
	}
}

// TestStoreDeleteOutboxAndOptionalStatus exercises migration 0089 against a
// migrated disposable database. No ordinary status is treated as a delete ACK.
func TestStoreDeleteOutboxAndOptionalStatus(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewStore(pool)
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM connector_delete_outbox WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM connector_instance WHERE tenant_id=$1`, dbTestTenant)
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE canonical_name='cn-gw-outbox'`)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, dbTestTenant)
	}
	clean()
	t.Cleanup(clean)
	if _, err := pool.Exec(ctx, `INSERT INTO tenant(id,name) VALUES($1,'connector-outbox-test')`, dbTestTenant); err != nil {
		t.Fatal(err)
	}
	var gateway string
	if err := pool.QueryRow(ctx,
		`INSERT INTO device(id,tenant_id,canonical_name,is_gateway)
		 VALUES(gen_random_uuid(),$1,'cn-gw-outbox',TRUE) RETURNING id::text`, dbTestTenant).Scan(&gateway); err != nil {
		t.Fatal(err)
	}
	sp, err := store.Create(ctx, dbTestTenant, gateway, SaveInput{
		ConnectorType: "edge-manager-resource", TypeVersion: "1",
		Config: json.RawMessage(`{"manager_device":"nereid-inference","resource":"PipelinesJSON","pipeline_id":"p1"}`),
	}, "edge-manager-resource", "1")
	if err != nil {
		t.Fatal(err)
	}
	zero := uint64(0)
	p50 := 47.5
	observed := int64(1)
	now := time.Now().UTC()
	got, err := store.ApplyStatus(ctx, dbTestTenant, StatusFrame{
		SpecID: sp.ID, State: StateApplied, Revision: 1,
		Stats:            &StatusStats{SuccessTotal: &zero, P50MS: &p50},
		ObservedRevision: &observed, ObservedHash: "readback-hash",
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats == nil || got.Stats.SuccessTotal == nil || *got.Stats.SuccessTotal != 0 ||
		got.Stats.ErrorTotal != nil || got.Stats.P50MS == nil || got.Stats.P95MS != nil ||
		got.ObservedRevision == nil || *got.ObservedRevision != 1 || got.ObservedHash != "readback-hash" {
		t.Fatalf("optional status did not preserve independent zero/presence: %+v", got)
	}

	// A partial replacement neither merges old stats nor retains observed data.
	errorZero := uint64(0)
	got, err = store.ApplyStatus(ctx, dbTestTenant, StatusFrame{
		SpecID: sp.ID, State: StateApplied, Revision: 1,
		Stats: &StatusStats{ErrorTotal: &errorZero},
	}, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats == nil || got.Stats.SuccessTotal != nil || got.Stats.ErrorTotal == nil ||
		*got.Stats.ErrorTotal != 0 || got.Stats.P50MS != nil || got.ObservedRevision != nil || got.ObservedHash != "" {
		t.Fatalf("partial status retained stale fields: %+v", got)
	}
	got, err = store.ApplyStatus(ctx, dbTestTenant, StatusFrame{
		SpecID: sp.ID, State: StateApplied, Revision: 1,
	}, now.Add(2*time.Second))
	if err != nil || got.Stats != nil {
		t.Fatalf("omitted stats must clear the projection: stats=%+v err=%v", got.Stats, err)
	}

	// Older and unknown-future reports are no-ops, including all optional data.
	for _, revision := range []int64{0, 2} {
		fenced, err := store.ApplyStatus(ctx, dbTestTenant, StatusFrame{
			SpecID: sp.ID, State: StateFailed, Revision: revision, Reason: "stale",
			Stats: &StatusStats{SuccessTotal: &zero}, ObservedHash: "wrong",
		}, now.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if fenced.State != StateApplied || fenced.ReportReason != "" || fenced.ReportedRevision != 1 || fenced.Stats != nil {
			t.Fatalf("revision-fenced report mutated status: %+v", fenced)
		}
	}

	updated, err := store.Update(ctx, dbTestTenant, sp.ID, SaveInput{
		ConnectorType: "edge-manager-resource", TypeVersion: "1", Config: sp.Config,
	}, "edge-manager-resource", "1")
	if err != nil || updated.Revision != 2 || updated.Stats != nil || updated.ObservedRevision != nil {
		t.Fatalf("desired update did not reset optional projection: %+v err=%v", updated, err)
	}
	deleted, err := store.Delete(ctx, dbTestTenant, sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != 2 {
		t.Fatalf("delete revision=%d want 2", deleted.Revision)
	}
	tombstones, err := store.ListDeleteTombstones(ctx, gateway, 100)
	if err != nil || len(tombstones) != 1 {
		t.Fatalf("delete tombstone missing: %+v err=%v", tombstones, err)
	}
	tombstone := tombstones[0]
	if tombstone.SpecID != sp.ID || tombstone.DeleteRevision != 2 || tombstone.DeviceID != "" ||
		tombstone.ConnectorType != "edge-manager-resource" || tombstone.TypeVersion != "1" {
		t.Fatalf("wrong tombstone identity: %+v", tombstone)
	}
	if err := store.MarkDeleteAttempt(ctx, tombstone, now, "publish failed"); err != nil {
		t.Fatal(err)
	}
	tombstones, _ = store.ListDeleteTombstones(ctx, gateway, 100)
	if len(tombstones) != 1 || tombstones[0].State != StatePending || tombstones[0].AttemptCount != 1 ||
		tombstones[0].LastError != "publish failed" {
		t.Fatalf("failed publish was not durable/retryable: %+v", tombstones)
	}

	// An old apply ACK and duplicate API delete cannot retire the tombstone.
	if _, err := store.ApplyStatus(ctx, dbTestTenant, StatusFrame{
		SpecID: sp.ID, State: StateApplied, Revision: 2,
	}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("status for deleted desired must be ErrNotFound, got %v", err)
	}
	if _, err := store.Delete(ctx, dbTestTenant, sp.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("duplicate delete must be idempotent without duplication, got %v", err)
	}
	tombstones, _ = store.ListDeleteTombstones(ctx, gateway, 100)
	if len(tombstones) != 1 {
		t.Fatalf("ordinary apply ACK/delete duplicate retired tombstone: %+v", tombstones)
	}

	// A newer desired revision supersedes an older delete in the same update.
	recreated, err := store.Create(ctx, dbTestTenant, gateway, SaveInput{
		ConnectorType: "legacy", Config: json.RawMessage(`{}`),
	}, "legacy", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO connector_delete_outbox(tenant_id,gateway_id,spec_id,delete_revision)
		 VALUES($1,$2,$3,1)`, dbTestTenant, gateway, recreated.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BumpRevision(ctx, dbTestTenant, recreated.ID); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM connector_delete_outbox WHERE spec_id=$1`, recreated.ID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatal("new desired revision did not supersede its older delete outbox")
	}
}
