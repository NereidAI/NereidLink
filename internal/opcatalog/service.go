// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// service.go is the operation catalog facade: hello registration, private
// descriptor CRUD, clone-to-private, disable/enable and the effective
// per-tenant catalog merge.

package opcatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// catalogStore is the persistence surface Service needs.
type catalogStore interface {
	ReplaceGatewayAdvertisements(ctx context.Context, gatewayID string, accepted []Descriptor) error
	ListGatewayAdvertisements(ctx context.Context, tenantID, gatewayID string) ([]Descriptor, error)
	SelectGatewayAdvertisement(ctx context.Context, tenantID, gatewayID, operation string) (Descriptor, bool, error)
	ListTenantAdvertised(ctx context.Context, tenantID string) ([]Advertised, error)

	CreatePrivate(ctx context.Context, tenantID string, d Descriptor) error
	UpdatePrivate(ctx context.Context, tenantID, operation, version string, d Descriptor) error
	DeletePrivate(ctx context.Context, tenantID, operation, version string) error
	ListPrivate(ctx context.Context, tenantID string) ([]Descriptor, error)

	SetDisabled(ctx context.Context, tenantID, operation, version, userID string) error
	ClearDisabled(ctx context.Context, tenantID, operation, version string) error
	DisabledKeys(ctx context.Context, tenantID string) (map[string]bool, error)
}

// Source labels for View.
const (
	SourceEdge   = "edge"
	SourceManual = "manual"
)

// Service applies and merges the operation catalog.
type Service struct {
	store catalogStore
	cache *schemaCache
}

// New constructs a Service over a Store.
func New(store catalogStore) *Service {
	return &Service{store: store, cache: newSchemaCache()}
}

// ValidateParams checks one invocation params document against a
// descriptor's compiled param schema. An empty/{} schema imposes no rule.
// It is the invocation-time counterpart of the registration-time compile,
// so a gateway-advertised schema is enforced by the platform, not trusted
// to the console.
func (s *Service) ValidateParams(rawSchema json.RawMessage, params map[string]any) error {
	trimmed := bytes.TrimSpace(rawSchema)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) {
		return nil
	}
	compiled, err := s.cache.compiled(trimmed)
	if err != nil {
		return err
	}
	if err := compiled.Validate(params); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidParams, err)
	}
	return nil
}

// RegisterFromHello validates every advertised descriptor and reconciles the
// gateway's full advertised set (full-set replacement: keys absent from this
// hello are removed). Partial success: a bad entry is reported rejected and
// excluded from the accepted set, but it never fails the session. gatewayID
// comes from the authenticated connection.
func (s *Service) RegisterFromHello(ctx context.Context, gatewayID string, regs []Descriptor) []Result {
	results := make([]Result, 0, len(regs))
	accepted := make([]Descriptor, 0, len(regs))
	for _, reg := range regs {
		result := Result{Operation: reg.Operation, TypeVersion: reg.TypeVersion}
		canonical, err := validate(reg, s.cache)
		if err != nil {
			result.Reason = err.Error()
			results = append(results, result)
			continue
		}
		accepted = append(accepted, canonical)
		result.Accepted = true
		results = append(results, result)
	}
	// The accepted set is authoritative for this gateway; persist it as a
	// whole so removed operations disappear even if one entry was rejected.
	if err := s.store.ReplaceGatewayAdvertisements(ctx, gatewayID, accepted); err != nil {
		// Persistence failure invalidates every accept result; surface it on
		// each entry rather than silently advertising an unstored descriptor.
		for i := range results {
			if results[i].Accepted {
				results[i].Accepted = false
				results[i].Reason = err.Error()
			}
		}
	}
	return results
}

// CreateManual stores one tenant-defined descriptor.
func (s *Service) CreateManual(ctx context.Context, tenantID string, d Descriptor) error {
	canonical, err := validate(d, s.cache)
	if err != nil {
		return err
	}
	return s.store.CreatePrivate(ctx, tenantID, canonical)
}

// UpdateManual replaces one private descriptor.
func (s *Service) UpdateManual(ctx context.Context, tenantID, operation, version string, d Descriptor) error {
	canonical, err := validate(d, s.cache)
	if err != nil {
		return err
	}
	return s.store.UpdatePrivate(ctx, tenantID, operation, version, canonical)
}

// DeleteManual removes one private descriptor.
func (s *Service) DeleteManual(ctx context.Context, tenantID, operation, version string) error {
	return s.store.DeletePrivate(ctx, tenantID, operation, version)
}

// CloneToPrivate copies the effective descriptor of key into a new private
// descriptor. A private descriptor, when present, is the clone origin;
// otherwise the aggregate edge descriptor is used.
func (s *Service) CloneToPrivate(ctx context.Context, tenantID, operation, version, newKey string) error {
	origin, ok, err := s.findEffective(ctx, tenantID, operation, version)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	origin.Operation = newKey
	if newKey == "" {
		origin.Operation = operation + "-copy"
	}
	return s.CreateManual(ctx, tenantID, origin)
}

// SetDisabled disables an operation key for a tenant.
func (s *Service) SetDisabled(ctx context.Context, tenantID, operation, version, userID string) error {
	return s.store.SetDisabled(ctx, tenantID, operation, version, userID)
}

// ClearDisabled re-enables an operation key.
func (s *Service) ClearDisabled(ctx context.Context, tenantID, operation, version string) error {
	return s.store.ClearDisabled(ctx, tenantID, operation, version)
}

// edgeGroup collects the advertisers of one (operation, version) key.
type edgeGroup struct {
	rep         Descriptor
	advertisers []Advertiser
	conflict    bool
}

// contractFingerprint captures the enforceable half of a descriptor so that
// two advertisers of the same key can be compared for divergence. Localized
// labels are cosmetic and excluded; risk/timeout/schemas/interaction are not.
func contractFingerprint(d Descriptor) string {
	return string(bytes.TrimSpace(d.ParamSchema)) + "\x00" +
		string(bytes.TrimSpace(d.ResultSchema)) + "\x00" +
		d.Interaction + "\x00" + d.Risk + "\x00" +
		fmt.Sprintf("%d", d.TimeoutMs)
}

// aggregateEdge groups this tenant's per-gateway advertisements by
// (operation, version), attaching the advertiser list and flagging groups
// whose contracts disagree. The representative descriptor is the first row
// of a group (the store orders newest updated_at first within a key).
func (s *Service) aggregateEdge(ctx context.Context, tenantID string) (map[string]*edgeGroup, []string, error) {
	rows, err := s.store.ListTenantAdvertised(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}
	groups := map[string]*edgeGroup{}
	order := []string{}
	seenGateway := map[string]map[string]bool{}
	for _, row := range rows {
		key := row.Operation + "|" + row.TypeVersion
		g, exists := groups[key]
		if !exists {
			g = &edgeGroup{rep: row.Descriptor}
			groups[key] = g
			order = append(order, key)
			seenGateway[key] = map[string]bool{}
		} else if !g.conflict &&
			contractFingerprint(row.Descriptor) != contractFingerprint(g.rep) {
			g.conflict = true
		}
		if !seenGateway[key][row.GatewayID] {
			seenGateway[key][row.GatewayID] = true
			g.advertisers = append(g.advertisers, Advertiser{
				ID:   row.GatewayID,
				Name: row.GatewayName,
			})
		}
	}
	return groups, order, nil
}

// Effective returns the merged catalog for a tenant: edge descriptors
// aggregated across the tenant's gateways, with private descriptors
// overriding same keys. Disabled keys keep the disabled flag so the console
// can show and re-enable them. Edge rows carry the advertiser list and a
// conflict flag for divergent same-key contracts.
func (s *Service) Effective(ctx context.Context, tenantID string) ([]View, error) {
	disabled, err := s.store.DisabledKeys(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	private, err := s.store.ListPrivate(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	privateByKey := map[string]Descriptor{}
	for _, d := range private {
		privateByKey[d.Operation+"|"+d.TypeVersion] = d
	}

	groups, edgeOrder, err := s.aggregateEdge(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	out := make([]View, 0, len(edgeOrder)+len(privateByKey))
	for _, key := range edgeOrder {
		g := groups[key]
		if d, ok := privateByKey[key]; ok {
			out = append(out, View{Descriptor: d, Source: SourceManual, Disabled: disabled[key]})
			delete(privateByKey, key)
			continue
		}
		out = append(out, View{
			Descriptor:   g.rep,
			Source:       SourceEdge,
			Disabled:     disabled[key],
			AdvertisedBy: g.advertisers,
			GatewayCount: len(g.advertisers),
			Conflict:     g.conflict,
		})
	}
	// Append tenant-only private keys in a stable order.
	privateOnly := make([]Descriptor, 0, len(privateByKey))
	for _, d := range privateByKey {
		privateOnly = append(privateOnly, d)
	}
	sortDescriptors(privateOnly)
	for _, d := range privateOnly {
		key := d.Operation + "|" + d.TypeVersion
		out = append(out, View{Descriptor: d, Source: SourceManual, Disabled: disabled[key]})
	}
	return out, nil
}

// EffectiveForGateway resolves the descriptor used to invoke one operation
// ON a specific gateway: that gateway's own newest advertisement, then a
// tenant-private descriptor as fallback (the tenant-defined menu for
// firmware that does not advertise it). The resolved key's disabled state is
// annotated. The gateway-specific lookup is what keeps same-key contracts
// from bleeding across gateways.
func (s *Service) EffectiveForGateway(
	ctx context.Context, tenantID, gatewayID, operation string,
) (View, bool, error) {
	disabled, err := s.store.DisabledKeys(ctx, tenantID)
	if err != nil {
		return View{}, false, err
	}
	d, ok, err := s.store.SelectGatewayAdvertisement(ctx, tenantID, gatewayID, operation)
	if err != nil {
		return View{}, false, err
	}
	if ok {
		return View{
			Descriptor: d,
			Source:     SourceEdge,
			Disabled:   disabled[d.Operation+"|"+d.TypeVersion],
		}, true, nil
	}

	private, err := s.store.ListPrivate(ctx, tenantID)
	if err != nil {
		return View{}, false, err
	}
	for _, d := range private {
		if d.Operation == operation {
			return View{
				Descriptor: d,
				Source:     SourceManual,
				Disabled:   disabled[d.Operation+"|"+d.TypeVersion],
			}, true, nil
		}
	}
	return View{}, false, nil
}

// ListForGateway returns the catalog offered on one gateway's Operations tab:
// everything that gateway advertises plus tenant-private descriptors it does
// not itself advertise (the private fallback menu).
func (s *Service) ListForGateway(ctx context.Context, tenantID, gatewayID string) ([]View, error) {
	disabled, err := s.store.DisabledKeys(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	ads, err := s.store.ListGatewayAdvertisements(ctx, tenantID, gatewayID)
	if err != nil {
		return nil, err
	}
	private, err := s.store.ListPrivate(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	byKey := map[string]View{}
	order := []string{}
	for _, d := range ads {
		key := d.Operation + "|" + d.TypeVersion
		byKey[key] = View{Descriptor: d, Source: SourceEdge, Disabled: disabled[key]}
		order = append(order, key)
	}
	for _, d := range private {
		key := d.Operation + "|" + d.TypeVersion
		if _, exists := byKey[key]; exists {
			continue
		}
		byKey[key] = View{Descriptor: d, Source: SourceManual, Disabled: disabled[key]}
		order = append(order, key)
	}

	out := make([]View, 0, len(order))
	for _, key := range order {
		out = append(out, byKey[key])
	}
	return out, nil
}

// findEffective locates one effective descriptor for clone origin, preferring
// a private row and otherwise using the aggregate edge representative.
func (s *Service) findEffective(ctx context.Context, tenantID, operation, version string) (Descriptor, bool, error) {
	private, err := s.store.ListPrivate(ctx, tenantID)
	if err != nil {
		return Descriptor{}, false, err
	}
	for _, d := range private {
		if d.Operation == operation && d.TypeVersion == version {
			return d, true, nil
		}
	}
	groups, _, err := s.aggregateEdge(ctx, tenantID)
	if err != nil {
		return Descriptor{}, false, err
	}
	if g, ok := groups[operation+"|"+version]; ok {
		return g.rep, true, nil
	}
	return Descriptor{}, false, nil
}

// sortDescriptors orders descriptors by key deterministically.
func sortDescriptors(ds []Descriptor) {
	sort.Slice(ds, func(i, j int) bool {
		if ds[i].Operation != ds[j].Operation {
			return ds[i].Operation < ds[j].Operation
		}
		return ds[i].TypeVersion < ds[j].TypeVersion
	})
}
