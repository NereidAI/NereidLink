// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package ingestmqtt

import (
	"context"
	"testing"

	"github.com/nereidai/nereidlink/internal/deviceevent"
)

type fakeEventIngester struct {
	frames []deviceevent.Frame
	tenant string
	device string
	err    error
}

func (f *fakeEventIngester) Ingest(_ context.Context, tenantID, deviceID string, frames []deviceevent.Frame) error {
	f.tenant, f.device, f.frames = tenantID, deviceID, append(f.frames, frames...)
	return f.err
}

func TestHandleDeviceEventsSingleAndArray(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	ingester := &fakeEventIngester{}
	hook.SetEventIngester(ingester)

	// Single object.
	hook.handleDeviceEvents("dev",
		[]byte(`{"event_type":"door_opened","severity":"warning","payload":{"b":7}}`))
	// Array of two (one default severity).
	hook.handleDeviceEvents("dev",
		[]byte(`[{"event_type":"a"},{"event_type":"b","ts_ms":1700000000000}]`))

	if ingester.tenant != "t" || ingester.device != "d" {
		t.Fatalf("identity wrong: tenant=%q device=%q", ingester.tenant, ingester.device)
	}
	if len(ingester.frames) != 3 {
		t.Fatalf("expected 3 frames (1+2), got %d", len(ingester.frames))
	}
	if ingester.frames[0].EventType != "door_opened" || ingester.frames[0].Severity != "warning" {
		t.Fatalf("first frame mismatch: %+v", ingester.frames[0])
	}
	if ingester.frames[1].EventType != "a" || ingester.frames[2].TSMS != 1700000000000 {
		t.Fatalf("array frames mismatch: %+v %+v", ingester.frames[1], ingester.frames[2])
	}
}

func TestHandleDeviceEventsRejectsMalformed(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	ingester := &fakeEventIngester{}
	hook.SetEventIngester(ingester)

	hook.handleDeviceEvents("dev", []byte(`{not json`))
	hook.handleDeviceEvents("dev", []byte(`[1,2]`))
	if len(ingester.frames) != 0 {
		t.Fatalf("malformed frames must not reach the ingester, got %d", len(ingester.frames))
	}
}

func TestHandleDeviceEventsEmptyArrayIsNoOp(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	ingester := &fakeEventIngester{}
	hook.SetEventIngester(ingester)
	hook.handleDeviceEvents("dev", []byte(`[]`))
	if len(ingester.frames) != 0 {
		t.Fatalf("empty array should not call ingester, got %d", len(ingester.frames))
	}
}
