// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package silence

import (
	"testing"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
)

func ptrTime(t time.Time) *time.Time { return &t }

func TestDeadline(t *testing.T) {
	c := candidate{periodSeconds: 10, tolerance: 3}
	if got := deadline(c); got != 30*time.Second {
		t.Fatalf("deadline = %v, want 30s", got)
	}
	c.tolerance = 0
	if got := deadline(c); got != 30*time.Second {
		t.Fatalf("default-tolerance deadline = %v, want 30s", got)
	}
}

func TestLastSeenFallbacks(t *testing.T) {
	created := time.Unix(1000, 0).UTC()
	connected := time.Unix(2000, 0).UTC()
	telemetry := time.Unix(3000, 0).UTC()

	if got := lastSeen(candidate{createdAt: created, lastTelemetry: ptrTime(telemetry)}, connected); !got.Equal(telemetry) {
		t.Fatalf("telemetry fallback = %v, want telemetry", got)
	}
	if got := lastSeen(candidate{createdAt: created}, connected); !got.Equal(connected) {
		t.Fatalf("never-telemetry fallback = %v, want connectedAt", got)
	}
	if got := lastSeen(candidate{createdAt: created}, time.Time{}); !got.Equal(created) {
		t.Fatalf("no-connection fallback = %v, want createdAt", got)
	}
}

func TestOverdue(t *testing.T) {
	now := time.Unix(1000, 0).UTC()
	c := candidate{
		createdAt:     now.Add(-time.Hour),
		periodSeconds: 10,
		tolerance:     3, // 30s deadline
	}

	fresh := c
	fresh.lastTelemetry = ptrTime(now.Add(-5 * time.Second))
	if overdue(now, fresh, now.Add(-time.Hour)) {
		t.Fatal("fresh telemetry must not be overdue")
	}

	silent := candidate{periodSeconds: 10, tolerance: 3, createdAt: now.Add(-time.Hour),
		lastTelemetry: ptrTime(now.Add(-31 * time.Second))}
	if !overdue(now, silent, now.Add(-time.Hour)) {
		t.Fatal("telemetry older than deadline must be overdue")
	}

	// Never reported: grace runs from connection start, not createdAt.
	never := candidate{periodSeconds: 10, tolerance: 3, createdAt: now.Add(-24 * time.Hour)}
	if overdue(now, never, now.Add(-10*time.Second)) {
		t.Fatal("newly connected device with no telemetry must not be overdue yet")
	}
	if !overdue(now, never, now.Add(-31*time.Second)) {
		t.Fatal("device connected longer than deadline with no telemetry must be overdue")
	}
}

func TestReconcileTransitions(t *testing.T) {
	tenant := "11111111-1111-1111-1111-111111111111"
	k1 := deviceKey(tenant, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	k2 := deviceKey(tenant, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	k3 := deviceKey(tenant, "cccccccc-cccc-cccc-cccc-cccccccccccc")

	// k1 desired silent but no open alarm -> raise.
	// k2 desired silent and already open -> no action.
	// k3 has an open alarm but is no longer desired (offline/fresh) -> clear.
	desired := map[key]candidate{
		k1: {periodSeconds: 10, tolerance: 3},
		k2: {periodSeconds: 10, tolerance: 3},
	}
	open := map[key]alarm.OpenRef{
		k2: {TenantID: tenant, DeviceID: k2.device, DedupeKey: dedupeKey(k2.device)},
		k3: {TenantID: tenant, DeviceID: k3.device, DedupeKey: dedupeKey(k3.device)},
	}

	toRaise, toClear := reconcile(desired, open)
	if len(toRaise) != 1 || toRaise[0] != k1 {
		t.Fatalf("toRaise = %v, want only %v", toRaise, k1)
	}
	if len(toClear) != 1 || toClear[0].DeviceID != k3.device {
		t.Fatalf("toClear = %+v, want only %v", toClear, k3)
	}
}

func TestReconcileEmpty(t *testing.T) {
	toRaise, toClear := reconcile(map[key]candidate{}, map[key]alarm.OpenRef{})
	if len(toRaise) != 0 || len(toClear) != 0 {
		t.Fatalf("empty reconcile produced actions: %v %v", toRaise, toClear)
	}
}
