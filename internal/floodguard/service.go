// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package floodguard turns per-device ingest rate-limit rejections into a
// device alarm lifecycle (ADR-0014 bombardment defense).
//
// The ingest limiter runs on the connection hot path and must never block on
// the database, so this sink hands each transition to a bounded channel and
// a single worker raises/clears the alarm. A flood opens one
// DEVICE_INGEST_FLOOD alarm; staying calm for the limiter's recovery window
// auto-clears it. If the queue is full the transition is dropped (and
// counted) rather than applying back-pressure to ingestion.
package floodguard

import (
	"context"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nereidai/nereidlink/internal/alarm"
	"github.com/nereidai/nereidlink/internal/ingestlimit"
)

// AlarmType classifies a device ingest flood alarm.
const AlarmType = "DEVICE_INGEST_FLOOD"

// dedupePrefix names the open alarm per device (per-tenant dedupe index).
const dedupePrefix = "ingest-flood:"

// defaultQueueSize bounds events waiting for the alarm worker.
const defaultQueueSize = 1024

// AlarmSink is the alarm surface the worker drives. *alarm.Service satisfies
// it.
type AlarmSink interface {
	CreateAlarm(ctx context.Context, in alarm.CreateInput) (alarm.Outcome, error)
	ClearAlarm(ctx context.Context, tenantID, deviceID, dedupeKey string) (alarm.Outcome, error)
}

// Service consumes flood events and maintains the device alarm.
type Service struct {
	alarms AlarmSink
	queue  chan ingestlimit.FloodEvent

	// dropped counts events discarded because the queue was full.
	dropped atomic.Int64
}

// New constructs a flood guard with the default bounded queue.
func New(alarms AlarmSink) *Service {
	return &Service{
		alarms: alarms,
		queue:  make(chan ingestlimit.FloodEvent, defaultQueueSize),
	}
}

// FloodStarted enqueues a flood-open transition without blocking the hot
// path; it drops (and counts) the event when the queue is saturated.
func (s *Service) FloodStarted(ev ingestlimit.FloodEvent) {
	s.enqueue(ev)
}

// FloodRecovered enqueues a flood-clear transition.
func (s *Service) FloodRecovered(ev ingestlimit.FloodEvent) {
	s.enqueue(ev)
}

func (s *Service) enqueue(ev ingestlimit.FloodEvent) {
	select {
	case s.queue <- ev:
	default:
		if n := s.dropped.Add(1); n == 1 || n%100 == 0 {
			slog.Warn("flood guard event queue full; transition dropped",
				"dropped", n, "device", ev.Key)
		}
	}
}

// Run drains events until ctx is canceled.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-s.queue:
			s.handle(ctx, ev)
		}
	}
}

func (s *Service) handle(ctx context.Context, ev ingestlimit.FloodEvent) {
	tenantID, deviceID, ok := splitKey(ev.Key)
	if !ok {
		slog.Warn("flood event with malformed key ignored", "key", ev.Key)
		return
	}
	dedupeKey := dedupePrefix + deviceID

	if ev.Rejections > 0 {
		_, err := s.alarms.CreateAlarm(ctx, alarm.CreateInput{
			TenantID:  tenantID,
			DeviceID:  deviceID,
			AlarmType: AlarmType,
			Severity:  alarm.SeverityWarning,
			DedupeKey: dedupeKey,
			Details: map[string]any{
				"rejected_in_window": ev.Rejections,
				"detected_at":        ev.At.UTC().Format(time.RFC3339),
			},
		})
		if err != nil {
			slog.Warn("raise ingest flood alarm failed",
				"device", deviceID, "error", err)
		}
		return
	}

	if _, err := s.alarms.ClearAlarm(ctx, tenantID, deviceID, dedupeKey); err != nil {
		slog.Warn("clear ingest flood alarm failed",
			"device", deviceID, "error", err)
	}
}

// splitKey splits a "tenant.device" limiter key. Device and tenant ids are
// UUIDs (no dots); SplitN guards the first separator.
func splitKey(key string) (tenant, device string, ok bool) {
	tenant, device, found := strings.Cut(key, ".")
	if !found || tenant == "" || device == "" {
		return "", "", false
	}
	return tenant, device, true
}
