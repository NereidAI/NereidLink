// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDecodeWasmModule(t *testing.T) {
	s := NewStore(nil)
	t.Run("rejects non-base64", func(t *testing.T) {
		if _, err := s.decodeWasmModule("%%%not-base64"); err == nil {
			t.Fatal("expected base64 error")
		}
	})
	t.Run("rejects bad magic", func(t *testing.T) {
		encoded := base64.StdEncoding.EncodeToString([]byte("not a wasm binary at all"))
		if _, err := s.decodeWasmModule(encoded); err == nil ||
			!strings.Contains(err.Error(), "WebAssembly") {
			t.Fatalf("want bad-magic error, got %v", err)
		}
	})
	t.Run("accepts magic preamble within size", func(t *testing.T) {
		binary := append([]byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00},
			make([]byte, 8)...)
		encoded := base64.StdEncoding.EncodeToString(binary)
		got, err := s.decodeWasmModule(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, binary) {
			t.Fatal("decoded bytes mismatch")
		}
	})
	t.Run("rejects oversize module", func(t *testing.T) {
		binary := append([]byte{0x00, 0x61, 0x73, 0x6d},
			make([]byte, s.wasm.MaxModuleBytes+1)...)
		encoded := base64.StdEncoding.EncodeToString(binary)
		if _, err := s.decodeWasmModule(encoded); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("want size error, got %v", err)
		}
	})

	t.Run("honors custom size cap", func(t *testing.T) {
		custom := &Store{wasm: WasmOptions{MaxModuleBytes: 8}}
		binary := append([]byte{0x00, 0x61, 0x73, 0x6d}, make([]byte, 16)...)
		encoded := base64.StdEncoding.EncodeToString(binary)
		if _, err := custom.decodeWasmModule(encoded); err == nil ||
			!strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("want custom-cap error, got %v", err)
		}
	})
}

func TestLimitedBuffer(t *testing.T) {
	buf := &limitedBuffer{limit: 4}
	if _, err := buf.Write([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := buf.Write([]byte("cd")); err != nil {
		t.Fatalf("exactly at limit should succeed, got %v", err)
	}
	if _, err := buf.Write([]byte("e")); err == nil {
		t.Fatal("write past limit must fail")
	}
}

// guestTransform is a stdlib-only WASI program: it doubles v_double and sets
// unit=x2, returns {"drop":true} for measurement_key "dropme", and emits
// nothing for "noop".
const guestTransform = `package main

import ("encoding/json";"io";"os")

func main() {
	raw, _ := io.ReadAll(os.Stdin)
	var in map[string]any
	_ = json.Unmarshal(raw, &in)
	switch in["measurement_key"].(string) {
	case "dropme":
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"drop": true})
		return
	case "noop":
		return
	}
	v, _ := in["v_double"].(float64)
	_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"v_double": v * 2, "unit": "x2"})
}
`

// guestLoop never returns, exercising the hard timeout.
const guestLoop = `package main

func main() { for {} }
`

// buildGuest cross-compiles a stdlib-only Go WASI module to a temp .wasm file.
func buildGuest(t *testing.T, source string) []byte {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go toolchain required to build wasm fixtures")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module hookfxt\n\ngo 1.24\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "hook.wasm")
	cmd := exec.Command(goBin, "build", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	if out2, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasm fixture: %v\n%s", err, out2)
	}
	binary, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return binary
}

func newTestWasmRuntime(t *testing.T) *WasmRuntime {
	t.Helper()
	rt, err := NewWasmRuntime(context.Background(), DefaultWasmOptions())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close(context.Background()) })
	return rt
}

func TestWasmHookEndToEnd(t *testing.T) {
	if os.Getenv("NL_TEST_WASM") == "" {
		t.Skip("set NL_TEST_WASM=1 to run wazero guest execution tests")
	}
	rt := newTestWasmRuntime(t)
	module := buildGuest(t, guestTransform)

	if err := rt.Validate(context.Background(), module); err != nil {
		t.Fatalf("validate good module: %v", err)
	}

	point := Point{
		TenantID: "00000000-0000-0000-0000-000000000001",
		DeviceID: "00000000-0000-0000-0000-000000000002",
		// MeasurementKey set per case below.
		VDouble: 21,
	}

	t.Run("overlay", func(t *testing.T) {
		p := point
		p.MeasurementKey = "temp"
		hook := Hook{Type: TypeWasm, Module: module, TimeoutMS: 5000}
		overlay, drop, err := rt.Execute(context.Background(), hook, p)
		if err != nil {
			t.Fatal(err)
		}
		if drop {
			t.Fatal("overlay case must not drop")
		}
		if v, _ := overlayNumber(overlay, "v_double"); v != 42 {
			t.Fatalf("want doubled 42, got %v (%v)", v, overlay)
		}
		if overlay["unit"] != "x2" {
			t.Fatalf("want unit x2, got %v", overlay["unit"])
		}
	})

	t.Run("drop", func(t *testing.T) {
		p := point
		p.MeasurementKey = "dropme"
		hook := Hook{Type: TypeWasm, Module: module, TimeoutMS: 5000}
		_, drop, err := rt.Execute(context.Background(), hook, p)
		if err != nil || !drop {
			t.Fatalf("want drop, got drop=%v err=%v", drop, err)
		}
	})

	t.Run("noop empty stdout", func(t *testing.T) {
		p := point
		p.MeasurementKey = "noop"
		hook := Hook{Type: TypeWasm, Module: module, TimeoutMS: 5000}
		overlay, drop, err := rt.Execute(context.Background(), hook, p)
		if err != nil || drop || overlay != nil {
			t.Fatalf("want no-op, got overlay=%v drop=%v err=%v", overlay, drop, err)
		}
	})

	t.Run("compile cache reuse", func(t *testing.T) {
		hook := Hook{Type: TypeWasm, Module: module, TimeoutMS: 5000}
		p := point
		p.MeasurementKey = "temp"
		for i := 0; i < 3; i++ {
			if _, _, err := rt.Execute(context.Background(), hook, p); err != nil {
				t.Fatalf("iter %d: %v", i, err)
			}
		}
	})
}

func TestWasmHookTimeout(t *testing.T) {
	if os.Getenv("NL_TEST_WASM") == "" {
		t.Skip("set NL_TEST_WASM=1 to run wazero guest execution tests")
	}
	rt := newTestWasmRuntime(t)
	module := buildGuest(t, guestLoop)
	hook := Hook{Type: TypeWasm, Module: module, TimeoutMS: 200}
	start := time.Now()
	_, _, err := rt.Execute(context.Background(), hook, Point{MeasurementKey: "x"})
	if !errors.Is(err, ErrWasmHook) || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("want wasm timeout error, got %v", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("hard timeout did not preempt the guest, took %s", d)
	}
}

func TestWasmHookRejectsBadBinary(t *testing.T) {
	if os.Getenv("NL_TEST_WASM") == "" {
		t.Skip("set NL_TEST_WASM=1 to run wazero guest execution tests")
	}
	rt := newTestWasmRuntime(t)
	if err := rt.Validate(context.Background(), []byte{0, 0, 0, 0}); err == nil {
		t.Fatal("malformed module must fail validation")
	}
}
