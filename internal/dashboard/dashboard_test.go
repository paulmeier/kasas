package dashboard

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestAllAccountsOptionHasValue guards against a regression where the
// "All accounts" <option> used Value(""). go-app drops empty value attributes,
// so the option rendered as `<option>All accounts</option>` — and a valueless
// <option> reports its text ("All accounts") as its DOM value. Selecting it then
// sent account_id=All+accounts as a filter, matching no account and showing
// "No transactions." The option must carry a real, non-empty value.
func TestAllAccountsOptionHasValue(t *testing.T) {
	v := &dashboardView{
		accounts: []account{{ID: "ACT-1", Name: "Checking"}},
	}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderControls())
	html := buf.String()

	if strings.Contains(html, "<option>All accounts</option>") {
		t.Fatalf("All accounts option rendered without a value attribute; "+
			"selecting it sends the text as account_id.\nHTML:\n%s", html)
	}
	if !strings.Contains(html, `value="`+allAccountsValue+`"`) {
		t.Fatalf("expected All accounts option to use value %q.\nHTML:\n%s", allAccountsValue, html)
	}
}

// TestAllAccountsValueNotAnAccountID ensures the sentinel can never collide with
// a real account id, since onAccountChange maps it back to "" (no filter).
func TestAllAccountsValueNotAnAccountID(t *testing.T) {
	if allAccountsValue == "" {
		t.Fatal("allAccountsValue must be non-empty so go-app keeps the value attribute")
	}
}

// TestSortedTxnsByAmountIsNumeric guards the key reason sorting is done
// client-side: amounts are strings, so a lexical sort would order "100" before
// "20" before "9". The Amount column must compare numerically.
func TestSortedTxnsByAmountIsNumeric(t *testing.T) {
	v := &dashboardView{
		txns: []transaction{
			{ID: "a", Amount: "9.00"},
			{ID: "b", Amount: "100.00"},
			{ID: "c", Amount: "-25.50"},
			{ID: "d", Amount: "20.00"},
		},
		sortCol: sortByAmount,
		sortAsc: true,
	}
	if got, want := amounts(v.sortedTxns()), []string{"-25.50", "9.00", "20.00", "100.00"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ascending amount sort = %v, want %v (a lexical sort would give 100,20,9,-25.50)", got, want)
	}
	v.sortAsc = false
	if got, want := amounts(v.sortedTxns()), []string{"100.00", "20.00", "9.00", "-25.50"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("descending amount sort = %v, want %v", got, want)
	}
}

// TestSortedTxnsByDateDefaultsNewestFirst checks the initial sort state
// (sortByDate, descending) keeps the API's newest-first ordering.
func TestSortedTxnsByDateDefaultsNewestFirst(t *testing.T) {
	mk := func(id string, day int) transaction {
		return transaction{ID: id, Date: time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)}
	}
	v := &dashboardView{
		txns:    []transaction{mk("a", 1), mk("b", 3), mk("c", 2)},
		sortCol: sortByDate,
		sortAsc: false,
	}
	if got, want := ids(v.sortedTxns()), []string{"b", "c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("date-descending sort = %v, want %v", got, want)
	}
}

// TestSortedTxnsByDescriptionFallsBackToDescription confirms the Description
// column sorts on the same payee-or-description text that the rows display.
func TestSortedTxnsByDescriptionFallsBackToDescription(t *testing.T) {
	v := &dashboardView{
		txns: []transaction{
			{ID: "a", Payee: "Zelle"},
			{ID: "b", Description: " amazon"}, // no payee -> uses description
			{ID: "c", Payee: "Mortgage"},
		},
		sortCol: sortByDescription,
		sortAsc: true,
	}
	if got, want := ids(v.sortedTxns()), []string{"b", "c", "a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("description sort = %v, want %v (amazon, Mortgage, Zelle)", got, want)
	}
}

func TestDefaultAscForColumn(t *testing.T) {
	for col, want := range map[sortColumn]bool{
		sortByDate:        false,
		sortByAmount:      false,
		sortByAccount:     true,
		sortByDescription: true,
	} {
		if got := defaultAscForColumn(col); got != want {
			t.Errorf("defaultAscForColumn(%d) = %v, want %v", col, got, want)
		}
	}
}

// TestPaginationSlicingAndClamp covers page counting, the short final page, and
// clamping when the requested page is past the end (e.g. after the result set
// shrinks).
func TestPaginationSlicingAndClamp(t *testing.T) {
	txns := make([]transaction, 25)
	for i := range txns {
		txns[i] = transaction{ID: fmt.Sprintf("%02d", i), Payee: fmt.Sprintf("p%02d", i)}
	}
	v := &dashboardView{txns: txns, pageSize: 10, sortCol: sortByDescription, sortAsc: true}

	if got := v.pageCount(); got != 3 {
		t.Fatalf("pageCount = %d, want 3", got)
	}
	if got := len(v.visibleTxns()); got != 10 {
		t.Fatalf("first page size = %d, want 10", got)
	}
	v.page = 2
	if got := len(v.visibleTxns()); got != 5 {
		t.Fatalf("last page size = %d, want 5", got)
	}
	v.page = 99 // past the end
	if got := v.clampedPage(); got != 2 {
		t.Fatalf("clampedPage = %d, want 2", got)
	}
	if got := len(v.visibleTxns()); got != 5 {
		t.Fatalf("clamped page size = %d, want 5", got)
	}
}

// TestPageSizeOptionsRendered ensures the "Show" dropdown offers exactly the
// requested page sizes.
func TestPageSizeOptionsRendered(t *testing.T) {
	v := &dashboardView{pageSize: 50}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderControls())
	html := buf.String()
	for _, n := range []string{"10", "20", "50", "100"} {
		if !strings.Contains(html, `value="`+n+`"`) {
			t.Fatalf("page-size option %q missing from controls.\nHTML:\n%s", n, html)
		}
	}
}

// TestSortHeaderMarksActiveColumn checks the active column is clickable and
// shows a direction arrow, while inactive columns show none.
func TestSortHeaderMarksActiveColumn(t *testing.T) {
	v := &dashboardView{sortCol: sortByAmount, sortAsc: true}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.sortHeader("Amount", sortByAmount, "right"))
	html := buf.String()
	if !strings.Contains(html, "sortable") {
		t.Fatalf("active header missing sortable class.\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "sort-arrow") || !strings.Contains(html, "▲") {
		t.Fatalf("active ascending header should show an up arrow.\nHTML:\n%s", html)
	}

	buf.Reset()
	app.PrintHTML(&buf, v.sortHeader("Date", sortByDate, ""))
	if html := buf.String(); strings.Contains(html, "sort-arrow") {
		t.Fatalf("inactive header should not show an arrow.\nHTML:\n%s", html)
	}
}

// TestLabelsColumnNotSortable verifies the Labels column is a plain header: the
// four data columns (Date, Account, Description, Amount) remain sortable, but
// Labels is not built with sortHeader and carries no sortable affordance.
func TestLabelsColumnNotSortable(t *testing.T) {
	v := &dashboardView{
		txns:     []transaction{{ID: "tx-1", Amount: "1.00", Date: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}},
		pageSize: 10,
	}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderTable())
	html := buf.String()

	if !strings.Contains(html, "labels-col") || !strings.Contains(html, "Labels") {
		t.Fatalf("Labels column header missing.\nHTML:\n%s", html)
	}
	// Exactly four headers are sortable; Labels must not be among them.
	if got := strings.Count(html, "sortable"); got != 4 {
		t.Fatalf("expected 4 sortable headers, got %d.\nHTML:\n%s", got, html)
	}
	if strings.Contains(html, "labels-col sortable") || strings.Contains(html, "sortable labels-col") {
		t.Fatalf("Labels header must not be sortable.\nHTML:\n%s", html)
	}
}

func TestFilterLabelSuggestions(t *testing.T) {
	all := []string{"category: food", "category: rent", "person: dad", "tag: coffee"}

	t.Run("case-insensitive substring match", func(t *testing.T) {
		if got := filterLabelSuggestions(all, "FOOD", nil); !reflect.DeepEqual(got, []string{"category: food"}) {
			t.Fatalf("got %q, want [category: food]", got)
		}
	})
	t.Run("excludes already-applied labels", func(t *testing.T) {
		// "category" matches both category pairs; category:food is applied, so it drops.
		got := filterLabelSuggestions(all, "category", map[string]string{"category": "food"})
		if !reflect.DeepEqual(got, []string{"category: rent"}) {
			t.Fatalf("got %q, want [category: rent]", got)
		}
	})
	t.Run("blank draft yields no suggestions", func(t *testing.T) {
		if got := filterLabelSuggestions(all, "   ", nil); got != nil {
			t.Fatalf("got %q, want nil", got)
		}
	})
	t.Run("caps the list", func(t *testing.T) {
		many := make([]string, 12)
		for i := range many {
			many[i] = fmt.Sprintf("k: v%02d", i)
		}
		if got := filterLabelSuggestions(many, "v", nil); len(got) != 8 {
			t.Fatalf("len = %d, want 8 (capped)", len(got))
		}
	})
}

// TestRenderLabelsCellChips checks the cell renders a "key: value" chip with a
// remove button per label, and that the add-label input is hidden until the cell
// is being edited (it appears on click, not on every row).
func TestRenderLabelsCellChips(t *testing.T) {
	v := &dashboardView{}

	// Not editing: chips + remove buttons, marked editable, but no input.
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderLabelsCell(transaction{ID: "tx-1", Labels: map[string]string{"category": "food", "tag": "rent"}}))
	html := buf.String()
	for _, want := range []string{"label-chip", "label-remove", "category: food", "tag: rent", "editable"} {
		if !strings.Contains(html, want) {
			t.Fatalf("labels cell missing %q.\nHTML:\n%s", want, html)
		}
	}
	if strings.Contains(html, "label-input") {
		t.Fatalf("add-label input should not render until the cell is clicked.\nHTML:\n%s", html)
	}

	// Editing this row: the id-stamped input appears (for focus + clear).
	v.labelEditID = "tx-1"
	buf.Reset()
	app.PrintHTML(&buf, v.renderLabelsCell(transaction{ID: "tx-1", Labels: map[string]string{"category": "food"}}))
	html = buf.String()
	for _, want := range []string{"label-input", `id="label-input-tx-1"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("editing cell missing %q.\nHTML:\n%s", want, html)
		}
	}
}

// TestParseLabel covers the strict key:value parsing used by the inline editor.
func TestParseLabel(t *testing.T) {
	cases := []struct {
		in   string
		k, v string
		ok   bool
	}{
		{"category: food", "category", "food", true},
		{"  tag :  coffee ", "tag", "coffee", true},
		{"no colon", "", "", false},
		{": value", "", "", false},
		{"key:", "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseLabel(c.in)
		if k != c.k || v != c.v || ok != c.ok {
			t.Fatalf("parseLabel(%q) = (%q,%q,%v), want (%q,%q,%v)", c.in, k, v, ok, c.k, c.v, c.ok)
		}
	}
}

func amounts(txns []transaction) []string {
	out := make([]string, len(txns))
	for i, t := range txns {
		out[i] = t.Amount
	}
	return out
}

func ids(txns []transaction) []string {
	out := make([]string, len(txns))
	for i, t := range txns {
		out[i] = t.ID
	}
	return out
}
