// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// storagehook_wiring.go maps storage_hook configuration onto the wazero WASM
// sandbox budgets, leaving empty fields at the defaults (module upload 2 MiB,
// 256 linear-memory pages = 16 MiB, 64 KiB stdout overlay).

package main

import (
	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/storagehook"
)

// wasmHookOptions maps declarative configuration onto storagehook.WasmOptions.
func wasmHookOptions(cfg *config.Config) storagehook.WasmOptions {
	opts := storagehook.DefaultWasmOptions()
	if cfg.StorageHook.WasmMaxModuleBytes > 0 {
		opts.MaxModuleBytes = cfg.StorageHook.WasmMaxModuleBytes
	}
	if cfg.StorageHook.WasmMemoryPages > 0 {
		opts.MemoryPages = cfg.StorageHook.WasmMemoryPages
	}
	if cfg.StorageHook.WasmMaxOutputBytes > 0 {
		opts.MaxOutput = cfg.StorageHook.WasmMaxOutputBytes
	}
	return opts
}

// wasmHooksEnabled reports whether the wazero runtime should be constructed;
// it defaults on and is only off for an explicit wasm_enabled: false.
func wasmHooksEnabled(cfg *config.Config) bool {
	return cfg.StorageHook.WasmEnabled == nil || *cfg.StorageHook.WasmEnabled
}
