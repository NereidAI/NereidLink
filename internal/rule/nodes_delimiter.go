// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// nodes_delimiter.go implements the Delimiter node that demuxes an array
// field into separate messages.

package rule

import "context"

// delimiterConfig is the Delimiter node configuration.
type delimiterConfig struct {
	// Field names the array field demuxed into separate messages.
	Field string `json:"field"`
}

// delimiterNode splits a message carrying an array into one message per
// element. This is the MVP form of frame demux; character/byte delimiters
// for legacy protocols are added in later versions.
type delimiterNode struct {
	id     string
	config delimiterConfig
}

func (n *delimiterNode) ID() string   { return n.id }
func (n *delimiterNode) Type() string { return NodeTypeDelimiter }

func (n *delimiterNode) Execute(_ context.Context, in Msg, _ bool) ([]Branch, error) {
	raw, ok := in.Data[n.config.Field]
	if !ok {
		return []Branch{{Relation: RelationFailure, Msg: in}}, nil
	}

	items, ok := raw.([]any)
	if !ok {
		return []Branch{{Relation: RelationFailure, Msg: in}}, nil
	}

	branches := make([]Branch, 0, len(items))
	for index, item := range items {
		out := in
		element, ok := item.(map[string]any)
		if !ok {
			out.Data = map[string]any{"item": item}
		} else {
			out.Data = element
		}
		out.Metadata = copyMetadata(in.Metadata)
		out.Metadata["delimiter_index"] = index
		branches = append(branches, Branch{Relation: RelationSuccess, Msg: out})
	}
	return branches, nil
}

// copyMetadata duplicates metadata so Delimiter branches do not share a map
// across messages when later nodes mutate metadata.
func copyMetadata(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
