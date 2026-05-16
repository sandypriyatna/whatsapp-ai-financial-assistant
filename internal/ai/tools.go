package ai

// ToolDefinitions contains OpenAI-compatible function-calling tool schemas.
var ToolDefinitions = []map[string]interface{}{
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "record_transaction",
			"description": "Catat transaksi keuangan (pengeluaran atau pemasukan)",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"expense", "income"},
						"description": "Jenis transaksi",
					},
					"amount": map[string]interface{}{
						"type":        "number",
						"description": "Jumlah dalam Rupiah",
					},
					"category": map[string]interface{}{
						"type":        "string",
						"description": "Kategori transaksi",
					},
					"description": map[string]interface{}{
						"type":        "string",
						"description": "Deskripsi singkat transaksi",
					},
					"date": map[string]interface{}{
						"type":        "string",
						"description": "Tanggal spesifik transaksi (format YYYY-MM-DD). Jika user menyebutkan waktu relatif seperti 'kemarin', 'lusa', atau 'tanggal 15', WAJIB HITUNG dan isi dengan format YYYY-MM-DD yang akurat berdasarkan [INFO SISTEM] Waktu saat ini. Jika sama sekali tidak ada petunjuk waktu, KOSONGKAN.",
					},
				},
				"required": []string{"type", "amount", "category", "description"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "get_report",
			"description": "Ambil laporan keuangan berdasarkan periode",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"period": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"daily", "weekly", "monthly"},
						"description": "Periode laporan",
					},
				},
				"required": []string{"period"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "set_budget",
			"description": "Atur budget bulanan untuk kategori tertentu",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"category": map[string]interface{}{
						"type":        "string",
						"description": "Kategori budget",
					},
					"amount": map[string]interface{}{
						"type":        "number",
						"description": "Nilai budget dalam Rupiah",
					},
				},
				"required": []string{"category", "amount"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "save_note",
			"description": "Simpan catatan cepat ke Notes",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]interface{}{
						"type":        "string",
						"description": "Isi catatan",
					},
				},
				"required": []string{"content"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "edit_transaction",
			"description": "Edit transaksi berdasarkan ID",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "ID transaksi (format: YYYYMMDD-NNN)",
					},
					"field": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"amount", "category", "description"},
						"description": "Field yang ingin diubah",
					},
					"value": map[string]interface{}{
						"type":        "string",
						"description": "Nilai baru untuk field",
					},
				},
				"required": []string{"id", "field", "value"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "delete_transaction",
			"description": "Hapus transaksi berdasarkan ID",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"id": map[string]interface{}{
						"type":        "string",
						"description": "ID transaksi (format: YYYYMMDD-NNN)",
					},
				},
				"required": []string{"id"},
			},
		},
	},
	{
		"type": "function",
		"function": map[string]interface{}{
			"name":        "search_transactions",
			"description": "Cari transaksi berdasarkan keyword, kategori, rentang tanggal, atau rentang jumlah. Gunakan tool ini ketika user ingin mencari, memfilter, atau menanyakan transaksi spesifik.",
			"parameters": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"keyword": map[string]interface{}{
						"type":        "string",
						"description": "Kata kunci pencarian pada deskripsi transaksi (opsional)",
					},
					"category": map[string]interface{}{
						"type":        "string",
						"description": "Filter berdasarkan kategori (opsional)",
					},
					"date_from": map[string]interface{}{
						"type":        "string",
						"description": "Tanggal mulai rentang (YYYY-MM-DD, opsional)",
					},
					"date_to": map[string]interface{}{
						"type":        "string",
						"description": "Tanggal akhir rentang (YYYY-MM-DD, opsional)",
					},
					"min_amount": map[string]interface{}{
						"type":        "number",
						"description": "Jumlah minimum (opsional)",
					},
					"max_amount": map[string]interface{}{
						"type":        "number",
						"description": "Jumlah maksimum (opsional)",
					},
					"tx_type": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"expense", "income", "all"},
						"description": "Filter jenis transaksi (default: all)",
					},
				},
			},
		},
	},
}

// RecordTransactionArgs represents arguments for record_transaction.
type RecordTransactionArgs struct {
	Type        string  `json:"type"` // expense | income
	Amount      float64 `json:"amount"`
	Category    string  `json:"category"`
	Description string  `json:"description"`
	Date        string  `json:"date"`
}

// GetReportArgs represents arguments for get_report.
type GetReportArgs struct {
	Period string `json:"period"` // daily | weekly | monthly
}

// SetBudgetArgs represents arguments for set_budget.
type SetBudgetArgs struct {
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
}

// SaveNoteArgs represents arguments for save_note.
type SaveNoteArgs struct {
	Content string `json:"content"`
}

// EditTransactionArgs represents arguments for edit_transaction.
type EditTransactionArgs struct {
	ID    string `json:"id"`
	Field string `json:"field"` // amount | category | description
	Value string `json:"value"`
}

// DeleteTransactionArgs represents arguments for delete_transaction.
type DeleteTransactionArgs struct {
	ID string `json:"id"`
}

// SearchTransactionArgs represents arguments for search_transactions.
type SearchTransactionArgs struct {
	Keyword   string  `json:"keyword"`
	Category  string  `json:"category"`
	DateFrom  string  `json:"date_from"`
	DateTo    string  `json:"date_to"`
	MinAmount float64 `json:"min_amount"`
	MaxAmount float64 `json:"max_amount"`
	TxType    string  `json:"tx_type"` // "expense" | "income" | "all"
}
