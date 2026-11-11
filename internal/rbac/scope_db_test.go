// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// scope_db_test.go proves against a real PostgreSQL that an rbac.DataScope
// actually narrows rows, for both levels ADR-0022 declares supported.
//
// The unit tests in scope_test.go assert on the generated SQL; this file
// asserts on what PostgreSQL returns for that SQL. The gap matters: a clause
// can look correct, use the right operators and still evaluate to true for
// every row (NULL handling, an ANY() over an empty array, a uuid cast that
// silently becomes NULL). Only executing it proves the narrowing holds, and a
// leak here is invisible to every other kind of test.
//
// Fixtures are unique per run so the file is safe to run against a shared
// development database.

package rbac

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// scopeFixture is one tenant with two customers, a group tree and one device
// per anchor, used to prove what a narrowed caller can and cannot see.
type scopeFixture struct {
	pool     *pgxpool.Pool
	tenantID string

	// otherTenantID is a second tenant used to prove a scope cannot reach
	// across the tenant boundary.
	otherTenantID string

	// otherCustomerID is a customer in otherTenantID. A CUSTOMER scope must
	// refuse it as an anchor both on write and on read.
	otherCustomerID string

	customerA string
	customerB string

	groupRoot  string // root of the tree a GROUP scope is anchored at
	groupChild string // descendant of groupRoot, must be visible through it

	// Devices, one per ownership anchor.
	deviceA       string // customer A
	deviceB       string // customer B
	deviceRoot    string // groupRoot
	deviceChild   string // groupChild, inside groupRoot's subtree
	deviceOrphan  string // no customer, no group
	deviceForeign string // owned by another tenant's customer
}

// newScopeFixture builds the fixture inside a fresh tenant so a scoped caller
// can never be confused by rows from a previous run.
func newScopeFixture(t *testing.T) *scopeFixture {
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

	f := &scopeFixture{
		pool:         pool,
		tenantID:     uuid.NewString(),
		customerA:    uuid.NewString(),
		customerB:    uuid.NewString(),
		groupRoot:    uuid.NewString(),
		groupChild:   uuid.NewString(),
		deviceA:      uuid.NewString(),
		deviceB:      uuid.NewString(),
		deviceRoot:   uuid.NewString(),
		deviceChild:  uuid.NewString(),
		deviceOrphan: uuid.NewString(),
	}
	f.deviceForeign = uuid.NewString()
	f.otherTenantID = uuid.NewString()

	foreignCustomer := uuid.NewString()
	foreignGroup := uuid.NewString()
	f.otherCustomerID = foreignCustomer

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, query, args...); err != nil {
			t.Fatalf("fixture %q: %v", query, err)
		}
	}

	exec(`INSERT INTO tenant (id, name) VALUES ($1, $2)`,
		f.tenantID, "scope-test-"+f.tenantID[:8])
	exec(`INSERT INTO tenant (id, name) VALUES ($1, $2)`,
		f.otherTenantID, "scope-other-"+f.otherTenantID[:8])

	exec(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,'custA'),
	     ($3,$2,'custB')`,
		f.customerA, f.tenantID, f.customerB)
	// A customer and a group belonging to the OTHER tenant. A scope must never
	// be able to reach them, which is the cross-tenant leak this file exists
	// to rule out.
	exec(`INSERT INTO customer (id, tenant_id, name) VALUES ($1,$2,'foreign')`,
		foreignCustomer, f.otherTenantID)

	exec(`INSERT INTO device_group (id, tenant_id, name)
	      VALUES ($1,$2,'root'), ($3,$2,'child')`,
		f.groupRoot, f.tenantID, f.groupChild)
	exec(`UPDATE device_group SET parent_id = $1 WHERE id = $2`,
		f.groupRoot, f.groupChild)
	exec(`INSERT INTO device_group (id, tenant_id, name) VALUES ($1,$2,'foreign')`,
		foreignGroup, f.otherTenantID)

	exec(`INSERT INTO device (id, tenant_id, canonical_name, state,
	                          customer_id, group_id)
	      VALUES ($1,$2,'dev-a','ACTIVE',$3,NULL),
	         ($4,$2,'dev-b','ACTIVE',$5,NULL),
	         ($6,$2,'dev-root','ACTIVE',NULL,$7),
	         ($8,$2,'dev-child','ACTIVE',NULL,$9),
	         ($10,$2,'dev-orphan','ACTIVE',NULL,NULL)`,
		f.deviceA, f.tenantID, f.customerA,
		f.deviceB, f.customerB,
		f.deviceRoot, f.groupRoot,
		f.deviceChild, f.groupChild,
		f.deviceOrphan)
	// A device in our tenant whose ownership points at the other tenant's
	// rows. Denormalized ownership is not supposed to happen, and if it ever
	// does the scope must not become a cross-tenant read.
	exec(`INSERT INTO device (id, tenant_id, canonical_name, state,
	                          customer_id)
	      VALUES ($1,$2,'dev-foreign-owner','ACTIVE',$3)`,
		f.deviceForeign, f.tenantID, foreignCustomer)

	t.Cleanup(func() {
		// Delete children before parents to satisfy the foreign keys.
		for _, q := range []string{
			`DELETE FROM device WHERE tenant_id IN ($1, $2)`,
			`DELETE FROM device_group WHERE tenant_id IN ($1, $2)`,
			`DELETE FROM customer WHERE tenant_id IN ($1, $2)`,
			`DELETE FROM tenant WHERE id IN ($1, $2)`,
		} {
			if _, err := pool.Exec(context.Background(), q,
				f.tenantID, f.otherTenantID); err != nil {
				t.Logf("cleanup: %v", err)
			}
		}
	})
	return f
}

// visibleDevices runs the registry-shaped device list query under scope and
// returns the ids it produced.
//
// The shape mirrors internal/registry ListDevices: tenant filter, an
// unrelated optional filter, then the scope predicate appended last with its
// own trailing arguments. Reproducing the argument offsets here is deliberate
// — an offset bug would change the SQL, and this is the test that would catch
// it.
func visibleDevices(t *testing.T, f *scopeFixture, scope DataScope) []string {
	t.Helper()
	ctx := context.Background()
	clause, args := scope.DevicePredicate("d", 2)
	rows, err := f.pool.Query(ctx,
		`SELECT d.id::text
		 FROM device d
		 WHERE d.tenant_id = $1
		   AND ($2 = '' OR d.canonical_name ILIKE '%' || $2 || '%')`+clause+`
		 ORDER BY d.canonical_name`,
		append([]any{f.tenantID, ""}, args...)...)
	if err != nil {
		t.Fatalf("visible devices: %v", err)
	}
	defer rows.Close()

	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return ids
}

// countDevices runs the count twin of visibleDevices. A narrowed caller must
// get a narrowed count too, or the device list leaks the tenant-wide size even
// while hiding the rows.
func countDevices(t *testing.T, f *scopeFixture, scope DataScope) int {
	t.Helper()
	ctx := context.Background()
	clause, args := scope.DevicePredicate("", 2)
	var total int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM device
		 WHERE tenant_id = $1
		   AND ($2 = '' OR canonical_name ILIKE '%' || $2 || '%')`+clause,
		append([]any{f.tenantID, ""}, args...)...).Scan(&total); err != nil {
		t.Fatalf("count devices: %v", err)
	}
	return total
}

func contains(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// TestCustomerScopeNarrowsToCustomer verifies a CUSTOMER scope returns exactly
// the anchoring customer's devices — including nothing unowned, nothing in the
// sibling customer, and nothing reachable only through a group.
func TestCustomerScopeNarrowsToCustomer(t *testing.T) {
	f := newScopeFixture(t)
	scope := DataScope{
		TenantID: f.tenantID, Level: ScopeCustomer, CustomerID: f.customerA,
	}

	visible := visibleDevices(t, f, scope)
	if !contains(visible, f.deviceA) {
		t.Error("the anchoring customer's device must be visible")
	}
	for _, hidden := range []string{
		f.deviceB, f.deviceRoot, f.deviceChild,
		f.deviceOrphan, f.deviceForeign,
	} {
		if contains(visible, hidden) {
			t.Errorf("customer scope leaked device %s", hidden)
		}
	}
	if got := countDevices(t, f, scope); got != 1 {
		t.Errorf("customer scope count = %d, want 1 (count must be narrowed too)",
			got)
	}
}

// TestGroupScopeNarrowsToSubtree verifies a GROUP scope includes the anchor
// group AND its descendants, and nothing outside the tree.
func TestGroupScopeNarrowsToSubtree(t *testing.T) {
	f := newScopeFixture(t)
	scope := DataScope{
		TenantID: f.tenantID, Level: ScopeGroup, GroupID: f.groupRoot,
		GroupIDs: []string{f.groupRoot, f.groupChild},
	}

	visible := visibleDevices(t, f, scope)
	if !contains(visible, f.deviceRoot) {
		t.Error("the anchoring group's device must be visible")
	}
	if !contains(visible, f.deviceChild) {
		t.Error("a descendant group's device must be visible through the subtree")
	}
	for _, hidden := range []string{
		f.deviceA, f.deviceB, f.deviceOrphan, f.deviceForeign,
	} {
		if contains(visible, hidden) {
			t.Errorf("group scope leaked device %s", hidden)
		}
	}
	if got := countDevices(t, f, scope); got != 2 {
		t.Errorf("group scope count = %d, want 2", got)
	}
}

// TestTenantScopeSeesEverything guards the opposite direction: the narrowing
// must not narrow an unscoped tenant admin, or the feature would break the
// platform's own operators.
func TestTenantScopeSeesEverything(t *testing.T) {
	f := newScopeFixture(t)
	scope := UnrestrictedScope(f.tenantID)

	visible := visibleDevices(t, f, scope)
	for _, want := range []string{
		f.deviceA, f.deviceB, f.deviceRoot, f.deviceChild,
		f.deviceOrphan, f.deviceForeign,
	} {
		if !contains(visible, want) {
			t.Errorf("tenant scope must see %s", want)
		}
	}
	if got := countDevices(t, f, scope); got != 6 {
		t.Errorf("tenant scope count = %d, want 6", got)
	}
}

// TestScopeNeverCrossesTenant verifies the tenant predicate still holds when a
// scope is active. Our fixture deliberately contains a device in our tenant
// whose customer_id points at another tenant's customer; the scope must not
// become a way to read across the boundary.
func TestScopeNeverCrossesTenant(t *testing.T) {
	f := newScopeFixture(t)
	scope := DataScope{
		TenantID: f.tenantID, Level: ScopeCustomer, CustomerID: f.customerA,
	}
	if contains(visibleDevices(t, f, scope), f.deviceForeign) {
		t.Error("a device owned by a foreign customer must stay invisible")
	}
}

// TestDeviceSubqueryPredicateNarrowsNonOwnedTables verifies the
// device-membership variant used by alarm/telemetry/command/ota/presence
// tables, which carry a device_id but no ownership columns of their own.
func TestDeviceSubqueryPredicateNarrowsNonOwnedTables(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()

	// A stand-in for a device-referencing table. ts_presence already has this
	// shape and needs no extra fixture rows to be inserted by hand.
	scope := DataScope{
		TenantID: f.tenantID, Level: ScopeCustomer, CustomerID: f.customerA,
	}
	clause, args := scope.DeviceSubqueryPredicate("p.device_id", 1)

	rows, err := f.pool.Query(ctx,
		`SELECT p.device_id::text
		 FROM device d
		 CROSS JOIN LATERAL (SELECT d.id AS device_id) p
		 WHERE d.tenant_id = $1`+clause,
		append([]any{f.tenantID}, args...)...)
	if err != nil {
		t.Fatalf("subquery narrowing: %v", err)
	}
	defer rows.Close()
	seen := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		seen = append(seen, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	if !contains(seen, f.deviceA) {
		t.Error("the anchoring customer's device must survive the membership filter")
	}
	for _, hidden := range []string{f.deviceB, f.deviceRoot, f.deviceOrphan} {
		if contains(seen, hidden) {
			t.Errorf("membership filter leaked device %s", hidden)
		}
	}
}

// TestGroupSubtreeResolution walks a real group tree and verifies the
// resolver's output, which is what every GROUP-scoped request binds.
func TestGroupSubtreeResolution(t *testing.T) {
	f := newScopeFixture(t)
	resolver := &Resolver{pool: f.pool}
	ctx := context.Background()

	groups, err := resolver.groupSubtree(ctx, f.tenantID, f.groupRoot)
	if err != nil {
		t.Fatalf("group subtree: %v", err)
	}
	if !contains(groups, f.groupRoot) || !contains(groups, f.groupChild) {
		t.Errorf("subtree = %v, want both root and child", groups)
	}
	if len(groups) != 2 {
		t.Errorf("subtree = %v, want exactly 2 groups", groups)
	}

	// A group that exists but not in this tenant must be refused rather than
	// resolved, or a corrupt membership would become a cross-tenant read.
	foreignGroup := uuid.NewString()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO device_group (id, tenant_id, name) VALUES ($1,$2,'foreign')`,
		foreignGroup, f.otherTenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.groupSubtree(ctx, f.tenantID, foreignGroup); err == nil {
		t.Error("a group anchor from another tenant must be refused")
	}
}

// TestDataScopeForRejectsForeignAnchor verifies DataScopeFor refuses an anchor
// that does not belong to the tenant instead of falling back to tenant-wide.
//
// A fallback would be the worst possible outcome here: the membership is
// corrupt, and answering with an unrestricted scope would turn corrupt data
// into a full-tenant read.
func TestDataScopeForRejectsForeignAnchor(t *testing.T) {
	f := newScopeFixture(t)
	resolver := &Resolver{pool: f.pool}
	ctx := context.Background()

	// A real customer belonging to the OTHER tenant.
	var foreignCustomer string
	if err := f.pool.QueryRow(ctx,
		`SELECT id::text FROM customer WHERE tenant_id <> $1 LIMIT 1`,
		f.tenantID).Scan(&foreignCustomer); err != nil {
		t.Fatal(err)
	}

	// Asking for another tenant's customer as our scope anchor must fail.
	if _, err := resolver.DataScopeFor(ctx, f.tenantID, TenantGrant{
		Scope: ScopeCustomer, ScopeID: foreignCustomer,
	}); err == nil {
		t.Error("a customer anchor from another tenant must be refused")
	}

	// A syntactically valid but nonexistent anchor must fail too.
	if _, err := resolver.DataScopeFor(ctx, f.tenantID, TenantGrant{
		Scope: ScopeCustomer, ScopeID: uuid.NewString(),
	}); err == nil {
		t.Error("a nonexistent customer anchor must be refused")
	}

	// SELF is refused at the resolution boundary too, not only at assignment.
	if _, err := resolver.DataScopeFor(ctx, f.tenantID, TenantGrant{
		Scope: ScopeSelf,
	}); err == nil {
		t.Error("SELF scope must be refused")
	}

	// A valid anchor resolves, and TENANT stays unrestricted.
	narrowed, err := resolver.DataScopeFor(ctx, f.tenantID, TenantGrant{
		Scope: ScopeCustomer, ScopeID: f.customerA,
	})
	if err != nil {
		t.Fatalf("valid customer anchor: %v", err)
	}
	if !narrowed.Restricted() || narrowed.CustomerID != f.customerA {
		t.Errorf("resolved scope = %+v, want narrowed to %s",
			narrowed, f.customerA)
	}

	tenantWide, err := resolver.DataScopeFor(ctx, f.tenantID, TenantGrant{
		Scope: ScopeTenant,
	})
	if err != nil {
		t.Fatalf("tenant scope: %v", err)
	}
	if tenantWide.Restricted() {
		t.Error("TENANT scope must resolve to unrestricted")
	}

	// An empty tenant id is a programming error, not an unrestricted scope.
	if _, err := resolver.DataScopeFor(ctx, "", TenantGrant{
		Scope: ScopeTenant,
	}); err == nil {
		t.Error("an empty tenant id must be refused")
	}
}

// TestScopeWritePathExecutes proves the scope *write* path runs against a real
// tenant_membership, not just that the read predicates narrow.
//
// The read-side tests in this file all pass while SetDataScope was broken:
// that statement referenced tenant_membership.updated_at, a column migration
// 0014 never created, so every scope change failed at runtime with SQLSTATE
// 42703 and the REST endpoint answered 500. Unit tests with a mocked pool
// cannot catch this, because the mock accepts any column name. Only executing
// the statement proves the schema and the SQL agree.
func TestScopeWritePathExecutes(t *testing.T) {
	f := newScopeFixture(t)
	ctx := context.Background()
	userID := uuid.NewString()

	// A platform-level user: tenant_id NULL, as the real identity model does.
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO app_user (id, tenant_id, email, display_name,
		    password_hash, email_verified)
		 VALUES ($1, NULL, $2, 'Scope Writer', '', TRUE)`,
		userID, "scope-writer-"+userID+"@acme.example"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		// tenant_membership cascades from app_user.
		if _, err := f.pool.Exec(ctx,
			`DELETE FROM app_user WHERE id = $1`, userID); err != nil {
			t.Logf("cleanup user: %v", err)
		}
	})

	// EnsureMembershipScoped must insert a TENANT membership by default.
	if err := EnsureMembershipScoped(ctx, f.pool, userID, f.tenantID,
		RoleCustomerUser, ScopeTenant, ""); err != nil {
		t.Fatalf("EnsureMembershipScoped: %v", err)
	}

	read := func() (string, string) {
		t.Helper()
		var scope, scopeID string
		err := f.pool.QueryRow(ctx,
			`SELECT scope, COALESCE(scope_id::text,'') FROM tenant_membership
			 WHERE user_id = $1 AND tenant_id = $2`,
			userID, f.tenantID).Scan(&scope, &scopeID)
		if err != nil {
			t.Fatalf("read membership scope: %v", err)
		}
		return scope, scopeID
	}

	if scope, anchor := read(); scope != ScopeTenant || anchor != "" {
		t.Fatalf("default membership scope = %q/%q, want TENANT/empty",
			scope, anchor)
	}

	// Narrow to customer A. This is the statement that referenced a column
	// that does not exist.
	if err := SetDataScope(ctx, f.pool, userID, f.tenantID,
		ScopeCustomer, f.customerA); err != nil {
		t.Fatalf("SetDataScope to CUSTOMER: %v", err)
	}
	if scope, anchor := read(); scope != ScopeCustomer || anchor != f.customerA {
		t.Fatalf("scope after narrowing = %q/%q, want CUSTOMER/%s",
			scope, anchor, f.customerA)
	}

	// A GROUP anchor must round-trip the same way.
	if err := SetDataScope(ctx, f.pool, userID, f.tenantID,
		ScopeGroup, f.groupRoot); err != nil {
		t.Fatalf("SetDataScope to GROUP: %v", err)
	}
	if scope, anchor := read(); scope != ScopeGroup || anchor != f.groupRoot {
		t.Fatalf("scope after group narrowing = %q/%q, want GROUP/%s",
			scope, anchor, f.groupRoot)
	}

	// Widening back to TENANT must clear the anchor, or a stale customer id
	// would survive and later be honoured by a CUSTOMER check.
	if err := SetDataScope(ctx, f.pool, userID, f.tenantID,
		ScopeTenant, ""); err != nil {
		t.Fatalf("SetDataScope back to TENANT: %v", err)
	}
	if scope, anchor := read(); scope != ScopeTenant || anchor != "" {
		t.Fatalf("scope after widening = %q/%q, want TENANT/empty",
			scope, anchor)
	}

	// A foreign anchor must be refused before the write, not merely ignored.
	if err := SetDataScope(ctx, f.pool, userID, f.tenantID,
		ScopeCustomer, f.otherCustomerID); err == nil {
		t.Fatal("SetDataScope accepted another tenant's customer anchor")
	}
	if scope, _ := read(); scope != ScopeTenant {
		t.Fatalf("rejected write still changed the scope to %q", scope)
	}
}
