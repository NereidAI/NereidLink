// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package ota implements firmware OTA task orchestration (ADR-0026 edge
// plane). An application posts a firmware descriptor plus a list of target
// sub-devices; the platform groups them by owning gateway, sends one
// v1/gateway/ota/task downlink frame per gateway, and correlates the
// v1/gateway/ota/progress frames the gateways report by task id and device.
//
// The platform carries no firmware bytes: package_url points at an
// operator-supplied HTTPS endpoint the edge downloads from directly and the
// edge enforces package_sha256 before applying. NL only relays the descriptor
// and the progress. OTA tasks are long-lived (a rollout may take hours or
// days) so terminal results are retained rather than swept after 24h.
package ota

import (
	"encoding/hex"
	"time"
)

// Task aggregate states.
const (
	TaskStatePending = "pending"
	TaskStateRunning = "running"
	// TaskStatePaused is an operator hold: no new batches open, devices
	// already applying finish undisturbed (ADR-0006d §5).
	TaskStatePaused    = "paused"
	TaskStateCompleted = "completed"
	TaskStatePartial   = "partial"
	TaskStateFailed    = "failed"
	// TaskStateAborted is the automatic threshold abort; it is distinct from
	// the human-initiated TaskStateCanceled.
	TaskStateAborted  = "aborted"
	TaskStateCanceled = "canceled"
)

// Target modes record how a rollout's devices were chosen.
const (
	// TargetModeExplicit is the historical UUID list.
	TargetModeExplicit = "explicit"
	// TargetModeSelector resolves a profile/group/customer selector to a
	// device snapshot at creation time.
	TargetModeSelector = "selector"
)

// Per-device item phases. The edge advances through them in order; the
// ota_progress_order() SQL function enforces forward-only movement.
//
//	pending -> initiated -> downloading -> downloaded -> verified
//	       -> updating -> updated -> rebooting -> confirmed (-> succeeded)
//
// succeeded/failed/canceled/skipped are terminal. 'confirmed' is a
// device-reported timeline phase only: under the confirmed_after_reboot
// policy a matching confirmation is normalized to succeeded, so it is never
// stored as the item state. skipped is set by the platform for a target it
// could not dispatch (for example an offline gateway at creation); canceled is
// set by the safe cancel operation before the device started applying.
const (
	ItemStatePending     = "pending"
	ItemStateInitiated   = "initiated"
	ItemStateDownloading = "downloading"
	ItemStateDownloaded  = "downloaded"
	ItemStateVerified    = "verified"
	ItemStateUpdating    = "updating"
	ItemStateUpdated     = "updated"
	ItemStateRebooting   = "rebooting"
	ItemStateConfirmed   = "confirmed"
	ItemStateSucceeded   = "succeeded"
	ItemStateFailed      = "failed"
	ItemStateCanceled    = "canceled"
	ItemStateSkipped     = "skipped"
)

// Success policies select how a device reaches the succeeded terminal state.
const (
	// PolicyDeviceReported accepts the edge's own success report immediately
	// (the historical behaviour): an updated/succeeded frame finalizes the
	// item and writes the firmware version back.
	PolicyDeviceReported = "device_reported"
	// PolicyConfirmedAfterReboot keeps the item open after 'updated': the
	// device must reconnect and report 'confirmed' with the running version,
	// which the platform matches against the task target version before it
	// finalizes success.
	PolicyConfirmedAfterReboot = "confirmed_after_reboot"
)

// Structured result/reason codes reported alongside a phase. They are
// orthogonal to the ordered phase: progress phases carry ResultNone; failed
// and skipped/canceled outcomes carry the specific reason.
const (
	ResultNone                 = "none"
	ResultFlashFull            = "flash_full"
	ResultOutOfRAM             = "out_of_ram"
	ResultConnectionLost       = "connection_lost"
	ResultIntegrityCheckFailed = "integrity_check_failed"
	ResultUnsupportedType      = "unsupported_type"
	ResultModelMismatch        = "model_mismatch"
	ResultVersionMismatch      = "version_mismatch"
	ResultInvalidURI           = "invalid_uri"
	ResultApplyFailed          = "apply_failed"
	ResultRebootTimeout        = "reboot_timeout"
	ResultDownloadNoProgress   = "download_no_progress"
	ResultAlreadyCurrent       = "already_current"
	ResultCanceled             = "canceled"
	ResultUnknown              = "unknown"
)

// Timeline event sources.
const (
	SourcePlatform = "platform"
	SourceDevice   = "device"
	SourceGateway  = "gateway"
)

// isProgressState reports a non-terminal, forward-only edge progress phase.
func isProgressState(s string) bool {
	switch s {
	case ItemStatePending, ItemStateInitiated, ItemStateDownloading,
		ItemStateDownloaded, ItemStateVerified, ItemStateUpdating,
		ItemStateUpdated, ItemStateRebooting, ItemStateConfirmed:
		return true
	}
	return false
}

// IsItemTerminal reports whether the item phase closes the rollout for one
// device.
func IsItemTerminal(s string) bool {
	switch s {
	case ItemStateSucceeded, ItemStateFailed, ItemStateCanceled, ItemStateSkipped:
		return true
	}
	return false
}

// openItemStates lists the phases from which a progress frame or safe cancel
// may still move an item. It is used to build SQL IN (...) clauses.
var openItemStates = []string{
	ItemStatePending, ItemStateInitiated, ItemStateDownloading,
	ItemStateDownloaded, ItemStateVerified, ItemStateUpdating,
	ItemStateUpdated, ItemStateRebooting,
}

// ValidSuccessPolicy reports whether p is an accepted success policy.
func ValidSuccessPolicy(p string) bool {
	return p == PolicyDeviceReported || p == PolicyConfirmedAfterReboot
}

// freezeStateFor maps a pinned aggregate state for recompute: canceled and
// aborted stay pinned through late device results.
func freezeStateFor(state string) string {
	switch state {
	case TaskStateCanceled:
		return TaskStateCanceled
	case TaskStateAborted:
		return TaskStateAborted
	case TaskStatePaused:
		return TaskStatePaused
	}
	return ""
}

// ValidResultCode reports whether code is a known result code (empty is also
// accepted and normalized to "none").
func ValidResultCode(code string) bool {
	switch code {
	case "", ResultNone, ResultFlashFull, ResultOutOfRAM, ResultConnectionLost,
		ResultIntegrityCheckFailed, ResultUnsupportedType, ResultModelMismatch,
		ResultVersionMismatch, ResultInvalidURI, ResultApplyFailed,
		ResultRebootTimeout, ResultDownloadNoProgress, ResultAlreadyCurrent,
		ResultCanceled, ResultUnknown:
		return true
	}
	return false
}

// Package describes one firmware artifact.
type Package struct {
	// Version is the firmware version label the edge reports after success.
	Version string `json:"version"`
	// URL is the operator-supplied HTTPS download endpoint. NL never proxies
	// the bytes.
	URL string `json:"url"`
	// SHA256 is the mandatory lowercase hex digest the edge verifies before
	// applying. 64 hex chars are required.
	SHA256 string `json:"sha256"`
	// Size is the package size in bytes, used for progress/space hints.
	Size int64 `json:"size"`
	// Headers are extra HTTP headers (e.g. Authorization) sent on download.
	Headers map[string]string `json:"headers,omitempty"`
}

// TargetSelector dynamically selects rollout targets (ADR-0006d §1). Any
// non-empty fields are AND-combined; it is resolved once at task creation and
// snapshotted into items, so later membership changes never move a rollout.
type TargetSelector struct {
	// ProfileID selects devices of one model.
	ProfileID string `json:"profile_id,omitempty"`
	// GroupID selects devices assigned to one group.
	GroupID string `json:"group_id,omitempty"`
	// CustomerID selects devices owned by one customer.
	CustomerID string `json:"customer_id,omitempty"`
}

// Empty reports whether no selector dimension is set.
func (s TargetSelector) Empty() bool {
	return s.ProfileID == "" && s.GroupID == "" && s.CustomerID == ""
}

// RolloutPolicy is the per-task canary/abort/watchdog configuration. Pointer
// fields distinguish "not set" (legacy single-batch behaviour) from an
// explicit value (ADR-0006d §5).
type RolloutPolicy struct {
	// BatchPercent opens this percentage of targets per batch; nil lets
	// BatchSize decide. 100 / nil-nil means one full batch.
	BatchPercent *int `json:"batch_percent,omitempty"`
	// BatchSize opens this many targets per batch.
	BatchSize *int `json:"batch_size,omitempty"`
	// BatchPauseSeconds is the observation delay between settled batches.
	BatchPauseSeconds int `json:"batch_pause_seconds,omitempty"`
	// FailureAbortPercent aborts when the failed share of settled devices
	// reaches this percentage; nil disables the threshold.
	FailureAbortPercent *int `json:"failure_abort_percent,omitempty"`
	// MaxConsecutiveFailures aborts after this many consecutive failures
	// within a batch; nil disables the threshold.
	MaxConsecutiveFailures *int `json:"max_consecutive_failures,omitempty"`
	// StallTimeoutSeconds fails an item with no progress for this long;
	// nil leaves items open indefinitely (legacy).
	StallTimeoutSeconds *int `json:"stall_timeout_seconds,omitempty"`
	// ConfirmTimeoutSeconds fails a rebooting item unconfirmed for this
	// long; nil leaves it open indefinitely (legacy).
	ConfirmTimeoutSeconds *int `json:"confirm_timeout_seconds,omitempty"`
}

// Request is the validated REST input for creating an OTA task.
type Request struct {
	// Devices is the explicit list of target sub-device UUIDs.
	Devices []string `json:"devices"`
	// Selector dynamically chooses targets when Devices is empty.
	Selector TargetSelector `json:"selector,omitempty"`
	// Package is the firmware descriptor for an external-URL task. It is
	// ignored when FirmwareID is set (a repository-managed task).
	Package Package `json:"package"`
	// FirmwareID selects a platform-hosted package from the firmware
	// repository. When set, the manager resolves version/sha/size from it and
	// sets PackageURL to the authenticated relative download path; the
	// external Package.URL is not used.
	FirmwareID string `json:"firmware_id,omitempty"`
	// SuccessPolicy selects how a device reaches succeeded. Empty defaults to
	// device_reported.
	SuccessPolicy string `json:"success_policy,omitempty"`
	// Policy configures batching/abort/watchdogs. The zero value reproduces
	// the legacy single-batch, immediate, no-timeout behaviour.
	Policy RolloutPolicy `json:"policy,omitempty"`
}

// Task is one rollout and its aggregate progress.
type Task struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"tenant_id"`
	CreatedBy       string            `json:"created_by,omitempty"`
	FirmwareID      string            `json:"firmware_id,omitempty"`
	FirmwareVersion string            `json:"firmware_version"`
	PackageURL      string            `json:"package_url"`
	PackageSHA256   string            `json:"package_sha256"`
	PackageSize     int64             `json:"package_size"`
	HTTPHeaders     map[string]string `json:"headers,omitempty"`
	State           string            `json:"state"`
	// SuccessPolicy selects how a device reaches the succeeded terminal
	// state (see Policy* constants).
	SuccessPolicy string `json:"success_policy"`
	// TargetMode is explicit (UUID list) or selector (resolved snapshot).
	TargetMode string `json:"target_mode"`
	// TargetSelector records the selector dimensions when TargetMode is
	// selector, kept for provenance after snapshotting.
	TargetSelector TargetSelector `json:"target_selector,omitempty"`
	// Policy is the batching/abort/watchdog configuration.
	Policy         RolloutPolicy `json:"policy"`
	BatchNo        int           `json:"batch_no"`
	NextBatchAt    *time.Time    `json:"next_batch_at,omitempty"`
	AbortReason    string        `json:"abort_reason,omitempty"`
	TotalCount     int           `json:"total_count"`
	SucceededCount int           `json:"succeeded_count"`
	FailedCount    int           `json:"failed_count"`
	CanceledCount  int           `json:"canceled_count"`
	SkippedCount   int           `json:"skipped_count"`
	Error          string        `json:"error,omitempty"`
	CreatedAt      time.Time     `json:"created_at"`
	StartedAt      *time.Time    `json:"started_at,omitempty"`
	FinishedAt     *time.Time    `json:"finished_at,omitempty"`
}

// Item is the per-device member of an OTA task. State is the latest ordered
// phase; ResultCode is the orthogonal reason axis (ResultNone unless the item
// failed/was canceled or skipped).
type Item struct {
	ID            string `json:"id"`
	TaskID        string `json:"task_id"`
	TenantID      string `json:"tenant_id"`
	GatewayID     string `json:"gateway_id"`
	DeviceID      string `json:"device_id"`
	State         string `json:"state"`
	ResultCode    string `json:"result_code,omitempty"`
	ProgressPct   int    `json:"progress_pct"`
	BytesReceived int64  `json:"bytes_received"`
	// BatchNo is the rollout batch that opened this item (0 = single legacy).
	BatchNo    int        `json:"batch_no"`
	Message    string     `json:"message,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// DeviceItem is one rollout row from a single device's point of view. It
// joins the per-device item with the aggregate task so a device detail view
// can show the target version and task state without a second request.
type DeviceItem struct {
	Item
	// FirmwareVersion is the target version carried by the parent task.
	FirmwareVersion string `json:"firmware_version"`
	// TaskState is the aggregate rollout state of the parent task.
	TaskState string `json:"task_state"`
}

// TimelineEvent is one append-only row of a device's OTA phase history.
type TimelineEvent struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	TenantID      string    `json:"tenant_id"`
	DeviceID      string    `json:"device_id"`
	GatewayID     string    `json:"gateway_id"`
	Seq           int64     `json:"seq"`
	Phase         string    `json:"phase"`
	ResultCode    string    `json:"result_code,omitempty"`
	ProgressPct   int       `json:"progress_pct"`
	BytesReceived int64     `json:"bytes_received,omitempty"`
	Message       string    `json:"message,omitempty"`
	Source        string    `json:"source"`
	CreatedAt     time.Time `json:"created_at"`
}

// DispatchFrame is the Core NATS request from the manager to the ingress node
// owning a gateway connection, and the JSON written to v1/gateway/ota/task for
// that one client. One frame covers every device of the task on the gateway.
type DispatchFrame struct {
	TaskID    string            `json:"task_id"`
	TenantID  string            `json:"tenant_id"`
	GatewayID string            `json:"gateway_id"`
	Devices   []string          `json:"devices"`
	Version   string            `json:"version"`
	URL       string            `json:"url"`
	SHA256    string            `json:"sha256"`
	Size      int64             `json:"size"`
	Headers   map[string]string `json:"headers,omitempty"`
	// SuccessPolicy tells the edge whether it must send a post-reboot
	// 'confirmed' frame (with the running version) in addition to 'updated'.
	SuccessPolicy string `json:"success_policy,omitempty"`
}

// ProgressFrame is the uplink body on v1/gateway/ota/progress, reported by the
// gateway per device. State carries both progress and terminal outcomes.
type ProgressFrame struct {
	TaskID      string `json:"task_id"`
	Device      string `json:"device"`
	State       string `json:"state"`
	ProgressPct int    `json:"progress_pct"`
	Message     string `json:"message,omitempty"`
	// ResultCode is the structured failure/reason axis (Result* constants).
	ResultCode string `json:"result_code,omitempty"`
	// Bytes is the number of package bytes received so far.
	Bytes int64 `json:"bytes,omitempty"`
	// FirmwareVersion carries the running version on the post-reboot
	// 'confirmed' frame; matched against the task target version.
	FirmwareVersion string `json:"firmware_version,omitempty"`
}

// Event is the per-device progress the ingress relay publishes on Core NATS
// and the manager consumes.
type Event struct {
	TaskID      string `json:"task_id"`
	TenantID    string `json:"tenant_id"`
	GatewayID   string `json:"gateway_id"`
	DeviceID    string `json:"device_id"`
	State       string `json:"state"`
	ResultCode  string `json:"result_code,omitempty"`
	ProgressPct int    `json:"progress_pct"`
	Bytes       int64  `json:"bytes"`
	// FirmwareVersion carries the running version on a post-reboot confirmed
	// frame under the confirmed_after_reboot policy.
	FirmwareVersion string `json:"firmware_version,omitempty"`
	Message         string `json:"message"`
	// Source records who produced the event (Source* constants).
	Source string `json:"source,omitempty"`
}

// ProgressResult is the outcome of applying one device frame: the refreshed
// aggregate, the updated item and any timeline rows appended (only on a phase
// boundary or a terminal transition).
type ProgressResult struct {
	Task    Task
	Item    Item
	Events  []TimelineEvent
	Changed bool
}

// TaskEvent is the progress frame published over Core NATS for WebSocket
// fan-out. Type is "task", "item" or "event".
type TaskEvent struct {
	Type  string         `json:"type"`
	Task  Task           `json:"task"`
	Item  *Item          `json:"item,omitempty"`
	Event *TimelineEvent `json:"event,omitempty"`
}

const (
	eventTypeTask  = "task"
	eventTypeItem  = "item"
	eventTypeEvent = "event"
)

// DispatchSubjectPrefix is nl.ota.dispatch.<ingress-id>.
const DispatchSubjectPrefix = "nl.ota.dispatch."

// EventsSubjectPrefix is nl.ota.events.<tenant>.<gateway>.
const EventsSubjectPrefix = "nl.ota.events."

// TaskSubjectPrefix is nl.ota.task.<tenant>: WebSocket progress frames.
const TaskSubjectPrefix = "nl.ota.task."

// EventsWildcard subscribes the manager to every per-device progress event.
const EventsWildcard = EventsSubjectPrefix + "*.*"

// dispatchSubject addresses one ingress node.
func dispatchSubject(ingressID string) string {
	return DispatchSubjectPrefix + ingressID
}

// taskSubject addresses one tenant's progress channel.
func taskSubject(tenantID string) string {
	return TaskSubjectPrefix + tenantID
}

// ValidSHA256 reports whether s is 64 lowercase (or uppercase) hex chars.
func ValidSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
