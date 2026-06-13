package dashboard

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// renderAddFormHTML renders the relationships add-form for a given draft state.
func renderAddFormHTML(t *testing.T, v *relationshipsViewing) string {
	t.Helper()
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderAddForm())
	return buf.String()
}

// TestAddFormIsManualIDEntry locks in that the target is a plain manually-entered
// id field, not a transaction picker: the input is present with the manual-id
// placeholder and there is no suggestion list.
func TestAddFormIsManualIDEntry(t *testing.T) {
	html := renderAddFormHTML(t, &relationshipsViewing{})

	if !strings.Contains(html, "target transaction id") {
		t.Errorf("expected the manual target-id placeholder, got:\n%s", html)
	}
	for _, never := range []string{"rel-suggestions", "label-suggestion", "rel-target-wrap", "find target transaction"} {
		if strings.Contains(html, never) {
			t.Errorf("add form must not include the removed typeahead (%q), got:\n%s", never, html)
		}
	}
}

// TestAddButtonGatesOnTargetState is the core gate: Add is enabled only once a kind
// is set and the target id is confirmed to exist. A still-"checking" id (existence
// check in flight) and a "missing" id both keep Add disabled; a transient lookup
// error (state "") falls through to the backend's own validation on submit.
func TestAddButtonGatesOnTargetState(t *testing.T) {
	found := transaction{ID: "c2", Description: "Groceries", Amount: "-10.00", Date: time.Unix(1700000000, 0)}

	cases := []struct {
		name         string
		kind         string
		targetID     string
		state        string
		found        transaction
		wantDisabled bool
		wantStatus   string // a token the status line must contain ("" = no status line)
	}{
		{name: "no kind", kind: "", targetID: "c2", state: "found", found: found, wantDisabled: true, wantStatus: "Groceries"},
		{name: "no target", kind: "refund_of", targetID: "", state: "", wantDisabled: true},
		{name: "checking blocks Add", kind: "refund_of", targetID: "abc", state: "checking", wantDisabled: true, wantStatus: "Checking id"},
		{name: "missing blocks Add", kind: "refund_of", targetID: "nope", state: "missing", wantDisabled: true, wantStatus: "No transaction with id nope"},
		{name: "found enables Add", kind: "refund_of", targetID: "c2", state: "found", found: found, wantDisabled: false, wantStatus: "Groceries"},
		{name: "transient error falls through", kind: "refund_of", targetID: "c2", state: "", wantDisabled: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := &relationshipsViewing{
				relKindDraft:   tc.kind,
				relTargetID:    tc.targetID,
				relTargetState: tc.state,
				relTargetFound: tc.found,
			}
			html := renderAddFormHTML(t, v)

			addBtn := addButtonTag(t, html)
			// app.PrintHTML renders Disabled(true) as a bare `disabled` and
			// Disabled(false) as `disabled="false"` (the live browser sets the DOM
			// property either way), so "disabled but not =false" means disabled.
			gotDisabled := strings.Contains(addBtn, "disabled") && !strings.Contains(addBtn, `disabled="false"`)
			if gotDisabled != tc.wantDisabled {
				t.Errorf("Add disabled = %v, want %v\nbutton: %s", gotDisabled, tc.wantDisabled, addBtn)
			}
			if tc.wantStatus != "" && !strings.Contains(html, tc.wantStatus) {
				t.Errorf("status line missing %q\n%s", tc.wantStatus, html)
			}
		})
	}
}

// addButtonTag extracts the opening <button …> tag of the Add control so the test
// asserts on that element's attributes rather than the whole form.
func addButtonTag(t *testing.T, html string) string {
	t.Helper()
	i := strings.Index(html, "rel-add-btn")
	if i < 0 {
		t.Fatalf("rel-add-btn not found in:\n%s", html)
	}
	start := strings.LastIndex(html[:i], "<button")
	end := strings.IndexByte(html[i:], '>')
	if start < 0 || end < 0 {
		t.Fatalf("could not bound the Add button tag in:\n%s", html)
	}
	return html[start : i+end+1]
}
