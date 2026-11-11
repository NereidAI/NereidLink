// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// web_assets.go mounts the embedded production frontend into the gateway-api
// HTTP mux. It implements SPA history-mode fallback while preserving real
// 404 responses for missing hashed asset files.

package gatewayapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	webassets "github.com/nereidai/nereidlink/web"
)

// assetsDir is the embedded Vite output subdirectory served by this handler.
const assetsDir = "dist"

// serveWeb serves the embedded single-page application.
//
// Resolution rules:
//
//   - Existing files (index.html, hashed /assets/* entries) are served by the
//     standard file server with correct content types and cache behavior.
//   - Unknown paths that look like client-side routes (no file extension)
//     fall back to index.html so the React router can render them.
//   - Unknown paths that look like static assets (an extension is present)
//     return 404 instead of silently returning HTML under an asset URL.
func (h *Hub) serveWeb(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	// Clean the request path before touching the embedded filesystem to
	// prevent path traversal via segments such as "/../".
	clean := path.Clean(r.URL.Path)
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}

	assets, err := fs.Sub(webassets.Dist, assetsDir)
	if err != nil {
		// This can only fail when the embedded tree layout changes; it is a
		// build-time defect rather than a client error.
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	relative := strings.TrimPrefix(clean, "/")
	if relative == "" {
		relative = "index.html"
	}

	if _, statErr := fs.Stat(assets, relative); statErr != nil {
		if !looksLikeAsset(relative) {
			// Client-side route: let the file server serve the directory,
			// which returns index.html. Requesting "/index.html" directly
			// would make net/http issue a 301 redirect to "./".
			r.URL.Path = "/"
		} else {
			// Missing static asset: do not masquerade as HTML.
			http.NotFound(w, r)
			return
		}
	}

	http.FileServerFS(assets).ServeHTTP(w, r)
}

// looksLikeAsset reports whether a request path refers to a static file
// rather than a history-mode route. Vite emits fingerprinted files with an
// extension under /assets/, while client routes never carry one.
func looksLikeAsset(relativePath string) bool {
	return path.Ext(relativePath) != ""
}
