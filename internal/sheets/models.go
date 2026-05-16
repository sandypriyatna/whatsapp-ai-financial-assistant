package sheets

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TransactionType string

const (
	Expense TransactionType = "Pengeluaran"
	Income  TransactionType = "Pemasukan"
)

// WIB is the fixed timezone used across the assistant.
var WIB = time.FixedZone("WIB", 7*60*60)

type Transaction struct {
	ID          string
	Date        time.Time
	Type        TransactionType
	Category    string
	Description string
	Amount      float64
}

// ToRow converts Transaction into monthly sheet row format:
// [ID, Tanggal, Waktu, Tipe, Kategori, Deskripsi, Jumlah]
func (t *Transaction) ToRow() []interface{} {
	return []interface{}{
		t.ID,
		t.Date.In(WIB).Format("02/01/2006"),
		t.Date.In(WIB).Format("15:04"),
		string(t.Type),
		t.Category,
		t.Description,
		t.Amount,
	}
}

// TransactionFromRow parses a sheet row back into a Transaction.
// Expected order: [ID, DD/MM/YYYY, HH:MM, Tipe, Kategori, Deskripsi, Jumlah]
func TransactionFromRow(row []interface{}) (*Transaction, error) {
	if len(row) < 7 {
		return nil, fmt.Errorf("invalid row: expected at least 7 columns, got %d", len(row))
	}

	id := cellString(row[0])
	dateStr := cellString(row[1])
	timeStr := cellString(row[2])
	txType := TransactionType(cellString(row[3]))
	category := cellString(row[4])
	description := cellString(row[5])

	amount, err := cellFloat64(row[6])
	if err != nil {
		return nil, fmt.Errorf("invalid amount at column 7: %w", err)
	}

	dt, err := time.ParseInLocation("02/01/2006 15:04", strings.TrimSpace(dateStr)+" "+strings.TrimSpace(timeStr), WIB)
	if err != nil {
		return nil, fmt.Errorf("invalid date/time: %w", err)
	}

	return &Transaction{
		ID:          id,
		Date:        dt,
		Type:        txType,
		Category:    category,
		Description: description,
		Amount:      amount,
	}, nil
}

type Note struct {
	Date    time.Time
	Content string
}

func (n *Note) ToRow() []interface{} {
	return []interface{}{
		n.Date.In(WIB).Format("02/01/2006"),
		n.Date.In(WIB).Format("15:04"),
		n.Content,
	}
}

type Budget struct {
	Category string
	Amount   float64
}

// TransactionFilter defines flexible search criteria for SearchTransactions.
// Zero values are ignored (i.e., not applied as filters).
type TransactionFilter struct {
	Keyword   string          // substring match on Description (case-insensitive)
	Category  string          // exact match (case-insensitive)
	DateFrom  time.Time       // inclusive lower bound (zero = no bound)
	DateTo    time.Time       // inclusive upper bound (zero = no bound)
	MinAmount float64         // 0 = no minimum
	MaxAmount float64         // 0 = no maximum
	TxType    TransactionType // "" = both
	Limit     int             // 0 = default 50
}

type IDGenerator struct {
	mu       sync.Mutex
	lastDate string
	counter  int
}

var globalIDGen = &IDGenerator{}

// Next generates a transaction ID in format YYYYMMDD-NNN.
// Counter resets when date changes (WIB).
func (g *IDGenerator) Next(t time.Time) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	dateStr := t.In(WIB).Format("20060102")
	if dateStr != g.lastDate {
		g.lastDate = dateStr
		g.counter = 0
	}
	g.counter++

	return fmt.Sprintf("%s-%03d", dateStr, g.counter)
}

// GenerateTransactionID is the public ID generator API.
func GenerateTransactionID(t time.Time) string {
	return globalIDGen.Next(t)
}

func cellString(v interface{}) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return strings.TrimSpace(fmt.Sprintf("%v", x))
	}
}

func cellFloat64(v interface{}) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case float32:
		return float64(x), nil
	case int:
		return float64(x), nil
	case int32:
		return float64(x), nil
	case int64:
		return float64(x), nil
	case uint:
		return float64(x), nil
	case uint32:
		return float64(x), nil
	case uint64:
		return float64(x), nil
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0, fmt.Errorf("empty string")
		}
		// tolerate Indonesian thousand separator in case values are returned as text
		s = strings.ReplaceAll(s, ".", "")
		s = strings.ReplaceAll(s, ",", ".")
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, err
		}
		return f, nil
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
