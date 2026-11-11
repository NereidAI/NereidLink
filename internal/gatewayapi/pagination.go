// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// pagination.go parses the shared page/page_size query parameters.

package gatewayapi

import (
	"net/http"
	"strconv"
)

// Pagination defaults and limits, matching the ADR-0030 table settings.
const (
	// defaultPage is used when page is absent or invalid.
	defaultPage = 1

	// defaultPageSize is used when page_size is absent or invalid.
	defaultPageSize = 10

	// maxPageSize prevents unbounded result sets.
	maxPageSize = 100
)

// parsePagination extracts normalized 1-based page and page size values.
func parsePagination(r *http.Request) (int, int) {
	page, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil || page < 1 {
		page = defaultPage
	}

	pageSize, err := strconv.Atoi(r.URL.Query().Get("page_size"))
	if err != nil || pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}
