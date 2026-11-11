// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// secrets_test.go covers secret:// substitution used for the connector drift
// hash (migration 0047): references are resolved, non-reference strings and
// non-strings survive, a missing secret blanks and is reported, and a nil
// resolver is a no-op.

package connector

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeSecretResolver struct {
	values map[string]string
	calls  []string
}

func (f *fakeSecretResolver) Resolve(_ context.Context, _ string, names []string) (map[string]string, error) {
	f.calls = append(f.calls, names...)
	return f.values, nil
}

func TestResolveSecrets(t *testing.T) {
	svc := testSvc(&fakeStore{gatewayOK: true}, &fakeAlarms{})
	sp := Spec{ID: "sp1", TenantID: "t1"}
	raw := json.RawMessage(`{"host":"h","token":"secret://tok","nested":{"pw":"secret://pw"},"arr":["secret://tok",1,true]}`)

	// No resolver: passthrough.
	if got := svc.resolveSecrets(context.Background(), sp, raw); string(got) != string(raw) {
		t.Fatalf("nil resolver must pass through, got %s", got)
	}

	svc.SetSecretResolver(&fakeSecretResolver{values: map[string]string{"tok": "T", "pw": "P"}})
	got := svc.resolveSecrets(context.Background(), sp, raw)
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("resolved not JSON: %v", err)
	}
	if doc["token"] != "T" {
		t.Fatalf("token = %v", doc["token"])
	}
	if doc["nested"].(map[string]any)["pw"] != "P" {
		t.Fatalf("nested pw not resolved: %v", doc["nested"])
	}
	arr := doc["arr"].([]any)
	if arr[0] != "T" || arr[1].(float64) != 1 || arr[2] != true {
		t.Fatalf("array substitution damaged values: %v", arr)
	}
	if doc["host"] != "h" {
		t.Fatalf("plain string changed: %v", doc["host"])
	}
}

func TestResolveSecretsMissingBlanks(t *testing.T) {
	svc := testSvc(&fakeStore{gatewayOK: true}, &fakeAlarms{})
	svc.SetSecretResolver(&fakeSecretResolver{values: map[string]string{}})
	sp := Spec{ID: "sp", TenantID: "t"}
	got := svc.resolveSecrets(context.Background(), sp,
		json.RawMessage(`{"a":"secret://ghost"}`))
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["a"] != "" {
		t.Fatalf("missing secret must blank, got %v", doc["a"])
	}
}
