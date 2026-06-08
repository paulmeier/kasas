package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"

	"github.com/paulmeier/kasas/internal/extensions"
	"github.com/paulmeier/kasas/internal/search"
)

// Stable DOM ids for the (uncontrolled) rule-form inputs. go-app drops empty
// value attributes, so the inputs are populated and cleared imperatively (the
// same approach as the Search box and the label editor).
const (
	ruleNameInputID     = "rule-name-input"
	ruleQueryInputID    = "rule-query-input"
	ruleLabelInputID    = "rule-label-input"
	ruleExtKeyInputID   = "rule-ext-key-input"
	ruleExtValueInputID = "rule-ext-value-input"
)

// rulesView is the Rules page: a list of rules and a create/edit form. A rule
// pairs a condition (a query in the kasas search syntax, validated in-browser for
// instant feedback) with the labels and/or schema extensions applied to every
// matching transaction. Enabled rules auto-apply to newly-synced transactions;
// "Run" applies a rule (or all enabled rules) over existing transactions.
type rulesView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	rules   []rule
	loading bool
	errMsg  string

	// create/edit form state. editID == 0 means creating; > 0 means editing that
	// rule. The name/query inputs are uncontrolled (their DOM value is the source
	// of truth); formName/formQuery mirror them for validation and submit.
	editing        bool
	editID         int64
	formName       string
	formQuery      string
	formLabels     map[string]string          // staged action labels (rendered as chips)
	formExtensions map[string]json.RawMessage // staged action extensions (rendered as chips)
	formEnabled    bool
	parseErr       string // live query-syntax error ("" = ok)
	formErr        string // form-level error (e.g. no labels)
	saving         bool

	// run feedback.
	running bool
	runMsg  string

	showHelp bool
}

func (v *rulesView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.formLabels = map[string]string{}
	v.formExtensions = map[string]json.RawMessage{}
	v.fetchRules(ctx)
}

func (v *rulesView) fetchRules(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		rs, err := v.client.listRules(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.rules = rs
			ctx.Update()
		})
	})
}

// --- form open/close ---

func (v *rulesView) onNewRule(ctx app.Context, _ app.Event) {
	v.editing = true
	v.editID = 0
	v.formName = ""
	v.formQuery = ""
	v.formLabels = map[string]string{}
	v.formExtensions = map[string]json.RawMessage{}
	v.formEnabled = true
	v.parseErr = ""
	v.formErr = ""
	ctx.Update()
	ctx.Defer(func(app.Context) {
		setElementValue(ruleNameInputID, "")
		setElementValue(ruleQueryInputID, "")
		setElementValue(ruleLabelInputID, "")
		setElementValue(ruleExtKeyInputID, "")
		setElementValue(ruleExtValueInputID, "")
	})
}

func (v *rulesView) onEdit(ctx app.Context, r rule) {
	v.editing = true
	v.editID = r.ID
	v.formName = r.Name
	v.formQuery = r.Query
	v.formLabels = cloneLabels(r.Labels)
	v.formExtensions = cloneExtensions(r.Extensions)
	v.formEnabled = r.Enabled
	v.parseErr = ""
	v.formErr = ""
	ctx.Update()
	ctx.Defer(func(app.Context) {
		setElementValue(ruleNameInputID, r.Name)
		setElementValue(ruleQueryInputID, r.Query)
		setElementValue(ruleLabelInputID, "")
		setElementValue(ruleExtKeyInputID, "")
		setElementValue(ruleExtValueInputID, "")
	})
}

func (v *rulesView) onCancel(ctx app.Context, _ app.Event) { v.closeForm(ctx) }

func (v *rulesView) closeForm(ctx app.Context) {
	v.editing = false
	v.editID = 0
	v.formName = ""
	v.formQuery = ""
	v.formLabels = map[string]string{}
	v.formExtensions = map[string]json.RawMessage{}
	v.parseErr = ""
	v.formErr = ""
	ctx.Update()
}

// --- form input handlers ---

func (v *rulesView) onNameInput(ctx app.Context, _ app.Event) {
	v.formName = ctx.JSSrc().Get("value").String()
}

func (v *rulesView) onQueryInput(ctx app.Context, _ app.Event) {
	v.formQuery = ctx.JSSrc().Get("value").String()
	v.validateQuery()
	ctx.Update()
}

func (v *rulesView) onFormEnabledChange(ctx app.Context, _ app.Event) {
	v.formEnabled = ctx.JSSrc().Get("checked").Bool()
}

// validateQuery sets parseErr from the current query text using the same parser
// the server uses (compiled into the WASM), so syntax errors show as you type.
func (v *rulesView) validateQuery() {
	q := strings.TrimSpace(v.formQuery)
	if q == "" {
		v.parseErr = ""
		return
	}
	if _, err := search.Parse(q); err != nil {
		v.parseErr = err.Error()
		return
	}
	v.parseErr = ""
}

func (v *rulesView) onAddLabel(ctx app.Context, _ app.Event) { v.addLabelFromInput(ctx) }

func (v *rulesView) onLabelKeyDown(ctx app.Context, e app.Event) {
	if e.Get("key").String() == "Enter" {
		e.PreventDefault()
		v.addLabelFromInput(ctx)
	}
}

// addLabelFromInput parses the "key: value" in the label input and stages it on
// the form. Invalid input is reported and left for the user to correct.
func (v *rulesView) addLabelFromInput(ctx app.Context) {
	key, value, ok := parseLabel(elementValue(ruleLabelInputID))
	if !ok {
		v.formErr = `Add a label as "key: value".`
		ctx.Update()
		return
	}
	if v.formLabels == nil {
		v.formLabels = map[string]string{}
	}
	v.formLabels[key] = value // one value per key: a repeat replaces it
	v.formErr = ""
	setElementValue(ruleLabelInputID, "")
	ctx.Update()
}

func (v *rulesView) onRemoveFormLabel(ctx app.Context, key string) {
	delete(v.formLabels, key)
	ctx.Update()
}

func (v *rulesView) onAddExtension(ctx app.Context, _ app.Event) { v.addExtensionFromInput(ctx) }

func (v *rulesView) onExtKeyDown(ctx app.Context, e app.Event) {
	if e.Get("key").String() == "Enter" {
		e.PreventDefault()
		v.addExtensionFromInput(ctx)
	}
}

// addExtensionFromInput stages the namespaced key + value from the extension
// inputs on the form. The value is interpreted as JSON, falling back to a JSON
// string for plain text (so `meal` -> "meal", `88` -> 88, `true` -> true). An
// empty/invalid key is reported and left for the user to correct.
func (v *rulesView) addExtensionFromInput(ctx app.Context) {
	key := extensions.NormalizeKey(elementValue(ruleExtKeyInputID))
	if key == "" {
		v.formErr = `Add an extension as a namespaced key (e.g. tax.category) and a value.`
		ctx.Update()
		return
	}
	if v.formExtensions == nil {
		v.formExtensions = map[string]json.RawMessage{}
	}
	v.formExtensions[key] = parseExtValue(elementValue(ruleExtValueInputID)) // one value per key: a repeat replaces it
	v.formErr = ""
	setElementValue(ruleExtKeyInputID, "")
	setElementValue(ruleExtValueInputID, "")
	ctx.Update()
}

func (v *rulesView) onRemoveFormExtension(ctx app.Context, key string) {
	delete(v.formExtensions, key)
	ctx.Update()
}

// parseExtValue interprets the extension-value input as JSON, falling back to a
// JSON string for non-JSON text. Empty input becomes an empty JSON string, which
// the server keeps (a present key with an empty-string value).
func parseExtValue(s string) json.RawMessage {
	s = strings.TrimSpace(s)
	if s == "" {
		return json.RawMessage(`""`)
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, err := json.Marshal(s)
	if err != nil {
		return json.RawMessage(`""`)
	}
	return json.RawMessage(b)
}

// --- save ---

func (v *rulesView) onSave(ctx app.Context, _ app.Event) {
	if v.saving {
		return
	}
	// Re-read the inputs in case an OnInput event was missed.
	v.formName = elementValue(ruleNameInputID)
	v.formQuery = elementValue(ruleQueryInputID)

	q := strings.TrimSpace(v.formQuery)
	if q == "" {
		v.formErr = "Enter a condition query."
		ctx.Update()
		return
	}
	if _, err := search.Parse(q); err != nil {
		v.parseErr = err.Error()
		ctx.Update()
		return
	}
	if len(v.formLabels) == 0 && len(v.formExtensions) == 0 {
		v.formErr = "Add at least one label or extension to apply."
		ctx.Update()
		return
	}

	v.saving = true
	v.formErr = ""
	v.parseErr = ""
	ctx.Update()

	payload := rulePayload{
		Name:       strings.TrimSpace(v.formName),
		Query:      q,
		Labels:     cloneLabels(v.formLabels),
		Extensions: cloneExtensions(v.formExtensions),
		Enabled:    v.formEnabled,
	}
	editID := v.editID
	ctx.Async(func() {
		var (
			saved rule
			err   error
		)
		if editID > 0 {
			saved, err = v.client.updateRule(context.Background(), editID, payload)
		} else {
			saved, err = v.client.createRule(context.Background(), payload)
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.saving = false
			if err != nil {
				v.formErr = err.Error()
				ctx.Update()
				return
			}
			v.upsertRule(saved)
			v.runMsg = ""
			v.closeForm(ctx)
		})
	})
}

// --- row actions ---

func (v *rulesView) onToggleEnabled(ctx app.Context, r rule) {
	id := r.ID
	payload := rulePayload{Name: r.Name, Query: r.Query, Labels: r.Labels, Extensions: r.Extensions, Enabled: !r.Enabled}
	ctx.Async(func() {
		saved, err := v.client.updateRule(context.Background(), id, payload)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = "Failed to update rule: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.upsertRule(saved)
			ctx.Update()
		})
	})
}

func (v *rulesView) onRun(ctx app.Context, r rule) {
	if v.running {
		return
	}
	v.running = true
	v.runMsg = "Running…"
	v.errMsg = ""
	ctx.Update()

	id := r.ID
	name := ruleLabel(r)
	ctx.Async(func() {
		res, err := v.client.runRule(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.running = false
			if err != nil {
				v.runMsg = ""
				v.errMsg = "Run failed: " + err.Error()
				ctx.Update()
				return
			}
			v.runMsg = fmt.Sprintf("Ran %q: updated %d of %d matching transactions.", name, res.Updated, res.Matched)
			ctx.Update()
		})
	})
}

func (v *rulesView) onRunAll(ctx app.Context, _ app.Event) {
	if v.running {
		return
	}
	v.running = true
	v.runMsg = "Running all enabled rules…"
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		res, err := v.client.runAllRules(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.running = false
			if err != nil {
				v.runMsg = ""
				v.errMsg = "Run failed: " + err.Error()
				ctx.Update()
				return
			}
			v.runMsg = fmt.Sprintf("Ran all enabled rules: updated %d of %d matching transactions.", res.Updated, res.Matched)
			ctx.Update()
		})
	})
}

func (v *rulesView) onDelete(ctx app.Context, r rule) {
	msg := fmt.Sprintf("Delete the rule %q? Labels and extensions already applied to transactions are kept.", ruleLabel(r))
	if !app.Window().Call("confirm", msg).Bool() {
		return
	}
	prev := v.rules
	v.rules = removeRule(v.rules, r.ID)
	ctx.Update()

	id := r.ID
	ctx.Async(func() {
		err := v.client.deleteRule(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.rules = prev // revert the optimistic removal
				v.errMsg = "Failed to delete rule: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			ctx.Update()
		})
	})
}

func (v *rulesView) toggleHelp(ctx app.Context, _ app.Event) { v.showHelp = !v.showHelp; ctx.Update() }
func (v *rulesView) closeHelp(ctx app.Context, _ app.Event)  { v.showHelp = false; ctx.Update() }

// --- list mutation helpers ---

// upsertRule replaces the rule with a matching id, or appends it, keeping the
// list ordered by id (matching the server).
func (v *rulesView) upsertRule(r rule) {
	for i := range v.rules {
		if v.rules[i].ID == r.ID {
			v.rules[i] = r
			return
		}
	}
	v.rules = append(v.rules, r)
	sort.Slice(v.rules, func(i, j int) bool { return v.rules[i].ID < v.rules[j].ID })
}

func removeRule(list []rule, id int64) []rule {
	out := make([]rule, 0, len(list))
	for _, r := range list {
		if r.ID == id {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (v *rulesView) hasEnabledRules() bool {
	for _, r := range v.rules {
		if r.Enabled {
			return true
		}
	}
	return false
}

// ruleDisplayName is the Name column text: the name, or a muted dash when unnamed.
func ruleDisplayName(r rule) string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return "—"
}

// ruleLabel identifies a rule in messages and confirmations: its name, falling
// back to the query when unnamed.
func ruleLabel(r rule) string {
	if strings.TrimSpace(r.Name) != "" {
		return r.Name
	}
	return r.Query
}

// elementValue reads and trims a DOM input's value by id. Returns "" when the
// element is absent (e.g. during a host/test render).
func elementValue(id string) string {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return ""
	}
	el := doc.Call("getElementById", id)
	if !el.Truthy() {
		return ""
	}
	return strings.TrimSpace(el.Get("value").String())
}

// --- rendering ---

func (v *rulesView) Render() app.UI {
	return v.renderShell(navRules,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Rules"),
			app.Span().Class("page-subtitle").Text("Automatically apply labels and extensions to transactions that match a query"),
		),
		v.renderError(),
		v.renderToolbar(),
		v.renderForm(),
		v.renderRunMsg(),
		v.renderList(),
		renderSyntaxModal(v.showHelp, "Query syntax", v.closeHelp),
	)
}

func (v *rulesView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *rulesView) renderToolbar() app.UI {
	return app.Div().Class("controls rules-toolbar").Body(
		app.Button().Class("btn btn-primary").Text("New rule").OnClick(v.onNewRule),
		app.Span().Class("controls-spacer"),
		app.Button().Class("btn").
			Title("Run all enabled rules over existing transactions").
			Text(runAllLabel(v.running)).
			Disabled(v.running || !v.hasEnabledRules()).
			OnClick(v.onRunAll),
	)
}

func runAllLabel(running bool) string {
	if running {
		return "Running…"
	}
	return "Run all"
}

func (v *rulesView) renderRunMsg() app.UI {
	if v.runMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("settings-ok rules-run-msg").Text(v.runMsg)
}

func (v *rulesView) renderForm() app.UI {
	if !v.editing {
		return app.Text("")
	}
	title := "New rule"
	if v.editID > 0 {
		title = "Edit rule"
	}
	return app.Section().Class("card settings-section rule-form").Body(
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text(title),
		),
		app.Div().Class("form-row rule-field").Body(
			app.Label().Class("control-label").Text("Name"),
			app.Input().ID(ruleNameInputID).Class("settings-input").Type("text").
				Placeholder("Optional, e.g. Coffee").OnInput(v.onNameInput),
		),
		app.Div().Class("form-row rule-field").Body(
			app.Label().Class("control-label").Text("If a transaction matches"),
			app.Input().ID(ruleQueryInputID).Class("settings-input rule-query-input").Type("text").
				Placeholder(`e.g. amount:<0 description:coffee`).OnInput(v.onQueryInput),
			app.Button().Class("btn search-help").Title("Query syntax help").Text("? Help").OnClick(v.toggleHelp),
		),
		v.renderParseError(),
		app.Div().Class("form-row rule-field").Body(
			app.Label().Class("control-label").Text("Apply these labels"),
			app.Input().ID(ruleLabelInputID).Class("settings-input").Type("text").
				Placeholder("key: value").OnKeyDown(v.onLabelKeyDown),
			app.Button().Class("btn").Text("Add").OnClick(v.onAddLabel),
		),
		v.renderFormLabels(),
		app.Div().Class("form-row rule-field rule-ext-field").Body(
			app.Label().Class("control-label").Text("Apply these extensions"),
			app.Input().ID(ruleExtKeyInputID).Class("settings-input rule-ext-key").Type("text").
				Placeholder("namespace.key").OnKeyDown(v.onExtKeyDown),
			app.Input().ID(ruleExtValueInputID).Class("settings-input rule-ext-value").Type("text").
				Placeholder("value (JSON or text)").OnKeyDown(v.onExtKeyDown),
			app.Button().Class("btn").Text("Add").OnClick(v.onAddExtension),
		),
		v.renderFormExtensions(),
		app.Label().Class("rule-enabled-toggle").Body(
			app.Input().Type("checkbox").Checked(v.formEnabled).OnChange(v.onFormEnabledChange),
			app.Span().Text("Enabled — auto-apply to newly-synced transactions"),
		),
		v.renderFormError(),
		app.Div().Class("form-row rule-form-actions").Body(
			app.Button().Class("btn btn-primary").Text(saveLabel(v.saving)).Disabled(v.saving).OnClick(v.onSave),
			app.Button().Class("btn").Text("Cancel").OnClick(v.onCancel),
		),
	)
}

func (v *rulesView) renderParseError() app.UI {
	if v.parseErr == "" {
		return app.Text("")
	}
	return app.Div().Class("search-parse-error").Body(
		app.Span().Class("search-parse-label").Text("Invalid query: "),
		app.Span().Text(v.parseErr),
	)
}

func (v *rulesView) renderFormError() app.UI {
	if v.formErr == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text(v.formErr)
}

// renderFormLabels shows the staged action labels as removable chips.
func (v *rulesView) renderFormLabels() app.UI {
	if len(v.formLabels) == 0 {
		return app.Div().Class("rule-form-labels").Body(
			app.Span().Class("empty-hint").Text("No labels yet."),
		)
	}
	keys := sortedLabelKeys(v.formLabels)
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		key, value := k, v.formLabels[k]
		chips = append(chips, app.Span().Class("label-chip").Body(
			app.Span().Class("label-label").Text(formatLabel(key, value)),
			app.Button().Type("button").Class("label-remove").Title("Remove "+key).Text("×").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRemoveFormLabel(ctx, key) }),
		))
	}
	return app.Div().Class("rule-form-labels label-chips").Body(chips...)
}

// renderFormExtensions shows the staged action extensions as removable green chips
// (mirroring the read-only transaction extensions cell, plus a remove ×).
func (v *rulesView) renderFormExtensions() app.UI {
	if len(v.formExtensions) == 0 {
		return app.Div().Class("rule-form-extensions").Body(
			app.Span().Class("empty-hint").Text("No extensions — optional app-owned metadata."),
		)
	}
	keys := sortedExtKeys(v.formExtensions)
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		key := k
		val := extensions.StringifyValue(v.formExtensions[k])
		chips = append(chips, app.Span().Class("ext-chip").Title(key+": "+val).Body(
			app.Span().Class("ext-key").Text(key),
			app.Span().Class("ext-val").Text(val),
			app.Button().Type("button").Class("label-remove").Title("Remove "+key).Text("×").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRemoveFormExtension(ctx, key) }),
		))
	}
	return app.Div().Class("rule-form-extensions ext-chips").Body(chips...)
}

func (v *rulesView) renderList() app.UI {
	if v.loading && len(v.rules) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.rules) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No rules yet."),
			app.P().Class("empty-hint").Text(
				`Click “New rule” to create one — for example, label everything from a payee, or attach an extension to flag large charges for review.`),
		)
	}
	return app.Table().Class("txns rules-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Name"),
				app.Th().Text("Condition"),
				app.Th().Text("Applies"),
				app.Th().Class("right").Text("Enabled"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.rules).Slice(func(i int) app.UI {
				return v.renderRuleRow(v.rules[i])
			}),
		),
	)
}

func (v *rulesView) renderRuleRow(r rule) app.UI {
	return app.Tr().Body(
		app.Td().Text(ruleDisplayName(r)),
		app.Td().Body(app.Code().Class("rule-query").Text(r.Query)),
		app.Td().Body(
			renderRuleLabelChips(r.Labels),
			renderRuleExtChips(r.Extensions),
		),
		app.Td().Class("right").Body(
			app.Input().Type("checkbox").Class("rule-enabled").Checked(r.Enabled).
				OnChange(func(ctx app.Context, _ app.Event) { v.onToggleEnabled(ctx, r) }),
		),
		app.Td().Class("right rule-actions").Body(
			app.Button().Type("button").Class("btn btn-small").
				Title("Run this rule over existing transactions").Text("Run").
				Disabled(v.running).
				OnClick(func(ctx app.Context, _ app.Event) { v.onRun(ctx, r) }),
			app.Button().Type("button").Class("btn btn-small").Text("Edit").
				OnClick(func(ctx app.Context, _ app.Event) { v.onEdit(ctx, r) }),
			app.Button().Type("button").Class("label-delete").Title("Delete rule").
				OnClick(func(ctx app.Context, _ app.Event) { v.onDelete(ctx, r) }).
				Body(iconTrash()),
		),
	)
}

// renderRuleLabelChips renders a rule's action labels as read-only chips.
func renderRuleLabelChips(lbls map[string]string) app.UI {
	keys := sortedLabelKeys(lbls)
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		chips = append(chips, app.Span().Class("label-chip").Body(
			app.Span().Class("label-label").Text(formatLabel(k, lbls[k])),
		))
	}
	return app.Div().Class("label-chips").Body(chips...)
}

// renderRuleExtChips renders a rule's action extensions as read-only green chips
// (mirroring the transaction extensions cell). Renders nothing when the rule
// applies no extensions, so a labels-only rule's cell is unchanged.
func renderRuleExtChips(ext map[string]json.RawMessage) app.UI {
	if len(ext) == 0 {
		return app.Text("")
	}
	keys := sortedExtKeys(ext)
	chips := make([]app.UI, 0, len(keys))
	for _, k := range keys {
		val := extensions.StringifyValue(ext[k])
		chips = append(chips, app.Span().Class("ext-chip").Title(k+": "+val).Body(
			app.Span().Class("ext-key").Text(k),
			app.Span().Class("ext-val").Text(val),
		))
	}
	return app.Div().Class("ext-chips").Body(chips...)
}
