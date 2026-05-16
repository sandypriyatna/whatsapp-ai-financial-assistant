package app

import "strings"

// normalizeLower trims, lowercases, and collapses internal whitespace.
func normalizeLower(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// containsWord returns true if haystack contains the entire needle as a
// contiguous substring (case-insensitive, already normalised).
func containsWord(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}
