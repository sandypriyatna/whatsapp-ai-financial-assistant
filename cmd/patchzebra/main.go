package main

import (
	"context"
	"fmt"
	"log"

	"github.com/joho/godotenv"
	googlesheets "google.golang.org/api/sheets/v4"
	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

var (
	colorHeader = &googlesheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263} // #434343 (abu gelap)
	colorOdd    = &googlesheets.Color{Red: 1.0, Green: 1.0, Blue: 1.0}        // #FFFFFF (putih)
	colorEven   = &googlesheets.Color{Red: 0.976, Green: 0.973, Blue: 0.965}  // #F8F8F6 (abu sangat muda)
	whiteFG     = &googlesheets.Color{Red: 1.0, Green: 1.0, Blue: 1.0}
	borderColor = &googlesheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		log.Fatal(err)
	}

	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatal(err)
	}

	svc, sid := repo.SheetService()
	ctx := context.Background()

	sp, err := svc.Spreadsheets.Get(sid).Context(ctx).Do()
	if err != nil {
		log.Fatal(err)
	}

	for _, sh := range sp.Sheets {
		title := sh.Properties.Title
		sheetID := int(sh.Properties.SheetId)

		if title == "Dashboard" {
			continue
		}

		nCols := 0
		switch title {
		case "Budget":
			nCols = 5
		case "Notes":
			nCols = 3
		case "Reminders":
			nCols = 12
		default:
			nCols = 7
		}

		var reqs []*googlesheets.Request

		if len(sh.BandedRanges) > 0 {
			for _, br := range sh.BandedRanges {
				reqs = append(reqs, &googlesheets.Request{
					DeleteBanding: &googlesheets.DeleteBandingRequest{BandedRangeId: br.BandedRangeId},
				})
			}
		}

		reqs = append(reqs, &googlesheets.Request{
			AddBanding: &googlesheets.AddBandingRequest{
				BandedRange: &googlesheets.BandedRange{
					Range: &googlesheets.GridRange{
						SheetId:          int64(sheetID),
						StartRowIndex:    0,
						StartColumnIndex: 0,
						EndColumnIndex:   int64(nCols),
					},
					RowProperties: &googlesheets.BandingProperties{
						HeaderColor:     colorHeader,
						FirstBandColor:  colorOdd,
						SecondBandColor: colorEven,
					},
				},
			},
		})

		reqs = append(reqs, &googlesheets.Request{
			RepeatCell: &googlesheets.RepeatCellRequest{
				Range: &googlesheets.GridRange{
					SheetId:          int64(sheetID),
					StartRowIndex:    0,
					EndRowIndex:      1,
					StartColumnIndex: 0,
					EndColumnIndex:   int64(nCols),
				},
				Cell: &googlesheets.CellData{
					UserEnteredFormat: &googlesheets.CellFormat{
						BackgroundColor: colorHeader,
						TextFormat: &googlesheets.TextFormat{
							Bold:            true,
							FontFamily:      "Roboto",
							FontSize:        10,
							ForegroundColor: whiteFG,
						},
						HorizontalAlignment: "CENTER",
						VerticalAlignment:   "MIDDLE",
						Borders: &googlesheets.Borders{
							Top:    &googlesheets.Border{Style: "SOLID", Color: borderColor},
							Bottom: &googlesheets.Border{Style: "SOLID", Color: borderColor},
							Left:   &googlesheets.Border{Style: "SOLID", Color: borderColor},
							Right:  &googlesheets.Border{Style: "SOLID", Color: borderColor},
						},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat,horizontalAlignment,verticalAlignment,borders)",
			},
		})

		_, err := svc.Spreadsheets.BatchUpdate(sid, &googlesheets.BatchUpdateSpreadsheetRequest{
			Requests: reqs,
		}).Context(ctx).Do()
		if err != nil {
			log.Printf("  ❌ %s: %v", title, err)
		} else {
			log.Printf("  ✅ %s: banding, title, borders diperbarui (%d kolom)", title, nCols)
		}
	}

	fmt.Println("\n✅ Patch selesai! Refresh spreadsheet untuk melihat hasilnya.")
}
