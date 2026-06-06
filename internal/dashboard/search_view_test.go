package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

func TestSearchBarRenders(t *testing.T) {
	v := &searchView{}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderSearchBar())
	html := buf.String()
	for _, want := range []string{`id="search-input"`, ">Search<", ">Clear<", "Help"} {
		if !strings.Contains(html, want) {
			t.Fatalf("search bar missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSearchResultsRenderRowsWithEditableLabels confirms a matched row shows its
// fields and an editable Labels cell (parity with the Dashboard), and that the
// data columns are sortable.
func TestSearchResultsRenderRowsWithEditableLabels(t *testing.T) {
	v := &searchView{
		loaded:   true,
		searched: true,
		pageSize: 50,
		sortCol:  sortByDate,
		byID:     map[string]account{"a1": {ID: "a1", Name: "Checking"}},
		allTxns: []transaction{{
			ID: "tx-1", AccountID: "a1", Amount: "-12.34",
			Date:        time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC),
			Description: "Coffee", Labels: map[string]string{"category": "food"},
		}},
		results: []int{0},
	}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderResults())
	html := buf.String()
	for _, want := range []string{"Coffee", "Checking", "-12.34", "category: food", "labels-cell", "editable"} {
		if !strings.Contains(html, want) {
			t.Fatalf("results missing %q\nHTML:\n%s", want, html)
		}
	}
	// Exactly the four data columns are sortable; Labels is not.
	if got := strings.Count(html, "sortable"); got != 4 {
		t.Fatalf("expected 4 sortable headers, got %d\nHTML:\n%s", got, html)
	}
}

func TestSearchEmptyStates(t *testing.T) {
	// Loaded but no search run yet => initial prompt.
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&searchView{loaded: true}).renderResults())
	if html := buf.String(); !strings.Contains(html, "Search your transactions") {
		t.Fatalf("expected initial prompt, got:\n%s", html)
	}
	// Searched with no matches => no-match state.
	buf.Reset()
	app.PrintHTML(&buf, (&searchView{loaded: true, searched: true}).renderResults())
	if html := buf.String(); !strings.Contains(html, "No matching") {
		t.Fatalf("expected no-match state, got:\n%s", html)
	}
}

func TestSearchParseErrorDisplay(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&searchView{parseErr: "missing ')'"}).renderParseError())
	html := buf.String()
	if !strings.Contains(html, "Invalid query") || !strings.Contains(html, "missing") {
		t.Fatalf("parse error not shown:\n%s", html)
	}
}

// TestSearchHelpModal checks the modal is hidden by default, renders a
// scrollable body when open, and documents the key syntax forms.
func TestSearchHelpModal(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&searchView{}).renderHelpModal())
	if strings.Contains(buf.String(), "modal-overlay") {
		t.Fatalf("help modal should be hidden by default")
	}

	buf.Reset()
	app.PrintHTML(&buf, (&searchView{showHelp: true}).renderHelpModal())
	html := buf.String()
	for _, want := range []string{
		"modal-overlay", "modal-body", "Search syntax",
		"label:category=food", "category:food", "date:2024", "Combining",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("help modal missing %q\nHTML:\n%s", want, html)
		}
	}
}
