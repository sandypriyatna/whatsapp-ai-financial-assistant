package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

// bootstrapSpreadsheet is called every time the bot starts.
// It is designed to be idempotent — safe to run on an existing spreadsheet.
//
// Prinsip:
//   - Hanya tab bulan SAAT INI yang dibuat saat init.
//   - Tab bulan baru akan dibuat otomatis saat transaksi pertama bulan tersebut masuk
//     (via AppendTransaction → EnsureTabExists).
//   - Tidak pernah membuat tab bulan yang belum terjadi.
//
// Urutan:
//  1. Hapus tab default "Sheet1" (jika spreadsheet baru)
//  2. Pastikan tab bulan ini ada (dengan header + formatting)
//  3. Inisialisasi Budget, Notes, Reminders (skip jika sudah ada)
//  4. Inisialisasi Dashboard (skip jika sudah ada — preserve user customisation)
//  5. Atur urutan tab: Dashboard → Bulan Ini → Budget → Notes → Reminders
func bootstrapSpreadsheet(ctx context.Context, repo *sheets.GoogleSheetRepository) error {
	now := time.Now().In(sheets.WIB)
	currentMonth := monthTabName(now)

	steps := []struct {
		name string
		fn   func() error
	}{
		{
			"Hapus tab default Sheet1 (jika spreadsheet baru)",
			func() error { return repo.DeleteDefaultSheet(ctx) },
		},
		{
			fmt.Sprintf("Pastikan tab bulan ini ada: %s", currentMonth),
			func() error { return repo.EnsureTabExists(ctx, currentMonth) },
		},
		{
			"Inisialisasi tab Budget",
			func() error { return repo.InitBudgetTab(ctx) },
		},
		{
			"Inisialisasi tab Notes",
			func() error { return repo.InitNotesTab(ctx) },
		},
		{
			"Inisialisasi tab Reminders",
			func() error { return repo.InitReminderTab(ctx) },
		},
		{
			"Inisialisasi Dashboard (skip jika sudah ada)",
			func() error { return repo.InitDashboard(ctx) },
		},
		{
			"Terapkan zebra banding ke tab yang sudah ada (patch one-time)",
			func() error { return repo.ApplyZebraToExistingTabs(ctx) },
		},
		{
			"Atur urutan tab: Dashboard → Bulan Ini → Budget → Notes → Reminders",
			func() error { return repo.ReorderTabs(ctx) },
		},
	}

	for _, step := range steps {
		log.Printf("  ➜ %s...", step.name)
		if err := step.fn(); err != nil {
			return fmt.Errorf("langkah '%s' gagal: %w", step.name, err)
		}
		log.Printf("  ✅ %s", step.name)
	}

	return nil
}

// monthTabName returns the Indonesian month tab name for the given time.
// Example: time di Mei 2026 → "Mei 2026"
func monthTabName(t time.Time) string {
	t = t.In(sheets.WIB)
	names := map[time.Month]string{
		time.January: "Januari", time.February: "Februari", time.March: "Maret",
		time.April: "April", time.May: "Mei", time.June: "Juni",
		time.July: "Juli", time.August: "Agustus", time.September: "September",
		time.October: "Oktober", time.November: "November", time.December: "Desember",
	}
	return fmt.Sprintf("%s %d", names[t.Month()], t.Year())
}
