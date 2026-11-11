// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// route.go normalizes HTTP paths into bounded-cardinality route labels
// for the REST instrumentation.

package prometrics

import (
	"regexp"
	"strings"
)

// uuidSegment matches a full UUID token.
var uuidSegment = regexp.MustCompile(
	`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// longNumber matches a purely numeric token of 6+ digits, which is an
// identifier rather than a small enum.
var longNumber = regexp.MustCompile(`^[0-9]{6,}$`)

// normalizeRoute rewrites UUID and long-numeric path segments to :id so
// metric labels stay bounded. Static API routes keep their literal form.
func normalizeRoute(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		if uuidSegment.MatchString(segment) || longNumber.MatchString(segment) {
			segments[i] = ":id"
		}
	}
	route := strings.Join(segments, "/")
	if route == "" {
		return "/"
	}
	return route
}
