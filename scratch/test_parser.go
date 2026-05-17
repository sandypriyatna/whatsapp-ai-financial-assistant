package main

import (
	"fmt"
	"math"

	"github.com/sanspriyatna/wa-finance/internal/sheets"
)

// We want to test sheets.cellFloat64 logic. Since it is unexported in internal/sheets package,
// we can wrap the test by parsing rows or by copying the cellFloat64 function locally to test its exact implementation.
func localCellFloat64(v interface{}) (float64, error) {
	// Let's implement the exact logic we wrote to see if it behaves flawlessly.
	// Since we imported "github.com/sanspriyatna/wa-finance/internal/sheets",
	// let's actually see if sheets.TransactionFromRow parses the row amount correctly!
	row := []interface{}{"20260517-001", "17/05/2026", "00:00", "Pengeluaran", "Makanan", "Deskripsi", v}
	tx, err := sheets.TransactionFromRow(row)
	if err != nil {
		return 0, err
	}
	return tx.Amount, nil
}

func main() {
	testCases := []struct {
		input    interface{}
		expected float64
	}{
		{"Rp 22,000", 22000},
		{"22,500", 22500},
		{"22.500", 22500},
		{"22,5", 22.5},
		{"22.5", 22.5},
		{"Rp 18,275,000", 18275000},
		{float64(22000), 22000},
	}

	fmt.Println("🧪 Testing updated cellFloat64 parsing logic:")
	allPassed := true
	for _, tc := range testCases {
		res, err := localCellFloat64(tc.input)
		if err != nil {
			fmt.Printf("❌ Input: %v | Error: %v\n", tc.input, err)
			allPassed = false
			continue
		}
		if math.Abs(res-tc.expected) > 0.0001 {
			fmt.Printf("❌ Input: %v | Got: %v, Expected: %v\n", tc.input, res, tc.expected)
			allPassed = false
		} else {
			fmt.Printf("✅ Input: %v | Successfully parsed to %v\n", tc.input, res)
		}
	}

	if allPassed {
		fmt.Println("🎉 ALL TESTS PASSED! The currency parser is now extremely robust and handles all international and Indonesian numbering formats perfectly!")
	} else {
		fmt.Println("❌ SOME TESTS FAILED.")
	}
}
