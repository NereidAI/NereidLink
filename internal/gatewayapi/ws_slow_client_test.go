// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestClientEnqueueNeverBlocksOnSlowPeer is the regression guard for the
// hub-wide fanout deadlock: a WebSocket peer that stops reading must not be
// able to stall routing. Once the per-connection send buffer fills, enqueue
// must return promptly and the client must be unregistered via onClose.
func TestClientEnqueueNeverBlocksOnSlowPeer(t *testing.T) {
	accepted := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		close(accepted)
		// Block without reading until the handler tears down.
		ctx := r.Context()
		<-ctx.Done()
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx,
		strings.Replace(srv.URL, "http://", "ws://", 1), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.CloseNow() }()
	<-accepted

	removed := make(chan struct{}, 1)
	client := NewClient(conn, func(*Client) { removed <- struct{}{} }, time.Hour)

	payload := make([]byte, 32*1024)
	copy(payload, []byte(`{"type":"telemetry"}`))
	done := make(chan struct{})
	go func() {
		// One frame beyond the buffer: the peer never reads, so this must
		// still return once the slow-consumer path triggers.
		for i := 0; i < wsSendBuffer+2; i++ {
			client.enqueue(payload)
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wsWriteTimeout + 3*time.Second):
		t.Fatal("enqueue blocked on a non-reading peer; fanout could deadlock")
	}

	select {
	case <-removed:
	case <-time.After(wsWriteTimeout + 3*time.Second):
		t.Fatal("slow client was not unregistered via onClose")
	}
}
