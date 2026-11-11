// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientCheckContract(t *testing.T) {
	var gotAuth, gotContract string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != versionPath {
			t.Errorf("path = %q, want %q", r.URL.Path, versionPath)
		}
		gotAuth = r.Header.Get("Authorization")
		gotContract = r.Header.Get(headerContract)
		writeJSON(t, w, map[string]any{
			"service": "face-service", "version": "v0.0.1",
			"apiVersion": ContractV1,
		})
	}))
	defer server.Close()

	client := NewHTTPClient(Endpoint{
		BaseURL: server.URL, ContractVersion: ContractV1, AuthToken: "secret",
	})
	if err := client.CheckContract(context.Background()); err != nil {
		t.Fatalf("CheckContract: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContract != ContractV1 {
		t.Fatalf("contract header = %q", gotContract)
	}
}

func TestHTTPClientContractMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, map[string]any{"apiVersion": "companion-subscription/v9"})
	}))
	defer server.Close()

	client := NewHTTPClient(Endpoint{BaseURL: server.URL, ContractVersion: ContractV1})
	err := client.CheckContract(context.Background())
	if !errors.Is(err, ErrContractMismatch) {
		t.Fatalf("err = %v, want ErrContractMismatch", err)
	}
}

func TestHTTPClientPutSubscriptionsWire(t *testing.T) {
	var received wirePutRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != subsPath {
			t.Errorf("request = %s %q", r.Method, r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &received); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		items := make([]map[string]any, 0, len(received.Subscriptions))
		for _, sub := range received.Subscriptions {
			items = append(items, map[string]any{
				"id": sub.ID, "revision": 1, "status": StatusApplied, "reason": "",
			})
		}
		writeJSON(t, w, map[string]any{"items": items})
	}))
	defer server.Close()

	client := NewHTTPClient(Endpoint{BaseURL: server.URL, ContractVersion: ContractV1})
	readback, err := client.PutSubscriptions(context.Background(), []DesiredRow{
		{
			RowID: "companion:g:d:s1", SourceDevice: "s1", Camera: "", // blank → source device
			PipelineID: "pipe-1", ConclusionDeviceID: "d", Enabled: true,
		},
		{
			RowID: "companion:g:d:s2", SourceDevice: "s2", Camera: "front door",
			PipelineID: "pipe-1", ConclusionDeviceID: "d", Enabled: false,
		},
	})
	if err != nil {
		t.Fatalf("PutSubscriptions: %v", err)
	}
	if len(received.Subscriptions) != 2 {
		t.Fatalf("sent %d rows", len(received.Subscriptions))
	}
	first := received.Subscriptions[0]
	if first.Camera != "s1" {
		t.Fatalf("blank camera not defaulted to source device: %q", first.Camera)
	}
	if first.ID != "companion:g:d:s1" || first.PipelineID != "pipe-1" ||
		first.ConclusionDeviceID != "d" || !first.Enabled {
		t.Fatalf("wire row mismatch: %+v", first)
	}
	if len(readback) != 2 || readback[0].ID != "companion:g:d:s1" ||
		readback[0].Status != StatusApplied || readback[0].Revision != 1 {
		t.Fatalf("read-back mismatch: %+v", readback)
	}
}

func TestHTTPClientEmptySet(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body wirePutRequest
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if body.Subscriptions == nil {
			t.Fatal("empty set must serialize as [], not null")
		}
		writeJSON(t, w, map[string]any{"items": []any{}})
	}))
	defer server.Close()

	client := NewHTTPClient(Endpoint{BaseURL: server.URL, ContractVersion: ContractV1})
	readback, err := client.PutSubscriptions(context.Background(), nil)
	if err != nil {
		t.Fatalf("PutSubscriptions empty: %v", err)
	}
	if len(readback) != 0 {
		t.Fatalf("read-back = %d rows", len(readback))
	}
}

func TestHTTPClientUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"validation-error"}`))
	}))
	defer server.Close()

	client := NewHTTPClient(Endpoint{BaseURL: server.URL, ContractVersion: ContractV1})
	_, err := client.PutSubscriptions(context.Background(), []DesiredRow{
		{RowID: "x", SourceDevice: "s", PipelineID: "p", ConclusionDeviceID: "d"},
	})
	if !errors.Is(err, ErrUpstreamError) {
		t.Fatalf("err = %v, want ErrUpstreamError", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode: %v", err)
	}
}
