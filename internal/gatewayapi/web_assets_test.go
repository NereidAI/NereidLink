// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the embedded SPA handler: an unbuilt or partial embed
// must never surface as a directory listing,
// dotfiles and source maps must be unreachable, history-mode deep links must
// keep falling back to index.html, and a hub without configured web assets
// (split backend container) must answer 404 instead of serving a placeholder.
package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// webTestFS mirrors a complete Vite output tree plus the build-only entries
// the handler must hide.
func webTestFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html>console-index")},
		"favicon.svg":       {Data: []byte("<svg/>")},
		".gitkeep":          {},
		"assets/app.js":     {Data: []byte("console.log(1)")},
		"assets/app.css":    {Data: []byte("body{}")},
		"assets/app.js.map": {Data: []byte("original sources")},
		"assets/_draft.js":  {Data: []byte("tooling only")},
	}
}

// webRequest runs one request through a hub configured with the hardened
// handler and returns the recorded response.
func webRequest(t *testing.T, hub *Hub, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	hub.serveWeb(rec, httptest.NewRequest(method, target, nil))
	return rec
}

// configuredWebHub builds a hub whose embedded asset tree is the synthetic
// complete Vite output.
func configuredWebHub() *Hub {
	hub := NewHub(nil, Tuning{})
	hub.ConfigureWebAssets(webTestFS())
	return hub
}

// TestServeWebWithoutConfigReturns404 verifies the split backend container
// (embed holds only .gitkeep, no ConfigureWebAssets call) never emits a
// directory listing or the placeholder itself.
func TestServeWebWithoutConfigReturns404(t *testing.T) {
	t.Parallel()
	hub := NewHub(nil, Tuning{})
	for _, target := range []string{"/", "/.gitkeep", "/dashboard", "/assets/"} {
		rec := webRequest(t, hub, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("unconfigured hub GET %s status = %d, want 404", target, rec.Code)
		}
	}
}

// TestServeWebServesIndex verifies the document root renders index.html.
func TestServeWebServesIndex(t *testing.T) {
	t.Parallel()
	rec := webRequest(t, configuredWebHub(), http.MethodGet, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); body != "<!doctype html>console-index" {
		t.Fatalf("GET / body = %q, want index.html", body)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("GET / content type = %q, want text/html", contentType)
	}
}

// TestServeWebHistoryFallback verifies extension-less unknown paths return
// index.html so the client-side router can render them.
func TestServeWebHistoryFallback(t *testing.T) {
	t.Parallel()
	hub := configuredWebHub()
	for _, target := range []string{"/dashboard", "/tenants/t1/devices/d1", "/login"} {
		rec := webRequest(t, hub, http.MethodGet, target)
		if rec.Code != http.StatusOK || rec.Body.String() != "<!doctype html>console-index" {
			t.Fatalf("GET %s -> code %d body %q, want 200 index.html", target, rec.Code, rec.Body.String())
		}
	}
}

// TestServeWebRejectsDirectories verifies directory URLs return 404 instead
// of an http.FileServer autoindex page, with or without a trailing slash.
func TestServeWebRejectsDirectories(t *testing.T) {
	t.Parallel()
	hub := configuredWebHub()
	for _, target := range []string{"/assets", "/assets/"} {
		rec := webRequest(t, hub, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "<a href") {
			t.Fatalf("GET %s returned a directory listing: %q", target, rec.Body.String())
		}
	}
}

// TestServeWebRejectsBuildOnlyFiles verifies dotfiles, underscore entries
// and source maps are unreachable even though they exist in the raw embed.
func TestServeWebRejectsBuildOnlyFiles(t *testing.T) {
	t.Parallel()
	hub := configuredWebHub()
	for _, target := range []string{"/.gitkeep", "/assets/app.js.map", "/assets/_draft.js"} {
		rec := webRequest(t, hub, http.MethodGet, target)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want 404", target, rec.Code)
		}
	}
}

// TestServeWebMissingAssetReturns404 verifies unknown fingerprinted files
// never masquerade as the HTML document.
func TestServeWebMissingAssetReturns404(t *testing.T) {
	t.Parallel()
	rec := webRequest(t, configuredWebHub(), http.MethodGet, "/assets/missing-CUu.js")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", rec.Code)
	}
}

// TestServeWebServesHashedAsset verifies real fingerprinted assets keep
// working with their content type and exact bytes.
func TestServeWebServesHashedAsset(t *testing.T) {
	t.Parallel()
	rec := webRequest(t, configuredWebHub(), http.MethodGet, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("hashed asset status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "console.log(1)" {
		t.Fatalf("hashed asset body = %q", rec.Body.String())
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.Contains(contentType, "javascript") {
		t.Fatalf("hashed asset content type = %q, want javascript", contentType)
	}
}

// TestServeWebRejectsNonReadMethods verifies the handler stays read-only.
func TestServeWebRejectsNonReadMethods(t *testing.T) {
	t.Parallel()
	hub := configuredWebHub()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := webRequest(t, hub, method, "/")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s / status = %d, want 405", method, rec.Code)
		}
	}
}
