// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// firmware_wiring.go adapts the firmware repository service to the narrow
// ota.FirmwareResolver port, keeping internal/ota free of an import on
// internal/firmware.

package main

import (
	"context"

	"github.com/nereidai/nereidlink/internal/firmware"
	"github.com/nereidai/nereidlink/internal/ota"
)

// firmwareResolver adapts *firmware.Service to ota.FirmwareResolver.
type firmwareResolver struct {
	svc *firmware.Service
}

// ResolveFirmware returns the integrity metadata the OTA manager embeds in a
// platform-hosted task.
func (r firmwareResolver) ResolveFirmware(ctx context.Context, tenantID, packageID string) (ota.FirmwareMeta, error) {
	pkg, err := r.svc.Get(ctx, tenantID, packageID)
	if err != nil {
		return ota.FirmwareMeta{}, err
	}
	return ota.FirmwareMeta{
		Version:   pkg.Version,
		SHA256:    pkg.SHA256,
		Size:      pkg.SizeBytes,
		ProfileID: pkg.ProfileID,
	}, nil
}
