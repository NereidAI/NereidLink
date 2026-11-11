// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// batch_test.go covers non-database batch helpers.

package command

import (
	"testing"
	"time"
)

// TestItemOutcome verifies accepted and failed status classification.
func TestItemOutcome(t *testing.T) {
	accepted := []string{StatusSucceeded, StatusDelivered, StatusQueued}
	for _, status := range accepted {
		if state, _ := itemOutcome(Ack{Status: status}); state != ItemStateSucceeded {
			t.Fatalf("%s classified as %s", status, state)
		}
	}
	failures := []string{StatusFailed, StatusTimeout, StatusNotConnected, StatusExpired, StatusModuleOffline}
	for _, status := range failures {
		state, failure := itemOutcome(Ack{Status: status})
		if state != ItemStateFailed || failure != status {
			t.Fatalf("%s classified as %s (%s)", status, state, failure)
		}
	}
}

// TestUniqueDevices verifies duplicate removal.
func TestUniqueDevices(t *testing.T) {
	result := uniqueDevices([]string{"a", "b", "a", "c", "b"})
	if len(result) != 3 {
		t.Fatalf("result = %v", result)
	}
}

// TestDefaultLimits verifies omitted safety limits are replaced rather than
// silently disabling protection.
func TestDefaultLimits(t *testing.T) {
	manager := NewBatchManager(nil, nil, nil, 0, 0)
	if manager.maxBody != DefaultMaxBodyBytes || manager.maxTargets != DefaultMaxBatchTargets {
		t.Fatalf("manager limits = %d/%d", manager.maxBody, manager.maxTargets)
	}

	api := NewAPI(nil, nil, time.Second, 0)
	if api.maxBodyBytes != DefaultMaxBodyBytes {
		t.Fatalf("api limit = %d", api.maxBodyBytes)
	}
}
