// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package horizon is the NereidLink client for the NereidHorizon
// nh-model-distribution/v1 contract. NereidLink never moves model bytes: it
// posts idempotent ensure requests to NereidHorizon, which streams Forge
// packages through ESS and owns task lifecycle, cancellation and rollback.
// See NereidOverview/docs/contracts/nh-model-distribution-v1.md.
package horizon

// ContractVersion identifies the NH contract this client conforms to.
const ContractVersion = "nh-model-distribution/v1"

// Task lifecycle states, mirrored from the NH contract.
const (
	StateInProgress = "in_progress"
	StateDelivered  = "delivered"
	StateFailed     = "failed"
	StateCancelled  = "cancelled"
	StateRolledBack = "rolled_back"
)

// Precision values form the closed hardware-aware payload vocabulary; no
// per-application or per-silicon branch exists in NereidLink.
const (
	PrecisionFP16 = "fp16"
	PrecisionINT8 = "int8"
)

// DefaultObjectType is the neutral ESS object type used when an assembly does
// not pin one. NH treats objectType as an opaque safe token; keeping one
// platform-wide type makes the derived object IDs predictable across tenants.
const DefaultObjectType = "inference-model"

// EnsureRequest is the POST distribution-tasks envelope. Field tags match the
// NH wire contract byte for byte.
type EnsureRequest struct {
	ModelName    string   `json:"modelName"`
	Version      string   `json:"version"`
	Precision    string   `json:"precision"`
	ObjectType   string   `json:"objectType"`
	ObjectID     string   `json:"objectID"`
	Destinations []string `json:"destinations"`
	Description  string   `json:"description,omitempty"`
}

// Task is the NH distribution task handle. Only the fields NL acts on are
// typed; the wire struct stays the full contract shape for diagnostics.
type Task struct {
	TaskID           string   `json:"taskId"`
	Org              string   `json:"org"`
	ObjectType       string   `json:"objectType"`
	ObjectID         string   `json:"objectID"`
	ModelName        string   `json:"modelName"`
	Version          string   `json:"version"`
	Precision        string   `json:"precision"`
	Destinations     []string `json:"destinations"`
	State            string   `json:"state"`
	DeliveryState    string   `json:"deliveryState"`
	EdgeReady        bool     `json:"edgeReady"`
	Delivery         []any    `json:"delivery"`
	FailureReason    string   `json:"failureReason"`
	PackageSHA256    string   `json:"packageSHA256"`
	PackageBytes     int64    `json:"packageBytes"`
	IdempotentReplay bool     `json:"idempotentReplay"`
	Adopted          bool     `json:"adopted"`
	CreatedAt        string   `json:"createdAt"`
	UpdatedAt        string   `json:"updatedAt"`
	DeliveredAt      string   `json:"deliveredAt"`
	FinishedAt       string   `json:"finishedAt"`
}

// Active reports whether the task still holds NH's ensure idempotency key.
func (t Task) Active() bool {
	return t.State == StateInProgress || t.State == StateDelivered
}
