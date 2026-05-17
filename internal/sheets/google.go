package sheets

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type GoogleSheetRepository struct {
	service       *sheets.Service
	spreadsheetID string
	tabManager    *TabManager

	// txMu serialises the read-ID + append sequence so concurrent messages
	// never generate the same transaction ID.
	txMu sync.Mutex

	// In-memory cache to reduce Google Sheets API roundtrips
	cacheMu         sync.RWMutex
	budgetCache     map[string]float64
	budgetCacheTime time.Time
	txCache         map[string][]Transaction
	txCacheTime     map[string]time.Time
}

func (r *GoogleSheetRepository) GetService() *sheets.Service {
	if r == nil {
		return nil
	}
	return r.service
}

func NewGoogleSheetRepository(credsPath, credsJSON, spreadsheetID string) (*GoogleSheetRepository, error) {
	ctx := context.Background()

	var opts []option.ClientOption
	if credsJSON != "" {
		opts = append(opts, option.WithCredentialsJSON([]byte(credsJSON)))
	} else {
		opts = append(opts, option.WithCredentialsFile(credsPath))
	}
	opts = append(opts, option.WithScopes(sheets.SpreadsheetsScope))

	srv, err := sheets.NewService(ctx, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create sheets service: %w", err)
	}

	return &GoogleSheetRepository{
		service:         srv,
		spreadsheetID:   spreadsheetID,
		tabManager:      NewTabManager(srv, spreadsheetID),
		budgetCache:     make(map[string]float64),
		txCache:         make(map[string][]Transaction),
		txCacheTime:     make(map[string]time.Time),
	}, nil
}

// SheetService returns the underlying Sheets API service and spreadsheet ID.
// Used by tooling (e.g. seed scripts) that need direct API access.
func (r *GoogleSheetRepository) SheetService() (*sheets.Service, string) {
	return r.service, r.spreadsheetID
}

// DeleteDefaultSheet removes the default "Sheet1" tab that Google Sheets
// creates automatically when a new spreadsheet is made.
// It is safe to call on spreadsheets that don't have this tab — it silently
// succeeds in that case.
func (r *GoogleSheetRepository) DeleteDefaultSheet(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	sp, err := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get spreadsheet metadata: %w", err)
	}

	// Spreadsheets must have at least 1 sheet — only delete Sheet1 if there
	// are other sheets present (otherwise we'd leave the spreadsheet empty).
	if len(sp.Sheets) <= 1 {
		return nil // nothing to delete, or it's the only tab
	}

	for _, sh := range sp.Sheets {
		if sh == nil || sh.Properties == nil {
			continue
		}
		title := sh.Properties.Title
		if title != "Sheet1" && title != "Лист1" && title != "Feuille 1" {
			continue
		}

		req := &sheets.BatchUpdateSpreadsheetRequest{
			Requests: []*sheets.Request{
				{
					DeleteSheet: &sheets.DeleteSheetRequest{
						SheetId: sh.Properties.SheetId,
					},
				},
			},
		}
		if err := r.withRetry(ctx, func() error {
			_, e := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
			return e
		}); err != nil {
			// Non-fatal — the sheet might be the last one in a timing race.
			return nil
		}
		// Invalidate tab cache after deletion.
		_ = r.tabManager.RefreshCache(ctx)
		return nil
	}

	return nil // Sheet1 not found — already clean
}

var monthNamesID = map[time.Month]string{
	time.January:   "Januari",
	time.February:  "Februari",
	time.March:     "Maret",
	time.April:     "April",
	time.May:       "Mei",
	time.June:      "Juni",
	time.July:      "Juli",
	time.August:    "Agustus",
	time.September: "September",
	time.October:   "Oktober",
	time.November:  "November",
	time.December:  "Desember",
}

func TabNameForTime(t time.Time) string {
	local := t.In(WIB)
	monthName, ok := monthNamesID[local.Month()]
	if !ok {
		monthName = local.Month().String()
	}
	return fmt.Sprintf("%s %d", monthName, local.Year())
}

// withRetry executes op with up to maxAttempts tries, applying exponential
// back-off on retryable errors (rate-limit 429 and transient 5xx).
func (r *GoogleSheetRepository) withRetry(ctx context.Context, op func() error) error {
	const maxAttempts = 3
	backoff := 600 * time.Millisecond

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		lastErr = op()
		if lastErr == nil {
			return nil
		}
		if !isRetryableSheetError(lastErr) {
			return lastErr
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return lastErr
}

// isRetryableSheetError returns true for transient Google API errors.
func isRetryableSheetError(err error) bool {
	if err == nil {
		return false
	}
	// googleapi.Error carries HTTP status codes.
	var gErr *googleapi.Error
	if ok := isGoogleAPIError(err, &gErr); ok {
		code := gErr.Code
		return code == http.StatusTooManyRequests ||
			code == http.StatusServiceUnavailable ||
			code == http.StatusBadGateway ||
			code == http.StatusGatewayTimeout ||
			code == http.StatusInternalServerError
	}
	// Treat generic network errors as retryable.
	s := err.Error()
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "EOF") ||
		strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "deadline exceeded")
}

// isGoogleAPIError unwraps the error chain looking for *googleapi.Error.
func isGoogleAPIError(err error, out **googleapi.Error) bool {
	for err != nil {
		if e, ok := err.(*googleapi.Error); ok {
			*out = e
			return true
		}
		// Unwrap one level.
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}

// AppendTransaction adds a transaction row to the month tab.
// The txMu mutex ensures that reading the last ID and appending the new row
// are treated as one atomic unit — preventing duplicate IDs when concurrent
// messages arrive (e.g. from a group chat).
func (r *GoogleSheetRepository) AppendTransaction(ctx context.Context, tx *Transaction) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if r.service == nil {
		return fmt.Errorf("sheets service is nil")
	}
	if tx == nil {
		return fmt.Errorf("transaction is nil")
	}

	tabName := TabNameForTime(tx.Date)

	if err := r.EnsureTabExists(ctx, tabName); err != nil {
		return fmt.Errorf("failed to ensure tab %q: %w", tabName, err)
	}

	// --- ATOMIC: lock for ID generation + append ---
	r.txMu.Lock()
	nextID, err := r.nextDailyTransactionID(ctx, tabName, tx.Date)
	if err != nil {
		r.txMu.Unlock()
		return fmt.Errorf("failed to generate transaction id: %w", err)
	}
	tx.ID = nextID

	valueRange := &sheets.ValueRange{
		Values: [][]interface{}{tx.ToRow()},
	}
	appendRange := fmt.Sprintf("'%s'!A:G", tabName)

	appendErr := r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.Values.
			Append(r.spreadsheetID, appendRange, valueRange).
			ValueInputOption("USER_ENTERED").
			InsertDataOption("INSERT_ROWS").
			IncludeValuesInResponse(true).
			Context(ctx).
			Do()
		return e
	})
	r.txMu.Unlock() // release as soon as the row is committed
	if appendErr != nil {
		return fmt.Errorf("sheets append failed: %w", appendErr)
	}

	// Post-append: auto-sort by Date ascending + set BasicFilter (best-effort).
	if sheetID, tabErr := r.tabManager.GetTabID(ctx, tabName); tabErr == nil {
		sortReq := &sheets.BatchUpdateSpreadsheetRequest{
			Requests: []*sheets.Request{
				{
					SetBasicFilter: &sheets.SetBasicFilterRequest{
						Filter: &sheets.BasicFilter{
							Range: &sheets.GridRange{
								SheetId:          sheetID,
								StartRowIndex:    0,
								EndRowIndex:      1000,
								StartColumnIndex: 0,
								EndColumnIndex:   7,
							},
						},
					},
				},
				{
					SortRange: &sheets.SortRangeRequest{
						Range: &sheets.GridRange{
							SheetId:          sheetID,
							StartRowIndex:    1,
							EndRowIndex:      1000,
							StartColumnIndex: 0,
							EndColumnIndex:   7,
						},
						SortSpecs: []*sheets.SortSpec{
							{
								DimensionIndex: 1, // Column B = Tanggal
								SortOrder:      "ASCENDING",
							},
						},
					},
				},
			},
		}
		_ = r.withRetry(ctx, func() error {
			_, e := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, sortReq).Context(ctx).Do()
			return e
		})
	}

	r.cacheMu.Lock()
	if r.txCache != nil {
		delete(r.txCache, tabName)
		delete(r.txCacheTime, tabName)
	}
	r.cacheMu.Unlock()

	return nil
}

// ClearTab clears all data in a given tab starting from row 2 (preserving header).
func (r *GoogleSheetRepository) ClearTab(ctx context.Context, tabName string) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if r.service == nil {
		return fmt.Errorf("sheets service is nil")
	}
	tabName = strings.TrimSpace(tabName)
	if tabName == "" {
		return fmt.Errorf("tab name is required")
	}

	clearRange := fmt.Sprintf("'%s'!A2:G", tabName)
	err := r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.Values.Clear(r.spreadsheetID, clearRange, &sheets.ClearValuesRequest{}).Context(ctx).Do()
		return e
	})
	if err != nil {
		return fmt.Errorf("failed to clear tab %s: %w", tabName, err)
	}
	
	// Invalidate local transaction cache
	r.cacheMu.Lock()
	if r.txCache != nil {
		delete(r.txCache, tabName)
		delete(r.txCacheTime, tabName)
	}
	r.cacheMu.Unlock()
	return nil
}

// AppendTransactionsBatch appends a batch of transactions to a tab in a single write.
func (r *GoogleSheetRepository) AppendTransactionsBatch(ctx context.Context, tabName string, txs []*Transaction) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if r.service == nil {
		return fmt.Errorf("sheets service is nil")
	}
	if len(txs) == 0 {
		return nil
	}
	
	if err := r.EnsureTabExists(ctx, tabName); err != nil {
		return fmt.Errorf("failed to ensure tab %q: %w", tabName, err)
	}
	
	r.txMu.Lock()
	defer r.txMu.Unlock()
	
	// Calculate starting daily counters for each date prefix in the batch
	dailyCounters := make(map[string]int)
	
	// Read existing transactions to find the starting max counter for each date prefix
	existing, err := r.GetTransactions(ctx, tabName)
	if err == nil {
		for _, tx := range existing {
			for _, newTx := range txs {
				datePrefix := newTx.Date.In(WIB).Format("20060102")
				if counter, ok := parseDailyCounter(tx.ID, datePrefix); ok {
					if counter > dailyCounters[datePrefix] {
						dailyCounters[datePrefix] = counter
					}
				}
			}
		}
	}
	
	var rows [][]interface{}
	for _, tx := range txs {
		datePrefix := tx.Date.In(WIB).Format("20060102")
		dailyCounters[datePrefix]++
		tx.ID = fmt.Sprintf("%s-%03d", datePrefix, dailyCounters[datePrefix])
		rows = append(rows, tx.ToRow())
	}
	
	valueRange := &sheets.ValueRange{
		Values: rows,
	}
	appendRange := fmt.Sprintf("'%s'!A:G", tabName)
	
	err = r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.Values.
			Append(r.spreadsheetID, appendRange, valueRange).
			ValueInputOption("USER_ENTERED").
			InsertDataOption("INSERT_ROWS").
			Context(ctx).
			Do()
		return e
	})
	if err != nil {
		return fmt.Errorf("batch append failed: %w", err)
	}
	
	// Post-append: auto-sort and format headers (best-effort)
	if sheetID, tabErr := r.tabManager.GetTabID(ctx, tabName); tabErr == nil {
		sortReq := &sheets.BatchUpdateSpreadsheetRequest{
			Requests: []*sheets.Request{
				{
					SetBasicFilter: &sheets.SetBasicFilterRequest{
						Filter: &sheets.BasicFilter{
							Range: &sheets.GridRange{
								SheetId:          sheetID,
								StartRowIndex:    0,
								EndRowIndex:      1000,
								StartColumnIndex: 0,
								EndColumnIndex:   7,
							},
						},
					},
				},
				{
					SortRange: &sheets.SortRangeRequest{
						Range: &sheets.GridRange{
							SheetId:          sheetID,
							StartRowIndex:    1,
							EndRowIndex:      1000,
							StartColumnIndex: 0,
							EndColumnIndex:   7,
						},
						SortSpecs: []*sheets.SortSpec{
							{
								DimensionIndex: 1, // Column B = Tanggal
								SortOrder:      "ASCENDING",
							},
						},
					},
				},
			},
		}
		_ = r.withRetry(ctx, func() error {
			_, e := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, sortReq).Context(ctx).Do()
			return e
		})
	}
	
	// Invalidate local transaction cache
	r.cacheMu.Lock()
	if r.txCache != nil {
		delete(r.txCache, tabName)
		delete(r.txCacheTime, tabName)
	}
	r.cacheMu.Unlock()
	
	return nil
}

// FormatFont applies premium formatting to any tab (Monthly transactions, Budget, Notes, Reminders):
// - Header (row 1): Bold, White text, dark professional grey template background (#434343), middle/center aligned.
// - Zebra banding: alternating between White and Light Grey (#F2F2F2) for crisp readability.
// - Data rows (row 2+): Roboto 10pt, pure Black text color.
// - Date & Time & Rupiah Column Formats: dynamically applied per sheet layout.
// - Columns: exact fixed standard template widths.
func (r *GoogleSheetRepository) FormatFont(ctx context.Context, tabName string) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if r.service == nil {
		return fmt.Errorf("sheets service is nil")
	}
	if strings.EqualFold(tabName, "Dashboard") {
		return nil
	}

	sheetID, err := r.tabManager.GetTabID(ctx, tabName)
	if err != nil {
		return fmt.Errorf("failed to get tab ID for %s: %w", tabName, err)
	}

	// 1. Fetch current spreadsheet to find and delete any existing banding styles on this tab
	spreadsheet, err := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if err == nil {
		var deleteRequests []*sheets.Request
		for _, sheet := range spreadsheet.Sheets {
			if sheet.Properties.SheetId == int64(sheetID) {
				for _, banding := range sheet.BandedRanges {
					deleteRequests = append(deleteRequests, &sheets.Request{
						DeleteBanding: &sheets.DeleteBandingRequest{
							BandedRangeId: banding.BandedRangeId,
						},
					})
				}
			}
		}
		if len(deleteRequests) > 0 {
			_ = r.withRetry(ctx, func() error {
				_, e := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, &sheets.BatchUpdateSpreadsheetRequest{
					Requests: deleteRequests,
				}).Context(ctx).Do()
				return e
			})
		}
	}

	// 2. Define layout configuration dynamically based on the tabName
	var colCount int64
	var colWidths []int64
	var customRequests []*sheets.Request

	switch tabName {
	case "Budget":
		colCount = 5
		colWidths = []int64{320, 497, 327, 279, 222}
		// Budget, Terpakai, Sisa columns (B, C, D / index 1, 2, 3) as Rupiah right-aligned
		customRequests = append(customRequests, &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    1,
					EndRowIndex:      1000,
					StartColumnIndex: 1,
					EndColumnIndex:   4,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat: &sheets.NumberFormat{
							Type:    "NUMBER",
							Pattern: "\"Rp\" #,##0",
						},
						HorizontalAlignment: "RIGHT",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		})

	case "Notes":
		colCount = 3
		colWidths = []int64{302, 338, 1005}
		// Tanggal column (B / index 1) as DATE centered
		customRequests = append(customRequests, &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    1,
					EndRowIndex:      1000,
					StartColumnIndex: 1,
					EndColumnIndex:   2,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat: &sheets.NumberFormat{
							Type:    "DATE",
							Pattern: "dd/mm/yyyy",
						},
						HorizontalAlignment: "CENTER",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		})

	case ReminderSheetName: // "Reminders"
		colCount = 13 // 12 visible columns + 1 hidden Target JID column
		colWidths = []int64{109, 134, 135, 279, 85, 134, 86, 133, 123, 136, 126, 159, 100}
		// TargetDate (B / 1), CreatedDate (H / 7), ModifiedDate (J / 9) as DATE centered
		for _, colIdx := range []int64{1, 7, 9} {
			customRequests = append(customRequests, &sheets.Request{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId:          sheetID,
						StartRowIndex:    1,
						EndRowIndex:      1000,
						StartColumnIndex: colIdx,
						EndColumnIndex:   colIdx + 1,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							NumberFormat: &sheets.NumberFormat{
								Type:    "DATE",
								Pattern: "dd/mm/yyyy",
							},
							HorizontalAlignment: "CENTER",
						},
					},
					Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
				},
			})
		}
		// TargetTime (C / 2), CreatedTime (I / 8), ModifiedTime (K / 10) as TEXT centered
		for _, colIdx := range []int64{2, 8, 10} {
			customRequests = append(customRequests, &sheets.Request{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId:          sheetID,
						StartRowIndex:    1,
						EndRowIndex:      1000,
						StartColumnIndex: colIdx,
						EndColumnIndex:   colIdx + 1,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							NumberFormat: &sheets.NumberFormat{
								Type: "TEXT",
							},
							HorizontalAlignment: "CENTER",
						},
					},
					Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
				},
			})
		}
		// Hide Column M (index 12, Target JID)
		customRequests = append(customRequests, &sheets.Request{
			UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
				Properties: &sheets.DimensionProperties{
					HiddenByUser: true,
				},
				Fields: "hiddenByUser",
				Range: &sheets.DimensionRange{
					SheetId:    sheetID,
					Dimension:  "COLUMNS",
					StartIndex: 12,
					EndIndex:   13,
				},
			},
		})

	default: // Monthly transaction tabs
		colCount = 7
		colWidths = []int64{158, 163, 144, 288, 233, 389, 260}
		// Tanggal Column (B / index 1) as DATE centered
		customRequests = append(customRequests, &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    1,
					EndRowIndex:      1000,
					StartColumnIndex: 1,
					EndColumnIndex:   2,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat: &sheets.NumberFormat{
							Type:    "DATE",
							Pattern: "dd/mm/yyyy",
						},
						HorizontalAlignment: "CENTER",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		})
		// Waktu Column (C / index 2) as TEXT centered
		customRequests = append(customRequests, &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    1,
					EndRowIndex:      1000,
					StartColumnIndex: 2,
					EndColumnIndex:   3,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat: &sheets.NumberFormat{
							Type: "TEXT",
						},
						HorizontalAlignment: "CENTER",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		})
		// Jumlah Column (G / index 6) as Rupiah
		customRequests = append(customRequests, &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    1,
					EndRowIndex:      1000,
					StartColumnIndex: 6,
					EndColumnIndex:   7,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat: &sheets.NumberFormat{
							Type:    "NUMBER",
							Pattern: "\"Rp\" #,##0",
						},
						HorizontalAlignment: "RIGHT",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		})
		// Hide Column A (index 0, technical Transaction ID)
		customRequests = append(customRequests, &sheets.Request{
			UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
				Properties: &sheets.DimensionProperties{
					HiddenByUser: true,
				},
				Fields: "hiddenByUser",
				Range: &sheets.DimensionRange{
					SheetId:    sheetID,
					Dimension:  "COLUMNS",
					StartIndex: 0,
					EndIndex:   1,
				},
			},
		})
	}

	blackColor := &sheets.Color{Red: 0, Green: 0, Blue: 0}
	whiteColor := &sheets.Color{Red: 1, Green: 1, Blue: 1}
	bgHeaderColor := &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263} // Dark grey template color: #434343

	var requests []*sheets.Request

	// Ensure grid column count is at least colCount to prevent dimension errors
	requests = append(requests, &sheets.Request{
		UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
			Properties: &sheets.SheetProperties{
				SheetId: int64(sheetID),
				GridProperties: &sheets.GridProperties{
					ColumnCount: colCount,
				},
			},
			Fields: "gridProperties.columnCount",
		},
	})

	// 3. Format Headers (Row 1)
	requests = append(requests, &sheets.Request{
		RepeatCell: &sheets.RepeatCellRequest{
			Range: &sheets.GridRange{
				SheetId:          sheetID,
				StartRowIndex:    0,
				EndRowIndex:      1,
				StartColumnIndex: 0,
				EndColumnIndex:   colCount,
			},
			Cell: &sheets.CellData{
				UserEnteredFormat: &sheets.CellFormat{
					BackgroundColor: bgHeaderColor,
					TextFormat: &sheets.TextFormat{
						Bold:            true,
						FontFamily:      "Roboto",
						FontSize:        11,
						ForegroundColor: whiteColor,
					},
					HorizontalAlignment: "CENTER",
					VerticalAlignment:   "MIDDLE",
				},
			},
			Fields: "userEnteredFormat(backgroundColor,textFormat(bold,fontFamily,fontSize,foregroundColor),horizontalAlignment,verticalAlignment)",
		},
	})

	// 4. Add brand-new crisp zebra banding
	requests = append(requests, &sheets.Request{
		AddBanding: &sheets.AddBandingRequest{
			BandedRange: &sheets.BandedRange{
				Range: &sheets.GridRange{
					SheetId:          sheetID,
					StartRowIndex:    0,
					StartColumnIndex: 0,
					EndColumnIndex:   colCount,
				},
				RowProperties: &sheets.BandingProperties{
					HeaderColor:     bgHeaderColor,
					FirstBandColor:  &sheets.Color{Red: 1.0, Green: 1.0, Blue: 1.0},        // White #FFFFFF
					SecondBandColor: &sheets.Color{Red: 0.949, Green: 0.949, Blue: 0.949},  // Distinct Light Grey #F2F2F2
				},
			},
		},
	})

	// 5. Format Data Rows (Rows 2 to 1000) - Font Roboto 10pt pure Black text color and clear manual background color
	requests = append(requests, &sheets.Request{
		RepeatCell: &sheets.RepeatCellRequest{
			Range: &sheets.GridRange{
				SheetId:          sheetID,
				StartRowIndex:    1,
				EndRowIndex:      1000,
				StartColumnIndex: 0,
				EndColumnIndex:   colCount,
			},
			Cell: &sheets.CellData{
				UserEnteredFormat: &sheets.CellFormat{
					TextFormat: &sheets.TextFormat{
						FontFamily:      "Roboto",
						FontSize:        10,
						ForegroundColor: blackColor,
					},
				},
			},
			Fields: "userEnteredFormat.textFormat(fontFamily,fontSize,foregroundColor),userEnteredFormat.backgroundColor",
		},
	})

	// 6. Add custom formats (DATE, TEXT, Rupiah, alignments)
	requests = append(requests, customRequests...)

	// 7. Set Standard Column Widths
	for i, width := range colWidths {
		requests = append(requests, &sheets.Request{
			UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
				Properties: &sheets.DimensionProperties{PixelSize: width},
				Fields:     "pixelSize",
				Range: &sheets.DimensionRange{
					SheetId: sheetID, Dimension: "COLUMNS", StartIndex: int64(i), EndIndex: int64(i + 1),
				},
			},
		})
	}

	// 8. Set Standard Row Heights (Margin)
	// Header Row (Row 1 / index 0) to 44 pixels
	requests = append(requests, &sheets.Request{
		UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
			Properties: &sheets.DimensionProperties{PixelSize: 44},
			Fields:     "pixelSize",
			Range: &sheets.DimensionRange{
				SheetId:    sheetID,
				Dimension:  "ROWS",
				StartIndex: 0,
				EndIndex:   1,
			},
		},
	})

	// Data Rows (Rows 2 to 1000 / index 1 to 1000) to 24 pixels
	requests = append(requests, &sheets.Request{
		UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
			Properties: &sheets.DimensionProperties{PixelSize: 24},
			Fields:     "pixelSize",
			Range: &sheets.DimensionRange{
				SheetId:    sheetID,
				Dimension:  "ROWS",
				StartIndex: 1,
				EndIndex:   1000,
			},
		},
	})

	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: requests,
	}

	err = r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
		return e
	})
	if err != nil {
		return fmt.Errorf("failed to apply generic premium formatting to %s: %w", tabName, err)
	}
	return nil
}

// GetTransactions reads all transactions for a date range/tab.
func (r *GoogleSheetRepository) GetTransactions(ctx context.Context, tabName string) ([]Transaction, error) {
	if r == nil {
		return nil, fmt.Errorf("repository is nil")
	}
	tabName = strings.TrimSpace(tabName)
	if tabName == "" {
		return nil, fmt.Errorf("tab name is required")
	}

	r.cacheMu.RLock()
	if r.txCache != nil {
		if txs, ok := r.txCache[tabName]; ok && time.Since(r.txCacheTime[tabName]) < 2*time.Minute {
			defer r.cacheMu.RUnlock()
			res := make([]Transaction, len(txs))
			copy(res, txs)
			return res, nil
		}
	}
	r.cacheMu.RUnlock()

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	if r.txCache != nil {
		if txs, ok := r.txCache[tabName]; ok && time.Since(r.txCacheTime[tabName]) < 2*time.Minute {
			res := make([]Transaction, len(txs))
			copy(res, txs)
			return res, nil
		}
	}

	readRange := fmt.Sprintf("'%s'!A2:G", tabName)
	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, readRange).
		ValueRenderOption("FORMATTED_VALUE").
		Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to read transactions from %s: %w", tabName, err)
	}

	out := []Transaction{}
	if resp != nil && len(resp.Values) > 0 {
		out = make([]Transaction, 0, len(resp.Values))
		for _, row := range resp.Values {
			tx, err := TransactionFromRow(row)
			if err != nil {
				log.Printf("⚠️ [SHEETS] Skipping row due to parse error: %v | Row: %v", err, row)
				continue
			}
			out = append(out, *tx)
		}
	}

	if r.txCache == nil {
		r.txCache = make(map[string][]Transaction)
		r.txCacheTime = make(map[string]time.Time)
	}
	r.txCache[tabName] = out
	r.txCacheTime[tabName] = time.Now()

	res := make([]Transaction, len(out))
	copy(res, out)
	return res, nil
}

// ClearRange deletes values in the specified A1 range.
func (r *GoogleSheetRepository) ClearRange(ctx context.Context, readRange string) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	_, err := r.service.Spreadsheets.Values.Clear(r.spreadsheetID, readRange, &sheets.ClearValuesRequest{}).Context(ctx).Do()
	return err
}

// GetTransactionByID finds a specific transaction in the monthly tab inferred from ID date.
func (r *GoogleSheetRepository) GetTransactionByID(ctx context.Context, id string) (*Transaction, int, string, error) {
	if r == nil {
		return nil, 0, "", fmt.Errorf("repository is nil")
	}
	id = strings.TrimSpace(id)
	if len(id) < 8 {
		return nil, 0, "", fmt.Errorf("invalid transaction id: %s", id)
	}

	datePart := id[:8]
	t, err := time.ParseInLocation("20060102", datePart, WIB)
	if err != nil {
		return nil, 0, "", fmt.Errorf("invalid transaction id date: %w", err)
	}
	tabName := TabNameForTime(t)

	txs, err := r.GetTransactions(ctx, tabName)
	if err != nil {
		return nil, 0, "", err
	}

	for i := range txs {
		if strings.TrimSpace(txs[i].ID) == id {
			tx := txs[i]
			return &tx, i + 2, tabName, nil
		}
	}

	return nil, 0, tabName, fmt.Errorf("transaction %s not found", id)
}

// UpdateTransaction updates a row at specific index.
func (r *GoogleSheetRepository) UpdateTransaction(ctx context.Context, tabName string, rowIndex int, tx *Transaction) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if strings.TrimSpace(tabName) == "" {
		return fmt.Errorf("tab name is required")
	}
	if rowIndex < 2 {
		return fmt.Errorf("invalid row index: %d", rowIndex)
	}
	if tx == nil {
		return fmt.Errorf("transaction is nil")
	}

	writeRange := fmt.Sprintf("'%s'!A%d:G%d", tabName, rowIndex, rowIndex)
	valueRange := &sheets.ValueRange{
		Values: [][]interface{}{tx.ToRow()},
	}

	_, err := r.service.Spreadsheets.Values.
		Update(r.spreadsheetID, writeRange, valueRange).
		ValueInputOption("USER_ENTERED").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to update transaction row %d on %s: %w", rowIndex, tabName, err)
	}

	r.cacheMu.Lock()
	if r.txCache != nil {
		delete(r.txCache, tabName)
		delete(r.txCacheTime, tabName)
	}
	r.cacheMu.Unlock()

	return nil
}

// DeleteTransaction removes a row.
func (r *GoogleSheetRepository) DeleteTransaction(ctx context.Context, tabName string, rowIndex int) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if strings.TrimSpace(tabName) == "" {
		return fmt.Errorf("tab name is required")
	}
	if rowIndex < 2 {
		return fmt.Errorf("invalid row index: %d", rowIndex)
	}

	sheetID, err := r.getSheetIDWithRefresh(ctx, tabName)
	if err != nil {
		return err
	}

	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				DeleteDimension: &sheets.DeleteDimensionRequest{
					Range: &sheets.DimensionRange{
						SheetId:    int64(sheetID),
						Dimension:  "ROWS",
						StartIndex: int64(rowIndex - 1),
						EndIndex:   int64(rowIndex),
					},
				},
			},
		},
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to delete transaction row %d on %s: %w", rowIndex, tabName, err)
	}

	r.cacheMu.Lock()
	if r.txCache != nil {
		delete(r.txCache, tabName)
		delete(r.txCacheTime, tabName)
	}
	r.cacheMu.Unlock()

	return nil
}

// AppendNote adds a note to the Notes tab.
func (r *GoogleSheetRepository) AppendNote(ctx context.Context, note *Note) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if note == nil {
		return fmt.Errorf("note is nil")
	}

	if err := r.EnsureTabExists(ctx, "Notes"); err != nil {
		return err
	}
	_ = r.ensureNotesHeader(ctx)

	valueRange := &sheets.ValueRange{
		Values: [][]interface{}{note.ToRow()},
	}

	_, err := r.service.Spreadsheets.Values.
		Append(r.spreadsheetID, "'Notes'!A:C", valueRange).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("OVERWRITE").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to append note: %w", err)
	}

	// Proactively trigger premium formatting
	_ = r.FormatFont(ctx, "Notes")

	return nil
}

func (r *GoogleSheetRepository) getBudgetsFromCache(ctx context.Context) (map[string]float64, error) {
	r.cacheMu.RLock()
	if r.budgetCache != nil && time.Since(r.budgetCacheTime) < 2*time.Minute {
		defer r.cacheMu.RUnlock()
		res := make(map[string]float64, len(r.budgetCache))
		for k, v := range r.budgetCache {
			res[k] = v
		}
		return res, nil
	}
	r.cacheMu.RUnlock()

	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()

	if r.budgetCache != nil && time.Since(r.budgetCacheTime) < 2*time.Minute {
		res := make(map[string]float64, len(r.budgetCache))
		for k, v := range r.budgetCache {
			res[k] = v
		}
		return res, nil
	}

	if err := r.EnsureTabExists(ctx, "Budget"); err != nil {
		return nil, err
	}
	_ = r.ensureBudgetHeader(ctx)

	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, "'Budget'!A2:B").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to read budget: %w", err)
	}

	cache := make(map[string]float64)
	if resp != nil {
		for _, row := range resp.Values {
			if len(row) < 2 {
				continue
			}
			cat := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
			if cat == "" {
				continue
			}
			amount, err := cellFloat64(row[1])
			if err != nil {
				continue
			}
			cache[strings.ToLower(cat)] = amount
		}
	}

	r.budgetCache = cache
	r.budgetCacheTime = time.Now()

	res := make(map[string]float64, len(cache))
	for k, v := range cache {
		res[k] = v
	}
	return res, nil
}

// GetBudget reads the budget for a category.
func (r *GoogleSheetRepository) GetBudget(ctx context.Context, category string) (float64, error) {
	if r == nil {
		return 0, fmt.Errorf("repository is nil")
	}

	cat := strings.TrimSpace(category)
	if cat == "" {
		return 0, fmt.Errorf("category is required")
	}

	budgets, err := r.getBudgetsFromCache(ctx)
	if err != nil {
		return 0, err
	}

	return budgets[strings.ToLower(cat)], nil
}

// SetBudget writes/updates budget for a category.
func (r *GoogleSheetRepository) SetBudget(ctx context.Context, category string, amount float64) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	cat := strings.TrimSpace(category)
	if cat == "" {
		return fmt.Errorf("category is required")
	}
	if amount <= 0 {
		return fmt.Errorf("amount must be greater than zero")
	}

	r.cacheMu.Lock()
	r.budgetCache = nil
	r.budgetCacheTime = time.Time{}
	r.cacheMu.Unlock()

	if err := r.EnsureTabExists(ctx, "Budget"); err != nil {
		return err
	}
	_ = r.ensureBudgetHeader(ctx)

	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, "'Budget'!A2:E").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to read budget tab: %w", err)
	}

	targetRow := -1
	if resp != nil {
		for i, row := range resp.Values {
			if len(row) == 0 {
				continue
			}
			if strings.EqualFold(strings.TrimSpace(fmt.Sprintf("%v", row[0])), cat) {
				targetRow = i + 2
				break
			}
		}
	}

	now := time.Now().In(WIB)
	tab := TabNameForTime(now)

	if targetRow > 0 {
		sumFormula := fmt.Sprintf(`=SUMIFS('%s'!G:G,'%s'!D:D,"Pengeluaran",'%s'!E:E,A%d)`, tab, tab, tab, targetRow)
		sisaFormula := fmt.Sprintf("=B%d-C%d", targetRow, targetRow)
		statusFormula := fmt.Sprintf(`=IF(D%d<0,"🚨 Over Budget",IF(D%d<B%d*0.2,"⚠️ Hampir Habis","✅ Aman"))`, targetRow, targetRow, targetRow)

		writeRange := fmt.Sprintf("'Budget'!A%d:E%d", targetRow, targetRow)
		values := &sheets.ValueRange{
			Values: [][]interface{}{
				{cat, amount, sumFormula, sisaFormula, statusFormula},
			},
		}
		_, err = r.service.Spreadsheets.Values.Update(r.spreadsheetID, writeRange, values).
			ValueInputOption("USER_ENTERED").
			Context(ctx).
			Do()
		if err != nil {
			return fmt.Errorf("failed to update budget row: %w", err)
		}
		// Proactively trigger premium formatting
		_ = r.FormatFont(ctx, "Budget")
		return nil
	}

	// Figure out next row for formula references.
	nextRow := 2
	if resp != nil {
		nextRow = len(resp.Values) + 2
	}
	sumFormula := fmt.Sprintf(`=SUMIFS('%s'!G:G,'%s'!D:D,"Pengeluaran",'%s'!E:E,A%d)`, tab, tab, tab, nextRow)
	sisaFormula := fmt.Sprintf("=B%d-C%d", nextRow, nextRow)
	statusFormula := fmt.Sprintf(`=IF(D%d<0,"🚨 Over Budget",IF(D%d<B%d*0.2,"⚠️ Hampir Habis","✅ Aman"))`, nextRow, nextRow, nextRow)

	values := &sheets.ValueRange{
		Values: [][]interface{}{
			{cat, amount, sumFormula, sisaFormula, statusFormula},
		},
	}
	_, err = r.service.Spreadsheets.Values.
		Append(r.spreadsheetID, "'Budget'!A:E", values).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("OVERWRITE").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to append budget row: %w", err)
	}

	// Proactively trigger premium formatting
	_ = r.FormatFont(ctx, "Budget")

	return nil
}

// GetCategoryTotal sums amounts for a category in current month/tab.
func (r *GoogleSheetRepository) GetCategoryTotal(ctx context.Context, tabName string, category string) (float64, error) {
	if r == nil {
		return 0, fmt.Errorf("repository is nil")
	}
	tabName = strings.TrimSpace(tabName)
	category = strings.TrimSpace(category)
	if tabName == "" {
		return 0, fmt.Errorf("tab name is required")
	}
	if category == "" {
		return 0, fmt.Errorf("category is required")
	}

	txs, err := r.GetTransactions(ctx, tabName)
	if err != nil {
		return 0, err
	}

	var total float64
	for _, tx := range txs {
		if tx.Type != Expense {
			continue
		}
		if !strings.EqualFold(tx.Category, category) {
			continue
		}
		total += tx.Amount
	}

	return total, nil
}

func (r *GoogleSheetRepository) getLatestMonthlyTabID() (int, bool) {
	var maxYear int
	var maxMonth time.Month
	var latestID int
	var found bool

	for name, id := range r.tabManager.GetExistingTabs() {
		if isMonthlyTabName(name) {
			parts := strings.Fields(name)
			year, _ := strconv.Atoi(parts[1])
			
			var m time.Month
			for mon, monName := range monthNamesID {
				if strings.EqualFold(parts[0], monName) {
					m = mon
					break
				}
			}
			
			if year > maxYear || (year == maxYear && m > maxMonth) {
				maxYear = year
				maxMonth = m
				latestID = id
				found = true
			}
		}
	}
	return latestID, found
}

// EnsureTabExists creates or copies tab if it doesn't exist.
func (r *GoogleSheetRepository) EnsureTabExists(ctx context.Context, tabName string) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if r.tabManager == nil {
		return fmt.Errorf("tab manager is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, tabName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if isMonthlyTabName(tabName) {
		latestID, found := r.getLatestMonthlyTabID()
		if found {
			req := &sheets.BatchUpdateSpreadsheetRequest{
				Requests: []*sheets.Request{
					{
						DuplicateSheet: &sheets.DuplicateSheetRequest{
							SourceSheetId: int64(latestID),
							NewSheetName:  tabName,
						},
					},
				},
			}
			_, err := r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
			if err != nil {
				return fmt.Errorf("failed to duplicate sheet %s: %w", tabName, err)
			}

			_ = r.tabManager.RefreshCache(ctx)

			// Clear the duplicated data in rows 2+
			clearRange := fmt.Sprintf("'%s'!A2:Z1000", tabName)
			_, _ = r.service.Spreadsheets.Values.Clear(r.spreadsheetID, clearRange, &sheets.ClearValuesRequest{}).Context(ctx).Do()

			// Reset formatting to ensure clean slate without manual background color inheritance
			_ = r.FormatFont(ctx, tabName)

			// Reorder tabs in background so current month stays near front.
			go func() { _ = r.ReorderTabs(context.Background()) }()
			return nil
		}
	}

	if err := r.tabManager.CreateBlankTab(ctx, tabName); err != nil {
		return err
	}

	switch tabName {
	case "Budget":
		return r.ensureBudgetHeader(ctx)
	case "Notes":
		return r.ensureNotesHeader(ctx)
	case "Dashboard":
		return nil
	case ReminderSheetName:
		return r.ensureReminderHeader(ctx)
	default:
		if isMonthlyTabName(tabName) {
			if err := r.ensureMonthlyHeader(ctx, tabName); err != nil {
				return err
			}
			// Reorder tabs in background so new month tab appears in position 2.
			go func() { _ = r.ReorderTabs(context.Background()) }()
		}
	}

	return nil
}

// FormatHeaders applies header formatting to a tab.
func (r *GoogleSheetRepository) FormatHeaders(ctx context.Context, tabName string) error {
	sheetID, err := r.getSheetIDWithRefresh(ctx, tabName)
	if err != nil {
		return err
	}

	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId:          int64(sheetID),
						StartRowIndex:    0,
						EndRowIndex:      1,
						StartColumnIndex: 0,
						EndColumnIndex:   7,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							BackgroundColor: &sheets.Color{
								Red:   0.263,
								Green: 0.263,
								Blue:  0.263,
							},
							TextFormat: &sheets.TextFormat{
								Bold:       true,
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{
									Red:   1,
									Green: 1,
									Blue:  1,
								},
							},
							HorizontalAlignment: "CENTER",
							VerticalAlignment:   "MIDDLE",
							Borders: &sheets.Borders{
								Top:    &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Bottom: &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Left:   &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Right:  &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
							},
						},
					},
					Fields: "userEnteredFormat(backgroundColor,textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
		},
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to format headers on tab %s: %w", tabName, err)
	}
	return nil
}

// FormatRow is deprecated and returns nil to preserve user manual styles.
func (r *GoogleSheetRepository) FormatRow(ctx context.Context, tabName string, rowIndex int, isExpense bool) error {
	return nil
}

func colWidthReq(sheetID int, startCol, endCol int, width int) *sheets.Request {
	return &sheets.Request{
		UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
			Properties: &sheets.DimensionProperties{PixelSize: int64(width)},
			Fields:     "pixelSize",
			Range: &sheets.DimensionRange{
				SheetId: int64(sheetID), Dimension: "COLUMNS", StartIndex: int64(startCol), EndIndex: int64(endCol),
			},
		},
	}
}

func rowHeightReq(sheetID int, startRow, endRow int, height int) *sheets.Request {
	return &sheets.Request{
		UpdateDimensionProperties: &sheets.UpdateDimensionPropertiesRequest{
			Properties: &sheets.DimensionProperties{PixelSize: int64(height)},
			Fields:     "pixelSize",
			Range: &sheets.DimensionRange{
				SheetId: int64(sheetID), Dimension: "ROWS", StartIndex: int64(startRow), EndIndex: int64(endRow),
			},
		},
	}
}

// zebraBandingReq returns an AddBanding request with alternating colors senada dengan warna title.
//   Header row  → Biru gelap #1C4587
//   Baris ganjil → Putih #FFFFFF
//   Baris genap  → Biru muda #C9DAF8  (jelas terlihat, senada header)
//
// startCol/endCol are 0-indexed. EndRowIndex is omitted so it covers all rows infinitely.
func zebraBandingReq(sheetID, startCol, endCol int) *sheets.Request {
	return &sheets.Request{
		AddBanding: &sheets.AddBandingRequest{
			BandedRange: &sheets.BandedRange{
				Range: &sheets.GridRange{
					SheetId:          int64(sheetID),
					StartRowIndex:    0,
					StartColumnIndex: int64(startCol),
					EndColumnIndex:   int64(endCol),
				},
				RowProperties: &sheets.BandingProperties{
					HeaderColor:     &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263}, // #434343 abu gelap (header)
					FirstBandColor:  &sheets.Color{Red: 1.0, Green: 1.0, Blue: 1.0},        // #FFFFFF putih (baris ganjil)
					SecondBandColor: &sheets.Color{Red: 0.965, Green: 0.973, Blue: 0.976},  // #F6F8F9 abu kebiruan muda (baris genap)
				},
			},
		},
	}
}

// InitDashboard creates/updates Dashboard tab with comprehensive analytics.
func (r *GoogleSheetRepository) InitDashboard(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, "Dashboard")
	if err != nil {
		return err
	}
	if !exists {
		if err := r.EnsureTabExists(ctx, "Dashboard"); err != nil {
			return err
		}
	}

	now := time.Now().In(WIB)
	year := now.Year()
	currentTab := TabNameForTime(now)

	// Generate ordered month names for the current year.
	monthOrder := []time.Month{
		time.January, time.February, time.March, time.April,
		time.May, time.June, time.July, time.August,
		time.September, time.October, time.November, time.December,
	}

	// Helper: build sum formula across all 12 months of current year.
	allMonthsSum := func(condition string) string {
		parts := make([]string, 0, 12)
		for _, m := range monthOrder {
			tab := fmt.Sprintf("%s %d", monthNamesID[m], year)
			parts = append(parts, fmt.Sprintf("IFERROR(SUMIF('%s'!D:D,\"%s\",'%s'!G:G),0)", tab, condition, tab))
		}
		return "=" + strings.Join(parts, "+")
	}

	allMonthsCount := func() string {
		parts := make([]string, 0, 12)
		for _, m := range monthOrder {
			tab := fmt.Sprintf("%s %d", monthNamesID[m], year)
			parts = append(parts, fmt.Sprintf("IF(ISERR(INDIRECT(\"'%s'!A2\")),0,COUNTIF(INDIRECT(\"'%s'!A2:A\"),\"<>\"))", tab, tab))
		}
		return "=" + strings.Join(parts, "+")
	}

	// ─── SECTION 1: Ringkasan Keseluruhan (All-Time Year) ───
	rows := [][]interface{}{
		{fmt.Sprintf("📊 FINANCIAL TRACKER %d", year), "", "", "", ""}, // row 1
		{"", "", "", "", ""},                                             // row 2
		{"📋 RINGKASAN KESELURUHAN", "", "", "", ""},                    // row 3
		{"💵 Total Pemasukan", allMonthsSum("Pemasukan"), "", "💸 Total Pengeluaran", allMonthsSum("Pengeluaran")}, // row 4
		{"💰 Saldo Bersih", "=B4-E4", "", "📈 Jumlah Transaksi", allMonthsCount()},                              // row 5
		{"📊 Rasio Tabungan", `=IF(B4>0,(B4-E4)/B4*100,0)`, "", "", ""},                                          // row 6
		{"", "", "", "", ""},                                                                                       // row 7
	}

	// ─── SECTION 2: Ringkasan Bulan Ini ───
	currentMonthName := monthNamesID[now.Month()]
	rows = append(rows,
		[]interface{}{fmt.Sprintf("📅 RINGKASAN %s %d", strings.ToUpper(currentMonthName), year), "", "", "", ""}, // row 8
		[]interface{}{
			"💵 Pemasukan",
			fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pemasukan\",'%s'!G:G),0)", currentTab, currentTab),
			"",
			"💸 Pengeluaran",
			fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pengeluaran\",'%s'!G:G),0)", currentTab, currentTab),
		}, // row 9
		[]interface{}{
			"💰 Saldo Bulan Ini",
			"=B9-E9",
			"",
			"📈 Transaksi",
			fmt.Sprintf("=IF(ISERR(INDIRECT(\"'%s'!A2\")),0,COUNTIF(INDIRECT(\"'%s'!A2:A\"),\"<>\"))", currentTab, currentTab),
		}, // row 10
		[]interface{}{"📊 Rata-rata Pengeluaran/Hari", fmt.Sprintf("=IF(E10>0,E9/DAY(TODAY()),0)"), "", "", ""}, // row 11
		[]interface{}{"", "", "", "", ""},                                                                         // row 12
	)

	// ─── SECTION 3: Tren Bulanan ───
	rows = append(rows,
		[]interface{}{"📈 TREN BULANAN", "", "", "", ""},                         // row 13
		[]interface{}{"Bulan", "Pemasukan", "Pengeluaran", "Saldo Bersih", "Transaksi"}, // row 14
	)

	trendStartRow := 15
	for i, m := range monthOrder {
		tab := fmt.Sprintf("%s %d", monthNamesID[m], year)
		rowNum := trendStartRow + i
		incomeF := fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pemasukan\",'%s'!G:G),0)", tab, tab)
		expenseF := fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pengeluaran\",'%s'!G:G),0)", tab, tab)
		netF := fmt.Sprintf("=B%d-C%d", rowNum, rowNum)
		countF := fmt.Sprintf("=IF(ISERR(INDIRECT(\"'%s'!A2\")),0,COUNTIF(INDIRECT(\"'%s'!A2:A\"),\"<>\"))", tab, tab)
		rows = append(rows, []interface{}{monthNamesID[m], incomeF, expenseF, netF, countF})
	}
	trendEndRow := trendStartRow + 12 // row 27 (exclusive)

	// Total row for trend
	rows = append(rows, []interface{}{
		"TOTAL",
		fmt.Sprintf("=SUM(B%d:B%d)", trendStartRow, trendEndRow-1),
		fmt.Sprintf("=SUM(C%d:C%d)", trendStartRow, trendEndRow-1),
		fmt.Sprintf("=SUM(D%d:D%d)", trendStartRow, trendEndRow-1),
		fmt.Sprintf("=SUM(E%d:E%d)", trendStartRow, trendEndRow-1),
	}) // row 27
	rows = append(rows, []interface{}{"", "", "", "", ""}) // row 28

	// ─── SECTION 4: Pengeluaran per Kategori (All-Time) ───
	rows = append(rows,
		[]interface{}{"📂 PENGELUARAN PER KATEGORI (ALL-TIME)", "", "", "", ""}, // row 29
		[]interface{}{"Kategori", "Total (All-Time)", "% dari Total", fmt.Sprintf("Bulan Ini (%s)", currentMonthName), "% Bulan Ini"}, // row 30
	)
	catDataStart := 31

	categories := []string{
		"Makanan", "Transportasi", "Rumah Tangga", "Belanja",
		"Kesehatan", "Pendidikan", "Hiburan", "Fashion",
		"Komunikasi", "Perawatan", "Sosial", "Lainnya",
	}

	for i, cat := range categories {
		rowNum := catDataStart + i
		// All-time sum per category
		catParts := make([]string, 0, 12)
		for _, m := range monthOrder {
			tab := fmt.Sprintf("%s %d", monthNamesID[m], year)
			catParts = append(catParts, fmt.Sprintf("IFERROR(SUMIFS('%s'!G:G,'%s'!D:D,\"Pengeluaran\",'%s'!E:E,\"%s\"),0)", tab, tab, tab, cat))
		}
		allTimeCatF := "=" + strings.Join(catParts, "+")
		pctAllF := fmt.Sprintf(`=IF(E4>0,B%d/E4*100,0)`, rowNum)
		monthCatF := fmt.Sprintf("=IFERROR(SUMIFS('%s'!G:G,'%s'!D:D,\"Pengeluaran\",'%s'!E:E,\"%s\"),0)", currentTab, currentTab, currentTab, cat)
		pctMonthF := fmt.Sprintf(`=IF(E9>0,D%d/E9*100,0)`, rowNum)
		rows = append(rows, []interface{}{cat, allTimeCatF, pctAllF, monthCatF, pctMonthF})
	}
	catDataEnd := catDataStart + len(categories) // row 43

	// Category totals
	rows = append(rows, []interface{}{
		"TOTAL",
		fmt.Sprintf("=SUM(B%d:B%d)", catDataStart, catDataEnd-1),
		"",
		fmt.Sprintf("=SUM(D%d:D%d)", catDataStart, catDataEnd-1),
		"",
	}) // row 43

	// Write all data.
	totalRows := len(rows)
	writeRange := fmt.Sprintf("'Dashboard'!A1:E%d", totalRows)
	values := &sheets.ValueRange{Values: rows}

	_, err = r.service.Spreadsheets.Values.
		Update(r.spreadsheetID, writeRange, values).
		ValueInputOption("USER_ENTERED").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to initialize Dashboard values: %w", err)
	}

	sheetID, err := r.getSheetIDWithRefresh(ctx, "Dashboard")
	if err != nil {
		return err
	}

	// ─── Formatting ───
	headerBG := &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263}
	sectionBG := &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263}
	whiteFG := &sheets.Color{Red: 1, Green: 1, Blue: 1}
	lightGray := &sheets.Color{Red: 0.949, Green: 0.949, Blue: 0.949} // #F2F2F2

	// Unmerge existing merges first.
	r.unmergeAll(ctx, sheetID)

	sectionTitleStyle := func(row int) *sheets.Request {
		return &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(row - 1), EndRowIndex: int64(row),
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: sectionBG,
						TextFormat:      &sheets.TextFormat{Bold: true, FontSize: 12, ForegroundColor: whiteFG},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		}
	}

	tableHeaderStyle := func(row int) *sheets.Request {
		return &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(row - 1), EndRowIndex: int64(row),
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: headerBG,
						TextFormat:      &sheets.TextFormat{Bold: true, ForegroundColor: whiteFG},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		}
	}

	numberFormatRange := func(startRow, endRow, startCol, endCol int) *sheets.Request {
		return &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(startRow - 1), EndRowIndex: int64(endRow),
					StartColumnIndex: int64(startCol), EndColumnIndex: int64(endCol),
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat:        &sheets.NumberFormat{Type: "NUMBER", Pattern: "\"Rp\" #,##0"},
						HorizontalAlignment: "RIGHT",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		}
	}

	countFormatRange := func(startRow, endRow, startCol, endCol int) *sheets.Request {
		return &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(startRow - 1), EndRowIndex: int64(endRow),
					StartColumnIndex: int64(startCol), EndColumnIndex: int64(endCol),
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat:        &sheets.NumberFormat{Type: "NUMBER", Pattern: "#,##0"},
						HorizontalAlignment: "RIGHT",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		}
	}

	pctFormatRange := func(startRow, endRow, col int) *sheets.Request {
		return &sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(startRow - 1), EndRowIndex: int64(endRow),
					StartColumnIndex: int64(col), EndColumnIndex: int64(col + 1),
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						NumberFormat:        &sheets.NumberFormat{Type: "NUMBER", Pattern: `#,##0.0"%"`},
						HorizontalAlignment: "RIGHT",
					},
				},
				Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
			},
		}
	}

	formatReqs := []*sheets.Request{
		{
			UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
				Properties: &sheets.SheetProperties{
					SheetId:        int64(sheetID),
					GridProperties: &sheets.GridProperties{
						FrozenRowCount: 1,
						ColumnCount:    12,
						RowCount:       66,
					},
				},
				Fields: "gridProperties(frozenRowCount,columnCount,rowCount)",
			},
		},
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 1,
					StartColumnIndex: 0, EndColumnIndex: 12,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						TextFormat: &sheets.TextFormat{
							FontFamily: "Roboto",
							FontSize:   10,
							ForegroundColor: &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263},
						},
					},
				},
				Fields: "userEnteredFormat.textFormat",
			},
		},
		// Title row (row 1): merge + style
		{
			MergeCells: &sheets.MergeCellsRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 0, EndRowIndex: 1,
					StartColumnIndex: 0, EndColumnIndex: 12,
				},
				MergeType: "MERGE_ALL",
			},
		},
		{
			MergeCells: &sheets.MergeCellsRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 1, EndRowIndex: 66,
					StartColumnIndex: 5, EndColumnIndex: 12,
				},
				MergeType: "MERGE_ALL",
			},
		},
		{
			MergeCells: &sheets.MergeCellsRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 46, EndRowIndex: 48,
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				MergeType: "MERGE_ALL",
			},
		},
		{
			MergeCells: &sheets.MergeCellsRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 49, EndRowIndex: 66,
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				MergeType: "MERGE_ALL",
			},
		},
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 0, EndRowIndex: 1,
					StartColumnIndex: 0, EndColumnIndex: 12,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor:     headerBG,
						TextFormat:          &sheets.TextFormat{Bold: true, FontFamily: "Arial", FontSize: 16, ForegroundColor: whiteFG},
						HorizontalAlignment: "CENTER",
						VerticalAlignment:   "MIDDLE",
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat,horizontalAlignment,verticalAlignment)",
			},
		},

		// Section titles
		sectionTitleStyle(3),  // Ringkasan Keseluruhan
		sectionTitleStyle(8),  // Ringkasan Bulan Ini
		sectionTitleStyle(13), // Tren Bulanan
		sectionTitleStyle(29), // Pengeluaran per Kategori

		// Summary sections (rows 4-6, 9-11): light grey bg
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 3, EndRowIndex: 6,
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: lightGray,
						TextFormat:      &sheets.TextFormat{Bold: true, FontSize: 11},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		},
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: 8, EndRowIndex: 11,
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: lightGray,
						TextFormat:      &sheets.TextFormat{Bold: true, FontSize: 11},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		},

		// Number format for summary values
		numberFormatRange(4, 5, 1, 2),  // B4:B5 (Pemasukan, Saldo Bersih)
		pctFormatRange(6, 6, 1),        // B6 (Rasio)
		numberFormatRange(4, 4, 4, 5),  // E4 (Total Pengeluaran)
		countFormatRange(5, 5, 4, 5),   // E5 (Jumlah Transaksi)
		
		numberFormatRange(9, 11, 1, 2), // B9:B11 (Pemasukan, Saldo, Rata-rata)
		numberFormatRange(9, 9, 4, 5),   // E9 (Pengeluaran)
		countFormatRange(10, 10, 4, 5),  // E10 (Transaksi)

		// Trend table header (row 14)
		tableHeaderStyle(14),

		// Trend data number format (rows 15-27, cols B-D)
		numberFormatRange(trendStartRow, trendEndRow, 1, 4), // B-D
		countFormatRange(trendStartRow, trendEndRow, 4, 5),  // E (count)

		// Trend total row bold
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(trendEndRow - 1), EndRowIndex: int64(trendEndRow),
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: lightGray,
						TextFormat:      &sheets.TextFormat{Bold: true},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		},

		// Trend zebra striping
		{
			AddBanding: &sheets.AddBandingRequest{
				BandedRange: &sheets.BandedRange{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 13, EndRowIndex: int64(trendEndRow),
						StartColumnIndex: 0, EndColumnIndex: 5,
					},
					RowProperties: &sheets.BandingProperties{
						HeaderColor:     headerBG,
						FirstBandColor:  &sheets.Color{Red: 1, Green: 1, Blue: 1},
						SecondBandColor: lightGray,
					},
				},
			},
		},

		// Category header (row 30)
		tableHeaderStyle(30),

		// Category data number format
		numberFormatRange(catDataStart, catDataEnd, 1, 2), // B (all-time total)
		pctFormatRange(catDataStart, catDataEnd-1, 2),     // C (% all-time)
		numberFormatRange(catDataStart, catDataEnd, 3, 4), // D (month total)
		pctFormatRange(catDataStart, catDataEnd-1, 4),     // E (% month)

		// Category total row bold
		&sheets.Request{
			RepeatCell: &sheets.RepeatCellRequest{
				Range: &sheets.GridRange{
					SheetId: int64(sheetID), StartRowIndex: int64(catDataEnd - 1), EndRowIndex: int64(catDataEnd),
					StartColumnIndex: 0, EndColumnIndex: 5,
				},
				Cell: &sheets.CellData{
					UserEnteredFormat: &sheets.CellFormat{
						BackgroundColor: lightGray,
						TextFormat:      &sheets.TextFormat{Bold: true},
					},
				},
				Fields: "userEnteredFormat(backgroundColor,textFormat)",
			},
		},

		// Category zebra striping
		{
			AddBanding: &sheets.AddBandingRequest{
				BandedRange: &sheets.BandedRange{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 29, EndRowIndex: int64(catDataEnd),
						StartColumnIndex: 0, EndColumnIndex: 5,
					},
					RowProperties: &sheets.BandingProperties{
						HeaderColor:     headerBG,
						FirstBandColor:  &sheets.Color{Red: 1, Green: 1, Blue: 1},
						SecondBandColor: lightGray,
					},
				},
			},
		},

		// Freeze row 1
		{
			UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
				Properties: &sheets.SheetProperties{
					SheetId:        int64(sheetID),
					GridProperties: &sheets.GridProperties{FrozenRowCount: 1},
				},
				Fields: "gridProperties.frozenRowCount",
			},
		},
		// Fixed dimensions matching template
		rowHeightReq(sheetID, 0, 1, 47),
		colWidthReq(sheetID, 0, 1, 322),
		colWidthReq(sheetID, 1, 2, 148),
		colWidthReq(sheetID, 2, 3, 160),
		colWidthReq(sheetID, 3, 4, 172),
		colWidthReq(sheetID, 4, 5, 123),
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
		&sheets.BatchUpdateSpreadsheetRequest{Requests: formatReqs}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to format Dashboard tab: %w", err)
	}

	// Add embedded charts (non-fatal if fails).
	_ = r.addDashboardCharts(ctx, sheetID, trendStartRow, trendEndRow, catDataStart, catDataEnd)

	return nil
}

// unmergeAll removes all cell merges from a sheet.
func (r *GoogleSheetRepository) unmergeAll(ctx context.Context, sheetID int) {
	sp, _ := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if sp == nil {
		return
	}
	for _, sh := range sp.Sheets {
		if sh == nil || sh.Properties == nil || int(sh.Properties.SheetId) != sheetID {
			continue
		}
		if len(sh.Merges) > 0 {
			var reqs []*sheets.Request
			for _, merge := range sh.Merges {
				reqs = append(reqs, &sheets.Request{
					UnmergeCells: &sheets.UnmergeCellsRequest{Range: merge},
				})
			}
			_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
				&sheets.BatchUpdateSpreadsheetRequest{Requests: reqs}).Context(ctx).Do()
		}
		// Also remove existing banded ranges.
		if len(sh.BandedRanges) > 0 {
			var reqs []*sheets.Request
			for _, br := range sh.BandedRanges {
				reqs = append(reqs, &sheets.Request{
					DeleteBanding: &sheets.DeleteBandingRequest{BandedRangeId: br.BandedRangeId},
				})
			}
			_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
				&sheets.BatchUpdateSpreadsheetRequest{Requests: reqs}).Context(ctx).Do()
		}
	}
}

// addDashboardCharts adds 5 embedded charts for comprehensive analytics.
func (r *GoogleSheetRepository) addDashboardCharts(ctx context.Context, sheetID, trendStart, trendEnd, catStart, catEnd int) error {
	// Remove existing charts first.
	sp, err := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return err
	}

	var removeReqs []*sheets.Request
	for _, sh := range sp.Sheets {
		if sh == nil || sh.Properties == nil || int(sh.Properties.SheetId) != sheetID {
			continue
		}
		for _, chart := range sh.Charts {
			if chart == nil {
				continue
			}
			removeReqs = append(removeReqs, &sheets.Request{
				DeleteEmbeddedObject: &sheets.DeleteEmbeddedObjectRequest{
					ObjectId: chart.ChartId,
				},
			})
		}
	}
	if len(removeReqs) > 0 {
		_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
			&sheets.BatchUpdateSpreadsheetRequest{Requests: removeReqs}).Context(ctx).Do()
	}

	sid := int64(sheetID)
	ts := int64(trendStart - 1) // 0-indexed start for trend data
	te := int64(trendEnd - 1)   // 0-indexed end (exclude total row)
	cs := int64(catStart - 1)   // 0-indexed start for category data
	ce := int64(catEnd - 1)     // 0-indexed end (exclude total row)

	// Helper to create a grid range on the Dashboard
	gRange := func(startRow, endRow int64, startCol, endCol int64) *sheets.GridRange {
		return &sheets.GridRange{
			SheetId: sid, StartRowIndex: startRow, EndRowIndex: endRow,
			StartColumnIndex: startCol, EndColumnIndex: endCol,
		}
	}

	chartData := func(startRow, endRow, startCol, endCol int64) *sheets.ChartData {
		return &sheets.ChartData{
			SourceRange: &sheets.ChartSourceRange{
				Sources: []*sheets.GridRange{gRange(startRow, endRow, startCol, endCol)},
			},
		}
	}

	chartReqs := []*sheets.Request{
		// ───── Chart 1: Column chart — Monthly Income vs Expense ─────
		{
			AddChart: &sheets.AddChartRequest{
				Chart: &sheets.EmbeddedChart{
					Position: &sheets.EmbeddedObjectPosition{
						OverlayPosition: &sheets.OverlayPosition{
							AnchorCell:   &sheets.GridCoordinate{SheetId: sid, RowIndex: 1, ColumnIndex: 5},
							OffsetXPixels: 11, OffsetYPixels: 8,
							WidthPixels:  696, HeightPixels: 330,
						},
					},
					Spec: &sheets.ChartSpec{
						Title: "Tren Pemasukan vs Pengeluaran Bulanan", FontName: "Roboto",
						BasicChart: &sheets.BasicChartSpec{
							ChartType: "COLUMN", LegendPosition: "BOTTOM_LEGEND", HeaderCount: 0,
							Domains: []*sheets.BasicChartDomain{{Domain: chartData(ts, te, 0, 1)}},
							Series: []*sheets.BasicChartSeries{
								{Series: chartData(ts, te, 1, 2), Color: &sheets.Color{Red: 0.18, Green: 0.69, Blue: 0.23}},
								{Series: chartData(ts, te, 2, 3), Color: &sheets.Color{Red: 0.85, Green: 0.20, Blue: 0.20}},
							},
							Axis: []*sheets.BasicChartAxis{
								{Position: "BOTTOM_AXIS", Title: "Bulan"},
								{Position: "LEFT_AXIS", Title: "Rupiah"},
							},
						},
					},
				},
			},
		},

		// ───── Chart 2: Line chart — Monthly Net Balance Trend ─────
		{
			AddChart: &sheets.AddChartRequest{
				Chart: &sheets.EmbeddedChart{
					Position: &sheets.EmbeddedObjectPosition{
						OverlayPosition: &sheets.OverlayPosition{
							AnchorCell:   &sheets.GridCoordinate{SheetId: sid, RowIndex: 16, ColumnIndex: 5},
							OffsetXPixels: 11, OffsetYPixels: 2,
							WidthPixels:  696, HeightPixels: 330,
						},
					},
					Spec: &sheets.ChartSpec{
						Title: "Tren Saldo Bersih Bulanan", FontName: "Roboto",
						BasicChart: &sheets.BasicChartSpec{
							ChartType: "LINE", LegendPosition: "BOTTOM_LEGEND", HeaderCount: 0,
							Domains: []*sheets.BasicChartDomain{{Domain: chartData(ts, te, 0, 1)}},
							Series: []*sheets.BasicChartSeries{
								{Series: chartData(ts, te, 3, 4), Color: &sheets.Color{Red: 0.27, Green: 0.55, Blue: 0.84}},
							},
							Axis: []*sheets.BasicChartAxis{
								{Position: "BOTTOM_AXIS", Title: "Bulan"},
								{Position: "LEFT_AXIS", Title: "Rupiah"},
							},
						},
					},
				},
			},
		},

		// ───── Chart 3: Donut chart — Category Distribution (All-Time) ─────
		{
			AddChart: &sheets.AddChartRequest{
				Chart: &sheets.EmbeddedChart{
					Position: &sheets.EmbeddedObjectPosition{
						OverlayPosition: &sheets.OverlayPosition{
							AnchorCell:   &sheets.GridCoordinate{SheetId: sid, RowIndex: 31, ColumnIndex: 5},
							OffsetXPixels: 11, OffsetYPixels: 17,
							WidthPixels:  696, HeightPixels: 350,
						},
					},
					Spec: &sheets.ChartSpec{
						Title: "Distribusi Pengeluaran per Kategori (All-Time)", FontName: "Roboto",
						PieChart: &sheets.PieChartSpec{
							LegendPosition: "RIGHT_LEGEND", PieHole: 0.45,
							Domain: chartData(cs, ce, 0, 1),
							Series: chartData(cs, ce, 1, 2),
						},
					},
				},
			},
		},

		// ───── Chart 4: Bar chart — Top Kategori Bulan Ini ─────
		{
			AddChart: &sheets.AddChartRequest{
				Chart: &sheets.EmbeddedChart{
					Position: &sheets.EmbeddedObjectPosition{
						OverlayPosition: &sheets.OverlayPosition{
							AnchorCell:   &sheets.GridCoordinate{SheetId: sid, RowIndex: 48, ColumnIndex: 4},
							OffsetXPixels: 20, OffsetYPixels: 14,
							WidthPixels:  811, HeightPixels: 350,
						},
					},
					Spec: &sheets.ChartSpec{
						Title: "Pengeluaran per Kategori (Bulan Ini)", FontName: "Roboto",
						BasicChart: &sheets.BasicChartSpec{
							ChartType: "BAR", LegendPosition: "NO_LEGEND", HeaderCount: 0,
							Domains: []*sheets.BasicChartDomain{{Domain: chartData(cs, ce, 0, 1)}},
							Series: []*sheets.BasicChartSeries{
								{Series: chartData(cs, ce, 3, 4), Color: &sheets.Color{Red: 0.95, Green: 0.55, Blue: 0.15}},
							},
							Axis: []*sheets.BasicChartAxis{
								{Position: "BOTTOM_AXIS", Title: "Rupiah"},
								{Position: "LEFT_AXIS", Title: ""},
							},
						},
					},
				},
			},
		},

		// ───── Chart 5: Area chart — Jumlah Transaksi per Bulan ─────
		{
			AddChart: &sheets.AddChartRequest{
				Chart: &sheets.EmbeddedChart{
					Position: &sheets.EmbeddedObjectPosition{
						OverlayPosition: &sheets.OverlayPosition{
							AnchorCell:   &sheets.GridCoordinate{SheetId: sid, RowIndex: 48, ColumnIndex: 0},
							OffsetXPixels: 0, OffsetYPixels: 14,
							WidthPixels:  811, HeightPixels: 350,
						},
					},
					Spec: &sheets.ChartSpec{
						Title: "Jumlah Transaksi per Bulan", FontName: "Roboto",
						BasicChart: &sheets.BasicChartSpec{
							ChartType: "AREA", LegendPosition: "NO_LEGEND", HeaderCount: 0,
							Domains: []*sheets.BasicChartDomain{{Domain: chartData(ts, te, 0, 1)}},
							Series: []*sheets.BasicChartSeries{
								{Series: chartData(ts, te, 4, 5), Color: &sheets.Color{Red: 0.55, Green: 0.34, Blue: 0.78}},
							},
							Axis: []*sheets.BasicChartAxis{
								{Position: "BOTTOM_AXIS", Title: "Bulan"},
								{Position: "LEFT_AXIS", Title: "Transaksi"},
							},
						},
					},
				},
			},
		},
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
		&sheets.BatchUpdateSpreadsheetRequest{Requests: chartReqs}).Context(ctx).Do()
	return err
}

// InitBudgetTab creates Budget tab structure with auto-calculating formulas.
func (r *GoogleSheetRepository) InitBudgetTab(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, "Budget")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if err := r.EnsureTabExists(ctx, "Budget"); err != nil {
		return err
	}
	if err := r.ensureBudgetHeader(ctx); err != nil {
		return err
	}

	if err := r.formatHeaderRow(ctx, "Budget", 5); err != nil {
		return err
	}

	sheetID, err := r.getSheetIDWithRefresh(ctx, "Budget")
	if err != nil {
		return err
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
		&sheets.BatchUpdateSpreadsheetRequest{Requests: []*sheets.Request{
			{
				UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
					Properties: &sheets.SheetProperties{
						SheetId:        int64(sheetID),
						GridProperties: &sheets.GridProperties{
							FrozenRowCount: 1,
							ColumnCount:    5,
						},
					},
					Fields: "gridProperties(frozenRowCount,columnCount)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1,
						StartColumnIndex: 0, EndColumnIndex: 5,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							TextFormat: &sheets.TextFormat{
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263},
							},
							HorizontalAlignment: "LEFT",
							VerticalAlignment:   "MIDDLE",
							Borders:             &sheets.Borders{},
						},
					},
					Fields: "userEnteredFormat(textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1, EndRowIndex: 100,
						StartColumnIndex: 1, EndColumnIndex: 4,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							NumberFormat:        &sheets.NumberFormat{Type: "NUMBER", Pattern: "\"Rp\" #,##0"},
							HorizontalAlignment: "RIGHT",
						},
					},
					Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
				},
			},
			rowHeightReq(sheetID, 0, 1, 41),
			colWidthReq(sheetID, 0, 1, 320),
			colWidthReq(sheetID, 1, 2, 497),
			colWidthReq(sheetID, 2, 3, 327),
			colWidthReq(sheetID, 3, 4, 279),
			colWidthReq(sheetID, 4, 5, 222),
			zebraBandingReq(sheetID, 0, 5),
			{
				SetBasicFilter: &sheets.SetBasicFilterRequest{
					Filter: &sheets.BasicFilter{
						Range: &sheets.GridRange{
							SheetId:          int64(sheetID),
							StartRowIndex:    0,
							EndRowIndex:      1,
							StartColumnIndex: 0,
							EndColumnIndex:   5,
						},
					},
				},
			},
		}}).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to format Budget tab: %w", err)
	}

	_ = r.FormatFont(ctx, "Budget")

	return nil
}

// InitNotesTab creates Notes tab structure.
func (r *GoogleSheetRepository) InitNotesTab(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, "Notes")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if err := r.EnsureTabExists(ctx, "Notes"); err != nil {
		return err
	}
	if err := r.ensureNotesHeader(ctx); err != nil {
		return err
	}
	if err := r.formatHeaderRow(ctx, "Notes", 3); err != nil {
		return err
	}

	sheetID, _ := r.getSheetIDWithRefresh(ctx, "Notes")
	_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
					Properties: &sheets.SheetProperties{
						SheetId:        int64(sheetID),
						GridProperties: &sheets.GridProperties{
							FrozenRowCount: 1,
							ColumnCount:    3,
						},
					},
					Fields: "gridProperties(frozenRowCount,columnCount)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1,
						StartColumnIndex: 0, EndColumnIndex: 3,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							TextFormat: &sheets.TextFormat{
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263},
							},
							HorizontalAlignment: "RIGHT",
							VerticalAlignment:   "MIDDLE",
							Borders:             &sheets.Borders{},
						},
					},
					Fields: "userEnteredFormat(textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
			rowHeightReq(sheetID, 0, 1, 44),
			colWidthReq(sheetID, 0, 1, 302),
			colWidthReq(sheetID, 1, 2, 338),
			colWidthReq(sheetID, 2, 3, 1005),
			zebraBandingReq(sheetID, 0, 3),
			{
				SetBasicFilter: &sheets.SetBasicFilterRequest{
					Filter: &sheets.BasicFilter{
						Range: &sheets.GridRange{
							SheetId:          int64(sheetID),
							StartRowIndex:    0,
							EndRowIndex:      1,
							StartColumnIndex: 0,
							EndColumnIndex:   3,
						},
					},
				},
			},
		},
	}).Context(ctx).Do()

	_ = r.FormatFont(ctx, "Notes")

	return nil
}

// InitReminderTab creates Reminders tab structure.
func (r *GoogleSheetRepository) InitReminderTab(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, ReminderSheetName)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if err := r.EnsureTabExists(ctx, ReminderSheetName); err != nil {
		return err
	}
	if err := r.ensureReminderHeader(ctx); err != nil {
		return err
	}
	if err := r.formatHeaderRow(ctx, ReminderSheetName, int64(len(ReminderHeaders))); err != nil {
		return err
	}

	nCols := int64(13)
	sheetID, _ := r.getSheetIDWithRefresh(ctx, ReminderSheetName)
	_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
					Properties: &sheets.SheetProperties{
						SheetId:        int64(sheetID),
						GridProperties: &sheets.GridProperties{
							FrozenRowCount: 1,
							ColumnCount:    13,
						},
					},
					Fields: "gridProperties(frozenRowCount,columnCount)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1,
						StartColumnIndex: 0, EndColumnIndex: 13,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							TextFormat: &sheets.TextFormat{
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263},
							},
							HorizontalAlignment: "LEFT",
							VerticalAlignment:   "MIDDLE",
							Borders:             &sheets.Borders{},
						},
					},
					Fields: "userEnteredFormat(textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
			rowHeightReq(sheetID, 0, 1, 43),
			colWidthReq(sheetID, 0, 1, 109),
			colWidthReq(sheetID, 1, 2, 134),
			colWidthReq(sheetID, 2, 3, 135),
			colWidthReq(sheetID, 3, 4, 279),
			colWidthReq(sheetID, 4, 5, 85),
			colWidthReq(sheetID, 5, 6, 134),
			colWidthReq(sheetID, 6, 7, 86),
			colWidthReq(sheetID, 7, 8, 133),
			colWidthReq(sheetID, 8, 9, 123),
			colWidthReq(sheetID, 9, 10, 136),
			colWidthReq(sheetID, 10, 11, 126),
			colWidthReq(sheetID, 11, 12, 159),
			zebraBandingReq(sheetID, 0, int(nCols)),
			{
				SetBasicFilter: &sheets.SetBasicFilterRequest{
					Filter: &sheets.BasicFilter{
						Range: &sheets.GridRange{
							SheetId:          int64(sheetID),
							StartRowIndex:    0,
							EndRowIndex:      1,
							StartColumnIndex: 0,
							EndColumnIndex:   nCols,
						},
					},
				},
			},
		},
	}).Context(ctx).Do()

	_ = r.FormatFont(ctx, ReminderSheetName)

	return nil
}

func (r *GoogleSheetRepository) formatHeaderRow(ctx context.Context, tabName string, colCount int64) error {
	if colCount <= 0 {
		return nil
	}

	sheetID, err := r.getSheetIDWithRefresh(ctx, tabName)
	if err != nil {
		return err
	}

	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId:          int64(sheetID),
						StartRowIndex:    0,
						EndRowIndex:      1,
						StartColumnIndex: 0,
						EndColumnIndex:   colCount,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							BackgroundColor: &sheets.Color{
								Red:   0.263,
								Green: 0.263,
								Blue:  0.263,
							},
							TextFormat: &sheets.TextFormat{
								Bold:       true,
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{
									Red:   1,
									Green: 1,
									Blue:  1,
								},
							},
							HorizontalAlignment: "CENTER",
							VerticalAlignment:   "MIDDLE",
							Borders: &sheets.Borders{
								Top:    &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Bottom: &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Left:   &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
								Right:  &sheets.Border{Style: "SOLID", Color: &sheets.Color{Red: 0.196, Green: 0.196, Blue: 0.196}},
							},
						},
					},
					Fields: "userEnteredFormat(backgroundColor,textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
		},
	}

	_, err = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID, req).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to format header row on %s: %w", tabName, err)
	}
	return nil
}

func (r *GoogleSheetRepository) ensureMonthlyHeader(ctx context.Context, tabName string) error {
	headerRange := fmt.Sprintf("'%s'!A1:G1", tabName)
	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, headerRange).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to check monthly header on %s: %w", tabName, err)
	}

	if resp != nil && len(resp.Values) > 0 && len(resp.Values[0]) >= 7 {
		return nil
	}

	headers := &sheets.ValueRange{
		Values: [][]interface{}{
			{"ID", "Tanggal", "Waktu", "Tipe", "Kategori", "Deskripsi", "Jumlah"},
		},
	}
	_, err = r.service.Spreadsheets.Values.Update(r.spreadsheetID, headerRange, headers).
		ValueInputOption("RAW").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to write monthly headers on %s: %w", tabName, err)
	}

	if err := r.FormatHeaders(ctx, tabName); err != nil {
		return err
	}

	// Add frozen header, number format for Jumlah column, and auto-resize.
	sheetID, err := r.getSheetIDWithRefresh(ctx, tabName)
	if err != nil {
		return nil // non-fatal
	}

	_, _ = r.service.Spreadsheets.BatchUpdate(r.spreadsheetID,
		&sheets.BatchUpdateSpreadsheetRequest{Requests: []*sheets.Request{
			{
				UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
					Properties: &sheets.SheetProperties{
						SheetId:        int64(sheetID),
						GridProperties: &sheets.GridProperties{
							FrozenRowCount: 1,
							ColumnCount:    7,
						},
					},
					Fields: "gridProperties(frozenRowCount,columnCount)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1,
						StartColumnIndex: 0, EndColumnIndex: 7,
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							TextFormat: &sheets.TextFormat{
								FontFamily: "Roboto",
								FontSize:   10,
								ForegroundColor: &sheets.Color{Red: 0.263, Green: 0.263, Blue: 0.263},
							},
							HorizontalAlignment: "LEFT",
							VerticalAlignment:   "MIDDLE",
							Borders:             &sheets.Borders{},
						},
					},
					Fields: "userEnteredFormat(textFormat,horizontalAlignment,verticalAlignment,borders)",
				},
			},
			{
				RepeatCell: &sheets.RepeatCellRequest{
					Range: &sheets.GridRange{
						SheetId: int64(sheetID), StartRowIndex: 1,
						StartColumnIndex: 6, EndColumnIndex: 7, // Column G (Jumlah)
					},
					Cell: &sheets.CellData{
						UserEnteredFormat: &sheets.CellFormat{
							NumberFormat:        &sheets.NumberFormat{Type: "NUMBER", Pattern: "\"Rp\" #,##0"},
							HorizontalAlignment: "RIGHT",
						},
					},
					Fields: "userEnteredFormat(numberFormat,horizontalAlignment)",
				},
			},
			rowHeightReq(sheetID, 0, 1, 42),
			colWidthReq(sheetID, 0, 1, 158),
			colWidthReq(sheetID, 1, 2, 163),
			colWidthReq(sheetID, 2, 3, 144),
			colWidthReq(sheetID, 3, 4, 288),
			colWidthReq(sheetID, 4, 5, 233),
			colWidthReq(sheetID, 5, 6, 389),
			colWidthReq(sheetID, 6, 7, 260),
			zebraBandingReq(sheetID, 0, 7),
			{
				SetBasicFilter: &sheets.SetBasicFilterRequest{
					Filter: &sheets.BasicFilter{
						Range: &sheets.GridRange{
							SheetId:          int64(sheetID),
							StartRowIndex:    0,
							EndRowIndex:      1,
							StartColumnIndex: 0,
							EndColumnIndex:   7,
						},
					},
				},
			},
		}}).Context(ctx).Do()

	return nil
}

func (r *GoogleSheetRepository) ensureBudgetHeader(ctx context.Context) error {
	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, "'Budget'!A1:E1").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to check budget header: %w", err)
	}
	if resp != nil && len(resp.Values) > 0 && len(resp.Values[0]) >= 5 {
		return nil
	}

	headers := &sheets.ValueRange{
		Values: [][]interface{}{
			{"Kategori", "Budget Bulanan", "Terpakai", "Sisa", "Status"},
		},
	}
	_, err = r.service.Spreadsheets.Values.Update(r.spreadsheetID, "'Budget'!A1:E1", headers).
		ValueInputOption("RAW").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to write budget headers: %w", err)
	}
	return nil
}

func (r *GoogleSheetRepository) ensureNotesHeader(ctx context.Context) error {
	resp, err := r.service.Spreadsheets.Values.Get(r.spreadsheetID, "'Notes'!A1:C1").Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to check notes header: %w", err)
	}
	if resp != nil && len(resp.Values) > 0 && len(resp.Values[0]) >= 3 {
		return nil
	}

	headers := &sheets.ValueRange{
		Values: [][]interface{}{
			{"Tanggal", "Waktu", "Catatan"},
		},
	}
	_, err = r.service.Spreadsheets.Values.Update(r.spreadsheetID, "'Notes'!A1:C1", headers).
		ValueInputOption("RAW").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to write notes headers: %w", err)
	}
	return nil
}

func (r *GoogleSheetRepository) ensureReminderHeader(ctx context.Context) error {
	resp, err := r.service.Spreadsheets.Values.
		Get(r.spreadsheetID, "'Reminders'!A1:P1").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to check reminders header: %w", err)
	}
	if resp != nil && len(resp.Values) > 0 && len(resp.Values[0]) >= len(ReminderHeaders) {
		return nil
	}

	headers := &sheets.ValueRange{
		Values: [][]interface{}{ReminderHeaders},
	}
	_, err = r.service.Spreadsheets.Values.
		Update(r.spreadsheetID, "'Reminders'!A1:P1", headers).
		ValueInputOption("RAW").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to write reminders headers: %w", err)
	}
	return nil
}

func (r *GoogleSheetRepository) getSheetIDWithRefresh(ctx context.Context, tabName string) (int, error) {
	if id, ok := r.tabManager.GetSheetID(tabName); ok {
		return id, nil
	}
	if err := r.tabManager.RefreshCache(ctx); err != nil {
		return 0, fmt.Errorf("failed to refresh tab cache: %w", err)
	}
	if id, ok := r.tabManager.GetSheetID(tabName); ok {
		return id, nil
	}
	return 0, fmt.Errorf("tab %s not found", tabName)
}

func isMonthlyTabName(tabName string) bool {
	parts := strings.Fields(strings.TrimSpace(tabName))
	if len(parts) != 2 {
		return false
	}

	year, err := strconv.Atoi(parts[1])
	if err != nil || year < 2000 || year > 9999 {
		return false
	}

	for _, name := range monthNamesID {
		if strings.EqualFold(parts[0], name) {
			return true
		}
	}
	return false
}

// IsMonthlyTab is the exported version of isMonthlyTabName for use in tooling.
func IsMonthlyTab(tabName string) bool { return isMonthlyTabName(tabName) }

func parseUpdatedRowIndex(updatedRange string) int {
	if updatedRange == "" {
		return -1
	}
	excl := strings.LastIndex(updatedRange, "!")
	target := updatedRange
	if excl >= 0 && excl+1 < len(updatedRange) {
		target = updatedRange[excl+1:]
	}
	colon := strings.Index(target, ":")
	if colon >= 0 {
		target = target[:colon]
	}
	var digits strings.Builder
	for _, r := range target {
		if r >= '0' && r <= '9' {
			digits.WriteRune(r)
		}
	}
	if digits.Len() == 0 {
		return -1
	}
	n, err := strconv.Atoi(digits.String())
	if err != nil {
		return -1
	}
	return n
}

// AppendReminder adds a reminder row to Reminders tab.
func (r *GoogleSheetRepository) AppendReminder(ctx context.Context, reminder *Reminder) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if reminder == nil {
		return fmt.Errorf("reminder is nil")
	}
	reminder.Normalize()
	if err := reminder.Validate(); err != nil {
		return err
	}

	if err := r.InitReminderTab(ctx); err != nil {
		return err
	}

	valueRange := &sheets.ValueRange{
		Values: [][]interface{}{reminder.ToRow()},
	}

	_, err := r.service.Spreadsheets.Values.
		Append(r.spreadsheetID, "'Reminders'!A:P", valueRange).
		ValueInputOption("USER_ENTERED").
		InsertDataOption("OVERWRITE").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to append reminder: %w", err)
	}

	// Proactively trigger premium formatting
	_ = r.FormatFont(ctx, ReminderSheetName)

	return nil
}

// ListActiveReminders returns active reminders.
func (r *GoogleSheetRepository) ListActiveReminders(ctx context.Context) ([]Reminder, error) {
	if r == nil {
		return nil, fmt.Errorf("repository is nil")
	}
	if err := r.InitReminderTab(ctx); err != nil {
		return nil, err
	}

	resp, err := r.service.Spreadsheets.Values.
		Get(r.spreadsheetID, "'Reminders'!A2:P").
		Context(ctx).
		Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list reminders: %w", err)
	}
	if resp == nil || len(resp.Values) == 0 {
		return []Reminder{}, nil
	}

	out := make([]Reminder, 0, len(resp.Values))
	for _, row := range resp.Values {
		rem, err := ReminderFromRow(row)
		if err != nil {
			continue
		}
		if rem.Status == ReminderStatusActive {
			out = append(out, *rem)
		}
	}
	return out, nil
}

// GetReminderByID returns reminder and row index in Reminders tab.
func (r *GoogleSheetRepository) GetReminderByID(ctx context.Context, id string) (*Reminder, int, error) {
	if r == nil {
		return nil, 0, fmt.Errorf("repository is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, 0, fmt.Errorf("reminder ID is required")
	}
	if err := r.InitReminderTab(ctx); err != nil {
		return nil, 0, err
	}

	resp, err := r.service.Spreadsheets.Values.
		Get(r.spreadsheetID, "'Reminders'!A2:P").
		Context(ctx).
		Do()
	if err != nil {
		return nil, 0, fmt.Errorf("failed to read reminders: %w", err)
	}
	if resp == nil || len(resp.Values) == 0 {
		return nil, 0, fmt.Errorf("reminder %s not found", id)
	}

	for i, row := range resp.Values {
		if len(row) == 0 {
			continue
		}
		rowID := strings.TrimSpace(fmt.Sprintf("%v", row[0]))
		if rowID != id {
			continue
		}
		rem, err := ReminderFromRow(row)
		if err != nil {
			return nil, 0, err
		}
		return rem, i + 2, nil
	}

	return nil, 0, fmt.Errorf("reminder %s not found", id)
}

// UpdateReminder updates reminder row at rowIndex.
func (r *GoogleSheetRepository) UpdateReminder(ctx context.Context, rowIndex int, reminder *Reminder) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}
	if rowIndex < 2 {
		return fmt.Errorf("invalid row index: %d", rowIndex)
	}
	if reminder == nil {
		return fmt.Errorf("reminder is nil")
	}
	reminder.Normalize()
	if err := reminder.Validate(); err != nil {
		return err
	}

	if err := r.InitReminderTab(ctx); err != nil {
		return err
	}

	writeRange := fmt.Sprintf("'Reminders'!A%d:P%d", rowIndex, rowIndex)
	valueRange := &sheets.ValueRange{
		Values: [][]interface{}{reminder.ToRow()},
	}
	_, err := r.service.Spreadsheets.Values.
		Update(r.spreadsheetID, writeRange, valueRange).
		ValueInputOption("USER_ENTERED").
		Context(ctx).
		Do()
	if err != nil {
		return fmt.Errorf("failed to update reminder row %d: %w", rowIndex, err)
	}

	return nil
}

// ListDueReminders returns active reminders that can be sent at the provided time.
func (r *GoogleSheetRepository) ListDueReminders(ctx context.Context, now time.Time) ([]Reminder, error) {
	active, err := r.ListActiveReminders(ctx)
	if err != nil {
		return nil, err
	}

	due := make([]Reminder, 0, len(active))
	for _, rem := range active {
		copyRem := rem
		if copyRem.CanSendNow(now) {
			due = append(due, copyRem)
		}
	}
	return due, nil
}

func (r *GoogleSheetRepository) nextDailyTransactionID(ctx context.Context, tabName string, when time.Time) (string, error) {
	datePrefix := when.In(WIB).Format("20060102")

	txs, err := r.GetTransactions(ctx, tabName)
	if err != nil {
		return "", err
	}

	maxCounter := 0
	for _, tx := range txs {
		counter, ok := parseDailyCounter(tx.ID, datePrefix)
		if !ok {
			continue
		}
		if counter > maxCounter {
			maxCounter = counter
		}
	}

	return fmt.Sprintf("%s-%03d", datePrefix, maxCounter+1), nil
}

func parseDailyCounter(id string, datePrefix string) (int, bool) {
	prefix := datePrefix + "-"
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}

	suffix := strings.TrimPrefix(id, prefix)
	if len(suffix) != 3 {
		return 0, false
	}

	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 {
		return 0, false
	}

	return n, true
}

// SearchTransactions searches transactions across monthly tabs that overlap the
// requested date range. When no dates are specified it searches the current
// month. Filters are applied in-memory after fetching the relevant tabs.
func (r *GoogleSheetRepository) SearchTransactions(ctx context.Context, filter TransactionFilter) ([]Transaction, error) {
	if r == nil {
		return nil, fmt.Errorf("repository is nil")
	}

	const defaultLimit = 50
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLimit
	}

	now := time.Now().In(WIB)

	// Determine which monthly tabs to scan.
	var tabsToScan []string
	if !filter.DateFrom.IsZero() && !filter.DateTo.IsZero() {
		// Collect all month tabs between DateFrom and DateTo.
		tabsToScan = monthTabsBetween(filter.DateFrom, filter.DateTo)
	} else if !filter.DateFrom.IsZero() {
		tabsToScan = []string{TabNameForTime(filter.DateFrom), TabNameForTime(now)}
		// Deduplicate if same month.
		if tabsToScan[0] == tabsToScan[1] {
			tabsToScan = tabsToScan[:1]
		}
	} else {
		// Default: current month only.
		tabsToScan = []string{TabNameForTime(now)}
	}

	var results []Transaction

	for _, tabName := range tabsToScan {
		exists, err := r.tabManager.HasTab(ctx, tabName)
		if err != nil || !exists {
			continue
		}

		txs, err := r.GetTransactions(ctx, tabName)
		if err != nil {
			continue
		}

		for _, tx := range txs {
			if !matchesFilter(tx, filter) {
				continue
			}
			results = append(results, tx)
			if len(results) >= limit {
				return results, nil
			}
		}
	}

	return results, nil
}

// monthTabsBetween returns the ordered list of monthly tab names (Indonesian)
// that cover the period from `from` to `to` inclusive.
func monthTabsBetween(from, to time.Time) []string {
	from = from.In(WIB)
	to = to.In(WIB)

	if to.Before(from) {
		from, to = to, from
	}

	var tabs []string
	cur := time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, WIB)
	end := time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, WIB)

	for !cur.After(end) {
		tabs = append(tabs, TabNameForTime(cur))
		cur = cur.AddDate(0, 1, 0)
	}
	return tabs
}

// matchesFilter applies all filter criteria to one transaction.
func matchesFilter(tx Transaction, f TransactionFilter) bool {
	// Type filter: TransactionFilter.TxType is TransactionType ("Pengeluaran"|"Pemasukan"|"").
	if f.TxType != "" {
		if tx.Type != f.TxType {
			return false
		}
	}

	// Keyword filter (case-insensitive substring on description).
	if f.Keyword != "" {
		if !strings.Contains(strings.ToLower(tx.Description), strings.ToLower(f.Keyword)) {
			return false
		}
	}

	// Category filter (case-insensitive exact).
	if f.Category != "" {
		if !strings.EqualFold(tx.Category, f.Category) {
			return false
		}
	}

	// Date range filters.
	if !f.DateFrom.IsZero() && tx.Date.In(WIB).Before(f.DateFrom.In(WIB)) {
		return false
	}
	if !f.DateTo.IsZero() {
		endOfDay := time.Date(f.DateTo.Year(), f.DateTo.Month(), f.DateTo.Day(), 23, 59, 59, 0, WIB)
		if tx.Date.In(WIB).After(endOfDay) {
			return false
		}
	}

	// Amount range filters.
	if f.MinAmount > 0 && tx.Amount < f.MinAmount {
		return false
	}
	if f.MaxAmount > 0 && tx.Amount > f.MaxAmount {
		return false
	}

	return true
}

// RefreshDashboard updates only the "Ringkasan Bulan Ini" section (rows 8-11)
// in the Dashboard tab to point to the current month's tab. This is called on
// the first message of a new month so the dashboard always reflects live data
// without recreating the whole tab (which would lose user chart positions).
func (r *GoogleSheetRepository) RefreshDashboard(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	exists, err := r.tabManager.HasTab(ctx, "Dashboard")
	if err != nil || !exists {
		return r.InitDashboard(ctx)
	}

	now := time.Now().In(WIB)
	currentTab := TabNameForTime(now)
	year := now.Year()

	monthNames := map[time.Month]string{
		time.January: "Januari", time.February: "Februari", time.March: "Maret",
		time.April: "April", time.May: "Mei", time.June: "Juni",
		time.July: "Juli", time.August: "Agustus", time.September: "September",
		time.October: "Oktober", time.November: "November", time.December: "Desember",
	}
	currentMonthName := monthNames[now.Month()]

	// Re-write rows 8-11: Ringkasan Bulan Ini section.
	rows := [][]interface{}{
		{fmt.Sprintf("📅 RINGKASAN %s %d", strings.ToUpper(currentMonthName), year), "", "", "", ""},
		{
			"💵 Pemasukan",
			fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pemasukan\",'%s'!G:G),0)", currentTab, currentTab),
			"",
			"💸 Pengeluaran",
			fmt.Sprintf("=IFERROR(SUMIF('%s'!D:D,\"Pengeluaran\",'%s'!G:G),0)", currentTab, currentTab),
		},
		{
			"💰 Saldo Bulan Ini",
			"=B9-E9",
			"",
			"📈 Transaksi",
			fmt.Sprintf("=IF(ISERR(INDIRECT(\"'%s'!A2\")),0,COUNTIF(INDIRECT(\"'%s'!A2:A\"),\"<>\"))", currentTab, currentTab),
		},
		{"📊 Rata-rata Pengeluaran/Hari", fmt.Sprintf("=IF(E10>0,E9/DAY(TODAY()),0)"), "", "", ""},
	}

	writeRange := "Dashboard!A8:E11"
	values := &sheets.ValueRange{Values: rows}

	return r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.Values.
			Update(r.spreadsheetID, writeRange, values).
			ValueInputOption("USER_ENTERED").
			Context(ctx).
			Do()
		return e
	})
}

// ReorderTabs arranges the spreadsheet tabs in the preferred display order:
//   Dashboard → <current month> → Budget → Notes → Reminders → [older months…]
//
// Called during bootstrap and automatically after each new monthly tab is created.
func (r *GoogleSheetRepository) ReorderTabs(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	sp, err := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get spreadsheet for reorder: %w", err)
	}

	// Find all monthly tabs
	var monthlyTabs []string
	for _, sh := range sp.Sheets {
		title := sh.Properties.Title
		if isMonthlyTabName(title) {
			monthlyTabs = append(monthlyTabs, title)
		}
	}

	// Priority order: Dashboard -> All Monthly Tabs -> Budget -> Notes -> Reminders
	priority := []string{"Dashboard"}
	priority = append(priority, monthlyTabs...)
	priority = append(priority, "Budget", "Notes", ReminderSheetName)

	// Build a map: name → sheetId
	idByName := make(map[string]int64, len(sp.Sheets))
	for _, sh := range sp.Sheets {
		if sh != nil && sh.Properties != nil {
			idByName[sh.Properties.Title] = sh.Properties.SheetId
		}
	}

	// Build ordered list of sheetIds to move, in priority sequence.
	var reqs []*sheets.Request
	position := 0

	for _, name := range priority {
		id, ok := idByName[name]
		if !ok {
			continue // tab doesn't exist yet — skip
		}
		reqs = append(reqs, &sheets.Request{
			UpdateSheetProperties: &sheets.UpdateSheetPropertiesRequest{
				Properties: &sheets.SheetProperties{
					SheetId: id,
					Index:   int64(position),
				},
				Fields: "index",
			},
		})
		position++
	}

	if len(reqs) == 0 {
		return nil
	}

	return r.withRetry(ctx, func() error {
		_, e := r.service.Spreadsheets.BatchUpdate(
			r.spreadsheetID,
			&sheets.BatchUpdateSpreadsheetRequest{Requests: reqs},
		).Context(ctx).Do()
		return e
	})
}

// ApplyZebraToExistingTabs applies premium formatting and zebra banding to all existing tabs.
// Safe to run on every startup — it clears manual background colors to ensure zebra banding is fully visible.
func (r *GoogleSheetRepository) ApplyZebraToExistingTabs(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("repository is nil")
	}

	sp, err := r.service.Spreadsheets.Get(r.spreadsheetID).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get spreadsheet for formatting patch: %w", err)
	}

	// isFormattedTab returns true if the tab is one we want to format.
	isFormattedTab := func(title string) bool {
		switch title {
		case "Budget", "Notes", ReminderSheetName:
			return true
		default:
			return isMonthlyTabName(title)
		}
	}

	for _, sh := range sp.Sheets {
		if sh == nil || sh.Properties == nil {
			continue
		}
		title := sh.Properties.Title
		if !isFormattedTab(title) {
			continue // skip Dashboard and unknown tabs
		}

		// Run premium formatting to reset manual backgrounds and restore zebra banding
		if err := r.FormatFont(ctx, title); err != nil {
			// Log and continue to next tab
			log.Printf("  ⚠️  Failed to format existing tab %s: %v", title, err)
		}
	}

	return nil
}
