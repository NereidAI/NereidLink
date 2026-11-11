// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// l2.go implements the shared tier backed by a NATS KV bucket.
//
// Keys are a fixed prefix plus the hex of the credentials id because KV
// key tokens reject some characters that a credentials id can contain
// (for example underscores). Entries carry their own per-entry TTL.

package credcache

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/nats-io/nats.go"
)

// l2KeyPrefix namespaces credential entries inside the bucket.
const l2KeyPrefix = "k/"

// DefaultL2Bucket is the KV bucket name used when configuration names none.
const DefaultL2Bucket = "CREDENTIALS"

// l2Cache wraps a NATS KV bucket.
type l2Cache struct {
	kv nats.KeyValue
}

// L2Cache is the exported alias for composition-root wiring.
type L2Cache = l2Cache

// NewL2 creates or opens the shared bucket through js. bucketTTL is the
// stream-level maximum age; per-entry freshness is additionally enforced
// from Entry.CachedUntil because this client has no per-key Put TTL.
// replicas is the JetStream Raft factor (1 for the bundled single NATS,
// 3/5 clustered).
func NewL2(js nats.JetStreamContext, bucket string, bucketTTL time.Duration, replicas int) (*l2Cache, error) {
	if bucket == "" {
		bucket = DefaultL2Bucket
	}
	if bucketTTL <= 0 {
		bucketTTL = DefaultPositiveTTL
	}
	if replicas < 1 {
		replicas = 1
	}
	kv, err := js.CreateKeyValue(&nats.KeyValueConfig{
		Bucket:   bucket,
		History:  1,
		TTL:      bucketTTL,
		Storage:  nats.FileStorage,
		Replicas: replicas,
	})
	if err != nil {
		if !errors.Is(err, nats.ErrStreamNameAlreadyInUse) {
			return nil, err
		}
		kv, err = js.KeyValue(bucket)
		if err != nil {
			return nil, err
		}
	}
	return &l2Cache{kv: kv}, nil
}

// key maps a credentials id to a KV key.
func key(credentialsID string) string {
	return l2KeyPrefix + hex.EncodeToString([]byte(credentialsID))
}

// get loads one entry; a missing key yields nil without an error.
func (l *l2Cache) get(_ context.Context, credentialsID string) (*Entry, error) {
	raw, err := l.kv.Get(key(credentialsID))
	if err != nil {
		if errors.Is(err, nats.ErrKeyNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var entry Entry
	if err := json.Unmarshal(raw.Value(), &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

// put stores one entry. The ttl is recorded as Entry.CachedUntil by the
// caller; the bucket-level TTL provides the server-side cleanup backstop.
func (l *l2Cache) put(_ context.Context, credentialsID string, entry *Entry, _ time.Duration) error {
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	_, err = l.kv.Put(key(credentialsID), raw)
	return err
}

// delete removes one entry.
func (l *l2Cache) delete(_ context.Context, credentialsID string) error {
	return l.kv.Delete(key(credentialsID))
}

// keys lists the stored credentials ids, used by the emergency epoch fence.
func (l *l2Cache) keys() ([]string, error) {
	lister, err := l.kv.ListKeys()
	if err != nil {
		if errors.Is(err, nats.ErrNoKeysFound) {
			return nil, nil
		}
		return nil, err
	}
	var ids []string
	for k := range lister.Keys() {
		if len(k) < len(l2KeyPrefix) || k[:len(l2KeyPrefix)] != l2KeyPrefix {
			continue
		}
		raw, err := hex.DecodeString(k[len(l2KeyPrefix):])
		if err != nil {
			continue
		}
		ids = append(ids, string(raw))
	}
	if err := <-lister.Error(); err != nil {
		return nil, err
	}
	return ids, nil
}
