package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	ctx := context.Background()
	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets repository connection error: %v", err)
	}

	srv := repo.GetService()
	
	// Read the top rows of Mei 2026 (current month)
	resp, err := srv.Spreadsheets.Values.Get(cfg.SheetsID, "'Mei 2026'!A1:G10").Context(ctx).Do()
	if err != nil {
		log.Fatalf("❌ Failed to fetch values: %v", err)
	}

	fmt.Println("📋 Raw rows in Mei 2026:")
	for r, row := range resp.Values {
		fmt.Printf("Row %d: %v\n", r+1, row)
	}
}
