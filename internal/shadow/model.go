// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// model.go defines the shadow API documents and delta calculation.

package shadow

import (
	"reflect"
	"strconv"
	"time"
)

// State is one complete device shadow.
type State struct {
	// DeviceID identifies the shadowed device.
	DeviceID string `json:"device_id"`

	// Desired is the platform-requested configuration.
	Desired map[string]any `json:"desired"`

	// Reported is the device-observed configuration.
	Reported map[string]any `json:"reported"`

	// Delta contains desired values not matching reported.
	Delta map[string]any `json:"delta"`

	// DesiredVersion increments on desired changes.
	DesiredVersion int64 `json:"desired_version"`

	// ReportedVersion records the applied desired version.
	ReportedVersion int64 `json:"reported_version"`

	// ETag is the optimistic-concurrency tag for desired updates.
	ETag string `json:"etag"`

	// InSync reports an empty delta.
	InSync bool `json:"in_sync"`

	// DesiredAt is the last desired update time.
	DesiredAt *time.Time `json:"desired_at,omitempty"`

	// ReportedAt is the last reported update time.
	ReportedAt *time.Time `json:"reported_at,omitempty"`

	// UpdatedAt is the last shadow row update time.
	UpdatedAt time.Time `json:"updated_at"`
}

// DesiredPatch is a platform update to desired state.
type DesiredPatch struct {
	// State contains top-level configuration values to merge.
	State map[string]any `json:"state"`

	// ExpectedETag enables optimistic concurrency; empty skips the check.
	ExpectedETag string `json:"-"`
}

// ReportedPatch is a device application report.
type ReportedPatch struct {
	// State contains top-level values reported by the device.
	State map[string]any `json:"state"`

	// Version is the desired version applied; nil means current desired version.
	Version *int64 `json:"version,omitempty"`
}

// PushFrame is delivered to an ingress when desired changes or a device connects.
type PushFrame struct {
	// TenantID owns the device.
	TenantID string `json:"tenant_id"`

	// DeviceID receives the desired state.
	DeviceID string `json:"device_id"`

	// Version is the desired version.
	Version int64 `json:"version"`

	// State is the complete desired state.
	State map[string]any `json:"state"`
}

// EventFrame is the console-plane shadow change frame pushed after every
// committed desired or reported upsert. It carries the complete merged
// shadow (not just the patch) so console clients can replace their view
// directly, and identifies which writer caused the change.
type EventFrame struct {
	// State embeds the full post-commit shadow.
	State

	// TenantID owns the shadowed device.
	TenantID string `json:"tenant_id"`

	// Source is SourceServer for desired patches and SourceDevice for
	// device-reported state.
	Source string `json:"source"`
}

// Change origins carried in EventFrame.Source.
const (
	// SourceServer marks a platform REST write to desired state.
	SourceServer = "server"

	// SourceDevice marks a device-reported (or rule-driven) reported state.
	SourceDevice = "device"
)

func newState(deviceID string) State {
	return State{
		DeviceID: deviceID,
		Desired:  map[string]any{},
		Reported: map[string]any{},
		Delta:    map[string]any{},
		ETag:     etag(0),
		InSync:   true,
	}
}

func computeDelta(desired, reported map[string]any) map[string]any {
	delta := map[string]any{}
	for key, desiredValue := range desired {
		reportedValue, ok := reported[key]
		if !ok || !reflect.DeepEqual(desiredValue, reportedValue) {
			delta[key] = desiredValue
		}
	}
	return delta
}

// mergePatch applies a top-level shadow patch to current and returns a new
// map. A present key is overwritten; a JSON null value deletes the key, so a
// target/observed state can shrink as well as grow. current is never mutated.
func mergePatch(current, patch map[string]any) map[string]any {
	merged := make(map[string]any, len(current)+len(patch))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range patch {
		if value == nil {
			delete(merged, key)
			continue
		}
		merged[key] = value
	}
	return merged
}

func etag(version int64) string {
	return "\"" + strconv.FormatInt(version, 10) + "\""
}
