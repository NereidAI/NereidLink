// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// authorize_test.go covers the registry Authorize RPC (ADR-0009).
//
// The RPC previously returned Allowed: true unconditionally, which meant the
// gRPC contract an external ingress connector is meant to depend on promised
// access it never actually checked. These tests pin the three properties that
// matter: malformed requests are denied, a caller cannot grant itself the
// gateway capability, and a quarantined device cannot obtain topic access.

package registry

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	v1 "github.com/nereidai/nereidlink/api/gen/nereidlink/v1"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// authorizeTestPool connects when NL_TEST_DATABASE_URL is configured and
// skips otherwise, keeping the default hermetic run database-free.
func authorizeTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("NL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set NL_TEST_DATABASE_URL to run database tests")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestAuthorizeRejectsMalformedRequest verifies the guard clauses deny before
// any database work. A nil Pool is deliberate: reaching the query would panic
// and fail the test, which is exactly the signal wanted here.
func TestAuthorizeRejectsMalformedRequest(t *testing.T) {
	ctx := context.Background()
	srv := &Server{}

	cases := []struct {
		name string
		req  *v1.AuthorizeRequest
	}{
		{"nil device", &v1.AuthorizeRequest{
			Topic: mqttapi.TopicDeviceTelemetry, Action: mqttapi.ActionPublish}},
		{"empty topic", &v1.AuthorizeRequest{
			Device: &v1.DeviceIdentity{TenantId: "t", DeviceId: "d"}, Action: mqttapi.ActionPublish}},
		{"empty action", &v1.AuthorizeRequest{
			Device: &v1.DeviceIdentity{TenantId: "t", DeviceId: "d"},
			Topic:  mqttapi.TopicDeviceTelemetry}},
		{"empty topic and action", &v1.AuthorizeRequest{
			Device: &v1.DeviceIdentity{TenantId: "t", DeviceId: "d"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.Authorize(ctx, tc.req)
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			if resp.GetAllowed() {
				t.Error("malformed request was allowed")
			}
		})
	}
}

// TestAuthorizeRejectsUnknownDevice verifies a device that does not exist in
// the claimed tenant is denied rather than reaching the topic rules.
func TestAuthorizeRejectsUnknownDevice(t *testing.T) {
	pool := authorizeTestPool(t)
	ctx := context.Background()
	srv := NewServer(pool)

	resp, err := srv.Authorize(ctx, &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{
			TenantId: "00000000-0000-0000-0000-0000000000ff",
			DeviceId: "00000000-0000-0000-0000-0000000000fe",
		},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("unknown device was allowed")
	}
}

// TestAuthorizeRejectsMalformedIdentifiers verifies a non-UUID tenant or
// device identifier is denied at the lookup instead of panicking or being
// passed through to the topic rules.
func TestAuthorizeRejectsMalformedIdentifiers(t *testing.T) {
	pool := authorizeTestPool(t)
	ctx := context.Background()
	srv := NewServer(pool)

	resp, err := srv.Authorize(ctx, &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{TenantId: "not-a-uuid", DeviceId: "also-not"},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("malformed identifiers were allowed")
	}
}

// seedAuthorizeDevice inserts a disposable tenant and device and returns their
// identifiers. The rows are removed when the test ends so repeated runs against
// a long-lived development database stay clean.
func seedAuthorizeDevice(t *testing.T, pool *pgxpool.Pool, state string) (string, string) {
	t.Helper()
	ctx := context.Background()

	tenantID := "00000000-0000-0000-0000-00000000ac01"
	deviceID := "00000000-0000-0000-0000-00000000ad01"
	suffix := state

	if _, err := pool.Exec(ctx,
		`INSERT INTO tenant (id, name) VALUES ($1, $2)
		 ON CONFLICT (id) DO NOTHING`,
		tenantID, "authorize-test-"+suffix); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device (id, tenant_id, canonical_name, state)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state`,
		deviceID, tenantID, "authorize-test-device-"+suffix, state); err != nil {
		t.Fatalf("insert device: %v", err)
	}
	t.Cleanup(func() {
		// Delete the child first: device references tenant.
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM device WHERE id = $1`, deviceID); err != nil {
			t.Logf("cleanup device: %v", err)
		}
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id = $1`, tenantID); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})
	return tenantID, deviceID
}

// TestAuthorizeGrantsDeviceTopicForLiveDevice verifies the RPC is not simply
// denying everything: a real, non-quarantined device keeps the device topic
// set it is entitled to.
func TestAuthorizeGrantsDeviceTopicForLiveDevice(t *testing.T) {
	pool := authorizeTestPool(t)
	tenantID, deviceID := seedAuthorizeDevice(t, pool, "ACTIVE")
	srv := NewServer(pool)

	resp, err := srv.Authorize(context.Background(), &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{TenantId: tenantID, DeviceId: deviceID},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if !resp.GetAllowed() {
		t.Error("device uplink topic denied for an active device")
	}
}

// TestAuthorizeIgnoresCallerClaimedCapabilities verifies the central security
// property: capabilities in the request are not trusted. A connector that
// asserts the gateway capability must still be refused the gateway topics,
// because no capability model is persisted and the server grants only what it
// can prove. Trusting this field would make the RPC a no-op dressed as a
// check.
func TestAuthorizeIgnoresCallerClaimedCapabilities(t *testing.T) {
	pool := authorizeTestPool(t)
	tenantID, deviceID := seedAuthorizeDevice(t, pool, "ACTIVE")
	srv := NewServer(pool)

	resp, err := srv.Authorize(context.Background(), &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{
			TenantId:     tenantID,
			DeviceId:     deviceID,
			Capabilities: []string{mqttapi.CapabilityGateway},
		},
		Topic:  mqttapi.TopicGatewayTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("caller-claimed gateway capability unlocked the gateway topic set")
	}
}

// TestAuthorizeRejectsQuarantinedDevice verifies quarantine outranks topic
// authorization: a device that passed (or bypassed) authentication must not
// gain access to the platform while quarantined.
func TestAuthorizeRejectsQuarantinedDevice(t *testing.T) {
	pool := authorizeTestPool(t)
	tenantID, deviceID := seedAuthorizeDevice(t, pool, "QUARANTINED")
	srv := NewServer(pool)

	resp, err := srv.Authorize(context.Background(), &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{TenantId: tenantID, DeviceId: deviceID},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("quarantined device was granted a topic")
	}
}

// TestAuthorizeRejectsUnknownActionOnLiveDevice verifies the action allowlist
// is enforced for a device that otherwise passes every lookup, so a
// misspelled action cannot fall through to a permissive default.
func TestAuthorizeRejectsUnknownActionOnLiveDevice(t *testing.T) {
	pool := authorizeTestPool(t)
	tenantID, deviceID := seedAuthorizeDevice(t, pool, "ACTIVE")
	srv := NewServer(pool)

	resp, err := srv.Authorize(context.Background(), &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{TenantId: tenantID, DeviceId: deviceID},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: "PUBLISH",
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("unknown action was allowed on a valid device")
	}
}

// TestAuthorizeRejectsCrossTenantDevice verifies a device cannot be authorized
// by presenting another tenant's identifier pair, which is the tenant
// isolation boundary the lookup enforces.
func TestAuthorizeRejectsCrossTenantDevice(t *testing.T) {
	pool := authorizeTestPool(t)
	deviceTenant, deviceID := seedAuthorizeDevice(t, pool, "ACTIVE")

	otherTenant := "00000000-0000-0000-0000-00000000ac99"
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO tenant (id, name) VALUES ($1, 'authorize-test-other')
		 ON CONFLICT (id) DO NOTHING`, otherTenant); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(),
			`DELETE FROM tenant WHERE id = $1`, otherTenant); err != nil {
			t.Logf("cleanup tenant: %v", err)
		}
	})

	srv := NewServer(pool)
	resp, err := srv.Authorize(context.Background(), &v1.AuthorizeRequest{
		Device: &v1.DeviceIdentity{TenantId: otherTenant, DeviceId: deviceID},
		Topic:  mqttapi.TopicDeviceTelemetry,
		Action: mqttapi.ActionPublish,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if resp.GetAllowed() {
		t.Error("device was authorized under a foreign tenant")
	}
	if deviceTenant == otherTenant {
		t.Fatal("test setup reused the tenant identifier")
	}
}
