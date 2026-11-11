// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// model_test.go covers shadow state merging and delta semantics, in
// particular JSON null key deletion on both desired and reported patches.
package shadow

import "testing"

func TestMergePatchOverwritesAndDeletes(t *testing.T) {
	current := map[string]any{"a": 1, "b": 2, "c": 3}
	patch := map[string]any{"b": 20, "c": nil, "d": 4}
	merged := mergePatch(current, patch)

	if merged["a"] != 1 || merged["b"] != 20 || merged["d"] != 4 {
		t.Fatalf("unexpected merge result: %+v", merged)
	}
	if _, present := merged["c"]; present {
		t.Fatal("null patch value must delete the key")
	}
	// The input map must not be mutated.
	if _, deleted := current["c"]; !deleted {
		t.Fatal("mergePatch must not mutate the current map")
	}
	if current["b"] != 2 {
		t.Fatal("mergePatch must not mutate the current map")
	}
}

func TestMergePatchHandlesObjectAndArrayValues(t *testing.T) {
	current := map[string]any{"config": map[string]any{"x": 1}}
	patch := map[string]any{"config": map[string]any{"y": 2}, "list": []any{1, 2}}
	merged := mergePatch(current, patch)
	cfg, ok := merged["config"].(map[string]any)
	if !ok {
		t.Fatalf("config must remain an object: %+v", merged["config"])
	}
	if cfg["y"] != 2 || len(cfg) != 1 {
		t.Fatalf("object value must be replaced wholesale: %+v", merged["config"])
	}
	list, ok := merged["list"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("array value must be accepted: %+v", merged["list"])
	}
}

func TestComputeDeltaReportsOnlyMismatchedDesired(t *testing.T) {
	desired := map[string]any{"sync": "yes", "n": 3, "onlyDesired": true}
	reported := map[string]any{"sync": "yes", "n": 99}
	delta := computeDelta(desired, reported)
	if _, ok := delta["sync"]; ok {
		t.Fatal("equal values must not appear in delta")
	}
	if delta["n"] != 3 || delta["onlyDesired"] != true {
		t.Fatalf("unexpected delta: %+v", delta)
	}
	if len(delta) != 2 {
		t.Fatalf("expected 2 delta keys, got %d (%+v)", len(delta), delta)
	}
}
