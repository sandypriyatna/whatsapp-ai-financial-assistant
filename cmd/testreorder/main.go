package main

import (
	"context"
	"fmt"
	"log"

	"github.com/joho/godotenv"
	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
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

	ctx := context.Background()

	fmt.Println("Mengurutkan tab sesuai urutan terbaru...")
	if err := repo.ReorderTabs(ctx); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Sukses diurutkan!")
}
