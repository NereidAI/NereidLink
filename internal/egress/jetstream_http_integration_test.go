// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package egress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// TestJetStreamPublisherWorkerHTTPDelivery is an environment-gated integration
// test for the complete producer-to-webhook path. It deliberately does not
// import github.com/nats-io/nats-server/v2: set NEREIDLINK_TEST_NATS_URL to a
// NATS server with JetStream enabled to run it. The default unit suite skips
// this test and continues to use sink-level and in-memory seam coverage.
func TestJetStreamPublisherWorkerHTTPDelivery(t *testing.T) {
	natsURL := os.Getenv("NEREIDLINK_TEST_NATS_URL")
	if natsURL == "" {
		t.Skip("NEREIDLINK_TEST_NATS_URL not set; skipping JetStream publisher/worker integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	nc, err := nats.Connect(natsURL, nats.Name("nereidlink-egress-integration"))
	if err != nil {
		t.Fatalf("connect NATS: %v", err)
	}
	t.Cleanup(func() { _ = nc.Drain() })

	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("create JetStream context: %v", err)
	}

	received := make(chan Envelope, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Errorf("read webhook body: %v", readErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var env Envelope
		if unmarshalErr := json.Unmarshal(raw, &env); unmarshalErr != nil {
			t.Errorf("decode webhook envelope: %v", unmarshalErr)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received <- env
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	suffix := time.Now().UnixNano()
	tenantID := "egress-js-integration"
	forwarderID := "fwd-" + strconv.FormatInt(suffix, 10)
	forwarder := Forwarder{
		ID:                forwarderID,
		TenantID:          tenantID,
		Name:              "jetstream-http-integration",
		SinkType:          SinkHTTP,
		SinkConfig:        map[string]any{"url": server.URL},
		Encoder:           EncoderJSON,
		MaxAttempts:       1,
		MessageTTLMS:      60_000,
		MaxBufferMessages: 10,
		Enabled:           true,
		CreatedAt:         time.Now().UTC(),
		UpdatedAt:         time.Now().UTC(),
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = DeleteStream(cleanupCtx, js, tenantID, forwarderID)
	})

	worker := NewWorker(js, nil, WorkerTuning{
		Workers: 1, DeliveryTimeout: 5 * time.Second, MaxAckPending: 1,
	}, 1)
	worker.baseCtx = context.Background()
	worker.store = &fakeWorkerStore{all: []Forwarder{forwarder}}
	if err := worker.reconcile(ctx); err != nil {
		t.Fatalf("start egress consumer: %v", err)
	}
	t.Cleanup(worker.stopAll)

	deadline := time.Now().Add(5 * time.Second)
	for len(worker.running) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if len(worker.running) != 1 {
		t.Fatalf("egress consumer did not start: %+v", worker.running)
	}

	env := Envelope{
		ID:       "env-" + strconv.FormatInt(suffix, 10),
		TenantID: tenantID,
		DeviceID: "device-1",
		Channel:  "meter-reading.v1",
		Ts:       time.Now().UnixMilli(),
		Data:     map[string]any{"meterId": "m-1", "value": 42.5},
	}
	publisher := NewPublisher(js, time.Second)
	if err := publisher.Publish(ctx, forwarderID, env); err != nil {
		t.Fatalf("publish egress envelope: %v", err)
	}

	select {
	case got := <-received:
		if got.ID != env.ID || got.Channel != env.Channel || got.TenantID != env.TenantID {
			t.Fatalf("webhook received mismatched envelope: got=%+v want=%+v", got, env)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for HTTP webhook delivery: %v", ctx.Err())
	}
}
