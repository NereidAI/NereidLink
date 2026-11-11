// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// collector.go implements the custom Prometheus collector. Every scrape
// fetches a fresh stats snapshot and emits gauges/counters derived from
// it.

package prometrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nereidai/nereidlink/internal/stats"
)

// scrapeTimeout bounds the stats collection during a Prometheus scrape.
const scrapeTimeout = 10_000_000_000

// nereidCollector adapts stats.Service to prometheus.Collector.
type nereidCollector struct {
	source    Source
	startedAt int64

	uptime *prometheus.Desc

	pgMaxConns       *prometheus.Desc
	pgActiveConns    *prometheus.Desc
	pgPoolTotal      *prometheus.Desc
	pgPoolIdle       *prometheus.Desc
	pgTxCommit       *prometheus.Desc
	pgTxRollback     *prometheus.Desc
	pgBufferHitRatio *prometheus.Desc
	pgDeadlocks      *prometheus.Desc

	natsInMsgs     *prometheus.Desc
	natsOutMsgs    *prometheus.Desc
	natsInBytes    *prometheus.Desc
	natsOutBytes   *prometheus.Desc
	natsReconnects *prometheus.Desc

	streamMessages      *prometheus.Desc
	streamBytes         *prometheus.Desc
	streamFirstSeq      *prometheus.Desc
	streamLastSeq       *prometheus.Desc
	streamConsumerCount *prometheus.Desc

	consumerPending     *prometheus.Desc
	consumerRedelivered *prometheus.Desc
	consumerAckFloor    *prometheus.Desc

	brokerClients *prometheus.Desc

	devicesTotal          *prometheus.Desc
	devicesDecommissioned *prometheus.Desc
	devicesOnline         *prometheus.Desc
	devicesOffline        *prometheus.Desc
	alarmsLive            *prometheus.Desc
	commandsInflight      *prometheus.Desc
	ruleChainsActive      *prometheus.Desc

	tenantCustomers        *prometheus.Desc
	tenantDeviceGroups     *prometheus.Desc
	tenantUsers            *prometheus.Desc
	tenantMemberships      *prometheus.Desc
	tenantCredentials      *prometheus.Desc
	tenantSecrets          *prometheus.Desc
	tenantFirmwarePackages *prometheus.Desc

	taskTotal  *prometheus.Desc
	taskActive *prometheus.Desc

	wsClients *prometheus.Desc

	// capacity current/limit/utilization, ADR-0025 watermarks. Plane is a
	// fixed, low-cardinality value; stream/consumer are empty for planes
	// other than jetstream_consumer.
	capCurrent     *prometheus.Desc
	capLimit       *prometheus.Desc
	capUtilization *prometheus.Desc

	// liveClients returns the current WebSocket client count. It is optional;
	// a nil func suppresses the gauge so non-gateway processes can reuse the
	// collector without reporting a meaningless zero.
	liveClients func() int64

	// queueWatermarks returns bounded in-process queue occupancy at scrape
	// time. It is optional; nil suppresses queue watermarks.
	queueWatermarks func() []stats.QueueWatermark
}

// newNereidCollector constructs the collector and its descriptors. liveClients,
// when non-nil, supplies the live WebSocket client count at scrape time.
// queueWatermarks, when non-nil, supplies bounded in-process queue occupancy
// for the ADR-0025 capacity watermark gauges.
func newNereidCollector(source Source, startedAt time.Time, liveClients func() int64, queueWatermarks func() []stats.QueueWatermark) *nereidCollector {
	return &nereidCollector{
		source:          source,
		startedAt:       startedAt.Unix(),
		liveClients:     liveClients,
		queueWatermarks: queueWatermarks,
		uptime: prometheus.NewDesc("nereidlink_uptime_seconds",
			"Process uptime in seconds.", nil, nil),

		pgMaxConns: prometheus.NewDesc("nereidlink_postgres_connections_max",
			"PostgreSQL max_connections setting.", nil, nil),
		pgActiveConns: prometheus.NewDesc("nereidlink_postgres_connections_active",
			"Active PostgreSQL backends.", nil, nil),
		pgPoolTotal: prometheus.NewDesc("nereidlink_postgres_pool_connections",
			"Total connections in the application pool.", nil, nil),
		pgPoolIdle: prometheus.NewDesc("nereidlink_postgres_pool_idle_connections",
			"Idle connections in the application pool.", nil, nil),
		pgTxCommit: prometheus.NewDesc("nereidlink_postgres_transactions_committed_total",
			"Total committed transactions.", nil, nil),
		pgTxRollback: prometheus.NewDesc("nereidlink_postgres_transactions_rolledback_total",
			"Total rolled back transactions.", nil, nil),
		pgBufferHitRatio: prometheus.NewDesc("nereidlink_postgres_buffer_hit_ratio_percent",
			"Shared buffer read hit ratio in percent.", nil, nil),
		pgDeadlocks: prometheus.NewDesc("nereidlink_postgres_deadlocks_total",
			"Total recorded deadlocks.", nil, nil),

		natsInMsgs: prometheus.NewDesc("nereidlink_nats_messages_in_total",
			"NATS client inbound messages.", nil, nil),
		natsOutMsgs: prometheus.NewDesc("nereidlink_nats_messages_out_total",
			"NATS client outbound messages.", nil, nil),
		natsInBytes: prometheus.NewDesc("nereidlink_nats_bytes_in_total",
			"NATS client inbound bytes.", nil, nil),
		natsOutBytes: prometheus.NewDesc("nereidlink_nats_bytes_out_total",
			"NATS client outbound bytes.", nil, nil),
		natsReconnects: prometheus.NewDesc("nereidlink_nats_reconnects_total",
			"NATS client reconnect count.", nil, nil),

		streamMessages: prometheus.NewDesc("nereidlink_jetstream_stream_messages",
			"Messages stored in the stream.", []string{"stream"}, nil),
		streamBytes: prometheus.NewDesc("nereidlink_jetstream_stream_bytes",
			"Bytes stored in the stream.", []string{"stream"}, nil),
		streamFirstSeq: prometheus.NewDesc("nereidlink_jetstream_stream_first_seq",
			"First sequence stored in the stream.", []string{"stream"}, nil),
		streamLastSeq: prometheus.NewDesc("nereidlink_jetstream_stream_last_seq",
			"Last sequence stored in the stream.", []string{"stream"}, nil),
		streamConsumerCount: prometheus.NewDesc("nereidlink_jetstream_stream_consumers",
			"Consumers bound to the stream.", []string{"stream"}, nil),

		consumerPending: prometheus.NewDesc("nereidlink_jetstream_consumer_pending",
			"Messages pending acknowledgment for the consumer.",
			[]string{"stream", "consumer"}, nil),
		consumerRedelivered: prometheus.NewDesc("nereidlink_jetstream_consumer_redelivered_total",
			"Redelivered messages for the consumer.",
			[]string{"stream", "consumer"}, nil),
		consumerAckFloor: prometheus.NewDesc("nereidlink_jetstream_consumer_ack_floor_seq",
			"Ack floor stream sequence for the consumer.",
			[]string{"stream", "consumer"}, nil),

		brokerClients: prometheus.NewDesc("nereidlink_mqtt_clients",
			"Currently connected MQTT clients.", nil, nil),

		devicesTotal: prometheus.NewDesc("nereidlink_devices_total",
			"Registered devices, excluding DECOMMISSIONED tombstones.", []string{"tenant"}, nil),
		devicesDecommissioned: prometheus.NewDesc("nereidlink_devices_decommissioned",
			"Decommissioned (tombstoned) devices; never physically deleted.", []string{"tenant"}, nil),
		devicesOnline: prometheus.NewDesc("nereidlink_devices_online",
			"Currently online devices.", []string{"tenant"}, nil),
		devicesOffline: prometheus.NewDesc("nereidlink_devices_offline",
			"Currently offline devices.", []string{"tenant"}, nil),
		alarmsLive: prometheus.NewDesc("nereidlink_alarms_live",
			"Live (ACTIVE and ACK) alarms.", []string{"tenant"}, nil),
		commandsInflight: prometheus.NewDesc("nereidlink_commands_inflight",
			"Non-terminal commands.", []string{"tenant"}, nil),
		ruleChainsActive: prometheus.NewDesc("nereidlink_rule_chains_active",
			"Currently active rule chains.", []string{"tenant"}, nil),

		tenantCustomers: prometheus.NewDesc("nereidlink_tenant_customers",
			"Customer entities in the tenant.", []string{"tenant"}, nil),
		tenantDeviceGroups: prometheus.NewDesc("nereidlink_tenant_device_groups",
			"Device groups in the tenant.", []string{"tenant"}, nil),
		tenantUsers: prometheus.NewDesc("nereidlink_tenant_users",
			"Human accounts in the tenant.", []string{"tenant"}, nil),
		tenantMemberships: prometheus.NewDesc("nereidlink_tenant_memberships",
			"Tenant membership rows (may exceed users).", []string{"tenant"}, nil),
		tenantCredentials: prometheus.NewDesc("nereidlink_tenant_device_credentials",
			"Device credentials via the owning device.", []string{"tenant"}, nil),
		tenantSecrets: prometheus.NewDesc("nereidlink_tenant_secrets",
			"Connector secrets stored for the tenant.", []string{"tenant"}, nil),
		tenantFirmwarePackages: prometheus.NewDesc("nereidlink_firmware_packages",
			"Hosted firmware packages.", []string{"tenant"}, nil),

		// Task gauges carry a "kind" label (batch_command|probe|ota) so one
		// descriptor covers each family with total and active series.
		taskTotal: prometheus.NewDesc("nereidlink_tasks_total",
			"Tasks created, by family.", []string{"tenant", "kind"}, nil),
		taskActive: prometheus.NewDesc("nereidlink_tasks_active",
			"Tasks currently pending or running, by family.",
			[]string{"tenant", "kind"}, nil),

		wsClients: prometheus.NewDesc("nereidlink_ws_clients",
			"Currently connected WebSocket clients.", nil, nil),

		// ADR-0025 capacity watermarks. The label tuple is shared across all
		// planes; irrelevant labels are emitted as empty strings so the
		// descriptor set stays fixed. "plane" is a bounded vocabulary:
		// postgres_pool, jetstream_consumer, presence_persist, notify_queue.
		capCurrent: prometheus.NewDesc("nereidlink_capacity_current",
			"Current usage of a bounded resource plane.",
			[]string{"plane", "stream", "consumer", "queue"}, nil),
		capLimit: prometheus.NewDesc("nereidlink_capacity_limit",
			"Configured ceiling of a bounded resource plane; 0 means unlimited (utilization not reported).",
			[]string{"plane", "stream", "consumer", "queue"}, nil),
		capUtilization: prometheus.NewDesc("nereidlink_capacity_utilization_ratio",
			"current/limit of a bounded resource plane in [0,1].",
			[]string{"plane", "stream", "consumer", "queue"}, nil),
	}
}

// Describe emits every descriptor.
func (c *nereidCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.uptime
	ch <- c.pgMaxConns
	ch <- c.pgActiveConns
	ch <- c.pgPoolTotal
	ch <- c.pgPoolIdle
	ch <- c.pgTxCommit
	ch <- c.pgTxRollback
	ch <- c.pgBufferHitRatio
	ch <- c.pgDeadlocks
	ch <- c.natsInMsgs
	ch <- c.natsOutMsgs
	ch <- c.natsInBytes
	ch <- c.natsOutBytes
	ch <- c.natsReconnects
	ch <- c.streamMessages
	ch <- c.streamBytes
	ch <- c.streamFirstSeq
	ch <- c.streamLastSeq
	ch <- c.streamConsumerCount
	ch <- c.consumerPending
	ch <- c.consumerRedelivered
	ch <- c.consumerAckFloor
	ch <- c.brokerClients
	ch <- c.devicesTotal
	ch <- c.devicesDecommissioned
	ch <- c.devicesOnline
	ch <- c.devicesOffline
	ch <- c.alarmsLive
	ch <- c.commandsInflight
	ch <- c.ruleChainsActive
	ch <- c.tenantCustomers
	ch <- c.tenantDeviceGroups
	ch <- c.tenantUsers
	ch <- c.tenantMemberships
	ch <- c.tenantCredentials
	ch <- c.tenantSecrets
	ch <- c.tenantFirmwarePackages
	ch <- c.taskTotal
	ch <- c.taskActive
	ch <- c.wsClients
	ch <- c.capCurrent
	ch <- c.capLimit
	ch <- c.capUtilization
}

// Collect gathers a fresh snapshot and emits its metrics. Collection
// errors yield no samples for the affected group but do not fail the
// scrape.
func (c *nereidCollector) Collect(ch chan<- prometheus.Metric) {
	now := float64(time.Now().Unix())
	ch <- prometheus.MustNewConstMetric(c.uptime, prometheus.GaugeValue,
		now-float64(c.startedAt))

	ctx, cancel := context.WithTimeout(context.Background(), scrapeTimeout)
	defer cancel()

	system, err := c.source.System(ctx)
	if err != nil {
		collectFailed("system", err)
		return
	}
	c.collectSystem(ch, system)
	c.collectCapacity(ch, system)

	// The WebSocket client count is in-process rather than sourced from the
	// stats snapshot, so it is read through an optional provider.
	if c.liveClients != nil {
		ch <- mustGauge(c.wsClients, float64(c.liveClients()))
	}

	// Bounded in-process queues are also in-process; they are read through an
	// optional provider like the WebSocket client count (ADR-0025).
	if c.queueWatermarks != nil {
		c.collectQueueWatermarks(ch, c.queueWatermarks())
	}

	tenants, err := c.source.Current(ctx)
	if err != nil {
		collectFailed("current", err)
		return
	}
	for _, tenant := range tenants {
		labels := []string{tenant.TenantID}
		ch <- prometheus.MustNewConstMetric(c.devicesTotal, prometheus.GaugeValue,
			float64(tenant.DevicesTotal), labels...)
		ch <- prometheus.MustNewConstMetric(c.devicesDecommissioned, prometheus.GaugeValue,
			float64(tenant.DevicesDecommissioned), labels...)
		ch <- prometheus.MustNewConstMetric(c.devicesOnline, prometheus.GaugeValue,
			float64(tenant.DevicesOnline), labels...)
		ch <- prometheus.MustNewConstMetric(c.devicesOffline, prometheus.GaugeValue,
			float64(tenant.DevicesOffline), labels...)
		ch <- prometheus.MustNewConstMetric(c.alarmsLive, prometheus.GaugeValue,
			float64(tenant.AlarmsLive), labels...)
		ch <- prometheus.MustNewConstMetric(c.commandsInflight, prometheus.GaugeValue,
			float64(tenant.CommandsInflight), labels...)
		ch <- prometheus.MustNewConstMetric(c.ruleChainsActive, prometheus.GaugeValue,
			float64(tenant.RuleChainsActive), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantCustomers, prometheus.GaugeValue,
			float64(tenant.Customers), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantDeviceGroups, prometheus.GaugeValue,
			float64(tenant.DeviceGroups), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantUsers, prometheus.GaugeValue,
			float64(tenant.Users), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantMemberships, prometheus.GaugeValue,
			float64(tenant.Memberships), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantCredentials, prometheus.GaugeValue,
			float64(tenant.Credentials), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantSecrets, prometheus.GaugeValue,
			float64(tenant.Secrets), labels...)
		ch <- prometheus.MustNewConstMetric(c.tenantFirmwarePackages, prometheus.GaugeValue,
			float64(tenant.FirmwarePackages), labels...)

		// Task families share two descriptors via a bounded "kind" label.
		ch <- prometheus.MustNewConstMetric(c.taskTotal, prometheus.GaugeValue,
			float64(tenant.BatchTasks), tenant.TenantID, "batch_command")
		ch <- prometheus.MustNewConstMetric(c.taskActive, prometheus.GaugeValue,
			float64(tenant.BatchTasksActive), tenant.TenantID, "batch_command")
		ch <- prometheus.MustNewConstMetric(c.taskTotal, prometheus.GaugeValue,
			float64(tenant.ProbeTasks), tenant.TenantID, "probe")
		ch <- prometheus.MustNewConstMetric(c.taskActive, prometheus.GaugeValue,
			float64(tenant.ProbeTasksActive), tenant.TenantID, "probe")
		ch <- prometheus.MustNewConstMetric(c.taskTotal, prometheus.GaugeValue,
			float64(tenant.OTATasks), tenant.TenantID, "ota")
		ch <- prometheus.MustNewConstMetric(c.taskActive, prometheus.GaugeValue,
			float64(tenant.OTATasksActive), tenant.TenantID, "ota")
	}
}

// collectSystem emits metrics derived from the system snapshot.
func (c *nereidCollector) collectSystem(ch chan<- prometheus.Metric, system stats.SystemSnapshot) {
	pg := system.Postgres
	ch <- mustGauge(c.pgMaxConns, float64(pg.MaxConnections))
	ch <- mustGauge(c.pgActiveConns, float64(pg.ActiveConnections))
	ch <- mustGauge(c.pgPoolTotal, float64(pg.TotalConnections))
	ch <- mustGauge(c.pgPoolIdle, float64(pg.IdleConnections))
	ch <- prometheus.MustNewConstMetric(c.pgTxCommit, prometheus.CounterValue,
		float64(pg.TxCommit))
	ch <- prometheus.MustNewConstMetric(c.pgTxRollback, prometheus.CounterValue,
		float64(pg.TxRollback))
	ch <- mustGauge(c.pgBufferHitRatio, pg.BlockHitRatio)
	ch <- prometheus.MustNewConstMetric(c.pgDeadlocks, prometheus.CounterValue,
		float64(pg.Deadlocks))

	nats := system.NATS
	ch <- prometheus.MustNewConstMetric(c.natsInMsgs, prometheus.CounterValue,
		float64(nats.InMsgs))
	ch <- prometheus.MustNewConstMetric(c.natsOutMsgs, prometheus.CounterValue,
		float64(nats.OutMsgs))
	ch <- prometheus.MustNewConstMetric(c.natsInBytes, prometheus.CounterValue,
		float64(nats.InBytes))
	ch <- prometheus.MustNewConstMetric(c.natsOutBytes, prometheus.CounterValue,
		float64(nats.OutBytes))
	ch <- prometheus.MustNewConstMetric(c.natsReconnects, prometheus.CounterValue,
		float64(nats.Reconnects))

	for _, stream := range system.Streams {
		labels := []string{stream.Name}
		ch <- prometheus.MustNewConstMetric(c.streamMessages, prometheus.GaugeValue,
			float64(stream.Messages), labels...)
		ch <- prometheus.MustNewConstMetric(c.streamBytes, prometheus.GaugeValue,
			float64(stream.Bytes), labels...)
		ch <- prometheus.MustNewConstMetric(c.streamFirstSeq, prometheus.GaugeValue,
			float64(stream.FirstSeq), labels...)
		ch <- prometheus.MustNewConstMetric(c.streamLastSeq, prometheus.GaugeValue,
			float64(stream.LastSeq), labels...)
		ch <- prometheus.MustNewConstMetric(c.streamConsumerCount, prometheus.GaugeValue,
			float64(stream.ConsumerCount), labels...)

		for _, consumer := range stream.Consumers {
			consumerLabels := []string{stream.Name, consumer.Name}
			ch <- prometheus.MustNewConstMetric(c.consumerPending, prometheus.GaugeValue,
				float64(consumer.Pending), consumerLabels...)
			ch <- prometheus.MustNewConstMetric(c.consumerRedelivered, prometheus.CounterValue,
				float64(consumer.Redelivered), consumerLabels...)
			ch <- prometheus.MustNewConstMetric(c.consumerAckFloor, prometheus.GaugeValue,
				float64(consumer.AckFloorSeq), consumerLabels...)
		}
	}

	ch <- mustGauge(c.brokerClients, float64(system.Broker.OnlineClients))
}

// mustGauge builds a label-less gauge metric.
func mustGauge(desc *prometheus.Desc, value float64) prometheus.Metric {
	return prometheus.MustNewConstMetric(desc, prometheus.GaugeValue, value)
}

// collectCapacity emits current/limit/utilization watermarks derived from the
// infrastructure snapshot (ADR-0025): the application connection pool and the
// JetStream consumer in-flight ceilings. Planes without a configured positive
// limit emit current/limit only; utilization is suppressed so an unlimited
// plane never reports a misleading ratio.
func (c *nereidCollector) collectCapacity(ch chan<- prometheus.Metric, system stats.SystemSnapshot) {
	const planePool = "postgres_pool"
	pgCurrent := float64(system.Postgres.TotalConnections)
	pgLimit := float64(system.Postgres.PoolMaxConns)
	c.emitWatermark(ch, planePool, "", "", "", pgCurrent, pgLimit)

	const planeConsumer = "jetstream_consumer"
	for _, stream := range system.Streams {
		for _, consumer := range stream.Consumers {
			c.emitWatermark(ch, planeConsumer, stream.Name, consumer.Name, "",
				float64(consumer.Pending), float64(consumer.MaxAckPending))
		}
	}
}

// collectQueueWatermarks emits current/limit/utilization for bounded in-process
// queues reported by providers (presence persistence, notification pools).
func (c *nereidCollector) collectQueueWatermarks(ch chan<- prometheus.Metric, queues []stats.QueueWatermark) {
	for _, q := range queues {
		c.emitWatermark(ch, q.Plane, "", "", q.Sub,
			float64(q.Current), float64(q.Limit))
	}
}

// emitWatermark emits the three capacity samples for one labelset. The
// current/limit pair is always emitted; utilization only for a positive limit.
func (c *nereidCollector) emitWatermark(ch chan<- prometheus.Metric, plane, stream, consumer, queue string, current, limit float64) {
	labels := []string{plane, stream, consumer, queue}
	ch <- prometheus.MustNewConstMetric(c.capCurrent, prometheus.GaugeValue, current, labels...)
	ch <- prometheus.MustNewConstMetric(c.capLimit, prometheus.GaugeValue, limit, labels...)
	if limit > 0 {
		ratio := current / limit
		if ratio > 1 {
			ratio = 1
		}
		ch <- prometheus.MustNewConstMetric(c.capUtilization, prometheus.GaugeValue, ratio, labels...)
	}
}
