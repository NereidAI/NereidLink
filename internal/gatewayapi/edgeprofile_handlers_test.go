// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package gatewayapi

import (
	"net/http"
	"testing"

	"github.com/nereidai/nereidlink/internal/rbac"
)

// TestEdgeProfileRoutePoints verifies cached edge profile listing is a
// profile read and adoption is a profile write, since adoption creates or
// updates a native device profile rather than a connector spec.
func TestEdgeProfileRoutePoints(t *testing.T) {
	gw := "00000000-0000-0000-0000-000000000001"
	cases := []struct {
		method string
		rest   []string
		want   string
	}{
		{http.MethodGet, []string{"gateways", gw, "edge-profiles"}, rbac.PointProfileRead},
		{http.MethodPost, []string{"gateways", gw, "edge-profiles", "P", "adopt"}, rbac.PointProfileWrite},
		{http.MethodGet, []string{"gateways", gw, "connectors"}, rbac.PointConnectorRead},
	}
	for _, tc := range cases {
		if got := requiredPoint(tc.method, tc.rest); got != tc.want {
			t.Fatalf("%s %v: got %s want %s", tc.method, tc.rest, got, tc.want)
		}
	}
}
