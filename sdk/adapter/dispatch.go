// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package adapter

import (
	"context"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/sdk/contract"
)

// CommandHandler processes one dispatched command in protocol-specific
// terms. It returns the ack answering the dispatch request: DELIVERED for
// a one-way command handed to the transport, SUCCEEDED/FAILED for a
// request-response command, or NotConnected when no protocol session
// exists.
type CommandHandler func(ctx context.Context, cmd *v1.Command) (*v1.CommandAck, error)

// Dispatcher subscribes to the module dispatch subject and correlates
// protocol results with the Core NATS reply subject.
type Dispatcher struct {
	nc      *nats.Conn
	handler CommandHandler
}

// NewDispatcher constructs a command dispatcher backed by handler.
func NewDispatcher(nc *nats.Conn, handler CommandHandler) *Dispatcher {
	return &Dispatcher{nc: nc, handler: handler}
}

// Subscribe begins listening for dispatch requests for ingressID.
func (d *Dispatcher) Subscribe(ingressID string) (*nats.Subscription, error) {
	return d.nc.Subscribe(contract.DispatchSubjectPrefix+ingressID, d.onDispatch)
}

// onDispatch unmarshals one command, delegates it to the protocol handler
// and replies on the dispatch subject. A handler error is reported as
// FAILED rather than dropped so the command does not time out silently.
func (d *Dispatcher) onDispatch(msg *nats.Msg) {
	var wire v1.Command
	if err := proto.Unmarshal(msg.Data, &wire); err != nil {
		d.reply(msg.Reply, &v1.CommandAck{
			Status: contract.StatusFailed,
			Error:  "invalid command",
		})
		return
	}

	ack, err := d.handler(context.Background(), &wire)
	if err != nil {
		d.reply(msg.Reply, &v1.CommandAck{
			Id:     wire.Id,
			Status: contract.StatusFailed,
			Error:  err.Error(),
		})
		return
	}
	if ack == nil {
		ack = &v1.CommandAck{
			Id:     wire.Id,
			Status: contract.StatusDelivered,
		}
	}
	if ack.Id == "" {
		ack.Id = wire.Id
	}
	d.reply(msg.Reply, ack)
}

// reply publishes one ack onto the dispatch reply subject.
func (d *Dispatcher) reply(subject string, ack *v1.CommandAck) {
	if subject == "" {
		return
	}
	raw, err := proto.Marshal(ack)
	if err != nil {
		return
	}
	if err := d.nc.Publish(subject, raw); err != nil {
		return
	}
	if err := d.nc.Flush(); err != nil {
		return
	}
}

// Succeeded builds a SUCCEEDED ack with a raw JSON response.
func Succeeded(id string, response []byte) *v1.CommandAck {
	return &v1.CommandAck{
		Id:       id,
		Status:   contract.StatusSucceeded,
		Response: response,
	}
}

// Delivered builds a DELIVERED ack.
func Delivered(id string) *v1.CommandAck {
	return &v1.CommandAck{
		Id:     id,
		Status: contract.StatusDelivered,
	}
}

// Failed builds a FAILED ack with a message.
func Failed(id, message string) *v1.CommandAck {
	return &v1.CommandAck{
		Id:     id,
		Status: contract.StatusFailed,
		Error:  message,
	}
}

// NotConnected builds a NOT_CONNECTED ack.
func NotConnected(id string) *v1.CommandAck {
	return &v1.CommandAck{
		Id:     id,
		Status: contract.StatusNotConnected,
		Error:  "device not connected",
	}
}
