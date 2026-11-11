// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// api_docs_test.go verifies the self-contained OpenAPI documentation
// endpoint: the UI shell, the embedded YAML spec and the vendored static
// assets are served, while unknown files and non-GET methods are rejected.

package gatewayapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apidocs "github.com/nereidai/nereidlink/docs/api"
)

// docsHandler builds the production mux for a bare Hub. /api/docs is outside
// the /v1/ prefix, so the authorization layer exempts it and no resolver
// wiring is required.
func docsHandler() http.Handler {
	return (&Hub{}).Handler("/ws")
}

// TestAPIDocsShell verifies /api/docs and /api/docs/ both serve the Swagger
// UI shell.
func TestAPIDocsShell(t *testing.T) {
	handler := docsHandler()
	for _, path := range []string{"/api/docs", "/api/docs/"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, recorder.Code)
		}
		body := recorder.Body.String()
		if !strings.Contains(body, "swagger-ui-bundle.js") ||
			!strings.Contains(body, "/api/docs/openapi.yaml") {
			t.Fatalf("%s missing expected UI references", path)
		}
	}
}

// TestAPIDocsSpec verifies the embedded OpenAPI document is served as YAML
// and matches the embedded source bytes.
func TestAPIDocsSpec(t *testing.T) {
	recorder := httptest.NewRecorder()
	docsHandler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/api/docs/openapi.yaml", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("spec status = %d, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Header().Get("Content-Type"), "yaml") {
		t.Fatalf("spec content-type = %q, want yaml",
			recorder.Header().Get("Content-Type"))
	}
	want, err := apidocs.Spec()
	if err != nil {
		t.Fatalf("read embedded spec: %v", err)
	}
	if recorder.Body.String() != string(want) {
		t.Fatal("served spec does not match embedded bytes")
	}
	if !strings.Contains(recorder.Body.String(), "openapi:") {
		t.Fatal("served spec does not look like an OpenAPI document")
	}
}

// TestAPIDocsStaticAssets verifies a vendored UI asset is served and an
// unknown file yields 404 instead of falling through to the SPA.
func TestAPIDocsStaticAssets(t *testing.T) {
	handler := docsHandler()

	asset := httptest.NewRecorder()
	handler.ServeHTTP(asset,
		httptest.NewRequest(http.MethodGet, "/api/docs/swagger-ui.css", nil))
	if asset.Code != http.StatusOK {
		t.Fatalf("asset status = %d, want 200", asset.Code)
	}
	if !strings.Contains(asset.Header().Get("Content-Type"), "css") {
		t.Fatalf("asset content-type = %q, want css",
			asset.Header().Get("Content-Type"))
	}

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing,
		httptest.NewRequest(http.MethodGet, "/api/docs/does-not-exist.js", nil))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing asset status = %d, want 404", missing.Code)
	}
}

// TestAPIDocsMethodGuarding verifies the spec is not mutable over POST.
func TestAPIDocsMethodGuarding(t *testing.T) {
	recorder := httptest.NewRecorder()
	docsHandler().ServeHTTP(recorder,
		httptest.NewRequest(http.MethodPost, "/api/docs/openapi.yaml", nil))
	if recorder.Code == http.StatusOK {
		t.Fatalf("POST unexpectedly returned 200")
	}
}
