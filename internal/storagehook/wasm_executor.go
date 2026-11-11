// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// WASM hook budgets (ADR-0015). They are deliberately fixed rather than
// per-hook so a tenant cannot configure itself a larger sandbox than the
// platform allows; the only per-hook knob is the execution timeout already
// shared with expression/webhook hooks.
const (
	// DefaultWasmMemoryPages caps one invocation's linear memory at 16 MiB.
	// A Go-compiled WASI module initializes inside this; growing past it
	// makes the memory.grow instruction fail, which surfaces as a hook error.
	DefaultWasmMemoryPages uint32 = 256

	// DefaultWasmMaxModuleBytes bounds an uploaded module (decoded size),
	// keeping a hook row and its per-node compilation cache small.
	DefaultWasmMaxModuleBytes = 2 * 1024 * 1024

	// DefaultWasmMaxOutput bounds the stdout JSON overlay a module may emit.
	DefaultWasmMaxOutput = 64 * 1024
)

// WasmOptions are the hard sandbox budgets for wasm hooks. They are platform
// limits, not per-hook configuration, so a tenant cannot grant itself a larger
// sandbox than the deployment allows.
type WasmOptions struct {
	// MaxModuleBytes bounds an uploaded module (decoded size).
	MaxModuleBytes int

	// MemoryPages caps one invocation's linear memory in 64 KiB pages.
	MemoryPages uint32

	// MaxOutput bounds the stdout overlay a module may emit.
	MaxOutput int
}

// DefaultWasmOptions returns the documented ADR budget defaults.
func DefaultWasmOptions() WasmOptions {
	return WasmOptions{
		MaxModuleBytes: DefaultWasmMaxModuleBytes,
		MemoryPages:    DefaultWasmMemoryPages,
		MaxOutput:      DefaultWasmMaxOutput,
	}
}

// ErrWasmHook classifies a WASM hook execution/validation failure so the
// pipeline failure policy applies like it does for expressions and webhooks.
var ErrWasmHook = errors.New("wasm hook failed")

// WasmRuntime compiles and runs WASM hook modules under shared hard budgets.
// One runtime serves every hook; each invocation gets a fresh, isolated
// module instance, so guest globals/heap never leak between telemetry points.
type WasmRuntime struct {
	runtime wazero.Runtime
	opts    WasmOptions

	mu      sync.Mutex
	modules map[string]wazero.CompiledModule
}

// NewWasmRuntime constructs the shared runtime with the given memory cap and
// context-cancellation-based preemption enabled.
func NewWasmRuntime(ctx context.Context, opts WasmOptions) (*WasmRuntime, error) {
	if opts.MemoryPages == 0 {
		opts.MemoryPages = DefaultWasmMemoryPages
	}
	if opts.MaxOutput <= 0 {
		opts.MaxOutput = DefaultWasmMaxOutput
	}
	cache := wazero.NewCompilationCache()
	config := wazero.NewRuntimeConfig().
		WithMemoryLimitPages(opts.MemoryPages).
		WithCloseOnContextDone(true).
		WithCompilationCache(cache)
	rt := wazero.NewRuntimeWithConfig(ctx, config)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("instantiate wasi: %w", err)
	}
	return &WasmRuntime{
		runtime: rt,
		opts:    opts,
		modules: map[string]wazero.CompiledModule{},
	}, nil
}

// Close releases the runtime and its compilation cache.
func (w *WasmRuntime) Close(ctx context.Context) error {
	return w.runtime.Close(ctx)
}

// Validate compiles an uploaded module so management rejects a malformed or
// non-WASI binary at create/update time with a 400 rather than on the first
// telemetry point.
func (w *WasmRuntime) Validate(ctx context.Context, binary []byte) error {
	if _, err := w.compile(ctx, binary); err != nil {
		return fmt.Errorf("%w: %v", ErrWasmHook, err)
	}
	return nil
}

// compile returns the cached compiled module for a binary, compiling once.
func (w *WasmRuntime) compile(ctx context.Context, binary []byte) (wazero.CompiledModule, error) {
	sum := sha256.Sum256(binary)
	key := hex.EncodeToString(sum[:])

	w.mu.Lock()
	if compiled, ok := w.modules[key]; ok {
		w.mu.Unlock()
		return compiled, nil
	}
	w.mu.Unlock()

	compiled, err := w.runtime.CompileModule(ctx, binary)
	if err != nil {
		return nil, fmt.Errorf("compile wasm module: %w", err)
	}

	w.mu.Lock()
	w.modules[key] = compiled
	w.mu.Unlock()
	return compiled, nil
}

// Execute runs one point through a module: the point is marshaled to JSON on
// stdin and the module's stdout is decoded as an optional overlay object,
// using the same return contract as an expression hook (object overlays
// fields, {"drop": true} drops the point, empty/whitespace is a no-op).
func (w *WasmRuntime) Execute(ctx context.Context, hook Hook, point Point) (map[string]any, bool, error) {
	if len(hook.Module) == 0 {
		return nil, false, fmt.Errorf("%w: empty module", ErrWasmHook)
	}
	compiled, err := w.compile(ctx, hook.Module)
	if err != nil {
		return nil, false, err
	}

	input, err := json.Marshal(point.asMap())
	if err != nil {
		return nil, false, fmt.Errorf("%w: encode point: %v", ErrWasmHook, err)
	}
	output := &limitedBuffer{limit: w.opts.MaxOutput}

	timeout := time.Duration(hook.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	moduleConfig := wazero.NewModuleConfig().
		WithName("").
		WithArgs("hook").
		WithStdin(bytes.NewReader(input)).
		WithStdout(output).
		WithStderr(io.Discard)

	// A fresh instance per point isolates guest state. WithCloseOnContextDone
	// makes the deadline preempt a module stuck in an infinite loop.
	instance, err := w.runtime.InstantiateModule(runCtx, compiled, moduleConfig)
	if err != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return nil, false, fmt.Errorf("%w: execution timed out after %s", ErrWasmHook, timeout)
		}
		return nil, false, fmt.Errorf("%w: %v", ErrWasmHook, err)
	}
	_ = instance.Close(runCtx)

	raw := bytes.TrimSpace(output.Bytes())
	if len(raw) == 0 {
		return nil, false, nil
	}
	var overlay map[string]any
	if err := json.Unmarshal(raw, &overlay); err != nil {
		return nil, false, fmt.Errorf("%w: stdout must be a JSON object: %v", ErrWasmHook, err)
	}
	drop, _ := overlay["drop"].(bool)
	return overlay, drop, nil
}

// limitedBuffer is an io.Writer that fails once the captured output exceeds
// limit, bounding a module that writes without bound.
type limitedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		return 0, fmt.Errorf("%w: stdout exceeded %d bytes", ErrWasmHook, b.limit)
	}
	return b.buf.Write(p)
}

// Bytes returns the captured output.
func (b *limitedBuffer) Bytes() []byte { return b.buf.Bytes() }
