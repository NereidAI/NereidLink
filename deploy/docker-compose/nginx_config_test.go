// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package dockercompose_test

import (
	"os"
	"strings"
	"testing"
)

// edgeConfigs are the two edge configurations that reproduce the same
// backend/frontend path split as the Kubernetes Ingress.
var edgeConfigs = []string{"nginx/edge-local.conf", "nginx/nereidlink.conf"}

// backendLocationMarkers are every proxy block that forwards to the Go
// backend. The SSO callback is an exact match: a broad /auth regex would
// also capture the SPA route /auth/callback, which the backend redirects
// to after NS ticket verification.
var backendLocationMarkers = []string{
	`location ~ ^/(v1|ws|api|metrics|healthz)(/|$) {`,
	`location = /auth/ns-callback {`,
}

// blockFor returns the text of the location block starting at marker,
// stopping at its closing brace (four-space indent in both configs).
func blockFor(t *testing.T, text, marker string) string {
	t.Helper()
	start := strings.Index(text, marker)
	if start < 0 {
		t.Fatalf("backend location %q not found", marker)
	}
	block := text[start:]
	if end := strings.Index(block, "\n    }"); end >= 0 {
		block = block[:end]
	}
	return block
}

// forwardedHost extracts the Host header value configured in a location
// block, minus the trailing semicolon.
func forwardedHost(block string) string {
	for _, line := range strings.Split(block, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == "proxy_set_header" && fields[1] == "Host" {
			return strings.TrimSuffix(fields[2], ";")
		}
	}
	return ""
}

// TestBackendLocationsPreservePublicPort keeps a non-default edge port in
// the Host header forwarded to the API. coder/websocket's default
// same-origin check compares that host with Origin, so $host (which drops
// the port) makes a same-origin upgrade through :18082 or :10443 fail with
// HTTP 403. Every backend block, including the exact SSO callback, must
// forward $http_host.
func TestBackendLocationsPreservePublicPort(t *testing.T) {
	for _, path := range edgeConfigs {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			for _, marker := range backendLocationMarkers {
				if host := forwardedHost(blockFor(t, text, marker)); host != "$http_host" {
					t.Fatalf("%s block %q forwards Host = %q, want $http_host to preserve the public port",
						path, marker, host)
				}
			}
		})
	}
}

// TestSSOCallbackIsExactRoute pins the split topology: only the NS SSO
// callback may reach the backend, whose split image embeds just the
// dist/.gitkeep placeholder. Every other /auth/* URL, including the SPA's
// /auth/callback, must fall through to the frontend location.
func TestSSOCallbackIsExactRoute(t *testing.T) {
	for _, path := range edgeConfigs {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)

			if strings.Contains(text, "healthz|auth") {
				t.Fatalf("%s still proxies the broad /auth prefix to the backend; "+
					"use an exact location for /auth/ns-callback so /auth/callback stays on the frontend", path)
			}
			callbackBlock := blockFor(t, text, `location = /auth/ns-callback {`)
			if !strings.Contains(callbackBlock, "proxy_pass http://nereidlink:8080;") {
				t.Fatalf("%s SSO callback block does not proxy to the backend", path)
			}
			if !strings.Contains(text, "location / {") {
				t.Fatalf("%s is missing the frontend catch-all location", path)
			}
		})
	}
}
