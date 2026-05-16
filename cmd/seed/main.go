// cmd/seed/main.go — Seeder untuk insert contoh data ke semua sheet.
// Jalankan sekali: go run ./cmd/seed/main.go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
	"github.com/sanspriyatna/wa-finance/internal/config"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
	googlesheets "google.golang.org/api/sheets/v4"
)

func main() {
	_ = godotenv.Load()
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("❌ Config error: %v", err)
	}

	repo, err := sheets.NewGoogleSheetRepository(cfg.GoogleCredsPath, cfg.GoogleCredsJSON, cfg.SheetsID)
	if err != nil {
		log.Fatalf("❌ Sheets error: %v", err)
	}

	ctx := context.Background()

	// Dapatkan service Sheets via exported accessor
	svc, spreadsheetID := repo.SheetService()

	write := func(tabRange string, values [][]interface{}) {
		_, err := svc.Spreadsheets.Values.
			Append(spreadsheetID, tabRange, &googlesheets.ValueRange{Values: values}).
			ValueInputOption("USER_ENTERED").
			InsertDataOption("OVERWRITE").
			Context(ctx).
			Do()
		if err != nil {
			log.Printf("  ❌ %s: %v", tabRange, err)
		} else {
			log.Printf("  ✅ %s: %d baris berhasil dimasukkan", tabRange, len(values))
		}
	}

	// ─── Mei 2026: 7 transaksi realistis ───────────────────────────────────────
	// Kolom: ID | Tanggal | Waktu | Tipe | Kategori | Deskripsi | Jumlah
	fmt.Println("\n📝 Mengisi tab Mei 2026...")
	write("'Mei 2026'!A2:G", [][]interface{}{
		{"20260501-001", "01/05/2026", "07:30", "Pemasukan",    "Gaji",           "Gaji bulanan Mei 2026",                    9500000},
		{"20260503-001", "03/05/2026", "12:15", "Pengeluaran",  "Makanan",        "Makan siang di warung Padang",             35000},
		{"20260505-001", "05/05/2026", "08:00", "Pengeluaran",  "Transportasi",   "Grab ke kantor pp",                        48000},
		{"20260507-001", "07/05/2026", "19:30", "Pengeluaran",  "Hiburan",        "Nonton bioskop Avengers",                  120000},
		{"20260510-001", "10/05/2026", "09:00", "Pengeluaran",  "Belanja",        "Belanja bulanan di Indomaret",             450000},
		{"20260512-001", "12/05/2026", "13:45", "Pengeluaran",  "Makanan",        "Order GoFood Pizza Hut",                   95000},
		{"20260514-001", "14/05/2026", "16:00", "Pemasukan",    "Lainnya",        "Freelance desain logo klien",              800000},
	})

	// ─── Budget: 6 kategori dengan target realistis ────────────────────────────
	// Kolom: Kategori | Budget Bulanan | Terpakai (formula) | Sisa (formula) | Status (formula)
	fmt.Println("\n💰 Mengisi tab Budget...")
	write("'Budget'!A2:E", [][]interface{}{
		{"Makanan",       1500000, "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A2),0)", "=B2-C2", "=IF(D2<0,\"🚨 Over Budget\",IF(D2<B2*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
		{"Transportasi",  500000,  "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A3),0)", "=B3-C3", "=IF(D3<0,\"🚨 Over Budget\",IF(D3<B3*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
		{"Hiburan",       300000,  "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A4),0)", "=B4-C4", "=IF(D4<0,\"🚨 Over Budget\",IF(D4<B4*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
		{"Belanja",       800000,  "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A5),0)", "=B5-C5", "=IF(D5<0,\"🚨 Over Budget\",IF(D5<B5*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
		{"Kesehatan",     400000,  "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A6),0)", "=B6-C6", "=IF(D6<0,\"🚨 Over Budget\",IF(D6<B6*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
		{"Komunikasi",    200000,  "=IFERROR(SUMIFS('Mei 2026'!G:G,'Mei 2026'!D:D,\"Pengeluaran\",'Mei 2026'!E:E,A7),0)", "=B7-C7", "=IF(D7<0,\"🚨 Over Budget\",IF(D7<B7*0.2,\"⚠️ Hampir Habis\",\"✅ Aman\"))"},
	})

	// ─── Notes: 5 catatan ──────────────────────────────────────────────────────
	// Kolom: Tanggal | Waktu | Catatan
	fmt.Println("\n📒 Mengisi tab Notes...")
	write("'Notes'!A2:C", [][]interface{}{
		{"01/05/2026", "09:00", "Target tabungan bulan ini: Rp 2.000.000. Mulai disiplin pengeluaran makanan."},
		{"03/05/2026", "20:30", "Jangan lupa perpanjang BPJS sebelum tanggal 15 Mei — bayar via m-banking."},
		{"07/05/2026", "21:00", "Review portfolio investasi saham — BBCA dan TLKM perlu dievaluasi."},
		{"10/05/2026", "08:15", "Rekening darurat sudah terisi 3 bulan pengeluaran — target 6 bulan akhir tahun."},
		{"14/05/2026", "17:45", "Rencana liburan akhir tahun ke Bali — mulai sisihkan Rp 500.000 per bulan."},
	})

	// ─── Reminders: 5 pengingat ────────────────────────────────────────────────
	// Kolom sesuai ReminderHeaders:
	// ID | Tanggal Target | Waktu Target | Pesan | Mode | Pengingat/Hari | Status | Dibuat Tanggal | Dibuat Waktu | Diubah Tanggal | Diubah Waktu | Last Reminder Date
	fmt.Println("\n⏰ Mengisi tab Reminders...")
	write("'Reminders'!A2", [][]interface{}{
		{"REM-001", "20/05/2026", "09:00", "Bayar tagihan listrik PLN",        "once",    "",  "pending",   "16/05/2026", "09:00", "", "", ""},
		{"REM-002", "25/05/2026", "10:00", "Transfer cicilan KPR BNI",         "once",    "",  "pending",   "16/05/2026", "09:01", "", "", ""},
		{"REM-003", "01/06/2026", "08:00", "Bayar premi asuransi jiwa",        "monthly", "1", "pending",   "16/05/2026", "09:02", "", "", ""},
		{"REM-004", "17/05/2026", "12:00", "Beli hadiah ulang tahun Ayah",     "once",    "",  "pending",   "16/05/2026", "09:03", "", "", ""},
		{"REM-005", "31/05/2026", "20:00", "Rekap pengeluaran bulan Mei 2026", "monthly", "31","pending",   "16/05/2026", "09:04", "", "", ""},
	})

	fmt.Println("\n✅ Seeding selesai! Buka spreadsheet untuk melihat hasilnya.")
	os.Exit(0)
}
