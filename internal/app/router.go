package app

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
	"github.com/sanspriyatna/wa-finance/internal/commands"
	"github.com/sanspriyatna/wa-finance/internal/finance"
	"github.com/sanspriyatna/wa-finance/internal/notes"
	"github.com/sanspriyatna/wa-finance/internal/reminder"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
	"github.com/sanspriyatna/wa-finance/pkg/formatter"
)

const defaultPendingTTL = 5 * time.Minute

// defaultSystemPrompt — injected as "system" at the start of every LLM call.
// Runtime: augmented with live WIB timestamp + sliding-window history.
const defaultSystemPrompt = `Kamu adalah *Satpam Rekening* 👮 — asisten keuangan pribadi milik sanspriyatna yang terintegrasi dengan WhatsApp.

ATURAN UTAMA:
1. Selalu jawab dalam Bahasa Indonesia yang santai tapi tegas (ala satpam penjaga rekening).
2. Gunakan emoji minimalis (maksimal 1-2 per pesan) agar tetap profesional.
3. Jika user menyebut membeli/bayar/beli/keluar/habis → PENGELUARAN (expense). 💸
4. Jika user menyebut terima/gaji/dapat/masuk/transfer masuk → PEMASUKAN (income). 💵
5. Parse nominal dari format Indonesia: "16k"=16000, "1.5jt"=1.500.000, "50rb"=50.000, "16.000"=16.000.
6. Jika pesan BUKAN tentang keuangan → jawab sebagai asisten AI yang helpful (general chat). 🤝
7. Gunakan kategori HANYA dari daftar resmi di bawah. Jangan buat kategori baru.
8. Jika satu pesan mengandung BEBERAPA transaksi, panggil record_transaction BEBERAPA KALI sekaligus.
9. Jika ada riwayat percakapan sebelumnya, gunakan konteksnya untuk memahami referensi seperti "yang tadi", "itu", "update jadi".
10. Jangan tanya klarifikasi jika kamu sudah bisa menentukan type, amount, dan category dengan yakin.
11. Jika tidak yakin antara expense/income atau jumlahnya tidak jelas, baru tanya klarifikasi.
12. Untuk pencarian transaksi, gunakan tool search_transactions dengan filter yang tepat. 🔍

KATEGORI RESMI:
Pengeluaran → Makanan | Transportasi | Rumah Tangga | Belanja | Kesehatan | Pendidikan | Hiburan | Fashion | Komunikasi | Perawatan | Sosial | Lainnya
Pemasukan   → Gaji | Freelance | Investasi | Hadiah | Transfer | Lainnya

CONTOH POLA PESAN:
- "beli sate 30rb" → record_transaction(expense, 30000, Makanan, "Sate")
- "gaji bulan ini 8jt" → record_transaction(income, 8000000, Gaji, "Gaji bulan ini")
- "makan pizza 85k, grab 25k" → 2x record_transaction sekaligus
- "yang tadi salah, harusnya 45rb bukan 30rb" → edit_transaction berdasarkan konteks
- "berapa pengeluaran makanan minggu ini?" → search_transactions(category=Makanan, date_from=..., date_to=...)
- "cari transaksi grab" → search_transactions(keyword="grab")

TOOLS YANG TERSEDIA:
- record_transaction: Catat pengeluaran atau pemasukan
- get_report: Buat laporan keuangan (harian/mingguan/bulanan)
- set_budget: Atur budget per kategori
- save_note: Simpan catatan cepat
- edit_transaction: Edit transaksi (berdasarkan ID)
- delete_transaction: Hapus transaksi (berdasarkan ID)
- search_transactions: Cari/filter transaksi berdasarkan keyword, kategori, tanggal, atau jumlah`

type PendingAction struct {
	Transaction *ai.RecordTransactionArgs
	CreatedAt   time.Time
}

type transactionExecResult struct {
	ID          string
	Description string
	Category    string
	Amount      float64
	IsIncome    bool
	When        time.Time
	TabName     string
	RowIndex    int
}

type AppRouter struct {
	cmdRouter       *commands.Router
	llmClient       *ai.LLMClient
	financeService  *finance.FinanceService
	notesService    *notes.NotesService
	reminderService *reminder.Service
	repo            sheets.SheetRepository // for undo row lookup

	pendingActions sync.Map // map[string]*PendingAction
	pendingTTL     time.Duration
	systemPrompt   string

	conversation *conversationStore
	rateLimiter  *rateLimiter
	undoStore    *undoStore

	// dashboardMonth tracks which month the Dashboard was last refreshed for.
	dashboardMu    sync.Mutex
	dashboardMonth string // "YYYYMM"
}

func NewAppRouter(
	cmdRouter *commands.Router,
	llmClient *ai.LLMClient,
	financeService *finance.FinanceService,
	notesService *notes.NotesService,
	reminderService ...*reminder.Service,
) *AppRouter {
	var remSvc *reminder.Service
	if len(reminderService) > 0 {
		remSvc = reminderService[0]
	}

	return &AppRouter{
		cmdRouter:       cmdRouter,
		llmClient:       llmClient,
		financeService:  financeService,
		notesService:    notesService,
		reminderService: remSvc,
		pendingTTL:      defaultPendingTTL,
		systemPrompt:    defaultSystemPrompt,
		conversation:    newConversationStore(),
		rateLimiter:     newRateLimiter(),
		undoStore:       newUndoStore(),
	}
}

// SetRepo wires the sheet repository for undo operations. Call after construction
// when the repo is available.
func (r *AppRouter) SetRepo(repo sheets.SheetRepository) {
	r.repo = repo
}

func (r *AppRouter) HandleMessage(ctx context.Context, sender string, text string) string {
	if r == nil {
		return formatter.FormatError("Router belum siap.")
	}

	ctx = context.WithValue(ctx, commands.SenderContextKey, sender)

	sender = strings.TrimSpace(sender)
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// 0) Rate limiter — before any processing.
	if !r.rateLimiter.allow(sender) {
		return "⏳ Terlalu banyak pesan. Tunggu sebentar ya sebelum kirim lagi."
	}

	// Periodic maintenance (cheap, lock-protected).
	r.rateLimiter.pruneExpiredBuckets()

	// 1) Check pending confirmation flow.
	if pendingRaw, ok := r.pendingActions.Load(sender); ok {
		pending, _ := pendingRaw.(*PendingAction)
		if pending == nil || pending.Transaction == nil {
			r.pendingActions.Delete(sender)
		} else if time.Since(pending.CreatedAt) > r.pendingTTL {
			r.pendingActions.Delete(sender)
		} else {
			lower := strings.ToLower(text)
			switch lower {
			case "ya", "yes", "y", "iya", "ok", "oke", "benar":
				r.pendingActions.Delete(sender)
				resp := r.executeTransaction(ctx, sender, pending.Transaction)
				r.conversation.addTurn(sender, text, resp)
				return resp
			case "bukan", "tidak", "no", "n", "cancel", "batal":
				r.pendingActions.Delete(sender)
				resp := "❌ Dibatalkan."
				r.conversation.addTurn(sender, text, resp)
				return resp
			default:
				r.pendingActions.Delete(sender)
			}
		}
	}

	// 1.5) Undo last transaction.
	if isUndoIntent(text) {
		resp := r.handleUndo(ctx, sender)
		r.conversation.addTurn(sender, text, resp)
		return resp
	}

	// 2) Slash commands (no history needed).
	if r.cmdRouter != nil {
		if response, matched := r.cmdRouter.Route(ctx, text); matched {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(text)), "/start") {
				r.conversation.clear(sender)
			}
			return response
		}
	}

	// 2.5) Natural reminder intent shortcut (keyword detection before LLM).
	if r.reminderService != nil {
		if resp, handled := r.tryHandleNaturalReminder(ctx, sender, text); handled {
			r.conversation.addTurn(sender, text, resp)
			return resp
		}
	}

	// 3) Auto-refresh dashboard on first message of a new month.
	r.maybeRefreshDashboard(ctx)

	// 4) LLM route with multi-turn history.
	if r.llmClient == nil {
		return formatter.FormatError("Layanan AI belum siap.")
	}
	return r.routeWithLLM(ctx, sender, text)
}

// maybeRefreshDashboard updates Dashboard formulas once per calendar month.
func (r *AppRouter) maybeRefreshDashboard(ctx context.Context) {
	if r.repo == nil {
		return
	}
	key := time.Now().In(sheets.WIB).Format("200601")

	r.dashboardMu.Lock()
	if r.dashboardMonth == key {
		r.dashboardMu.Unlock()
		return
	}
	r.dashboardMonth = key
	r.dashboardMu.Unlock()

	// Run in goroutine — dashboard refresh should not block message handling.
	go func() {
		_ = r.repo.RefreshDashboard(context.Background())
	}()
}

// handleUndo deletes the last transaction committed by this sender (if within TTL).
func (r *AppRouter) handleUndo(ctx context.Context, sender string) string {
	entry := r.undoStore.pop(sender)
	if entry == nil {
		return "❓ Tidak ada transaksi terbaru yang bisa di-undo, atau sudah melewati batas waktu 5 menit."
	}

	if r.repo == nil {
		return formatter.FormatError("Repository belum siap untuk undo.")
	}

	// Look up the actual current row index (row might have shifted after sort).
	txs, err := r.repo.GetTransactions(ctx, entry.tabName)
	if err != nil {
		return formatter.FormatError("Gagal membaca data: " + err.Error())
	}

	rowIndex := -1
	for i, tx := range txs {
		if tx.ID == entry.tx.ID {
			rowIndex = i + 2 // 1-indexed, +1 header
			break
		}
	}
	if rowIndex < 2 {
		return fmt.Sprintf("⚠️ Transaksi `%s` tidak ditemukan lagi (mungkin sudah dihapus).", entry.tx.ID)
	}

	if err := r.repo.DeleteTransaction(ctx, entry.tabName, rowIndex); err != nil {
		return formatter.FormatError("Gagal undo: " + err.Error())
	}

	return fmt.Sprintf("↩️ *Transaksi di-undo!*\n\n🆔 `%s` — %s (Rp %s) telah dihapus.",
		entry.tx.ID, entry.tx.Description, formatter.FormatAmountShort(entry.tx.Amount))
}

// routeWithLLM builds the full message history and calls the LLM.
func (r *AppRouter) routeWithLLM(ctx context.Context, sender, text string) string {
	systemContent := fmt.Sprintf("%s\n\n[INFO SISTEM]\nWaktu saat ini: %s\nTimezone: WIB (UTC+7)",
		r.systemPrompt,
		time.Now().In(sheets.WIB).Format("02 January 2006, 15:04 WIB"),
	)

	messages := []ai.Message{
		{Role: "system", Content: systemContent},
	}
	messages = append(messages, r.conversation.getHistory(sender)...)
	messages = append(messages, ai.Message{Role: "user", Content: text})

	llmResp, err := r.llmClient.ChatWithHistory(ctx, messages)
	if err != nil {
		return formatter.FormatError("Maaf, sedang ada gangguan. Coba lagi nanti ya.")
	}

	if len(llmResp.ToolCalls) > 0 {
		response := r.handleToolCalls(ctx, sender, llmResp.ToolCalls)
		r.conversation.addTurn(sender, text, response)
		return response
	}

	content := strings.TrimSpace(llmResp.Content)
	if content == "" {
		return formatter.FormatError("Tidak ada respons dari AI. Coba lagi ya.")
	}
	r.conversation.addTurn(sender, text, content)
	return content
}

func (r *AppRouter) handleToolCalls(ctx context.Context, sender string, calls []ai.ToolCall) string {
	var responses []string
	var txResults []transactionExecResult
	var budgetAlerts []string

	for _, call := range calls {
		switch call.Name {
		case "record_transaction":
			var args ai.RecordTransactionArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data transaksi tidak valid."))
				continue
			}
			result, budgetAlert, err := r.executeTransactionResult(ctx, sender, &args)
			if err != nil {
				responses = append(responses, formatter.FormatError("Gagal mencatat: "+err.Error()))
				continue
			}
			txResults = append(txResults, result)
			if strings.TrimSpace(budgetAlert) != "" {
				budgetAlerts = append(budgetAlerts, budgetAlert)
			}

		case "get_report":
			if r.financeService == nil {
				responses = append(responses, formatter.FormatError("Service laporan belum siap."))
				continue
			}
			var args ai.GetReportArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data laporan tidak valid."))
				continue
			}
			report, err := r.financeService.GenerateReport(ctx, args.Period)
			if err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			switch normalizeReportPeriod(args.Period, report.Period) {
			case "weekly":
				responses = append(responses, formatter.FormatWeeklyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories))
			case "monthly":
				responses = append(responses, formatter.FormatMonthlyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories))
			default:
				responses = append(responses, formatter.FormatDailyReport(report.DateRange, report.TotalIncome, report.TotalExpense, report.Categories))
			}

		case "set_budget":
			if r.financeService == nil {
				responses = append(responses, formatter.FormatError("Service budget belum siap."))
				continue
			}
			var args ai.SetBudgetArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data budget tidak valid."))
				continue
			}
			if err := r.financeService.SetBudget(ctx, args.Category, args.Amount); err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			responses = append(responses, formatter.FormatBudgetSet(args.Category, args.Amount))

		case "save_note":
			var args ai.SaveNoteArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data catatan tidak valid."))
				continue
			}
			if r.reminderService != nil && isReminderIntent(args.Content) {
				rem, err := r.reminderService.CreateFromText(ctx, sender, args.Content)
				if err != nil {
					responses = append(responses, formatter.FormatError("Gagal membuat reminder: "+err.Error()))
					continue
				}
				responses = append(responses, formatReminderCreatedMessage(rem))
				continue
			}
			if r.notesService == nil {
				responses = append(responses, formatter.FormatError("Service catatan belum siap."))
				continue
			}
			if err := r.notesService.SaveNote(ctx, args.Content); err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			responses = append(responses, formatter.FormatNoteSaved(args.Content))

		case "edit_transaction":
			if r.financeService == nil {
				responses = append(responses, formatter.FormatError("Service edit belum siap."))
				continue
			}
			var args ai.EditTransactionArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data edit tidak valid."))
				continue
			}
			_, err := r.financeService.EditTransaction(ctx, args.ID, args.Field, args.Value)
			if err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			responses = append(responses, formatter.FormatTransactionEdited(args.ID, args.Field, "", args.Value))

		case "delete_transaction":
			if r.financeService == nil {
				responses = append(responses, formatter.FormatError("Service hapus belum siap."))
				continue
			}
			var args ai.DeleteTransactionArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data hapus tidak valid."))
				continue
			}
			if err := r.financeService.DeleteTransaction(ctx, args.ID); err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			responses = append(responses, formatter.FormatTransactionDeleted(args.ID))

		case "search_transactions":
			if r.financeService == nil {
				responses = append(responses, formatter.FormatError("Service pencarian belum siap."))
				continue
			}
			var args ai.SearchTransactionArgs
			if err := json.Unmarshal(call.Arguments, &args); err != nil {
				responses = append(responses, formatter.FormatError("Format data pencarian tidak valid."))
				continue
			}
			result, err := r.financeService.SearchTransactions(ctx, &args)
			if err != nil {
				responses = append(responses, formatter.FormatError(err.Error()))
				continue
			}
			responses = append(responses, result)
		}
	}

	if len(txResults) > 0 {
		responses = append([]string{formatCompactTransactionSummary(txResults)}, responses...)
	}
	if len(budgetAlerts) > 0 {
		responses = append(responses, strings.Join(budgetAlerts, "\n\n"))
	}
	if len(responses) == 0 {
		return formatter.FormatError("Tidak dapat memproses permintaan.")
	}

	return strings.Join(responses, "\n\n")
}

// executeTransaction is used for the pending-confirmation flow.
func (r *AppRouter) executeTransaction(ctx context.Context, sender string, args *ai.RecordTransactionArgs) string {
	result, budgetAlert, err := r.executeTransactionResult(ctx, sender, args)
	if err != nil {
		return formatter.FormatError("Gagal mencatat: " + err.Error())
	}

	var response string
	if result.IsIncome {
		response = formatter.FormatIncomeRecorded(result.ID, result.Description, result.Category, result.Amount)
	} else {
		response = formatter.FormatExpenseRecorded(result.ID, result.Description, result.Category, result.Amount)
	}
	if strings.TrimSpace(budgetAlert) != "" {
		response += "\n\n" + budgetAlert
	}
	return response
}

func (r *AppRouter) executeTransactionResult(ctx context.Context, sender string, args *ai.RecordTransactionArgs) (transactionExecResult, string, error) {
	if r.financeService == nil {
		return transactionExecResult{}, "", fmt.Errorf("service transaksi belum siap")
	}
	if args == nil {
		return transactionExecResult{}, "", fmt.Errorf("data transaksi kosong")
	}

	tx, budgetAlert, err := r.financeService.RecordTransactionWithBudget(ctx, args)
	if err != nil {
		return transactionExecResult{}, "", err
	}

	result := transactionExecResult{
		ID:          tx.ID,
		Description: tx.Description,
		Category:    tx.Category,
		Amount:      tx.Amount,
		IsIncome:    strings.EqualFold(strings.TrimSpace(args.Type), "income"),
		When:        tx.Date,
	}

	// Save to undo store so user can immediately undo if needed.
	// Row index will be re-resolved at undo time via a sheet scan.
	r.undoStore.save(sender, tx, "", 0)

	return result, budgetAlert, nil
}

func formatCompactTransactionSummary(results []transactionExecResult) string {
	if len(results) == 0 {
		return formatter.FormatError("Tidak ada transaksi yang dapat ditampilkan.")
	}

	allIncome := true
	allExpense := true
	for _, res := range results {
		if res.IsIncome {
			allExpense = false
		} else {
			allIncome = false
		}
	}

	title := "✅ *Transaksi berhasil dicatat!*"
	if allExpense {
		title = "✅ *Pengeluaran tercatat!*"
	} else if allIncome {
		title = "✅ *Pemasukan tercatat!*"
	}

	var b strings.Builder
	b.WriteString(title)

	totalAmount := 0.0
	for _, item := range results {
		b.WriteString("\n\n📝 ")
		b.WriteString(item.Description)
		b.WriteString("\n📂 ")
		b.WriteString(item.Category)
		b.WriteString("\n💰 ")
		b.WriteString(formatIDRCompact(item.Amount))
		totalAmount += item.Amount
	}

	if len(results) > 1 {
		icon := "💰"
		label := "Total"
		if allExpense {
			icon = "💸"
			label = "Total Pengeluaran"
		} else if allIncome {
			icon = "💵"
			label = "Total Pemasukan"
		}
		b.WriteString("\n\n")
		b.WriteString(icon)
		b.WriteString(" ")
		b.WriteString(label)
		b.WriteString(": ")
		b.WriteString(formatIDRCompact(totalAmount))
	}

	last := results[len(results)-1].When.In(time.FixedZone("WIB", 7*60*60))
	b.WriteString("\n\n📅 ")
	b.WriteString(last.Format("02 Jan 2006 • 15:04 WIB"))
	b.WriteString("\n\n_Kirim \"undo\" dalam 5 menit jika ada yang salah._")
	b.WriteString("\n\n...\n👮 Satpam Rekening")
	return b.String()
}

func formatIDRCompact(amount float64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}
	intPart := int64(math.Round(amount))
	return sign + "Rp " + withThousandDotsCompact(fmt.Sprintf("%d", intPart))
}

func withThousandDotsCompact(s string) string {
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

func (r *AppRouter) tryHandleNaturalReminder(ctx context.Context, sender string, text string) (string, bool) {
	if r == nil || r.reminderService == nil {
		return "", false
	}
	if !isReminderIntent(text) {
		return "", false
	}
	rem, err := r.reminderService.CreateFromText(ctx, sender, text)
	if err != nil {
		return formatter.FormatError("Gagal membuat reminder: " + err.Error()), true
	}
	return formatReminderCreatedMessage(rem), true
}

func isReminderIntent(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if t == "" {
		return false
	}
	keywords := []string{
		"ingetin", "ingatkan", "pengingat", "reminder", "jangan lupa", "tolong ingatkan",
	}
	for _, k := range keywords {
		if strings.Contains(t, k) {
			return true
		}
	}
	return false
}

func formatReminderCreatedMessage(rem *sheets.Reminder) string {
	if rem == nil {
		return formatter.FormatError("Reminder tidak valid.")
	}
	targetDate := rem.TargetDate.Format("02 Jan 2006")
	targetTime := rem.TargetTime
	if targetTime == "" {
		targetTime = "tanpa jam spesifik (3x/hari sampai selesai)"
	} else {
		targetTime += " WIB"
	}
	return fmt.Sprintf(
		"✅ *Pengingat disimpan!* 🔔\n\n🆔 ID: %s\n🗓️ Tanggal: %s\n🕒 Waktu: %s\n📝 %s\n\nJika sudah dilakukan, kirim: */done %s* ✅",
		rem.ID, targetDate, targetTime, rem.Message, rem.ID,
	)
}

func normalizeReportPeriod(raw string, fallback string) string {
	p := strings.ToLower(strings.TrimSpace(raw))
	switch p {
	case "daily", "harian", "hari ini":
		return "daily"
	case "weekly", "mingguan", "minggu ini":
		return "weekly"
	case "monthly", "bulanan", "bulan ini":
		return "monthly"
	}
	fb := strings.ToLower(strings.TrimSpace(fallback))
	switch fb {
	case "daily", "weekly", "monthly":
		return fb
	default:
		return "daily"
	}
}
