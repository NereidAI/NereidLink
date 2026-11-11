// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// chainstore_test.go verifies the per-device chain resolution order:
// device binding, then profile binding, then the tenant default group,
// including the ON DELETE SET NULL fallback. It is an integration test
// and only runs when NEREIDLINK_TEST_DSN points at a PostgreSQL database.

package rule

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestChainForDeviceResolution exercises the binding priority of the
// device chain resolver against a real PostgreSQL database.
func TestChainForDeviceResolution(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping chain resolution integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tenantID := uuid.NewString()
	deviceID := uuid.NewString()
	profileID := uuid.NewString()
	defaultGroupID := uuid.NewString()
	profileGroupID := uuid.NewString()
	deviceGroupID := uuid.NewString()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'rule-chain-test')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		// Cascade removes groups, chains, profile and device.
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenantID)
	})

	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name)
		 VALUES ($1, $2, 'resolution-profile')`,
		profileID, tenantID); err != nil {
		t.Fatalf("create profile: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, profile_id)
		 VALUES ($1, $2, 'resolution-device', $3)`,
		deviceID, tenantID, profileID); err != nil {
		t.Fatalf("create device: %v", err)
	}

	groups := map[string]string{
		defaultGroupID: "default group",
		profileGroupID: "profile group",
		deviceGroupID:  "device group",
	}
	chains := map[string]string{}
	for groupID, name := range groups {
		isDefault := groupID == defaultGroupID
		if _, err := pool.Exec(ctx,
			`INSERT INTO rule_chain_group
			   (id, tenant_id, name, is_default)
			 VALUES ($1, $2, $3, $4)`,
			groupID, tenantID, name, isDefault); err != nil {
			t.Fatalf("create group %s: %v", name, err)
		}
		chainID := uuid.NewString()
		if _, err := pool.Exec(ctx,
			`INSERT INTO rule_chain
			   (id, tenant_id, name, version, state, is_root, chain_group_id)
			 VALUES ($1, $2, $3, 1, 'ACTIVE', $4, $5)`,
			chainID, tenantID, name, isDefault, groupID); err != nil {
			t.Fatalf("create chain %s: %v", name, err)
		}
		chains[groupID] = chainID
	}

	store := NewChainStore(pool)

	// Case 1: no bindings resolves to the tenant default group.
	assertResolvedGroup(t, ctx, store, tenantID, deviceID, defaultGroupID)

	// Case 2: profile binding replaces the default group.
	if _, err := pool.Exec(ctx,
		`UPDATE device_profile SET rule_chain_group_id = $2 WHERE id = $1`,
		profileID, profileGroupID); err != nil {
		t.Fatalf("bind profile: %v", err)
	}
	assertResolvedGroup(t, ctx, store, tenantID, deviceID, profileGroupID)

	// Case 3: device binding fully replaces the profile binding.
	if _, err := pool.Exec(ctx,
		`UPDATE device SET rule_chain_group_id = $2 WHERE id = $1`,
		deviceID, deviceGroupID); err != nil {
		t.Fatalf("bind device: %v", err)
	}
	assertResolvedGroup(t, ctx, store, tenantID, deviceID, deviceGroupID)

	// Case 4: dropping the device group sets the binding NULL and the
	// resolver falls back through the profile binding to the default.
	if _, err := pool.Exec(ctx,
		`DELETE FROM rule_chain_group WHERE id = $1`, deviceGroupID); err != nil {
		t.Fatalf("delete device group: %v", err)
	}
	assertResolvedGroup(t, ctx, store, tenantID, deviceID, profileGroupID)

	// Case 5: a group without an active version yields ErrNoActiveChain.
	if _, err := pool.Exec(ctx,
		`UPDATE rule_chain SET state = 'ARCHIVED'
		 WHERE chain_group_id = $1`, profileGroupID); err != nil {
		t.Fatalf("archive profile chain: %v", err)
	}
	if _, _, _, err := store.ChainForDevice(ctx, tenantID, deviceID); err != ErrNoActiveChain {
		t.Fatalf("want ErrNoActiveChain, got %v", err)
	}
}

// TestDeleteGroupProtection verifies that DeleteGroup refuses the tenant
// default group and any group still bound to a device or profile, that an
// unreferenced custom group is removed with its versions cascading, and
// that ListGroups reports the binding counts.
func TestDeleteGroupProtection(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping group delete integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tenantID := uuid.NewString()
	profileID := uuid.NewString()
	defaultGroupID := uuid.NewString()
	boundGroupID := uuid.NewString()
	freeGroupID := uuid.NewString()

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'rule-chain-delete-test')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenantID)
	})
	for _, spec := range []struct {
		id, name  string
		isDefault bool
	}{
		{defaultGroupID, "delete-default", true},
		{boundGroupID, "delete-bound", false},
		{freeGroupID, "delete-free", false},
	} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO rule_chain_group (id, tenant_id, name, is_default)
			 VALUES ($1, $2, $3, $4)`,
			spec.id, tenantID, spec.name, spec.isDefault); err != nil {
			t.Fatalf("create group %s: %v", spec.name, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO rule_chain
			   (id, tenant_id, name, version, state, is_root, chain_group_id)
			 VALUES ($1, $2, $3, 1, 'DRAFT', FALSE, $4)`,
			uuid.NewString(), tenantID, spec.name, spec.id); err != nil {
			t.Fatalf("create chain %s: %v", spec.name, err)
		}
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_profile (id, tenant_id, name, rule_chain_group_id)
		 VALUES ($1, $2, 'bound-profile', $3)`,
		profileID, tenantID, boundGroupID); err != nil {
		t.Fatalf("create profile: %v", err)
	}

	store := NewChainStore(pool)

	if err := store.DeleteGroup(ctx, tenantID, defaultGroupID); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("delete default: want ErrInvalidArgument, got %v", err)
	}
	if err := store.DeleteGroup(ctx, tenantID, boundGroupID); !errors.Is(err, ErrGroupInUse) {
		t.Fatalf("delete bound: want ErrGroupInUse, got %v", err)
	}

	groups, err := store.ListGroups(ctx, tenantID)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	counts := map[string]GroupMeta{}
	for _, group := range groups {
		counts[group.ID] = group
	}
	bound, ok := counts[boundGroupID]
	if !ok {
		t.Fatal("bound group missing from list")
	}
	if bound.ProfileCount != 1 || bound.DeviceCount != 0 {
		t.Fatalf("bound counts = devices %d profiles %d, want 0/1",
			bound.DeviceCount, bound.ProfileCount)
	}
	free, ok := counts[freeGroupID]
	if !ok {
		t.Fatal("free group missing from list")
	}
	if free.ProfileCount != 0 || free.DeviceCount != 0 {
		t.Fatalf("free counts = devices %d profiles %d, want 0/0",
			free.DeviceCount, free.ProfileCount)
	}

	if err := store.DeleteGroup(ctx, tenantID, freeGroupID); err != nil {
		t.Fatalf("delete free group: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM rule_chain_group WHERE id = $1`, freeGroupID).
		Scan(&remaining); err != nil {
		t.Fatalf("check removal: %v", err)
	}
	if remaining != 0 {
		t.Fatalf("free group still exists after delete")
	}
	var chainRemaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM rule_chain
		  WHERE tenant_id = $1 AND chain_group_id = $2`,
		tenantID, freeGroupID).Scan(&chainRemaining); err != nil {
		t.Fatalf("check cascade: %v", err)
	}
	if chainRemaining != 0 {
		t.Fatalf("free group versions not cascaded, %d remain", chainRemaining)
	}
}

// assertResolvedGroup validates that ChainForDevice resolves a device to
// the expected group and its active chain.
func assertResolvedGroup(t *testing.T, ctx context.Context, store *ChainStore, tenantID, deviceID, wantGroupID string) {
	t.Helper()
	groupID, chainID, _, err := store.ChainForDevice(ctx, tenantID, deviceID)
	if err != nil {
		t.Fatalf("resolve chain: %v", err)
	}
	if groupID != wantGroupID {
		t.Fatalf("resolved group %s, want %s", groupID, wantGroupID)
	}
	if chainID == "" {
		t.Fatal("resolved chain identifier is empty")
	}
}

// TestGraphReturnsEmptyArraysNotNil is the regression guard for the editor
// crash "Cannot read properties of null (reading 'map')": a chain version
// without node/edge rows must return non-nil empty slices so they JSON
// encode as [], never null.
func TestGraphReturnsEmptyArraysNotNil(t *testing.T) {
	dsn := os.Getenv("NEREIDLINK_TEST_DSN")
	if dsn == "" {
		t.Skip("NEREIDLINK_TEST_DSN not set; skipping graph empty-array integration test")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	tenantID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, 'rule-graph-test')`,
		tenantID); err != nil {
		t.Fatalf("create tenant: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM tenant WHERE id = $1`, tenantID) })

	groupID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO rule_chain_group (id, tenant_id, name, is_default)
		 VALUES ($1, $2, 'graph-test-group', FALSE)`,
		groupID, tenantID); err != nil {
		t.Fatalf("create group: %v", err)
	}

	chainID := uuid.NewString()
	if _, err := pool.Exec(ctx,
		`INSERT INTO rule_chain
		   (id, tenant_id, name, version, state, is_root, chain_group_id)
		 VALUES ($1, $2, 'no-nodes-chain', 1, 'DRAFT', FALSE, $3)`,
		chainID, tenantID, groupID); err != nil {
		t.Fatalf("create empty chain: %v", err)
	}

	dto, err := NewChainStore(pool).Graph(ctx, chainID)
	if err != nil {
		t.Fatalf("load graph: %v", err)
	}
	if dto.Nodes == nil || dto.Edges == nil {
		t.Fatalf("empty segments must be non-nil slices, got nodes=%v edges=%v",
			dto.Nodes, dto.Edges)
	}
	if len(dto.Nodes) != 0 || len(dto.Edges) != 0 {
		t.Fatalf("expected empty graph, got %d nodes %d edges",
			len(dto.Nodes), len(dto.Edges))
	}

	// CreateDraft with an absent edge segment must echo [] too.
	_, echoed, err := NewChainStore(pool).CreateDraft(ctx, tenantID, groupID, GraphDTO{
		Nodes: []NodeDTO{{ID: "local-1", Type: "log", Name: "log"}},
	})
	if err != nil {
		t.Fatalf("create draft: %v", err)
	}
	if echoed.Edges == nil {
		t.Fatal("CreateDraft must normalize a nil edge segment to []")
	}
}
