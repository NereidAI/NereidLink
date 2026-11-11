// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// collector_test.go asserts the custom collector emits every tenant gauge the
// product is expected to expose, including the newer task/identity/firmware
// gauges and the in-process WebSocket client gauge. A new business gauge that
// is added to TenantGauges but forgotten in the collector would otherwise
// silently never appear at /metrics.

package prometrics

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/nereidai/nereidlink/internal/stats"
)

// fakeSource returns fixed snapshots for the collector.
type fakeSource struct {
	tenants []stats.TenantGauges
	system  stats.SystemSnapshot
}

func (f *fakeSource) System(_ context.Context) (stats.SystemSnapshot, error) {
	return f.system, nil
}

func (f *fakeSource) Current(_ context.Context) ([]stats.TenantGauges, error) {
	return f.tenants, nil
}

func TestCollectorEmitsTenantAndTaskMetrics(t *testing.T) {
	c := newNereidCollector(&fakeSource{tenants: []stats.TenantGauges{
		{
			TenantID: "tenant-1", DevicesTotal: 4, AlarmsLive: 2,
			Customers: 3, DeviceGroups: 5, Users: 7, Memberships: 9,
			Credentials: 11, Secrets: 13, FirmwarePackages: 2,
			BatchTasks: 6, BatchTasksActive: 1,
			ProbeTasks: 8, ProbeTasksActive: 2,
			OTATasks: 3, OTATasksActive: 1,
		},
	}}, time.Now(), func() int64 { return 3 }, nil)

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}

	names := make(map[string]*dto.MetricFamily, len(families))
	for _, fam := range families {
		names[fam.GetName()] = fam
	}

	expected := []string{
		"nereidlink_devices_total",
		"nereidlink_alarms_live",
		"nereidlink_tenant_customers",
		"nereidlink_tenant_device_groups",
		"nereidlink_tenant_users",
		"nereidlink_tenant_memberships",
		"nereidlink_tenant_device_credentials",
		"nereidlink_tenant_secrets",
		"nereidlink_firmware_packages",
		"nereidlink_tasks_total",
		"nereidlink_tasks_active",
		"nereidlink_ws_clients",
	}
	for _, name := range expected {
		if _, ok := names[name]; !ok {
			t.Errorf("expected metric family %q to be present", name)
		}
	}

	taskKinds := map[string]float64{}
	for _, m := range names["nereidlink_tasks_total"].GetMetric() {
		for _, l := range m.GetLabel() {
			if l.GetName() == "kind" {
				taskKinds[l.GetValue()] = m.GetGauge().GetValue()
			}
		}
	}
	for kind, want := range map[string]float64{
		"batch_command": 6, "probe": 8, "ota": 3,
	} {
		if taskKinds[kind] != want {
			t.Errorf("tasks_total kind %q = %v, want %v", kind, taskKinds[kind], want)
		}
	}

	if got := names["nereidlink_ws_clients"].GetMetric()[0].GetGauge().GetValue(); got != 3 {
		t.Errorf("ws_clients = %v, want 3", got)
	}
}

// TestCollectorCapacityWatermarks asserts the ADR-0025 current/limit/
// utilization watermarks: ratio math, suppression when the limit is zero
// (unlimited), and fixed plane labels for queues.
func TestCollectorCapacityWatermarks(t *testing.T) {
	system := stats.SystemSnapshot{
		Postgres: stats.PostgresStats{TotalConnections: 5, PoolMaxConns: 10},
		Streams: []stats.StreamStats{
			{
				Name: "TL_S0",
				Consumers: []stats.ConsumerStats{
					{Name: "telemetry-writer-s0", Pending: 2500, MaxAckPending: 10000},
					{Name: "rp-s0", Pending: 3, MaxAckPending: 0},
				},
			},
		},
	}
	queues := func() []stats.QueueWatermark {
		return []stats.QueueWatermark{
			{Plane: "presence_persist", Current: 1024, Limit: 4096},
			{Plane: "notify_queue", Sub: "critical", Current: 1024, Limit: 1024},
			{Plane: "notify_queue", Sub: "normal", Current: 10, Limit: 0},
		}
	}
	c := newNereidCollector(&fakeSource{system: system}, time.Now(), nil, queues)

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	byName := map[string]*dto.MetricFamily{}
	for _, fam := range families {
		byName[fam.GetName()] = fam
	}

	cur := byName["nereidlink_capacity_current"]
	lim := byName["nereidlink_capacity_limit"]
	util := byName["nereidlink_capacity_utilization_ratio"]
	if cur == nil || lim == nil || util == nil {
		t.Fatalf("missing capacity families: cur=%v lim=%v util=%v", cur != nil, lim != nil, util != nil)
	}

	// labelset -> current / limit / utilization(-1 means absent).
	type row struct{ current, limit, util float64 }
	rows := map[string]*row{}
	keyFor := func(m *dto.Metric) string {
		var plane, stream, consumer, queue string
		for _, l := range m.GetLabel() {
			switch l.GetName() {
			case "plane":
				plane = l.GetValue()
			case "stream":
				stream = l.GetValue()
			case "consumer":
				consumer = l.GetValue()
			case "queue":
				queue = l.GetValue()
			}
		}
		return plane + "|" + stream + "|" + consumer + "|" + queue
	}
	collect := func(fam *dto.MetricFamily) {
		for _, m := range fam.GetMetric() {
			k := keyFor(m)
			r := rows[k]
			if r == nil {
				r = &row{util: -1}
				rows[k] = r
			}
			switch fam.GetName() {
			case "nereidlink_capacity_current":
				r.current = m.GetGauge().GetValue()
			case "nereidlink_capacity_limit":
				r.limit = m.GetGauge().GetValue()
			case "nereidlink_capacity_utilization_ratio":
				r.util = m.GetGauge().GetValue()
			}
		}
	}
	collect(cur)
	collect(lim)
	collect(util)

	want := map[string]row{
		"postgres_pool|||": {5, 10, 0.5},
		"jetstream_consumer|TL_S0|telemetry-writer-s0|": {2500, 10000, 0.25},
		"jetstream_consumer|TL_S0|rp-s0|":               {3, 0, -1},
		"presence_persist|||":                           {1024, 4096, 0.25},
		"notify_queue|||critical":                       {1024, 1024, 1},
		"notify_queue|||normal":                         {10, 0, -1},
	}
	for k, w := range want {
		got, ok := rows[k]
		if !ok {
			t.Errorf("missing watermark %q", k)
			continue
		}
		if got.current != w.current || got.limit != w.limit {
			t.Errorf("%s current/limit = %v/%v, want %v/%v", k, got.current, got.limit, w.current, w.limit)
		}
		if w.util < 0 {
			if got.util >= 0 {
				t.Errorf("%s utilization = %v, want suppressed", k, got.util)
			}
		} else if got.util != w.util {
			t.Errorf("%s utilization = %v, want %v", k, got.util, w.util)
		}
	}
}
