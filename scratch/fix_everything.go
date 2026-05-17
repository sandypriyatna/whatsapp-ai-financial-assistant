package main

import (
	"context"
	"log"

	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

func main() {
	log.Println("🚀 Connecting to target Google Spreadsheet...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	ctx := context.Background()
	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets repository connection error: %v", err)
	}

	log.Printf("📊 Target Spreadsheet ID: %s", cfg.SheetsID)

	// Step 1: Reformat all existing tabs (Budget, Notes, Reminders, and all Monthly Tabs)
	log.Println("⚡ Step 1: Restoring premium zebra banding and clearing manual backgrounds on all existing tabs...")
	if err := repo.ApplyZebraToExistingTabs(ctx); err != nil {
		log.Fatalf("❌ Failed to format existing tabs: %v", err)
	}
	log.Println("✅ Formatting applied successfully!")

	// Step 2: Initialize / rebuild Dashboard with correct row offsets and formulas
	log.Println("⚡ Step 2: Rebuilding Dashboard with perfect formulas, layout alignment, and charts...")
	if err := repo.InitDashboard(ctx); err != nil {
		log.Fatalf("❌ Failed to initialize dashboard: %v", err)
	}
	log.Println("✅ Dashboard rebuilt successfully!")

	// Step 3: Reorder tabs so they are presented in the priority sequence
	log.Println("⚡ Step 3: Reordering spreadsheet tabs...")
	if err := repo.ReorderTabs(ctx); err != nil {
		log.Fatalf("❌ Failed to reorder tabs: %v", err)
	}
	log.Println("✅ Tabs reordered successfully!")

	log.Println("🎉 HUGE SUCCESS! Spreadsheet fully updated and completely restored to the absolute premium layout standards!")
}
