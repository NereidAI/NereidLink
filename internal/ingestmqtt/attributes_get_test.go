// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// attributes_get_test.go verifies on-demand attribute pulls resolve the
// authenticated identity, default/validate scopes in the fetcher, and gate
// gateway-proxied fetches on negotiation plus an attached sub-device.
package ingestmqtt

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// fakeAttributeFetcher records FetchAttributes calls.
type fakeAttributeFetcher struct {
	calls   []fetchedAttrs
	err     error
	payload map[string]map[string]json.RawMessage
}

type fetchedAttrs struct {
	tenantID string
	deviceID string
	scopes   []string
}

func (f *fakeAttributeFetcher) FetchAttributes(_ context.Context, tenantID, deviceID string, scopes []string) (map[string]map[string]json.RawMessage, error) {
	f.calls = append(f.calls, fetchedAttrs{tenantID, deviceID, scopes})
	if f.err != nil {
		return nil, f.err
	}
	return f.payload, nil
}

func TestHandleDeviceAttributesGetFetchesIdentity(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	fetcher := &fakeAttributeFetcher{}
	hook.SetAttributeFetcher(fetcher)

	// Empty scopes defaults inside the registry; the hook forwards what was
	// sent (nil) so the direct device identity must be resolved.
	hook.handleDeviceAttributesGet("dev", []byte(`{}`))
	hook.handleDeviceAttributesGet("dev", []byte(`{"scopes":["SHARED_SCOPE"]}`))

	if len(fetcher.calls) != 2 {
		t.Fatalf("want 2 fetch calls, got %d", len(fetcher.calls))
	}
	for i, wantScopes := range [][]string{nil, {"SHARED_SCOPE"}} {
		got := fetcher.calls[i]
		if got.tenantID != "t" || got.deviceID != "d" {
			t.Fatalf("call %d resolved wrong identity: %+v", i, got)
		}
		if len(got.scopes) != len(wantScopes) {
			t.Fatalf("call %d scopes=%v want %v", i, got.scopes, wantScopes)
		}
	}
}

func TestHandleDeviceAttributesGetRejectsMalformedAndUnknownSession(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	fetcher := &fakeAttributeFetcher{}
	hook.SetAttributeFetcher(fetcher)

	hook.handleDeviceAttributesGet("dev", []byte(`{bad`))
	hook.handleDeviceAttributesGet("ghost", []byte(`{}`))

	if len(fetcher.calls) != 0 {
		t.Fatalf("malformed/unknown must not fetch, got %d calls", len(fetcher.calls))
	}
}

func TestHandleGatewayAttributesGetGatesSubdevice(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	fetcher := &fakeAttributeFetcher{}
	hook.SetAttributeFetcher(fetcher)

	// Attached sub-device on a negotiated gateway fetches with the sub id.
	hook.handleGatewayAttributesGet("gw",
		[]byte(`{"device":"sub1","scopes":["SERVER_SCOPE"]}`))
	// Unattached device is dropped before fetching.
	hook.handleGatewayAttributesGet("gw",
		[]byte(`{"device":"sub2","scopes":["SERVER_SCOPE"]}`))
	// Plain device identity may not proxy.
	hook.handleGatewayAttributesGet("dev",
		[]byte(`{"device":"sub1"}`))
	// Gateway that has not completed hello is rejected.
	hook.handleGatewayAttributesGet("gw-raw",
		[]byte(`{"device":"sub1"}`))

	if len(fetcher.calls) != 1 {
		t.Fatalf("want exactly 1 authorized fetch, got %d: %+v", len(fetcher.calls), fetcher.calls)
	}
	got := fetcher.calls[0]
	if got.tenantID != "t" || got.deviceID != "sub1" ||
		len(got.scopes) != 1 || got.scopes[0] != "SERVER_SCOPE" {
		t.Fatalf("unexpected gateway fetch: %+v", got)
	}
}

func TestHandleGatewayAttributesGetMalformedNoFetch(t *testing.T) {
	hook, _ := newAttributeTestHook(t)
	fetcher := &fakeAttributeFetcher{err: errors.New("boom")}
	hook.SetAttributeFetcher(fetcher)

	hook.handleGatewayAttributesGet("gw", []byte(`{not json`))
	if len(fetcher.calls) != 0 {
		t.Fatal("malformed gateway fetch must not reach the fetcher")
	}
}
