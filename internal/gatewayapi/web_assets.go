// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// web_assets.go mounts the embedded production frontend into the gateway-api
// HTTP mux. It implements SPA history-mode fallback while preserving real
// 404 responses for missing hashed asset files. Directory URLs and build-only
// files (dotfiles, source maps) are rejected so the server can never emit an
// http.FileServer autoindex page.

package gatewayapi

import (
	"io/fs"
	"net/http"
	"path"
	"strings"

	webassets "github.com/nereidai/nereidlink/web"
)

// ConfigureWebAssets installs the validated embedded SPA filesystem. main
// calls it once at startup after webassets.DistFS succeeds; hubs in the
// split backend container (embed holds only .gitkeep) stay unconfigured and
// serveWeb answers 404, because the nginx frontend image owns the console.
func (h *Hub) ConfigureWebAssets(assets fs.FS) {
	h.webHandler = newWebHandler(assets)
}

// newWebHandler builds the SPA handler around an injectable asset filesystem
// so the routing rules can be tested against synthetic embedded layouts.
//
// Resolution rules:
//
//   - Existing files (index.html, hashed /assets/* entries) are served by the
//     standard file server with correct content types and cache behavior.
//   - Unknown paths that look like client-side routes (no file extension)
//     fall back to index.html so the React router can render them.
//   - Unknown paths that look like static assets (an extension is present)
//     return 404 instead of silently returning HTML under an asset URL.
//   - Directory URLs, dotfiles/underscore entries and *.map files always
//     return 404; no directory listing is ever produced.
func newWebHandler(assets fs.FS) http.Handler {
	fileServer := http.FileServerFS(assets)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		relative := strings.TrimPrefix(clean, "/")
		if relative == "" {
			relative = "index.html"
		}

		// Build-only files (.gitkeep, source maps, underscore tooling) must
		// never reach the SPA fallback: answer like a deleted resource.
		if webassets.IsForbiddenAsset(relative) {
			http.NotFound(w, r)
			return
		}

		info, statErr := fs.Stat(assets, relative)
		switch {
		case statErr == nil && info.IsDir():
			// A directory URL (for example /assets/) must never render an
			// autoindex page; the SPA has no browseable directories.
			http.NotFound(w, r)
			return
		case statErr != nil && looksLikeAsset(relative):
			// Missing static asset: do not masquerade as HTML.
			http.NotFound(w, r)
			return
		case statErr != nil:
			// Client-side route: let the file server serve index.html via
			// the root path. Requesting "/index.html" directly would make
			// net/http issue a 301 redirect to "./".
			r.URL.Path = "/"
		}

		fileServer.ServeHTTP(w, r)
	})
}

// serveWeb serves the embedded single-page application when the standalone
// binary carries a complete embed. A hub without configured web assets (the
// split backend container) returns 404: in that topology browser traffic is
// routed to the separate frontend image, so reaching this handler means the
// request was misrouted and must never produce a directory listing.
func (h *Hub) serveWeb(w http.ResponseWriter, r *http.Request) {
	if h.webHandler == nil {
		http.NotFound(w, r)
		return
	}
	h.webHandler.ServeHTTP(w, r)
}

// looksLikeAsset reports whether a request path refers to a static file
// rather than a history-mode route. Vite emits fingerprinted files with an
// extension under /assets/, while client routes never carry one.
func looksLikeAsset(relativePath string) bool {
	return path.Ext(relativePath) != ""
}
