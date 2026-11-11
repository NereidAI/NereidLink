// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// quarantine_contract_test.go locks the JSON body contract of the device
// quarantine entry and release endpoints. A frontend that posts only a
// free-text "reason" once made every quarantine attempt fail with HTTP 400
// ("quarantine reason code is required"); these tests fail if the accepted
// field names drift away from reason_code/note again.

package gatewayapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nereidai/nereidlink/internal/registry"
)

// fakeQuarantineAdmin implements just the quarantine methods of
// OwnershipAdmin; the embedded nil interface satisfies the remaining
// methods, which these tests never call.
type fakeQuarantineAdmin struct {
	OwnershipAdmin
	gotCode string
	gotNote string
}

// QuarantineDevice records the decoded code and note for assertions.
func (f *fakeQuarantineAdmin) QuarantineDevice(_ context.Context, _, _, reasonCode, note, _ string) (registry.DeviceDTO, error) {
	f.gotCode = reasonCode
	f.gotNote = note
	return registry.DeviceDTO{State: registry.StateActive}, nil
}

// ReleaseFromQuarantine records the decoded release note for assertions.
func (f *fakeQuarantineAdmin) ReleaseFromQuarantine(_ context.Context, _, _, note, _ string) (registry.DeviceDTO, error) {
	f.gotNote = note
	return registry.DeviceDTO{State: registry.StateActive}, nil
}

// TestHandleQuarantineAcceptsReasonCode verifies the console payload
// {"reason_code": ..., "note": ...} reaches the admin service and returns
// 200 instead of 400.
func TestHandleQuarantineAcceptsReasonCode(t *testing.T) {
	admin := &fakeQuarantineAdmin{}
	hub := &Hub{}
	hub.SetOwnershipAdmin(admin)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/devices/d1/quarantine",
		strings.NewReader(`{"reason_code":"firmware-anomaly","note":"spikes"}`))
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("device", "d1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hub.handleQuarantine(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if admin.gotCode != "firmware-anomaly" || admin.gotNote != "spikes" {
		t.Fatalf("decoded (%q, %q), want (firmware-anomaly, spikes)", admin.gotCode, admin.gotNote)
	}
}

// TestHandleQuarantineReleaseAcceptsNote verifies the release body {"note":
// ...} decodes and reaches the release service.
func TestHandleQuarantineReleaseAcceptsNote(t *testing.T) {
	admin := &fakeQuarantineAdmin{}
	hub := &Hub{}
	hub.SetOwnershipAdmin(admin)

	req := httptest.NewRequest(http.MethodPost, "/v1/tenants/t1/devices/d1/quarantine/release",
		strings.NewReader(`{"note":"incident closed"}`))
	req.SetPathValue("tenant", "t1")
	req.SetPathValue("device", "d1")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	hub.handleQuarantineRelease(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if admin.gotNote != "incident closed" {
		t.Fatalf("decoded note %q, want %q", admin.gotNote, "incident closed")
	}
}
