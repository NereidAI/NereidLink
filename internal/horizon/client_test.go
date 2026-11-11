// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package horizon

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPClientEnsureTaskSendsContractEnvelopeAndAPIKey(t *testing.T) {
	var gotPath, gotKey, gotMethod string
	var got EnsureRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotKey = r.Header.Get("X-Api-Key")
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("request body is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Task{
			TaskID: "dt_123", Org: "yiqisoft", State: StateInProgress,
			ObjectType: got.ObjectType, ObjectID: got.ObjectID,
		})
	}))
	defer server.Close()

	client, err := NewHTTPClient(ClientConfig{
		BaseURL: server.URL + "/", Org: "yiqisoft", APIKey: "nk-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := client.EnsureTask(context.Background(), EnsureRequest{
		ModelName: "face-detector", Version: "1.0.0", Precision: PrecisionFP16,
		ObjectType: "inference-model", ObjectID: "face-detector-1.0.0-fp16",
		Destinations: []string{"openhorizon.edgenode:node1"},
	})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if gotMethod != http.MethodPost || gotKey != "nk-secret" {
		t.Fatalf("unexpected request %s key=%q", gotMethod, gotKey)
	}
	if gotPath != "/api/service/v1/orgs/yiqisoft/distribution-tasks" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if got.ObjectID != "face-detector-1.0.0-fp16" || len(got.Destinations) != 1 {
		t.Fatalf("envelope not forwarded verbatim: %+v", got)
	}
	if task.TaskID != "dt_123" || task.State != StateInProgress {
		t.Fatalf("unexpected task: %+v", task)
	}
}

func TestHTTPClientGetTaskBuildsHandlePath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewEncoder(w).Encode(Task{TaskID: "dt_123", State: StateDelivered, EdgeReady: true})
	}))
	defer server.Close()

	client, _ := NewHTTPClient(ClientConfig{BaseURL: server.URL, Org: "yiqisoft", APIKey: "k"})
	task, err := client.GetTask(context.Background(), "dt_123")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/service/v1/orgs/yiqisoft/distribution-tasks/dt_123" {
		t.Fatalf("unexpected path %q", gotPath)
	}
	if !task.EdgeReady || task.State != StateDelivered {
		t.Fatalf("unexpected task: %+v", task)
	}
}

func TestHTTPClientMapsNon2xxToTypedUpstreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"msg":"no ready fp16 package exists for this model version"}`))
	}))
	defer server.Close()

	client, _ := NewHTTPClient(ClientConfig{BaseURL: server.URL, Org: "yiqisoft", APIKey: "k"})
	_, err := client.EnsureTask(context.Background(), EnsureRequest{ObjectID: "x"})
	var upstream *UpstreamError
	if !errors.As(err, &upstream) {
		t.Fatalf("expected UpstreamError, got %T: %v", err, err)
	}
	if upstream.Status != http.StatusConflict || !errors.Is(err, ErrUpstream) {
		t.Fatalf("unexpected error: %+v", upstream)
	}
	if !strings.Contains(upstream.Body, "no ready fp16 package") {
		t.Fatalf("NH reason body not preserved: %q", upstream.Body)
	}
}

func TestNewHTTPClientRejectsUnreadableCA(t *testing.T) {
	if _, err := NewHTTPClient(ClientConfig{
		BaseURL: "https://nh.example", Org: "o", APIKey: "k", CAFile: "/no/such/ca.pem",
	}); err == nil {
		t.Fatal("expected CA load failure")
	}
}

func TestTaskActive(t *testing.T) {
	cases := map[string]bool{
		StateInProgress: true,
		StateDelivered:  true,
		StateFailed:     false,
		StateCancelled:  false,
		StateRolledBack: false,
	}
	for state, want := range cases {
		if got := (Task{State: state}).Active(); got != want {
			t.Errorf("state %s Active()=%v want %v", state, got, want)
		}
	}
}
