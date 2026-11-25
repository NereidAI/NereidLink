// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// The webassets tests pin the production embed contract: an incomplete SPA
// tree must be rejected, and dotfiles/underscore entries/source maps must
// never be reachable through the protected filesystem.
package webassets

import (
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"
)

// healthyMapFS models a complete Vite output tree plus the build-only files
// the protection layer is supposed to hide.
func healthyMapFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":        {Data: []byte("<!doctype html>console")},
		"favicon.svg":       {Data: []byte("<svg/>")},
		".gitkeep":          {},
		"assets/app.js":     {Data: []byte("console.log(1)")},
		"assets/app.js.map": {Data: []byte("original sources")},
		"assets/_draft.js":  {Data: []byte("tooling only")},
	}
}

// TestProtectedFSRequiresIndexHTML verifies that every layout missing
// index.html is rejected, including the dotfile-only embed the split backend
// image ships and any fresh checkout before the first frontend build.
func TestProtectedFSRequiresIndexHTML(t *testing.T) {
	t.Parallel()
	cases := map[string]fstest.MapFS{
		"empty filesystem": {},
		"dotfile placeholder only": {
			".gitkeep": {},
		},
		"assets without document": {
			"assets/app.js": {Data: []byte("console.log(1)")},
		},
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := ProtectedFS(source); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("ProtectedFS() error = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

// TestProtectedFSServesRealEntries verifies the legitimate SPA files remain
// readable once the protected filesystem is accepted.
func TestProtectedFSServesRealEntries(t *testing.T) {
	t.Parallel()
	protected, err := ProtectedFS(healthyMapFS())
	if err != nil {
		t.Fatalf("ProtectedFS() unexpected error: %v", err)
	}
	for _, name := range []string{"index.html", "favicon.svg", "assets/app.js"} {
		if _, err := fs.ReadFile(protected, name); err != nil {
			t.Fatalf("ReadFile(%q) error = %v", name, err)
		}
	}
}

// TestProtectedFSHidesForbiddenEntries verifies that build-only dotfiles,
// underscore entries and source maps can neither be opened nor listed.
func TestProtectedFSHidesForbiddenEntries(t *testing.T) {
	t.Parallel()
	protected, err := ProtectedFS(healthyMapFS())
	if err != nil {
		t.Fatalf("ProtectedFS() unexpected error: %v", err)
	}

	for _, name := range []string{".gitkeep", "assets/app.js.map", "assets/_draft.js"} {
		if _, err := protected.Open(name); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("Open(%q) error = %v, want fs.ErrNotExist", name, err)
		}
	}

	rootEntries, err := fs.ReadDir(protected, ".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, entry := range rootEntries {
		if IsForbiddenAsset(entry.Name()) {
			t.Fatalf("root listing leaks forbidden entry %q", entry.Name())
		}
	}

	assetEntries, err := fs.ReadDir(protected, "assets")
	if err != nil {
		t.Fatalf("ReadDir(assets): %v", err)
	}
	if len(assetEntries) != 1 || assetEntries[0].Name() != "app.js" {
		t.Fatalf("assets listing = %v, want only app.js", assetEntries)
	}
}

// TestIsForbiddenAssetClassifiesPaths documents the exact deny rules shared
// by the filesystem wrapper and the HTTP static handler.
func TestIsForbiddenAssetClassifiesPaths(t *testing.T) {
	t.Parallel()
	forbidden := []string{".gitkeep", ".env", "assets/app.js.map", "assets/_draft.js", "dir/.hidden/x"}
	for _, name := range forbidden {
		if !IsForbiddenAsset(name) {
			t.Errorf("IsForbiddenAsset(%q) = false, want true", name)
		}
	}
	allowed := []string{".", "index.html", "favicon.svg", "assets/app.js", "assets/index-CUu.css"}
	for _, name := range allowed {
		if IsForbiddenAsset(name) {
			t.Errorf("IsForbiddenAsset(%q) = true, want false", name)
		}
	}
}
