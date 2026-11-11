// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package webassets provides the built web client through Go embed, so the
// all-in-one single binary can serve the complete UI without external files.
// Kubernetes deployments may serve the same assets from a sidecar instead.

package webassets

import "embed"

// Dist holds the production web build produced by `npm run build`.
// The directory must exist before `go build`; `make build` and the image
// build guarantee it, and dist/.gitkeep keeps the path valid in a fresh
// checkout.
//
//go:embed all:dist
var Dist embed.FS
