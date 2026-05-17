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
	
	// Read rows 51 to 100 of Mei 2026
	resp, err := srv.Spreadsheets.Values.Get(cfg.SheetsID, "'Mei 2026'!A51:G100").Context(ctx).Do()
	if err != nil {
		log.Fatalf("❌ Failed to fetch values: %v", err)
	}

	fmt.Println("📋 Rows 51-100 in Mei 2026:")
	for r, row := range resp.Values {
		fmt.Printf("Row %d: %v\n", r+51, row)
	}
}
