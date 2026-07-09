package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestRulesListRenders checks a rule row shows its name, condition query, applied
// labels, and the per-row actions.
func TestRulesListRenders(t *testing.T) {
	v := &rulesView{rules: []rule{
		{ID: 1, Name: "Coffee", Query: "description:coffee", Labels: map[string]string{"category": "coffee"}, Enabled: true},
	}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	html := buf.String()
	for _, want := range []string{"Coffee", "description:coffee", "category: coffee", ">Run<", ">Edit<", "rules-table"} {
		if !strings.Contains(html, want) {
			t.Fatalf("rules table missing %q\nHTML:\n%s", want, html)
		}
	}
}

func TestRulesEmptyState(t *testing.T) {
	v := &rulesView{} // no rules, not loading
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	if html := buf.String(); !strings.Contains(html, "No rules yet") {
		t.Fatalf("expected the empty state, got:\n%s", html)
	}
}

// TestRuleFormRenders checks the create form shows its inputs, the staged label
// chips, and the help button.
func TestRuleFormRenders(t *testing.T) {
	v := &rulesView{editing: true, formEnabled: true, formLabels: map[string]string{"category": "coffee"}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderForm())
	html := buf.String()
	for _, want := range []string{
		"New rule", `id="rule-query-input"`, `id="rule-label-input"`,
		"Apply these labels", "category: coffee", ">Save<", "Help",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rule form missing %q\nHTML:\n%s", want, html)
		}
	}

	// Editing an existing rule changes the form title.
	v.editID = 7
	buf.Reset()
	app.PrintHTML(&buf, v.renderForm())
	if html := buf.String(); !strings.Contains(html, "Edit rule") {
		t.Fatalf("expected the edit title, got:\n%s", html)
	}
}

// TestRuleFormDangerZone checks the "remove applied labels & extensions" action
// appears only when editing an existing rule that actually applies something, and
// stays out of the create form (reveal-on-interaction: not a per-row button).
func TestRuleFormDangerZone(t *testing.T) {
	applied := rule{ID: 7, Name: "Coffee", Query: "description:coffee", Labels: map[string]string{"category": "coffee"}, Enabled: true}

	// Editing a rule that applies a label: the action is shown.
	v := &rulesView{editing: true, editID: 7, formEnabled: true, rules: []rule{applied}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderForm())
	if html := buf.String(); !strings.Contains(html, "Remove applied labels") {
		t.Fatalf("expected the danger zone when editing an applying rule, got:\n%s", html)
	}

	// The create form (editID 0) never shows it.
	buf.Reset()
	app.PrintHTML(&buf, (&rulesView{editing: true, editID: 0}).renderForm())
	if html := buf.String(); strings.Contains(html, "Remove applied labels") {
		t.Fatalf("did not expect the danger zone in the create form:\n%s", html)
	}

	// A rule that applies nothing has nothing to remove.
	empty := rule{ID: 9, Query: "amount:<0"}
	buf.Reset()
	app.PrintHTML(&buf, (&rulesView{editing: true, editID: 9, rules: []rule{empty}}).renderForm())
	if html := buf.String(); strings.Contains(html, "Remove applied labels") {
		t.Fatalf("did not expect the danger zone for a rule that applies nothing:\n%s", html)
	}
}

func TestRuleFormParseError(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&rulesView{parseErr: "missing ')'"}).renderParseError())
	html := buf.String()
	if !strings.Contains(html, "Invalid query") || !strings.Contains(html, "missing") {
		t.Fatalf("parse error not shown:\n%s", html)
	}
}

// TestRulesHelpModalSharesSyntax confirms the Rules page reuses the same query
// syntax reference as Search.
func TestRulesHelpModalSharesSyntax(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, renderSyntaxModal(true, "Query syntax", func(app.Context, app.Event) {}))
	html := buf.String()
	for _, want := range []string{"Query syntax", "label:category=food", "Amounts"} {
		if !strings.Contains(html, want) {
			t.Fatalf("syntax modal missing %q\nHTML:\n%s", want, html)
		}
	}
}
