// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// lifecycle.go defines the persisted instance workflow and health projection.
// The documents remain declarative; these types add only generation, ownership
// and observed-health metadata needed to deliver them safely.

package business

import (
	"context"
	"errors"
	"time"

	"github.com/nereidai/nereidlink/internal/connector"
)

// Instance lifecycle states stored in business_instance.
const (
	StatusDraft      = "DRAFT"
	StatusValidating = "VALIDATING"
	StatusDeploying  = "DEPLOYING"
	StatusRunning    = "RUNNING"
	StatusDegraded   = "DEGRADED"
	StatusFailed     = "FAILED"
	StatusUndeployed = "UNDEPLOYED"
)

// Expansion snapshot lifecycle states stored in business_expansion.
const (
	ExpansionDraft      = "DRAFT"
	ExpansionDeploying  = "DEPLOYING"
	ExpansionActive     = "ACTIVE"
	ExpansionFailed     = "FAILED"
	ExpansionUndeployed = "UNDEPLOYED"
)

// Domain errors mapped to stable REST responses by the gateway.
var (
	ErrNotFound           = errors.New("business resource not found")
	ErrGenerationConflict = errors.New("business instance generation conflict")
	ErrConflict           = errors.New("business resource conflict")
	ErrInvalid            = errors.New("business document invalid")
)

// Instance is one generation-fenced business assembly. TemplateSnapshot is
// persisted for expansion but deliberately omitted from the REST response.
type Instance struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	TemplateID        string           `json:"template_id"`
	TemplateVersion   string           `json:"template_version"`
	Status            string           `json:"status"`
	Generation        int64            `json:"generation"`
	ActiveGeneration  int64            `json:"active_generation"`
	PendingGeneration int64            `json:"pending_generation"`
	Parameters        map[string]any   `json:"parameters"`
	Assembly          Assembly         `json:"assembly"`
	Validation        ValidationReport `json:"validation"`
	LastError         string           `json:"last_error,omitempty"`
	CreatedAt         time.Time        `json:"created_at"`
	UpdatedAt         time.Time        `json:"updated_at"`
	DeployedAt        *time.Time       `json:"deployed_at,omitempty"`
	TemplateSnapshot  Template         `json:"-"`
}

// InstanceInput creates an instance from an immutable template version.
type InstanceInput struct {
	Name            string         `json:"name"`
	TemplateID      string         `json:"template_id"`
	TemplateVersion string         `json:"template_version"`
	Parameters      map[string]any `json:"parameters"`
	Assembly        Assembly       `json:"assembly"`
}

// InstanceUpdate replaces the desired document only when expected_generation
// still matches. Active and pending generations are never client-writable.
type InstanceUpdate struct {
	Name               string         `json:"name"`
	Parameters         map[string]any `json:"parameters"`
	Assembly           Assembly       `json:"assembly"`
	ExpectedGeneration int64          `json:"expected_generation"`
}

// Resource records the owner-specific identity created for one artifact. It
// contains no credentials or opaque secrets. OwnerID is a rule group when Kind
// is rule_chain; PreviousID preserves a device's former rule-group binding.
type Resource struct {
	ArtifactKey string `json:"artifact_key"`
	Kind        string `json:"kind"`
	ResourceID  string `json:"resource_id,omitempty"`
	OwnerID     string `json:"owner_id,omitempty"`
	PreviousID  string `json:"previous_id,omitempty"`
}

// LiveResource locates a shared owner mapping in an active/pending generation.
type LiveResource struct {
	Resource
	InstanceID string
	Generation int64
}

// LiveExpansion locates one active or pending expansion across instances.
type LiveExpansion struct {
	InstanceID string
	Generation int64
	ExpansionSnapshot
}

// ExpansionSnapshot is the durable declarative document plus its applied-owner
// mappings. Resources are retained after undeploy so retry and audit remain
// deterministic; an empty resource id denotes a validated external reference.
type ExpansionSnapshot struct {
	Expansion Expansion  `json:"expansion"`
	Resources []Resource `json:"resources"`
	Status    string     `json:"status"`
}

// DeploymentPatch changes lifecycle metadata while fencing on the latest
// desired generation. Nil generation pointers leave their columns unchanged.
type DeploymentPatch struct {
	Status              string
	LastError           string
	ActiveGeneration    *int64
	PendingGeneration   *int64
	ExpansionGeneration int64
	ExpansionStatus     string
	RequireNotDeploying bool
}

// Store is the persistence surface required by Service. PGStore satisfies it;
// tests can provide an in-memory implementation without PostgreSQL or NATS.
type Store interface {
	ListTemplates(ctx context.Context, tenantID string) ([]Template, error)
	CreateTemplate(ctx context.Context, tenantID, actor string, template Template) (Template, error)
	DeleteTemplate(ctx context.Context, tenantID, templateID, version string) error
	GetTemplate(ctx context.Context, tenantID, templateID, version string) (Template, error)
	ListInstances(ctx context.Context, tenantID string) ([]Instance, error)
	GetInstance(ctx context.Context, tenantID, instanceID string) (Instance, error)
	CreateInstance(ctx context.Context, tenantID string, instance Instance, expansion Expansion) (Instance, error)
	UpdateInstance(ctx context.Context, tenantID, instanceID string, update InstanceUpdate, expansion Expansion, report ValidationReport) (Instance, error)
	DeleteInstance(ctx context.Context, tenantID, instanceID string) error
	SaveValidation(ctx context.Context, tenantID, instanceID string, generation int64, report ValidationReport) error
	GetExpansion(ctx context.Context, tenantID, instanceID string, generation int64) (ExpansionSnapshot, error)
	FindResource(ctx context.Context, tenantID, instanceID, artifactKey string) (Resource, bool, error)
	FindLiveResource(ctx context.Context, tenantID, kind, ownerID string) (LiveResource, bool, error)
	ListLiveResources(ctx context.Context, tenantID, kind string) ([]LiveResource, error)
	ListLiveExpansions(ctx context.Context, tenantID string) ([]LiveExpansion, error)
	ResourceReferenced(ctx context.Context, tenantID string, resource Resource, excludeInstanceID string, excludeGeneration int64) (bool, error)
	RecordResource(ctx context.Context, tenantID, instanceID string, desiredGeneration int64, expansion Expansion, resource Resource) error
	UpdateDeployment(ctx context.Context, tenantID, instanceID string, desiredGeneration int64, patch DeploymentPatch) (Instance, error)
}

// CameraMonitor is the authoritative health and optional NI job-metric view of
// one generated inference binding. Nil metrics mean unavailable, never zero.
type CameraMonitor struct {
	NodeID          string   `json:"node_id"`
	DeviceID        string   `json:"device_id"`
	GatewayID       string   `json:"gateway_id"`
	State           string   `json:"state"`
	LastError       string   `json:"last_error,omitempty"`
	SuccessTotal    *uint64  `json:"success_total,omitempty"`
	ErrorTotal      *uint64  `json:"error_total,omitempty"`
	P50MS           *float64 `json:"p50_ms,omitempty"`
	P95MS           *float64 `json:"p95_ms,omitempty"`
	EventsPerSecond *float64 `json:"events_per_second,omitempty"`
	HealthSource    string   `json:"health_source"`
}

// Monitor is a point-in-time health projection. It is fetched on demand or
// after a server-push invalidation; the service never requires REST polling.
type Monitor struct {
	InstanceID       string          `json:"instance_id"`
	Generation       int64           `json:"generation"`
	ActiveGeneration int64           `json:"active_generation"`
	Status           string          `json:"status"`
	Cameras          []CameraMonitor `json:"cameras"`
	Issues           []Issue         `json:"issues"`
}

// JobMetrics contains optional per-binding NI counters and latency readings.
type JobMetrics = connector.JobMetrics

// JobMetricsReader resolves metrics by generated connector spec id. A missing
// reader or (false, nil) result means the readings are unavailable.
type JobMetricsReader interface {
	JobMetrics(ctx context.Context, tenantID, connectorSpecID string) (JobMetrics, bool, error)
}
