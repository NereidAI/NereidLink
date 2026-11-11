// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// linkepisode.go consumes edge-local link watchdog episode reports on
// v1/gateway/linkhealth. It validates the frame against the authenticated
// connection, applies the episode through the link-notify store (correcting
// the lease-guessed row or inserting a closed edge-only row), feeds
// unmatched episodes into the edge flap window, and always answers with a
// positive ack so the edge does not retransmit.

package ingestmqtt

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mochi-mqtt/server/v2/packets"

	"github.com/nereidai/nereidlink/internal/linkhealth"
	"github.com/nereidai/nereidlink/internal/linknotify"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// LinkEpisodeStore applies validated edge episodes. It is satisfied by
// *linknotify.Store.
type LinkEpisodeStore interface {
	ApplyEdgeEpisode(ctx context.Context, ep linknotify.EdgeEpisode) (linknotify.ApplyResult, error)
}

// validateLinkEpisode validates the frame against the authenticated gateway
// connection. Identity is never taken from the frame: a mismatching edge_id
// is rejected rather than re-scoped. It returns the rejection reason, empty
// when the frame is valid.
func validateLinkEpisode(frame linkhealth.LinkEpisodeReport, gatewayID string) string {
	if frame.EdgeID == "" {
		return "edge_id required"
	}
	if frame.EdgeID != gatewayID {
		return "edge_id mismatch"
	}
	if frame.DownAt.IsZero() {
		return "down_at required"
	}
	if frame.UpAt.IsZero() {
		return "up_at required"
	}
	if frame.UpAt.Before(frame.DownAt) {
		return "up_at before down_at"
	}
	if frame.Reason == "" {
		return "reason required"
	}
	return ""
}

// handleLinkEpisode processes one edge episode uplink. Frames that cannot be
// parsed are dropped; parseable but invalid frames receive a rejected ack so
// the edge stops retrying; a store failure returns no ack so the edge retries
// the transient error.
func (h *PublishHook) handleLinkEpisode(clientID string, payload []byte) {
	if h.linkEpisodes == nil {
		return
	}
	current, ok := h.connections.connectionFor(clientID)
	if !ok || !isGatewayIdentity(current.identity) || !current.negotiated {
		return
	}

	var frame linkhealth.LinkEpisodeReport
	if err := json.Unmarshal(payload, &frame); err != nil {
		h.Log.Warn("invalid link episode payload", "client", clientID, "error", err)
		return
	}

	if reason := validateLinkEpisode(frame, current.identity.DeviceID); reason != "" {
		h.Log.Warn("link episode rejected", "client", clientID, "reason", reason)
		h.sendLinkEpisodeAck(clientID, frame.ResponseTopic, linkhealth.LinkEpisodeAck{
			EdgeID: current.identity.DeviceID,
			DownAt: frame.DownAt,
			Status: linkhealth.EpisodeAckRejected,
			Reason: reason,
		})
		return
	}

	result, err := h.linkEpisodes.ApplyEdgeEpisode(context.Background(), linknotify.EdgeEpisode{
		TenantID:  current.identity.TenantID,
		GatewayID: current.identity.DeviceID,
		DownAt:    frame.DownAt.UTC(),
		UpAt:      frame.UpAt.UTC(),
		Reason:    frame.Reason,
	})
	if err != nil {
		h.Log.Error("apply link episode failed",
			"client", clientID, "down_at", frame.DownAt, "error", err)
		return
	}

	// Only episodes the lease never saw feed the independent edge flap
	// window; a corrected lease episode already counted toward lease flap.
	if result.Status == linknotify.ApplyAccepted && !result.MatchedLease && h.linkHealth != nil {
		h.linkHealth.ObserveEdgeEpisode(context.Background(),
			current.identity.TenantID, current.identity.DeviceID, frame.DownAt.UTC())
	}

	status := linkhealth.EpisodeAckAccepted
	if result.Status == linknotify.ApplyDuplicate {
		status = linkhealth.EpisodeAckDuplicate
	}
	h.sendLinkEpisodeAck(clientID, frame.ResponseTopic, linkhealth.LinkEpisodeAck{
		EdgeID: current.identity.DeviceID,
		DownAt: frame.DownAt,
		Status: status,
	})
}

// sendLinkEpisodeAck writes the ack to the edge, using the frame-provided
// response topic or the built-in default.
func (h *PublishHook) sendLinkEpisodeAck(clientID, responseTopic string, ack linkhealth.LinkEpisodeAck) {
	topic := responseTopic
	if topic == "" {
		topic = mqttapi.TopicGatewayLinkHealthAck
	}
	client, ok := h.brokerClient(clientID)
	if !ok {
		return
	}
	payload, err := json.Marshal(ack)
	if err != nil {
		h.Log.Warn("link episode ack encode failed", "client", clientID, "error", err)
		return
	}

	out := packets.Packet{
		FixedHeader: packets.FixedHeader{Type: packets.Publish},
		TopicName:   topic,
		Payload:     payload,
		Created:     time.Now().Unix(),
	}
	out.ProtocolVersion = client.Properties.ProtocolVersion
	if err := client.WritePacket(out); err != nil {
		h.Log.Warn("link episode ack write failed", "client", clientID, "error", err)
	}
}
