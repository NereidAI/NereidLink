// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// relay.go bridges Core NATS command dispatch requests with HTTP device
// sessions: commands are buffered in the session mailbox for the next
// long-poll, and request-response commands are correlated with the later
// POST cmdres uplink.

package ingesthttp

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/command"
)

// pendingResponse correlates one request-response command with the NATS
// reply subject while waiting for the device response.
type pendingResponse struct {
	// reply is the Core NATS subject answering the dispatch request.
	reply string

	// stop cancels the timeout timer when the response arrives first.
	stop func()
}

// Relay handles dispatch requests for this ingress node.
type Relay struct {
	nc       *nats.Conn
	sessions SessionLookup

	mu      sync.Mutex
	pending map[string]pendingResponse
}

// SessionLookup resolves devices to their live session. *Server satisfies
// it.
type SessionLookup interface {
	// SessionFor returns the live session, when one is open.
	SessionFor(tenantID, deviceID string) (*session, bool)
}

// NewRelay constructs the HTTP ingress command relay.
func NewRelay(nc *nats.Conn, sessions SessionLookup) *Relay {
	return &Relay{
		nc:       nc,
		sessions: sessions,
		pending:  map[string]pendingResponse{},
	}
}

// Subscribe begins listening for dispatch requests for ingressID.
func (r *Relay) Subscribe(ingressID string) error {
	_, err := r.nc.Subscribe(command.DispatchSubjectPrefix+ingressID, r.handleDispatch)
	return err
}

// handleDispatch queues one command in the session mailbox and, for
// request-response commands, waits for the correlated uplink.
func (r *Relay) handleDispatch(msg *nats.Msg) {
	var wire v1.Command
	if err := proto.Unmarshal(msg.Data, &wire); err != nil {
		r.reply(msg.Reply, &v1.CommandAck{
			Status: command.StatusFailed,
			Error:  "invalid command",
		})
		return
	}

	current, ok := r.sessions.SessionFor(wire.TenantId, wire.DeviceId)
	if !ok {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusNotConnected,
			Error:  "device not connected",
		})
		return
	}

	// Non-blocking enqueue: the device is expected to keep one long-poll in
	// flight, so the mailbox normally drains immediately. A full mailbox
	// means the session stopped polling and the command must not be lost in
	// the dispatch queue.
	select {
	case current.mailbox <- &wire:
	default:
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusFailed,
			Error:  "command mailbox full",
		})
		return
	}

	if wire.Interaction != command.InteractionRequestResponse {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusDelivered,
		})
		return
	}
	r.awaitResponse(&wire, msg.Reply)
}

// awaitResponse registers the correlation and replies TIMEOUT when the
// device response does not arrive within the command timeout.
func (r *Relay) awaitResponse(wire *v1.Command, reply string) {
	timeout := time.Duration(wire.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	timer := time.AfterFunc(timeout, func() {
		r.mu.Lock()
		_, awaited := r.pending[wire.Id]
		delete(r.pending, wire.Id)
		r.mu.Unlock()
		if !awaited {
			return
		}
		r.reply(reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: command.StatusTimeout,
			Error:  "response timeout",
		})
	})

	r.mu.Lock()
	r.pending[wire.Id] = pendingResponse{
		reply: reply,
		stop:  func() { timer.Stop() },
	}
	r.mu.Unlock()
}

// ReportResponse correlates a device response uplink with its command. When
// the synchronous wait still exists the dispatch request is answered;
// otherwise the late result is published as a lifecycle event, per ADR-0012.
func (r *Relay) ReportResponse(tenantID, deviceID, commandID string, payload []byte) {
	r.mu.Lock()
	pending, ok := r.pending[commandID]
	if ok {
		pending.stop()
		delete(r.pending, commandID)
	}
	r.mu.Unlock()

	if ok {
		r.reply(pending.reply, &v1.CommandAck{
			Id:       commandID,
			Status:   command.StatusSucceeded,
			Response: payload,
		})
		return
	}

	event, err := json.Marshal(command.Event{
		CommandID: commandID,
		TenantID:  tenantID,
		DeviceID:  deviceID,
		Status:    command.StatusSucceeded,
		Response:  string(payload),
		Time:      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = r.nc.Publish(command.EventsSubjectPrefix+tenantID+"."+deviceID, event)
}

// reply publishes one ack onto the Core NATS reply subject.
func (r *Relay) reply(subject string, ack *v1.CommandAck) {
	if subject == "" {
		return
	}
	raw, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	_ = r.nc.Publish(subject, raw)
}
