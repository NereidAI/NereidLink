// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// handlers.go implements the three device-facing HTTP endpoints: telemetry
// uplink, long-poll command retrieval and command response uplink.

package ingesthttp

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/nereidai/nereidlink/internal/accounting"
	"github.com/nereidai/nereidlink/internal/contract"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// commandFrame is the JSON document returned to a polling device.
type commandFrame struct {
	// ID is the command identifier.
	ID string `json:"id"`

	// Name is the command name from the catalog.
	Name string `json:"name"`

	// Params is the catalog-validated JSON document delivered verbatim.
	Params json.RawMessage `json:"params"`
}

// commandsResponse is the long-poll envelope. Command is null when no
// command became available within the wait window.
type commandsResponse struct {
	// Command carries the next queued command, if any.
	Command *commandFrame `json:"command"`
}

// telemetryResponse reports how many points the frame produced.
type telemetryResponse struct {
	// Published is the number of Envelopes published.
	Published int `json:"published"`
}

// handleTelemetry accepts one JSON telemetry frame and publishes one
// Envelope per measurement key through the shared uplink path.
func (s *Server) handleTelemetry(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	r.Body = http.MaxBytesReader(w, r.Body, s.tune.MaxTelemetryBytes)
	frame, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeDeviceError(w, http.StatusRequestEntityTooLarge, "TELEMETRY_TOO_LARGE",
				"telemetry frame exceeds the accepted body size")
			return
		}
		writeDeviceError(w, http.StatusBadRequest, "TELEMETRY_READ_FAILED",
			"failed to read telemetry frame")
		return
	}

	if s.deviceLimit != nil &&
		!s.deviceLimit.Allow(identity.TenantID+"."+identity.DeviceID, time.Now()) {
		writeDeviceError(w, http.StatusTooManyRequests, "RATE_LIMITED",
			"device ingest rate limit exceeded")
		return
	}

	published, err := sdktelemetry.Publish(r.Context(), s.js, s.resolver,
		sdktelemetry.Identity{
			TenantID: identity.TenantID,
			DeviceID: identity.DeviceID,
		}, contract.ProtocolHTTP, frame, time.Now(),
		sdktelemetry.WithEncoder(s.encoder),
		sdktelemetry.WithMediaStore(s.mediaStore))
	if err != nil {
		writeDeviceError(w, http.StatusBadRequest, "TELEMETRY_REJECTED", err.Error())
		return
	}
	// Accounting runs only after a successful publish so the sampled view
	// reflects traffic the platform actually accepted.
	if s.accounting != nil {
		s.accounting.Observe(accounting.Event{
			TenantID: identity.TenantID,
			DeviceID: identity.DeviceID,
			RemoteIP: clientIP(r),
			Protocol: contract.ProtocolHTTP,
			Bytes:    len(frame),
		})
	}
	writeDeviceJSON(w, http.StatusOK, telemetryResponse{Published: published})
}

// clientIP returns the uploader's address for the accounting "ip"
// dimension. Proxy headers are deliberately NOT consulted: accounting must
// not be influenced by a client-supplied value, and the platform does not
// sit behind a trusted proxy it controls.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// handleCommands returns one queued command or holds the request open until
// one arrives. The device polls continuously; an empty response simply
// triggers the next poll.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	timer := time.NewTimer(s.tune.LongPollWait)
	defer timer.Stop()

	select {
	case wire := <-current.mailbox:
		writeDeviceJSON(w, http.StatusOK, commandsResponse{
			Command: &commandFrame{
				ID:     wire.Id,
				Name:   wire.Name,
				Params: wire.Params,
			},
		})
	case <-timer.C:
		writeDeviceJSON(w, http.StatusOK, commandsResponse{})
	case <-r.Context().Done():
		w.WriteHeader(499)
	}
}

// handleCommandResponse correlates a device response with its pending
// request-response command.
func (s *Server) handleCommandResponse(w http.ResponseWriter, r *http.Request) {
	identity, ok := s.authenticate(r)
	if !ok {
		writeDeviceError(w, http.StatusUnauthorized, "UNAUTHORIZED",
			"missing or invalid access token")
		return
	}
	current, err := s.ensureSession(r.Context(), identity)
	if err != nil {
		writeDeviceError(w, http.StatusInternalServerError, "SESSION_FAILED",
			"failed to open device session")
		return
	}
	s.touch(current)

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.tune.MaxTelemetryBytes))
	if err != nil {
		writeDeviceError(w, http.StatusBadRequest, "RESPONSE_READ_FAILED",
			"failed to read command response")
		return
	}
	s.relay.ReportResponse(identity.TenantID, identity.DeviceID,
		r.PathValue("command"), payload)
	w.WriteHeader(http.StatusNoContent)
}
