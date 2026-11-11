// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package profile

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nereidai/nereidlink/sdk/scriptruntime"
)

// Resolver resolves the effective profile and descriptors for a device.
type Resolver struct {
	store *Store
	ttl   time.Duration

	bindings    *ttlLRU
	profiles    *ttlLRU
	overrides   *ttlLRU
	expressions *scriptruntime.Runtime
}

// profileBundle is a cached profile with its descriptor set.
type profileBundle struct {
	profile     Profile
	descriptors map[string]Descriptor
}

// NewResolver constructs a resolver with bounded caches.
func NewResolver(pool *pgxpool.Pool, ttl time.Duration, cacheSize int, expressions *scriptruntime.Runtime) *Resolver {
	if cacheSize < 1 {
		cacheSize = 1
	}
	return &Resolver{
		store:       NewStore(pool),
		ttl:         ttl,
		bindings:    newTTLLRU(ttl, cacheSize),
		profiles:    newTTLLRU(ttl, cacheSize),
		overrides:   newTTLLRU(ttl, cacheSize),
		expressions: expressions,
	}
}

// Resolve returns the effective descriptors for a device. A nil result means
// the device has no profile and ingress should pass values through.
func (r *Resolver) Resolve(ctx context.Context, tenantID, deviceID string) (Profile, map[string]Descriptor, error) {
	bindingKey := tenantID + "|" + deviceID
	profileID, ok := r.bindings.get(bindingKey)
	if !ok {
		id, bound, err := r.store.Binding(ctx, tenantID, deviceID)
		if err != nil {
			return Profile{}, nil, err
		}
		if bound {
			profileID = id
		} else {
			profileID = ""
		}
		r.bindings.set(bindingKey, profileID)
	}

	id, _ := profileID.(string)
	if id == "" {
		return Profile{}, nil, nil
	}

	bundle, ok := r.profiles.get(id)
	if !ok {
		profile, descriptors, err := r.store.ProfileAndDescriptors(ctx, id)
		if err != nil {
			return Profile{}, nil, err
		}
		bundle = profileBundle{profile: profile, descriptors: descriptors}
		r.profiles.set(id, bundle)
	}
	resolved := bundle.(profileBundle)

	cachedOverrides, ok := r.overrides.get(deviceID)
	if !ok {
		loaded, err := r.store.Overrides(ctx, deviceID)
		if err != nil {
			return Profile{}, nil, err
		}
		cachedOverrides = loaded
		r.overrides.set(deviceID, loaded)
	}
	overrides, _ := cachedOverrides.(map[string]Override)

	effective := make(map[string]Descriptor, len(resolved.descriptors))
	for key, descriptor := range resolved.descriptors {
		if override, ok := overrides[key]; ok {
			descriptor = applyOverride(descriptor, override)
		}
		effective[key] = descriptor
	}
	return resolved.profile, effective, nil
}

// ExpressionRuntime returns the runtime used for expression descriptors.
func (r *Resolver) ExpressionRuntime() *scriptruntime.Runtime {
	return r.expressions
}

// applyOverride returns a descriptor copy adjusted for one device.
func applyOverride(descriptor Descriptor, override Override) Descriptor {
	if descriptor.TransformType == TransformIdentity || descriptor.TransformType == TransformLinear {
		descriptor.Scale = override.Scale
		descriptor.Offset = override.Offset
	}
	if override.ValidMin != nil {
		descriptor.ValidMin = override.ValidMin
	}
	if override.ValidMax != nil {
		descriptor.ValidMax = override.ValidMax
	}
	if override.Precision >= 0 {
		descriptor.Precision = override.Precision
	}
	return descriptor
}
