// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// cacheTTL bounds how stale hook configuration may become. Hook changes
// become effective within this interval without restarting the writer.
const cacheTTL = 30 * time.Second

// cachedHooks is one tenant's time-stamped hook projection.
type cachedHooks struct {
	hooks   []Hook
	checked time.Time
}

// Pipeline runs the ordered enabled hooks of a tenant against one point.
type Pipeline struct {
	store   *Store
	scripts *scriptruntime.Runtime
	wasm    *WasmRuntime
	client  *http.Client

	mu       sync.Mutex
	cache    map[string]cachedHooks
	breakers map[string]*breaker
}

// NewPipeline constructs a hook pipeline sharing one HTTP client.
func NewPipeline(store *Store, scripts *scriptruntime.Runtime) *Pipeline {
	return &Pipeline{
		store:    store,
		scripts:  scripts,
		client:   &http.Client{},
		cache:    map[string]cachedHooks{},
		breakers: map[string]*breaker{},
	}
}

// SetWasm enables wazero-backed wasm hooks. It is optional: a deployment that
// never constructs the runtime simply fails wasm hooks by policy rather than
// running them.
func (p *Pipeline) SetWasm(wasm *WasmRuntime) {
	p.wasm = wasm
}

// breakerFor returns the circuit breaker of one hook.
func (p *Pipeline) breakerFor(hookID string) *breaker {
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.breakers[hookID]
	if !ok {
		b = &breaker{}
		p.breakers[hookID] = b
	}
	return b
}

// hooksFor returns hooks from cache or reloads them from the store.
func (p *Pipeline) hooksFor(ctx context.Context, tenantID string) ([]Hook, error) {
	p.mu.Lock()
	cached, ok := p.cache[tenantID]
	p.mu.Unlock()
	if ok && time.Since(cached.checked) < cacheTTL {
		return cached.hooks, nil
	}

	hooks, err := p.store.EnabledHooks(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.cache[tenantID] = cachedHooks{hooks: hooks, checked: time.Now()}
	p.mu.Unlock()
	return hooks, nil
}

// Apply executes the matching hooks of a tenant in order and returns the
// final decision. The point is mutated in place by overlays.
//
// An explicit drop returned by a hook always aborts. An execution failure
// is resolved by the hook failure policy: passthrough continues, abort
// drops the point, and dlq routes the point to telemetry_dlq; the caller
// remains responsible for the DLQ insert so this function stays free of
// persistence side effects.
func (p *Pipeline) Apply(ctx context.Context, point *Point) Decision {
	hooks, err := p.hooksFor(ctx, point.TenantID)
	if err != nil {
		// Configuration resolution failure must not silently drop data;
		// pass the point through and surface the error in logs upstream.
		return Decision{}
	}

	for _, hook := range hooks {
		if !matchesKey(hook, point.MeasurementKey) {
			continue
		}

		var overlay map[string]any
		var drop bool
		var execErr error
		switch hook.Type {
		case TypeExpression:
			overlay, drop, execErr = executeExpression(ctx, p.scripts, hook, *point)
		case TypeWebhook:
			overlay, execErr = executeWebhook(ctx, p.client, p.breakerFor(hook.ID), hook, *point)
		case TypeWasm:
			if p.wasm == nil {
				execErr = fmt.Errorf("%w: wasm runtime not enabled", ErrWasmHook)
			} else {
				overlay, drop, execErr = p.wasm.Execute(ctx, hook, *point)
			}
		}

		if execErr != nil {
			return failureDecision(hook, execErr)
		}
		applyOverlay(point, overlay)
		if drop {
			return Decision{Drop: true, HookName: hook.Name, Reason: "hook requested drop"}
		}
	}
	return Decision{}
}

// matchesKey reports whether a hook applies to the given measurement.
func matchesKey(hook Hook, key string) bool {
	if len(hook.Keys) == 0 {
		return true
	}
	for _, allowed := range hook.Keys {
		if allowed == key {
			return true
		}
	}
	return false
}

// failureDecision maps an execution error to the configured policy.
func failureDecision(hook Hook, err error) Decision {
	reason := err.Error()
	switch hook.FailurePolicy {
	case PolicyAbort:
		return Decision{Drop: true, HookName: hook.Name, Reason: reason}
	case PolicyDLQ:
		return Decision{ToDLQ: true, HookName: hook.Name, Reason: reason}
	default:
		return Decision{}
	}
}

// overlayNumber reads a numeric overlay field. goja exports integer-valued
// JavaScript numbers as int64 while JSON webhooks always use float64, so
// both kinds are accepted.
func overlayNumber(overlay map[string]any, key string) (float64, bool) {
	switch value := overlay[key].(type) {
	case float64:
		return value, true
	case int64:
		return float64(value), true
	default:
		return 0, false
	}
}

// applyOverlay merges supported overlay fields into the point. Unknown
// overlay fields are ignored so callback responses stay forward compatible.
func applyOverlay(point *Point, overlay map[string]any) {
	if overlay == nil {
		return
	}
	if value, ok := overlayNumber(overlay, "v_double"); ok {
		point.VDouble = value
	}
	if value, ok := overlay["quality"].(string); ok {
		point.Quality = value
	}
	if value, ok := overlay["unit"].(string); ok {
		point.Unit = value
	}
	if value, ok := overlay["measurement_key"].(string); ok {
		point.MeasurementKey = value
	}
}
