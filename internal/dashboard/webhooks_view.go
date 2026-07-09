package dashboard

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// webhookURLInputID is the stable DOM id of the (uncontrolled) URL input, populated
// and cleared imperatively (the same approach as the rule/search inputs).
const webhookURLInputID = "webhook-url-input"

// allEventsKey is the sentinel in the form's type set meaning "subscribe to all
// types"; it is sent to the server as the "*" wildcard.
const allEventsKey = "*"

// webhookEventTypes is the canonical event taxonomy a webhook may subscribe to. It
// mirrors the constants in internal/events; hardcoded here (like the locally-mirrored
// DTOs) so the WASM build does not import the server packages.
var webhookEventTypes = []string{
	"transaction.created", "transaction.updated", "transaction.deleted",
	"account.created", "account.updated", "account.deleted",
	"label.applied", "label.removed",
	"extension.set", "extension.removed",
	"relationship.created", "relationship.removed",
	"rule.created", "rule.updated", "rule.deleted", "rule.executed", "rule.reverted",
	"sync.completed",
}

// webhooksView is the Webhooks page: a list of registered delivery endpoints and a
// create/edit form. kasas POSTs each subscribed event to a webhook's URL, HMAC-signed;
// the page also reveals the signing secret, sends a test delivery, rotates the secret,
// and shows each endpoint's last-delivery health.
type webhooksView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	webhooks []webhook
	loading  bool
	errMsg   string

	// tokenRequired is set when the webhooks list could not be loaded because the
	// instance is unsecured: requireConfiguredToken returns 503 for admin-tier reads
	// until a dashboard token is set (PR #148). Treated as a best-effort empty state
	// with a hint rather than a red error, mirroring the API-keys panel.
	tokenRequired bool

	// create/edit form state. editID == 0 means creating; > 0 means editing. The URL
	// input is uncontrolled (its DOM value is the source of truth); formURL mirrors it
	// for submit. formTypes is the set of selected event types (allEventsKey = all).
	editing     bool
	editID      int64
	formURL     string
	formTypes   map[string]bool
	formEnabled bool
	formErr     string
	saving      bool

	// A freshly minted/revealed signing secret, shown once with a copy button.
	revealURL    string
	revealSecret string

	// Per-action feedback shown above the list.
	busyID  int64 // id of the webhook with an action in flight (disables its buttons)
	testMsg string
	testErr string
}

func (v *webhooksView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.formTypes = map[string]bool{}
	v.fetchWebhooks(ctx)
}

func (v *webhooksView) fetchWebhooks(ctx app.Context) {
	v.loading = true
	ctx.Async(func() {
		hooks, err := v.client.listWebhooks(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loading = false
			if err != nil {
				// An unsecured instance refuses admin-tier reads with 503 until a
				// dashboard token is set; show a calm empty state with a hint rather
				// than a red error banner (mirrors the API-keys panel).
				if isAdminTokenRequired(err) {
					v.tokenRequired = true
					v.errMsg = ""
					v.webhooks = nil
					ctx.Update()
					return
				}
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.tokenRequired = false
			v.errMsg = ""
			v.webhooks = hooks
			ctx.Update()
		})
	})
}

// --- form open/close ---

func (v *webhooksView) onNew(ctx app.Context, _ app.Event) {
	v.editing = true
	v.editID = 0
	v.formURL = ""
	v.formTypes = map[string]bool{allEventsKey: true} // default: all events
	v.formEnabled = true
	v.formErr = ""
	ctx.Update()
	ctx.Defer(func(app.Context) { setElementValue(webhookURLInputID, "") })
}

func (v *webhooksView) onEdit(ctx app.Context, wh webhook) {
	v.editing = true
	v.editID = wh.ID
	v.formURL = wh.URL
	v.formTypes = typesToSet(wh.EventTypes)
	v.formEnabled = wh.Enabled
	v.formErr = ""
	ctx.Update()
	ctx.Defer(func(app.Context) { setElementValue(webhookURLInputID, wh.URL) })
}

func (v *webhooksView) onCancel(ctx app.Context, _ app.Event) { v.closeForm(ctx) }

func (v *webhooksView) closeForm(ctx app.Context) {
	v.editing = false
	v.editID = 0
	v.formURL = ""
	v.formTypes = map[string]bool{}
	v.formErr = ""
	ctx.Update()
}

// --- form input handlers ---

func (v *webhooksView) onURLInput(ctx app.Context, _ app.Event) {
	v.formURL = ctx.JSSrc().Get("value").String()
}

func (v *webhooksView) onFormEnabledChange(ctx app.Context, _ app.Event) {
	v.formEnabled = ctx.JSSrc().Get("checked").Bool()
}

func (v *webhooksView) onToggleAllEvents(ctx app.Context, _ app.Event) {
	on := ctx.JSSrc().Get("checked").Bool()
	if on {
		// Selecting "all" clears the specific selections; they are ignored anyway.
		v.formTypes = map[string]bool{allEventsKey: true}
	} else {
		delete(v.formTypes, allEventsKey)
	}
	ctx.Update()
}

func (v *webhooksView) onToggleType(ctx app.Context, t string) {
	if v.formTypes == nil {
		v.formTypes = map[string]bool{}
	}
	if v.formTypes[t] {
		delete(v.formTypes, t)
	} else {
		v.formTypes[t] = true
		delete(v.formTypes, allEventsKey) // choosing specifics turns off "all"
	}
	ctx.Update()
}

// selectedTypes is the event_types payload: ["*"] when "all" is selected, otherwise
// the chosen specific types in taxonomy order (empty = all on the server).
func (v *webhooksView) selectedTypes() []string {
	if v.formTypes[allEventsKey] {
		return []string{allEventsKey}
	}
	out := make([]string, 0, len(v.formTypes))
	for _, t := range webhookEventTypes {
		if v.formTypes[t] {
			out = append(out, t)
		}
	}
	return out
}

// --- save ---

func (v *webhooksView) onSave(ctx app.Context, _ app.Event) {
	if v.saving {
		return
	}
	v.formURL = elementValue(webhookURLInputID) // re-read in case an OnInput was missed
	url := strings.TrimSpace(v.formURL)
	if url == "" {
		v.formErr = "Enter a delivery URL."
		ctx.Update()
		return
	}

	v.saving = true
	v.formErr = ""
	ctx.Update()

	payload := webhookPayload{URL: url, EventTypes: v.selectedTypes(), Enabled: v.formEnabled}
	editID := v.editID
	ctx.Async(func() {
		var (
			saved webhook
			err   error
		)
		if editID > 0 {
			saved, err = v.client.updateWebhook(context.Background(), editID, payload)
		} else {
			saved, err = v.client.createWebhook(context.Background(), payload)
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.saving = false
			if err != nil {
				v.formErr = err.Error()
				ctx.Update()
				return
			}
			v.upsertWebhook(saved)
			// A create returns the signing secret; show it once.
			if editID == 0 && saved.Secret != "" {
				v.revealURL = saved.URL
				v.revealSecret = saved.Secret
			}
			v.testMsg, v.testErr = "", ""
			v.closeForm(ctx)
		})
	})
}

// --- row actions ---

func (v *webhooksView) onToggleEnabled(ctx app.Context, wh webhook) {
	id := wh.ID
	payload := webhookPayload{URL: wh.URL, EventTypes: wh.EventTypes, Enabled: !wh.Enabled}
	ctx.Async(func() {
		saved, err := v.client.updateWebhook(context.Background(), id, payload)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = "Failed to update webhook: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.upsertWebhook(saved)
			ctx.Update()
		})
	})
}

func (v *webhooksView) onTest(ctx app.Context, wh webhook) {
	if v.busyID != 0 {
		return
	}
	v.busyID = wh.ID
	v.testMsg, v.testErr = "Sending test event…", ""
	ctx.Update()

	id, url := wh.ID, wh.URL
	ctx.Async(func() {
		res, err := v.client.testWebhook(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			v.busyID = 0
			if err != nil {
				v.testMsg = ""
				v.testErr = "Test failed: " + err.Error()
				ctx.Update()
				return
			}
			if res.Delivered {
				v.testErr = ""
				v.testMsg = fmt.Sprintf("Test delivered to %s (HTTP %d).", url, res.Status)
			} else {
				v.testMsg = ""
				v.testErr = fmt.Sprintf("Test to %s failed: %s", url, testFailureText(res))
			}
			// Refresh the row so its last-delivery health reflects the test.
			v.refreshWebhook(ctx, id)
			ctx.Update()
		})
	})
}

func (v *webhooksView) onRevealSecret(ctx app.Context, wh webhook) {
	id := wh.ID
	ctx.Async(func() {
		full, err := v.client.getWebhook(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = "Failed to load secret: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.revealURL = full.URL
			v.revealSecret = full.Secret
			ctx.Update()
		})
	})
}

func (v *webhooksView) onRotateSecret(ctx app.Context, wh webhook) {
	if !app.Window().Call("confirm", "Rotate the signing secret? The old secret stops verifying immediately — update your receiver.").Bool() {
		return
	}
	id := wh.ID
	ctx.Async(func() {
		saved, err := v.client.rotateWebhookSecret(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = "Failed to rotate secret: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			v.upsertWebhook(saved)
			v.revealURL = saved.URL
			v.revealSecret = saved.Secret
			ctx.Update()
		})
	})
}

func (v *webhooksView) onDelete(ctx app.Context, wh webhook) {
	if !app.Window().Call("confirm", "Delete the webhook for "+wh.URL+"? Deliveries stop immediately.").Bool() {
		return
	}
	prev := v.webhooks
	v.webhooks = removeWebhook(v.webhooks, wh.ID)
	ctx.Update()

	id := wh.ID
	ctx.Async(func() {
		err := v.client.deleteWebhook(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.webhooks = prev // revert the optimistic removal
				v.errMsg = "Failed to delete webhook: " + err.Error()
				ctx.Update()
				return
			}
			v.errMsg = ""
			ctx.Update()
		})
	})
}

func (v *webhooksView) onCopySecret(ctx app.Context, _ app.Event) {
	if v.revealSecret == "" {
		return
	}
	clip := app.Window().Get("navigator").Get("clipboard")
	if !clip.Truthy() {
		return
	}
	clip.Call("writeText", v.revealSecret)
	ctx.Update()
}

func (v *webhooksView) onDismissSecret(ctx app.Context, _ app.Event) {
	v.revealURL, v.revealSecret = "", ""
	ctx.Update()
}

// refreshWebhook re-fetches one webhook and upserts it, so the row's last-delivery
// health is current after a test. Best-effort: a failure leaves the stale row.
func (v *webhooksView) refreshWebhook(ctx app.Context, id int64) {
	ctx.Async(func() {
		full, err := v.client.getWebhook(context.Background(), id)
		if err != nil {
			return
		}
		ctx.Dispatch(func(ctx app.Context) {
			v.upsertWebhook(full)
			ctx.Update()
		})
	})
}

// --- list mutation helpers ---

func (v *webhooksView) upsertWebhook(wh webhook) {
	for i := range v.webhooks {
		if v.webhooks[i].ID == wh.ID {
			v.webhooks[i] = wh
			return
		}
	}
	v.webhooks = append(v.webhooks, wh)
	sort.Slice(v.webhooks, func(i, j int) bool { return v.webhooks[i].ID < v.webhooks[j].ID })
}

func removeWebhook(list []webhook, id int64) []webhook {
	out := make([]webhook, 0, len(list))
	for _, wh := range list {
		if wh.ID == id {
			continue
		}
		out = append(out, wh)
	}
	return out
}

// typesToSet turns a stored event_types list into the form's selection set. An empty
// list or a "*" entry selects "all".
func typesToSet(types []string) map[string]bool {
	set := map[string]bool{}
	if len(types) == 0 {
		set[allEventsKey] = true
		return set
	}
	for _, t := range types {
		set[t] = true
	}
	return set
}

func testFailureText(res webhookTestResult) string {
	if res.Error != "" {
		return res.Error
	}
	return fmt.Sprintf("HTTP %d", res.Status)
}

// --- rendering ---

func (v *webhooksView) Render() app.UI {
	return v.renderShell(navWebhooks,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Webhooks"),
			app.Span().Class("page-subtitle").Text("Push events to external apps — HMAC-signed HTTP POST deliveries"),
		),
		v.renderError(),
		v.renderToolbar(),
		v.renderForm(),
		v.renderSecretReveal(),
		v.renderTestMsg(),
		v.renderList(),
	)
}

func (v *webhooksView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *webhooksView) renderToolbar() app.UI {
	return app.Div().Class("controls rules-toolbar").Body(
		app.Button().Class("btn btn-primary").Text("New webhook").OnClick(v.onNew),
	)
}

func (v *webhooksView) renderForm() app.UI {
	if !v.editing {
		return app.Text("")
	}
	title := "New webhook"
	if v.editID > 0 {
		title = "Edit webhook"
	}
	return app.Section().Class("card settings-section rule-form").Body(
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text(title),
		),
		app.Div().Class("form-row rule-field").Body(
			app.Label().Class("control-label").Text("Delivery URL"),
			app.Input().ID(webhookURLInputID).Class("settings-input rule-query-input").Type("url").
				Placeholder("https://example.com/hooks/kasas").OnInput(v.onURLInput),
		),
		app.Div().Class("form-row rule-field").Body(
			app.Label().Class("control-label").Text("Events"),
			v.renderEventTypePicker(),
		),
		app.Label().Class("rule-enabled-toggle").Body(
			app.Input().Type("checkbox").Checked(v.formEnabled).OnChange(v.onFormEnabledChange),
			app.Span().Text("Enabled — deliver matching events"),
		),
		v.renderFormError(),
		app.Div().Class("form-row rule-form-actions").Body(
			app.Button().Class("btn btn-primary").Text(saveLabel(v.saving)).Disabled(v.saving).OnClick(v.onSave),
			app.Button().Class("btn").Text("Cancel").OnClick(v.onCancel),
		),
	)
}

// renderEventTypePicker renders the "All events" toggle plus a checkbox per taxonomy
// type. The specific checkboxes are disabled while "all" is selected.
func (v *webhooksView) renderEventTypePicker() app.UI {
	all := v.formTypes[allEventsKey]
	items := []app.UI{
		app.Label().Class("webhook-type-option webhook-type-all").Body(
			app.Input().Type("checkbox").Checked(all).OnChange(v.onToggleAllEvents),
			app.Span().Text("All events (*)"),
		),
	}
	for _, t := range webhookEventTypes {
		eventType := t
		items = append(items, app.Label().Class("webhook-type-option").Body(
			app.Input().Type("checkbox").
				Checked(v.formTypes[eventType]).
				Disabled(all).
				OnChange(func(ctx app.Context, _ app.Event) { v.onToggleType(ctx, eventType) }),
			app.Span().Text(eventType),
		))
	}
	return app.Div().Class("webhook-types").Body(items...)
}

func (v *webhooksView) renderFormError() app.UI {
	if v.formErr == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text(v.formErr)
}

// renderSecretReveal shows a freshly minted/revealed signing secret with a copy
// button, dismissable. The secret is needed to verify the X-Kasas-Signature header.
func (v *webhooksView) renderSecretReveal() app.UI {
	if v.revealSecret == "" {
		return app.Text("")
	}
	return app.Section().Class("card settings-section token-reveal").Body(
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("Signing secret"),
			app.Button().Class("btn btn-small").Text("Dismiss").OnClick(v.onDismissSecret),
		),
		app.P().Class("settings-help").Text(
			"Signing secret for "+v.revealURL+". Your receiver uses it to verify the "+
				"X-Kasas-Signature header (HMAC-SHA256 of \"<timestamp>.<body>\")."),
		app.Div().Class("form-row").Body(
			app.Input().Class("settings-input token-value").Type("text").ReadOnly(true).Value(v.revealSecret),
			app.Button().Class("btn").Text("Copy").OnClick(v.onCopySecret),
		),
	)
}

func (v *webhooksView) renderTestMsg() app.UI {
	switch {
	case v.testErr != "":
		return app.Div().Class("error").Text(v.testErr)
	case v.testMsg != "":
		return app.Div().Class("settings-ok rules-run-msg").Text(v.testMsg)
	default:
		return app.Text("")
	}
}

func (v *webhooksView) renderList() app.UI {
	if v.loading && len(v.webhooks) == 0 {
		return app.Div().Class("status").Text("Loading…")
	}
	if v.tokenRequired && len(v.webhooks) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("Set a dashboard token to manage webhooks."),
			app.P().Class("empty-hint").Text(
				"Webhook management is admin-only. Generate or set a dashboard token on the Settings page, then reload this page."),
		)
	}
	if len(v.webhooks) == 0 {
		return app.Div().Class("empty-state").Body(
			app.P().Class("empty-title").Text("No webhooks yet."),
			app.P().Class("empty-hint").Text(
				`Click “New webhook” to push events to an external app — budgeting, accounting, fraud detection, notifications, and more.`),
		)
	}
	return app.Table().Class("txns rules-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("URL"),
				app.Th().Text("Events"),
				app.Th().Text("Last delivery"),
				app.Th().Class("right").Text("Enabled"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.webhooks).Slice(func(i int) app.UI {
				return v.renderWebhookRow(v.webhooks[i])
			}),
		),
	)
}

func (v *webhooksView) renderWebhookRow(wh webhook) app.UI {
	return app.Tr().Body(
		app.Td().Body(app.Code().Class("rule-query").Text(wh.URL)),
		app.Td().Text(webhookTypesText(wh.EventTypes)),
		app.Td().Body(renderDeliveryStatus(wh)),
		app.Td().Class("right").Body(
			app.Input().Type("checkbox").Class("rule-enabled").Checked(wh.Enabled).
				OnChange(func(ctx app.Context, _ app.Event) { v.onToggleEnabled(ctx, wh) }),
		),
		app.Td().Class("right rule-actions").Body(
			app.Button().Type("button").Class("btn btn-small").Title("Send a test event").Text("Test").
				Disabled(v.busyID == wh.ID).
				OnClick(func(ctx app.Context, _ app.Event) { v.onTest(ctx, wh) }),
			app.Button().Type("button").Class("btn btn-small").Title("Reveal the signing secret").Text("Secret").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRevealSecret(ctx, wh) }),
			app.Button().Type("button").Class("btn btn-small").Title("Rotate the signing secret").Text("Rotate").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRotateSecret(ctx, wh) }),
			app.Button().Type("button").Class("btn btn-small").Text("Edit").
				OnClick(func(ctx app.Context, _ app.Event) { v.onEdit(ctx, wh) }),
			app.Button().Type("button").Class("label-delete").Title("Delete webhook").
				OnClick(func(ctx app.Context, _ app.Event) { v.onDelete(ctx, wh) }).
				Body(iconTrash()),
		),
	)
}

// webhookTypesText renders the subscribed types: "All events" when empty or wildcard,
// otherwise a comma-separated list.
func webhookTypesText(types []string) string {
	if len(types) == 0 {
		return "All events"
	}
	for _, t := range types {
		if t == allEventsKey {
			return "All events"
		}
	}
	return strings.Join(types, ", ")
}

// renderDeliveryStatus renders a webhook's last-delivery health as a badge.
func renderDeliveryStatus(wh webhook) app.UI {
	if wh.LastAttemptAt == nil {
		return app.Span().Class("badge muted").Text("Never delivered")
	}
	when := wh.LastAttemptAt.Format("2006-01-02 15:04")
	if wh.LastError == "" && wh.LastStatus >= 200 && wh.LastStatus < 300 {
		return app.Span().Class("status-pill connected").
			Title("Delivered " + when + " UTC").
			Text(fmt.Sprintf("HTTP %d", wh.LastStatus))
	}
	detail := wh.LastError
	if detail == "" {
		detail = fmt.Sprintf("HTTP %d", wh.LastStatus)
	}
	return app.Span().Class("status-pill disconnected").
		Title("Failed " + when + " UTC: " + detail).
		Text("Failed")
}
