// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package coap

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/message/codes"
	"github.com/plgd-dev/go-coap/v3/mux"
)

// handleTelemetry accepts one JSON frame and publishes it through the
// shared uplink path.
func (s *Server) handleTelemetry(w mux.ResponseWriter, r *mux.Message) {
	_, id, ok := s.sessionFor(w, r)
	if !ok {
		return
	}

	body, err := r.ReadBody()
	if err != nil {
		writeError(w, codes.BadRequest, "failed to read telemetry")
		return
	}
	if int64(len(body)) > s.maxBytes {
		writeError(w, codes.RequestEntityTooLarge, "telemetry too large")
		return
	}
	published, err := s.publish(r.Context(), id, body, time.Now())
	if err != nil {
		writeError(w, codes.BadRequest, err.Error())
		return
	}
	replyJSON(w, codes.Content,
		`{"published":`+itoa(published)+`}`)
}

// handleCommands starts an asynchronous long poll. It must return
// immediately: go-coap serializes messages per connection in one receive
// loop, so blocking here would also stall the device's telemetry POSTs.
// The request is hijacked and answered from a goroutine with either the
// next mailbox command or an idle envelope once the long-poll window
// elapses.
func (s *Server) handleCommands(w mux.ResponseWriter, r *mux.Message) {
	session, _, ok := s.sessionFor(w, r)
	if !ok {
		return
	}

	reqType := r.Type()
	token := append(message.Token{}, r.Token()...)
	conn := w.Conn()
	r.Hijack()

	go func() {
		timer := time.NewTimer(s.longPoll)
		defer timer.Stop()

		var raw []byte
		select {
		case wire := <-session.Mailbox():
			frame := commandFrame{
				ID:     wire.Id,
				Name:   wire.Name,
				Params: json.RawMessage(wire.Params),
			}
			raw, _ = json.Marshal(commandsResponse{Command: &frame})
		case <-timer.C:
			raw = []byte(`{"command":null}`)
		}

		// A piggybacked response is an ACK for a CON request and a NON
		// message for a NON request, matching the client token.
		resp := conn.AcquireMessage(context.Background())
		defer conn.ReleaseMessage(resp)
		if reqType == message.Confirmable {
			resp.SetType(message.Acknowledgement)
		} else {
			resp.SetType(message.NonConfirmable)
		}
		resp.SetCode(codes.Content)
		resp.SetToken(token)
		resp.SetContentFormat(message.AppJSON)
		resp.SetBody(strings.NewReader(string(raw)))
		if err := conn.WriteMessage(resp); err != nil {
			slog.Debug("long-poll response write failed", "error", err)
		}
	}()
}

// handleCommandResponse correlates a device response with its pending
// request-response command.
func (s *Server) handleCommandResponse(w mux.ResponseWriter, r *mux.Message) {
	_, id, ok := s.sessionFor(w, r)
	if !ok {
		return
	}

	body, err := r.ReadBody()
	if err != nil {
		writeError(w, codes.BadRequest, "failed to read response")
		return
	}
	if int64(len(body)) > s.maxBytes {
		writeError(w, codes.RequestEntityTooLarge, "response too large")
		return
	}
	s.relay.ReportResponse(id, commandID(r), body)
	if err := w.SetResponse(codes.Changed, message.TextPlain, nil); err != nil {
		slog.Debug("cmdres response failed", "error", err)
	}
}
