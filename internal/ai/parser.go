package ai

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// parseIDRAmount parses Indonesian currency formats into float64.
// Supported examples:
// - 16k, 16rb, 16ribu
// - 16.000
// - 16,5k
// - 1.5jt, 2juta
// - 1500000
// - Rp 50.000, IDR 50.000
func parseIDRAmount(s string) (float64, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return 0, fmt.Errorf("amount is empty")
	}

	// Remove known currency prefixes.
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "rp."))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "rp"))
	raw = strings.TrimSpace(strings.TrimPrefix(raw, "idr"))

	// Remove all whitespace.
	raw = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, raw)

	if raw == "" {
		return 0, fmt.Errorf("amount is empty")
	}

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
		return 0, fmt.Errorf("invalid amount: missing numeric value")
	}

	norm, err := normalizeNumber(raw)
	if err != nil {
		return 0, err
	}

	base, err := strconv.ParseFloat(norm, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", s)
	}

	amount := base * multiplier
	if amount <= 0 {
		return 0, fmt.Errorf("amount must be greater than zero")
	}

	return amount, nil
}

// formatIDR formats a numeric amount to Indonesian Rupiah style.
// Examples: Rp 16.000, Rp 16.500, Rp 1.500.000, Rp 1.234,56
func formatIDR(amount float64) string {
	sign := ""
	if amount < 0 {
		sign = "-"
		amount = -amount
	}

	// Keep two decimals max, avoid floating point noise.
	amount = math.Round(amount*100) / 100

	intPart := int64(amount)
	frac := int(math.Round((amount - float64(intPart)) * 100))

	intStr := withThousandDots(strconv.FormatInt(intPart, 10))

	if frac == 0 {
		return fmt.Sprintf("%sRp %s", sign, intStr)
	}

	return fmt.Sprintf("%sRp %s,%02d", sign, intStr, frac)
}

func normalizeNumber(s string) (string, error) {
	if s == "" {
		return "", fmt.Errorf("invalid amount")
	}

	hasDot := strings.Contains(s, ".")
	hasComma := strings.Contains(s, ",")

	switch {
	case hasDot && hasComma:
		lastDot := strings.LastIndex(s, ".")
		lastComma := strings.LastIndex(s, ",")
		if lastDot > lastComma {
			// Dot is decimal separator, comma is thousands separator.
			s = strings.ReplaceAll(s, ",", "")
		} else {
			// Comma is decimal separator, dot is thousands separator.
			s = strings.ReplaceAll(s, ".", "")
			s = strings.ReplaceAll(s, ",", ".")
		}

	case hasComma:
		// Could be decimal comma or thousands comma.
		if isGroupedThousands(s, ',') {
			s = strings.ReplaceAll(s, ",", "")
		} else {
			s = strings.ReplaceAll(s, ",", ".")
		}

	case hasDot:
		// Could be decimal dot or thousands dot.
		if isGroupedThousands(s, '.') {
			s = strings.ReplaceAll(s, ".", "")
		}
	}

	// Final validation: only digits and at most one dot.
	if s == "" {
		return "", fmt.Errorf("invalid amount")
	}
	dotCount := 0
	for _, r := range s {
		if r == '.' {
			dotCount++
			if dotCount > 1 {
				return "", fmt.Errorf("invalid amount")
			}
			continue
		}
		if !unicode.IsDigit(r) {
			return "", fmt.Errorf("invalid amount")
		}
	}

	return s, nil
}

// isGroupedThousands checks patterns like 16.000, 1.234.567 (or with comma).
func isGroupedThousands(s string, sep rune) bool {
	parts := strings.Split(s, string(sep))
	if len(parts) < 2 {
		return false
	}

	// First part 1-3 digits, next parts exactly 3 digits.
	if len(parts[0]) < 1 || len(parts[0]) > 3 || !allDigits(parts[0]) {
		return false
	}
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) != 3 || !allDigits(parts[i]) {
			return false
		}
	}
	return true
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func withThousandDots(s string) string {
	if len(s) <= 3 {
		return s
	}

	n := len(s)
	first := n % 3
	if first == 0 {
		first = 3
	}

	var b strings.Builder
	b.WriteString(s[:first])

	for i := first; i < n; i += 3 {
		b.WriteByte('.')
		b.WriteString(s[i : i+3])
	}

	return b.String()
}
