// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// linkepisode.go defines the wire frames for edge-local link watchdog
// episode reports (v1/gateway/linkhealth). The frames are mirrored here
// rather than imported from the device SDK module, which is a separate Go
// module for external consumers and is not part of the platform build; the
// same convention is used for Frame and Goodbye in this package.

package linkhealth

import "time"

// EpisodeReasonTransportDown is the documented episode reason: the transport
// went down outside a planned stop.
const EpisodeReasonTransportDown = "transport_down"

// Link episode ack status values.
const (
	// EpisodeAckAccepted means the episode was applied.
	EpisodeAckAccepted = "accepted"

	// EpisodeAckDuplicate means the (gateway, down_at) episode already
	// exists; the edge must stop retransmitting it.
	EpisodeAckDuplicate = "duplicate"

	// EpisodeAckRejected means the frame failed validation.
	EpisodeAckRejected = "rejected"
)

// LinkEpisodeReport is the uplink frame on v1/gateway/linkhealth. DownAt and
// UpAt are the edge local ground truth; the server uses them to correct its
// lease-guessed episode boundaries.
type LinkEpisodeReport struct {
	// EdgeID is the gateway device UUID. It must equal the authenticated
	// connection's device id.
	EdgeID string `json:"edge_id"`

	// SessionID optionally names the platform session that preceded the
	// episode (hello ack session_id); informational only.
	SessionID string `json:"session_id,omitempty"`

	// DownAt is the edge-local time the link went down, RFC3339.
	DownAt time.Time `json:"down_at"`

	// UpAt is the edge-local time re-hello completed, RFC3339.
	UpAt time.Time `json:"up_at"`

	// Reason explains the episode; documented value
	// EpisodeReasonTransportDown.
	Reason string `json:"reason"`

	// ResponseTopic optionally overrides the default ack topic.
	ResponseTopic string `json:"response_topic,omitempty"`
}

// LinkEpisodeAck is the platform response frame.
type LinkEpisodeAck struct {
	// EdgeID echoes the gateway device UUID.
	EdgeID string `json:"edge_id"`

	// DownAt echoes the reported episode start.
	DownAt time.Time `json:"down_at"`

	// Status is one of the EpisodeAck* constants.
	Status string `json:"status"`

	// Reason explains a rejected frame.
	Reason string `json:"reason,omitempty"`
}
