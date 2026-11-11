// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// profile_bulk_import.go implements CSV bulk device-profile import.
//
// It follows the same row-independent partial-success contract as the
// device bulk import (see bulk_import.go): one bad line never discards
// the good lines, structural problems fail the whole upload up front,
// and per-row failures come back with the offending line number.

package registry

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"log/slog"
	"strings"

	"github.com/nereidai/nereidlink/internal/audit"
)

// MaxBulkImportProfiles caps the profile data rows in one upload.
const MaxBulkImportProfiles = 1000

// BulkImportProfileTemplate is the canonical example profile file and
// documents the accepted column order.
const BulkImportProfileTemplate = "name,rule_chain_group_id\n" +
	"Warehouse Sensor Profile,\n"

// bulkImportProfileColumns are the accepted profile CSV headers.
var bulkImportProfileColumns = []string{
	"name",
	"rule_chain_group_id",
}

// BulkImportProfileFailure is one profile row that could not be imported.
type BulkImportProfileFailure struct {
	// Line is the 1-based physical line number, counting the header.
	Line int `json:"line"`
	// Name echoes the profile name from the failed row when readable.
	Name string `json:"name,omitempty"`
	// Reason explains the rejection in operator terms.
	Reason string `json:"reason"`
}

// BulkImportProfilesResult summarises one profile upload. Created plus
// Failed always equals the number of data rows.
type BulkImportProfilesResult struct {
	// Total is the number of data rows, excluding the header.
	Total int `json:"total"`
	// Created counts the profiles inserted.
	Created int `json:"created"`
	// Failed lists every rejected row; it is always an array, never null.
	Failed []BulkImportProfileFailure `json:"failed"`
}

// profileBulkRecord is one parsed profile row with its line number.
type profileBulkRecord struct {
	line  int
	input ProfileDTO
}

// BulkImportProfiles creates every valid profile row of a CSV upload.
// Row problems fail only their own row; profiles already inserted stay.
func (a *Admin) BulkImportProfiles(ctx context.Context, tenantID, actorID string,
	reader io.Reader) (BulkImportProfilesResult, error) {
	result := BulkImportProfilesResult{Failed: []BulkImportProfileFailure{}}

	records, err := readBulkImportProfilesCSV(reader)
	if err != nil {
		return result, err
	}
	result.Total = len(records)

	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := a.CreateProfile(ctx, tenantID, rec.input); err != nil {
			result.Failed = append(result.Failed, BulkImportProfileFailure{
				Line:   rec.line,
				Name:   rec.input.Name,
				Reason: bulkImportProfileReason(err),
			})
			continue
		}
		result.Created++
	}

	if a.auditor != nil {
		if _, err := a.auditor.Append(ctx, audit.Input{
			TenantID:     tenantID,
			ActorID:      actorID,
			EventType:    audit.EventCredential,
			Action:       "bulk-import",
			ResourceType: "device_profile",
			Details: map[string]any{
				"total":   result.Total,
				"created": result.Created,
				"failed":  len(result.Failed),
			},
		}); err != nil {
			slog.Warn("bulk profile import audit append failed",
				"tenant", tenantID, "error", err)
		}
	}
	return result, nil
}

// readBulkImportProfilesCSV parses and validates the profile CSV shape
// without touching the database.
func readBulkImportProfilesCSV(reader io.Reader) ([]profileBulkRecord, error) {
	csvReader := csv.NewReader(io.LimitReader(reader, MaxBulkImportBytes))
	// FieldsPerRecord = -1 disables the ragged-row error so each row is
	// validated against the header instead of failing the whole file.
	csvReader.FieldsPerRecord = -1
	csvReader.TrimLeadingSpace = true

	header, err := csvReader.Read()
	if errors.Is(err, io.EOF) {
		return nil, invalid("the file is empty; the first line must be the header")
	}
	if err != nil {
		return nil, invalidf("cannot read the header line: %s", csvDetail(err))
	}
	index, err := bulkImportProfileHeaderIndex(header)
	if err != nil {
		return nil, err
	}

	records := make([]profileBulkRecord, 0, 32)
	// line tracks the physical line so a failure can be located by eye.
	line := 1
	for {
		row, err := csvReader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line++
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				return nil, invalidf("line %d: %s", parseErr.Line, csvDetail(err))
			}
			return nil, invalidf("line %d: %s", line, csvDetail(err))
		}
		if isBlankRow(row) {
			continue
		}
		if len(records) >= MaxBulkImportProfiles {
			return nil, invalidf("the file has more than %d profile rows; split it into smaller files",
				MaxBulkImportProfiles)
		}
		input, err := bulkImportProfileRow(row, index)
		if err != nil {
			return nil, invalidf("line %d: %s", line, err.Error())
		}
		records = append(records, profileBulkRecord{line: line, input: input})
	}
	return records, nil
}

// bulkImportProfileHeaderIndex maps header cells to positions, rejecting
// unknown and duplicated columns.
func bulkImportProfileHeaderIndex(header []string) (map[string]int, error) {
	index := make(map[string]int, len(header))
	for pos, cell := range header {
		name := strings.ToLower(strings.TrimSpace(cell))
		if name == "" {
			continue
		}
		if !isBulkImportProfileColumn(name) {
			return nil, invalidf("unknown column %q; supported columns are %s",
				name, strings.Join(bulkImportProfileColumns, ", "))
		}
		if _, dup := index[name]; dup {
			return nil, invalidf("column %q appears twice", name)
		}
		index[name] = pos
	}
	if _, ok := index["name"]; !ok {
		return nil, invalid("the header must contain the name column")
	}
	return index, nil
}

// bulkImportProfileRow turns one CSV row into a ProfileDTO.
func bulkImportProfileRow(row []string, index map[string]int) (ProfileDTO, error) {
	cell := func(name string) string {
		pos, ok := index[name]
		if !ok || pos >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[pos])
	}
	input := ProfileDTO{
		Name:             cell("name"),
		RuleChainGroupID: cell("rule_chain_group_id"),
	}
	if input.Name == "" {
		return ProfileDTO{}, invalid("name is empty")
	}
	// A row longer than the header hides stray cells; a shorter row is
	// fine because trailing columns are optional.
	for name, pos := range index {
		if pos >= len(row) {
			return ProfileDTO{}, invalidf("row has no value for column %q; expected %d cells",
				name, len(index))
		}
	}
	return input, nil
}

// isBulkImportProfileColumn reports whether name is a supported header.
func isBulkImportProfileColumn(name string) bool {
	for _, col := range bulkImportProfileColumns {
		if col == name {
			return true
		}
	}
	return false
}

// bulkImportProfileReason converts an insert failure into an operator-facing
// reason; the duplicate profile name is the common case.
func bulkImportProfileReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "UNIQUE constraint") {
		return "a profile with this name already exists in this tenant"
	}
	return msg
}
