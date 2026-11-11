// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"context"
	"testing"
	"time"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// newTestPipeline builds a pipeline with script support and no HTTP work.
func newTestPipeline(hooks []Hook) *Pipeline {
	p := &Pipeline{
		scripts:  scriptruntime.New(time.Second),
		cache:    map[string]cachedHooks{},
		breakers: map[string]*breaker{},
	}
	p.cache["tenant-1"] = cachedHooks{hooks: hooks, checked: time.Now()}
	return p
}

func TestExpressionOverlay(t *testing.T) {
	hooks := []Hook{{
		TenantID:      "tenant-1",
		Name:          "mark",
		Type:          TypeExpression,
		FailurePolicy: PolicyPassthrough,
		Script:        "msg.quality = 'UNCERTAIN'; return {quality: 'UNCERTAIN', v_double: msg.v_double * 2};",
	}}
	pipeline := newTestPipeline(hooks)
	point := &Point{TenantID: "tenant-1", MeasurementKey: "temperature", VDouble: 10, Quality: "GOOD"}

	if decision := pipeline.Apply(context.Background(), point); decision.Drop {
		t.Fatalf("point unexpectedly dropped: %+v", decision)
	}
	if point.Quality != "UNCERTAIN" || point.VDouble != 20 {
		t.Fatalf("overlay not applied: %+v", point)
	}
}

func TestExpressionDrop(t *testing.T) {
	hooks := []Hook{{
		TenantID: "tenant-1",
		Name:     "dropper",
		Type:     TypeExpression,
		Script:   "if (msg.v_double < 0) { return {drop: true}; } return null;",
	}}
	pipeline := newTestPipeline(hooks)
	point := &Point{TenantID: "tenant-1", VDouble: -1}

	if decision := pipeline.Apply(context.Background(), point); !decision.Drop {
		t.Fatalf("expected drop decision, got %+v", decision)
	}
}

func TestKeyFilter(t *testing.T) {
	hooks := []Hook{{
		TenantID: "tenant-1",
		Name:     "hum-only",
		Type:     TypeExpression,
		Keys:     []string{"humidity"},
		Script:   "return {quality: 'BAD'};",
	}}
	pipeline := newTestPipeline(hooks)
	point := &Point{TenantID: "tenant-1", MeasurementKey: "temperature", Quality: "GOOD"}

	pipeline.Apply(context.Background(), point)
	if point.Quality != "GOOD" {
		t.Fatal("hook applied to a non-matching measurement key")
	}
}

func TestBreakerOpens(t *testing.T) {
	b := &breaker{}
	now := time.Now()
	for i := 0; i < breakerFailureThreshold; i++ {
		b.recordFailure(now)
	}
	if b.allow(now.Add(time.Second)) {
		t.Fatal("breaker should reject calls while open")
	}
	if !b.allow(now.Add(breakerCooldown + time.Second)) {
		t.Fatal("breaker should allow a trial after cooldown")
	}
}
