// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// helpers.go provides small shared helpers for the rule package.

package rule

import "google.golang.org/protobuf/proto"

// protoUnmarshal decodes a protobuf message.
func protoUnmarshal(raw []byte, target proto.Message) error {
	return proto.Unmarshal(raw, target)
}
