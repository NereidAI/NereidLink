// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package leaderelection runs exactly one replica of a named background role
// across a serve replica set, using a JetStream KV bucket as the lock.
//
// The lease is a single KV key: the holder creates it (revision 0) and keeps
// it alive with revision-guarded renewals before the bucket TTL expires;
// everyone else waits on the key watch and races to create it the moment it
// is deleted or expires. A failed renewal cancels the role context, so a
// partitioned leader steps down instead of running split-brain.
//
// The single all-in-one topology never constructs an Elector: main wires
// roles directly, so docker compose and one-pod k3s keep the old behavior.
package leaderelection

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"golang.org/x/sync/errgroup"
)

// Bucket is the KV bucket holding one key per exclusive role.
const Bucket = "NL_LEADER_ELECTION"

// leaseValue is the JSON payload of a lease key.
type leaseValue struct {
	Holder  string    `json:"holder"`
	RenewAt time.Time `json:"renew_at"`
}

// Elector campaigns for named roles on behalf of one instance.
type Elector struct {
	kv            jetstream.KeyValue
	instanceID    string
	ttl           time.Duration
	renewInterval time.Duration
	log           *slog.Logger
}

// New creates or updates the lease bucket and returns an Elector.
func New(ctx context.Context, js jetstream.JetStream, instanceID string, replicas int, ttl time.Duration, log *slog.Logger) (*Elector, error) {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	if replicas < 1 {
		replicas = 1
	}
	if log == nil {
		log = slog.Default()
	}
	kv, err := js.CreateOrUpdateKeyValue(ctx, jetstream.KeyValueConfig{
		Bucket:   Bucket,
		TTL:      ttl,
		Replicas: replicas,
		History:  1,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure leader election KV: %w", err)
	}
	return &Elector{
		kv:            kv,
		instanceID:    instanceID,
		ttl:           ttl,
		renewInterval: ttl / 2,
		log:           log,
	}, nil
}

// Run campaigns for role and, while leader, executes fn with a context that
// is canceled the instant leadership is lost. It re-campaigns after every
// loss and returns when ctx is canceled. fn must honor its context.
func (e *Elector) Run(parent context.Context, role string, fn func(ctx context.Context) error) error {
	key := sanitizeKey(role)
	for {
		if parent.Err() != nil {
			return nil
		}
		err := e.acquireThenHold(parent, key, role, fn)
		if parent.Err() != nil {
			return nil
		}
		if err != nil {
			e.log.Warn("leader election cycle ended", "role", role, "error", err)
		}
		if !sleep(parent, e.renewInterval) {
			return nil
		}
	}
}

// acquireThenHold waits until it wins the lease, runs fn while renewing it,
// and returns when leadership or the role is lost.
func (e *Elector) acquireThenHold(parent context.Context, key, role string, fn func(ctx context.Context) error) error {
	revision, err := e.acquire(parent, key)
	if err != nil {
		return err
	}
	e.log.Info("leadership acquired", "role", role, "instance", e.instanceID)
	defer e.log.Info("leadership released", "role", role, "instance", e.instanceID)

	leadCtx, cancel := context.WithCancel(parent)
	defer cancel()
	return e.hold(leadCtx, key, revision, fn)
}

// acquire creates the lease key. While another holder owns it, it watches
// the key and retries on delete/expiry.
func (e *Elector) acquire(ctx context.Context, key string) (uint64, error) {
	for {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		value, _ := json.Marshal(leaseValue{
			Holder:  e.instanceID,
			RenewAt: time.Now().Add(e.renewInterval),
		})
		revision, err := e.kv.Create(ctx, key, value)
		if err == nil {
			return revision, nil
		}

		watcher, werr := e.kv.Watch(ctx, key, jetstream.IgnoreDeletes())
		if werr != nil {
			return 0, werr
		}
		turned := e.waitForTurn(ctx, key, watcher)
		_ = watcher.Stop()
		if !turned {
			return 0, ctx.Err()
		}
	}
}

// waitForTurn blocks until the lease is deleted/expires (a nil watch entry)
// or until the periodic existence probe finds the key gone. It returns false
// when the parent context ends.
func (e *Elector) waitForTurn(ctx context.Context, key string, watcher jetstream.KeyWatcher) bool {
	// Drain the initial snapshot entry.
	ticker := time.NewTicker(e.ttl)
	defer ticker.Stop()
	primed := false
	for {
		select {
		case <-ctx.Done():
			return false
		case entry, ok := <-watcher.Updates():
			if !ok {
				return false
			}
			if !primed {
				primed = true
				continue
			}
			if entry == nil {
				return true
			}
		case <-ticker.C:
			if _, err := e.kv.Get(ctx, key); errors.Is(err, jetstream.ErrKeyNotFound) {
				return true
			}
		}
	}
}

// hold renews the lease until it is lost while running fn.
func (e *Elector) hold(ctx context.Context, key string, revision uint64, fn func(ctx context.Context) error) error {
	var mu sync.Mutex
	rev := revision

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		ticker := time.NewTicker(e.renewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-gctx.Done():
				return nil
			case <-ticker.C:
				mu.Lock()
				current := rev
				mu.Unlock()
				value, _ := json.Marshal(leaseValue{
					Holder:  e.instanceID,
					RenewAt: time.Now().Add(e.renewInterval),
				})
				next, err := e.kv.Update(gctx, key, value, current)
				if err != nil {
					return fmt.Errorf("renew lease: %w", err)
				}
				mu.Lock()
				rev = next
				mu.Unlock()
			}
		}
	})
	g.Go(func() error {
		return fn(gctx)
	})
	return g.Wait()
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// sanitizeKey maps a role name to a legal KV key token.
func sanitizeKey(role string) string {
	r := strings.NewReplacer(":", "-", "/", "-", " ", "-", "_", "-")
	return strings.ToLower(r.Replace(role))
}
