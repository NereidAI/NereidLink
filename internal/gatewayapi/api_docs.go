// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// api_docs.go serves the embedded OpenAPI contract and an offline-capable
// Swagger UI under /api/docs. All assets and the spec ship inside the binary
// (see the docs/api package), so the endpoint works in air-gapped clusters
// with no external CDN. These paths are outside the /v1/ API prefix and are
// therefore exempt from bearer-token authentication (see isExemptPath), the
// same trust model as the embedded single-page application on a private
// on-premises deployment.

package gatewayapi

import (
	"net/http"
	"strings"

	apidocs "github.com/nereidai/nereidlink/docs/api"
)

// docsRoot is the documentation UI path prefix. A trailing slash makes the
// Go mux treat it as a subtree for the vendored static assets.
const docsRoot = "/api/docs/"

// swaggerUIHTML is the single-page Swagger UI shell. Asset and spec URLs are
// absolute so they resolve whether the UI is opened at /api/docs or
// /api/docs/. persistAuthorization keeps the entered bearer token across
// reloads for convenient interactive exploration.
const swaggerUIHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <link rel="icon" href="/api/docs/favicon.png" type="image/png">
  <link rel="stylesheet" href="/api/docs/swagger-ui.css">
  <title>NereidLink API Docs</title>
  <style>
    body { margin: 0; }
  </style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="/api/docs/swagger-ui-bundle.js" charset="utf-8"></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: "/api/docs/openapi.yaml",
        dom_id: "#swagger-ui",
        deepLinking: true,
        persistAuthorization: true,
        presets: [SwaggerUIBundle.presets.apis],
        layout: "BaseLayout"
      });
    };
  </script>
</body>
</html>`

// handleAPIDocs serves the Swagger UI shell at /api/docs and /api/docs/.
func (h *Hub) handleAPIDocs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The shell is a fixed, trusted constant, so no injection concerns apply.
	_, _ = w.Write([]byte(swaggerUIHTML))
}

// handleAPIDocsAsset serves the OpenAPI document and vendored Swagger UI
// static assets beneath /api/docs/. Unknown files return 404 rather than
// falling through to the SPA.
func (h *Hub) handleAPIDocsAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	relative := strings.TrimPrefix(r.URL.Path, docsRoot)
	if relative == "" {
		h.handleAPIDocs(w, r)
		return
	}
	if relative == "openapi.yaml" || relative == "openapi.yml" {
		spec, err := apidocs.Spec()
		if err != nil {
			// Embedded spec missing is a build/packaging defect.
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, _ = w.Write(spec)
		return
	}

	assets, err := apidocs.AssetFS()
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// StripPrefix + the rooted file server resolve only the flat vendored
	// files; net/http cleans "/../" segments, blocking path traversal.
	http.StripPrefix(docsRoot, http.FileServerFS(assets)).ServeHTTP(w, r)
}
