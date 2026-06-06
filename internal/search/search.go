// Package search implements kasas's transaction query language: a small but
// robust syntax for matching transactions on any stored field and on arbitrary
// combinations of labels.
//
// It is deliberately dependency-light (standard library only) so it can be
// shared by every consumer: the WebAssembly dashboard runs it in the browser
// against the already-fetched transaction set (instant results), and the REST
// API and MCP server run it server-side. It therefore knows nothing about the
// database or HTTP layers — callers adapt their own transaction type into a
// neutral [Record] and ask a parsed [Query] to [Query.Match] it.
//
// # Grammar
//
//	expr      := orExpr
//	orExpr    := andExpr ( ("OR" | "|") andExpr )*
//	andExpr   := notExpr ( ("AND" | "&" | <implicit>) notExpr )*
//	notExpr   := ("NOT" | "-")* primary
//	primary   := "(" orExpr ")" | term
//	term      := bareWord | "quoted phrase" | field ":" value
//
// Adjacent terms are implicitly AND-ed. Boolean keywords are case-insensitive.
// See the package README / the dashboard's help modal for the user-facing field
// and operator reference; the authoritative list of field names lives in
// [reservedFields].
package search

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Record is the neutral view of a transaction the matcher evaluates. Callers
// populate it from their own representation (db.Transaction, the dashboard's
// transaction DTO, ...). Amount is the parsed numeric value (for amount:
// comparisons); AmountRaw is the original string (for free-text matching and
// display). Times are compared as-is, so callers should use a consistent zone
// (kasas uses UTC throughout).
type Record struct {
	ID          string
	AccountID   string
	AccountName string
	Amount      float64
	AmountRaw   string
	Pending     bool
	Date        time.Time
	Description string
	Payee       string
	Memo        string
	Labels      map[string]string
	// Extensions is the transaction's schema extensions for matching: keys
	// lowercased and values stringified (a JSON string by its text, any other JSON
	// value by its compact encoding). Populated by callers from the stored object.
	Extensions map[string]string
	SyncedAt   time.Time
}

// Query is a parsed search expression. A Query parsed from an empty (or
// whitespace-only) input matches every record, which is what makes a blank or
// restored-but-empty search show everything rather than nothing.
type Query struct {
	root node
	text string
}

// String returns the original query text.
func (q *Query) String() string {
	if q == nil {
		return ""
	}
	return q.text
}

// Match reports whether the record satisfies the query.
func (q *Query) Match(r Record) bool {
	if q == nil || q.root == nil {
		return true
	}
	return q.root.eval(r)
}

// --- AST ---

type node interface{ eval(Record) bool }

type andNode struct{ a, b node }

func (n andNode) eval(r Record) bool { return n.a.eval(r) && n.b.eval(r) }

type orNode struct{ a, b node }

func (n orNode) eval(r Record) bool { return n.a.eval(r) || n.b.eval(r) }

type notNode struct{ a node }

func (n notNode) eval(r Record) bool { return !n.a.eval(r) }

// predNode wraps a leaf predicate built from a single term.
type predNode struct{ fn func(Record) bool }

func (n predNode) eval(r Record) bool { return n.fn(r) }

// --- Lexer ---

type tokenKind int

const (
	tEOF tokenKind = iota
	tLParen
	tRParen
	tAnd
	tOr
	tNot
	tTerm
)

// token is one lexed unit. For a tTerm, field is the lowercased field name ("" =
// free-text), value is the operand with the field prefix removed (leaf quotes
// are resolved later, per field, since e.g. a label value is quoted after its
// own = split). quoted marks a term that began as a "quoted phrase" so it is
// always free text, never a field:value.
type token struct {
	kind   tokenKind
	field  string
	value  string
	quoted bool
	raw    string
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// lex turns the input into a flat token stream. Whitespace separates terms;
// parentheses are structural; double quotes group spaces into a single term.
func lex(input string) ([]token, error) {
	var toks []token
	for i, n := 0, len(input); i < n; {
		c := input[i]
		if isSpace(c) {
			i++
			continue
		}
		switch c {
		case '(':
			toks = append(toks, token{kind: tLParen, raw: "("})
			i++
			continue
		case ')':
			toks = append(toks, token{kind: tRParen, raw: ")"})
			i++
			continue
		}
		raw, next, err := readRawTerm(input, i)
		if err != nil {
			return nil, err
		}
		i = next
		more, err := classify(raw)
		if err != nil {
			return nil, err
		}
		toks = append(toks, more...)
	}
	return toks, nil
}

// readRawTerm reads one whitespace/paren-delimited unit starting at i, keeping
// quote characters in the result (so classify can tell a quoted phrase from a
// field:value) but letting quotes protect spaces and parentheses.
func readRawTerm(s string, i int) (string, int, error) {
	var b strings.Builder
	inQuote := false
	for n := len(s); i < n; i++ {
		c := s[i]
		if inQuote {
			b.WriteByte(c)
			if c == '"' {
				inQuote = false
			}
			continue
		}
		if c == '"' {
			inQuote = true
			b.WriteByte(c)
			continue
		}
		if isSpace(c) || c == '(' || c == ')' {
			break
		}
		b.WriteByte(c)
	}
	if inQuote {
		return "", i, fmt.Errorf("unterminated quoted string")
	}
	return b.String(), i, nil
}

// classify turns a raw term into one or more tokens. A bare "|"/"&" or the words
// OR/AND/NOT (case-insensitive, unquoted) are operators; a leading "-" is a NOT
// applied to the rest; everything else is a tTerm.
func classify(raw string) ([]token, error) {
	switch raw {
	case "|":
		return []token{{kind: tOr, raw: raw}}, nil
	case "&":
		return []token{{kind: tAnd, raw: raw}}, nil
	}
	switch strings.ToUpper(raw) {
	case "OR":
		return []token{{kind: tOr, raw: raw}}, nil
	case "AND":
		return []token{{kind: tAnd, raw: raw}}, nil
	case "NOT":
		return []token{{kind: tNot, raw: raw}}, nil
	}
	if strings.HasPrefix(raw, "-") {
		rest := raw[1:]
		if rest == "" {
			return []token{{kind: tNot, raw: "-"}}, nil
		}
		t, err := classifyTerm(rest)
		if err != nil {
			return nil, err
		}
		return []token{{kind: tNot, raw: "-"}, t}, nil
	}
	t, err := classifyTerm(raw)
	if err != nil {
		return nil, err
	}
	return []token{t}, nil
}

// classifyTerm builds a tTerm, splitting an unquoted `field:value` on its first
// colon when the prefix is an identifier. A term that opens with a quote is a
// free-text phrase and is never treated as field:value.
func classifyTerm(raw string) (token, error) {
	if strings.HasPrefix(raw, `"`) {
		v, err := dequote(raw)
		if err != nil {
			return token{}, err
		}
		return token{kind: tTerm, value: v, quoted: true, raw: raw}, nil
	}
	if i := strings.IndexByte(raw, ':'); i > 0 && isFieldName(raw[:i]) {
		return token{kind: tTerm, field: strings.ToLower(raw[:i]), value: raw[i+1:], raw: raw}, nil
	}
	return token{kind: tTerm, value: raw, raw: raw}, nil
}

// isFieldName reports whether s is a bare identifier usable as a field prefix
// (letters, digits, underscore). Anything fancier must use the label: form with
// a quoted key.
func isFieldName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// dequote strips one layer of surrounding double quotes; unquoted input is
// returned unchanged.
func dequote(s string) (string, error) {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1], nil
	}
	return s, nil
}

func mustDequote(s string) string { v, _ := dequote(s); return v }

// --- Parser ---

type parseErr struct{ msg string }

type parser struct {
	toks []token
	pos  int
}

func (p *parser) fail(format string, a ...any) {
	panic(parseErr{fmt.Sprintf(format, a...)})
}

func (p *parser) peek() token {
	if p.pos < len(p.toks) {
		return p.toks[p.pos]
	}
	return token{kind: tEOF}
}

func (p *parser) next() token {
	t := p.peek()
	p.pos++
	return t
}

// Parse compiles a query string into a [Query]. An empty input yields a query
// that matches everything. A syntax error (unbalanced parentheses, a dangling
// operator, an invalid amount/date, ...) is returned with a human-readable
// message suitable for showing inline.
func Parse(input string) (q *Query, err error) {
	toks, lerr := lex(input)
	if lerr != nil {
		return nil, lerr
	}
	if len(toks) == 0 {
		return &Query{text: input}, nil
	}
	p := &parser{toks: toks}
	defer func() {
		if r := recover(); r != nil {
			if pe, ok := r.(parseErr); ok {
				q, err = nil, fmt.Errorf("%s", pe.msg)
				return
			}
			panic(r)
		}
	}()
	root := p.parseOr()
	if t := p.peek(); t.kind != tEOF {
		if t.kind == tRParen {
			p.fail("unexpected ')'")
		}
		p.fail("unexpected input near %q", t.raw)
	}
	return &Query{root: root, text: input}, nil
}

func (p *parser) parseOr() node {
	left := p.parseAnd()
	for p.peek().kind == tOr {
		p.next()
		left = orNode{left, p.parseAnd()}
	}
	return left
}

func (p *parser) parseAnd() node {
	left := p.parseNot()
	for {
		switch p.peek().kind {
		case tAnd:
			p.next()
			left = andNode{left, p.parseNot()}
		case tTerm, tNot, tLParen: // adjacency = implicit AND
			left = andNode{left, p.parseNot()}
		default:
			return left
		}
	}
}

func (p *parser) parseNot() node {
	neg := false
	for p.peek().kind == tNot {
		p.next()
		neg = !neg
	}
	n := p.parsePrimary()
	if neg {
		return notNode{n}
	}
	return n
}

func (p *parser) parsePrimary() node {
	t := p.peek()
	switch t.kind {
	case tLParen:
		p.next()
		n := p.parseOr()
		if p.peek().kind != tRParen {
			p.fail("missing ')'")
		}
		p.next()
		return n
	case tTerm:
		p.next()
		n, err := predFromTerm(t)
		if err != nil {
			p.fail("%s", err.Error())
		}
		return n
	case tOr, tAnd:
		p.fail("unexpected %q", t.raw)
	case tRParen:
		p.fail("unexpected ')'")
	case tEOF:
		p.fail("unexpected end of query")
	}
	p.fail("unexpected input")
	return nil
}

// --- Predicate builders ---

type cmpOp int

const (
	opEq cmpOp = iota
	opNe
	opGt
	opGe
	opLt
	opLe
	opContains
)

// predFromTerm turns one term token into a leaf predicate. The switch over
// t.field enumerates every built-in ("reserved") field name and is the single
// source of truth for them; any other field is treated as a label shorthand
// (`category:food` == `label:category=food`), so adding a built-in case here is
// what stops that name being silently shadowed by a label.
func predFromTerm(t token) (node, error) {
	if t.field == "" {
		needle := strings.ToLower(t.value)
		return predNode{fn: func(r Record) bool { return freeTextMatch(r, needle) }}, nil
	}
	switch t.field {
	case "description":
		return textPred(t.value, func(r Record) string { return r.Description })
	case "payee":
		return textPred(t.value, func(r Record) string { return r.Payee })
	case "memo":
		return textPred(t.value, func(r Record) string { return r.Memo })
	case "account":
		return textPred(t.value, func(r Record) string { return r.AccountName })
	case "id":
		return textPred(t.value, func(r Record) string { return r.ID })
	case "amount":
		return amountPred(t.value)
	case "date":
		return datePred(t.value, func(r Record) time.Time { return r.Date })
	case "synced":
		return datePred(t.value, func(r Record) time.Time { return r.SyncedAt })
	case "pending":
		return pendingPred(t.value)
	case "label":
		return labelPred(t.value)
	case "ext":
		return extPred(t.value)
	default:
		// Unreserved field => label shorthand `key:value` (exact, case-insensitive).
		v := strings.TrimSpace(mustDequote(t.value))
		if v == "" {
			return nil, fmt.Errorf("empty value for %q (did you mean label:%s for key presence?)", t.field, t.field)
		}
		return labelEqPred(normalizeKey(t.field), v, opEq), nil
	}
}

// freeTextMatch matches a bare term as a case-insensitive substring of any
// human-meaningful field, including label keys and values.
func freeTextMatch(r Record, needle string) bool {
	if needle == "" {
		return true
	}
	for _, s := range []string{r.Description, r.Payee, r.Memo, r.AccountName, r.ID} {
		if strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	for k, v := range r.Labels {
		if strings.Contains(strings.ToLower(k), needle) ||
			strings.Contains(strings.ToLower(v), needle) ||
			strings.Contains(strings.ToLower(k+":"+v), needle) {
			return true
		}
	}
	for k, v := range r.Extensions {
		if strings.Contains(strings.ToLower(k), needle) || strings.Contains(strings.ToLower(v), needle) {
			return true
		}
	}
	return false
}

func textPred(raw string, get func(Record) string) (node, error) {
	v, err := dequote(raw)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(strings.TrimSpace(v))
	if needle == "" {
		return nil, fmt.Errorf("empty search value")
	}
	return predNode{fn: func(r Record) bool {
		return strings.Contains(strings.ToLower(get(r)), needle)
	}}, nil
}

func pendingPred(raw string) (node, error) {
	switch strings.ToLower(strings.TrimSpace(mustDequote(raw))) {
	case "true", "yes", "y", "t", "1":
		return predNode{fn: func(r Record) bool { return r.Pending }}, nil
	case "false", "no", "n", "f", "0":
		return predNode{fn: func(r Record) bool { return !r.Pending }}, nil
	}
	return nil, fmt.Errorf("invalid pending value %q (use true or false)", raw)
}

func amountPred(raw string) (node, error) {
	val := strings.TrimSpace(raw)
	if val == "" {
		return nil, fmt.Errorf("empty amount filter")
	}
	if lo, hi, ok := splitRange(val); ok {
		hasLo, hasHi := lo != "", hi != ""
		if !hasLo && !hasHi {
			return nil, fmt.Errorf("empty amount range")
		}
		var l, h float64
		var err error
		if hasLo {
			if l, err = parseAmount(lo); err != nil {
				return nil, err
			}
		}
		if hasHi {
			if h, err = parseAmount(hi); err != nil {
				return nil, err
			}
		}
		if hasLo && hasHi && l > h {
			l, h = h, l
		}
		return predNode{fn: func(r Record) bool {
			if hasLo && r.Amount < l {
				return false
			}
			if hasHi && r.Amount > h {
				return false
			}
			return true
		}}, nil
	}
	op, rest := splitOp(val)
	num, err := parseAmount(rest)
	if err != nil {
		return nil, err
	}
	return predNode{fn: func(r Record) bool { return cmpFloat(r.Amount, num, op) }}, nil
}

// parseAmount parses a decimal amount, tolerating thousands separators, matching
// the dashboard's lenient amount handling.
func parseAmount(s string) (float64, error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount %q", strings.TrimSpace(s))
	}
	return f, nil
}

func cmpFloat(a, b float64, op cmpOp) bool {
	switch op {
	case opEq:
		return a == b
	case opNe:
		return a != b
	case opGt:
		return a > b
	case opGe:
		return a >= b
	case opLt:
		return a < b
	case opLe:
		return a <= b
	}
	return false
}

func datePred(raw string, get func(Record) time.Time) (node, error) {
	val := strings.TrimSpace(raw)
	if val == "" {
		return nil, fmt.Errorf("empty date filter")
	}
	if lo, hi, ok := splitRange(val); ok {
		hasLo, hasHi := lo != "", hi != ""
		if !hasLo && !hasHi {
			return nil, fmt.Errorf("empty date range")
		}
		var start, end time.Time
		var err error
		if hasLo {
			if start, _, err = parsePeriod(lo); err != nil {
				return nil, err
			}
		}
		if hasHi {
			if _, end, err = parsePeriod(hi); err != nil {
				return nil, err
			}
		}
		return predNode{fn: func(r Record) bool {
			d := get(r)
			if hasLo && d.Before(start) {
				return false
			}
			if hasHi && !d.Before(end) {
				return false
			}
			return true
		}}, nil
	}
	op, rest := splitOp(val)
	start, end, err := parsePeriod(rest)
	if err != nil {
		return nil, err
	}
	return predNode{fn: func(r Record) bool {
		d := get(r)
		switch op {
		case opEq:
			return !d.Before(start) && d.Before(end)
		case opNe:
			return d.Before(start) || !d.Before(end)
		case opGt:
			return !d.Before(end) // strictly after the whole period
		case opGe:
			return !d.Before(start)
		case opLt:
			return d.Before(start)
		case opLe:
			return d.Before(end)
		}
		return false
	}}, nil
}

// parsePeriod parses a partial date (YYYY, YYYY-MM, or YYYY-MM-DD) into the
// half-open interval [start, end) it denotes, in UTC.
func parsePeriod(s string) (time.Time, time.Time, error) {
	s = strings.TrimSpace(s)
	switch len(s) {
	case 4:
		y, err := strconv.Atoi(s)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q", s)
		}
		start := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), nil
	case 7:
		t, err := time.ParseInLocation("2006-01", s, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q", s)
		}
		return t, t.AddDate(0, 1, 0), nil
	case 10:
		t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q", s)
		}
		return t, t.AddDate(0, 0, 1), nil
	}
	return time.Time{}, time.Time{}, fmt.Errorf("invalid date %q (use YYYY, YYYY-MM, or YYYY-MM-DD)", s)
}

// labelPred builds a label predicate from the value of a `label:` term:
// `key` (presence), `key=value`, `key!=value`, or `key~value` (contains).
func labelPred(raw string) (node, error) {
	if i := strings.Index(raw, "!="); i >= 0 {
		return labelLeaf(raw[:i], raw[i+2:], opNe)
	}
	if i := strings.IndexByte(raw, '~'); i >= 0 {
		return labelLeaf(raw[:i], raw[i+1:], opContains)
	}
	if i := strings.IndexByte(raw, '='); i >= 0 {
		return labelLeaf(raw[:i], raw[i+1:], opEq)
	}
	key := normalizeKey(mustDequote(strings.TrimSpace(raw)))
	if key == "" {
		return nil, fmt.Errorf("empty label key")
	}
	return predNode{fn: func(r Record) bool { _, ok := r.Labels[key]; return ok }}, nil
}

func labelLeaf(rawKey, rawVal string, op cmpOp) (node, error) {
	key := normalizeKey(mustDequote(strings.TrimSpace(rawKey)))
	v := strings.TrimSpace(mustDequote(strings.TrimSpace(rawVal)))
	if key == "" {
		return nil, fmt.Errorf("empty label key")
	}
	if v == "" {
		return nil, fmt.Errorf("empty label value for %q", key)
	}
	return labelEqPred(key, v, op), nil
}

// labelEqPred matches a single label key against a value with the given
// operator (case-insensitive). A missing key is treated as not-equal: it
// satisfies != but not = or ~.
func labelEqPred(key, value string, op cmpOp) node {
	want := strings.ToLower(value)
	return predNode{fn: func(r Record) bool {
		cur, ok := r.Labels[key]
		if !ok {
			return op == opNe
		}
		got := strings.ToLower(cur)
		switch op {
		case opEq:
			return got == want
		case opNe:
			return got != want
		case opContains:
			return strings.Contains(got, want)
		}
		return false
	}}
}

// normalizeKey lowercases and trims a label key so it matches the canonical
// stored form (the API lowercases keys on write). It does not strip the
// characters the API removes on write, since stored keys already lack them.
func normalizeKey(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// extPred builds a schema-extension predicate from the value of an `ext:` term:
// `key` (presence), `key=value`, `key!=value`, or `key~value` (contains). Keys
// match case-insensitively (the Record stores them lowercased; storage preserves
// case); values match case-insensitively against the stringified extension value.
func extPred(raw string) (node, error) {
	if i := strings.Index(raw, "!="); i >= 0 {
		return extLeaf(raw[:i], raw[i+2:], opNe)
	}
	if i := strings.IndexByte(raw, '~'); i >= 0 {
		return extLeaf(raw[:i], raw[i+1:], opContains)
	}
	if i := strings.IndexByte(raw, '='); i >= 0 {
		return extLeaf(raw[:i], raw[i+1:], opEq)
	}
	key := normalizeKey(mustDequote(strings.TrimSpace(raw)))
	if key == "" {
		return nil, fmt.Errorf("empty extension key")
	}
	return predNode{fn: func(r Record) bool { _, ok := r.Extensions[key]; return ok }}, nil
}

func extLeaf(rawKey, rawVal string, op cmpOp) (node, error) {
	key := normalizeKey(mustDequote(strings.TrimSpace(rawKey)))
	v := strings.TrimSpace(mustDequote(strings.TrimSpace(rawVal)))
	if key == "" {
		return nil, fmt.Errorf("empty extension key")
	}
	if v == "" {
		return nil, fmt.Errorf("empty extension value for %q", key)
	}
	return extEqPred(key, v, op), nil
}

// extEqPred matches a single extension key's stringified value with the given
// operator (case-insensitive). A missing key satisfies != but not = or ~.
func extEqPred(key, value string, op cmpOp) node {
	want := strings.ToLower(value)
	return predNode{fn: func(r Record) bool {
		cur, ok := r.Extensions[key]
		if !ok {
			return op == opNe
		}
		got := strings.ToLower(cur)
		switch op {
		case opEq:
			return got == want
		case opNe:
			return got != want
		case opContains:
			return strings.Contains(got, want)
		}
		return false
	}}
}

// splitRange splits "a..b" into its (possibly empty) bounds. An open side
// ("..b" or "a..") yields a one-sided range.
func splitRange(s string) (lo, hi string, ok bool) {
	if i := strings.Index(s, ".."); i >= 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+2:]), true
	}
	return "", "", false
}

// splitOp peels a leading comparison operator off a value, longest match first.
// With no operator it defaults to equality.
func splitOp(s string) (cmpOp, string) {
	switch {
	case strings.HasPrefix(s, ">="):
		return opGe, s[2:]
	case strings.HasPrefix(s, "<="):
		return opLe, s[2:]
	case strings.HasPrefix(s, "!="):
		return opNe, s[2:]
	case strings.HasPrefix(s, ">"):
		return opGt, s[1:]
	case strings.HasPrefix(s, "<"):
		return opLt, s[1:]
	case strings.HasPrefix(s, "="):
		return opEq, s[1:]
	}
	return opEq, s
}
