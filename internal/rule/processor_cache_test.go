// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package rule

import (
	"context"
	"testing"
	"time"
)

// TestGraphForNegativeCacheNeverReturnsNilGraph reproduces the crash
// behind the HA CrashLoopBackOff: the first message for a device
// without an active chain writes a nil-graph negative cache entry; the
// second message within reloadInterval hit the fast path and received
// (nil, nil), after which handle called (*Graph).Execute on nil and
// panicked the process.
func TestGraphForNegativeCacheNeverReturnsNilGraph(t *testing.T) {
	p := &Processor{
		chains:         map[string]*cachedChain{},
		reloadInterval: time.Minute,
	}
	const device = "dev-1"

	// Seed the negative entry exactly as graphFor does on ErrNoActiveChain.
	p.chains[device] = &cachedChain{checked: time.Now()}

	graph, err := p.graphFor(context.Background(), "tenant-1", device)
	if err != ErrNoActiveChain {
		t.Fatalf("err = %v, want ErrNoActiveChain", err)
	}
	if graph != nil {
		t.Fatalf("graph = %v, want nil", graph)
	}
}
