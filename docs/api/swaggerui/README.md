<!--
  Copyright (C) 2026 YIQISOFT
  SPDX-License-Identifier: Apache-2.0
-->

# Vendored Swagger UI assets

The files in this directory (`swagger-ui.css`, `swagger-ui-bundle.js`,
`favicon.png`) are redistributed verbatim from the **swagger-ui-dist** npm
package, version **5.20.1**:

- Project: https://github.com/swagger-api/swagger-ui
- License: Apache License 2.0 (see `LICENSE` in this directory)
- Upstream package: https://www.npmjs.com/package/swagger-ui-dist/v/5.20.1

They are embedded into the NereidLink binary (see `../apidocs.go`) and served
under `/api/docs/` so the interactive API documentation works in air-gapped
on-premises deployments without any external CDN.

To upgrade, re-download the three files at the new pinned version and update
the version references in this README. Do not hand-edit the upstream files.
