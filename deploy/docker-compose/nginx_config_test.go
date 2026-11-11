// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package dockercompose_test

import (
	"os"
	"strings"
	"testing"
)

// TestWSProxyHostPreservesPublicPort keeps a non-default edge port in the
// Host header forwarded to the API. coder/websocket's default same-origin
// check compares that host with Origin, so $host (which drops the port) makes
// a same-origin upgrade through :18082 or :10443 fail with HTTP 403.
func TestWSProxyHostPreservesPublicPort(t *testing.T) {
	const apiLocation = `location ~ ^/(v1|ws|api|metrics|healthz|auth)(/|$) {`

	for _, path := range []string{"nginx/edge-local.conf", "nginx/nereidlink.conf"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			text := string(raw)
			start := strings.Index(text, apiLocation)
			if start < 0 {
				t.Fatalf("API/WS location not found in %s", path)
			}
			block := text[start:]
			if end := strings.Index(block, "\n    location / {"); end >= 0 {
				block = block[:end]
			}

			var forwardedHost string
			for _, line := range strings.Split(block, "\n") {
				fields := strings.Fields(line)
				if len(fields) == 3 && fields[0] == "proxy_set_header" && fields[1] == "Host" {
					forwardedHost = strings.TrimSuffix(fields[2], ";")
				}
			}
			if forwardedHost != "$http_host" {
				t.Fatalf("API/WS proxy Host = %q, want $http_host to preserve the public port", forwardedHost)
			}
		})
	}
}
