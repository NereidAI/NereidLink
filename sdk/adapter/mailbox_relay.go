// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/contract"
)

// pendingResponse correlates one request-response command with its NATS
// reply subject while waiting for the device response.
type pendingResponse struct {
	// reply is the Core NATS subject answering the dispatch request.
	reply string

	// stop cancels the timeout timer when the response arrives first.
	stop func()
}

// MailboxRelay bridges Core NATS dispatch with request-based protocol
// sessions: commands are buffered in the session mailbox for the next
// request, and request-response commands are correlated with the later
// response uplink.
type MailboxRelay struct {
	nc       *nats.Conn
	sessions *Sessions

	mu      sync.Mutex
	pending map[string]pendingResponse
}

// NewMailboxRelay constructs a mailbox command relay.
func NewMailboxRelay(nc *nats.Conn, sessions *Sessions) *MailboxRelay {
	return &MailboxRelay{
		nc:       nc,
		sessions: sessions,
		pending:  map[string]pendingResponse{},
	}
}

// Subscribe begins listening for dispatch requests for ingressID.
func (r *MailboxRelay) Subscribe(ingressID string) (*nats.Subscription, error) {
	return r.nc.Subscribe(contract.DispatchSubjectPrefix+ingressID, r.handleDispatch)
}

// handleDispatch queues one command in the session mailbox and, for a
// request-response command, waits for the correlated uplink.
func (r *MailboxRelay) handleDispatch(msg *nats.Msg) {
	var wire v1.Command
	if err := proto.Unmarshal(msg.Data, &wire); err != nil {
		r.reply(msg.Reply, &v1.CommandAck{
			Status: contract.StatusFailed,
			Error:  "invalid command",
		})
		return
	}

	current, ok := r.sessions.SessionFor(wire.TenantId, wire.DeviceId)
	if !ok {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: contract.StatusNotConnected,
			Error:  "device not connected",
		})
		return
	}

	// Non-blocking enqueue: the device is expected to keep one command
	// request in flight, so the mailbox normally drains immediately. A
	// full mailbox means the session stopped requesting and the command
	// must not be stranded silently in the dispatch wait.
	select {
	case current.mailbox <- &wire:
	default:
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: contract.StatusFailed,
			Error:  "command mailbox full",
		})
		return
	}

	if wire.Interaction != contract.InteractionRequestResponse {
		r.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: contract.StatusDelivered,
		})
		return
	}
	r.awaitResponse(&wire, msg.Reply)
}

// awaitResponse registers the correlation and replies TIMEOUT when the
// device response does not arrive within the command timeout.
func (r *MailboxRelay) awaitResponse(wire *v1.Command, reply string) {
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
			Status: contract.StatusTimeout,
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

// ReportResponse correlates a device response uplink with its contract.
// When the synchronous wait still exists the dispatch request is answered;
// otherwise the late result is published as a lifecycle event, per the
// command correlation contract.
func (r *MailboxRelay) ReportResponse(identity SessionIdentity, commandID string, payload []byte) {
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
			Status:   contract.StatusSucceeded,
			Response: payload,
		})
		return
	}

	event, err := json.Marshal(contract.Event{
		CommandID: commandID,
		TenantID:  identity.TenantID,
		DeviceID:  identity.DeviceID,
		Status:    contract.StatusSucceeded,
		Response:  string(payload),
		Time:      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return
	}
	_ = r.nc.Publish(contract.EventsSubjectPrefix+
		identity.TenantID+"."+identity.DeviceID, event)
}

// reply publishes one ack onto the Core NATS reply subject.
func (r *MailboxRelay) reply(subject string, ack *v1.CommandAck) {
	if subject == "" {
		return
	}
	raw, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	_ = r.nc.Publish(subject, raw)
}
