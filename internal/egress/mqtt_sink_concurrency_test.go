// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestMQTTSinkCoalescesConnectAttempts hammers one sink with concurrent
// deliveries against a dead broker. paho rejects overlapping Connect calls
// with "status can only transition to connecting from disconnected", so the
// sink must coalesce: no outcome may carry that error.
func TestMQTTSinkCoalescesConnectAttempts(t *testing.T) {
	sink, err := buildSink(Forwarder{
		SinkType: SinkMQTT,
		SinkConfig: map[string]any{
			"broker": "tcp://127.0.0.1:1", "topic": "nl/test", "qos": 0,
		},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer func() {
		if closer, ok := sink.(sinkCloser); ok {
			_ = closer.Close()
		}
	}()

	var wg sync.WaitGroup
	results := make(chan Outcome, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
			defer cancel()
			results <- sink.Deliver(ctx, Envelope{ID: "x"})
		}()
	}
	wg.Wait()
	close(results)
	for out := range results {
		if out.Result != ResultRetryable {
			t.Fatalf("want retryable, got %d (%s)", out.Result, out.Reason)
		}
		if strings.Contains(out.Reason, "status can only transition") {
			t.Fatalf("overlapping Connect calls: %s", out.Reason)
		}
	}
}
