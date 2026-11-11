// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package apidocs serves the OpenAPI contract and a self-contained Swagger
// UI. Both the spec (openapi.yaml) and the vendored swagger-ui-dist assets
// are embedded into the binary, so the documentation endpoint works in
// air-gapped on-premises deployments without any external CDN.
//
// openapi.yaml remains the single source of truth for the REST contract;
// this package only embeds that file and never mutates it.
package apidocs

import (
	"embed"
	"io/fs"
)

// SpecName is the embedded OpenAPI document filename.
const SpecName = "openapi.yaml"

//go:embed openapi.yaml
var specFS embed.FS

//go:embed swaggerui
var swaggerUIFS embed.FS

// Spec returns the raw OpenAPI 3.1 document bytes. An error is only
// possible if the embedded tree layout changes, which is a build-time defect.
func Spec() ([]byte, error) {
	return specFS.ReadFile(SpecName)
}

// AssetFS returns the Swagger UI static tree rooted at the swaggerui
// directory so that file-server paths match the request URL suffix.
func AssetFS() (fs.FS, error) {
	return fs.Sub(swaggerUIFS, "swaggerui")
}
