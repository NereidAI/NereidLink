// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"

	"github.com/nereidai/nereidlink/internal/business"
)

// ClientFactory builds the transport client for one registered endpoint.
// Production uses NewHTTPClient; tests inject a factory returning fakes.
type ClientFactory func(Endpoint) CompanionClient

// Owner is the single business.CompanionOwner implementation. It maps one
// companion_subscription artifact to one desired row per source device and
// reconciles the full endpoint set through the generic contract. Adding a
// new companion application is endpoint registration plus conformance, never
// another owner implementation.
type Owner struct {
	store   Store
	clients ClientFactory
	log     *slog.Logger
}

// NewOwner wires the owner. A nil factory defaults to the HTTP contract
// client; a nil logger defaults to the package-level slog logger.
func NewOwner(store Store, clients ClientFactory, log *slog.Logger) *Owner {
	if clients == nil {
		clients = func(ep Endpoint) CompanionClient { return NewHTTPClient(ep) }
	}
	if log == nil {
		log = slog.Default()
	}
	return &Owner{store: store, clients: clients, log: log}
}

var _ business.CompanionOwner = (*Owner)(nil)

// ApplySubscription implements business.CompanionOwner. It negotiates the
// contract version, replaces only this artifact's desired rows, then pushes
// the endpoint's full desired set and persists the per-row read-back. A
// companion-reported FAILED row fails the deployment even though the read-back
// is persisted for operator inspection.
func (o *Owner) ApplySubscription(ctx context.Context, tenantID string, artifact business.Artifact) (string, error) {
	gatewayID := strings.TrimSpace(artifact.GatewayID)
	companionID := specText(artifact.Spec, "companion_id")
	conclusionDeviceID := specText(artifact.Spec, "conclusion_device_id")
	pipelineID := specText(artifact.Spec, "pipeline_id")
	sourceDevices := sortedUnique(specDevices(artifact.Spec, "source_devices"))

	if companionID == "" || conclusionDeviceID == "" || pipelineID == "" || gatewayID == "" {
		return "", fmt.Errorf("%w: companion subscription requires companion_id, pipeline_id, conclusion_device_id and gateway",
			business.ErrInvalid)
	}
	if len(sourceDevices) == 0 {
		return "", fmt.Errorf("%w: companion subscription artifact %s declares no source_devices",
			business.ErrInvalid, artifact.Key)
	}

	endpoint, err := o.store.GetEndpoint(ctx, tenantID, companionID, gatewayID)
	if errors.Is(err, ErrStoreNotFound) {
		return "", fmt.Errorf("%w: no companion endpoint registered for %s on gateway %s",
			business.ErrInvalid, companionID, gatewayID)
	}
	if err != nil {
		return "", fmt.Errorf("load companion endpoint: %w", err)
	}
	if !endpoint.Enabled {
		return "", fmt.Errorf("%w: companion endpoint %s (%s on %s) is disabled",
			business.ErrInvalid, endpoint.ID, companionID, gatewayID)
	}

	rows := make([]DesiredRow, 0, len(sourceDevices))
	for _, device := range sourceDevices {
		rows = append(rows, DesiredRow{
			RowID:              RowID(gatewayID, conclusionDeviceID, device),
			SourceDevice:       device,
			Camera:             device,
			PipelineID:         pipelineID,
			ConclusionDeviceID: conclusionDeviceID,
			Enabled:            true,
			ArtifactKey:        artifact.Key,
			Revision:           0,
			Status:             StatusPending,
		})
	}

	client := o.clients(endpoint)
	// Negotiate before any local mutation so a contract mismatch never leaves
	// a desired set whose delivery protocol is unverified.
	if err := client.CheckContract(ctx); err != nil {
		return "", fmt.Errorf("companion contract check for %s: %w", companionID, err)
	}
	if err := o.store.ReplaceDesired(ctx, tenantID, endpoint.ID, artifact.Key, rows); err != nil {
		return "", fmt.Errorf("persist companion desired rows: %w", err)
	}
	desired, err := o.store.ListDesired(ctx, tenantID, endpoint.ID)
	if err != nil {
		return "", fmt.Errorf("load companion desired set: %w", err)
	}
	readback, err := client.PutSubscriptions(ctx, desired)
	if err != nil {
		return "", fmt.Errorf("deliver companion subscriptions: %w", err)
	}
	if err := o.store.ApplyReadback(ctx, tenantID, endpoint.ID, readback); err != nil {
		return "", fmt.Errorf("persist companion read-back: %w", err)
	}
	if reason := failedReadback(readback); reason != "" {
		return "", fmt.Errorf("companion %s rejected a subscription row: %s", companionID, reason)
	}
	o.log.Info("companion subscription desired set applied",
		"tenant", tenantID, "endpoint", endpoint.ID, "artifact", artifact.Key,
		"desired_rows", len(desired), "readback_rows", len(readback))
	return endpoint.ID, nil
}

// RemoveSubscription implements business.CompanionOwner. It deletes exactly
// this artifact's rows, then re-pushes the remaining endpoint set (an empty
// set unsubscribes everything on the companion). A deregistered endpoint is
// an idempotent success because its rows cascaded away with it.
func (o *Owner) RemoveSubscription(ctx context.Context, tenantID string, resource business.Resource) error {
	endpointID, remaining, err := o.store.DeleteDesiredArtifact(ctx, tenantID, resource.ArtifactKey)
	if err != nil {
		return fmt.Errorf("remove companion desired rows: %w", err)
	}
	if endpointID == "" {
		return nil
	}
	endpoint, err := o.store.GetEndpointByID(ctx, tenantID, endpointID)
	if errors.Is(err, ErrStoreNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load companion endpoint: %w", err)
	}
	desired, err := o.store.ListDesired(ctx, tenantID, endpointID)
	if err != nil {
		return fmt.Errorf("load companion desired set: %w", err)
	}
	readback, err := o.clients(endpoint).PutSubscriptions(ctx, desired)
	if err != nil {
		return fmt.Errorf("deliver companion unsubscribe: %w", err)
	}
	if err := o.store.ApplyReadback(ctx, tenantID, endpointID, readback); err != nil {
		return fmt.Errorf("persist companion read-back: %w", err)
	}
	if reason := failedReadback(readback); reason != "" {
		return fmt.Errorf("companion rejected an unsubscribe row: %s", reason)
	}
	o.log.Info("companion artifact removed",
		"tenant", tenantID, "endpoint", endpointID,
		"artifact", resource.ArtifactKey, "remaining_rows", remaining)
	return nil
}

// RowID builds the deterministic contract row id
// companion:<gateway>:<conclusionDevice>:<sourceDevice>. Stable ids make
// re-PUT idempotent and keep companion-local revisions monotonic.
func RowID(gatewayID, conclusionDeviceID, sourceDevice string) string {
	return strings.Join([]string{"companion", gatewayID, conclusionDeviceID, sourceDevice}, ":")
}

// failedReadback returns a human-readable reason for the first FAILED row, or
// "" when every row is APPLIED (or otherwise non-failed).
func failedReadback(readback []ReadbackRow) string {
	for _, row := range readback {
		if row.Status == StatusFailed {
			if strings.TrimSpace(row.Reason) != "" {
				return row.ID + ": " + row.Reason
			}
			return row.ID + ": failed without reason"
		}
	}
	return ""
}

func specText(spec map[string]any, key string) string {
	value, _ := spec[key].(string)
	return strings.TrimSpace(value)
}

// specDevices tolerates both the in-memory []string shape and the []any shape
// produced when an expansion snapshot round-trips through JSONB.
func specDevices(spec map[string]any, key string) []string {
	switch values := spec[key].(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				continue
			}
			if text = strings.TrimSpace(text); text != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func sortedUnique(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
