package formatter

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

func FormatExpenseRecorded(id, description, category string, amount float64) string {
	return fmt.Sprintf(
		"✅ *Pengeluaran tercatat!*\n\n📝 %s\n📂 %s\n💰 %s\n\n📅 %s\n\n_Kirim \"undo\" dalam 5 menit jika ada yang salah._\n\n—👮 Satpam Rekening",
		safe(description), safe(category), formatIDR(amount), nowWIBString(),
	)
}

func FormatIncomeRecorded(id, description, category string, amount float64) string {
	return fmt.Sprintf(
		"✅ *Pemasukan tercatat!*\n\n📝 %s\n📂 %s\n💰 %s\n\n📅 %s\n\n_Kirim \"undo\" dalam 5 menit jika ada yang salah._\n\n—👮 Satpam Rekening",
		safe(description), safe(category), formatIDR(amount), nowWIBString(),
	)
}

func FormatDailyReport(date string, totalIncome, totalExpense float64, categories map[string]float64) string {
	return formatReport("📊 *Laporan Harian*", date, totalIncome, totalExpense, categories)
}

func FormatWeeklyReport(dateRange string, totalIncome, totalExpense float64, categories map[string]float64) string {
	return formatReport("📊 *Laporan Mingguan*", dateRange, totalIncome, totalExpense, categories)
}

func FormatMonthlyReport(month string, totalIncome, totalExpense float64, categories map[string]float64) string {
	return formatReport("📊 *Laporan Bulanan*", month, totalIncome, totalExpense, categories)
}

func FormatWelcome() string {
	return "👋 *Halo! Saya Satpam Rekening!* 👮\n\n" +
		"Saya asisten yang akan menjaga dan mencatat setiap pergerakan uang Anda.\n\n" +
		"✨ *Fitur utama:*\n" +
		"• 🧠 Catat transaksi pakai bahasa natural\n" +
		"• 📊 Laporan harian, mingguan, bulanan\n" +
		"• 🎯 Budget per kategori + peringatan\n" +
		"• 📝 Catatan cepat & export Google Sheets\n\n" +
		"Ketik */help* untuk melihat daftar perintah."
}

func FormatHelp() string {
	return "📖 *Daftar Perintah:*\n\n" +
		"💰 *Keuangan*\n" +
		"• Kirim pesan seperti \"beli ayam crispy 16k\" untuk mencatat\n" +
		"• /laporan [hari ini|minggu ini|bulan ini] — Lihat laporan\n" +
		"• /budget [kategori] [jumlah] — Atur budget\n" +
		"• /edit [ID] [field] [nilai] — Edit transaksi\n" +
		"• /hapus [ID] — Hapus transaksi\n\n" +
		"📝 *Catatan*\n" +
		"• /notes [teks] — Simpan catatan cepat\n\n" +
		"⏰ *Pengingat*\n" +
		"• /reminder [teks] — Buat pengingat\n" +
		"• /done [ID] — Tandai sudah selesai\n\n" +
		"📂 *Lainnya*\n" +
		"• /kategori — Lihat daftar kategori\n" +
		"• /export — Dapatkan link Google Sheets\n" +
		"• /help — Tampilkan bantuan ini"
}

func FormatBudgetAlert(category string, budget, spent, remaining float64) string {
	if remaining >= 0 {
		return fmt.Sprintf(
			"⚠️ *Peringatan Budget!*\nKategori *%s* hampir habis.\nBudget: %s\nTerpakai: %s\nSisa: %s",
			safe(category), formatIDR(budget), formatIDR(spent), formatIDR(remaining),
		)
	}

	return fmt.Sprintf(
		"🚨 *Peringatan Budget!*\nKategori *%s* sudah melebihi budget!\nBudget: %s\nTerpakai: %s\nLebih: %s",
		safe(category), formatIDR(budget), formatIDR(spent), formatIDR(math.Abs(remaining)),
	)
}

func FormatBudgetSet(category string, amount float64) string {
	return fmt.Sprintf("✅ *Budget Diatur!*\n\nKategori *%s* diatur ke *%s* per bulan.", safe(category), formatIDR(amount))
}

func FormatNoteSaved(note string) string {
	return fmt.Sprintf("✅ *Catatan disimpan!*\n\n📝 \"%s\"", safe(note))
}

func FormatTransactionDeleted(id string) string {
	return fmt.Sprintf("✅ Transaksi *%s* berhasil dihapus.", safe(id))
}

func FormatTransactionEdited(id, field, oldValue, newValue string) string {
	return fmt.Sprintf(
		"✅ Transaksi *%s* diperbarui!\n📝 %s: %s → %s",
		safe(id), safe(field), safe(oldValue), safe(newValue),
	)
}

func FormatCategories(expenseCategories, incomeCategories []string) string {
	return fmt.Sprintf(
		"📂 *Daftar Kategori*\n\n💸 *Pengeluaran:*\n%s\n\n💵 *Pemasukan:*\n%s",
		"• "+strings.Join(expenseCategories, "\n• "),
		"• "+strings.Join(incomeCategories, "\n• "),
	)
}

func FormatExport(url string) string {
	return fmt.Sprintf("📊 *Data Keuangan*\n\n🔗 Link Google Sheets:\n%s", safe(url))
}

func FormatError(msg string) string {
	return fmt.Sprintf("❌ *Error:* %s", safe(msg))
}

func FormatConfirmation(description string, txType string, amount float64, category string) string {
	kind := strings.ToLower(strings.TrimSpace(txType))
	return fmt.Sprintf(
		"🤔 *Konfirmasi Catatan*\n\nSaya catat sebagai *%s*:\n📝 %s\n📂 %s\n💰 %s\n\nBenar? Balas *ya* atau *bukan*.",
		strings.Title(kind), safe(description), safe(category), formatIDR(amount),
	)
}

func FormatQuickSummary(todayExpense, monthExpense, monthIncome float64) string {
	net := monthIncome - monthExpense
	return fmt.Sprintf(
		"📈 *Kondisi Keuangan*\n• Pengeluaran hari ini: %s\n• Pengeluaran bulan ini: %s\n• Saldo bersih bulan ini: %s",
		formatIDR(todayExpense), formatIDR(monthExpense), formatIDR(net),
	)
}

func formatReport(title, period string, totalIncome, totalExpense float64, categories map[string]float64) string {
	net := totalIncome - totalExpense
	top := topCategories(categories, 5)

	var details strings.Builder
	if len(top) == 0 {
		details.WriteString("• Belum ada data kategori")
	} else {
		for _, c := range top {
			details.WriteString(fmt.Sprintf("• %s: %s\n", c.Name, formatIDR(c.Amount)))
		}
	}

	return fmt.Sprintf(
		"%s\n📅 %s\n\n💵 Total Pemasukan: %s\n💸 Total Pengeluaran: %s\n💰 Saldo Bersih: %s\n\n📂 *Rincian Kategori:*\n%s",
		title, safe(period), formatIDR(totalIncome), formatIDR(totalExpense), formatIDR(net), strings.TrimSpace(details.String()),
	)
}

type categoryAmount struct {
	Name   string
	Amount float64
}

func topCategories(m map[string]float64, n int) []categoryAmount {
	if len(m) == 0 || n <= 0 {
		return nil
	}

	items := make([]categoryAmount, 0, len(m))
	for k, v := range m {
		items = append(items, categoryAmount{Name: k, Amount: v})
	}

	sort.Slice(items, func(i, j int) bool {
		if items[i].Amount == items[j].Amount {
			return items[i].Name < items[j].Name
		}
		return items[i].Amount > items[j].Amount
	})

	if len(items) > n {
		items = items[:n]
	}
	return items
}

func formatIDR(amount float64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}

	amount = math.Round(amount)
	intPart := int64(amount)

	intText := withThousandDots(fmt.Sprintf("%d", intPart))
	return fmt.Sprintf("%sRp %s", sign, intText)
}

func withThousandDots(s string) string {
	if len(s) <= 3 {
		return s
	}
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}

	var b strings.Builder
	b.WriteString(s[:prefix])
	for i := prefix; i < len(s); i += 3 {
		b.WriteString(".")
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

func nowWIBString() string {
	wib := time.FixedZone("WIB", 7*60*60)
	return time.Now().In(wib).Format("02 Jan 2006 • 15:04 WIB")
}

func safe(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "-"
	}
	return s
}

// FormatAmountShort returns a compact IDR string without the "Rp " prefix
// for use inside longer formatted strings.
func FormatAmountShort(amount float64) string {
	if amount < 0 {
		return "-" + formatIDR(-amount)[3:] // strip "Rp "
	}
	full := formatIDR(amount)
	if len(full) > 3 {
		return full[3:] // strip "Rp "
	}
	return full
}
