// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package coap

import (
	"strconv"
	"strings"

	"github.com/plgd-dev/go-coap/v3/mux"
)

// itoa is a short alias for strconv.Itoa in numeric JSON responses.
func itoa(value int) string {
	return strconv.Itoa(value)
}

// strReader wraps strings.NewReader under a short name for response bodies.
func strReader(s string) *strings.Reader {
	return strings.NewReader(s)
}

// tokenFromRequest extracts the device access token. Devices supply it as
// the `token` URI-Query option, which keeps CoAP requests free of an extra
// custom option and works over both plain UDP and DTLS.
func tokenFromRequest(r *mux.Message) string {
	queries, err := r.Queries()
	if err != nil {
		return ""
	}
	for _, query := range queries {
		if value, ok := strings.CutPrefix(query, "token="); ok {
			return value
		}
	}
	return ""
}

// commandID returns the path variable captured on /cmdres/{id}.
func commandID(r *mux.Message) string {
	if r.RouteParams == nil || r.RouteParams.Vars == nil {
		return ""
	}
	return r.RouteParams.Vars["id"]
}
