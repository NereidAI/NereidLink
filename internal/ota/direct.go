// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// direct.go serves directly connected devices (gateway_id IS NULL). Unlike
// gateway-attached sub-devices, which get one pushed frame via their gateway,
// direct devices discover pending firmware work by polling an authenticated
// device endpoint and then download the bytes through the authorized NL
// relay. It also resolves the download authorization shared by both direct
// devices and gateways.

package ota

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNotHosted means the task uses an external package URL, so there is no
// platform object to relay; the edge must fetch package_url itself.
var ErrNotHosted = errors.New("ota task is not platform-hosted")

// ErrNotTarget means the caller is neither the target device nor the gateway
// responsible for a target of the task.
var ErrNotTarget = errors.New("caller is not an ota task target")

// ErrNotActive means the task is terminal/canceled and no download is allowed.
var ErrNotActive = errors.New("ota task is not active")

// DirectAssignment is one firmware job a direct device should perform.
type DirectAssignment struct {
	TaskID  string `json:"task_id"`
	Version string `json:"version"`
	// URL is the relative authenticated download path on this server.
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
	State  string `json:"state"`
}

// DevicePackagePath is the relative HTTPS path a direct device/gateway uses to
// fetch a platform-hosted package, authenticated with its own credential.
func DevicePackagePath(taskID string) string {
	return "/v1/me/ota/" + taskID + "/package"
}

// OpenAssignments returns the open, repository-hosted firmware jobs that
// target one directly connected device, newest first.
func (s *Store) OpenAssignments(ctx context.Context, tenantID, deviceID string) ([]DirectAssignment, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT t.id::text, t.firmware_version, t.package_sha256, t.package_size, i.state
		 FROM ota_task_item i
		 JOIN ota_task t ON t.id = i.task_id
		 WHERE i.tenant_id=$1 AND i.device_id=$2
		   AND i.gateway_id IS NULL
		   AND t.firmware_id IS NOT NULL
		   AND t.state IN ('pending','running')
		   AND i.batch_no >= 0
		   AND i.state IN ('pending','initiated','downloading','downloaded',
		                   'verified','updating','updated','rebooting')
		 ORDER BY t.created_at DESC`,
		tenantID, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list device ota assignments: %w", err)
	}
	defer rows.Close()
	out := []DirectAssignment{}
	for rows.Next() {
		var a DirectAssignment
		if err := rows.Scan(&a.TaskID, &a.Version, &a.SHA256, &a.Size, &a.State); err != nil {
			return nil, err
		}
		a.URL = DevicePackagePath(a.TaskID)
		out = append(out, a)
	}
	return out, rows.Err()
}

// DownloadGrant is the resolved, authorized platform object for one download.
type DownloadGrant struct {
	// ObjectKey is the blob key the HTTP layer streams.
	ObjectKey string
	Size      int64
	SHA256    string
}

// AuthorizeDownload validates that the caller (a direct device or a gateway)
// may fetch the task package and returns the backing object. Direct devices
// match an item by device_id with gateway_id NULL; gateways match any item by
// gateway_id.
func (s *Store) AuthorizeDownload(ctx context.Context, tenantID, callerID, taskID string) (DownloadGrant, error) {
	var (
		state      string
		firmwareID *string
	)
	err := s.pool.QueryRow(ctx,
		`SELECT state, firmware_id::text FROM ota_task
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, taskID).Scan(&state, &firmwareID)
	if errors.Is(err, pgx.ErrNoRows) {
		return DownloadGrant{}, ErrNotFound
	}
	if err != nil {
		return DownloadGrant{}, fmt.Errorf("load ota task for download: %w", err)
	}
	if state != TaskStatePending && state != TaskStateRunning {
		return DownloadGrant{}, ErrNotActive
	}
	if firmwareID == nil || *firmwareID == "" {
		return DownloadGrant{}, ErrNotHosted
	}

	var isTarget bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS(
		   SELECT 1 FROM ota_task_item
		   WHERE task_id=$1 AND tenant_id=$2
		     AND state NOT IN ('skipped','canceled','failed','succeeded')
		     AND (device_id=$3 OR gateway_id=$3)
		 )`,
		taskID, tenantID, callerID).Scan(&isTarget); err != nil {
		return DownloadGrant{}, fmt.Errorf("check ota target: %w", err)
	}
	if !isTarget {
		return DownloadGrant{}, ErrNotTarget
	}

	var grant DownloadGrant
	if err := s.pool.QueryRow(ctx,
		`SELECT object_key, size_bytes, sha256 FROM firmware_package
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, *firmwareID).Scan(&grant.ObjectKey, &grant.Size, &grant.SHA256); err != nil {
		return DownloadGrant{}, fmt.Errorf("load firmware object: %w", err)
	}
	return grant, nil
}

// SetDeviceFirmwareVersion records the firmware version a device reports or
// has just been rolled out to.
func (s *Store) SetDeviceFirmwareVersion(ctx context.Context, tenantID, deviceID, version string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE device SET firmware_version=$3, updated_at=now()
		 WHERE tenant_id=$1 AND id=$2`,
		tenantID, deviceID, version)
	if err != nil {
		return fmt.Errorf("set device firmware version: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
