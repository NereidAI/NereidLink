// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// system.go collects the point-in-time system snapshot: Go runtime,
// PostgreSQL, NATS core stats, JetStream stream/consumer lag and the
// embedded MQTT broker client count.

package stats

import (
	"context"
	"fmt"
	"math"
	"runtime"
	"time"

	"github.com/nereidai/nereidlink/internal/streams"
	"github.com/nereidai/nereidlink/sdk/partition"
)

// toInt64 converts a uint64 counter to int64, saturating at the int64
// maximum so an implausibly large counter cannot wrap negative.
func toInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

// SystemSnapshot is one on-demand infrastructure snapshot.
type SystemSnapshot struct {
	// GeneratedAt is the collection time as RFC3339 UTC.
	GeneratedAt string `json:"generated_at"`

	// UptimeSec is the elapsed seconds since process start.
	UptimeSec int64 `json:"uptime_sec"`

	// Go carries Go runtime metrics.
	Go GoRuntime `json:"go"`

	// Postgres carries database metrics.
	Postgres PostgresStats `json:"postgres"`

	// NATS carries core connection counters.
	NATS NATSStats `json:"nats"`

	// Streams carries per-shard JetStream state.
	Streams []StreamStats `json:"streams"`

	// Broker carries the embedded MQTT broker metrics.
	Broker BrokerStats `json:"broker"`
}

// GoRuntime summarizes the Go process.
type GoRuntime struct {
	// Goroutines is the live goroutine count.
	Goroutines int `json:"goroutines"`

	// HeapAllocBytes is the currently allocated heap.
	HeapAllocBytes uint64 `json:"heap_alloc_bytes"`

	// SysBytes is memory obtained from the OS.
	SysBytes uint64 `json:"sys_bytes"`

	// HeapObjects is the live heap object count.
	HeapObjects uint64 `json:"heap_objects"`

	// NextGCBytes is the heap target triggering the next GC.
	NextGCBytes uint64 `json:"next_gc_bytes"`

	// GCCycles is the number of completed GC cycles.
	GCCycles uint32 `json:"gc_cycles"`

	// LastGCPauseMS is the most recent GC stop-the-world pause.
	LastGCPauseMS float64 `json:"last_gc_pause_ms"`
}

// PostgresStats summarizes the database and its connection pool.
type PostgresStats struct {
	// MaxConnections is the server-side connection limit.
	MaxConnections int `json:"max_connections"`

	// ActiveConnections is pg_stat_database.numbackends.
	ActiveConnections int `json:"active_connections"`

	// TotalConnections is the pool's total connection count.
	TotalConnections int32 `json:"total_connections"`

	// PoolMaxConns is the application pool's configured connection ceiling
	// (pgxpool MaxConns); zero means the library default applies.
	PoolMaxConns int32 `json:"pool_max_conns"`

	// IdleConnections is the pool's idle count.
	IdleConnections int32 `json:"idle_connections"`

	// AcquireTotal is the cumulative successful pool acquires.
	AcquireTotal int64 `json:"acquire_total"`

	// TxCommit is the committed transaction total.
	TxCommit int64 `json:"tx_commit"`

	// TxRollback is the rolled back transaction total.
	TxRollback int64 `json:"tx_rollback"`

	// BlockHitRatio is the shared buffer read hit ratio in percent.
	BlockHitRatio float64 `json:"block_hit_ratio"`

	// Deadlocks is the recorded deadlock total.
	Deadlocks int64 `json:"deadlocks"`
}

// NATSStats summarizes the core NATS connection.
type NATSStats struct {
	// InMsgs is the inbound message total.
	InMsgs int64 `json:"in_msgs"`

	// OutMsgs is the outbound message total.
	OutMsgs int64 `json:"out_msgs"`

	// InBytes is the inbound byte total.
	InBytes int64 `json:"in_bytes"`

	// OutBytes is the outbound byte total.
	OutBytes int64 `json:"out_bytes"`

	// Reconnects is the client reconnect total.
	Reconnects int64 `json:"reconnects"`
}

// StreamStats describes one telemetry JetStream stream.
type StreamStats struct {
	// Name is the stream name.
	Name string `json:"name"`

	// Messages is the stored message count.
	Messages uint64 `json:"messages"`

	// Bytes is the stored byte count.
	Bytes uint64 `json:"bytes"`

	// FirstSeq is the oldest stored sequence.
	FirstSeq uint64 `json:"first_seq"`

	// LastSeq is the newest stored sequence.
	LastSeq uint64 `json:"last_seq"`

	// ConsumerCount is the number of consumers.
	ConsumerCount int `json:"consumer_count"`

	// Consumers detail the consumer backlog.
	Consumers []ConsumerStats `json:"consumers"`
}

// ConsumerStats describes one stream consumer.
type ConsumerStats struct {
	// Name is the durable consumer name.
	Name string `json:"name"`

	// Pending is the unacknowledged message count.
	Pending uint64 `json:"pending"`

	// Redelivered is the redelivery count.
	Redelivered int64 `json:"redelivered"`

	// AckFloorSeq is the last acknowledged sequence.
	AckFloorSeq uint64 `json:"ack_floor_seq"`

	// MaxAckPending is the consumer's configured in-flight ceiling; zero
	// means the server default applies, in which case utilization is not
	// reported.
	MaxAckPending int `json:"max_ack_pending"`
}

// QueueWatermark is a bounded in-process queue's occupancy at scrape time.
// Plane values are a fixed, low-cardinality set owned by this package; device,
// tenant and IP identifiers never appear here (ADR-0025 cardinality redline).
type QueueWatermark struct {
	// Plane is the fixed queue family name, e.g. "notify_queue".
	Plane string `json:"plane"`

	// Sub is the optional fixed secondary label within a family, e.g.
	// "critical"/"normal"; empty for families without a sub-dimension.
	Sub string `json:"sub"`

	// Current is len(queue).
	Current int `json:"current"`

	// Limit is cap(queue); zero/negative means unbounded and suppresses the
	// utilization sample.
	Limit int `json:"limit"`
}

// BrokerStats describes the embedded MQTT broker.
type BrokerStats struct {
	// Type identifies the broker implementation.
	Type string `json:"type"`

	// OnlineClients is the connected MQTT device count.
	OnlineClients int `json:"online_clients"`

	// OnlineHTTPClients is the open HTTP device session count.
	OnlineHTTPClients int `json:"online_http_clients"`
}

// System collects the infrastructure snapshot.
func (s *Service) System(ctx context.Context) (SystemSnapshot, error) {
	snapshot := SystemSnapshot{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		UptimeSec:   int64(time.Since(s.started).Seconds()),
		Broker:      BrokerStats{Type: "embedded-mochi-mqtt"},
	}

	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	snapshot.Go = GoRuntime{
		Goroutines:     runtime.NumGoroutine(),
		HeapAllocBytes: mem.HeapAlloc,
		SysBytes:       mem.Sys,
		HeapObjects:    mem.HeapObjects,
		NextGCBytes:    mem.NextGC,
		GCCycles:       mem.NumGC,
		LastGCPauseMS:  float64(mem.PauseNs[(mem.NumGC+255)%256]) / 1e6,
	}

	pg, err := s.collectPostgres(ctx)
	if err != nil {
		return SystemSnapshot{}, err
	}
	snapshot.Postgres = pg

	stats := s.nc.Stats()
	snapshot.NATS = NATSStats{
		InMsgs:     toInt64(stats.InMsgs),
		OutMsgs:    toInt64(stats.OutMsgs),
		InBytes:    toInt64(stats.InBytes),
		OutBytes:   toInt64(stats.OutBytes),
		Reconnects: toInt64(stats.Reconnects),
	}

	for shard := 0; shard < partition.StreamShards; shard++ {
		stream, err := s.collectStream(ctx, uint32(shard))
		if err != nil {
			return SystemSnapshot{}, err
		}
		snapshot.Streams = append(snapshot.Streams, stream)
	}

	if s.broker != nil {
		snapshot.Broker.OnlineClients = s.broker.ClientCount()
	}
	if s.http != nil {
		snapshot.Broker.OnlineHTTPClients = s.http.SessionCount()
	}
	return snapshot, nil
}

// collectPostgres queries server statistics and reads the pool counters.
func (s *Service) collectPostgres(ctx context.Context) (PostgresStats, error) {
	var out PostgresStats
	if err := s.pool.QueryRow(ctx,
		`SELECT setting::int FROM pg_settings WHERE name = 'max_connections'`,
	).Scan(&out.MaxConnections); err != nil {
		return PostgresStats{}, fmt.Errorf("read max_connections: %w", err)
	}

	var reads, hits int64
	if err := s.pool.QueryRow(ctx,
		`SELECT numbackends, xact_commit, xact_rollback,
		        blks_read, blks_hit, deadlocks
		 FROM pg_stat_database WHERE datname = current_database()`,
	).Scan(&out.ActiveConnections, &out.TxCommit, &out.TxRollback,
		&reads, &hits, &out.Deadlocks); err != nil {
		return PostgresStats{}, fmt.Errorf("read pg stat: %w", err)
	}
	if reads+hits > 0 {
		out.BlockHitRatio = float64(hits) / float64(reads+hits) * 100
	}

	poolStat := s.pool.Stat()
	out.TotalConnections = int32(poolStat.TotalConns())
	out.IdleConnections = int32(poolStat.IdleConns())
	out.AcquireTotal = poolStat.AcquireCount()
	out.PoolMaxConns = int32(s.pool.Config().MaxConns)
	return out, nil
}

// collectStream reads stream state and enumerates consumer backlog.
func (s *Service) collectStream(ctx context.Context, shard uint32) (StreamStats, error) {
	name := streams.TelemetryStreamName(shard)
	stream, err := s.js.Stream(ctx, name)
	if err != nil {
		return StreamStats{}, fmt.Errorf("open stream %s: %w", name, err)
	}
	info, err := stream.Info(ctx)
	if err != nil {
		return StreamStats{}, fmt.Errorf("read stream %s: %w", name, err)
	}

	out := StreamStats{
		Name:          name,
		Messages:      info.State.Msgs,
		Bytes:         info.State.Bytes,
		FirstSeq:      info.State.FirstSeq,
		LastSeq:       info.State.LastSeq,
		ConsumerCount: info.State.Consumers,
	}

	consumerIter := stream.ListConsumers(ctx)
	for consumer := range consumerIter.Info() {
		out.Consumers = append(out.Consumers, ConsumerStats{
			Name:          consumer.Name,
			Pending:       consumer.NumPending,
			Redelivered:   int64(consumer.NumRedelivered),
			AckFloorSeq:   consumer.AckFloor.Stream,
			MaxAckPending: consumer.Config.MaxAckPending,
		})
	}
	if out.Consumers == nil {
		out.Consumers = []ConsumerStats{}
	}
	return out, nil
}
