package search

import (
	"testing"
	"time"
)

func date(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

// sample records used across the matching tests.
var (
	coffee = Record{
		ID: "tx-1", AccountID: "acct-checking", AccountName: "Checking",
		Amount: -12.34, AmountRaw: "-12.34", Pending: false, Date: date("2024-01-15"),
		Description: "Morning Coffee", Payee: "Blue Bottle Cafe", Memo: "",
		Labels: map[string]string{"category": "food", "person": "dad"},
	}
	books = Record{
		ID: "tx-2", AccountID: "acct-checking", AccountName: "Checking",
		Amount: -56.78, AmountRaw: "-56.78", Pending: true, Date: date("2024-06-01"),
		Description: "Books", Payee: "Local Store", Memo: "birthday gift",
		Labels: map[string]string{"category": "shopping"},
	}
	paycheck = Record{
		ID: "tx-3", AccountID: "acct-checking", AccountName: "Checking",
		Amount: 2500.00, AmountRaw: "2500.00", Pending: false, Date: date("2024-12-01"),
		Description: "Direct Deposit", Payee: "Employer Inc", Memo: "",
		Labels: map[string]string{"category": "income"},
	}
	transfer = Record{
		ID: "tx-4", AccountID: "acct-savings", AccountName: "Savings",
		Amount: 250.00, AmountRaw: "250.00", Pending: false, Date: date("2024-06-15"),
		Description: "Transfer", Payee: "Self", Memo: "",
		Labels: map[string]string{},
	}
	all = []Record{coffee, books, paycheck, transfer}
)

// matched runs q against every sample record and returns the matching IDs.
func matched(t *testing.T, query string) []string {
	t.Helper()
	q, err := Parse(query)
	if err != nil {
		t.Fatalf("Parse(%q) error: %v", query, err)
	}
	var out []string
	for _, r := range all {
		if q.Match(r) {
			out = append(out, r.ID)
		}
	}
	return out
}

func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMatch(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  []string
	}{
		// Empty / match-all.
		{"empty matches all", "", []string{"tx-1", "tx-2", "tx-3", "tx-4"}},
		{"whitespace matches all", "   ", []string{"tx-1", "tx-2", "tx-3", "tx-4"}},

		// Free text (case-insensitive, across fields + labels).
		{"free text description", "coffee", []string{"tx-1"}},
		{"free text case-insensitive", "COFFEE", []string{"tx-1"}},
		{"free text payee", "employer", []string{"tx-3"}},
		{"free text memo", "birthday", []string{"tx-2"}},
		{"free text account name", "savings", []string{"tx-4"}},
		{"free text label value", "income", []string{"tx-3"}},
		{"free text label key", "person", []string{"tx-1"}},
		{"quoted phrase", `"blue bottle"`, []string{"tx-1"}},
		{"quoted phrase no match across words", `"bottle blue"`, nil},

		// Text fields.
		{"description field", "description:books", []string{"tx-2"}},
		{"payee field", "payee:self", []string{"tx-4"}},
		{"memo field", "memo:gift", []string{"tx-2"}},
		{"account field by name", "account:checking", []string{"tx-1", "tx-2", "tx-3"}},
		{"id field", "id:tx-3", []string{"tx-3"}},
		{"payee quoted phrase", `payee:"employer inc"`, []string{"tx-3"}},

		// Amount comparisons (sign-aware).
		{"amount gt", "amount:>0", []string{"tx-3", "tx-4"}},
		{"amount gte", "amount:>=250", []string{"tx-3", "tx-4"}},
		{"amount lt zero", "amount:<0", []string{"tx-1", "tx-2"}},
		{"amount le negative", "amount:<=-56.78", []string{"tx-2"}},
		{"amount eq", "amount:250", []string{"tx-4"}},
		{"amount explicit eq", "amount:=250.00", []string{"tx-4"}},
		{"amount ne", "amount:!=250", []string{"tx-1", "tx-2", "tx-3"}},
		{"amount range", "amount:0..300", []string{"tx-4"}},
		{"amount range negatives", "amount:-60..-10", []string{"tx-1", "tx-2"}},
		{"amount range reversed bounds", "amount:300..0", []string{"tx-4"}},
		{"amount open upper", "amount:..0", []string{"tx-1", "tx-2"}},
		{"amount open lower", "amount:1000..", []string{"tx-3"}},
		{"amount gt negative", "amount:>-20", []string{"tx-1", "tx-3", "tx-4"}},

		// Dates.
		{"date year", "date:2024", []string{"tx-1", "tx-2", "tx-3", "tx-4"}},
		{"date month", "date:2024-06", []string{"tx-2", "tx-4"}},
		{"date day", "date:2024-01-15", []string{"tx-1"}},
		{"date gt year", "date:>2024-06", []string{"tx-3"}},
		{"date gte day", "date:>=2024-06-15", []string{"tx-3", "tx-4"}},
		{"date lt month", "date:<2024-06", []string{"tx-1"}},
		{"date range", "date:2024-01..2024-06", []string{"tx-1", "tx-2", "tx-4"}},

		// Pending.
		{"pending true", "pending:true", []string{"tx-2"}},
		{"pending false", "pending:false", []string{"tx-1", "tx-3", "tx-4"}},
		{"pending yes", "pending:yes", []string{"tx-2"}},

		// Labels.
		{"label presence", "label:person", []string{"tx-1"}},
		{"label eq", "label:category=food", []string{"tx-1"}},
		{"label eq case-insensitive", "label:category=FOOD", []string{"tx-1"}},
		{"label ne includes missing key", "label:category!=food", []string{"tx-2", "tx-3", "tx-4"}},
		{"label contains", "label:category~shop", []string{"tx-2"}},
		{"label quoted value with space", `label:payee="local store"`, nil}, // payee isn't a label
		{"label shorthand", "category:income", []string{"tx-3"}},
		{"label shorthand value with colon", "note:12:30", nil},

		// Boolean combinations.
		{"implicit and", "category:food coffee", []string{"tx-1"}},
		{"explicit and", "amount:<0 AND pending:true", []string{"tx-2"}},
		{"or", "category:food OR category:income", []string{"tx-1", "tx-3"}},
		{"or pipe", "id:tx-1 | id:tx-4", []string{"tx-1", "tx-4"}},
		{"negation dash", "-category:food", []string{"tx-2", "tx-3", "tx-4"}},
		{"negation not", "NOT pending:true", []string{"tx-1", "tx-3", "tx-4"}},
		{"and binds tighter than or", "category:income OR category:food coffee", []string{"tx-1", "tx-3"}},
		{"grouping overrides precedence", "(category:income OR category:food) amount:<0", []string{"tx-1"}},
		{"complex", "account:checking AND amount:<0 AND -memo:gift", []string{"tx-1"}},
		{"nested groups", "(account:savings OR (amount:>1000 AND category:income))", []string{"tx-3", "tx-4"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matched(t, c.query)
			if !equalIDs(got, c.want) {
				t.Fatalf("query %q matched %v, want %v", c.query, got, c.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	cases := []struct {
		name  string
		query string
	}{
		{"unbalanced open", "(category:food"},
		{"unbalanced close", "category:food)"},
		{"dangling not", "category:food NOT"},
		{"dangling dash", "category:food -"},
		{"trailing and", "coffee AND"},
		{"trailing or", "coffee OR"},
		{"leading and", "AND coffee"},
		{"empty parens", "()"},
		{"unterminated quote", `payee:"unclosed`},
		{"invalid amount", "amount:abc"},
		{"invalid amount op", "amount:>x"},
		{"invalid date", "date:notadate"},
		{"invalid date length", "date:2024-3"},
		{"invalid pending", "pending:maybe"},
		{"empty description value", "description:"},
		{"empty label key", "label:=food"},
		{"empty shorthand value", "category:"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if q, err := Parse(c.query); err == nil {
				t.Fatalf("Parse(%q) = %v, want error", c.query, q)
			}
		})
	}
}

func TestQueryStringAndNilMatch(t *testing.T) {
	q, err := Parse("coffee")
	if err != nil {
		t.Fatal(err)
	}
	if q.String() != "coffee" {
		t.Fatalf("String() = %q, want %q", q.String(), "coffee")
	}
	// A nil *Query matches everything (defensive).
	var nilQ *Query
	if !nilQ.Match(coffee) {
		t.Fatal("nil query should match all records")
	}
}
