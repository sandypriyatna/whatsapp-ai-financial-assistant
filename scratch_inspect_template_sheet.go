package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

func main() {
	log.Println("🚀 Inspecting template spreadsheet 1HlCM4YwFJ2KYRpxGomIOiWaMyqhK5yMk6pMdkpqBWIQ...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	ctx := context.Background()
	// Use NewGoogleSheetRepository to load auth credentials
	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets connection error: %v", err)
	}

	srv := repo.GetService()
	templateID := "1HlCM4YwFJ2KYRpxGomIOiWaMyqhK5yMk6pMdkpqBWIQ"

	// Fetch full spreadsheet layout including formats, grids, formulas
	log.Println("📖 Fetching spreadsheet metadata and format details...")
	req := srv.Spreadsheets.Get(templateID).
		IncludeGridData(true)

	spreadsheet, err := req.Context(ctx).Do()
	if err != nil {
		log.Fatalf("❌ Failed to fetch template spreadsheet: %v", err)
	}

	outputFile, err := os.Create("template_inspection_report.md")
	if err != nil {
		log.Fatalf("❌ Failed to create report file: %v", err)
	}
	defer outputFile.Close()

	w := io.MultiWriter(os.Stdout, outputFile)

	fmt.Fprintln(w, "# Google Sheets Template Inspection Report")
	fmt.Fprintf(w, "- **Spreadsheet Title:** %s\n", spreadsheet.Properties.Title)
	fmt.Fprintf(w, "- **Spreadsheet ID:** %s\n\n", templateID)

	for _, sheet := range spreadsheet.Sheets {
		props := sheet.Properties
		fmt.Fprintf(w, "## Tab: %s (SheetID: %d)\n", props.Title, props.SheetId)
		fmt.Fprintf(w, "- **Grid Properties:** Rows=%d, Columns=%d, FrozenRows=%d, FrozenCols=%d\n",
			props.GridProperties.RowCount,
			props.GridProperties.ColumnCount,
			props.GridProperties.FrozenRowCount,
			props.GridProperties.FrozenColumnCount)

		// Check for hidden dimensions
		var hiddenRows []int
		var hiddenCols []int
		for _, data := range sheet.Data {
			// Row metadata
			for r, rm := range data.RowMetadata {
				if rm.HiddenByUser {
					hiddenRows = append(hiddenRows, r)
				}
			}
			// Column metadata
			for c, cm := range data.ColumnMetadata {
				if cm.HiddenByUser {
					hiddenCols = append(hiddenCols, c)
				}
			}
		}

		if len(hiddenRows) > 0 {
			fmt.Fprintf(w, "- **Hidden Rows:** %v\n", hiddenRows)
		} else {
			fmt.Fprintln(w, "- **Hidden Rows:** [None]")
		}
		if len(hiddenCols) > 0 {
			fmt.Fprintf(w, "- **Hidden Columns:** %v (Column index 0-based)\n", hiddenCols)
		} else {
			fmt.Fprintln(w, "- **Hidden Columns:** [None]")
		}

		// Read first few rows/cells to see styles and formulas
		fmt.Fprintln(w, "### Grid Data / Cell Details:")
		for _, data := range sheet.Data {
			for r, rowData := range data.RowData {
				if r >= 66 && !strings.Contains(strings.ToLower(props.Title), "april") {
					// Cap output for very long sheets
					continue
				}
				hasData := false
				for _, cell := range rowData.Values {
					if cell.FormattedValue != "" || cell.UserEnteredValue != nil || cell.UserEnteredFormat != nil {
						hasData = true
						break
					}
				}
				if !hasData {
					continue
				}

				fmt.Fprintf(w, "Row %02d: ", r+1)
				for c, cell := range rowData.Values {
					colChar := string('A' + c)
					if c >= 26 {
						colChar = string('A'+(c/26)-1) + string('A'+(c%26))
					}
					if cell.FormattedValue == "" && cell.UserEnteredValue == nil && cell.UserEnteredFormat == nil {
						continue
					}

					var valStr interface{}
					if cell.UserEnteredValue != nil {
						if cell.UserEnteredValue.FormulaValue != nil {
							valStr = "Formula: " + *cell.UserEnteredValue.FormulaValue
						} else if cell.UserEnteredValue.StringValue != nil {
							valStr = "String: " + *cell.UserEnteredValue.StringValue
						} else if cell.UserEnteredValue.NumberValue != nil {
							valStr = fmt.Sprintf("Number: %g", *cell.UserEnteredValue.NumberValue)
						} else if cell.UserEnteredValue.BoolValue != nil {
							valStr = fmt.Sprintf("Bool: %v", *cell.UserEnteredValue.BoolValue)
						}
					}

					fmt.Fprintf(w, "[%s: Formatted=%q, Entered=%v", colChar, cell.FormattedValue, valStr)
					if cell.UserEnteredFormat != nil {
						fmt.Fprint(w, ", Format=")
						fBytes, _ := json.Marshal(cell.UserEnteredFormat)
						fmt.Fprint(w, string(fBytes))
					}
					fmt.Fprint(w, "] | ")
				}
				fmt.Fprintln(w)
			}
		}
		fmt.Fprintln(w, "\n---")
	}

	log.Println("🎉 Template inspection complete. Report saved to template_inspection_report.md!")
}
