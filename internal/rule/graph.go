// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// graph.go defines the chain graph DTO and the validation/compilation of a
// graph document into an executable graph.

package rule

import (
	"encoding/json"
	"fmt"
)

// NodeDTO is one node in a chain graph document.
type NodeDTO struct {
	// ID is the local graph identifier; CreateDraft replaces it with a
	// fresh stored identifier and rewrites edge references accordingly.
	ID string `json:"id"`

	// Type is the node type identifier.
	Type string `json:"type"`

	// Name is the human-readable node name.
	Name string `json:"name"`

	// Config is the node configuration document.
	Config json.RawMessage `json:"config"`

	// PositionX is the canvas x coordinate; null for unpositioned nodes.
	PositionX *float64 `json:"position_x"`

	// PositionY is the canvas y coordinate; null for unpositioned nodes.
	PositionY *float64 `json:"position_y"`
}

// EdgeDTO is one edge in a chain graph document.
type EdgeDTO struct {
	// From is the source node identifier.
	From string `json:"from"`

	// To is the target node identifier.
	To string `json:"to"`

	// Relation is the edge label.
	Relation string `json:"relation"`
}

// GraphDTO is a full chain graph version document.
type GraphDTO struct {
	// Nodes is the ordered node set.
	Nodes []NodeDTO `json:"nodes"`

	// Edges is the edge set.
	Edges []EdgeDTO `json:"edges"`
}

// Graph is an executable, compiled chain graph.
type Graph struct {
	// chainID owns the graph.
	chainID string

	// nodes maps node identifiers to executors.
	nodes map[string]Node

	// targets maps "node.relation" to destination node identifiers.
	targets map[string][]string

	// roots are nodes without incoming edges; messages enter them.
	roots []string
}

// ChainID returns the owning chain identifier.
func (g *Graph) ChainID() string { return g.chainID }

// Compile validates and compiles a graph document into an executable graph.
func Compile(chainID string, dto GraphDTO, deps Deps) (*Graph, error) {
	graph := &Graph{
		chainID: chainID,
		nodes:   map[string]Node{},
		targets: map[string][]string{},
	}

	hasIncoming := map[string]bool{}
	for _, dtoNode := range dto.Nodes {
		if dtoNode.ID == "" {
			return nil, fmt.Errorf("node is missing an id: type=%s", dtoNode.Type)
		}
		if _, exists := graph.nodes[dtoNode.ID]; exists {
			return nil, fmt.Errorf("duplicate node id: %s", dtoNode.ID)
		}
		node, err := buildNode(dtoNode.ID, dtoNode.Type, dtoNode.Config, deps)
		if err != nil {
			return nil, err
		}
		graph.nodes[dtoNode.ID] = node
	}

	for _, edge := range dto.Edges {
		if _, ok := graph.nodes[edge.From]; !ok {
			return nil, fmt.Errorf("edge source node not found: %s", edge.From)
		}
		if _, ok := graph.nodes[edge.To]; !ok {
			return nil, fmt.Errorf("edge target node not found: %s", edge.To)
		}
		key := edge.From + "." + edge.Relation
		graph.targets[key] = append(graph.targets[key], edge.To)
		hasIncoming[edge.To] = true
	}

	for id := range graph.nodes {
		if !hasIncoming[id] {
			graph.roots = append(graph.roots, id)
		}
	}
	if len(graph.roots) == 0 {
		return nil, fmt.Errorf("chain graph has no root nodes")
	}
	return graph, nil
}
