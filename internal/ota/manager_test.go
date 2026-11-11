// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ota

import (
	"strings"
	"testing"
)

const validSHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func goodReq(devices ...string) Request {
	return Request{
		Devices: devices,
		Package: Package{
			Version: "fw-1.2.3",
			URL:     "https://files.example.com/fw-1.2.3.bin",
			SHA256:  validSHA,
			Size:    4096,
		},
	}
}

// validateRequest mirrors Create's validation for an external-URL request.
func validateRequest(req Request, tune Tuning) error {
	devices := dedupe(req.Devices)
	if err := validateDevices(devices, tune); err != nil {
		return err
	}
	return validateExternalPackage(req.Package)
}

func TestValidateRequest(t *testing.T) {
	dev := "00000000-0000-0000-0000-000000000001"
	tune := DefaultTuning()

	if err := validateRequest(goodReq(dev), tune); err != nil {
		t.Fatalf("good request must pass: %v", err)
	}

	// Empty devices.
	bad := goodReq(dev)
	bad.Devices = nil
	if err := validateRequest(bad, tune); err == nil {
		t.Fatal("empty devices must fail")
	}

	// Bad device id.
	if err := validateRequest(goodReq("not-a-uuid"), tune); err == nil {
		t.Fatal("invalid device id must fail")
	}

	// Missing version.
	bad = goodReq(dev)
	bad.Package.Version = "  "
	if err := validateRequest(bad, tune); err == nil {
		t.Fatal("blank version must fail")
	}

	// Non-http(s) URL.
	for _, u := range []string{"ftp://x/f.bin", "not a url", "/relative/path", ""} {
		b := goodReq(dev)
		b.Package.URL = u
		if err := validateRequest(b, tune); err == nil {
			t.Fatalf("url %q must fail", u)
		}
	}

	// Bad sha256.
	for _, s := range []string{"", "abc", strings.Repeat("z", 64), validSHA[:63]} {
		b := goodReq(dev)
		b.Package.SHA256 = s
		if err := validateRequest(b, tune); err == nil {
			t.Fatalf("sha %q must fail", s)
		}
	}

	// Negative size.
	b := goodReq(dev)
	b.Package.Size = -1
	if err := validateRequest(b, tune); err == nil {
		t.Fatal("negative size must fail")
	}

	// Device cap.
	small := Tuning{MaxDevices: 1}
	if err := validateRequest(goodReq(
		"00000000-0000-0000-0000-000000000001",
		"00000000-0000-0000-0000-000000000002"), small); err == nil {
		t.Fatal("over-cap request must fail")
	}
}

func TestDedupe(t *testing.T) {
	got := dedupe([]string{"a", "b", "a", "c", "b"})
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestValidSHA256(t *testing.T) {
	if !ValidSHA256(validSHA) {
		t.Fatal("64 hex chars must be valid")
	}
	if !ValidSHA256(strings.ToUpper(validSHA)) {
		t.Fatal("uppercase hex must be valid")
	}
	if ValidSHA256(validSHA[:63]) || ValidSHA256("") {
		t.Fatal("short/empty digest must be invalid")
	}
}

func TestResolveFramePolicies(t *testing.T) {
	taskReported := Task{SuccessPolicy: PolicyDeviceReported, FirmwareVersion: "2.0.0"}
	taskConfirmed := Task{SuccessPolicy: PolicyConfirmedAfterReboot, FirmwareVersion: "2.0.0"}
	cur := Item{State: ItemStateUpdating}

	// device_reported: 'updated' finalizes immediately.
	state, _, term := resolveFrame(taskReported, cur, Event{State: ItemStateUpdated})
	if state != ItemStateSucceeded || !term {
		t.Fatalf("device_reported updated must succeed/terminal, got %s/%v", state, term)
	}

	// confirmed_after_reboot: 'updated' stays open.
	state, _, term = resolveFrame(taskConfirmed, cur, Event{State: ItemStateUpdated})
	if state != ItemStateUpdated || term {
		t.Fatalf("confirm-policy updated must stay open, got %s/%v", state, term)
	}

	// Matching confirmed version finalizes.
	state, _, term = resolveFrame(taskConfirmed, cur,
		Event{State: ItemStateConfirmed, FirmwareVersion: "2.0.0"})
	if state != ItemStateSucceeded || !term {
		t.Fatalf("matching confirm must succeed, got %s/%v", state, term)
	}

	// Mismatched confirmed version is a version_mismatch failure.
	var code string
	state, code, term = resolveFrame(taskConfirmed, cur,
		Event{State: ItemStateConfirmed, FirmwareVersion: "1.0.0"})
	if state != ItemStateFailed || code != ResultVersionMismatch || !term {
		t.Fatalf("mismatch must fail/version_mismatch, got %s/%s/%v", state, code, term)
	}

	// A bare failure without a code defaults to unknown.
	_, code, term = resolveFrame(taskReported, cur, Event{State: ItemStateFailed})
	if !term || code != ResultUnknown {
		t.Fatalf("bare failure must be terminal/unknown, got %s/%v", code, term)
	}
}
