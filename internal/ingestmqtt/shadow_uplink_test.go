// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// shadow_uplink_test.go verifies gateway-proxied sub-device shadow reports:
// per-entry isolation, attachment gating, object-only state, and an audit
// trail for every rejected entry.
package ingestmqtt

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/nereidai/nereidlink/internal/audit"
	"github.com/nereidai/nereidlink/internal/mqttapi"
	"github.com/nereidai/nereidlink/internal/shadow"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// savedShadowReport is one captured ShadowReporter invocation.
type savedShadowReport struct {
	deviceID string
	patch    shadow.ReportedPatch
}

type fakeShadowReporter struct {
	calls []savedShadowReport
	err   error
}

func (r *fakeShadowReporter) UpdateReported(_ context.Context, deviceID string, patch shadow.ReportedPatch) (shadow.State, error) {
	r.calls = append(r.calls, savedShadowReport{deviceID, patch})
	return shadow.State{}, r.err
}

type fakeShadowAuditor struct {
	inputs []audit.Input
}

func (a *fakeShadowAuditor) Append(_ context.Context, input audit.Input) (audit.Event, error) {
	a.inputs = append(a.inputs, input)
	return audit.Event{}, nil
}

// newShadowTestHook builds a publish hook with one direct device, one
// negotiated gateway that attached sub1, and one pre-hello gateway.
func newShadowTestHook(t *testing.T) (*PublishHook, *fakeShadowReporter, *fakeShadowAuditor) {
	t.Helper()
	hooks := NewAuthHook(nil, nil, "ing")
	hooks.connections["dev"] = connection{identity: sdktelemetry.DeviceIdentity{
		TenantID: "t", DeviceID: "d",
	}}
	hooks.connections["gw"] = connection{
		identity: sdktelemetry.DeviceIdentity{
			TenantID:     "t",
			DeviceID:     "g",
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
		negotiated: true,
	}
	hooks.connections["gw-raw"] = connection{
		identity: sdktelemetry.DeviceIdentity{
			TenantID:     "t",
			DeviceID:     "g2",
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
		negotiated: false,
	}
	hooks.recordSubdevice("gw", "t", "sub1")

	hook := NewPublishHook(nil, hooks, nil, nil, nil)
	hook.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	reporter := &fakeShadowReporter{}
	auditor := &fakeShadowAuditor{}
	hook.SetShadowReporter(reporter)
	hook.SetShadowAuditor(auditor)
	return hook, reporter, auditor
}

func TestHandleGatewayShadowReportPersistsAttached(t *testing.T) {
	hook, reporter, auditor := newShadowTestHook(t)
	hook.handleGatewayShadowReport("gw", []byte(
		`{"reports":[{"device":"sub1","state":{"led":"on","brightness":7}}]}`))

	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.calls))
	}
	got := reporter.calls[0]
	if got.deviceID != "sub1" {
		t.Fatalf("unexpected device: %q", got.deviceID)
	}
	if got.patch.State["led"] != "on" || got.patch.State["brightness"] != float64(7) {
		t.Fatalf("unexpected state: %+v", got.patch.State)
	}
	if len(auditor.inputs) != 0 {
		t.Fatalf("valid entry must not be audited, got %d", len(auditor.inputs))
	}
}

func TestHandleGatewayShadowReportIsolatesBadEntries(t *testing.T) {
	hook, reporter, auditor := newShadowTestHook(t)
	// sub2 is not attached, one entry has non-object state, one an empty
	// device, and the final entry for sub1 is valid and must still persist.
	hook.handleGatewayShadowReport("gw", []byte(`{"reports":[
		{"device":"sub2","state":{"a":1}},
		{"device":"sub1","state":5},
		{"device":"","state":{"a":1}},
		{"device":"sub1","state":{}} ,
		{"device":"sub1","state":{"ok":true}}
	]}`))

	if len(reporter.calls) != 1 || reporter.calls[0].deviceID != "sub1" {
		t.Fatalf("only the single valid entry persists, got %+v", reporter.calls)
	}
	if got := reporter.calls[0].patch.State["ok"]; got != true {
		t.Fatalf("unexpected surviving state: %+v", reporter.calls[0].patch.State)
	}
	// Four rejects: unattached, non-object state, empty device, empty object.
	if len(auditor.inputs) != 4 {
		t.Fatalf("expected 4 audited rejects, got %d", len(auditor.inputs))
	}
	for _, in := range auditor.inputs {
		if in.Action != "shadow-report-rejected" || in.ResourceType != "device_shadow" {
			t.Fatalf("unexpected audit record: %+v", in)
		}
	}
}

func TestHandleGatewayShadowReportDropsUnparseableFrame(t *testing.T) {
	hook, reporter, auditor := newShadowTestHook(t)
	hook.handleGatewayShadowReport("gw", []byte(`{not json`))
	if len(reporter.calls) != 0 || len(auditor.inputs) != 0 {
		t.Fatalf("unparseable frame must be wholly dropped, calls=%d audits=%d",
			len(reporter.calls), len(auditor.inputs))
	}
}

func TestHandleGatewayShadowReportGatesIdentity(t *testing.T) {
	hook, reporter, auditor := newShadowTestHook(t)
	// A direct device may not publish gateway frames.
	hook.handleGatewayShadowReport("dev", []byte(
		`{"reports":[{"device":"sub1","state":{"a":1}}]}`))
	// A pre-hello gateway is rejected.
	hook.handleGatewayShadowReport("gw-raw", []byte(
		`{"reports":[{"device":"sub1","state":{"a":1}}]}`))
	if len(reporter.calls) != 0 || len(auditor.inputs) != 0 {
		t.Fatalf("gated frames must not persist or audit, calls=%d audits=%d",
			len(reporter.calls), len(auditor.inputs))
	}
}

func TestHandleGatewayShadowReportPassesAppliedVersion(t *testing.T) {
	hook, reporter, _ := newShadowTestHook(t)
	hook.handleGatewayShadowReport("gw", []byte(
		`{"reports":[{"device":"sub1","state":{"led":"off"},"version":9}]}`))
	if len(reporter.calls) != 1 {
		t.Fatalf("expected 1 report, got %d", len(reporter.calls))
	}
	v := reporter.calls[0].patch.Version
	if v == nil || *v != 9 {
		t.Fatalf("expected applied version 9, got %v", v)
	}
}
