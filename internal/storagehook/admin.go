// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// admin.go implements management operations for pre-storage hooks and the
// telemetry dead-letter queue: CRUD on hooks and list/delete/replay on DLQ
// rows. Replay inserts the original point straight into the telemetry
// hypertable, bypassing hooks, and retains the DLQ row until deleted.

package storagehook

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/nereidai/nereidlink/internal/apipage"
)

// HookDTO is the management representation of a storage hook.
type HookDTO struct {
	// ID is the hook identifier.
	ID string `json:"id,omitempty"`

	// Name is the unique tenant-scoped name.
	Name string `json:"name"`

	// Enabled toggles execution without deleting the hook.
	Enabled bool `json:"enabled"`

	// Order is the ascending execution order.
	Order int `json:"order"`

	// HookType is expression or webhook.
	HookType string `json:"hook_type"`

	// MeasurementKeys limits the keys; empty means every key.
	MeasurementKeys []string `json:"measurement_keys"`

	// Config holds {script} or {url} depending on type.
	Config json.RawMessage `json:"config"`

	// TimeoutMS bounds one execution.
	TimeoutMS int `json:"timeout_ms"`

	// FailurePolicy is passthrough, abort or dlq.
	FailurePolicy string `json:"failure_policy"`

	// CreatedAt is the creation time as RFC3339 UTC.
	CreatedAt string `json:"created_at"`

	// UpdatedAt is the last change time as RFC3339 UTC.
	UpdatedAt string `json:"updated_at"`
}

// DLQRecord is one dead-letter row.
type DLQRecord struct {
	// ID is the identity-column identifier.
	ID int64 `json:"id"`

	// Time is the original failure time as RFC3339 UTC.
	Time string `json:"time"`

	// TenantID owns the point.
	TenantID string `json:"tenant_id"`

	// DeviceID originates the point.
	DeviceID string `json:"device_id"`

	// MeasurementKey identifies the measurement.
	MeasurementKey string `json:"measurement_key"`

	// Point is the original point document.
	Point json.RawMessage `json:"point"`

	// HookName is the failing hook.
	HookName string `json:"hook_name"`

	// Reason explains the failure.
	Reason string `json:"reason"`
}

// Management errors alongside the pipeline package errors.
var (
	// ErrHookNotFound means the hook id is unknown.
	ErrHookNotFound = errors.New("storage hook not found")

	// ErrInvalidHook means a hook field is invalid.
	ErrInvalidHook = errors.New("invalid storage hook")
)

// ListHooks returns every hook of a tenant including disabled ones.
func (s *Store) ListHooks(ctx context.Context, tenantID string) ([]HookDTO, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id::text, name, enabled, hook_order, hook_type,
		        measurement_keys, config::text, timeout_ms, failure_policy,
		        created_at, updated_at
		 FROM storage_hook
		 WHERE tenant_id = $1
		 ORDER BY hook_order, name`,
		tenantID)
	if err != nil {
		return nil, fmt.Errorf("list storage hooks: %w", err)
	}
	defer rows.Close()

	hooks := []HookDTO{}
	for rows.Next() {
		hook, err := scanHook(rows)
		if err != nil {
			return nil, err
		}
		hooks = append(hooks, hook)
	}
	return hooks, rows.Err()
}

// maskConfig strips the potentially-megabyte wasm module from a management
// response and replaces it with non-secret metadata. The processing-plane
// loader (EnabledHooks) reads the full config directly; only the REST list/get
// projection is masked so the hook table does not ship every module on load.
// A wasm hook is returned as config {module_size, module_sha256}.
func maskConfig(hookType, raw string) json.RawMessage {
	if hookType != TypeWasm {
		return json.RawMessage(raw)
	}
	var shape configShape
	if json.Unmarshal([]byte(raw), &shape) != nil || shape.ModuleB64 == "" {
		return json.RawMessage(raw)
	}
	binary, err := base64.StdEncoding.DecodeString(shape.ModuleB64)
	if err != nil {
		return json.RawMessage(raw)
	}
	sum := sha256.Sum256(binary)
	masked, err := json.Marshal(map[string]any{
		"module_size":   len(binary),
		"module_sha256": hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return json.RawMessage(raw)
	}
	return masked
}

// maskInputConfig masks the module on a just-accepted wasm DTO so the create/
// update response does not echo back the uploaded binary.
func maskInputConfig(input *HookDTO) {
	if input.HookType == TypeWasm && len(input.Config) > 0 {
		input.Config = maskConfig(TypeWasm, string(input.Config))
	}
}

// rowScanner abstracts Scan for Query and QueryRow results.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanHook scans one hook row and renders timestamps as RFC3339.
func scanHook(scanner rowScanner) (HookDTO, error) {
	var hook HookDTO
	var config string
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(&hook.ID, &hook.Name, &hook.Enabled, &hook.Order,
		&hook.HookType, &hook.MeasurementKeys, &config, &hook.TimeoutMS,
		&hook.FailurePolicy, &createdAt, &updatedAt); err != nil {
		return HookDTO{}, fmt.Errorf("scan storage hook: %w", err)
	}
	hook.Config = maskConfig(hook.HookType, config)
	hook.CreatedAt = createdAt.UTC().Format(time.RFC3339)
	hook.UpdatedAt = updatedAt.UTC().Format(time.RFC3339)
	return hook, nil
}

// CreateHook validates and inserts one hook.
func (s *Store) CreateHook(ctx context.Context, tenantID string, input HookDTO) (HookDTO, error) {
	if err := s.validateHook(ctx, input); err != nil {
		return HookDTO{}, err
	}
	input.ID = uuid.NewString()

	if _, err := s.pool.Exec(ctx,
		`INSERT INTO storage_hook
		   (id, tenant_id, name, enabled, hook_order, hook_type,
		    measurement_keys, config, timeout_ms, failure_policy)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10)`,
		input.ID, tenantID, input.Name, input.Enabled, input.Order,
		input.HookType, input.MeasurementKeys, string(input.Config),
		input.TimeoutMS, input.FailurePolicy); err != nil {
		return HookDTO{}, mapAdminSQLError(err)
	}
	maskInputConfig(&input)
	return input, nil
}

// UpdateHook validates and replaces one hook.
func (s *Store) UpdateHook(ctx context.Context, tenantID, hookID string, input HookDTO) (HookDTO, error) {
	// A wasm update may omit module_b64 to edit metadata without re-uploading
	// the multi-MB binary; reuse the stored config in that case.
	if input.HookType == TypeWasm {
		if kept, ok, err := s.keepWasmModule(ctx, tenantID, hookID, input); err != nil {
			return HookDTO{}, err
		} else if ok {
			input.Config = kept
		}
	}
	if err := s.validateHook(ctx, input); err != nil {
		return HookDTO{}, err
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE storage_hook
		 SET name = $1, enabled = $2, hook_order = $3, hook_type = $4,
		     measurement_keys = $5, config = $6::jsonb, timeout_ms = $7,
		     failure_policy = $8, updated_at = now()
		 WHERE id = $9 AND tenant_id = $10`,
		input.Name, input.Enabled, input.Order, input.HookType,
		input.MeasurementKeys, string(input.Config), input.TimeoutMS,
		input.FailurePolicy, hookID, tenantID)
	if err != nil {
		return HookDTO{}, mapAdminSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return HookDTO{}, ErrHookNotFound
	}
	input.ID = hookID
	maskInputConfig(&input)
	return input, nil
}

// DeleteHook removes one hook.
func (s *Store) DeleteHook(ctx context.Context, tenantID, hookID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM storage_hook WHERE id = $1 AND tenant_id = $2`,
		hookID, tenantID)
	if err != nil {
		return mapAdminSQLError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrHookNotFound
	}
	return nil
}

// keepWasmModule returns the existing stored config when a wasm update omits
// module_b64, so metadata-only edits need not re-upload the binary.
func (s *Store) keepWasmModule(ctx context.Context, tenantID, hookID string, input HookDTO) (json.RawMessage, bool, error) {
	var shape configShape
	if len(input.Config) > 0 && json.Unmarshal(input.Config, &shape) == nil && shape.ModuleB64 == "" {
		var storedType, storedConfig string
		err := s.pool.QueryRow(ctx,
			`SELECT hook_type, config::text FROM storage_hook
			 WHERE id=$1 AND tenant_id=$2`, hookID, tenantID).
			Scan(&storedType, &storedConfig)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		if err != nil {
			return nil, false, fmt.Errorf("load storage hook for wasm update: %w", err)
		}
		if storedType == TypeWasm {
			return json.RawMessage(storedConfig), true, nil
		}
	}
	return nil, false, nil
}

// validateHook enforces the same invariants as the table constraints plus
// the type-specific config presence required by the pipeline loader. A wasm
// module is decoded and, when a runtime is wired, compiled so an invalid
// binary is rejected with 400 rather than on the first telemetry point.
func (s *Store) validateHook(ctx context.Context, input HookDTO) error {
	if strings.TrimSpace(input.Name) == "" {
		return invalidHook("name is required")
	}
	if input.HookType != TypeExpression && input.HookType != TypeWebhook &&
		input.HookType != TypeWasm {
		return invalidHook("hook_type must be expression, webhook or wasm")
	}
	if input.TimeoutMS <= 0 {
		return invalidHook("timeout_ms must be positive")
	}
	if input.FailurePolicy != PolicyPassthrough &&
		input.FailurePolicy != PolicyAbort &&
		input.FailurePolicy != PolicyDLQ {
		return invalidHook("failure_policy must be passthrough, abort or dlq")
	}

	var shape configShape
	if len(input.Config) > 0 {
		if !json.Valid(input.Config) {
			return invalidHook("config must be valid JSON")
		}
		if err := json.Unmarshal(input.Config, &shape); err != nil {
			return invalidHook("config must be a JSON object")
		}
	}
	switch input.HookType {
	case TypeExpression:
		if strings.TrimSpace(shape.Script) == "" {
			return invalidHook("config.script is required for expression hooks")
		}
	case TypeWebhook:
		if strings.TrimSpace(shape.URL) == "" {
			return invalidHook("config.url is required for webhook hooks")
		}
	case TypeWasm:
		if strings.TrimSpace(shape.ModuleB64) == "" {
			return invalidHook("config.module_b64 is required for wasm hooks")
		}
		module, err := s.decodeWasmModule(shape.ModuleB64)
		if err != nil {
			return invalidHook(err.Error())
		}
		if s.wasmValidator != nil {
			if err := s.wasmValidator.Validate(ctx, module); err != nil {
				return invalidHook(err.Error())
			}
		}
	}
	return nil
}

// ListDLQ returns one page of DLQ rows of a tenant. hookName filters by exact
// hook name when non-empty; device filters device_id case-insensitively as a
// substring when non-empty.
func (s *Store) ListDLQ(ctx context.Context, tenantID, hookName, device string, page, pageSize int) (apipage.Page[DLQRecord], error) {
	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM telemetry_dlq
		 WHERE tenant_id = $1
		   AND ($2 = '' OR hook_name = $2)
		   AND ($3 = '' OR device_id::text ILIKE '%' || $3 || '%')`,
		tenantID, hookName, device).Scan(&total); err != nil {
		return apipage.Page[DLQRecord]{}, fmt.Errorf("count dlq: %w", err)
	}

	rows, err := s.pool.Query(ctx,
		`SELECT id, time, tenant_id::text, device_id::text, measurement_key,
		        point::text, hook_name, reason
		 FROM telemetry_dlq
		 WHERE tenant_id = $1
		   AND ($2 = '' OR hook_name = $2)
		   AND ($3 = '' OR device_id::text ILIKE '%' || $3 || '%')
		 ORDER BY time DESC
		 LIMIT $4 OFFSET $5`,
		tenantID, hookName, device, pageSize, (page-1)*pageSize)
	if err != nil {
		return apipage.Page[DLQRecord]{}, fmt.Errorf("list dlq: %w", err)
	}
	defer rows.Close()

	records := []DLQRecord{}
	for rows.Next() {
		var record DLQRecord
		var failureTime time.Time
		var point string
		if err := rows.Scan(&record.ID, &failureTime, &record.TenantID,
			&record.DeviceID, &record.MeasurementKey, &point,
			&record.HookName, &record.Reason); err != nil {
			return apipage.Page[DLQRecord]{}, fmt.Errorf("scan dlq: %w", err)
		}
		record.Time = failureTime.UTC().Format(time.RFC3339)
		record.Point = json.RawMessage(point)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return apipage.Page[DLQRecord]{}, err
	}
	return apipage.New(records, total, page, pageSize), nil
}

// DeleteDLQ removes the given tenant-owned DLQ rows.
func (s *Store) DeleteDLQ(ctx context.Context, tenantID string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`DELETE FROM telemetry_dlq
		 WHERE tenant_id = $1 AND id = ANY($2)`,
		tenantID, ids)
	if err != nil {
		return fmt.Errorf("delete dlq: %w", err)
	}
	return nil
}

// replayPoint is the JSON subset stored in telemetry_dlq.point.
type replayPoint struct {
	DeviceID       string  `json:"device_id"`
	MeasurementKey string  `json:"measurement_key"`
	VDouble        float64 `json:"v_double"`
	Quality        string  `json:"quality"`
	Unit           string  `json:"unit"`
	RawDouble      float64 `json:"raw_value"`
}

// ReplayDLQ re-inserts the original points directly into ts_telemetry at the
// replay time, bypassing hooks. The original DLQ rows are retained so the
// operator keeps an audit trail until deleting them explicitly.
func (s *Store) ReplayDLQ(ctx context.Context, tenantID string, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}

	rows, err := s.pool.Query(ctx,
		`SELECT point::text FROM telemetry_dlq
		 WHERE tenant_id = $1 AND id = ANY($2)`,
		tenantID, ids)
	if err != nil {
		return 0, fmt.Errorf("load dlq for replay: %w", err)
	}
	defer rows.Close()

	var points []replayPoint
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return 0, err
		}
		var point replayPoint
		if err := json.Unmarshal([]byte(raw), &point); err != nil {
			return 0, fmt.Errorf("decode dlq point: %w", err)
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, point := range points {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ts_telemetry
			   (time, tenant_id, device_id, measurement_key, v_double,
			    quality, raw_value, unit)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			now, tenantID, point.DeviceID, point.MeasurementKey,
			point.VDouble, point.Quality, point.RawDouble, point.Unit); err != nil {
			return 0, fmt.Errorf("replay insert: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(points), nil
}

// invalidHook wraps an admin validation error.
func invalidHook(message string) error {
	return fmt.Errorf("%w: %s", ErrInvalidHook, message)
}

// mapAdminSQLError maps unique-constraint failures to admin vocabulary.
func mapAdminSQLError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %s", ErrInvalidHook, pgErr.Message)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrHookNotFound
	}
	return err
}
