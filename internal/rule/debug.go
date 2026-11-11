// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// debug.go implements node debug captures and bounded full-chain traces.

package rule

import (
	"sync"
	"time"
)

// Capture is one debug record for a node.
type Capture struct {
	// Time is the capture time as RFC3339 UTC.
	Time string `json:"time"`

	// In is the inbound message.
	In Msg `json:"in"`

	// Out lists the emitted messages per relation.
	Out []Branch `json:"out"`

	// DurationMS is the node execution time in milliseconds.
	DurationMS float64 `json:"duration_ms"`

	// Error carries the node failure detail.
	Error string `json:"error,omitempty"`
}

// TraceCapture pairs a full-chain trace for trace retrieval.
type TraceCapture struct {
	// At is the capture time.
	At time.Time

	// Trace is the recorded trace.
	Trace Trace
}

// DebugStore holds node debug captures and chain traces. Collection is
// in-memory and short-lived (records expire after captureTTL), so it never
// touches the hot path when disabled nor the long-term database.
type DebugStore struct {
	mu sync.Mutex

	// nodeDebug lists nodes currently capturing.
	nodeDebug map[string]struct{}

	// captures keeps a bounded ring per node.
	captures map[string][]Capture

	// chainTrace lists chains currently tracing.
	chainTrace map[string]struct{}

	// traces keeps a bounded ring per chain.
	traces map[string][]TraceCapture

	// capturePerNode bounds the ring size.
	capturePerNode int

	// tracesPerChain bounds the trace ring size.
	tracesPerChain int

	// captureTTL bounds capture lifetime.
	captureTTL time.Duration
}

// NewDebugStore constructs the debug and trace store.
func NewDebugStore(capturePerNode, tracesPerChain int, captureTTL time.Duration) *DebugStore {
	return &DebugStore{
		nodeDebug:      map[string]struct{}{},
		captures:       map[string][]Capture{},
		chainTrace:     map[string]struct{}{},
		traces:         map[string][]TraceCapture{},
		capturePerNode: capturePerNode,
		tracesPerChain: tracesPerChain,
		captureTTL:     captureTTL,
	}
}

// SetNodeDebug enables or disables capture for one node.
func (d *DebugStore) SetNodeDebug(nodeID string, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if enabled {
		d.nodeDebug[nodeID] = struct{}{}
	} else {
		delete(d.nodeDebug, nodeID)
	}
}

// NodeEnabled reports whether a node captures. It is called on the hot path,
// so the lock must be short; an absent lookup is the common zero-cost case.
func (d *DebugStore) NodeEnabled(nodeID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.nodeDebug[nodeID]
	return ok
}

// SetChainTrace enables or disables full-chain tracing.
func (d *DebugStore) SetChainTrace(chainID string, enabled bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if enabled {
		d.chainTrace[chainID] = struct{}{}
	} else {
		delete(d.chainTrace, chainID)
	}
}

// OnNode implements ExecHook: a capture is appended only when the node is in
// debug mode. Expired entries are pruned on append.
func (d *DebugStore) OnNode(node Node, in Msg, outputs []Branch, duration time.Duration, failure error) {
	d.mu.Lock()
	_, enabled := d.nodeDebug[node.ID()]
	d.mu.Unlock()
	if !enabled {
		return
	}

	capture := Capture{
		Time:       time.Now().UTC().Format(time.RFC3339),
		In:         in,
		Out:        outputs,
		DurationMS: float64(duration.Microseconds()) / 1000,
	}
	if failure != nil {
		capture.Error = failure.Error()
	}

	d.mu.Lock()
	ring := pruneCaptures(d.captures[node.ID()], d.captureTTL)
	ring = append(ring, capture)
	if len(ring) > d.capturePerNode {
		ring = ring[len(ring)-d.capturePerNode:]
	}
	d.captures[node.ID()] = ring
	d.mu.Unlock()
}

// Captures returns the live captures for one node, newest last.
func (d *DebugStore) Captures(nodeID string) []Capture {
	d.mu.Lock()
	defer d.mu.Unlock()
	return pruneCaptures(d.captures[nodeID], d.captureTTL)
}

// RecordTrace appends a trace when its chain is in trace mode.
func (d *DebugStore) RecordTrace(trace Trace) {
	d.mu.Lock()
	_, enabled := d.chainTrace[trace.ChainID]
	d.mu.Unlock()
	if !enabled {
		return
	}

	d.mu.Lock()
	ring := append(d.traces[trace.ChainID], TraceCapture{
		At:    time.Now().UTC(),
		Trace: trace,
	})
	if len(ring) > d.tracesPerChain {
		ring = ring[len(ring)-d.tracesPerChain:]
	}
	d.traces[trace.ChainID] = ring
	d.mu.Unlock()
}

// Traces returns recent traces for a chain, optionally filtered by trace ID.
func (d *DebugStore) Traces(chainID, traceID string) []Trace {
	d.mu.Lock()
	defer d.mu.Unlock()

	var result []Trace
	for _, item := range d.traces[chainID] {
		if traceID != "" && item.Trace.TraceID != traceID {
			continue
		}
		result = append(result, item.Trace)
	}
	return result
}

// pruneCaptures drops entries older than captureTTL.
func pruneCaptures(ring []Capture, ttl time.Duration) []Capture {
	if ttl <= 0 {
		return ring
	}
	cutoff := time.Now().UTC().Add(-ttl)
	kept := ring[:0]
	for _, capture := range ring {
		at, err := time.Parse(time.RFC3339, capture.Time)
		if err != nil || at.Before(cutoff) {
			continue
		}
		kept = append(kept, capture)
	}
	return kept
}
