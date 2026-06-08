package csv

import (
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/source"
)

// dateLayouts are tried in order when a folder declares no explicit date_format.
// Slash/dash forms assume US month-first ordering; set Mapping.DateFormat for any
// other locale rather than guessing.
var dateLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	"2006/01/02",
	"01/02/2006",
	"1/2/2006",
	"01-02-2006",
	"02 Jan 2006",
	"Jan 2, 2006",
	"2006-01-02 15:04:05",
}

// headerAliases maps each logical field to the lowercased header names that
// commonly denote it, used to auto-detect columns when a folder omits its mapping.
var headerAliases = map[string][]string{
	"date":        {"date", "transaction date", "posted date", "posting date", "trans date"},
	"amount":      {"amount", "value"},
	"debit":       {"debit", "withdrawal", "withdrawals", "money out", "paid out"},
	"credit":      {"credit", "deposit", "deposits", "money in", "paid in"},
	"description": {"description", "name", "details", "narrative", "transaction"},
	"payee":       {"payee", "merchant", "to/from"},
	"memo":        {"memo", "notes", "note", "reference", "extended details"},
}

// resolvedMapping is a Mapping with column specs resolved to 0-based indices
// (-1 when absent) against a particular file's header.
type resolvedMapping struct {
	date, amount, debit, credit int
	description, payee, memo    int
	dateFormat                  string
}

// parseCSV reads CSV content and maps each data row to an ImportTxn using the
// folder's mapping, synthesizing a content-hash ExternalID so re-importing the
// same row is idempotent. Rows that cannot be mapped (unparseable date or amount)
// are skipped and counted rather than failing the whole file. It returns the
// transactions, the number of skipped rows, and an error only when the CSV itself
// is unreadable or its columns cannot be resolved.
func parseCSV(r io.Reader, f Folder) (txns []source.ImportTxn, skipped int, err error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1 // tolerate ragged rows
	reader.LazyQuotes = true
	reader.TrimLeadingSpace = true
	if d := []rune(f.Mapping.Delimiter); len(d) == 1 {
		reader.Comma = d[0]
	}

	records, err := reader.ReadAll()
	if err != nil {
		return nil, 0, fmt.Errorf("parse csv: %w", err)
	}
	if len(records) == 0 {
		return nil, 0, nil
	}

	hasHeader := f.Mapping.HasHeader == nil || *f.Mapping.HasHeader
	var header []string
	rows := records
	if hasHeader {
		header = records[0]
		rows = records[1:]
	}

	rm, err := resolveMapping(f.Mapping, header)
	if err != nil {
		return nil, 0, err
	}

	for _, rec := range rows {
		if isBlankRow(rec) {
			continue
		}
		t, ok := rowToTxn(rec, rm)
		if !ok {
			skipped++
			continue
		}
		t.ExternalID = contentHash(f.Name, t)
		txns = append(txns, t)
	}
	return txns, skipped, nil
}

// resolveMapping turns a folder's column specs (header names or numeric indices)
// into concrete indices for one file, auto-detecting from the header when the
// folder left a field unset. A configured spec that cannot be resolved is an
// error; an unresolved auto-detect is left as -1 (that field is simply omitted).
func resolveMapping(m Mapping, header []string) (resolvedMapping, error) {
	idx := headerIndex(header)
	rm := resolvedMapping{dateFormat: m.DateFormat}

	resolve := func(field, spec string) (int, error) {
		if strings.TrimSpace(spec) == "" {
			return autoDetect(field, idx), nil // -1 when not found
		}
		return resolveColumn(spec, idx, header)
	}

	var err error
	if rm.date, err = resolve("date", m.DateColumn); err != nil {
		return rm, fmt.Errorf("date_column: %w", err)
	}
	if rm.amount, err = resolve("amount", m.AmountColumn); err != nil {
		return rm, fmt.Errorf("amount_column: %w", err)
	}
	if rm.debit, err = resolve("debit", m.DebitColumn); err != nil {
		return rm, fmt.Errorf("debit_column: %w", err)
	}
	if rm.credit, err = resolve("credit", m.CreditColumn); err != nil {
		return rm, fmt.Errorf("credit_column: %w", err)
	}
	if rm.description, err = resolve("description", m.DescriptionColumn); err != nil {
		return rm, fmt.Errorf("description_column: %w", err)
	}
	if rm.payee, err = resolve("payee", m.PayeeColumn); err != nil {
		return rm, fmt.Errorf("payee_column: %w", err)
	}
	if rm.memo, err = resolve("memo", m.MemoColumn); err != nil {
		return rm, fmt.Errorf("memo_column: %w", err)
	}

	if rm.date < 0 {
		return rm, fmt.Errorf("could not find a date column (set date_column)")
	}
	if rm.amount < 0 && rm.debit < 0 && rm.credit < 0 {
		return rm, fmt.Errorf("could not find an amount column (set amount_column, or debit_column/credit_column)")
	}
	return rm, nil
}

// headerIndex maps each lowercased, trimmed header name to its column index.
func headerIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, h := range header {
		idx[strings.ToLower(strings.TrimSpace(h))] = i
	}
	return idx
}

// resolveColumn resolves one explicit spec to an index: a numeric spec is a
// 0-based column index; otherwise it is matched against the header by name
// (case-insensitive). A name spec without a header, or a negative index, is an
// error.
func resolveColumn(spec string, idx map[string]int, header []string) (int, error) {
	spec = strings.TrimSpace(spec)
	if n, err := strconv.Atoi(spec); err == nil {
		if n < 0 {
			return -1, fmt.Errorf("negative column index %d", n)
		}
		return n, nil
	}
	if len(header) == 0 {
		return -1, fmt.Errorf("column %q given by name but the file has no header (set has_header or use an index)", spec)
	}
	if i, ok := idx[strings.ToLower(spec)]; ok {
		return i, nil
	}
	return -1, fmt.Errorf("no column named %q in the header", spec)
}

// autoDetect finds a column for a logical field via its common header aliases,
// or -1 when none match.
func autoDetect(field string, idx map[string]int) int {
	for _, alias := range headerAliases[field] {
		if i, ok := idx[alias]; ok {
			return i
		}
	}
	return -1
}

// rowToTxn maps one record to an ImportTxn (without its ExternalID), reporting
// false when the row lacks a parseable date or amount.
func rowToTxn(rec []string, rm resolvedMapping) (source.ImportTxn, bool) {
	get := func(i int) string {
		if i < 0 || i >= len(rec) {
			return ""
		}
		return strings.TrimSpace(rec[i])
	}

	date, ok := parseDate(get(rm.date), rm.dateFormat)
	if !ok {
		return source.ImportTxn{}, false
	}
	amount, ok := resolveAmount(get(rm.amount), get(rm.debit), get(rm.credit), rm)
	if !ok {
		return source.ImportTxn{}, false
	}
	return source.ImportTxn{
		Amount:      amount,
		Date:        date,
		Description: get(rm.description),
		Payee:       get(rm.payee),
		Memo:        get(rm.memo),
	}, true
}

// resolveAmount produces the signed decimal amount string from either a single
// signed amount column or a debit/credit pair (credit is an inflow, debit an
// outflow). Reports false when no amount can be derived.
func resolveAmount(amount, debit, credit string, rm resolvedMapping) (string, bool) {
	if rm.amount >= 0 && amount != "" {
		if a, ok := cleanAmount(amount); ok {
			return a, true
		}
		return "", false
	}
	// Debit/credit pair: net = credit - debit. At least one side must be present.
	cr, crOK := cleanAmountValue(credit)
	dr, drOK := cleanAmountValue(debit)
	if !crOK && !drOK {
		return "", false
	}
	return formatAmount(cr - dr), true
}

// parseDate parses a date with the configured layout, or by trying the common
// layouts in order. It returns the date as unix seconds (UTC midnight for
// date-only layouts) and whether parsing succeeded.
func parseDate(value, layout string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if layout != "" {
		t, err := time.Parse(layout, value)
		if err != nil {
			return 0, false
		}
		return t.UTC().Unix(), true
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, value); err == nil {
			return t.UTC().Unix(), true
		}
	}
	return 0, false
}

// cleanAmount normalizes an amount string to a signed decimal, preserving the
// original digits as text (no float round-trip): it strips currency symbols,
// thousands separators, and spaces, and treats parentheses as a negative. Amounts
// are assumed to use '.' as the decimal separator (US-style). Reports false when
// no digits remain.
func cleanAmount(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg = true
		s = s[1 : len(s)-1]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.':
			b.WriteRune(r)
		case r == '-':
			neg = true
		default:
			// Drop everything else: currency symbols, thousands separators, spaces,
			// and plus signs.
		}
	}
	digits := b.String()
	if strings.Trim(digits, ".") == "" {
		return "", false
	}
	if neg {
		return "-" + digits, true
	}
	return digits, true
}

// cleanAmountValue parses one side of a debit/credit pair into a float magnitude,
// returning false for an empty/unparseable cell so a missing side is ignored.
func cleanAmountValue(s string) (float64, bool) {
	cleaned, ok := cleanAmount(s)
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(cleaned, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// formatAmount renders a debit/credit net as a plain decimal string. The
// debit/credit path is the only place kasas computes an amount, so a float is
// acceptable here (a single subtraction); the single-column path stays text-only.
func formatAmount(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// isBlankRow reports whether every field in a record is empty/whitespace.
func isBlankRow(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// contentHash derives a stable, namespaced transaction id from a folder profile
// plus the transaction's meaningful fields, so the same row always maps to the
// same id (idempotent re-import) and rows are deduplicated by content. The "csv:"
// prefix namespaces it away from other sources' ids.
func contentHash(profile string, t source.ImportTxn) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%d\x00%s\x00%s\x00%s\x00%s",
		profile, t.Date, t.Amount, t.Description, t.Payee, t.Memo)
	return "csv:" + hex.EncodeToString(h.Sum(nil))
}
