// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package floodguard

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/ingestlimit"
)

type fakeAlarms struct {
	mu      sync.Mutex
	created []alarm.CreateInput
	cleared []clearCall
}

type clearCall struct {
	tenantID  string
	deviceID  string
	dedupeKey string
}

func (f *fakeAlarms) CreateAlarm(_ context.Context, in alarm.CreateInput) (alarm.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.created = append(f.created, in)
	return alarm.Outcome{Created: true}, nil
}

func (f *fakeAlarms) ClearAlarm(_ context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, clearCall{tenantID, deviceID, dedupeKey})
	return alarm.Outcome{}, nil
}

func (f *fakeAlarms) snapshot() ([]alarm.CreateInput, []clearCall) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created, f.cleared
}

const (
	tenant = "11111111-1111-1111-1111-111111111111"
	device = "22222222-2222-2222-2222-222222222222"
	key    = tenant + "." + device
)

func TestStartRaisesAlarmAndRecoverClears(t *testing.T) {
	alarms := &fakeAlarms{}
	svc := New(alarms)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	svc.FloodStarted(ingestlimit.FloodEvent{
		Key: key, Rejections: 12, At: time.Unix(0, 0),
	})
	svc.FloodRecovered(ingestlimit.FloodEvent{Key: key, At: time.Unix(60, 0)})

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		created, cleared := alarms.snapshot()
		if len(created) == 1 && len(cleared) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	created, cleared := alarms.snapshot()
	if len(created) != 1 {
		t.Fatalf("created = %d, want 1", len(created))
	}
	if created[0].AlarmType != AlarmType || created[0].DeviceID != device ||
		created[0].TenantID != tenant || created[0].Severity != alarm.SeverityWarning {
		t.Fatalf("unexpected create: %+v", created[0])
	}
	if created[0].DedupeKey != "ingest-flood:"+device {
		t.Fatalf("dedupe = %q", created[0].DedupeKey)
	}
	if len(cleared) != 1 || cleared[0].dedupeKey != "ingest-flood:"+device {
		t.Fatalf("unexpected clears: %+v", cleared)
	}
}

func TestMalformedKeyIgnored(t *testing.T) {
	alarms := &fakeAlarms{}
	svc := New(alarms)
	svc.handle(context.Background(), ingestlimit.FloodEvent{
		Key: "no-separator", Rejections: 5,
	})
	if created, _ := alarms.snapshot(); len(created) != 0 {
		t.Fatalf("malformed key raised an alarm: %d", len(created))
	}
}

func TestQueueFullDropsNotBlocks(t *testing.T) {
	alarms := &fakeAlarms{}
	svc := New(alarms) // worker not running: channel fills
	for i := 0; i < defaultQueueSize+10; i++ {
		svc.FloodStarted(ingestlimit.FloodEvent{Key: key, Rejections: 1})
	}
	if got := svc.dropped.Load(); got != 10 {
		t.Fatalf("dropped = %d, want 10", got)
	}
}
