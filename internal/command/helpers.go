// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package command

import (
	"encoding/json"
	"errors"

	"github.com/google/uuid"
)

// ErrInvalidArgument indicates a malformed command or batch request.
var ErrInvalidArgument = errors.New("invalid argument")

// mustJSON serializes a Core NATS progress payload.
func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// newInstanceID returns a unique identifier for a router process, used to tag
// claimed commands for observability.
func newInstanceID() string {
	return "router-" + uuid.NewString()
}
