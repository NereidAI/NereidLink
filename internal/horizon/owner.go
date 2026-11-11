// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package horizon

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/nereidai/nereidlink/internal/business"
	"github.com/nereidai/nereidlink/internal/config"
)

// unsafeToken matches characters outside NH's safe ESS token set
// ([\w.\-~!@#$^*]); each run is collapsed into a single dash.
var unsafeToken = regexp.MustCompile(`[^\w.\-~!@#$^*]+`)

// Owner is the single business.ModelDistributor implementation. It maps one
// model_distribution artifact onto one NH ensure envelope; the mapping is
// fully data-driven (assembly assets + deployment config), so a new model or
// application never requires an owner change.
type Owner struct {
	cfg    config.HorizonSection
	client TaskClient
	log    *slog.Logger
}

// NewOwner wires the owner from the validated deployment config. A nil
// client defaults to the HTTP contract client.
func NewOwner(cfg config.HorizonSection, client TaskClient, log *slog.Logger) (*Owner, error) {
	if client == nil {
		httpClient, err := NewHTTPClient(ClientConfig{
			BaseURL:            cfg.BaseURL,
			Org:                cfg.Org,
			APIKey:             cfg.APIKey,
			CAFile:             cfg.CAFile,
			InsecureSkipVerify: cfg.InsecureSkipVerify,
		})
		if err != nil {
			return nil, err
		}
		client = httpClient
	}
	if log == nil {
		log = slog.Default()
	}
	return &Owner{cfg: cfg, client: client, log: log}, nil
}

var _ business.ModelDistributor = (*Owner)(nil)

// plan is the side-effect-free artifact -> NH envelope mapping. It is shared
// by CheckTarget (preflight) and EnsureDistribution (apply) so the two phases
// can never disagree about the target.
func (o *Owner) plan(artifact business.Artifact) (EnsureRequest, error) {
	modelID := strings.TrimSpace(specText(artifact.Spec, "model_id"))
	version := strings.TrimSpace(specText(artifact.Spec, "version"))
	gatewayID := strings.TrimSpace(artifact.GatewayID)
	if modelID == "" || version == "" || gatewayID == "" {
		return EnsureRequest{}, fmt.Errorf("%w: model distribution requires model_id, version and gateway_id",
			business.ErrInvalid)
	}

	precision, err := o.cfg.PrecisionOrDefault(strings.TrimSpace(specText(artifact.Spec, "precision")))
	if err != nil {
		return EnsureRequest{}, fmt.Errorf("%w: %v", business.ErrInvalid, err)
	}

	destination, ok := o.cfg.GatewayDestinations[gatewayID]
	if !ok || strings.TrimSpace(destination) == "" {
		return EnsureRequest{}, fmt.Errorf("%w: gateway %s has no ESS destination mapping in horizon.gateway_destinations",
			business.ErrInvalid, gatewayID)
	}

	objectType := strings.TrimSpace(specText(artifact.Spec, "object_type"))
	if objectType == "" {
		objectType = DefaultObjectType
	}
	objectID := strings.TrimSpace(specText(artifact.Spec, "object_id"))
	if objectID == "" {
		objectID = safeObjectID(modelID, version, precision)
	}
	if !safeToken(objectType) || !safeToken(objectID) {
		return EnsureRequest{}, fmt.Errorf("%w: derived ESS object identity (%s/%s) contains unsafe characters",
			business.ErrInvalid, objectType, objectID)
	}

	return EnsureRequest{
		// Forge registers the package under the same model name the edge
		// inventory reports; aligning the two identifiers is template-pack
		// data, never a package-internal edit (treaty D6).
		ModelName:    modelID,
		Version:      version,
		Precision:    precision,
		ObjectType:   objectType,
		ObjectID:     objectID,
		Destinations: []string{destination},
		Description:  fmt.Sprintf("nereidlink ensure %s %s (%s)", modelID, version, precision),
	}, nil
}

// CheckTarget implements business.ModelDistributor.
func (o *Owner) CheckTarget(artifact business.Artifact) error {
	_, err := o.plan(artifact)
	return err
}

// EnsureDistribution implements business.ModelDistributor. NH owns
// idempotency: a retry returns the existing task instead of creating a second
// publication.
func (o *Owner) EnsureDistribution(ctx context.Context, _ string, artifact business.Artifact) (business.DistributionHandle, error) {
	req, err := o.plan(artifact)
	if err != nil {
		return business.DistributionHandle{}, err
	}
	task, err := o.client.EnsureTask(ctx, req)
	if err != nil {
		return business.DistributionHandle{}, fmt.Errorf("ensure model distribution for %s/%s: %w",
			req.ModelName, req.Version, err)
	}
	if task.TaskID == "" {
		return business.DistributionHandle{}, fmt.Errorf("NH ensure returned an empty task id for %s/%s",
			req.ModelName, req.Version)
	}
	o.log.Info("model distribution ensured",
		"task_id", task.TaskID, "object_id", task.ObjectID,
		"state", task.State, "replay", task.IdempotentReplay, "adopted", task.Adopted)
	return business.DistributionHandle{
		TaskID:    task.TaskID,
		State:     task.State,
		EdgeReady: task.EdgeReady,
		Replay:    task.IdempotentReplay,
		Adopted:   task.Adopted,
	}, nil
}

// safeObjectID derives a deterministic ESS object id from the model identity
// and precision. Determinism makes NL retries converge on the same NH
// idempotency key; precision is part of the id so fp16 and int8 packages never
// overwrite each other on the edge.
func safeObjectID(modelID, version, precision string) string {
	raw := strings.Join([]string{modelID, version, precision}, "-")
	return unsafeToken.ReplaceAllString(raw, "-")
}

// safeToken mirrors the NH isSafeObjectToken rule for a defensive local check.
func safeToken(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(".-~!@#$^*_", r):
		default:
			return false
		}
	}
	return true
}

func specText(spec map[string]any, key string) string {
	value, _ := spec[key].(string)
	return value
}
