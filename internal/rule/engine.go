// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// engine.go executes one message across a compiled graph, recording a
// per-node trace and honoring dry-run side-effect suppression.

package rule

import (
	"context"
	"fmt"
	"time"
)

// maxGraphSteps bounds node executions of one message, preventing an
// accidental cycle in a drafted chain from looping forever.
const maxGraphSteps = 1000

// NodeRecord is one per-node execution record in a chain trace.
type NodeRecord struct {
	// NodeID is the executed node.
	NodeID string `json:"node_id"`

	// Type is the node type.
	Type string `json:"node_type"`

	// Entered is the execution start time as RFC3339 UTC.
	Entered string `json:"entered"`

	// DurationMS is the node execution time in milliseconds.
	DurationMS float64 `json:"duration_ms"`

	// In is the message that arrived at the node.
	In Msg `json:"in"`

	// Out lists the messages emitted per relation.
	Out []Branch `json:"out,omitempty"`

	// Outputs lists the emitted relation labels.
	Outputs []string `json:"outputs,omitempty"`

	// Error carries the node failure detail.
	Error string `json:"error,omitempty"`
}

// Trace is the full per-message execution trace.
type Trace struct {
	// TraceID identifies the chain execution.
	TraceID string `json:"trace_id"`

	// ChainID is the executed chain.
	ChainID string `json:"chain_id"`

	// Started is the execution start time as RFC3339 UTC.
	Started string `json:"started"`

	// Records are the per-node records in execution order.
	Records []NodeRecord `json:"records"`
}

// ExecHook observes node executions for debugging and tracing.
type ExecHook interface {
	// OnNode is called after each node execution.
	OnNode(node Node, in Msg, outputs []Branch, duration time.Duration, failure error)
}

// pendingItem is one queued node execution in the traversal.
type pendingItem struct {
	// nodeID is the next node to execute.
	nodeID string

	// msg is the arriving message.
	msg Msg

	// steps is the traversal depth.
	steps int
}

// Execute runs one message through the graph. dryRun suppresses side
// effects; hooks receive every node execution when supplied.
func (g *Graph) Execute(ctx context.Context, msg Msg, dryRun bool, hook ExecHook) (Trace, error) {
	trace := Trace{
		TraceID: msg.TraceID,
		ChainID: g.chainID,
		Started: time.Now().UTC().Format(time.RFC3339),
	}

	queue := make([]pendingItem, 0, len(g.roots))
	for _, root := range g.roots {
		queue = append(queue, pendingItem{nodeID: root, msg: msg})
	}

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]

		if item.steps >= maxGraphSteps {
			return trace, fmt.Errorf("rule chain exceeded %d steps, possible cycle", maxGraphSteps)
		}
		node := g.nodes[item.nodeID]

		started := time.Now()
		branches, err := node.Execute(ctx, item.msg, dryRun)
		duration := time.Since(started)

		record := NodeRecord{
			NodeID:     node.ID(),
			Type:       node.Type(),
			Entered:    started.UTC().Format(time.RFC3339),
			DurationMS: float64(duration.Microseconds()) / 1000,
			In:         item.msg,
			Out:        branches,
		}
		for _, branch := range branches {
			record.Outputs = append(record.Outputs, branch.Relation)
		}
		if err != nil {
			record.Error = err.Error()
		}
		trace.Records = append(trace.Records, record)

		if hook != nil {
			hook.OnNode(node, item.msg, branches, duration, err)
		}

		if err != nil {
			// A Failure relation may catch the error; if no edge exists the
			// branch dies. Continue traversal for the Failure label only.
			failed := Msg(item.msg)
			queue = append(queue, g.enqueue(node.ID(), RelationFailure, failed, item.steps)...)
			continue
		}

		for _, branch := range branches {
			queue = append(queue, g.enqueue(node.ID(), branch.Relation, branch.Msg, item.steps)...)
		}
	}

	return trace, nil
}

// enqueue returns the queued items for one labeled node output.
func (g *Graph) enqueue(nodeID, relation string, msg Msg, steps int) []pendingItem {
	targets := g.targets[nodeID+"."+relation]
	items := make([]pendingItem, 0, len(targets))
	for _, target := range targets {
		items = append(items, pendingItem{
			nodeID: target,
			msg:    msg,
			steps:  steps + 1,
		})
	}
	return items
}
