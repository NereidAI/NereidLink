// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// executeExpression runs an expression hook and returns field overlays.
//
// The script receives the point as `msg` and follows ThingsBoard-style
// bare return semantics:
//
//   - returning an object overlays v_double / quality / unit /
//     measurement_key; {"drop": true} aborts the point.
//   - returning null or undefined leaves the point unchanged.
func executeExpression(ctx context.Context, runner *scriptruntime.Runtime, hook Hook, point Point) (map[string]any, bool, error) {
	value, err := runner.EvalValue(hook.Script, point.asMap())
	if err != nil {
		return nil, false, err
	}
	switch typed := value.(type) {
	case nil:
		return nil, false, nil
	case map[string]any:
		drop, _ := typed["drop"].(bool)
		return typed, drop, nil
	default:
		return nil, false, fmt.Errorf("expression hook must return an object or null")
	}
}

// executeWebhook posts the point to an external callback and returns field
// overlays. Calls pass through the per-hook circuit breaker: when the
// breaker is open the function fails fast with ErrCircuitOpen so the
// configured failure policy applies without spending timeout budget.
func executeWebhook(ctx context.Context, client *http.Client, breaker *breaker, hook Hook, point Point) (map[string]any, error) {
	now := time.Now()
	if !breaker.allow(now) {
		return nil, ErrCircuitOpen
	}

	encoded, err := json.Marshal(point.asMap())
	if err != nil {
		return nil, err
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(hook.TimeoutMS)*time.Millisecond)
	defer cancel()

	request, err := http.NewRequestWithContext(callCtx, http.MethodPost, hook.URL, bytes.NewReader(encoded))
	if err != nil {
		breaker.recordFailure(time.Now())
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		breaker.recordFailure(time.Now())
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		breaker.recordFailure(time.Now())
		return nil, fmt.Errorf("webhook returned status %d", response.StatusCode)
	}

	// An empty body is a valid no-overlay acknowledgement.
	var overlay map[string]any
	if err := json.NewDecoder(response.Body).Decode(&overlay); err != nil && !errors.Is(err, io.EOF) {
		breaker.recordFailure(time.Now())
		return nil, fmt.Errorf("decode webhook response: %w", err)
	}
	breaker.recordSuccess()
	return overlay, nil
}
