// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package companion

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// ErrInvalidArgument marks a malformed endpoint registration request. The
// gateway maps it to 400 instead of a 500.
var ErrInvalidArgument = errors.New("companion endpoint invalid")

// endpointIDPattern bounds the logical companion and gateway identifiers used
// as the endpoint natural key. Device names and companion ids are lowercase
// slug-like tokens in every NereidAI profile.
var endpointIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

// EndpointInput is the validated registration/update request. AuthToken is a
// pointer so update can distinguish "keep the existing token" (nil) from
// "clear the token" (explicit empty string).
type EndpointInput struct {
	CompanionID        string
	GatewayID          string
	BaseURL            string
	ContractVersion    string
	AuthToken          *string
	InsecureSkipVerify bool
	Enabled            *bool
}

// Admin is the endpoint-management service sitting between the REST handlers
// and the Store. All field validation lives here so the rules are testable
// without HTTP and identical for every transport.
type Admin struct {
	store Store
}

// NewAdmin constructs the endpoint admin service.
func NewAdmin(store Store) *Admin {
	return &Admin{store: store}
}

// ListEndpoints returns every endpoint of one tenant.
func (a *Admin) ListEndpoints(ctx context.Context, tenantID string) ([]Endpoint, error) {
	return a.store.ListEndpoints(ctx, tenantID)
}

// GetEndpoint loads one endpoint by surrogate id.
func (a *Admin) GetEndpoint(ctx context.Context, tenantID, endpointID string) (Endpoint, error) {
	ep, err := a.store.GetEndpointByID(ctx, tenantID, endpointID)
	if errors.Is(err, ErrStoreNotFound) {
		return Endpoint{}, ErrEndpointNotFound
	}
	return ep, err
}

// Register validates and inserts (or natural-key upserts) one endpoint.
func (a *Admin) Register(ctx context.Context, tenantID string, in EndpointInput) (Endpoint, error) {
	endpoint, err := a.validate(tenantID, in, true)
	if err != nil {
		return Endpoint{}, err
	}
	return a.store.UpsertEndpoint(ctx, endpoint)
}

// Update replaces one endpoint identified by surrogate id. The natural key
// (companion id, gateway) is immutable: changing it means registering a new
// endpoint. A nil auth token preserves the stored secret.
func (a *Admin) Update(ctx context.Context, tenantID, endpointID string, in EndpointInput) (Endpoint, error) {
	existing, err := a.store.GetEndpointByID(ctx, tenantID, endpointID)
	if errors.Is(err, ErrStoreNotFound) {
		return Endpoint{}, ErrEndpointNotFound
	}
	if err != nil {
		return Endpoint{}, err
	}
	if strings.TrimSpace(in.CompanionID) != existing.CompanionID ||
		strings.TrimSpace(in.GatewayID) != existing.GatewayID {
		return Endpoint{}, fmt.Errorf("%w: companion_id and gateway_id are immutable", ErrInvalidArgument)
	}
	endpoint, err := a.validate(tenantID, in, false)
	if err != nil {
		return Endpoint{}, err
	}
	endpoint.ID = existing.ID
	if in.AuthToken == nil {
		endpoint.AuthToken = existing.AuthToken
	}
	return a.store.UpsertEndpoint(ctx, endpoint)
}

// Delete removes one endpoint; its desired rows cascade.
func (a *Admin) Delete(ctx context.Context, tenantID, endpointID string) error {
	err := a.store.DeleteEndpoint(ctx, tenantID, endpointID)
	if errors.Is(err, ErrStoreNotFound) {
		return ErrEndpointNotFound
	}
	return err
}

// ListDesired exposes the desired rows and their last read-back status.
func (a *Admin) ListDesired(ctx context.Context, tenantID, endpointID string) ([]DesiredRow, error) {
	if _, err := a.GetEndpoint(ctx, tenantID, endpointID); err != nil {
		return nil, err
	}
	return a.store.ListDesired(ctx, tenantID, endpointID)
}

func (a *Admin) validate(tenantID string, in EndpointInput, tokenRequired bool) (Endpoint, error) {
	companionID := strings.TrimSpace(in.CompanionID)
	gatewayID := strings.TrimSpace(in.GatewayID)
	if !endpointIDPattern.MatchString(companionID) {
		return Endpoint{}, fmt.Errorf("%w: companion_id must match %s", ErrInvalidArgument, endpointIDPattern.String())
	}
	if !endpointIDPattern.MatchString(gatewayID) {
		return Endpoint{}, fmt.Errorf("%w: gateway_id must match %s", ErrInvalidArgument, endpointIDPattern.String())
	}
	baseURL := strings.TrimSpace(in.BaseURL)
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return Endpoint{}, fmt.Errorf("%w: base_url must be an absolute http(s) URL", ErrInvalidArgument)
	}
	if in.InsecureSkipVerify && parsed.Scheme != "https" {
		return Endpoint{}, fmt.Errorf("%w: insecure_skip_verify is only meaningful for https endpoints", ErrInvalidArgument)
	}
	contract := strings.TrimSpace(in.ContractVersion)
	if contract == "" {
		contract = ContractV1
	}
	if contract != ContractV1 {
		return Endpoint{}, fmt.Errorf("%w: unsupported contract_version %q", ErrInvalidArgument, contract)
	}
	token := ""
	if in.AuthToken != nil {
		token = strings.TrimSpace(*in.AuthToken)
	}
	if tokenRequired && in.AuthToken == nil {
		return Endpoint{}, fmt.Errorf("%w: auth_token is required at registration", ErrInvalidArgument)
	}
	if tokenRequired && token == "" {
		return Endpoint{}, fmt.Errorf("%w: auth_token must not be empty", ErrInvalidArgument)
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	return Endpoint{
		TenantID:           tenantID,
		CompanionID:        companionID,
		GatewayID:          gatewayID,
		BaseURL:            strings.TrimRight(baseURL, "/"),
		ContractVersion:    contract,
		AuthToken:          token,
		InsecureSkipVerify: in.InsecureSkipVerify,
		Enabled:            enabled,
	}, nil
}
