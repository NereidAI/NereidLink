// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// authenticator.go implements the caching authenticator: check L1/L2,
// prove possession locally through credverify, and on a miss resolve once
// through the fallback and populate every tier.

package credcache

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"

	sdkregistry "github.com/nereidai/nereidlink/sdk/registry"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"

	"github.com/nereidai/nereidlink/internal/credverify"
)

// DetailedFallback resolves a credential with its verification record.
// *registry.Client satisfies it.
type DetailedFallback interface {
	AuthenticateDetailed(ctx context.Context, username, password string) (sdkregistry.DetailedResult, error)
	AuthenticateCertificateDetailed(ctx context.Context, credentialsID, fingerprint string) (sdkregistry.DetailedResult, error)
}

// AuthenticatorConfig parameterizes the caching authenticator.
type AuthenticatorConfig struct {
	// PositiveTTL bounds reuse of a resolved credential; <=0 uses default.
	PositiveTTL time.Duration
	// NegativeTTL bounds caching of failed credentials; <=0 uses default.
	NegativeTTL time.Duration
	// Resolver supplies remote JWK Sets for JWT verification; may be nil.
	Resolver credverify.KeyResolver
}

// DefaultPositiveTTL is used when configuration names no positive TTL.
const DefaultPositiveTTL = 45 * time.Second

// DefaultNegativeTTL is used when configuration names no negative TTL.
const DefaultNegativeTTL = 3 * time.Second

// CachedAuthenticator authenticates devices with a credential cache.
type CachedAuthenticator struct {
	fallback DetailedFallback
	l1       *l1Cache
	l2       *l2Cache

	positiveTTL time.Duration
	negativeTTL time.Duration
	resolver    credverify.KeyResolver

	flights singleflight.Group
	metrics *Metrics
}

// NewCachedAuthenticator builds the caching authenticator. l2 may be nil
// for an L1-only node.
func NewCachedAuthenticator(fallback DetailedFallback, l1 *l1Cache, l2 *l2Cache,
	cfg AuthenticatorConfig, metrics *Metrics) *CachedAuthenticator {
	positiveTTL := cfg.PositiveTTL
	if positiveTTL <= 0 {
		positiveTTL = DefaultPositiveTTL
	}
	negativeTTL := cfg.NegativeTTL
	if negativeTTL <= 0 {
		negativeTTL = DefaultNegativeTTL
	}
	if metrics == nil {
		metrics = NewMetrics(nil)
	}
	return &CachedAuthenticator{
		fallback:    fallback,
		l1:          l1,
		l2:          l2,
		positiveTTL: positiveTTL,
		negativeTTL: negativeTTL,
		resolver:    cfg.Resolver,
		metrics:     metrics,
	}
}

// Authenticate validates a username/password credential.
func (a *CachedAuthenticator) Authenticate(ctx context.Context, username, password string,
) (sdktelemetry.DeviceIdentity, bool, error) {
	return a.authenticate(ctx, username, password, false)
}

// AuthenticateCertificate validates an mTLS leaf fingerprint.
func (a *CachedAuthenticator) AuthenticateCertificate(ctx context.Context, credentialsID, fingerprint string,
) (sdktelemetry.DeviceIdentity, bool, error) {
	return a.authenticate(ctx, credentialsID, fingerprint, true)
}

// authenticate implements both entry points. certificate distinguishes the
// X509 path where secret is the leaf fingerprint.
func (a *CachedAuthenticator) authenticate(ctx context.Context, credentialsID, secret string,
	certificate bool) (sdktelemetry.DeviceIdentity, bool, error) {
	if entry := a.read(credentialsID); entry != nil {
		if entry.Negative {
			a.metrics.negativeHits.Inc()
			return sdktelemetry.DeviceIdentity{}, false, nil
		}
		presented, fingerprint := secret, ""
		if certificate {
			presented, fingerprint = "", secret
		}
		ok, err := credverify.Verify(ctx, entry.Record, presented, fingerprint, a.resolver)
		if err == nil && ok {
			a.metrics.hits.Inc()
			return entry.Identity, true, nil
		}
		// Local verification failed: the credential may have rotated.
		// Resolve through the fallback rather than trusting the stale
		// record.
	}
	a.metrics.misses.Inc()

	type outcome struct {
		identity sdktelemetry.DeviceIdentity
		ok       bool
	}
	v, err, _ := a.flights.Do(credentialsID, func() (any, error) {
		var (
			result sdkregistry.DetailedResult
			err    error
		)
		if certificate {
			result, err = a.fallback.AuthenticateCertificateDetailed(ctx, credentialsID, secret)
		} else {
			result, err = a.fallback.AuthenticateDetailed(ctx, credentialsID, secret)
		}
		if err != nil {
			return nil, err
		}
		if !result.OK {
			a.write(credentialsID, &Entry{Negative: true}, a.negativeTTL)
			return outcome{}, nil
		}
		entry := &Entry{
			Identity: result.Identity,
			Record: credverify.Record{
				Type:   result.Record.Type,
				Secret: result.Record.CredentialsValue,
				Extra:  result.Record.Extra,
			},
			Epoch: result.Record.CredentialEpoch,
		}
		if result.Record.HasExpiry {
			expiry := result.Record.ExpiresAt
			entry.ExpiresAt = &expiry
		}
		a.write(credentialsID, entry, a.entryTTL(entry))
		return outcome{identity: result.Identity, ok: true}, nil
	})
	if err != nil {
		return sdktelemetry.DeviceIdentity{}, false, err
	}
	res := v.(outcome)
	return res.identity, res.ok, nil
}

// read resolves an entry through L1 and then L2, backfilling L1 from L2.
func (a *CachedAuthenticator) read(credentialsID string) *Entry {
	if entry, ok := a.l1.get(credentialsID); ok && entry.fresh() {
		return entry
	}
	if a.l2 == nil {
		return nil
	}
	entry, err := a.l2.get(context.Background(), credentialsID)
	if err != nil || entry == nil || !entry.fresh() {
		return nil
	}
	a.l1.set(credentialsID, entry, time.Until(entry.CachedUntil))
	return entry
}

// fresh reports whether the entry may still be reused.
func (e *Entry) fresh() bool {
	return !e.CachedUntil.IsZero() && time.Now().Before(e.CachedUntil)
}

// write stores an entry in L1 and L2. A failed L2 write does not lose the
// local decision; it is logged so a transient NATS issue stays visible.
func (a *CachedAuthenticator) write(credentialsID string, entry *Entry, ttl time.Duration) {
	entry.CachedUntil = time.Now().Add(ttl)
	a.l1.set(credentialsID, entry, ttl)
	if a.l2 != nil {
		if err := a.l2.put(context.Background(), credentialsID, entry, ttl); err != nil {
			logCacheWarn("l2 put failed", credentialsID, err)
		}
	}
}

// entryTTL caps the configured TTL at the credential expiry so an expiring
// credential cannot be served past its validity window.
func (a *CachedAuthenticator) entryTTL(entry *Entry) time.Duration {
	ttl := a.positiveTTL
	if entry.ExpiresAt != nil {
		remaining := time.Until(*entry.ExpiresAt)
		if remaining < ttl {
			ttl = remaining
		}
	}
	if ttl < time.Millisecond {
		return time.Millisecond
	}
	return ttl
}

// evict removes one credential from every tier.
func (a *CachedAuthenticator) evict(credentialsID string) {
	a.l1.delete(credentialsID)
	if a.l2 != nil {
		if err := a.l2.delete(context.Background(), credentialsID); err != nil {
			logCacheWarn("l2 delete failed", credentialsID, err)
		}
	}
	a.metrics.evictions.Inc()
}

// fenceTenant clears cached credentials of one tenant after an emergency
// epoch bump. L1 is cleared wholesale (node-local, bounded cost) and L2
// entries belonging to the tenant are deleted individually.
func (a *CachedAuthenticator) fenceTenant(tenantID string) {
	a.l1.clear()
	if a.l2 == nil {
		return
	}
	ids, err := a.l2.keys()
	if err != nil {
		logCacheWarn("l2 list keys failed", "", err)
		return
	}
	for _, id := range ids {
		entry, err := a.l2.get(context.Background(), id)
		if err != nil || entry == nil {
			continue
		}
		if entry.Identity.TenantID == tenantID {
			a.evict(id)
		}
	}
}
