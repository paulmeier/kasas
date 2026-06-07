package api

import (
	"errors"
	"regexp"
	"strings"
)

// amountPattern matches a plain signed decimal: an optional sign followed by an
// integer part with an optional fractional part, or a bare fractional part. It
// deliberately rejects thousands separators, exponents, currency symbols, and any
// internal whitespace. kasas stores the exact decimal string and never parses it to
// a float (the stored string is authoritative — see events.DiffSnapshots), so the
// input must already be a clean decimal we can keep verbatim.
var amountPattern = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d+)?|\.\d+)$`)

// validateAmount checks that s is a well-formed signed decimal and returns it in
// canonical stored form: trimmed, with a redundant leading "+" removed. It
// preserves the user's exact digits and decimal places and never rounds or
// reformats, so "12.340" stays "12.340". An empty or malformed value is an error;
// callers wrap it as a validationError to map to 400.
func validateAmount(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("amount is required")
	}
	if len(s) > 40 {
		return "", errors.New("amount is too long")
	}
	if !amountPattern.MatchString(s) {
		return "", errors.New("amount must be a decimal number like -12.34")
	}
	return strings.TrimPrefix(s, "+"), nil
}
