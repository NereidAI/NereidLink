// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// deployment_test.go verifies that the administrator deployment-license
// entry point refuses anything other than a deployment-scope envelope
// before touching the database.

package license

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstallDeploymentRejectsTenantScope(t *testing.T) {
	public, private := mustTestKey(t)
	// A nil pool is safe because scope validation runs before any database
	// access: a correctly signed tenant envelope must be refused by the
	// deployment-only gate rather than reaching Service.Install.
	svc := &Service{key: public}

	tenant := Payload{
		Scope:        "00000000-0000-0000-0000-000000000001",
		TenantID:     "00000000-0000-0000-0000-000000000001",
		CustomerName: "Tenant Only",
		IssuedAt:     time.Now().Add(-time.Hour),
		ExpiresAt:    time.Now().Add(time.Hour),
		Features:     []string{},
	}
	if _, err := svc.InstallDeployment(context.Background(),
		mustSign(t, tenant, private), "tester"); !errors.Is(err, ErrInvalidLicense) {
		t.Fatalf("error = %v, want %v", err, ErrInvalidLicense)
	}
}
