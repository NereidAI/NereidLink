// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package telemetry implements the telemetry writer: it consumes canonical
// Envelope messages from the partitioned JetStream streams and batches the
// decoded measurement points into TimescaleDB.
//
// The writer is deliberately state-free with respect to connections: every
// shard consumer uses explicit acks and any replica can claim a stream
// consumer without shared memory state.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go/jetstream"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/license"
	"github.com/nereidai/nereidlink/internal/storagehook"
	"github.com/nereidai/nereidlink/internal/streams"
	"github.com/nereidai/nereidlink/sdk/partition"
)

// Tuning is the writer configuration. Values are supplied by the runtime
// configuration rather than compiled into service code.
type Tuning struct {
	// FlushInterval bounds how long a row waits before a batch flush.
	FlushInterval time.Duration

	// FlushSize is the row count at which a batch flushes immediately.
	FlushSize int

	// MaxAckPending bounds in-flight unacked messages per shard consumer.
	MaxAckPending int

	// DedupWindow bounds the envelope identifier redelivery cache.
	DedupWindow int
}

// row is one pending database insert.
type row struct {
	time       time.Time
	tenantID   string
	deviceID   string
	key        string
	value      float64
	quality    string
	raw        float64
	unit       string
	descriptor int
	vString    string
	vBool      *bool
}

// Server consumes every telemetry shard and writes batched rows.
type Server struct {
	js   jetstream.JetStream
	pool *pgxpool.Pool
	tune Tuning

	// batches holds one pending batch per shard consumer; M0 runs a single
	// writer, so a shared slice guarded by mu is sufficient.
	mu     sync.Mutex
	buffer []row

	// dedupMu guards the redelivery deduplication window.
	dedupMu sync.Mutex

	// seen is the membership set of recent envelope identifiers.
	seen map[string]struct{}

	// seenOrder is the FIFO ring of identifiers evicted from seen.
	seenOrder []string

	// hooks is the optional pre-storage hook pipeline.
	hooks *storagehook.Pipeline

	// dlq writes points rejected under the dlq failure policy.
	dlq storagehook.DLQWriter

	// gate blocks persistence for immediately suspended tenants.
	gate TenantWriteGate

	// rollupOnce guards the one-time coverage probe for the continuous
	// aggregates created by migration 0024, and rollupReady caches its
	// verdict. Series reads the hypertable until coverage is proven, so a
	// rollup that exists but was never backfilled cannot serve empty
	// historical charts. See series.go for the rationale.
	rollupOnce  sync.Once
	rollupReady bool

	// decoder transparently decompresses bus frames; nil is treated as
	// passthrough so the writer works with compression disabled.
	decoder FrameDecoder
}

// FrameDecoder decompresses a bus message. internal/buscodec.Codec
// satisfies it.
type FrameDecoder interface {
	// Decode returns the original envelope bytes.
	Decode(raw []byte) ([]byte, error)
}

// SetFrameDecoder wires bus decompression.
func (s *Server) SetFrameDecoder(decoder FrameDecoder) {
	s.decoder = decoder
}

// TenantWriteGate reports whether an already accepted tenant may write.
type TenantWriteGate interface {
	// AllowTenantWrite returns license.ErrSuspended when blocked.
	AllowTenantWrite(ctx context.Context, tenantID string) error
}

// SetTenantWriteGate wires the immediate suspension gate.
func (s *Server) SetTenantWriteGate(gate TenantWriteGate) {
	s.gate = gate
}

// NewServer constructs a telemetry writer.
func NewServer(js jetstream.JetStream, pool *pgxpool.Pool, tune Tuning, hooks *storagehook.Pipeline, dlq storagehook.DLQWriter) *Server {
	return &Server{
		js:        js,
		pool:      pool,
		tune:      tune,
		seen:      map[string]struct{}{},
		seenOrder: make([]string, 0, tune.DedupWindow),
		hooks:     hooks,
		dlq:       dlq,
	}
}

// duplicate reports whether an envelope identifier was already processed,
// recording it when new. The fixed FIFO bounds memory and covers the
// QoS1/consumer-redelivery window: a redelivered message is skipped and
// acked instead of producing a duplicate row.
func (s *Server) duplicate(id string) bool {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()

	if _, ok := s.seen[id]; ok {
		return true
	}

	s.seen[id] = struct{}{}
	s.seenOrder = append(s.seenOrder, id)
	if len(s.seenOrder) > s.tune.DedupWindow {
		evicted := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seen, evicted)
	}
	return false
}

// Run starts all shard consumers and the periodic flusher, blocking until
// ctx is canceled.
func (s *Server) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.tune.FlushInterval)
	defer ticker.Stop()

	consumeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for shard := 0; shard < partition.StreamShards; shard++ {
		if err := s.consume(consumeCtx, uint32(shard)); err != nil {
			return fmt.Errorf("consume shard %d: %w", shard, err)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := s.flush(context.Background()); err != nil {
				slog.Error("telemetry batch flush failed", "error", err)
			}
		}
	}
}

// consume registers a durable explicit-ack consumer on one shard stream.
func (s *Server) consume(ctx context.Context, shard uint32) error {
	stream := streams.TelemetryStreamName(shard)
	cons, err := s.js.CreateOrUpdateConsumer(ctx, stream, jetstream.ConsumerConfig{
		Durable:       fmt.Sprintf("telemetry-writer-s%d", shard),
		AckPolicy:     jetstream.AckExplicitPolicy,
		MaxAckPending: s.tune.MaxAckPending,
	})
	if err != nil {
		return err
	}

	if _, err := cons.Consume(func(msg jetstream.Msg) {
		s.onMessage(ctx, msg)
	}); err != nil {
		return err
	}
	return nil
}

// onMessage decodes one JetStream message, appends its point and acks only
// after the row is buffered for the next flush.
func (s *Server) onMessage(ctx context.Context, msg jetstream.Msg) {
	data := msg.Data()
	if s.decoder != nil {
		decoded, err := s.decoder.Decode(data)
		if err != nil {
			slog.Error("envelope decompression failed", "error", err)
			_ = msg.Nak()
			return
		}
		data = decoded
	}

	var env v1.Envelope
	if err := proto.Unmarshal(data, &env); err != nil {
		slog.Error("envelope decode failed", "error", err)
		_ = msg.Nak()
		return
	}

	if s.duplicate(env.Id) {
		// Already processed; ack the redelivery without a duplicate row.
		_ = msg.Ack()
		return
	}

	if s.gate != nil {
		if err := s.gate.AllowTenantWrite(ctx, env.TenantId); err != nil {
			if errors.Is(err, license.ErrSuspended) {
				slog.Info("telemetry point rejected for suspended tenant",
					"tenant", env.TenantId, "device", env.DeviceId)
				_ = msg.Ack()
				return
			}
			slog.Error("telemetry tenant gate failed", "error", err)
			_ = msg.Nak()
			return
		}
	}

	var point v1.TelemetryPoint
	if err := proto.Unmarshal(env.Payload, &point); err != nil {
		slog.Error("telemetry point decode failed", "error", err)
		_ = msg.Nak()
		return
	}

	hookPoint := storagehook.Point{
		TenantID:       env.TenantId,
		DeviceID:       env.DeviceId,
		MeasurementKey: point.MeasurementKey,
		VDouble:        point.VDouble,
		Quality:        point.Quality,
		Unit:           point.Unit,
		RawDouble:      point.RawDouble,
	}

	if s.hooks != nil {
		decision := s.hooks.Apply(ctx, &hookPoint)
		switch {
		case decision.Drop:
			slog.Info("telemetry point dropped by storage hook",
				"device", env.DeviceId, "hook", decision.HookName, "reason", decision.Reason)
			_ = msg.Ack()
			return
		case decision.ToDLQ:
			if s.dlq != nil {
				if err := s.dlq.WriteDLQ(ctx, hookPoint, decision.HookName, decision.Reason); err != nil {
					slog.Error("telemetry dlq write failed", "error", err)
					_ = msg.Nak()
					return
				}
			}
			_ = msg.Ack()
			return
		}
	}

	s.mu.Lock()
	row := row{
		time:       time.UnixMilli(env.EventTime),
		tenantID:   env.TenantId,
		deviceID:   env.DeviceId,
		key:        hookPoint.MeasurementKey,
		value:      hookPoint.VDouble,
		quality:    hookPoint.Quality,
		raw:        hookPoint.RawDouble,
		unit:       hookPoint.Unit,
		descriptor: int(point.DescriptorVersion),
		vString:    point.VString,
	}
	// The type tag disambiguates a boolean false from the proto zero
	// value; only boolean points populate the v_bool column.
	if point.ValueType == v1.ValueType_VALUE_TYPE_BOOLEAN {
		vBool := point.VBool
		row.vBool = &vBool
	}
	s.buffer = append(s.buffer, row)
	shouldFlush := len(s.buffer) >= s.tune.FlushSize
	s.mu.Unlock()

	if shouldFlush {
		if err := s.flush(ctx); err != nil {
			slog.Error("size-triggered flush failed", "error", err)
			_ = msg.Nak()
			return
		}
	}
	_ = msg.Ack()
}

// flush writes the buffered rows in a single multi-row insert and clears
// the buffer. A failed insert retains the buffered rows for retry.
func (s *Server) flush(ctx context.Context) error {
	s.mu.Lock()
	if len(s.buffer) == 0 {
		s.mu.Unlock()
		return nil
	}
	batch := s.buffer
	s.buffer = nil
	s.mu.Unlock()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		s.requeue(batch)
		return err
	}

	active, err := s.activeDevices(ctx, batch)
	if err != nil {
		s.requeue(batch)
		return err
	}

	rows := make([][]any, 0, len(batch))
	dropped := 0
	for _, r := range batch {
		if !active[r.deviceID] {
			// Device was decommissioned or otherwise deactivated after
			// the envelope was accepted; drop its buffered points rather
			// than writing against a retired identity.
			dropped++
			continue
		}
		var descriptor any
		if r.descriptor > 0 {
			descriptor = r.descriptor
		}
		var vString any
		if r.vString != "" {
			vString = r.vString
		}
		rows = append(rows, []any{
			r.time, r.tenantID, r.deviceID, r.key, r.value, r.quality,
			r.raw, r.unit, descriptor, vString, r.vBool,
		})
	}
	if dropped > 0 {
		slog.Info("dropped telemetry rows for inactive devices",
			"rows", dropped)
	}
	if len(rows) == 0 {
		return nil
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"ts_telemetry"},
		[]string{
			"time", "tenant_id", "device_id", "measurement_key", "v_double",
			"quality", "raw_value", "unit", "descriptor_version", "v_string",
			"v_bool",
		},
		pgx.CopyFromRows(rows)); err != nil {
		_ = tx.Rollback(ctx)
		s.requeue(batch)
		return err
	}

	if err := touchLastTelemetry(ctx, tx, rows); err != nil {
		_ = tx.Rollback(ctx)
		s.requeue(batch)
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		s.requeue(batch)
		return err
	}
	return nil
}

// touchLastTelemetry advances device.last_telemetry_at to the newest stored
// point of each device in the batch, in the same transaction as the insert.
// The silence sweep (ADR-0014) reads this watermark instead of scanning the
// ts_telemetry hypertable. Only forward movement is applied so late or
// redelivered points cannot roll the watermark backwards.
func touchLastTelemetry(ctx context.Context, tx pgx.Tx, rows [][]any) error {
	latest := map[string]time.Time{}
	for _, r := range rows {
		ts, _ := r[0].(time.Time)
		deviceID, _ := r[2].(string)
		if deviceID == "" {
			continue
		}
		if current, ok := latest[deviceID]; !ok || ts.After(current) {
			latest[deviceID] = ts
		}
	}
	if len(latest) == 0 {
		return nil
	}
	ids := make([]string, 0, len(latest))
	times := make([]time.Time, 0, len(latest))
	for id, ts := range latest {
		ids = append(ids, id)
		times = append(times, ts)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE device d
		    SET last_telemetry_at = x.mt
		   FROM unnest($1::uuid[], $2::timestamptz[]) AS x(id, mt)
		   WHERE d.id = x.id
		     AND (d.last_telemetry_at IS NULL OR d.last_telemetry_at < x.mt)`,
		ids, times); err != nil {
		return fmt.Errorf("update last telemetry watermark: %w", err)
	}
	return nil
}

// activeDevices returns the set of device ids currently in ACTIVE state
// among the buffered rows. A single batched query avoids per-row checks.
func (s *Server) activeDevices(ctx context.Context, batch []row) (map[string]bool, error) {
	ids := make([]string, 0, len(batch))
	seen := map[string]bool{}
	for _, r := range batch {
		if !seen[r.deviceID] {
			seen[r.deviceID] = true
			ids = append(ids, r.deviceID)
		}
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id::text FROM device
		 WHERE id = ANY($1::uuid[]) AND state = 'ACTIVE'`,
		ids)
	if err != nil {
		return nil, fmt.Errorf("resolve active devices: %w", err)
	}
	defer rows.Close()

	active := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		active[id] = true
	}
	return active, rows.Err()
}

// requeue returns rows to the front of the buffer after a transient write
// failure so they are retried on the next flush.
func (s *Server) requeue(rows []row) {
	s.mu.Lock()
	s.buffer = append(rows, s.buffer...)
	s.mu.Unlock()
}
