// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// policy_admin.go exposes effective-policy views and override management for
// the REST control plane (migration 0045). The discovery frame path only
// needs PolicyReader; these methods additionally require a PolicyAdmin.

package discovery

import (
	"context"
	"errors"
)

// SetPolicyAdmin wires override management (the reader is wired separately
// for the frame path; the store satisfies both).
func (s *Service) SetPolicyAdmin(admin PolicyAdmin) {
	s.admin = admin
	s.reader = admin
}

// errNoAdmin is returned when the REST surface is used without a store.
var errNoAdmin = errors.New("discovery policy store not configured")

// EffectiveView returns the resolved policy for the tenant default
// (gatewayID empty) or one gateway, with the scope's override flag.
func (s *Service) EffectiveView(ctx context.Context, tenantID, gatewayID string) (View, error) {
	policy, err := EffectivePolicy(ctx, s.reader, s.fallback, tenantID, gatewayID)
	if err != nil {
		return View{}, err
	}
	overridden := false
	if s.reader != nil {
		tenantRow, gatewayRow, rerr := s.reader.RowsFor(ctx, tenantID, gatewayID)
		if rerr != nil {
			return View{}, rerr
		}
		if gatewayID != "" {
			overridden = gatewayRow != nil
		} else {
			overridden = tenantRow != nil
		}
	}
	return toView(tenantID, gatewayID, policy, overridden), nil
}

// SetTenantPolicy upserts the tenant-default override.
func (s *Service) SetTenantPolicy(ctx context.Context, tenantID string, in Override) (View, error) {
	if s.admin == nil {
		return View{}, errNoAdmin
	}
	if _, err := s.admin.SetTenant(ctx, tenantID, in); err != nil {
		return View{}, err
	}
	return s.EffectiveView(ctx, tenantID, "")
}

// SetGatewayPolicy upserts one gateway override.
func (s *Service) SetGatewayPolicy(ctx context.Context, tenantID, gatewayID string, in Override) (View, error) {
	if s.admin == nil {
		return View{}, errNoAdmin
	}
	if _, err := s.admin.SetGateway(ctx, tenantID, gatewayID, in); err != nil {
		return View{}, err
	}
	return s.EffectiveView(ctx, tenantID, gatewayID)
}

// DeleteTenantPolicy removes the tenant-default override (inherit deployment).
func (s *Service) DeleteTenantPolicy(ctx context.Context, tenantID string) error {
	if s.admin == nil {
		return errNoAdmin
	}
	return s.admin.DeleteTenant(ctx, tenantID)
}

// DeleteGatewayPolicy removes one gateway override.
func (s *Service) DeleteGatewayPolicy(ctx context.Context, tenantID, gatewayID string) error {
	if s.admin == nil {
		return errNoAdmin
	}
	return s.admin.DeleteGateway(ctx, tenantID, gatewayID)
}
