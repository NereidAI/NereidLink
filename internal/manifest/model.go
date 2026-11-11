// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package manifest implements the 5-minute gateway manifest reconciliation
// slice of ADR-0026. A NereidEdge gateway publishes, aligned to an absolute
// wall-clock boundary, the full list of sub-devices it currently has online.
// NL reconciles that "reported" set against the "desired" set (devices bound
// to the gateway through device.gateway_id) entirely event-driven: there is
// no per-gateway timer.
//
// Three classes result from every reconciliation:
//
//   - in_sync:      reported and bound; presence is converged ONLINE.
//   - unregistered: reported but not bound; presence is never created and a
//     drift advisory is emitted. This slice does not implement
//     autonomous provisioning, so these devices are rejected
//     rather than created.
//   - missing:      bound but not reported; gateway-attached presence is
//     converged OFFLINE and a drift advisory is emitted.
//
// A gateway that stops sending manifests is detected two ways, mirroring the
// presence node-lease design: an explicit lease-key watch and a global sweep
// that backstops silent KV TTL expiry (which raises no watch event). Neither
// path keeps per-gateway timers.
//
// The optional connector segment (Frame.Connectors, migration 0044) is
// decoded here but reconciled by the connector service, not this package:
// the ingress hands the reported instance list to
// connector.Service.ReconcileReported for desired/reported reconciliation
// and optional strict desired override.
package manifest

import "time"

// ConnectorInstance is one entry of the connector segment: an applied
// connector instance the edge reports in its full-list manifest. It mirrors
// connector.ReportedInstance but is declared here to keep manifest transport
// structs self-contained.
type ConnectorInstance struct {
	// SpecID is the desired spec id the edge applied.
	SpecID string `json:"spec_id"`

	// Revision is the desired revision the edge applied.
	Revision int64 `json:"revision"`

	// ConfigHash is the optional sha256 of the canonical effective
	// config+mappings; empty means content drift is not assessed.
	ConfigHash string `json:"config_hash,omitempty"`
}

// ModelInstance is one entry of the optional full-replacement models segment
// (ADR-0039): a model currently present on the gateway. It mirrors the edge
// SDK's device.ManifestModel but is declared here to keep manifest transport
// structs self-contained. The inventory key is (ID, Version).
type ModelInstance struct {
	// ID is the globally meaningful model identifier.
	ID string `json:"id"`
	// Version is the semantic model version.
	Version string `json:"version"`
	// Revision is the build/optimization job id (a string, e.g. "76937").
	Revision string `json:"revision,omitempty"`
	// Task is the task family, e.g. "detection".
	Task string `json:"task"`
	// Family is the in-task result/post-process family, e.g. "yolov8".
	Family string `json:"family,omitempty"`
	// Backend is the inference protocol family, e.g. "kserve".
	Backend string `json:"backend"`
	// ServerFamily optionally names the concrete model-server product
	// serving this model, e.g. "ovms"/"triton"/"external". It is distinct
	// from Backend (a protocol family), omitted by older edges, and stored
	// verbatim as an open enum.
	ServerFamily string `json:"server_family,omitempty"`
	// ModelName is the runtime-facing name, for display/diagnostics only.
	ModelName string `json:"model_name,omitempty"`
	// State is one of EXTRACTING/VALIDATING/READY/FAILED/REMOVED.
	State string `json:"state"`
	// Reason carries a transport-safe failure code; empty on success.
	Reason string `json:"reason,omitempty"`
	// Labels are the class names indexed by class id; stored verbatim and
	// never interpreted or internationalized by the kernel.
	Labels []string `json:"labels,omitempty"`
	// ActiveBindings counts edge-inference instances referencing the model.
	ActiveBindings int `json:"active_bindings,omitempty"`
}

// Frame is the JSON payload of the gateway manifest uplink. It is a subset
// of the heartbeat manifest segment defined in docs/protocols/nereid-edge/
// v1.md section 6; the device segment is reconciled here, while the optional
// connector segment (migration 0044) is decoded and handed to the connector
// service for desired/reported reconciliation.
type Frame struct {
	// Devices is the gateway's full current online sub-device list. It is
	// a full-state report, not a delta: every bound sub-device absent from
	// this list is treated as missing for the cycle.
	Devices []string `json:"devices"`

	// Connectors is the gateway's full current applied connector-instance
	// list. It is a pointer so the decoder distinguishes an ABSENT field
	// (nil: this edge does not report connectors, so skip connector
	// reconciliation) from an explicit empty array (zero connectors: every
	// desired instance is missing for the cycle).
	Connectors *[]ConnectorInstance `json:"connectors,omitempty"`

	// Models is the gateway's full local model inventory (ADR-0039). As
	// with Connectors it is a pointer: nil means the edge did not advertise
	// a models segment (older/unsupported firmware: keep stored inventory
	// untouched), while an explicit empty non-nil slice is an authoritative
	// zero-inventory report that prunes every row for the gateway. It is
	// decoded here but reconciled by the modelinventory service, exactly
	// like the connector segment.
	Models *[]ModelInstance `json:"models,omitempty"`

	// Revision is the optional manifest/desired revision the gateway last
	// observed. It is recorded for drift correlation only in this slice.
	Revision int64 `json:"revision,omitempty"`

	// MissedCycles reports how many aligned boundaries the gateway skipped
	// before this frame (it never backfills old cycles). Recorded for
	// observability.
	MissedCycles int `json:"missed_cycles,omitempty"`
}

// Class partitions one cycle's devices into the three reconciliation sets.
type Class struct {
	// InSync lists devices both reported by the gateway and bound to it.
	InSync []string `json:"in_sync"`

	// Missing lists bound devices the gateway did not report this cycle.
	Missing []string `json:"missing"`

	// Unregistered lists reported devices that are not bound to the
	// gateway (unknown, another tenant, or another gateway).
	Unregistered []string `json:"unregistered"`
}

// Drift reports whether the cycle differed from the desired state.
func (c Class) Drift() bool {
	return len(c.Missing) > 0 || len(c.Unregistered) > 0
}

// Report is the outcome of reconciling one manifest frame.
type Report struct {
	// TenantID and GatewayID identify the reconciled gateway.
	TenantID  string `json:"tenant_id"`
	GatewayID string `json:"gateway_id"`

	// At is the server time the frame was reconciled.
	At time.Time `json:"-"`

	// Revision echoes the frame's observed desired revision.
	Revision int64 `json:"revision"`

	// MissedCycles echoes the gateway's skipped-boundary counter.
	MissedCycles int `json:"missed_cycles"`

	// Class holds the three reconciled sets.
	Class Class `json:"class"`

	// OnlinePromoted counts missing presence keys created for in-sync
	// devices; OfflineDerived counts presence keys removed for missing
	// devices. Both exclude no-op convergence.
	OnlinePromoted int `json:"online_promoted"`
	OfflineDerived int `json:"offline_derived"`
}
