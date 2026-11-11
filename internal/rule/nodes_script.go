// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_script.go implements the script-backed control nodes: Script Filter,
// Switch and Transform Script.

package rule

import (
	"context"
	"fmt"
)

// Scripter evaluates user JavaScript under the ScriptRuntime contract.
type Scripter interface {
	EvalBoolean(source string, msg, metadata map[string]any) (bool, error)
	EvalObject(source string, msg, metadata map[string]any) (map[string]any, error)
}

// filterConfig is the Script Filter node configuration.
type filterConfig struct {
	// Script is a boolean JavaScript expression.
	Script string `json:"script"`
}

// scriptFilter passes messages to True or False based on one boolean script.
type scriptFilter struct {
	id     string
	config filterConfig
	script Scripter
}

func (n *scriptFilter) ID() string   { return n.id }
func (n *scriptFilter) Type() string { return NodeTypeScriptFilter }

func (n *scriptFilter) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	ok, err := n.script.EvalBoolean(n.config.Script, in.Data, in.Metadata)
	if err != nil {
		return nil, fmt.Errorf("filter script: %w", err)
	}
	relation := RelationFalse
	if ok {
		relation = RelationTrue
	}
	return []Branch{{Relation: relation, Msg: in}}, nil
}

// switchCaseConfig is one Switch branch condition.
type switchCaseConfig struct {
	// Label is the outgoing relation label when the script is true.
	Label string `json:"label"`

	// Script is the boolean condition.
	Script string `json:"script"`
}

// switchConfig is the Switch node configuration.
type switchConfig struct {
	// Cases are evaluated in order; the first true case wins.
	Cases []switchCaseConfig `json:"cases"`

	// HasDefault enables the Default relation when no case matches.
	HasDefault bool `json:"has_default"`
}

// switchNode routes messages to the first matching labeled branch.
type switchNode struct {
	id     string
	config switchConfig
	script Scripter
}

func (n *switchNode) ID() string   { return n.id }
func (n *switchNode) Type() string { return NodeTypeSwitch }

func (n *switchNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	for _, branch := range n.config.Cases {
		ok, err := n.script.EvalBoolean(branch.Script, in.Data, in.Metadata)
		if err != nil {
			return nil, fmt.Errorf("switch case %q: %w", branch.Label, err)
		}
		if ok {
			return []Branch{{Relation: branch.Label, Msg: in}}, nil
		}
	}
	if n.config.HasDefault {
		return []Branch{{Relation: RelationDefault, Msg: in}}, nil
	}
	return nil, nil
}

// transformConfig is the Transform Script node configuration.
type transformConfig struct {
	// Script returns the new message body.
	Script string `json:"script"`
}

// transformNode replaces the message body with the script result.
type transformNode struct {
	id     string
	config transformConfig
	script Scripter
}

func (n *transformNode) ID() string   { return n.id }
func (n *transformNode) Type() string { return NodeTypeTransform }

func (n *transformNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	result, err := n.script.EvalObject(n.config.Script, in.Data, in.Metadata)
	if err != nil {
		return nil, fmt.Errorf("transform script: %w", err)
	}
	in.Data = result
	return []Branch{{Relation: RelationSuccess, Msg: in}}, nil
}
