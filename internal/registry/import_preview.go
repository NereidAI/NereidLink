// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// import_preview.go implements the validate-and-preview step that runs
// before a bulk import writes anything. It parses the upload, validates
// every row against the same rules creation uses and returns each row's
// parsed values with a valid flag, so the console can render a preview
// table and let the operator confirm before persisting.

package registry

import (
	"context"
	"io"
	"strconv"
)

// ImportPreviewRow is one parsed upload row with its validation result.
type ImportPreviewRow struct {
	// Line is the 1-based physical line number, counting the header.
	Line int `json:"line"`
	// Valid reports whether the row would be accepted on import.
	Valid bool `json:"valid"`
	// Reason explains an invalid row in operator terms.
	Reason string `json:"reason,omitempty"`
	// Values carries the parsed cells keyed by column name for preview.
	Values map[string]string `json:"values"`
}

// ImportPreview is the dry-run result for one upload.
type ImportPreview struct {
	// Total is the number of data rows, excluding the header.
	Total int `json:"total"`
	// Valid counts rows that would be imported.
	Valid int `json:"valid"`
	// Invalid counts rows that would be rejected.
	Invalid int `json:"invalid"`
	// Columns is the accepted header order, used to lay out the preview.
	Columns []string `json:"columns"`
	// Rows is every parsed row with its per-row validation; never null.
	Rows []ImportPreviewRow `json:"rows"`
}

// PreviewDeviceImport validates a device CSV upload without writing.
func (a *Admin) PreviewDeviceImport(ctx context.Context, tenantID string,
	reader io.Reader) (ImportPreview, error) {
	records, err := readBulkImportCSV(reader)
	if err != nil {
		return ImportPreview{Rows: []ImportPreviewRow{}}, err
	}

	// seen tracks names already present in this file, so a duplicate that
	// the database unique constraint would otherwise reject is reported
	// on the second row with a precise reason.
	seen := make(map[string]int, len(records))
	preview := ImportPreview{
		Total:   len(records),
		Columns: bulkImportColumns,
		Rows:    make([]ImportPreviewRow, 0, len(records)),
	}
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return preview, err
		}
		reason := ""
		if err := a.prepareDeviceCreate(ctx, tenantID, rec.input); err != nil {
			reason = err.Error()
		} else if firstLine, dup := seen[rec.input.CanonicalName]; dup {
			reason = "duplicate canonical_name in this file; first seen on line " +
				itoaLine(firstLine)
		} else if exists, err := a.deviceNameExists(ctx, tenantID, rec.input.CanonicalName); err != nil {
			reason = err.Error()
		} else if exists {
			reason = "a device with this canonical_name already exists in this tenant"
		}

		if reason == "" {
			seen[rec.input.CanonicalName] = rec.line
			preview.Valid++
		} else {
			preview.Invalid++
		}
		preview.Rows = append(preview.Rows, ImportPreviewRow{
			Line:   rec.line,
			Valid:  reason == "",
			Reason: reason,
			Values: deviceRowValues(rec.input),
		})
	}
	return preview, nil
}

// PreviewProfileImport validates a profile CSV upload without writing.
func (a *Admin) PreviewProfileImport(ctx context.Context, tenantID string,
	reader io.Reader) (ImportPreview, error) {
	records, err := readBulkImportProfilesCSV(reader)
	if err != nil {
		return ImportPreview{Rows: []ImportPreviewRow{}}, err
	}

	seen := make(map[string]int, len(records))
	preview := ImportPreview{
		Total:   len(records),
		Columns: bulkImportProfileColumns,
		Rows:    make([]ImportPreviewRow, 0, len(records)),
	}
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return preview, err
		}
		reason := ""
		if err := validateProfile(rec.input); err != nil {
			reason = err.Error()
		} else if err := ensureChainGroup(ctx, a.pool, tenantID, rec.input.RuleChainGroupID); err != nil {
			reason = err.Error()
		} else if firstLine, dup := seen[rec.input.Name]; dup {
			reason = "duplicate profile name in this file; first seen on line " +
				itoaLine(firstLine)
		} else if exists, err := a.profileNameExists(ctx, tenantID, rec.input.Name); err != nil {
			reason = err.Error()
		} else if exists {
			reason = "a profile with this name already exists in this tenant"
		}

		if reason == "" {
			seen[rec.input.Name] = rec.line
			preview.Valid++
		} else {
			preview.Invalid++
		}
		preview.Rows = append(preview.Rows, ImportPreviewRow{
			Line:   rec.line,
			Valid:  reason == "",
			Reason: reason,
			Values: profileRowValues(rec.input),
		})
	}
	return preview, nil
}

// deviceNameExists reports whether a device already carries canonicalName.
func (a *Admin) deviceNameExists(ctx context.Context, tenantID, canonicalName string) (bool, error) {
	var exists bool
	err := a.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device WHERE tenant_id = $1 AND canonical_name = $2)`,
		tenantID, canonicalName).Scan(&exists)
	return exists, err
}

// profileNameExists reports whether a profile already carries name.
func (a *Admin) profileNameExists(ctx context.Context, tenantID, name string) (bool, error) {
	var exists bool
	err := a.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM device_profile WHERE tenant_id = $1 AND name = $2)`,
		tenantID, name).Scan(&exists)
	return exists, err
}

// deviceRowValues maps a parsed device row back to its CSV cells.
func deviceRowValues(input DeviceDTO) map[string]string {
	state := input.State
	if state == "" {
		state = StateActive
	}
	return map[string]string{
		"canonical_name":      input.CanonicalName,
		"profile_id":          input.ProfileID,
		"customer_id":         input.CustomerID,
		"group_id":            input.GroupID,
		"rule_chain_group_id": input.RuleChainGroupID,
		"state":               state,
	}
}

// profileRowValues maps a parsed profile row back to its CSV cells.
func profileRowValues(input ProfileDTO) map[string]string {
	return map[string]string{
		"name":                input.Name,
		"rule_chain_group_id": input.RuleChainGroupID,
	}
}

// itoaLine renders a line number for an error message.
func itoaLine(line int) string {
	return strconv.Itoa(line)
}
