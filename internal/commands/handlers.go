package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/sanspriyatna/wa-finance/internal/finance"
	"github.com/sanspriyatna/wa-finance/internal/notes"
	"github.com/sanspriyatna/wa-finance/internal/reminder"
	"github.com/sanspriyatna/wa-finance/pkg/formatter"
)

type ContextKey string

const SenderContextKey ContextKey = "sender"

var ExpenseCategories = []string{
	"Makanan", "Transportasi", "Rumah Tangga", "Belanja",
	"Kesehatan", "Pendidikan", "Hiburan", "Fashion",
	"Komunikasi", "Perawatan", "Sosial", "Lainnya",
}

var IncomeCategories = []string{
	"Gaji", "Freelance", "Investasi", "Hadiah", "Transfer", "Lainnya",
}

func StartHandler(ctx context.Context, args string) string {
	_ = ctx
	_ = args
	return formatter.FormatWelcome()
}

func HelpHandler(ctx context.Context, args string) string {
	_ = ctx
	_ = args
	return formatter.FormatHelp()
}

func MenuHandler(ctx context.Context, args string) string {
	_ = ctx
	_ = args
	return formatter.FormatHelp()
}

func CategoryHandler(ctx context.Context, args string) string {
	_ = ctx
	_ = args
	return formatter.FormatCategories(ExpenseCategories, IncomeCategories)
}

type ExportHandlerFactory struct {
	sheetsID string
}

func NewExportHandlerFactory(sheetsID string) *ExportHandlerFactory {
	return &ExportHandlerFactory{sheetsID: sheetsID}
}

func (f *ExportHandlerFactory) Handler(ctx context.Context, args string) string {
	_ = ctx
	_ = args
	url := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s", f.sheetsID)
	return formatter.FormatExport(url)
}

type ReportHandlerFactory struct {
	finance *finance.FinanceService
}

func NewReportHandlerFactory(fin *finance.FinanceService) *ReportHandlerFactory {
	return &ReportHandlerFactory{finance: fin}
}

func (f *ReportHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.finance == nil {
		return formatter.FormatError("service laporan belum siap")
	}

	period := strings.TrimSpace(args)
	if period == "" {
		period = "hari ini"
	}

	report, err := f.finance.GenerateReport(ctx, period)
	if err != nil {
		return formatter.FormatError("Gagal membuat laporan: " + err.Error())
	}

	switch report.Period {
	case "daily":
		return formatter.FormatDailyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories)
	case "weekly":
		return formatter.FormatWeeklyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories)
	default:
		return formatter.FormatMonthlyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories)
	}
}

type BudgetHandlerFactory struct {
	finance *finance.FinanceService
}

func NewBudgetHandlerFactory(fin *finance.FinanceService) *BudgetHandlerFactory {
	return &BudgetHandlerFactory{finance: fin}
}

func (f *BudgetHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.finance == nil {
		return formatter.FormatError("service budget belum siap")
	}

	parts := strings.Fields(args)
	if len(parts) < 2 {
		return formatter.FormatError("Format: /budget [kategori] [jumlah]\nContoh: /budget Makanan 500000")
	}

	category := strings.TrimSpace(parts[0])
	if !isValidExpenseCategory(category) {
		return formatter.FormatError("Kategori tidak dikenal: " + category + "\nGunakan /kategori untuk melihat daftar.")
	}

	amount, err := parseAmount(parts[1])
	if err != nil {
		return formatter.FormatError("Jumlah tidak valid: " + parts[1])
	}

	if err := f.finance.SetBudget(ctx, category, amount); err != nil {
		return formatter.FormatError("Gagal mengatur budget: " + err.Error())
	}

	return formatter.FormatBudgetSet(category, amount)
}

type NotesHandlerFactory struct {
	notes *notes.NotesService
}

func NewNotesHandlerFactory(svc *notes.NotesService) *NotesHandlerFactory {
	return &NotesHandlerFactory{notes: svc}
}

func (f *NotesHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.notes == nil {
		return formatter.FormatError("service catatan belum siap")
	}

	content := strings.TrimSpace(args)
	if content == "" {
		return formatter.FormatError("Format: /notes [catatan]\nContoh: /notes beli kado ultah minggu depan")
	}

	if err := f.notes.SaveNote(ctx, content); err != nil {
		return formatter.FormatError("Gagal menyimpan catatan: " + err.Error())
	}

	return formatter.FormatNoteSaved(content)
}

type EditHandlerFactory struct {
	finance *finance.FinanceService
}

func NewEditHandlerFactory(fin *finance.FinanceService) *EditHandlerFactory {
	return &EditHandlerFactory{finance: fin}
}

func (f *EditHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.finance == nil {
		return formatter.FormatError("service edit belum siap")
	}

	parts := strings.Fields(args)
	if len(parts) < 3 {
		return formatter.FormatError("Format: /edit [ID] [field] [nilai]\nContoh: /edit 20260317-001 jumlah 20000\nField: jumlah, kategori, deskripsi")
	}

	id := parts[0]
	field := parts[1]
	value := strings.Join(parts[2:], " ")

	_, err := f.finance.EditTransaction(ctx, id, field, value)
	if err != nil {
		return formatter.FormatError(err.Error())
	}

	return formatter.FormatTransactionEdited(id, field, "", value)
}

type DeleteHandlerFactory struct {
	finance *finance.FinanceService
}

func NewDeleteHandlerFactory(fin *finance.FinanceService) *DeleteHandlerFactory {
	return &DeleteHandlerFactory{finance: fin}
}

func (f *DeleteHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.finance == nil {
		return formatter.FormatError("service hapus belum siap")
	}

	id := strings.TrimSpace(args)
	if id == "" {
		return formatter.FormatError("Format: /hapus [ID]\nContoh: /hapus 20260317-001")
	}

	if err := f.finance.DeleteTransaction(ctx, id); err != nil {
		return formatter.FormatError(err.Error())
	}

	return formatter.FormatTransactionDeleted(id)
}

type ReminderHandlerFactory struct {
	reminder *reminder.Service
}

func NewReminderHandlerFactory(svc *reminder.Service) *ReminderHandlerFactory {
	return &ReminderHandlerFactory{reminder: svc}
}

func (f *ReminderHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.reminder == nil {
		return formatter.FormatError("service reminder belum siap")
	}

	content := strings.TrimSpace(args)
	if content == "" {
		return formatter.FormatError("Format: /reminder [teks]\nContoh: /reminder tanggal 26 maret bayar vps contabo")
	}

	sender, _ := ctx.Value(SenderContextKey).(string)
	rem, err := f.reminder.CreateFromText(ctx, sender, content)
	if err != nil {
		return formatter.FormatError("Gagal membuat reminder: " + err.Error())
	}

	targetDate := rem.TargetDate.Format("02 Jan 2006")
	targetTime := rem.TargetTime
	if targetTime == "" {
		targetTime = "tanpa jam spesifik (diingatkan 3x/hari sampai selesai)"
	} else {
		targetTime += " WIB"
	}

	return fmt.Sprintf(
		"✅ *Pengingat disimpan!*\n\n🆔 ID: %s\n🗓️ Tanggal: %s\n🕒 Waktu: %s\n📝 %s\n\nJika sudah dilakukan, kirim: */done %s*",
		rem.ID,
		targetDate,
		targetTime,
		rem.Message,
		rem.ID,
	)
}

type DoneHandlerFactory struct {
	reminder *reminder.Service
}

func NewDoneHandlerFactory(svc *reminder.Service) *DoneHandlerFactory {
	return &DoneHandlerFactory{reminder: svc}
}

func (f *DoneHandlerFactory) Handler(ctx context.Context, args string) string {
	if f == nil || f.reminder == nil {
		return formatter.FormatError("service reminder belum siap")
	}

	parts := strings.Fields(strings.TrimSpace(args))
	if len(parts) < 1 {
		return formatter.FormatError("Format: /done [ID]\nContoh: /done RMD-20260318-090000-001")
	}

	id := parts[0]
	note := ""
	if len(parts) > 1 {
		note = strings.Join(parts[1:], " ")
	}

	rem, err := f.reminder.CompleteByID(ctx, id, note)
	if err != nil {
		return formatter.FormatError("Gagal menyelesaikan reminder: " + err.Error())
	}

	return fmt.Sprintf("✅ Reminder *%s* ditandai selesai.\n📝 %s", rem.ID, rem.Message)
}

func isValidExpenseCategory(category string) bool {
	c := strings.TrimSpace(category)
	for _, item := range ExpenseCategories {
		if strings.EqualFold(item, c) {
			return true
		}
	}
	return false
}

func parseAmount(input string) (float64, error) {
	s := strings.ToLower(strings.TrimSpace(input))
	s = strings.TrimPrefix(s, "rp")
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, " ", "")

	multiplier := 1.0
	switch {
	case strings.HasSuffix(s, "juta"):
		multiplier = 1_000_000
		s = strings.TrimSuffix(s, "juta")
	case strings.HasSuffix(s, "jt"):
		multiplier = 1_000_000
		s = strings.TrimSuffix(s, "jt")
	case strings.HasSuffix(s, "ribu"):
		multiplier = 1_000
		s = strings.TrimSuffix(s, "ribu")
	case strings.HasSuffix(s, "rb"):
		multiplier = 1_000
		s = strings.TrimSuffix(s, "rb")
	case strings.HasSuffix(s, "k"):
		multiplier = 1_000
		s = strings.TrimSuffix(s, "k")
	}

	if strings.Contains(s, ",") && strings.Contains(s, ".") {
		if strings.LastIndex(s, ",") > strings.LastIndex(s, ".") {
			s = strings.ReplaceAll(s, ".", "")
			s = strings.ReplaceAll(s, ",", ".")
		} else {
			s = strings.ReplaceAll(s, ",", "")
		}
	} else if strings.Contains(s, ",") {
		s = strings.ReplaceAll(s, ",", ".")
	} else if strings.Count(s, ".") >= 1 {
		parts := strings.Split(s, ".")
		grouped := true
		for i := 1; i < len(parts); i++ {
			if len(parts[i]) != 3 {
				grouped = false
				break
			}
		}
		if grouped {
			s = strings.ReplaceAll(s, ".", "")
		}
	}

	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	v *= multiplier
	if v <= 0 {
		return 0, fmt.Errorf("invalid amount")
	}
	return v, nil
}
