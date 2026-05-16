package finance

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/ai"
	"github.com/sanspriyatna/wa-finance/internal/sheets"
	"github.com/sanspriyatna/wa-finance/pkg/formatter"
)

var monthNamesID = map[time.Month]string{
	time.January:   "Januari",
	time.February:  "Februari",
	time.March:     "Maret",
	time.April:     "April",
	time.May:       "Mei",
	time.June:      "Juni",
	time.July:      "Juli",
	time.August:    "Agustus",
	time.September: "September",
	time.October:   "Oktober",
	time.November:  "November",
	time.December:  "Desember",
}

var expenseCategoriesCanonical = []string{
	"Makanan", "Transportasi", "Rumah Tangga", "Belanja",
	"Kesehatan", "Pendidikan", "Hiburan", "Fashion",
	"Komunikasi", "Perawatan", "Sosial", "Lainnya",
}

var incomeCategoriesCanonical = []string{
	"Gaji", "Freelance", "Investasi", "Hadiah", "Transfer", "Lainnya",
}

var categoryAliases = map[string]string{
	"makanan & minuman": "Makanan",
	"makanan dan minuman": "Makanan",
	"food & beverage": "Makanan",
	"food and beverage": "Makanan",
	"f&b": "Makanan",
	"makan minum": "Makanan",
}

type FinanceService struct {
	repo sheets.SheetRepository
	ai   *ai.LLMClient
}

type ReportData struct {
	Period       string
	DateRange    string
	TotalIncome  float64
	TotalExpense float64
	NetBalance   float64
	Categories   map[string]float64
}

func NewFinanceService(repo sheets.SheetRepository, aiClient *ai.LLMClient) *FinanceService {
	return &FinanceService{
		repo: repo,
		ai:   aiClient,
	}
}

// RecordTransaction keeps backward compatibility and ignores budget alert output.
func (s *FinanceService) RecordTransaction(ctx context.Context, args *ai.RecordTransactionArgs) (*sheets.Transaction, error) {
	tx, _, err := s.RecordTransactionWithBudget(ctx, args)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// RecordTransactionWithBudget creates and stores one transaction to the current WIB month tab
// and returns optional budget alert text for expense transactions.
func (s *FinanceService) RecordTransactionWithBudget(ctx context.Context, args *ai.RecordTransactionArgs) (*sheets.Transaction, string, error) {
	if s == nil {
		return nil, "", fmt.Errorf("finance service is nil")
	}
	if s.repo == nil {
		return nil, "", fmt.Errorf("sheet repository is nil")
	}
	if args == nil {
		return nil, "", fmt.Errorf("record transaction args is nil")
	}
	if strings.TrimSpace(args.Description) == "" {
		return nil, "", fmt.Errorf("description is required")
	}
	if strings.TrimSpace(args.Category) == "" {
		return nil, "", fmt.Errorf("category is required")
	}
	if args.Amount <= 0 {
		return nil, "", fmt.Errorf("amount must be greater than zero")
	}

	now := nowWIB()
	if strings.TrimSpace(args.Date) != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", args.Date, sheets.WIB); err == nil {
			// Gunakan tanggal yang diparsing, tapi pertahankan jam/menit/detik saat ini.
			now = time.Date(parsed.Year(), parsed.Month(), parsed.Day(), now.Hour(), now.Minute(), now.Second(), now.Nanosecond(), sheets.WIB)
		}
	}
	tabName := tabNameForTime(now)

	if err := s.repo.EnsureTabExists(ctx, tabName); err != nil {
		return nil, "", fmt.Errorf("failed to ensure tab %q: %w", tabName, err)
	}

	txType := toTransactionType(args.Type)

	// ID is now generated atomically inside AppendTransaction (repository layer).
	tx := &sheets.Transaction{
		Date:        now,
		Type:        txType,
		Category:    normalizeCategoryForType(args.Category, txType),
		Description: strings.TrimSpace(args.Description),
		Amount:      args.Amount,
	}

	if err := s.repo.AppendTransaction(ctx, tx); err != nil {
		return nil, "", fmt.Errorf("failed to append transaction: %w", err)
	}


	var budgetAlert string
	if tx.Type == sheets.Expense {
		alert, err := s.CheckBudget(ctx, tx.Category)
		if err == nil {
			budgetAlert = alert
		}
	}

	return tx, budgetAlert, nil
}

func (s *FinanceService) SetBudget(ctx context.Context, category string, amount float64) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("sheet repository is nil")
	}
	category = strings.TrimSpace(category)
	if category == "" {
		return fmt.Errorf("kategori tidak boleh kosong")
	}
	if amount <= 0 {
		return fmt.Errorf("jumlah budget harus lebih dari 0")
	}
	return s.repo.SetBudget(ctx, category, amount)
}

func (s *FinanceService) CheckBudget(ctx context.Context, category string) (string, error) {
	if s == nil || s.repo == nil {
		return "", fmt.Errorf("sheet repository is nil")
	}
	category = normalizeCategoryForType(category, sheets.Expense)
	if category == "" {
		return "", nil
	}

	budget, err := s.repo.GetBudget(ctx, category)
	if err != nil {
		return "", err
	}
	if budget <= 0 {
		return "", nil
	}

	spent, err := s.repo.GetCategoryTotal(ctx, tabNameForTime(nowWIB()), category)
	if err != nil {
		return "", err
	}

	remaining := budget - spent
	if remaining < 0 {
		return formatter.FormatBudgetAlert(category, budget, spent, remaining), nil
	}
	if remaining <= budget*0.2 {
		return formatter.FormatBudgetAlert(category, budget, spent, remaining), nil
	}

	return "", nil
}

func (s *FinanceService) GetQuickSummary(ctx context.Context) (todayExpense, monthExpense, monthIncome float64, err error) {
	if s == nil || s.repo == nil {
		return 0, 0, 0, fmt.Errorf("sheet repository is nil")
	}

	now := nowWIB()
	tabName := tabNameForTime(now)

	txs, err := s.repo.GetTransactions(ctx, tabName)
	if err != nil {
		return 0, 0, 0, err
	}

	todayStr := now.Format("2006-01-02")
	for _, tx := range txs {
		if tx.Type == sheets.Expense {
			monthExpense += tx.Amount
			if tx.Date.In(sheets.WIB).Format("2006-01-02") == todayStr {
				todayExpense += tx.Amount
			}
		} else {
			monthIncome += tx.Amount
		}
	}
	return todayExpense, monthExpense, monthIncome, nil
}

func (s *FinanceService) GenerateReport(ctx context.Context, period string) (*ReportData, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("sheet repository is nil")
	}

	now := nowWIB()
	tabName := tabNameForTime(now)

	txs, err := s.repo.GetTransactions(ctx, tabName)
	if err != nil {
		return nil, fmt.Errorf("failed to load transactions: %w", err)
	}

	mode := normalizePeriod(period)
	filtered, dateRange, err := filterTransactionsByPeriod(txs, now, mode)
	if err != nil {
		return nil, err
	}

	report := &ReportData{
		Period:     mode,
		DateRange:  dateRange,
		Categories: map[string]float64{},
	}

	for _, tx := range filtered {
		if tx.Type == sheets.Income {
			report.TotalIncome += tx.Amount
			continue
		}
		report.TotalExpense += tx.Amount
		normalizedCategory := normalizeCategoryForType(tx.Category, sheets.Expense)
		report.Categories[normalizedCategory] += tx.Amount
	}

	report.Categories = topNCategories(report.Categories, 5)
	report.NetBalance = report.TotalIncome - report.TotalExpense

	return report, nil
}

func (s *FinanceService) EditTransaction(ctx context.Context, id, field, value string) (*sheets.Transaction, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("sheet repository is nil")
	}
	id = strings.TrimSpace(id)
	field = strings.ToLower(strings.TrimSpace(field))
	value = strings.TrimSpace(value)

	if id == "" {
		return nil, fmt.Errorf("id transaksi tidak boleh kosong")
	}
	if field == "" {
		return nil, fmt.Errorf("field tidak boleh kosong")
	}
	if value == "" {
		return nil, fmt.Errorf("nilai baru tidak boleh kosong")
	}

	tx, rowIndex, tabName, err := s.repo.GetTransactionByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("transaksi %s tidak ditemukan: %w", id, err)
	}

	switch field {
	case "jumlah", "amount":
		amount, err := parseIDRAmountLocal(value)
		if err != nil {
			return nil, fmt.Errorf("jumlah tidak valid: %w", err)
		}
		tx.Amount = amount
	case "kategori", "category":
		tx.Category = normalizeCategoryForType(value, tx.Type)
	case "deskripsi", "description":
		tx.Description = value
	default:
		return nil, fmt.Errorf("field tidak dikenal: %s. Gunakan: jumlah, kategori, atau deskripsi", field)
	}

	if err := s.repo.UpdateTransaction(ctx, tabName, rowIndex, tx); err != nil {
		return nil, fmt.Errorf("gagal memperbarui transaksi: %w", err)
	}

	return tx, nil
}

func (s *FinanceService) DeleteTransaction(ctx context.Context, id string) error {
	if s == nil || s.repo == nil {
		return fmt.Errorf("sheet repository is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("id transaksi tidak boleh kosong")
	}

	_, rowIndex, tabName, err := s.repo.GetTransactionByID(ctx, id)
	if err != nil {
		return fmt.Errorf("transaksi %s tidak ditemukan: %w", id, err)
	}

	if err := s.repo.DeleteTransaction(ctx, tabName, rowIndex); err != nil {
		return fmt.Errorf("gagal menghapus transaksi: %w", err)
	}

	return nil
}

func nowWIB() time.Time {
	return time.Now().In(sheets.WIB)
}

func tabNameForTime(t time.Time) string {
	monthName, ok := monthNamesID[t.In(sheets.WIB).Month()]
	if !ok {
		monthName = t.In(sheets.WIB).Month().String()
	}
	return fmt.Sprintf("%s %d", monthName, t.In(sheets.WIB).Year())
}

func toTransactionType(raw string) sheets.TransactionType {
	if strings.EqualFold(strings.TrimSpace(raw), "income") {
		return sheets.Income
	}
	return sheets.Expense
}


func normalizePeriod(period string) string {
	p := strings.ToLower(strings.TrimSpace(period))
	switch p {
	case "", "daily", "hari ini", "harian":
		return "daily"
	case "weekly", "minggu ini", "mingguan":
		return "weekly"
	case "monthly", "bulan ini", "bulanan":
		return "monthly"
	default:
		return p
	}
}

func filterTransactionsByPeriod(txs []sheets.Transaction, now time.Time, mode string) ([]sheets.Transaction, string, error) {
	switch mode {
	case "daily":
		todayKey := now.In(sheets.WIB).Format("20060102")
		var out []sheets.Transaction
		for _, tx := range txs {
			if tx.Date.In(sheets.WIB).Format("20060102") == todayKey {
				out = append(out, tx)
			}
		}
		return out, formatDateID(now), nil

	case "weekly":
		start := now.AddDate(0, 0, -6)
		startKey := start.In(sheets.WIB).Format("20060102")
		endKey := now.In(sheets.WIB).Format("20060102")
		var out []sheets.Transaction
		for _, tx := range txs {
			k := tx.Date.In(sheets.WIB).Format("20060102")
			if k >= startKey && k <= endKey {
				out = append(out, tx)
			}
		}
		return out, fmt.Sprintf("%s - %s", formatDateID(start), formatDateID(now)), nil

	case "monthly":
		return txs, fmt.Sprintf("%s %d", monthNamesID[now.Month()], now.Year()), nil

	default:
		return nil, "", fmt.Errorf("periode tidak dikenal: %s", mode)
	}
}

func topNCategories(in map[string]float64, n int) map[string]float64 {
	type kv struct {
		Key string
		Val float64
	}
	items := make([]kv, 0, len(in))
	for k, v := range in {
		items = append(items, kv{Key: k, Val: v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Val == items[j].Val {
			return items[i].Key < items[j].Key
		}
		return items[i].Val > items[j].Val
	})
	if len(items) > n {
		items = items[:n]
	}
	out := make(map[string]float64, len(items))
	for _, it := range items {
		out[it.Key] = it.Val
	}
	return out
}

func formatDateID(t time.Time) string {
	local := t.In(sheets.WIB)
	month := monthNamesID[local.Month()]
	return fmt.Sprintf("%02d %s %d", local.Day(), month, local.Year())
}

func normalizeCategoryForType(raw string, txType sheets.TransactionType) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "Lainnya"
	}

	key := strings.ToLower(s)
	if alias, ok := categoryAliases[key]; ok {
		s = alias
	}

	var canonical []string
	if txType == sheets.Income {
		canonical = incomeCategoriesCanonical
	} else {
		canonical = expenseCategoriesCanonical
	}

	for _, c := range canonical {
		if strings.EqualFold(c, s) {
			return c
		}
	}

	return "Lainnya"
}

func parseIDRAmountLocal(s string) (float64, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return 0, fmt.Errorf("nilai kosong")
	}

	raw = strings.TrimSpace(strings.TrimPrefix(raw, "rp."))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "rp"))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "idr"))
	raw = strings.ReplaceAll(raw, " ", "")

	multiplier := 1.0
	switch {
	case strings.HasSuffix(raw, "juta"):
		multiplier = 1_000_000
		raw = strings.TrimSuffix(raw, "juta")
	case strings.HasSuffix(raw, "jt"):
		multiplier = 1_000_000
		raw = strings.TrimSuffix(raw, "jt")
	case strings.HasSuffix(raw, "ribu"):
		multiplier = 1_000
		raw = strings.TrimSuffix(raw, "ribu")
	case strings.HasSuffix(raw, "rb"):
		multiplier = 1_000
		raw = strings.TrimSuffix(raw, "rb")
	case strings.HasSuffix(raw, "k"):
		multiplier = 1_000
		raw = strings.TrimSuffix(raw, "k")
	}

	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, fmt.Errorf("format jumlah tidak valid")
	}

	if strings.Contains(raw, ",") && strings.Contains(raw, ".") {
		if strings.LastIndex(raw, ",") > strings.LastIndex(raw, ".") {
			raw = strings.ReplaceAll(raw, ".", "")
			raw = strings.ReplaceAll(raw, ",", ".")
		} else {
			raw = strings.ReplaceAll(raw, ",", "")
		}
	} else if strings.Contains(raw, ",") {
		raw = strings.ReplaceAll(raw, ",", ".")
	} else if strings.Count(raw, ".") >= 1 {
		parts := strings.Split(raw, ".")
		grouped := true
		for i := 1; i < len(parts); i++ {
			if len(parts[i]) != 3 {
				grouped = false
				break
			}
		}
		if grouped {
			raw = strings.ReplaceAll(raw, ".", "")
		}
	}

	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, fmt.Errorf("format jumlah tidak valid")
	}
	v *= multiplier

	if v <= 0 {
		return 0, fmt.Errorf("jumlah harus lebih dari 0")
	}

	return v, nil
}

// SearchTransactions executes a flexible transaction search via the repository.
// It translates the ai.SearchTransactionArgs into sheets.TransactionFilter and
// returns a formatted WhatsApp message with up to 20 results.
func (s *FinanceService) SearchTransactions(ctx context.Context, args *ai.SearchTransactionArgs) (string, error) {
	if s == nil || s.repo == nil {
		return "", fmt.Errorf("service belum siap")
	}
	if args == nil {
		return "", fmt.Errorf("args kosong")
	}

	filter := sheets.TransactionFilter{
		Keyword:  strings.TrimSpace(args.Keyword),
		Category: strings.TrimSpace(args.Category),
		Limit:    20,
	}

	// Parse date boundaries.
	wib := sheets.WIB
	if args.DateFrom != "" {
		if t, err := time.ParseInLocation("2006-01-02", args.DateFrom, wib); err == nil {
			filter.DateFrom = t
		}
	}
	if args.DateTo != "" {
		if t, err := time.ParseInLocation("2006-01-02", args.DateTo, wib); err == nil {
			filter.DateTo = t
		}
	}

	// Amount range.
	filter.MinAmount = args.MinAmount
	filter.MaxAmount = args.MaxAmount

	// Transaction type.
	switch strings.ToLower(strings.TrimSpace(args.TxType)) {
	case "expense":
		filter.TxType = sheets.Expense
	case "income":
		filter.TxType = sheets.Income
	}

	txs, err := s.repo.SearchTransactions(ctx, filter)
	if err != nil {
		return "", fmt.Errorf("pencarian gagal: %w", err)
	}

	return formatSearchResults(txs, args), nil
}

func formatSearchResults(txs []sheets.Transaction, args *ai.SearchTransactionArgs) string {
	if len(txs) == 0 {
		return "🔍 Tidak ada transaksi yang cocok dengan pencarian kamu."
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("🔍 *Hasil Pencarian* (%d transaksi)\n\n", len(txs)))

	var totalAmount float64
	for i, tx := range txs {
		icon := "💸"
		if tx.Type == sheets.Income {
			icon = "💵"
		}
		b.WriteString(fmt.Sprintf(
			"%d. %s `%s` — %s\n   📂 %s • %s • Rp %s\n",
			i+1,
			icon,
			tx.ID,
			tx.Description,
			tx.Category,
			tx.Date.In(sheets.WIB).Format("02 Jan 2006"),
			formatter.FormatAmountShort(tx.Amount),
		))
		totalAmount += tx.Amount
	}

	b.WriteString(fmt.Sprintf("\n💰 Total: Rp %s", formatter.FormatAmountShort(totalAmount)))
	if len(txs) >= 20 {
		b.WriteString("\n\n_Hanya menampilkan 20 hasil pertama._")
	}
	return b.String()
}
