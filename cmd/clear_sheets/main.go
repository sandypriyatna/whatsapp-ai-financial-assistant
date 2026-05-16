package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets error: %v", err)
	}

	tabs := []string{"Pemasukan", "Pengeluaran", sheets.TabNameForTime(time.Now())}
	for _, tab := range tabs {
		log.Printf("🧹 Membersihkan tab: %s...", tab)
		err := repo.ClearRange(context.Background(), tab+"!A2:Z1000")
		if err != nil {
			if strings.Contains(err.Error(), "404") {
				log.Printf("⚠️ Tab %s tidak ditemukan, skip.", tab)
				continue
			}
			log.Printf("❌ Gagal membersihkan %s: %v", tab, err)
		} else {
			log.Printf("✅ Tab %s bersih!", tab)
		}
	}
	log.Println("✨ Selesai! Spreadsheet sekarang kosong (kecuali Header).")
}
