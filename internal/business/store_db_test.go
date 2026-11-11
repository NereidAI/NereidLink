// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// store_db_test.go exercises template deletion and instance-creation row locks
// against real PostgreSQL. It is opt-in through NL_TEST_DATABASE_URL and uses a
// throwaway tenant so the concurrency check cannot disturb other fixtures.

package business

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGStoreTemplateDeletionSerialization(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	tenantID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenant (id,name) VALUES ($1,$2)`,
		tenantID, "business-store-test-"+tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM tenant WHERE id=$1`, tenantID); err != nil {
			t.Errorf("clean up business store tenant: %v", err)
		}
	})

	store := NewStore(pool)
	template := Template{
		SchemaVersion: TemplateSchemaVersion,
		ID:            "delete-lock-test",
		Version:       "1.0.0",
		Name:          "Delete lock test",
		Requires:      map[string]string{},
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"retries": map[string]any{"type": "integer", "default": 1},
			},
		},
		Assembly: Assembly{Nodes: []Node{}, Edges: []Edge{}},
	}
	created, err := store.CreateTemplate(ctx, tenantID, "tester", template)
	if err != nil {
		t.Fatal(err)
	}
	instance := Instance{
		ID:               uuid.NewString(),
		Name:             "delete-lock-test-instance",
		TemplateID:       created.ID,
		TemplateVersion:  created.Version,
		TemplateSnapshot: created,
		Generation:       1,
		Parameters:       map[string]any{},
		Assembly:         created.Assembly,
	}
	expansion, report := Expand(created, ExpansionInput{
		InstanceID: instance.ID,
		Generation: instance.Generation,
		Parameters: instance.Parameters,
		Assembly:   instance.Assembly,
	})
	instance.Validation = report

	// Hold the same UPDATE lock used by deletion. CreateInstance must wait for
	// its SHARE lock instead of inserting an instance against a doomed template.
	blocker, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback(context.Background()) }()
	if _, err := lockTemplateVersion(ctx, blocker, tenantID, created.ID, created.Version, true); err != nil {
		t.Fatal(err)
	}
	createdResult := make(chan error, 1)
	go func() {
		_, err := store.CreateInstance(ctx, tenantID, instance, expansion)
		createdResult <- err
	}()
	if err := waitForTemplateShareLock(ctx, pool, createdResult); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(ctx,
		`DELETE FROM business_template WHERE tenant_id=$1 AND id=$2 AND version=$3`,
		tenantID, created.ID, created.Version); err != nil {
		t.Fatal(err)
	}
	if err := blocker.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-createdResult:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("create after template deletion = %v, want ErrNotFound", err)
		}
	case <-ctx.Done():
		t.Fatalf("instance creation did not finish after lock release: %v", ctx.Err())
	}
	var instanceCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM business_instance WHERE tenant_id=$1`,
		tenantID).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 0 {
		t.Fatalf("deleted template has %d newly created instances", instanceCount)
	}

	// The reverse ordering must preserve the template and reject deletion. Once
	// the instance is removed, the same custom version becomes safely deletable.
	created, err = store.CreateTemplate(ctx, tenantID, "tester", template)
	if err != nil {
		t.Fatal(err)
	}
	instance.TemplateSnapshot = created
	if _, err := store.CreateInstance(ctx, tenantID, instance, expansion); err != nil {
		t.Fatalf("create reference before deletion: %v", err)
	}
	if err := store.DeleteTemplate(ctx, tenantID, created.ID, created.Version); !errors.Is(err, ErrConflict) {
		t.Fatalf("delete referenced template = %v, want ErrConflict", err)
	}
	if err := store.DeleteInstance(ctx, tenantID, instance.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteTemplate(ctx, tenantID, created.ID, created.Version); err != nil {
		t.Fatalf("delete unused custom template: %v", err)
	}
	if err := store.DeleteTemplate(ctx, tenantID, created.ID, created.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete missing custom template = %v, want ErrNotFound", err)
	}

	// A delete-and-recreate may reuse the id+version, but an instance creator
	// that read the old document must not attach its stale snapshot to the new row.
	stale := created
	replacementDocument := template
	replacementDocument.Name = "Replacement document"
	replacement, err := store.CreateTemplate(ctx, tenantID, "tester", replacementDocument)
	if err != nil {
		t.Fatal(err)
	}
	instance.ID = uuid.NewString()
	instance.Name = "stale-template-instance"
	instance.TemplateSnapshot = stale
	expansion.InstanceID = instance.ID
	if _, err := store.CreateInstance(ctx, tenantID, instance, expansion); !errors.Is(err, ErrConflict) {
		t.Fatalf("create from stale template snapshot = %v, want ErrConflict", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM business_instance WHERE tenant_id=$1`,
		tenantID).Scan(&instanceCount); err != nil {
		t.Fatal(err)
	}
	if instanceCount != 0 {
		t.Fatalf("stale template created %d instances", instanceCount)
	}
	if err := store.DeleteTemplate(ctx, tenantID, replacement.ID, replacement.Version); err != nil {
		t.Fatalf("delete replacement template: %v", err)
	}
}

func TestPGStoreLiveResourceReferences(t *testing.T) {
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tenantID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO tenant(id,name) VALUES($1,$2)`, tenantID, "live-resource-"+tenantID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := pool.Exec(cleanupCtx, `DELETE FROM tenant WHERE id=$1`, tenantID); err != nil {
			t.Errorf("clean up live-resource tenant: %v", err)
		}
	})
	store := NewStore(pool)
	template := loadSampleTemplate(t, "face-access-control", "0.0.1")
	if _, err := store.CreateTemplate(ctx, tenantID, "db-test", template); err != nil {
		t.Fatalf("seed template for instance lock test: %v", err)
	}
	resource := Resource{ArtifactKey: "pipeline-registration:same-key", Kind: ArtifactKindPipelineRegistration,
		ResourceID: uuid.NewString(), OwnerID: "gateway|nereid-inference|PipelinesJSON|pipelineId|p1"}
	create := func(name string) (Instance, Expansion) {
		instance := Instance{ID: uuid.NewString(), Name: name, TemplateID: template.ID,
			TemplateVersion: template.Version, TemplateSnapshot: template, Generation: 1,
			Parameters: map[string]any{}, Assembly: Assembly{Nodes: []Node{}, Edges: []Edge{}}}
		expansion := Expansion{SchemaVersion: ExpansionSchemaVersion, InstanceID: instance.ID,
			Generation: 1, Artifacts: []Artifact{}, Digest: uuid.NewString()}
		created, err := store.CreateInstance(ctx, tenantID, instance, expansion)
		if err != nil {
			t.Fatal(err)
		}
		return created, expansion
	}
	setDeployment := func(instance Instance, generation int64, expansion Expansion, status string, active, pending int64, withResource bool) {
		t.Helper()
		if status == StatusDeploying {
			if _, err := store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
				Status: StatusDeploying, PendingGeneration: &generation, ExpansionGeneration: generation,
				ExpansionStatus: ExpansionDeploying, RequireNotDeploying: true,
			}); err != nil {
				t.Fatal(err)
			}
			if withResource {
				if err := store.RecordResource(ctx, tenantID, instance.ID, instance.Generation, expansion, resource); err != nil {
					t.Fatal(err)
				}
			}
			return
		}
		if _, err := store.UpdateDeployment(ctx, tenantID, instance.ID, instance.Generation, DeploymentPatch{
			Status: status, ActiveGeneration: &active, PendingGeneration: &pending,
			ExpansionGeneration: generation, ExpansionStatus: ExpansionActive,
		}); err != nil {
			t.Fatal(err)
		}
	}

	first, firstExpansion := create("live-first")
	setDeployment(first, 1, firstExpansion, StatusDeploying, 0, 1, true)
	setDeployment(first, 1, firstExpansion, StatusRunning, 1, 0, false)
	second, secondExpansion := create("live-second")
	setDeployment(second, 1, secondExpansion, StatusDeploying, 0, 1, false)
	setDeployment(second, 1, secondExpansion, StatusRunning, 1, 0, false)

	live, found, err := store.FindLiveResource(ctx, tenantID, resource.Kind, resource.OwnerID)
	if err != nil || !found || live.ResourceID != resource.ResourceID || live.InstanceID != first.ID {
		t.Fatalf("active shared identity not found: %+v found=%v err=%v", live, found, err)
	}
	if referenced, err := store.ResourceReferenced(ctx, tenantID, resource, first.ID, 1); err != nil || referenced {
		t.Fatalf("empty generation must not pin shared resource: referenced=%v err=%v", referenced, err)
	}

	nextExpansion := secondExpansion
	nextExpansion.Generation = 2
	nextExpansion.Digest = uuid.NewString()
	second, err = store.UpdateInstance(ctx, tenantID, second.ID, InstanceUpdate{
		Name: second.Name, Parameters: map[string]any{}, Assembly: second.Assembly, ExpectedGeneration: 1,
	}, nextExpansion, ValidationReport{Valid: true, Issues: []Issue{}})
	if err != nil {
		t.Fatal(err)
	}
	setDeployment(second, 2, nextExpansion, StatusDeploying, 0, 2, true)
	if referenced, err := store.ResourceReferenced(ctx, tenantID, resource, first.ID, 1); err != nil || !referenced {
		t.Fatalf("pending generation must protect shared resource: referenced=%v err=%v", referenced, err)
	}
	setDeployment(first, 1, firstExpansion, StatusUndeployed, 0, 0, false)
	if referenced, err := store.ResourceReferenced(ctx, tenantID, resource, first.ID, 1); err != nil || !referenced {
		t.Fatalf("historical first generation hid pending reference: referenced=%v err=%v", referenced, err)
	}
	setDeployment(second, 2, nextExpansion, StatusRunning, 2, 0, false)
	setDeployment(second, 2, nextExpansion, StatusUndeployed, 0, 0, false)
	if referenced, err := store.ResourceReferenced(ctx, tenantID, resource, first.ID, 1); err != nil || referenced {
		t.Fatalf("undeployed/historical generations must not pin resource: referenced=%v err=%v", referenced, err)
	}
	if _, found, err := store.FindResource(ctx, tenantID, first.ID, resource.ArtifactKey); err != nil || !found {
		t.Fatalf("instance-local stable lookup lost its resource: found=%v err=%v", found, err)
	}
	if _, found, err := store.FindResource(ctx, tenantID, second.ID, resource.ArtifactKey); err != nil || !found {
		t.Fatalf("same artifact key in second instance must have its own mapping: found=%v err=%v", found, err)
	}
}

// waitForTemplateShareLock observes PostgreSQL's actual tuple wait rather than
// relying on a sleep to assume the goroutine reached the contested row.
func waitForTemplateShareLock(ctx context.Context, pool *pgxpool.Pool, created <-chan error) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var waiting int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname=current_database() AND pid<>pg_backend_pid()
			  AND wait_event_type='Lock'
			  AND query LIKE '%business_template%'
			  AND query LIKE '%FOR SHARE%'`).Scan(&waiting); err != nil {
			return fmt.Errorf("inspect template row-lock wait: %w", err)
		}
		if waiting > 0 {
			return nil
		}
		select {
		case err := <-created:
			return fmt.Errorf("instance creation finished before lock release: %w", err)
		case <-ctx.Done():
			return fmt.Errorf("instance creation never waited for the template row: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
