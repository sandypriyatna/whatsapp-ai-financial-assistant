package sheets

import (
	"context"
	"fmt"
	"sync"

	"google.golang.org/api/sheets/v4"
)

type TabManager struct {
	service       *sheets.Service
	spreadsheetID string

	// Injectable hooks for deterministic unit testing.
	getSpreadsheet func(ctx context.Context) (*sheets.Spreadsheet, error)
	batchUpdate    func(ctx context.Context, req *sheets.BatchUpdateSpreadsheetRequest) (*sheets.BatchUpdateSpreadsheetResponse, error)

	mu           sync.Mutex
	existingTabs map[string]int // tab name -> sheet ID
}

func NewTabManager(service *sheets.Service, spreadsheetID string) *TabManager {
	tm := &TabManager{
		service:       service,
		spreadsheetID: spreadsheetID,
		existingTabs:  make(map[string]int),
	}
	tm.getSpreadsheet = tm.defaultGetSpreadsheet
	tm.batchUpdate = tm.defaultBatchUpdate
	return tm
}

func (tm *TabManager) defaultGetSpreadsheet(ctx context.Context) (*sheets.Spreadsheet, error) {
	if tm.service == nil {
		return nil, fmt.Errorf("sheets service is nil")
	}
	return tm.service.Spreadsheets.
		Get(tm.spreadsheetID).
		Context(ctx).
		Do()
}

func (tm *TabManager) defaultBatchUpdate(ctx context.Context, req *sheets.BatchUpdateSpreadsheetRequest) (*sheets.BatchUpdateSpreadsheetResponse, error) {
	if tm.service == nil {
		return nil, fmt.Errorf("sheets service is nil")
	}
	return tm.service.Spreadsheets.
		BatchUpdate(tm.spreadsheetID, req).
		Context(ctx).
		Do()
}

// HasTab checks if a tab exists via the cache or API without creating it.
func (tm *TabManager) HasTab(ctx context.Context, tabName string) (bool, error) {
	if tm == nil {
		return false, fmt.Errorf("tab manager is nil")
	}
	if tabName == "" {
		return false, fmt.Errorf("tab name is empty")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	if _, ok := tm.existingTabs[tabName]; ok {
		return true, nil
	}

	spreadsheet, err := tm.getSpreadsheet(ctx)
	if err != nil {
		return false, fmt.Errorf("failed to fetch spreadsheet metadata: %w", err)
	}

	for _, sh := range spreadsheet.Sheets {
		if sh == nil || sh.Properties == nil {
			continue
		}
		tm.existingTabs[sh.Properties.Title] = int(sh.Properties.SheetId)
		if sh.Properties.Title == tabName {
			return true, nil
		}
	}

	return false, nil
}

// GetTabID retrieves the google sheet ID for a given tab name.
func (tm *TabManager) GetTabID(ctx context.Context, tabName string) (int64, error) {
	has, err := tm.HasTab(ctx, tabName)
	if err != nil {
		return 0, err
	}
	if !has {
		return 0, fmt.Errorf("tab %q not found", tabName)
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	return int64(tm.existingTabs[tabName]), nil
}

// CreateBlankTab creates a new empty tab. Thread-safe.
func (tm *TabManager) CreateBlankTab(ctx context.Context, tabName string) error {
	if tm == nil {
		return fmt.Errorf("tab manager is nil")
	}
	if tm.spreadsheetID == "" {
		return fmt.Errorf("spreadsheet ID is empty")
	}
	
	req := &sheets.BatchUpdateSpreadsheetRequest{
		Requests: []*sheets.Request{
			{
				AddSheet: &sheets.AddSheetRequest{
					Properties: &sheets.SheetProperties{
						Title: tabName,
					},
				},
			},
		},
	}

	resp, err := tm.batchUpdate(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to create tab %q: %w", tabName, err)
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()
	
	// Cache created sheet ID if present in response.
	for _, reply := range resp.Replies {
		if reply == nil || reply.AddSheet == nil || reply.AddSheet.Properties == nil {
			continue
		}
		if reply.AddSheet.Properties.Title == tabName {
			tm.existingTabs[tabName] = int(reply.AddSheet.Properties.SheetId)
			return nil
		}
	}

	return tm.refreshCacheLocked(ctx)
}

// RefreshCache reloads tab list from API.
func (tm *TabManager) RefreshCache(ctx context.Context) error {
	if tm == nil {
		return fmt.Errorf("tab manager is nil")
	}
	if tm.spreadsheetID == "" {
		return fmt.Errorf("spreadsheet ID is empty")
	}
	if tm.getSpreadsheet == nil {
		return fmt.Errorf("getSpreadsheet hook is nil")
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	return tm.refreshCacheLocked(ctx)
}

func (tm *TabManager) refreshCacheLocked(ctx context.Context) error {
	spreadsheet, err := tm.getSpreadsheet(ctx)
	if err != nil {
		return fmt.Errorf("failed to fetch spreadsheet metadata: %w", err)
	}

	cache := make(map[string]int, len(spreadsheet.Sheets))
	for _, sh := range spreadsheet.Sheets {
		if sh == nil || sh.Properties == nil {
			continue
		}
		cache[sh.Properties.Title] = int(sh.Properties.SheetId)
	}
	tm.existingTabs = cache

	return nil
}

// GetSheetID returns the sheet ID for a tab name.
func (tm *TabManager) GetSheetID(tabName string) (int, bool) {
	if tm == nil {
		return 0, false
	}

	tm.mu.Lock()
	defer tm.mu.Unlock()

	id, ok := tm.existingTabs[tabName]
	return id, ok
}

// GetExistingTabs returns a copy of the cached tabs.
func (tm *TabManager) GetExistingTabs() map[string]int {
	if tm == nil {
		return nil
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()

	cp := make(map[string]int, len(tm.existingTabs))
	for k, v := range tm.existingTabs {
		cp[k] = v
	}
	return cp
}
