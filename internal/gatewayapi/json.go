// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import "encoding/json"

// mustJSON serializes a frame for the write path. Frames are constructed by
// this package from trusted internal types, so an error indicates a
// programming bug and panicking is appropriate.
func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
