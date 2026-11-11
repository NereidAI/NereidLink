// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package storagehook

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// wasmMagic is the fixed 8-byte preamble every WebAssembly binary begins
// with ("\0asm" + version), used as a cheap pre-check before compilation.
var wasmMagic = []byte{0x00, 0x61, 0x73, 0x6d}

// WasmValidator compiles a module so management rejects an invalid binary at
// create/update time. *WasmRuntime implements it; a nil validator skips the
// compile check (the binary is still base64/magic/size validated).
type WasmValidator interface {
	Validate(ctx context.Context, binary []byte) error
}

// Store loads hook configuration and writes DLQ rows.
type Store struct {
	pool          *pgxpool.Pool
	wasmValidator WasmValidator
	wasm          WasmOptions
}

// NewStore constructs a storage-hook store.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, wasm: DefaultWasmOptions()}
}

// SetWasmValidator wires the optional compile-time validator for wasm hooks.
func (s *Store) SetWasmValidator(v WasmValidator) {
	s.wasmValidator = v
}

// SetWasmOptions overrides the wasm sandbox budgets (notably the uploaded
// module size cap). Zero fields keep the defaults.
func (s *Store) SetWasmOptions(opts WasmOptions) {
	if opts.MaxModuleBytes > 0 {
		s.wasm.MaxModuleBytes = opts.MaxModuleBytes
	}
}

// EnabledHooks returns the enabled hooks of a tenant in ascending order.
func (s *Store) EnabledHooks(ctx context.Context, tenantID string) ([]Hook, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text,
		        tenant_id::text,
		        name,
		        hook_order,
		        hook_type,
		        measurement_keys,
		        config,
		        timeout_ms,
		        failure_policy
		 FROM storage_hook
		 WHERE tenant_id = $1 AND enabled = TRUE
		 ORDER BY hook_order, name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("load storage hooks: %w", err)
	}
	defer rows.Close()

	hooks := []Hook{}
	for rows.Next() {
		var hook Hook
		var config []byte
		if err := rows.Scan(
			&hook.ID,
			&hook.TenantID,
			&hook.Name,
			&hook.Order,
			&hook.Type,
			&hook.Keys,
			&config,
			&hook.TimeoutMS,
			&hook.FailurePolicy,
		); err != nil {
			return nil, fmt.Errorf("scan storage hook: %w", err)
		}
		if err := s.decodeConfig(hook.Type, config, &hook); err != nil {
			return nil, fmt.Errorf("decode storage hook %s config: %w", hook.Name, err)
		}
		hooks = append(hooks, hook)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate storage hooks: %w", err)
	}
	return hooks, nil
}

// configShape carries the type-specific JSON configuration fields.
type configShape struct {
	Script    string `json:"script"`
	URL       string `json:"url"`
	ModuleB64 string `json:"module_b64"`
}

// decodeWasmModule base64-decodes and sanity-checks a wasm module.
func (s *Store) decodeWasmModule(encoded string) ([]byte, error) {
	binary, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("config.module_b64 must be standard base64: %w", err)
	}
	if len(binary) == 0 {
		return nil, fmt.Errorf("wasm module is empty")
	}
	if len(binary) > s.wasm.MaxModuleBytes {
		return nil, fmt.Errorf("wasm module exceeds %d bytes", s.wasm.MaxModuleBytes)
	}
	if len(binary) < len(wasmMagic) || !bytes.Equal(binary[:len(wasmMagic)], wasmMagic) {
		return nil, fmt.Errorf("not a WebAssembly binary (bad magic)")
	}
	return binary, nil
}

// decodeConfig validates and maps type-specific configuration onto hook.
func (s *Store) decodeConfig(hookType string, raw []byte, hook *Hook) error {
	var shape configShape
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &shape); err != nil {
			return err
		}
	}
	switch hookType {
	case TypeExpression:
		if shape.Script == "" {
			return fmt.Errorf("expression hook requires config.script")
		}
		hook.Script = shape.Script
	case TypeWebhook:
		if shape.URL == "" {
			return fmt.Errorf("webhook hook requires config.url")
		}
		hook.URL = shape.URL
	case TypeWasm:
		module, err := s.decodeWasmModule(shape.ModuleB64)
		if err != nil {
			return fmt.Errorf("wasm hook: %w", err)
		}
		hook.Module = module
	default:
		return fmt.Errorf("unsupported hook type %q", hookType)
	}
	return nil
}

// WriteDLQ persists one point rejected by a failing hook under the dlq
// policy. A DLQ write failure is returned so callers can retain the row for
// retry instead of losing the point.
func (s *Store) WriteDLQ(ctx context.Context, point Point, hookName, reason string) error {
	encoded, err := json.Marshal(point.asMap())
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO telemetry_dlq
		   (tenant_id, device_id, measurement_key, point, hook_name, reason)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		point.TenantID, point.DeviceID, point.MeasurementKey,
		encoded, hookName, reason)
	if err != nil {
		return fmt.Errorf("write telemetry dlq: %w", err)
	}
	return nil
}
