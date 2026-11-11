// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// write_scope_db_test.go proves against a real PostgreSQL that every
// device-derived write action is narrowed by the caller's data scope, not just
// the read surface. ListDevices/GetDevice were already scoped; the lifecycle,
// ownership, credential and quarantine writes historically read the device by
// tenant id only, which let a customer-scoped engineer act on another
// customer's device. Each case below asserts the action fails as not-found and
// leaves the foreign row unchanged, while an unrestricted caller still
// succeeds.
//
// The negative cases deliberately distinguish "invisible" (ErrNotFound) from
// "visible but wrong state" (ErrConflict): on an ACTIVE foreign device an
// unscoped state-only check would return the conflict, so observing
// ErrNotFound proves the scope predicate runs before the state gate.

package registry

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// writeScopeFixture holds one tenant, two customers and one ACTIVE device per
// customer.
type writeScopeFixture struct {
	pool   *pgxpool.Pool
	admin  *Admin
	tenant string
	custA  string
	custB  string
	devA   string
	devB   string
}

func newWriteScopeFixture(t *testing.T) *writeScopeFixture {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	f := &writeScopeFixture{
		pool:   pool,
		admin:  NewAdmin(pool),
		tenant: uuid.NewString(),
		custA:  uuid.NewString(),
		custB:  uuid.NewString(),
	}
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("fixture %q: %v", query, err)
		}
	}
	must(`INSERT INTO tenant (id, name) VALUES ($1,'write-scope')`, f.tenant)
	must(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,'A'),($3,$2,'B')`,
		f.custA, f.tenant, f.custB)

	devA, err := f.admin.CreateDevice(ctx, f.tenant, DeviceDTO{
		CanonicalName: "scope-a", State: StateActive, CustomerID: f.custA,
	})
	if err != nil {
		t.Fatal(err)
	}
	devB, err := f.admin.CreateDevice(ctx, f.tenant, DeviceDTO{
		CanonicalName: "scope-b", State: StateActive, CustomerID: f.custB,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.devA, f.devB = devA.ID, devB.ID

	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM device WHERE tenant_id = $1`,
			`DELETE FROM customer WHERE tenant_id = $1`,
			`DELETE FROM tenant WHERE id = $1`,
		} {
			_, _ = pool.Exec(context.Background(), q, f.tenant)
		}
	})
	return f
}

// customerAScope is a CUSTOMER data scope anchored on customer A.
func (f *writeScopeFixture) customerAScope() rbac.DataScope {
	return rbac.DataScope{
		TenantID:   f.tenant,
		Level:      rbac.ScopeCustomer,
		CustomerID: f.custA,
	}
}

// deviceState reads the lifecycle state directly so a test can prove a denied
// action did not mutate the foreign row.
func (f *writeScopeFixture) deviceState(t *testing.T, id string) string {
	t.Helper()
	var state string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT state FROM device WHERE id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

// deviceOwner reads the owning customer directly.
func (f *writeScopeFixture) deviceOwner(t *testing.T, id string) string {
	t.Helper()
	var owner *string
	if err := f.pool.QueryRow(context.Background(),
		`SELECT customer_id::text FROM device WHERE id = $1`, id).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner == nil {
		return ""
	}
	return *owner
}

// TestWriteScopeGetDevice establishes the baseline: a customer scope sees its
// own device, gets not-found for the other customer's, while a missing scope
// (system level) is unrestricted.
func TestWriteScopeGetDevice(t *testing.T) {
	f := newWriteScopeFixture(t)
	ctx := rbac.ContextWithScope(context.Background(), f.customerAScope())

	if _, err := f.admin.GetDevice(ctx, f.tenant, f.devA); err != nil {
		t.Fatalf("own device visible: %v", err)
	}
	if _, err := f.admin.GetDevice(ctx, f.tenant, f.devB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign device want ErrNotFound, got %v", err)
	}
	if _, err := f.admin.GetDevice(context.Background(), f.tenant, f.devB); err != nil {
		t.Fatalf("system level must remain unrestricted: %v", err)
	}
}

// TestWriteScopeLifecycleMutations exercises every write reached through the
// shared deviceState/loadDevice/locked-state chokepoints.
func TestWriteScopeLifecycleMutations(t *testing.T) {
	f := newWriteScopeFixture(t)
	ctx := rbac.ContextWithScope(context.Background(), f.customerAScope())

	// UpdateDevice: the foreign device must look missing, not merely
	// un-updatable.
	if _, err := f.admin.UpdateDevice(ctx, f.tenant, f.devB, DeviceDTO{
		CanonicalName: "hijacked",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateDevice foreign want ErrNotFound, got %v", err)
	}

	// IssueClaimToken on an ACTIVE foreign device: a tenant-only state check
	// would return a conflict; not-found proves the scope gate wins.
	if _, err := f.admin.IssueClaimToken(ctx, f.tenant, f.devB, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("IssueClaimToken foreign want ErrNotFound, got %v", err)
	}

	// DecommissionDevice must be refused and leave the foreign device ACTIVE.
	if _, err := f.admin.DecommissionDevice(ctx, f.tenant, f.devB, defaultRecallWindow, "actor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DecommissionDevice foreign want ErrNotFound, got %v", err)
	}
	if got := f.deviceState(t, f.devB); got != StateActive {
		t.Fatalf("foreign device state changed to %q despite denial", got)
	}

	// ReactivateDevice on an ACTIVE foreign device returns not-found (scope),
	// not the "not decommissioned" conflict a tenant-only read would give.
	if _, err := f.admin.ReactivateDevice(ctx, f.tenant, f.devB, "actor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReactivateDevice foreign want ErrNotFound, got %v", err)
	}

	// QuarantineDevice refuses and leaves the row ACTIVE.
	if _, err := f.admin.QuarantineDevice(ctx, f.tenant, f.devB,
		ReasonCredentialCompromise, "note", "actor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("QuarantineDevice foreign want ErrNotFound, got %v", err)
	}
	if got := f.deviceState(t, f.devB); got != StateActive {
		t.Fatalf("foreign device quarantined to %q despite denial", got)
	}

	// Ownership history of a foreign device is not readable.
	if _, err := f.admin.ListOwnershipEvents(ctx, f.tenant, f.devB, 1, 20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListOwnershipEvents foreign want ErrNotFound, got %v", err)
	}
}

// TestWriteScopeTransferTakeover guards the most severe case: moving another
// customer's device under the caller's own customer. The transfer must fail as
// not-found and leave ownership untouched.
func TestWriteScopeTransferTakeover(t *testing.T) {
	f := newWriteScopeFixture(t)
	ctx := rbac.ContextWithScope(context.Background(), f.customerAScope())

	_, err := f.admin.TransferDevice(ctx, f.tenant, f.devB, TransferInput{
		CustomerID: f.custA,
	}, "actor")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("transferring a foreign device want ErrNotFound, got %v", err)
	}
	if got := f.deviceOwner(t, f.devB); got != f.custB {
		t.Fatalf("foreign device ownership changed to %q despite denial", got)
	}
}

// TestWriteScopeCredentialAndRelease covers the GetDevice-gated credential
// actions and the quarantine release path.
func TestWriteScopeCredentialAndRelease(t *testing.T) {
	f := newWriteScopeFixture(t)
	ctx := rbac.ContextWithScope(context.Background(), f.customerAScope())

	// The credential id need not exist: the scope-aware GetDevice gate runs
	// first and must refuse before the credential lookup.
	if err := f.admin.DeleteCredential(ctx, f.tenant, f.devB,
		uuid.NewString(), "actor", "127.0.0.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteCredential foreign want ErrNotFound, got %v", err)
	}
	if err := f.admin.UpdateCredentialSecret(ctx, f.tenant, f.devB,
		uuid.NewString(), "new-secret", "actor", "127.0.0.1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("UpdateCredentialSecret foreign want ErrNotFound, got %v", err)
	}

	// Release on an ACTIVE foreign device returns not-found (scope), not the
	// "only QUARANTINED" conflict.
	if _, err := f.admin.ReleaseFromQuarantine(ctx, f.tenant, f.devB, "note", "actor"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReleaseFromQuarantine foreign want ErrNotFound, got %v", err)
	}
}

// TestWriteScopePositiveFlow proves an unrestricted caller can still
// quarantine and release a device, so the new gates do not lock down system
// operation.
func TestWriteScopePositiveFlow(t *testing.T) {
	f := newWriteScopeFixture(t)
	ctx := context.Background()

	quarantined, err := f.admin.QuarantineDevice(ctx, f.tenant, f.devA,
		ReasonOther, "incident", "actor")
	if err != nil {
		t.Fatalf("unrestricted quarantine: %v", err)
	}
	if quarantined.State != StateQuarantined {
		t.Fatalf("want QUARANTINED, got %q", quarantined.State)
	}
	released, err := f.admin.ReleaseFromQuarantine(ctx, f.tenant, f.devA, "resolved", "actor")
	if err != nil {
		t.Fatalf("unrestricted release: %v", err)
	}
	if released.State != StateActive {
		t.Fatalf("want ACTIVE after release, got %q", released.State)
	}
}
