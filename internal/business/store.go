// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store.go persists business documents and generation-fenced lifecycle state.
// Every statement is tenant-scoped, and every mutable desired write leaves an
// immutable expansion snapshot for retry, rollback and audit.

package business

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStore is the PostgreSQL-backed business orchestration store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore constructs the business orchestration PostgreSQL store.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// ListTemplates returns the tenant's imported template versions.
func (s *PGStore) ListTemplates(ctx context.Context, tenantID string) ([]Template, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT document, created_at FROM business_template
		 WHERE tenant_id=$1 ORDER BY id, version`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list business templates: %w", err)
	}
	defer rows.Close()
	out := make([]Template, 0)
	for rows.Next() {
		template, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, template)
	}
	return out, rows.Err()
}

// CreateTemplate inserts one immutable custom version. The primary key and the
// absence of an update route prevent in-place edits; deletion is reference-safe.
func (s *PGStore) CreateTemplate(ctx context.Context, tenantID, actor string, template Template) (Template, error) {
	template.CreatedAt = nil
	document, err := json.Marshal(template)
	if err != nil {
		return Template{}, fmt.Errorf("encode business template: %w", err)
	}
	var createdAt = template.CreatedAt
	err = s.pool.QueryRow(ctx,
		`INSERT INTO business_template
		   (tenant_id,id,version,name,description,document,created_by)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7)
		 RETURNING created_at`,
		tenantID, template.ID, template.Version, template.Name,
		template.Description, document, actor).Scan(&createdAt)
	if isBusinessUniqueViolation(err) {
		return Template{}, fmt.Errorf("%w: template %s/%s already exists", ErrConflict, template.ID, template.Version)
	}
	if err != nil {
		return Template{}, fmt.Errorf("create business template: %w", err)
	}
	template.CreatedAt = createdAt
	return template, nil
}

// DeleteTemplate removes an unreferenced custom version while serializing
// against concurrent instance creation on the same template row.
func (s *PGStore) DeleteTemplate(ctx context.Context, tenantID, templateID, version string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin business template delete: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := lockTemplateVersion(ctx, tx, tenantID, templateID, version, true); err != nil {
		return err
	}
	var references int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM business_instance
		 WHERE tenant_id=$1 AND template_id=$2 AND template_version=$3`,
		tenantID, templateID, version).Scan(&references); err != nil {
		return fmt.Errorf("count business template references: %w", err)
	}
	if references > 0 {
		return fmt.Errorf("%w: template %s/%s is referenced by %d instance(s)",
			ErrConflict, templateID, version, references)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM business_template WHERE tenant_id=$1 AND id=$2 AND version=$3`,
		tenantID, templateID, version); err != nil {
		return fmt.Errorf("delete business template: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit business template delete: %w", err)
	}
	return nil
}

// lockTemplateVersion serializes custom-template deletion with instance
// creation. FOR UPDATE excludes both operations; FOR SHARE allows independent
// instance creators while blocking a delete until their references commit. The
// returned document is read under that same lock, not from a stale earlier read.
func lockTemplateVersion(ctx context.Context, tx pgx.Tx, tenantID, templateID, version string, forUpdate bool) (Template, error) {
	lock := "SHARE"
	if forUpdate {
		lock = "UPDATE"
	}
	template, err := scanTemplate(tx.QueryRow(ctx,
		`SELECT document,created_at FROM business_template
		 WHERE tenant_id=$1 AND id=$2 AND version=$3 FOR `+lock,
		tenantID, templateID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, fmt.Errorf("%w: template %s/%s", ErrNotFound, templateID, version)
	}
	if err != nil {
		return Template{}, fmt.Errorf("lock business template: %w", err)
	}
	return template, nil
}

// sameTemplateDocument ignores persistence timestamps but compares every
// semantic field. It detects delete-and-recreate between the service's initial
// read and the locked insert, which must not attach an old snapshot to a new row.
func sameTemplateDocument(a, b Template) bool {
	a.CreatedAt, b.CreatedAt = nil, nil
	a.Requires, b.Requires = nonnilStringMap(a.Requires), nonnilStringMap(b.Requires)
	a.Parameters, b.Parameters = nonnilMap(a.Parameters), nonnilMap(b.Parameters)
	a.Assembly, b.Assembly = normalizedAssembly(a.Assembly), normalizedAssembly(b.Assembly)
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	// JSON comparison also treats an in-memory integer and its PostgreSQL JSON
	// number round trip as the same semantic value.
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

// GetTemplate loads one custom version scoped by tenant.
func (s *PGStore) GetTemplate(ctx context.Context, tenantID, templateID, version string) (Template, error) {
	template, err := scanTemplate(s.pool.QueryRow(ctx,
		`SELECT document, created_at FROM business_template
		 WHERE tenant_id=$1 AND id=$2 AND version=$3`, tenantID, templateID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return Template{}, fmt.Errorf("%w: template %s/%s", ErrNotFound, templateID, version)
	}
	return template, err
}

func scanTemplate(row pgx.Row) (Template, error) {
	var (
		raw       []byte
		createdAt = new(time.Time)
	)
	if err := row.Scan(&raw, createdAt); err != nil {
		return Template{}, err
	}
	var template Template
	if err := json.Unmarshal(raw, &template); err != nil {
		return Template{}, fmt.Errorf("decode business template: %w", err)
	}
	template.CreatedAt = createdAt
	template.Requires = nonnilStringMap(template.Requires)
	template.Parameters = nonnilMap(template.Parameters)
	template.Assembly = normalizedAssembly(template.Assembly)
	return template, nil
}

const businessInstanceColumns = `
	id::text,name,template_id,template_version,status,generation,
	active_generation,pending_generation,parameters,assembly,validation,
	last_error,created_at,updated_at,deployed_at,template_document`

// ListInstances returns newest edited instances first and always emits an
// array, including when the tenant has no instances.
func (s *PGStore) ListInstances(ctx context.Context, tenantID string) ([]Instance, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+businessInstanceColumns+` FROM business_instance
		 WHERE tenant_id=$1 ORDER BY updated_at DESC,id`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list business instances: %w", err)
	}
	defer rows.Close()
	out := make([]Instance, 0)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, instance)
	}
	return out, rows.Err()
}

// GetInstance loads an instance and its immutable template snapshot.
func (s *PGStore) GetInstance(ctx context.Context, tenantID, instanceID string) (Instance, error) {
	instance, err := scanInstance(s.pool.QueryRow(ctx,
		`SELECT `+businessInstanceColumns+` FROM business_instance
		 WHERE tenant_id=$1 AND id=$2`, tenantID, instanceID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, fmt.Errorf("%w: instance %s", ErrNotFound, instanceID)
	}
	return instance, err
}

func scanInstance(row pgx.Row) (Instance, error) {
	var (
		instance                         Instance
		parameters, assembly, validation []byte
		templateDocument                 []byte
	)
	if err := row.Scan(&instance.ID, &instance.Name, &instance.TemplateID,
		&instance.TemplateVersion, &instance.Status, &instance.Generation,
		&instance.ActiveGeneration, &instance.PendingGeneration,
		&parameters, &assembly, &validation, &instance.LastError,
		&instance.CreatedAt, &instance.UpdatedAt, &instance.DeployedAt,
		&templateDocument); err != nil {
		return Instance{}, err
	}
	if err := json.Unmarshal(parameters, &instance.Parameters); err != nil {
		return Instance{}, fmt.Errorf("decode business parameters: %w", err)
	}
	if err := json.Unmarshal(assembly, &instance.Assembly); err != nil {
		return Instance{}, fmt.Errorf("decode business assembly: %w", err)
	}
	if err := json.Unmarshal(validation, &instance.Validation); err != nil {
		return Instance{}, fmt.Errorf("decode business validation: %w", err)
	}
	if err := json.Unmarshal(templateDocument, &instance.TemplateSnapshot); err != nil {
		return Instance{}, fmt.Errorf("decode business template snapshot: %w", err)
	}
	instance.Parameters = nonnilMap(instance.Parameters)
	instance.Assembly = normalizedAssembly(instance.Assembly)
	instance.Validation.Issues = nonnilIssues(instance.Validation.Issues)
	return instance, nil
}

// CreateInstance persists generation one and its expansion atomically.
func (s *PGStore) CreateInstance(ctx context.Context, tenantID string, instance Instance, expansion Expansion) (Instance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Instance{}, fmt.Errorf("begin business instance create: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := lockTemplateVersion(ctx, tx, tenantID, instance.TemplateID, instance.TemplateVersion, false)
	if err != nil {
		return Instance{}, err
	}
	if !sameTemplateDocument(locked, instance.TemplateSnapshot) {
		return Instance{}, fmt.Errorf("%w: template %s/%s changed during instance creation; retry",
			ErrConflict, instance.TemplateID, instance.TemplateVersion)
	}
	created, err := insertInstance(ctx, tx, tenantID, instance)
	if err != nil {
		return Instance{}, err
	}
	if err := insertExpansion(ctx, tx, tenantID, instance.ID, expansion); err != nil {
		return Instance{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Instance{}, fmt.Errorf("commit business instance create: %w", err)
	}
	return created, nil
}

func insertInstance(ctx context.Context, tx pgx.Tx, tenantID string, instance Instance) (Instance, error) {
	parameters, _ := json.Marshal(nonnilMap(instance.Parameters))
	assembly, _ := json.Marshal(normalizedAssembly(instance.Assembly))
	validation, _ := json.Marshal(normalizeReport(instance.Validation))
	templateDocument, err := json.Marshal(instance.TemplateSnapshot)
	if err != nil {
		return Instance{}, fmt.Errorf("encode template snapshot: %w", err)
	}
	created, err := scanInstance(tx.QueryRow(ctx,
		`INSERT INTO business_instance
		   (id,tenant_id,name,template_id,template_version,template_document,
		    parameters,assembly,status,generation,active_generation,
		    pending_generation,validation,last_error)
		 VALUES ($1,$2,$3,$4,$5,$6::jsonb,$7::jsonb,$8::jsonb,'DRAFT',1,0,0,
		         $9::jsonb,'')
		 RETURNING `+businessInstanceColumns,
		instance.ID, tenantID, instance.Name, instance.TemplateID,
		instance.TemplateVersion, templateDocument, parameters, assembly, validation))
	if isBusinessUniqueViolation(err) {
		return Instance{}, fmt.Errorf("%w: instance name already exists", ErrConflict)
	}
	return created, err
}

func insertExpansion(ctx context.Context, tx pgx.Tx, tenantID, instanceID string, expansion Expansion) error {
	document, err := json.Marshal(expansion)
	if err != nil {
		return fmt.Errorf("encode business expansion: %w", err)
	}
	_, err = tx.Exec(ctx,
		`INSERT INTO business_expansion
		   (tenant_id,instance_id,generation,digest,document,resources,status)
		 VALUES ($1,$2,$3,$4,$5::jsonb,'[]'::jsonb,'DRAFT')`,
		tenantID, instanceID, expansion.Generation, expansion.Digest, document)
	if err != nil {
		return fmt.Errorf("insert business expansion: %w", err)
	}
	return nil
}

// UpdateInstance advances exactly one desired generation and writes its
// expansion in the same transaction. Deploying instances reject edits rather
// than allowing an in-flight owner mutation to become unaccounted for.
func (s *PGStore) UpdateInstance(ctx context.Context, tenantID, instanceID string,
	update InstanceUpdate, expansion Expansion, report ValidationReport) (Instance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Instance{}, fmt.Errorf("begin business instance update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	parameters, _ := json.Marshal(nonnilMap(update.Parameters))
	assembly, _ := json.Marshal(normalizedAssembly(update.Assembly))
	validation, _ := json.Marshal(normalizeReport(report))
	updated, err := scanInstance(tx.QueryRow(ctx,
		`UPDATE business_instance SET
		   name=$3,parameters=$4::jsonb,assembly=$5::jsonb,
		   validation=$6::jsonb,generation=generation+1,status='DRAFT',
		   pending_generation=0,last_error='',updated_at=now()
		 WHERE tenant_id=$1 AND id=$2 AND generation=$7 AND status<>'DEPLOYING'
		 RETURNING `+businessInstanceColumns,
		tenantID, instanceID, update.Name, parameters, assembly, validation,
		update.ExpectedGeneration))
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, fmt.Errorf("%w: expected generation %d", ErrGenerationConflict, update.ExpectedGeneration)
	}
	if isBusinessUniqueViolation(err) {
		return Instance{}, fmt.Errorf("%w: instance name already exists", ErrConflict)
	}
	if err != nil {
		return Instance{}, err
	}
	if updated.Generation != expansion.Generation {
		return Instance{}, fmt.Errorf("%w: expansion generation does not match updated instance", ErrGenerationConflict)
	}
	if err := insertExpansion(ctx, tx, tenantID, instanceID, expansion); err != nil {
		return Instance{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Instance{}, fmt.Errorf("commit business instance update: %w", err)
	}
	return updated, nil
}

// DeleteInstance removes only an instance with no active or pending resources;
// Service verifies that lifecycle precondition before calling this method.
func (s *PGStore) DeleteInstance(ctx context.Context, tenantID, instanceID string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM business_instance WHERE tenant_id=$1 AND id=$2`, tenantID, instanceID)
	if err != nil {
		return fmt.Errorf("delete business instance: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: instance %s", ErrNotFound, instanceID)
	}
	return nil
}

// SaveValidation replaces only the report of the current desired generation.
func (s *PGStore) SaveValidation(ctx context.Context, tenantID, instanceID string,
	generation int64, report ValidationReport) error {
	raw, _ := json.Marshal(normalizeReport(report))
	tag, err := s.pool.Exec(ctx,
		`UPDATE business_instance SET validation=$4::jsonb,updated_at=now()
		 WHERE tenant_id=$1 AND id=$2 AND generation=$3`,
		tenantID, instanceID, generation, raw)
	if err != nil {
		return fmt.Errorf("save business validation: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("%w: validation generation %d", ErrGenerationConflict, generation)
	}
	return nil
}

// GetExpansion loads one retained immutable snapshot.
func (s *PGStore) GetExpansion(ctx context.Context, tenantID, instanceID string,
	generation int64) (ExpansionSnapshot, error) {
	var (
		snapshot            ExpansionSnapshot
		document, resources []byte
	)
	err := s.pool.QueryRow(ctx,
		`SELECT document,resources,status FROM business_expansion
		 WHERE tenant_id=$1 AND instance_id=$2 AND generation=$3`,
		tenantID, instanceID, generation).Scan(&document, &resources, &snapshot.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExpansionSnapshot{}, fmt.Errorf("%w: expansion generation %d", ErrNotFound, generation)
	}
	if err != nil {
		return ExpansionSnapshot{}, fmt.Errorf("get business expansion: %w", err)
	}
	if err := json.Unmarshal(document, &snapshot.Expansion); err != nil {
		return ExpansionSnapshot{}, fmt.Errorf("decode business expansion: %w", err)
	}
	if err := json.Unmarshal(resources, &snapshot.Resources); err != nil {
		return ExpansionSnapshot{}, fmt.Errorf("decode business resources: %w", err)
	}
	snapshot.Expansion.Artifacts = nonnilArtifacts(snapshot.Expansion.Artifacts)
	snapshot.Resources = nonnilResources(snapshot.Resources)
	return snapshot, nil
}

// FindResource locates the newest owner mapping for a stable artifact key.
// Reusing it lets a new generation update the same connector/rule group rather
// than creating unowned duplicates.
func (s *PGStore) FindResource(ctx context.Context, tenantID, instanceID, artifactKey string) (Resource, bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT resources FROM business_expansion
		 WHERE tenant_id=$1 AND instance_id=$2 ORDER BY generation DESC`,
		tenantID, instanceID)
	if err != nil {
		return Resource{}, false, fmt.Errorf("find business resource: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return Resource{}, false, err
		}
		var resources []Resource
		if err := json.Unmarshal(raw, &resources); err != nil {
			return Resource{}, false, fmt.Errorf("decode business resources: %w", err)
		}
		for _, resource := range resources {
			if resource.ArtifactKey == artifactKey {
				return resource, true, nil
			}
		}
	}
	return Resource{}, false, rows.Err()
}

// liveResources reads only active/pending generations. Retained historical
// snapshots support rollback lookup but do not pin shared resources forever.
func (s *PGStore) liveResources(ctx context.Context, tenantID string) ([]LiveResource, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id::text,e.generation,e.resources
		 FROM business_instance i
		 JOIN business_expansion e
		   ON e.tenant_id=i.tenant_id AND e.instance_id=i.id
		  AND e.generation IN (i.active_generation,i.pending_generation)
		 WHERE i.tenant_id=$1
		 ORDER BY i.id,e.generation`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list live business resources: %w", err)
	}
	defer rows.Close()
	out := []LiveResource{}
	for rows.Next() {
		var instanceID string
		var generation int64
		var raw []byte
		if err := rows.Scan(&instanceID, &generation, &raw); err != nil {
			return nil, err
		}
		var resources []Resource
		if err := json.Unmarshal(raw, &resources); err != nil {
			return nil, fmt.Errorf("decode live business resources: %w", err)
		}
		for _, resource := range resources {
			out = append(out, LiveResource{Resource: resource, InstanceID: instanceID, Generation: generation})
		}
	}
	return out, rows.Err()
}

func (s *PGStore) FindLiveResource(ctx context.Context, tenantID, kind, ownerID string) (LiveResource, bool, error) {
	if ownerID == "" {
		return LiveResource{}, false, nil
	}
	resources, err := s.liveResources(ctx, tenantID)
	if err != nil {
		return LiveResource{}, false, err
	}
	for _, resource := range resources {
		if resource.Kind == kind && resource.OwnerID == ownerID {
			return resource, true, nil
		}
	}
	return LiveResource{}, false, nil
}

func (s *PGStore) ListLiveResources(ctx context.Context, tenantID, kind string) ([]LiveResource, error) {
	resources, err := s.liveResources(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make([]LiveResource, 0, len(resources))
	for _, resource := range resources {
		if resource.Kind == kind {
			out = append(out, resource)
		}
	}
	return out, nil
}

func (s *PGStore) ListLiveExpansions(ctx context.Context, tenantID string) ([]LiveExpansion, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT i.id::text,e.generation,e.document,e.resources,e.status
		 FROM business_instance i
		 JOIN business_expansion e
		   ON e.tenant_id=i.tenant_id AND e.instance_id=i.id
		  AND e.generation IN (i.active_generation,i.pending_generation)
		 WHERE i.tenant_id=$1
		 ORDER BY i.id,e.generation`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list live business expansions: %w", err)
	}
	defer rows.Close()
	out := make([]LiveExpansion, 0)
	for rows.Next() {
		var live LiveExpansion
		var document, resources []byte
		if err := rows.Scan(&live.InstanceID, &live.Generation, &document, &resources, &live.Status); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(document, &live.Expansion); err != nil {
			return nil, fmt.Errorf("decode live business expansion: %w", err)
		}
		if err := json.Unmarshal(resources, &live.Resources); err != nil {
			return nil, fmt.Errorf("decode live business resources: %w", err)
		}
		live.Expansion.Artifacts = nonnilArtifacts(live.Expansion.Artifacts)
		live.Resources = nonnilResources(live.Resources)
		out = append(out, live)
	}
	return out, rows.Err()
}

func (s *PGStore) ResourceReferenced(ctx context.Context, tenantID string, resource Resource, excludeInstanceID string, excludeGeneration int64) (bool, error) {
	resources, err := s.liveResources(ctx, tenantID)
	if err != nil {
		return false, err
	}
	return resourceReferenced(resources, resource, excludeInstanceID, excludeGeneration), nil
}

func resourceReferenced(resources []LiveResource, target Resource, excludeInstanceID string, excludeGeneration int64) bool {
	for _, candidate := range resources {
		if candidate.InstanceID == excludeInstanceID && candidate.Generation == excludeGeneration {
			continue
		}
		if candidate.Kind != target.Kind {
			continue
		}
		if target.ResourceID != "" && candidate.ResourceID == target.ResourceID {
			return true
		}
		if target.OwnerID != "" && candidate.OwnerID == target.OwnerID {
			return true
		}
	}
	return false
}

// RecordResource commits one applied-owner mapping only while the expected
// deployment is still current. This closes the stale-worker persistence gap.
func (s *PGStore) RecordResource(ctx context.Context, tenantID, instanceID string,
	desiredGeneration int64, expansion Expansion, resource Resource) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin business resource record: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var current, pending int64
	var status string
	if err := tx.QueryRow(ctx,
		`SELECT generation,pending_generation,status FROM business_instance
		 WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, tenantID, instanceID).
		Scan(&current, &pending, &status); err != nil {
		return fmt.Errorf("lock business deployment: %w", err)
	}
	if current != desiredGeneration || pending != expansion.Generation || status != StatusDeploying {
		return fmt.Errorf("%w: deployment is no longer current", ErrGenerationConflict)
	}
	var raw []byte
	if err := tx.QueryRow(ctx,
		`SELECT resources FROM business_expansion
		 WHERE tenant_id=$1 AND instance_id=$2 AND generation=$3 FOR UPDATE`,
		tenantID, instanceID, expansion.Generation).Scan(&raw); err != nil {
		return fmt.Errorf("lock business expansion resources: %w", err)
	}
	var resources []Resource
	if err := json.Unmarshal(raw, &resources); err != nil {
		return fmt.Errorf("decode business resources: %w", err)
	}
	replaced := false
	for index := range resources {
		if resources[index].ArtifactKey == resource.ArtifactKey {
			resources[index] = resource
			replaced = true
			break
		}
	}
	if !replaced {
		resources = append(resources, resource)
	}
	encoded, _ := json.Marshal(nonnilResources(resources))
	if _, err := tx.Exec(ctx,
		`UPDATE business_expansion SET resources=$4::jsonb
		 WHERE tenant_id=$1 AND instance_id=$2 AND generation=$3`,
		tenantID, instanceID, expansion.Generation, encoded); err != nil {
		return fmt.Errorf("record business resource: %w", err)
	}
	return tx.Commit(ctx)
}

// UpdateDeployment changes instance and selected-expansion lifecycle state in
// one transaction. ActiveGeneration changes only when the caller has completed
// health read-back and supplies an explicit pointer.
func (s *PGStore) UpdateDeployment(ctx context.Context, tenantID, instanceID string,
	desiredGeneration int64, patch DeploymentPatch) (Instance, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Instance{}, fmt.Errorf("begin business deployment update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	updated, err := scanInstance(tx.QueryRow(ctx,
		`UPDATE business_instance SET
		   status=$4,last_error=$5,
		   active_generation=COALESCE($6::bigint,active_generation),
		   pending_generation=COALESCE($7::bigint,pending_generation),
		   deployed_at=CASE WHEN $4 IN ('DEPLOYING','RUNNING','DEGRADED')
		                    THEN COALESCE(deployed_at,now()) ELSE deployed_at END,
		   updated_at=now()
		 WHERE tenant_id=$1 AND id=$2 AND generation=$3
		   AND (NOT $8::boolean OR status<>'DEPLOYING')
		 RETURNING `+businessInstanceColumns,
		tenantID, instanceID, desiredGeneration, patch.Status, patch.LastError,
		optionalGeneration(patch.ActiveGeneration), optionalGeneration(patch.PendingGeneration),
		patch.RequireNotDeploying))
	if errors.Is(err, pgx.ErrNoRows) {
		return Instance{}, fmt.Errorf("%w: deployment generation %d", ErrGenerationConflict, desiredGeneration)
	}
	if err != nil {
		return Instance{}, err
	}
	if patch.ExpansionGeneration > 0 && patch.ExpansionStatus != "" {
		if _, err := tx.Exec(ctx,
			`UPDATE business_expansion SET status=$4
			 WHERE tenant_id=$1 AND instance_id=$2 AND generation=$3`,
			tenantID, instanceID, patch.ExpansionGeneration, patch.ExpansionStatus); err != nil {
			return Instance{}, fmt.Errorf("update business expansion status: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Instance{}, fmt.Errorf("commit business deployment update: %w", err)
	}
	return updated, nil
}

func optionalGeneration(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func isBusinessUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func normalizeReport(report ValidationReport) ValidationReport {
	report.Issues = nonnilIssues(report.Issues)
	return report
}

func nonnilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}

func nonnilStringMap(value map[string]string) map[string]string {
	if value == nil {
		return map[string]string{}
	}
	return value
}

func nonnilIssues(value []Issue) []Issue {
	if value == nil {
		return []Issue{}
	}
	return value
}

func nonnilArtifacts(value []Artifact) []Artifact {
	if value == nil {
		return []Artifact{}
	}
	return value
}

func nonnilResources(value []Resource) []Resource {
	if value == nil {
		return []Resource{}
	}
	return value
}
