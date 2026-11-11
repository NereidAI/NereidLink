// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// authenticator_test.go verifies read-through caching, negative caching,
// TTL expiry and explicit eviction without a registry or NATS server.

package credcache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	sdkregistry "github.com/nereidai/nereidlink/sdk/registry"
	sdktelemetry "github.com/nereidai/nereidlink/sdk/telemetry"
)

// fakeFallback returns a TOKEN credential and counts upstream calls.
type fakeFallback struct {
	calls int32
}

// AuthenticateDetailed emulates registry resolution.
func (f *fakeFallback) AuthenticateDetailed(_ context.Context, _, password string) (sdkregistry.DetailedResult, error) {
	atomic.AddInt32(&f.calls, 1)
	if password != "device-secret" {
		return sdkregistry.DetailedResult{OK: false}, nil
	}
	return sdkregistry.DetailedResult{
		OK: true,
		Identity: sdktelemetry.DeviceIdentity{
			TenantID: "tenant-1",
			DeviceID: "device-1",
		},
		Record: &sdkregistry.CredentialRecord{
			Type:             "TOKEN",
			CredentialsValue: "device-secret",
			CredentialEpoch:  7,
		},
	}, nil
}

// AuthenticateCertificateDetailed is unused by these tests but satisfies
// the fallback interface.
func (f *fakeFallback) AuthenticateCertificateDetailed(context.Context, string, string,
) (sdkregistry.DetailedResult, error) {
	atomic.AddInt32(&f.calls, 1)
	return sdkregistry.DetailedResult{OK: false}, nil
}

// newTestAuthenticator builds an L1-only authenticator with the given TTLs.
func newTestAuthenticator(t *testing.T, fallback DetailedFallback, positive, negative time.Duration) *CachedAuthenticator {
	t.Helper()
	l1, err := NewL1(L1Config{MaxCost: 1 << 20, NumCounters: 1000})
	if err != nil {
		t.Fatalf("new l1: %v", err)
	}
	return NewCachedAuthenticator(fallback, l1, nil,
		AuthenticatorConfig{PositiveTTL: positive, NegativeTTL: negative}, nil)
}

// TestPositiveCacheServesSecondAuthenticationFromL1 ensures one successful
// fallback resolution is reused for subsequent connection attempts.
func TestPositiveCacheServesSecondAuthenticationFromL1(t *testing.T) {
	fallback := &fakeFallback{}
	auth := newTestAuthenticator(t, fallback, time.Minute, time.Second)

	identity, ok, err := auth.Authenticate(context.Background(), "cred-1", "device-secret")
	if err != nil || !ok {
		t.Fatalf("first authentication ok=%v err=%v", ok, err)
	}
	if identity.TenantID != "tenant-1" || identity.DeviceID != "device-1" {
		t.Fatalf("unexpected identity: %+v", identity)
	}
	identity, ok, err = auth.Authenticate(context.Background(), "cred-1", "device-secret")
	if err != nil || !ok {
		t.Fatalf("second authentication ok=%v err=%v", ok, err)
	}
	if identity.TenantID != "tenant-1" {
		t.Fatalf("unexpected cached identity: %+v", identity)
	}
	if got := atomic.LoadInt32(&fallback.calls); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

// TestNegativeCacheRejectsWithoutFallback ensures repeated failed attempts
// converge to one registry resolution during the negative TTL.
func TestNegativeCacheRejectsWithoutFallback(t *testing.T) {
	fallback := &fakeFallback{}
	auth := newTestAuthenticator(t, fallback, time.Minute, time.Second)

	for i := 0; i < 2; i++ {
		_, ok, err := auth.Authenticate(context.Background(), "cred-1", "wrong-secret")
		if err != nil {
			t.Fatalf("authentication %d error: %v", i, err)
		}
		if ok {
			t.Fatalf("authentication %d unexpectedly succeeded", i)
		}
	}
	if got := atomic.LoadInt32(&fallback.calls); got != 1 {
		t.Fatalf("fallback calls = %d, want 1", got)
	}
}

// TestPositiveTTLExpires forces a second fallback resolution after the
// cached entry exceeds its TTL.
func TestPositiveTTLExpires(t *testing.T) {
	fallback := &fakeFallback{}
	auth := newTestAuthenticator(t, fallback, 20*time.Millisecond, time.Second)

	if _, ok, err := auth.Authenticate(context.Background(), "cred-1", "device-secret"); err != nil || !ok {
		t.Fatalf("first authentication ok=%v err=%v", ok, err)
	}
	time.Sleep(40 * time.Millisecond)
	if _, ok, err := auth.Authenticate(context.Background(), "cred-1", "device-secret"); err != nil || !ok {
		t.Fatalf("second authentication ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&fallback.calls); got != 2 {
		t.Fatalf("fallback calls = %d, want 2", got)
	}
}

// TestEvictAllowsImmediateRefallback ensures explicit invalidation removes
// the positive entry without waiting for its TTL.
func TestEvictAllowsImmediateRefallback(t *testing.T) {
	fallback := &fakeFallback{}
	auth := newTestAuthenticator(t, fallback, time.Minute, time.Second)

	if _, ok, err := auth.Authenticate(context.Background(), "cred-1", "device-secret"); err != nil || !ok {
		t.Fatalf("first authentication ok=%v err=%v", ok, err)
	}
	auth.evict("cred-1")
	if _, ok, err := auth.Authenticate(context.Background(), "cred-1", "device-secret"); err != nil || !ok {
		t.Fatalf("second authentication ok=%v err=%v", ok, err)
	}
	if got := atomic.LoadInt32(&fallback.calls); got != 2 {
		t.Fatalf("fallback calls = %d, want 2", got)
	}
}

// TestEntryFreshness enforces the L2-compatible embedded cache deadline.
func TestEntryFreshness(t *testing.T) {
	expired := &Entry{CachedUntil: time.Now().Add(-time.Second)}
	if expired.fresh() {
		t.Fatal("entry with past CachedUntil reported fresh")
	}
	valid := &Entry{CachedUntil: time.Now().Add(time.Second)}
	if !valid.fresh() {
		t.Fatal("entry with future CachedUntil reported stale")
	}
	zero := &Entry{}
	if zero.fresh() {
		t.Fatal("entry with zero CachedUntil reported fresh")
	}
}
