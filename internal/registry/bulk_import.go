// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// bulk_import.go implements CSV bulk device import.
//
// The import is deliberately row-independent: one malformed line must not
// discard nine good lines, so each row is inserted on its own and failures
// come back with the offending line number. That partial-success contract is
// the same one the DLQ page uses, and it matches how operators actually hold
// inventory sheets: fix the reported lines, upload again.

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

// MaxBulkImport caps the data rows in one upload. The cap keeps a single
// request from monopolising a connection pool slot and turns an accidental
// 200k-line export into a clear error instead of a silent truncation.
const MaxBulkImport = 1000

// MaxBulkImportBytes caps the raw CSV payload. It is generous for MaxBulkImport
// rows while still refusing a mistaken multi-gigabyte upload.
const MaxBulkImportBytes = 4 << 20 // 4 MiB

// BulkImportTemplate is the canonical example file. It doubles as the
// documented column order offered to operators.
const BulkImportTemplate = "canonical_name,profile_id,customer_id,group_id,rule_chain_group_id,state\n" +
	"warehouse-sensor-001,,,,,ACTIVE\n"

// bulkImportColumns are the accepted CSV headers, in template order. The
// header row is mandatory so a differently ordered sheet can never silently
// shift values into the wrong field.
var bulkImportColumns = []string{
	"canonical_name",
	"profile_id",
	"customer_id",
	"group_id",
	"rule_chain_group_id",
	"state",
}

// BulkImportFailure is one row that could not be imported.
type BulkImportFailure struct {
	// Line is the 1-based line number in the uploaded file, counting the
	// header, so the operator can jump straight to the row.
	Line int `json:"line"`
	// CanonicalName echoes the name from the failed row when readable.
	CanonicalName string `json:"canonical_name,omitempty"`
	// Reason explains the rejection in operator terms.
	Reason string `json:"reason"`
}

// BulkImportResult summarises one upload. Partial success is the normal case:
// Created plus Failed always equals the number of data rows.
type BulkImportResult struct {
	// Total is the number of data rows, excluding the header.
	Total int `json:"total"`
	// Created counts the devices inserted.
	Created int `json:"created"`
	// Failed lists every rejected row; it is always an array, never null.
	Failed []BulkImportFailure `json:"failed"`
}

// bulkImportRecord is one parsed data row with its file line number.
type bulkImportRecord struct {
	line  int
	input DeviceDTO
}

// BulkImportDevices creates every valid row of a CSV upload.
//
// Structural problems (missing header, unparsable file) fail the whole
// request before anything is written. Row problems (duplicate name, unknown
// profile, malformed UUID) fail only their own row: the devices already
// inserted stay, because making an operator re-upload an entire sheet to fix
// one typo is the worse trade-off.
func (a *Admin) BulkImportDevices(ctx context.Context, tenantID, actorID string,
	reader io.Reader) (BulkImportResult, error) {
	result := BulkImportResult{Failed: []BulkImportFailure{}}

	records, err := readBulkImportCSV(reader)
	if err != nil {
		return result, err
	}
	result.Total = len(records)

	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := a.CreateDevice(ctx, tenantID, rec.input); err != nil {
			result.Failed = append(result.Failed, BulkImportFailure{
				Line:          rec.line,
				CanonicalName: rec.input.CanonicalName,
				Reason:        bulkImportReason(err),
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
			ResourceType: "device",
			Details: map[string]any{
				"total":   result.Total,
				"created": result.Created,
				"failed":  len(result.Failed),
			},
		}); err != nil {
			slog.Warn("bulk device import audit append failed",
				"tenant", tenantID, "error", err)
		}
	}
	return result, nil
}

// readBulkImportCSV parses and validates the CSV shape without touching the
// database. Parsing is separated from insertion so structural errors are
// reported once, up front, rather than after half the file is written.
func readBulkImportCSV(reader io.Reader) ([]bulkImportRecord, error) {
	csvReader := csv.NewReader(io.LimitReader(reader, MaxBulkImportBytes))
	// FieldsPerRecord = -1 disables the ragged-row error so each row can be
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
	index, err := bulkImportHeaderIndex(header)
	if err != nil {
		return nil, err
	}

	records := make([]bulkImportRecord, 0, 64)
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
		if len(records) >= MaxBulkImport {
			return nil, invalidf("the file has more than %d device rows; split it into smaller files",
				MaxBulkImport)
		}
		input, err := bulkImportRow(row, index)
		if err != nil {
			return nil, invalidf("line %d: %s", line, err.Error())
		}
		records = append(records, bulkImportRecord{line: line, input: input})
	}
	return records, nil
}

// bulkImportHeaderIndex maps header cells to their column position, rejecting
// unknown and duplicated columns.
func bulkImportHeaderIndex(header []string) (map[string]int, error) {
	index := make(map[string]int, len(header))
	for pos, cell := range header {
		name := strings.ToLower(strings.TrimSpace(cell))
		if name == "" {
			continue
		}
		if !isBulkImportColumn(name) {
			return nil, invalidf("unknown column %q; supported columns are %s",
				name, strings.Join(bulkImportColumns, ", "))
		}
		if _, dup := index[name]; dup {
			return nil, invalidf("column %q appears twice", name)
		}
		index[name] = pos
	}
	if _, ok := index["canonical_name"]; !ok {
		return nil, invalid("the header must contain the canonical_name column")
	}
	return index, nil
}

// bulkImportRow turns one CSV row into a DeviceDTO. Absent optional columns
// stay empty, which means unassigned rather than invalid.
func bulkImportRow(row []string, index map[string]int) (DeviceDTO, error) {
	cell := func(name string) string {
		pos, ok := index[name]
		if !ok || pos >= len(row) {
			return ""
		}
		return strings.TrimSpace(row[pos])
	}
	input := DeviceDTO{
		CanonicalName:    cell("canonical_name"),
		ProfileID:        cell("profile_id"),
		CustomerID:       cell("customer_id"),
		GroupID:          cell("group_id"),
		RuleChainGroupID: cell("rule_chain_group_id"),
		State:            cell("state"),
	}
	// Accept "active" as well as "ACTIVE"; operators paste from spreadsheets.
	if input.State != "" {
		input.State = strings.ToUpper(input.State)
	}
	if input.CanonicalName == "" {
		return DeviceDTO{}, invalid("canonical_name is empty")
	}
	// A row longer than the header means the sheet has stray cells that would
	// silently disappear; a shorter row is fine because trailing columns are
	// optional.
	for name, pos := range index {
		if pos >= len(row) {
			return DeviceDTO{}, invalidf("row has no value for column %q; expected %d cells",
				name, len(index))
		}
	}
	return input, nil
}

// isBulkImportColumn reports whether name is a supported header.
func isBulkImportColumn(name string) bool {
	for _, col := range bulkImportColumns {
		if col == name {
			return true
		}
	}
	return false
}

// isBlankRow reports whether a row carries no content, so trailing blank lines
// do not become phantom failures.
func isBlankRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}

// bulkImportReason converts an insert failure into an operator-facing reason.
// The unique violation on (tenant_id, canonical_name) is the common case, so
// it gets a dedicated message instead of raw SQL.
func bulkImportReason(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if strings.Contains(msg, "duplicate key") || strings.Contains(msg, "UNIQUE constraint") {
		return "a device with this canonical_name already exists in this tenant"
	}
	return msg
}

// csvDetail reduces the csv package error to the part an operator can act on.
func csvDetail(err error) string {
	var parseErr *csv.ParseError
	if errors.As(err, &parseErr) && parseErr.Err != nil {
		return parseErr.Err.Error()
	}
	return err.Error()
}
