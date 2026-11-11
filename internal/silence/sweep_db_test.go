// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package silence

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/presence"
)

func silenceTestPool(t *testing.T) *pgxpool.Pool {
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
	return pool
}

type fakeOnline struct{ entries []presence.OnlineEntry }

func (f fakeOnline) ListAllOnline(context.Context) ([]presence.OnlineEntry, error) {
	return f.entries, nil
}

type fakeAlarms struct {
	mu      sync.Mutex
	created []alarm.CreateInput
	cleared []string
	open    []alarm.OpenRef
}

func (f *fakeAlarms) CreateAlarm(_ context.Context, in alarm.CreateInput) (alarm.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	return alarm.Outcome{Created: true}, nil
}

func (f *fakeAlarms) ClearAlarm(_ context.Context, _, _, dedupe string) (alarm.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, dedupe)
	return alarm.Outcome{}, nil
}

func (f *fakeAlarms) ListOpenByType(context.Context, string) ([]alarm.OpenRef, error) {
	return f.open, nil
}

func TestSweepRaisesAndClears(t *testing.T) {
	pool := silenceTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	const (
		tenant = "00000000-0000-0000-0000-00000000aa83"
		prof   = "00000000-0000-0000-0000-000000000083"
		devA   = "00000000-0000-0000-0000-00000000da83" // online, overdue
		devB   = "00000000-0000-0000-0000-00000000db83" // online, fresh; stale open alarm
	)

	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE id IN ($1,$2)`, devA, devB)
		_, _ = pool.Exec(ctx, `DELETE FROM measurement_descriptor WHERE profile_id=$1`, prof)
		_, _ = pool.Exec(ctx, `DELETE FROM device_profile WHERE id=$1`, prof)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, tenant)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'silence-test')
		 ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id,tenant_id,name)
		 VALUES ($1,$2,'silence-test-profile')
		 ON CONFLICT (id) DO NOTHING`, prof, tenant); err != nil {
		t.Fatal(err)
	}
	// 10s period * 3 tolerance = 30s deadline.
	if _, err := pool.Exec(ctx,
		`INSERT INTO measurement_descriptor
		   (id,tenant_id,profile_id,measurement_key,transform_type,
		    expected_period_seconds,tolerance_multiplier)
		 VALUES ($1,$2,$3,'m','identity',10,3)
		 ON CONFLICT (id) DO NOTHING`,
		"00000000-0000-0000-0000-000000000183", tenant, prof); err != nil {
		t.Fatal(err)
	}
	insertDevice := func(id string, last *time.Time) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO device (id,tenant_id,canonical_name,state,profile_id,last_telemetry_at)
			 VALUES ($1,$2,$3,'ACTIVE',$4,$5)
			 ON CONFLICT (id) DO UPDATE
			 SET state='ACTIVE', profile_id=EXCLUDED.profile_id,
			     last_telemetry_at=EXCLUDED.last_telemetry_at`,
			id, tenant, "silence-"+id, prof, last); err != nil {
			t.Fatal(err)
		}
	}
	stale := now.Add(-5 * time.Minute)
	fresh := now.Add(-5 * time.Second)
	insertDevice(devA, &stale)
	insertDevice(devB, &fresh)

	online := fakeOnline{entries: []presence.OnlineEntry{
		{TenantID: tenant, DeviceID: devA, ConnectedAt: now.Add(-10 * time.Minute)},
		{TenantID: tenant, DeviceID: devB, ConnectedAt: now.Add(-10 * time.Minute)},
	}}
	alarms := &fakeAlarms{open: []alarm.OpenRef{
		// Stale open alarm on the now-fresh device must be cleared.
		{TenantID: tenant, DeviceID: devB, DedupeKey: dedupeKey(devB)},
	}}

	s := New(pool, online, alarms, nil)
	s.nowFunc = func() time.Time { return now }
	if err := s.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if len(alarms.created) != 1 || alarms.created[0].DeviceID != devA {
		t.Fatalf("created = %+v, want single raise for devA", alarms.created)
	}
	if alarms.created[0].AlarmType != AlarmType ||
		alarms.created[0].Severity != alarm.SeverityWarning ||
		alarms.created[0].DedupeKey != dedupeKey(devA) {
		t.Fatalf("unexpected raise: %+v", alarms.created[0])
	}
	if len(alarms.cleared) != 1 || alarms.cleared[0] != dedupeKey(devB) {
		t.Fatalf("cleared = %v, want only stale devB alarm", alarms.cleared)
	}
}

func TestSweepSkipsOfflineAndUnmonitored(t *testing.T) {
	pool := silenceTestPool(t)
	ctx := context.Background()
	now := time.Now().UTC()

	const (
		tenant = "00000000-0000-0000-0000-00000000aa84"
		prof   = "00000000-0000-0000-0000-000000000084"
		devC   = "00000000-0000-0000-0000-00000000dc84" // overdue but OFFLINE
	)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM device WHERE id=$1`, devC)
		_, _ = pool.Exec(ctx, `DELETE FROM measurement_descriptor WHERE profile_id=$1`, prof)
		_, _ = pool.Exec(ctx, `DELETE FROM device_profile WHERE id=$1`, prof)
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id=$1`, tenant)
	})
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id,name) VALUES ($1,'silence-offline-test')
		 ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id,tenant_id,name)
		 VALUES ($1,$2,'silence-offline-profile') ON CONFLICT (id) DO NOTHING`,
		prof, tenant); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO measurement_descriptor
		   (id,tenant_id,profile_id,measurement_key,transform_type,expected_period_seconds)
		 VALUES ($1,$2,$3,'m','identity',10) ON CONFLICT (id) DO NOTHING`,
		"00000000-0000-0000-0000-000000000184", tenant, prof); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-time.Hour)
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id,tenant_id,canonical_name,state,profile_id,last_telemetry_at)
		 VALUES ($1,$2,'silence-offline-dev','ACTIVE',$3,$4)
		 ON CONFLICT (id) DO UPDATE SET last_telemetry_at=EXCLUDED.last_telemetry_at`,
		devC, tenant, prof, stale); err != nil {
		t.Fatal(err)
	}

	// No presence entries: the overdue device is offline -> nothing happens.
	alarms := &fakeAlarms{}
	s := New(pool, fakeOnline{}, alarms, nil)
	s.nowFunc = func() time.Time { return now }
	if err := s.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if len(alarms.created) != 0 || len(alarms.cleared) != 0 {
		t.Fatalf("offline overdue device produced actions: %+v / %v",
			alarms.created, alarms.cleared)
	}
}
