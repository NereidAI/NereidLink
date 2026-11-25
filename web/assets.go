// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package webassets provides the built web client through Go embed, so the
// all-in-one single binary can serve the complete UI without external files.
// Kubernetes deployments may serve the same assets from the separate
// nginx-based frontend image instead (see deploy/images/frontend); in that
// split topology the backend image intentionally embeds only the tracked
// .gitkeep placeholder, and callers must handle DistFS's incomplete-embed
// error non-fatally rather than refusing to start the API.
//
// Hardening model: the //go:embed directive must stay
// "all:dist", because a plain "dist" pattern fails to compile when the
// directory contains only the dotfile placeholder and the CI backend job runs
// without a frontend build. Production safety is therefore enforced one layer
// down: ProtectedFS hides every dotfile/underscore entry and *.map source map
// from the served filesystem, and DistFS rejects an embed that lacks
// index.html so the standalone binary fails fast instead of rendering an
// http.FileServer directory listing.
package webassets

import (
	"embed"
	"fmt"
	"io/fs"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// DistFS returns the embedded SPA rooted at "dist", wrapped by the production
// protection layer. It returns an error wrapping fs.ErrNotExist when the
// embed is incomplete (no dist/index.html), which is a fatal build defect for
// the standalone binary and an expected, non-fatal condition for the split
// backend container whose console ships in the frontend image.
func DistFS() (fs.FS, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, fmt.Errorf("locate embedded web/dist: %w", err)
	}
	return ProtectedFS(sub)
}

// ProtectedFS wraps fsys for production static serving. It requires
// index.html at the root, so an unbuilt SPA fails fast instead of exposing
// an autoindex page, and hides dotfiles, underscore-prefixed entries and
// *.map source maps through both Open and ReadDir so they can never be
// listed or downloaded.
func ProtectedFS(fsys fs.FS) (fs.FS, error) {
	protected := &protectedFS{fsys: fsys}
	if _, err := fs.Stat(protected, "index.html"); err != nil {
		return nil, fmt.Errorf("embedded web assets are incomplete: dist/index.html is missing: %w", err)
	}
	return protected, nil
}

// protectedFS is the hardened view of the embedded SPA tree. http.FileServer
// accesses every entry through Open and reads directories through the
// returned file's ReadDir, so filtering those two paths covers all serves.
type protectedFS struct {
	fsys fs.FS
}

// Open implements fs.FS. Forbidden entries behave as if they did not exist,
// and directories are returned through a reader that filters their entries.
func (p *protectedFS) Open(name string) (fs.File, error) {
	if name != "." && IsForbiddenAsset(name) {
		return nil, fs.ErrNotExist
	}
	file, err := p.fsys.Open(name)
	if err != nil {
		return nil, err
	}
	dir, ok := file.(fs.ReadDirFile)
	if !ok {
		return file, nil
	}
	return &protectedDir{File: file, dir: dir}, nil
}

// protectedDir wraps an embedded directory so ReadDir never yields hidden or
// source-map entries; this keeps http.FileServer autoindex pages free of
// them even if a directory request reaches the file server.
type protectedDir struct {
	fs.File
	dir fs.ReadDirFile
}

// ReadDir implements fs.ReadDirFile and filters the underlying directory
// contents. net/http always requests the full entry set (n <= 0), so the
// filtered result keeps the directory-listing contract intact.
func (d *protectedDir) ReadDir(n int) ([]fs.DirEntry, error) {
	entries, err := d.dir.ReadDir(n)
	filtered := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !IsForbiddenAsset(entry.Name()) {
			filtered = append(filtered, entry)
		}
	}
	return filtered, err
}

// IsForbiddenAsset reports whether name must never be reachable through the
// production static handler: VCS/build placeholders and other dotfiles,
// underscore-prefixed tooling entries, and JavaScript/CSS source maps. The
// check applies to every slash-separated path segment.
func IsForbiddenAsset(name string) bool {
	for _, segment := range strings.Split(name, "/") {
		// A lone "." is the filesystem root, not a dotfile entry.
		if segment == "." {
			continue
		}
		if strings.HasPrefix(segment, ".") || strings.HasPrefix(segment, "_") {
			return true
		}
	}
	return strings.HasSuffix(name, ".map")
}
