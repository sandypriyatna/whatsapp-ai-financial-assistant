package app

import (
	"sync"
	"time"

	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

// undoStore remembers the last committed transaction per sender so the user
// can say "batal yang tadi" / "undo" without knowing the ID.
//
// The entry expires after undoTTL to prevent accidental deletions long after
// the fact. Only the most recent transaction is stored per sender.
type undoStore struct {
	mu      sync.Mutex
	entries map[string]*undoEntry
}

type undoEntry struct {
	tx        *sheets.Transaction // shallow copy of the committed tx
	tabName   string              // which monthly tab it lives in
	rowIndex  int                 // 1-indexed row in the sheet (set after append)
	createdAt time.Time
}

const undoTTL = 5 * time.Minute

func newUndoStore() *undoStore {
	return &undoStore{
		entries: make(map[string]*undoEntry),
	}
}

// save stores the last transaction for a sender.
func (u *undoStore) save(sender string, tx *sheets.Transaction, tabName string, rowIndex int) {
	if sender == "" || tx == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()

	// Deep copy to avoid aliasing.
	txCopy := *tx
	u.entries[sender] = &undoEntry{
		tx:        &txCopy,
		tabName:   tabName,
		rowIndex:  rowIndex,
		createdAt: time.Now(),
	}
}

// pop retrieves and removes the undo entry. Returns nil if expired or not found.
func (u *undoStore) pop(sender string) *undoEntry {
	u.mu.Lock()
	defer u.mu.Unlock()

	entry, ok := u.entries[sender]
	if !ok {
		return nil
	}
	delete(u.entries, sender)
	if time.Since(entry.createdAt) > undoTTL {
		return nil
	}
	return entry
}

// isUndoIntent returns true when the user message looks like an undo request.
func isUndoIntent(text string) bool {
	normalized := normalizeLower(text)
	keywords := []string{
		"undo",
		"umdo",
		"unso",
		"undo ",
		"batal yang tadi",
		"batalin yang tadi",
		"hapus yang tadi",
		"hapus yang barusan",
		"cancel yang tadi",
		"yang tadi salah semua",
	}
	for _, kw := range keywords {
		if normalized == kw || containsWord(normalized, kw) {
			return true
		}
	}
	return false
}
