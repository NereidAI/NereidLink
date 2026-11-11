// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// provision_wiring.go adapts the internal provisioning service to the
// gatewayapi ProvisionAdmin contract, translating sentinel errors and
// task DTOs at the composition boundary.

package main

import (
	"context"
	"errors"
	"time"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/gatewayapi"
	"github.com/nereidai/nereidlink/internal/provision"
)

// provisionTuning maps YAML values to the reconciler tuning, applying
// safe defaults for entries the operator did not set so the service
// works with a minimal configuration file.
func provisionTuning(cfg *config.Config) provision.Tuning {
	return provision.Tuning{
		JSReplicas:   cfg.NATS.JetStreamReplicas,
		PollInterval: durationOrDefault(cfg.Provision.PollInterval, 5*time.Second),
		Lease:        durationOrDefault(cfg.Provision.Lease, 2*time.Minute),
		MaxAttempts:  cfg.Provision.MaxAttempts,
		MinBackoff:   durationOrDefault(cfg.Provision.MinBackoff, 2*time.Second),
		MaxBackoff:   durationOrDefault(cfg.Provision.MaxBackoff, 5*time.Minute),
	}
}

// durationOrDefault parses value, falling back to fallback when empty
// or unparsable. The YAML file is operator-authored; a bad duration is
// tolerated here rather than crashing the process.
func durationOrDefault(value string, fallback time.Duration) time.Duration {
	if value == "" {
		return fallback
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return d
}

// provisionAdminAdapter wraps *provision.Service for gatewayapi.
type provisionAdminAdapter struct {
	service *provision.Service
}

// CreateTenant delegates to the provisioning service.
func (a *provisionAdminAdapter) CreateTenant(ctx context.Context, name string) (string, error) {
	id, err := a.service.CreateTenant(ctx, name)
	return id, mapProvisionError(err)
}

// GetTask delegates and converts the task DTO.
func (a *provisionAdminAdapter) GetTask(ctx context.Context, tenantID string) (gatewayapi.ProvisionTaskDTO, error) {
	task, err := a.service.GetTask(ctx, tenantID)
	if err != nil {
		return gatewayapi.ProvisionTaskDTO{}, mapProvisionError(err)
	}
	return toProvisionTaskDTO(task), nil
}

// ListTasks delegates and converts every task DTO.
func (a *provisionAdminAdapter) ListTasks(ctx context.Context) ([]gatewayapi.ProvisionTaskDTO, error) {
	tasks, err := a.service.ListTasks(ctx)
	if err != nil {
		return nil, mapProvisionError(err)
	}
	dtos := make([]gatewayapi.ProvisionTaskDTO, 0, len(tasks))
	for _, task := range tasks {
		dtos = append(dtos, toProvisionTaskDTO(task))
	}
	return dtos, nil
}

// Retry delegates to the provisioning service.
func (a *provisionAdminAdapter) Retry(ctx context.Context, tenantID string) error {
	return mapProvisionError(a.service.Retry(ctx, tenantID))
}

// toProvisionTaskDTO maps the internal task to the API DTO.
func toProvisionTaskDTO(task provision.Task) gatewayapi.ProvisionTaskDTO {
	return gatewayapi.ProvisionTaskDTO{
		TenantID:     task.TenantID,
		TenantName:   task.TenantName,
		TenantStatus: task.TenantStatus,
		Status:       task.Status,
		Attempts:     task.Attempts,
		LastError:    task.LastError,
		RunAfter:     task.RunAfter,
	}
}

// mapProvisionError translates internal sentinels to the gatewayapi
// sentinels; unknown errors pass through unchanged.
func mapProvisionError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, provision.ErrNotFound):
		return gatewayapi.ErrProvisionNotFound
	case errors.Is(err, provision.ErrInvalidArgument):
		return gatewayapi.ErrProvisionInvalidArgument
	default:
		return err
	}
}

// Compile-time interface assertion.
var _ gatewayapi.ProvisionAdmin = &provisionAdminAdapter{}
