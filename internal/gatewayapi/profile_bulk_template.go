// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profile_bulk_template.go streams the fillable device-profile import
// template. It is generated from the registry constant so the template
// header can never drift from the profile CSV parser.

package gatewayapi

import (
	"net/http"

	"github.com/nereidai/nereidlink/internal/registry"
)

// handleProfileBulkImportTemplate streams the profile import template.
func (h *Hub) handleProfileBulkImportTemplate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="nereidlink-profile-import-template.csv"`)
	_, _ = w.Write([]byte(registry.BulkImportProfileTemplate))
}
