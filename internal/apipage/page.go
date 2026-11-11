// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Package apipage defines the standard server-side pagination envelope
// shared by every list endpoint. Keeping the shape in one place lets the
// frontend useServerTable hook and OpenAPI document rely on a single
// contract.
package apipage

// Page is the JSON envelope for a paged collection.
type Page[T any] struct {
	// Items is the page content.
	Items []T `json:"items"`

	// Total is the unpaged row count.
	Total int `json:"total"`

	// Page is the current 1-based page number.
	Page int `json:"page"`

	// PageSize is the applied row count per page.
	PageSize int `json:"page_size"`

	// TotalPages is ceil(Total/PageSize).
	TotalPages int `json:"total_pages"`
}

// New builds an envelope, normalizing page numbers and deriving the total
// page count. A non-positive pageSize is treated as 1 so TotalPages stays
// finite; callers are expected to pass validated positive values.
func New[T any](items []T, total, page, pageSize int) Page[T] {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 1
	}
	totalPages := (total + pageSize - 1) / pageSize
	return Page[T]{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages,
	}
}
