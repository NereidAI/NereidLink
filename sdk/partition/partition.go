// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package partition defines the fixed logical partitioning scheme used by
// every stateful pipeline.
//
// The partition count is fixed at Count for the entire lifetime of the
// platform and MUST NEVER be remapped: messages are routed by partition,
// consumers claim partitions, and changing the count would silently reorder
// history. Horizontal scaling is achieved by changing how many consumers
// claim partitions, not by rehashing.
package partition

import (
	"hash/fnv"
)

const (
	// Count is the immutable number of logical partitions.
	Count = 1024

	// StreamShards is the number of physical JetStream stream shards per
	// message domain. Shard = partition / (Count / StreamShards), so each
	// shard covers Count/StreamShards consecutive partitions.
	StreamShards = 16
)

// Of returns the logical partition number for a device identifier.
// FNV-1a is deterministic, allocation-free for small inputs and gives a
// sufficiently even spread for UUID-shaped keys.
func Of(deviceID string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceID))
	return h.Sum32() % Count
}

// Shard maps a logical partition to its physical stream shard. Consecutive
// partitions share a shard, which keeps the total number of Raft groups
// independent of the tenant count.
func Shard(partition uint32) uint32 {
	return partition / (Count / StreamShards)
}
