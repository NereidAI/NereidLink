// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// bulk_import_template.go renders the fillable device import template in CSV
// and XLSX.
//
// The template is not a static file: it is generated from the same column
// list the parser accepts, so the two can never drift. The XLSX variant adds
// the guidance an operator actually needs -- an instruction sheet, a data
// validation dropdown for the state column and the reason codes -- which a
// bare CSV cannot carry.

package gatewayapi

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"io"
	"strings"

	"github.com/xuri/excelize/v2"
)

// bulkTemplateColumns is the canonical column order of the import file. It
// must stay identical to the parser's accepted headers.
var bulkTemplateColumns = []string{
	"canonical_name",
	"profile_id",
	"customer_id",
	"group_id",
	"rule_chain_group_id",
	"state",
}

// bulkTemplateSample is the example row written into every template. The
// values are deliberately blank except the two an operator must see filled in,
// so the shape is obvious without implying the UUIDs are real.
var bulkTemplateSample = []string{
	"warehouse-sensor-001",
	"",
	"",
	"",
	"",
	"ACTIVE",
}

// bulkTemplateReasonCodes are the structured quarantine reasons an operator
// may pick. They are listed on the template's instruction sheet so the
// dropdown and the backend vocabulary stay aligned by documentation.
var bulkTemplateReasonCodes = []string{
	"credential-compromise",
	"device-stolen",
	"burst-flood",
	"firmware-anomaly",
	"other",
}

// bulkTemplateStates are the lifecycle states a new device may be created in.
var bulkTemplateStates = []string{"ACTIVE", "UNCLAIMED"}

// renderBulkImportCSV builds the CSV template. encoding/csv quotes only where
// required, so the file stays readable when opened in a text editor, and it
// emits a UTF-8 BOM so Excel on Windows does not mangle non-ASCII device
// names.
func renderBulkImportCSV() ([]byte, error) {
	var buf bytes.Buffer
	// Excel assumes the ANSI code page for CSV without a BOM, which corrupts
	// UTF-8 names. The BOM is the standard, harmless fix.
	buf.Write([]byte{0xEF, 0xBB, 0xBF})

	writer := csv.NewWriter(&buf)
	if err := writer.Write(bulkTemplateColumns); err != nil {
		return nil, fmt.Errorf("write csv header: %w", err)
	}
	if err := writer.Write(bulkTemplateSample); err != nil {
		return nil, fmt.Errorf("write csv sample: %w", err)
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush csv: %w", err)
	}
	return buf.Bytes(), nil
}

// renderBulkImportXLSX builds the richer XLSX template: an instructions sheet,
// a data sheet with a validated state dropdown, and one example row.
func renderBulkImportXLSX() ([]byte, error) {
	f := excelize.NewFile()
	defer func() { _ = f.Close() }()

	const dataSheet = "devices"
	const guideSheet = "readme"
	// renameSheet keeps the default Sheet1 as the data sheet so the file opens
	// on the row the operator has to fill in.
	if err := f.SetSheetName("Sheet1", dataSheet); err != nil {
		return nil, fmt.Errorf("rename sheet: %w", err)
	}

	for col, name := range bulkTemplateColumns {
		cell, err := excelize.CoordinatesToCellName(col+1, 1)
		if err != nil {
			return nil, fmt.Errorf("header cell %d: %w", col, err)
		}
		if err := f.SetCellValue(dataSheet, cell, name); err != nil {
			return nil, fmt.Errorf("set header %s: %w", name, err)
		}
	}

	sample, err := excelize.CoordinatesToCellName(1, 2)
	if err != nil {
		return nil, fmt.Errorf("sample cell: %w", err)
	}
	if err := f.SetSheetRow(dataSheet, sample, &bulkTemplateSample); err != nil {
		return nil, fmt.Errorf("set sample row: %w", err)
	}

	// Bold the header so the required columns stand out when printed.
	headerStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true}})
	if err != nil {
		return nil, fmt.Errorf("header style: %w", err)
	}
	if err := f.SetCellStyle(dataSheet, "A1", "F1", headerStyle); err != nil {
		return nil, fmt.Errorf("style header: %w", err)
	}

	// Freeze the header row: bulk sheets are long enough that scrolling past
	// the column names is guaranteed.
	if err := f.SetPanes(dataSheet, &excelize.Panes{
		Freeze:      true,
		Split:       false,
		XSplit:      0,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	}); err != nil {
		return nil, fmt.Errorf("freeze header: %w", err)
	}

	// Restrict the state column to the two creatable states, so an operator
	// cannot type DECOMMISSIONED and wonder why the row was rejected.
	stateCol, err := excelize.ColumnNumberToName(6)
	if err != nil {
		return nil, fmt.Errorf("state column: %w", err)
	}
	validation := excelize.NewDataValidation(true)
	validation.SetSqref(fmt.Sprintf("%s2:%s5000", stateCol, stateCol))
	if err := validation.SetDropList(bulkTemplateStates); err != nil {
		return nil, fmt.Errorf("state drop list: %w", err)
	}
	validation.ShowErrorMessage = true
	validation.SetError(excelize.DataValidationErrorStyleStop, "Invalid state",
		"state must be ACTIVE or UNCLAIMED")
	if err := f.AddDataValidation(dataSheet, validation); err != nil {
		return nil, fmt.Errorf("state validation: %w", err)
	}

	// A second sheet carrying the documentation a CSV simply cannot hold.
	if _, err := f.NewSheet(guideSheet); err != nil {
		return nil, fmt.Errorf("guide sheet: %w", err)
	}
	guide := [][]any{
		{"Device bulk import"},
		{},
		{"Fill in the 'devices' sheet and upload it. One device per row."},
		{"The first line is the header; keep the column names exactly as given."},
		{},
		{"canonical_name", "Required. Unique within the tenant. Becomes the device identity."},
		{"profile_id", "Optional UUID. Empty means no profile."},
		{"customer_id", "Optional UUID. Empty leaves the device unassigned."},
		{"group_id", "Optional UUID. Empty leaves the device ungrouped."},
		{"rule_chain_group_id", "Optional UUID. Empty uses the tenant default."},
		{"state", "ACTIVE (normal) or UNCLAIMED (pre-registered, not yet claimed)."},
		{},
		{"Rules"},
		{"Rows are independent. A bad row is reported with its line number and the rest still import."},
		{"Only the header row is required; leave optional cells empty."},
		{"At most 1000 rows per file."},
		{"Quarantine reason codes (used when isolating a device): " + strings.Join(bulkTemplateReasonCodes, ", ")},
	}
	for row, cells := range guide {
		cell, err := excelize.CoordinatesToCellName(1, row+1)
		if err != nil {
			return nil, fmt.Errorf("guide cell %d: %w", row, err)
		}
		if err := f.SetSheetRow(guideSheet, cell, &cells); err != nil {
			return nil, fmt.Errorf("guide row %d: %w", row, err)
		}
	}
	titleStyle, err := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	if err != nil {
		return nil, fmt.Errorf("title style: %w", err)
	}
	if err := f.SetCellStyle(guideSheet, "A1", "A1", titleStyle); err != nil {
		return nil, fmt.Errorf("style title: %w", err)
	}

	// SetActiveSheet has no error return; the data sheet is index 0 already
	// because it was renamed from the default sheet rather than appended.
	f.SetActiveSheet(0)

	var out bytes.Buffer
	if err := f.Write(&out); err != nil {
		return nil, fmt.Errorf("encode xlsx: %w", err)
	}
	return out.Bytes(), nil
}

// xlsxToCSVReader reads an XLSX file and returns a CSV reader for the
// first data sheet, using the same column order as the template parser.
func xlsxToCSVReader(file io.Reader) (io.Reader, error) {
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("read xlsx: %w", err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("open xlsx: %w", err)
	}
	defer func() { _ = f.Close() }()

	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fmt.Errorf("xlsx has no sheets")
	}
	// The template generator uses "devices" as the data sheet name.
	// Fall back to the first sheet if not found.
	sheet := "devices"
	if idx, _ := f.GetSheetIndex(sheet); idx == -1 {
		sheet = sheets[0]
	}

	rows, err := f.GetRows(sheet)
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("sheet is empty")
	}

	var out bytes.Buffer
	writer := csv.NewWriter(&out)
	// Write header + all rows exactly as they appear (including the
	// template's header row and sample row). The parser will validate
	// the header and skip blank rows.
	for _, row := range rows {
		if err := writer.Write(row); err != nil {
			return nil, fmt.Errorf("write csv row: %w", err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush csv: %w", err)
	}
	return bytes.NewReader(out.Bytes()), nil
}
