// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package stats collects platform system metrics and tenant business
// statistics for the statistics center.
//
// The package owns no background goroutines: the REST layer invokes the
// collectors on demand (the frontend refreshes on an interval). System
// snapshots cover the Go runtime, PostgreSQL, NATS/JetStream and the
// embedded MQTT broker; business reports aggregate device, telemetry,
// alarm and command data over a requested time range.
package stats

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// ClientCounter reports the embedded broker's connected client count.
// The method set keeps mochi types out of this package.
type ClientCounter interface {
	// ClientCount returns the number of live MQTT clients.
	ClientCount() int
}

// OnlineCounter reports the number of online devices of a tenant. The
// presence service satisfies it.
type OnlineCounter interface {
	// CountOnline returns the number of devices currently online.
	CountOnline(ctx context.Context, tenantID string) (int, error)
}

// Service runs the stats collectors against the shared infrastructure.
type Service struct {
	pool    *pgxpool.Pool
	js      jetstream.JetStream
	nc      *nats.Conn
	started time.Time
	broker  ClientCounter
	http    SessionCounter
	online  OnlineCounter
}

// NewService constructs the stats service. started is the process start
// time used for the uptime metric.
func NewService(pool *pgxpool.Pool, js jetstream.JetStream, nc *nats.Conn, started time.Time) *Service {
	return &Service{pool: pool, js: js, nc: nc, started: started}
}

// SetBroker wires the embedded broker counter.
func (s *Service) SetBroker(broker ClientCounter) {
	s.broker = broker
}

// SessionCounter reports the open HTTP device session count.
type SessionCounter interface {
	// SessionCount returns the number of live HTTP device sessions.
	SessionCount() int
}

// SetHTTPIngest wires the HTTP ingress session counter.
func (s *Service) SetHTTPIngest(counter SessionCounter) {
	s.http = counter
}

// SetOnline wires the presence online counter.
func (s *Service) SetOnline(online OnlineCounter) {
	s.online = online
}

// TenantGauges is the point-in-time tenant-scoped gauge set used by the
// Prometheus collector.
type TenantGauges struct {
	// TenantID labels every gauge.
	TenantID string

	// DevicesTotal is the live fleet count: every registered device except
	// DECOMMISSIONED ones, which are tombstone rows (devices are never
	// physically deleted) and must not inflate offline/fleet numbers.
	DevicesTotal int64

	// DevicesDecommissioned is the count of tombstoned devices.
	DevicesDecommissioned int64

	// DevicesOnline is the currently online count.
	DevicesOnline int

	// DevicesOffline is DevicesTotal minus DevicesOnline.
	DevicesOffline int64

	// AlarmsLive is the count of ACTIVE and ACK alarms.
	AlarmsLive int64

	// CommandsInflight is the non-terminal command count.
	CommandsInflight int64

	// RuleChainsActive is the count of active rule chains.
	RuleChainsActive int64

	// Customers is the number of customer entities in the tenant.
	Customers int64

	// DeviceGroups is the number of device groups in the tenant.
	DeviceGroups int64

	// Users is the number of human accounts in the tenant.
	Users int64

	// Memberships is the number of tenant membership rows. A single user may
	// hold more than one membership across customers, so this can exceed
	// Users; both are useful for capacity and RBAC auditing.
	Memberships int64

	// Credentials is the number of device credentials via the owning device.
	Credentials int64

	// Secrets is the number of tenant-stored connector secrets.
	Secrets int64

	// FirmwarePackages is the number of hosted firmware packages.
	FirmwarePackages int64

	// BatchTasks is the total number of batch command tasks.
	BatchTasks int64

	// BatchTasksActive is the number of batch tasks still in flight.
	BatchTasksActive int64

	// ProbeTasks is the total number of on-demand probe tasks.
	ProbeTasks int64

	// ProbeTasksActive is the number of probe tasks still in flight.
	ProbeTasksActive int64

	// OTATasks is the total number of OTA rollout tasks.
	OTATasks int64

	// OTATasksActive is the number of OTA rollouts still in flight.
	OTATasksActive int64
}

// Current returns point-in-time gauges for every tenant. Presence is
// enriched via the online counter.
func (s *Service) Current(ctx context.Context) ([]TenantGauges, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.id::text,
		        (SELECT count(*) FROM device d
		          WHERE d.tenant_id = t.id AND d.state <> 'DECOMMISSIONED'),
		        (SELECT count(*) FROM device d
		          WHERE d.tenant_id = t.id AND d.state = 'DECOMMISSIONED'),
		        (SELECT count(*) FROM alarm a
		          WHERE a.tenant_id = t.id AND a.state IN ('ACTIVE', 'ACK')),
		        (SELECT count(*) FROM command c
		          WHERE c.tenant_id = t.id
		            AND c.state IN ('PENDING', 'CLAIMED', 'QUEUED')),
		        (SELECT count(*) FROM rule_chain r
		          WHERE r.tenant_id = t.id AND r.state = 'ACTIVE'),
		        (SELECT count(*) FROM customer c WHERE c.tenant_id = t.id),
		        (SELECT count(*) FROM device_group g WHERE g.tenant_id = t.id),
		        (SELECT count(*) FROM app_user u WHERE u.tenant_id = t.id),
		        (SELECT count(*) FROM tenant_membership m WHERE m.tenant_id = t.id),
		        (SELECT count(*) FROM device_credential k
		          JOIN device d ON d.id = k.device_id
		          WHERE d.tenant_id = t.id),
		        (SELECT count(*) FROM tenant_secret s WHERE s.tenant_id = t.id),
		        (SELECT count(*) FROM firmware_package f WHERE f.tenant_id = t.id),
		        (SELECT count(*) FROM command_task ct WHERE ct.tenant_id = t.id),
		        (SELECT count(*) FROM command_task ct
		          WHERE ct.tenant_id = t.id AND ct.state IN ('pending','running')),
		        (SELECT count(*) FROM probe_task p WHERE p.tenant_id = t.id),
		        (SELECT count(*) FROM probe_task p
		          WHERE p.tenant_id = t.id AND p.state IN ('pending','running')),
		        (SELECT count(*) FROM ota_task o WHERE o.tenant_id = t.id),
		        (SELECT count(*) FROM ota_task o
		          WHERE o.tenant_id = t.id AND o.state IN ('pending','running'))
		 FROM tenant t ORDER BY t.id`)
	if err != nil {
		return nil, fmt.Errorf("query tenant gauges: %w", err)
	}
	defer rows.Close()

	out := []TenantGauges{}
	for rows.Next() {
		var gauge TenantGauges
		if err := rows.Scan(&gauge.TenantID, &gauge.DevicesTotal,
			&gauge.DevicesDecommissioned,
			&gauge.AlarmsLive, &gauge.CommandsInflight,
			&gauge.RuleChainsActive,
			&gauge.Customers, &gauge.DeviceGroups, &gauge.Users,
			&gauge.Memberships, &gauge.Credentials, &gauge.Secrets,
			&gauge.FirmwarePackages,
			&gauge.BatchTasks, &gauge.BatchTasksActive,
			&gauge.ProbeTasks, &gauge.ProbeTasksActive,
			&gauge.OTATasks, &gauge.OTATasksActive); err != nil {
			return nil, fmt.Errorf("scan tenant gauge: %w", err)
		}
		out = append(out, gauge)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if s.online != nil {
			online, err := s.online.CountOnline(ctx, out[i].TenantID)
			if err != nil {
				return nil, fmt.Errorf("count online: %w", err)
			}
			out[i].DevicesOnline = online
		}
		out[i].DevicesOffline = out[i].DevicesTotal - int64(out[i].DevicesOnline)
	}
	return out, nil
}
