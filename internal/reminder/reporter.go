package reminder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

// scheduledReporter sends automatic daily/weekly/monthly financial summaries
// to the owner. It is driven by the same cron ticker as the reminder service.
//
// Schedule (WIB):
//   - Daily   → every day at 21:00
//   - Weekly  → every Monday at 10:00
type scheduledReporter struct {
	aiClient   *ai.LLMClient
	notifier   Notifier
	repo       sheets.SheetRepository
	recipients []string
	now        func() time.Time

	lastGreetingDate  string // "YYYYMMDD"
	lastAfternoonDate string // "YYYYMMDD"
	lastDailyDate     string // "YYYYMMDD"
	lastWeeklyDate    string // "YYYYWWW" (year+week)
	lastMonthlyDate   string // "YYYYMM"
}

func newScheduledReporter(notifier Notifier, repo sheets.SheetRepository, recipients []string, nowFn func() time.Time, aiClient *ai.LLMClient) *scheduledReporter {
	return &scheduledReporter{
		aiClient:   aiClient,
		notifier:   notifier,
		repo:       repo,
		recipients: recipients,
		now:        nowFn,
	}
}

// Tick is called every minute by the main cron loop. It checks whether any
// scheduled report is due and sends it exactly once per period.
func (sr *scheduledReporter) Tick(ctx context.Context) {
	if sr == nil || sr.notifier == nil || sr.repo == nil {
		return
	}

	now := sr.now()

	sr.maybeMorningGreeting(ctx, now)
	sr.maybeAfternoonReminder(ctx, now)
	sr.maybeDaily(ctx, now)
	sr.maybeWeekly(ctx, now)
	sr.maybeMonthly(ctx, now)
}

// --- morning greeting at 07:00 ---

func (sr *scheduledReporter) maybeMorningGreeting(ctx context.Context, now time.Time) {
	if now.Hour() != 7 || now.Minute() != 0 {
		return
	}
	key := now.Format("20060102")
	if sr.lastGreetingDate == key {
		return
	}
	sr.lastGreetingDate = key

	msg := sr.buildMorningGreeting(ctx)
	if msg == "" {
		return
	}
	for _, recip := range sr.recipients {
		_ = sr.notifier.SendText(ctx, recip, msg)
	}
}

func (sr *scheduledReporter) buildMorningGreeting(ctx context.Context) string {
	if sr.aiClient == nil {
		return "Selamat pagi! Semangat beraktivitas hari ini! 🚀"
	}

	systemPrompt := `Anda adalah asisten keuangan pribadi yang ramah dan inspiratif bernama "Intelijen Keuangan".
Tugas Anda adalah memberikan sapaan selamat pagi dan satu kutipan motivasi atau nasihat bijak singkat tentang kehidupan atau keuangan dalam bahasa Indonesia yang segar dan hangat.
Gunakan emoji secukupnya agar terlihat profesional tapi akrab.
Format output: Sapaan hangat + Motivasi/Quotes.`

	userMsg := "Berikan sapaan selamat pagi dan motivasi singkat yang segar untuk hari ini."
	resp, err := sr.aiClient.Chat(ctx, systemPrompt, userMsg)
	if err != nil || resp.Content == "" {
		return "Selamat pagi! Jangan lupa catat pengeluaranmu hari ini agar keuanganmu tetap sehat. Semangat! 💪"
	}

	return resp.Content
}

// --- afternoon reminder at 17:00 ---

func (sr *scheduledReporter) maybeAfternoonReminder(ctx context.Context, now time.Time) {
	if now.Hour() != 17 || now.Minute() != 0 {
		return
	}
	key := now.Format("20060102")
	if sr.lastAfternoonDate == key {
		return
	}
	sr.lastAfternoonDate = key

	msg := "👋 *Waktunya Catat Keuangan!*\n\nSudah sore nih, jangan lupa catat pemasukan dan pengeluaran kamu hari ini ya. Cukup ketik saja seperti biasa, contoh: _'makan siang 50rb'_ atau _'gajian 10jt'_.\n\nKeuangan yang tercatat adalah awal dari finansial yang sehat! 💰💪"
	for _, recip := range sr.recipients {
		_ = sr.notifier.SendText(ctx, recip, msg)
	}
}

// --- daily report at 21:00 ---

func (sr *scheduledReporter) maybeDaily(ctx context.Context, now time.Time) {
	if now.Hour() != 21 || now.Minute() != 0 {
		return
	}
	key := now.Format("20060102")
	if sr.lastDailyDate == key {
		return // already sent today
	}
	sr.lastDailyDate = key

	msg := sr.buildDailyReport(ctx, now)
	if msg == "" {
		return
	}
	for _, recip := range sr.recipients {
		_ = sr.notifier.SendText(ctx, recip, msg)
	}
}

func (sr *scheduledReporter) buildDailyReport(ctx context.Context, now time.Time) string {
	tabName := tabNameForWIB(now)
	txs, err := sr.repo.GetTransactions(ctx, tabName)
	if err != nil || len(txs) == 0 {
		return ""
	}

	today := now.Format("20060102")
	var income, expense float64
	cats := map[string]float64{}

	for _, tx := range txs {
		if tx.Date.In(sheets.WIB).Format("20060102") != today {
			continue
		}
		if tx.Type == sheets.Income {
			income += tx.Amount
		} else {
			expense += tx.Amount
			cats[tx.Category] += tx.Amount
		}
	}

	if income == 0 && expense == 0 {
		return "" // nothing to report
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 *Laporan Harian — %s*\n\n", now.Format("02 Jan 2006")))
	b.WriteString(fmt.Sprintf("💵 Pemasukan  : %s\n", fmtIDR(income)))
	b.WriteString(fmt.Sprintf("💸 Pengeluaran: %s\n", fmtIDR(expense)))
	b.WriteString(fmt.Sprintf("💰 Saldo Hari Ini: %s\n", fmtIDR(income-expense)))

	if len(cats) > 0 {
		b.WriteString("\n📂 *Pengeluaran per Kategori:*\n")
		for cat, amt := range cats {
			b.WriteString(fmt.Sprintf("  • %s: %s\n", cat, fmtIDR(amt)))
		}
	}

	// Add monthly so far summary
	var mIncome, mExpense float64
	for _, tx := range txs {
		if tx.Type == sheets.Income {
			mIncome += tx.Amount
		} else {
			mExpense += tx.Amount
		}
	}
	b.WriteString("\n──────────────────\n")
	b.WriteString(fmt.Sprintf("📅 *Ringkasan %s*\n", now.Format("Januari 2006")))
	b.WriteString(fmt.Sprintf("💰 Total Pemasukan: %s\n", fmtIDR(mIncome)))
	b.WriteString(fmt.Sprintf("💸 Total Pengeluaran: %s\n", fmtIDR(mExpense)))
	b.WriteString(fmt.Sprintf("📈 Saldo Berjalan: %s\n", fmtIDR(mIncome-mExpense)))

	return b.String()
}

// --- weekly report every Monday 10:00 ---

func (sr *scheduledReporter) maybeWeekly(ctx context.Context, now time.Time) {
	if now.Weekday() != time.Monday || now.Hour() != 10 || now.Minute() != 0 {
		return
	}
	yr, wk := now.ISOWeek()
	key := fmt.Sprintf("%d%02d", yr, wk)
	if sr.lastWeeklyDate == key {
		return
	}
	sr.lastWeeklyDate = key

	msg := sr.buildWeeklyReport(ctx, now)
	if msg == "" {
		return
	}
	for _, recip := range sr.recipients {
		_ = sr.notifier.SendText(ctx, recip, msg)
	}
}

func (sr *scheduledReporter) buildWeeklyReport(ctx context.Context, now time.Time) string {
	weekStart := now.AddDate(0, 0, -6) // last 7 days
	tabName := tabNameForWIB(now)
	txs, err := sr.repo.GetTransactions(ctx, tabName)
	if err != nil {
		return ""
	}

	var income, expense float64
	cats := map[string]float64{}

	for _, tx := range txs {
		d := tx.Date.In(sheets.WIB)
		if d.Before(weekStart) {
			continue
		}
		if tx.Type == sheets.Income {
			income += tx.Amount
		} else {
			expense += tx.Amount
			cats[tx.Category] += tx.Amount
		}
	}

	if income == 0 && expense == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 *Laporan Mingguan — %s s/d %s*\n\n",
		weekStart.Format("02 Jan"), now.Format("02 Jan 2006")))
	b.WriteString(fmt.Sprintf("💵 Pemasukan  : %s\n", fmtIDR(income)))
	b.WriteString(fmt.Sprintf("💸 Pengeluaran: %s\n", fmtIDR(expense)))
	b.WriteString(fmt.Sprintf("💰 Saldo Mingguan: %s\n", fmtIDR(income-expense)))

	if len(cats) > 0 {
		b.WriteString("\n📂 *Top Pengeluaran:*\n")
		for cat, amt := range cats {
			b.WriteString(fmt.Sprintf("  • %s: %s\n", cat, fmtIDR(amt)))
		}
	}
	return b.String()
}

// --- monthly report 1st of month 09:00 ---

func (sr *scheduledReporter) maybeMonthly(ctx context.Context, now time.Time) {
	if now.Day() != 1 || now.Hour() != 9 || now.Minute() != 0 {
		return
	}
	key := now.Format("200601")
	if sr.lastMonthlyDate == key {
		return
	}
	sr.lastMonthlyDate = key

	msg := sr.buildMonthlyReport(ctx, now)
	if msg == "" {
		return
	}
	for _, recip := range sr.recipients {
		_ = sr.notifier.SendText(ctx, recip, msg)
	}
}

func (sr *scheduledReporter) buildMonthlyReport(ctx context.Context, now time.Time) string {
	// Report on previous month.
	prevMonth := now.AddDate(0, -1, 0)
	tabName := tabNameForWIB(prevMonth)
	txs, err := sr.repo.GetTransactions(ctx, tabName)
	if err != nil || len(txs) == 0 {
		return ""
	}

	var income, expense float64
	cats := map[string]float64{}

	for _, tx := range txs {
		if tx.Type == sheets.Income {
			income += tx.Amount
		} else {
			expense += tx.Amount
			cats[tx.Category] += tx.Amount
		}
	}

	monthNames := map[time.Month]string{
		time.January: "Januari", time.February: "Februari", time.March: "Maret",
		time.April: "April", time.May: "Mei", time.June: "Juni",
		time.July: "Juli", time.August: "Agustus", time.September: "September",
		time.October: "Oktober", time.November: "November", time.December: "Desember",
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("📊 *Laporan Bulanan — %s %d*\n\n",
		monthNames[prevMonth.Month()], prevMonth.Year()))
	b.WriteString(fmt.Sprintf("💵 Total Pemasukan  : %s\n", fmtIDR(income)))
	b.WriteString(fmt.Sprintf("💸 Total Pengeluaran: %s\n", fmtIDR(expense)))
	b.WriteString(fmt.Sprintf("💰 Saldo Bersih     : %s\n", fmtIDR(income-expense)))
	b.WriteString(fmt.Sprintf("📈 Total Transaksi  : %d\n", len(txs)))

	if len(cats) > 0 {
		b.WriteString("\n📂 *Rincian Pengeluaran:*\n")
		for cat, amt := range cats {
			pct := 0.0
			if expense > 0 {
				pct = amt / expense * 100
			}
			b.WriteString(fmt.Sprintf("  • %s: %s (%.1f%%)\n", cat, fmtIDR(amt), pct))
		}
	}
	b.WriteString("\n_Laporan otomatis dari Intelijen Keuangan._")
	return b.String()
}

// tabNameForWIB returns the Indonesian month tab name for a given time.
func tabNameForWIB(t time.Time) string {
	t = t.In(sheets.WIB)
	monthNames := map[time.Month]string{
		time.January: "Januari", time.February: "Februari", time.March: "Maret",
		time.April: "April", time.May: "Mei", time.June: "Juni",
		time.July: "Juli", time.August: "Agustus", time.September: "September",
		time.October: "Oktober", time.November: "November", time.December: "Desember",
	}
	return fmt.Sprintf("%s %d", monthNames[t.Month()], t.Year())
}

// fmtIDR formats a float as "Rp X.XXX.XXX".
func fmtIDR(amount float64) string {
	if amount < 0 {
		return fmt.Sprintf("-Rp %s", thousandDots(fmt.Sprintf("%.0f", -amount)))
	}
	return fmt.Sprintf("Rp %s", thousandDots(fmt.Sprintf("%.0f", amount)))
}

func thousandDots(s string) string {
	if len(s) <= 3 {
		return s
	}
	prefix := len(s) % 3
	if prefix == 0 {
		prefix = 3
	}
	var sb strings.Builder
	sb.WriteString(s[:prefix])
	for i := prefix; i < len(s); i += 3 {
		sb.WriteString(".")
		sb.WriteString(s[i : i+3])
	}
	return sb.String()
}
