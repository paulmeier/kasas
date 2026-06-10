package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// sourcesView is the Sources page: one card per ingestion source — active AND
// inactive — with its connection status, editable configuration (persisted
// settings), a per-source "Sync now" control, a credential form for sources
// that take a pasted secret, and a "Connect" button for sources that use a
// browser OAuth flow (e.g. Google Drive). It manages every source uniformly —
// SimpleFIN, CSV, and any future source. An inactive source (registered but
// missing its activating config, like Plaid without app credentials) is shown
// so it can be configured here and activated by a restart.
type sourcesView struct {
	app.Compo
	chrome          // shared sidebar + API client + version badge
	settingsEditing // per-source config save/reset (shared with the Settings page)
	restartPrompt   // "restart required" banner + in-place restart

	sources []sourceStatus
	enabled bool
	loaded  bool
	errMsg  string

	savingType  string // type whose credential save is in flight
	saveMsg     string
	removingID  string // credential entry id whose removal is in flight
	syncingType string // type whose sync is in flight
	syncMsg     string

	// Post-OAuth banner, read from the callback's redirect query params.
	oauthMsg string
	oauthErr string
}

// credInputID is the stable DOM id of a source's credential input, so its value
// can be read on save and cleared afterwards (go-app drops empty value attrs).
func credInputID(typ string) string { return "source-cred-" + typ }

func (v *sourcesView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.initSettingsEditing(
		func() *apiClient { return v.client },
		v.adoptSetting,
	)
	v.readOAuthResult()
	v.loadSources(ctx)
}

// adoptSetting folds a saved/reset setting back into the matching source's
// config list so the row shows the server-normalized value and state.
func (v *sourcesView) adoptSetting(_ app.Context, st settingItem, restartRequired bool) {
	v.restartNeeded = restartRequired
	for i := range v.sources {
		if v.sources[i].Type != st.Source {
			continue
		}
		for j := range v.sources[i].Config {
			if v.sources[i].Config[j].Key == st.Key {
				v.sources[i].Config[j] = st
				return
			}
		}
	}
}

// readOAuthResult surfaces the result of a just-completed OAuth flow, which the
// callback delivers via the redirect query (?connected=<type> or ?error=<msg>).
func (v *sourcesView) readOAuthResult() {
	u := app.Window().URL()
	if u == nil {
		return
	}
	q := u.Query()
	if c := q.Get("connected"); c != "" {
		v.oauthMsg = "Connected " + c + "."
	}
	if e := q.Get("error"); e != "" {
		v.oauthErr = e
	}
}

func (v *sourcesView) loadSources(ctx app.Context) {
	ctx.Async(func() {
		data, err := v.client.listSources(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loaded = true
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.sources = data.Sources
			v.enabled = data.Enabled
			v.restartNeeded = data.RestartRequired
			ctx.Update()
		})
	})
}

func (v *sourcesView) onSyncSource(ctx app.Context, typ string) {
	if v.syncingType != "" {
		return
	}
	v.syncingType = typ
	v.syncMsg = ""
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		err := v.client.syncSource(context.Background(), typ)
		ctx.Dispatch(func(ctx app.Context) {
			v.syncingType = ""
			if err != nil {
				v.errMsg = "Sync failed: " + err.Error()
				ctx.Update()
				return
			}
			v.syncMsg = "Sync started for " + typ + ". Watch the Transactions page for new data."
			ctx.Update()
		})
	})
}

func (v *sourcesView) onSaveCredential(ctx app.Context, typ string) {
	if v.savingType != "" {
		return
	}
	token := domInputValue(credInputID(typ))
	if token == "" {
		v.errMsg = "Enter a credential first."
		ctx.Update()
		return
	}
	v.savingType = typ
	v.saveMsg = ""
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		connected, err := v.client.setSourceCredential(context.Background(), typ, token)
		ctx.Dispatch(func(ctx app.Context) {
			v.savingType = ""
			if err != nil {
				v.errMsg = "Could not save credential: " + err.Error()
				ctx.Update()
				return
			}
			v.saveMsg = "Credential saved for " + typ + "."
			clearDomInput(credInputID(typ))
			v.setConnected(typ, connected)
			v.loadSources(ctx) // refresh connection + (for multi-credential) the entry list
			ctx.Update()
		})
	})
}

// onConnect begins a source's browser OAuth flow: it fetches the consent URL
// (an authenticated request, since a full-page navigation cannot send the token)
// then navigates the browser there.
func (v *sourcesView) onConnect(ctx app.Context, typ string) {
	v.errMsg = ""
	ctx.Update()
	ctx.Async(func() {
		authURL, err := v.client.sourceOAuthStartURL(context.Background(), typ)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.errMsg = "Could not start sign-in: " + err.Error()
				ctx.Update()
				return
			}
			app.Window().Get("location").Set("href", authURL)
		})
	})
}

// setConnected updates a source's connected flag in place after a credential save.
func (v *sourcesView) setConnected(typ string, connected bool) {
	for i := range v.sources {
		if v.sources[i].Type == typ {
			v.sources[i].Connected = connected
			return
		}
	}
}

func (v *sourcesView) Render() app.UI {
	return v.renderShell(navSources,
		v.renderRestartBanner(func() *apiClient { return v.client }),
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Sources"),
			app.Span().Class("page-subtitle").Text("Connect, configure, and sync where your transactions come from"),
		),
		v.renderError(),
		v.renderSettingMessages(),
		v.renderOAuthBanner(),
		v.renderBody(),
	)
}

func (v *sourcesView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

func (v *sourcesView) renderOAuthBanner() app.UI {
	switch {
	case v.oauthErr != "":
		return app.Div().Class("error").Text("Connection failed: " + v.oauthErr)
	case v.oauthMsg != "":
		return app.Div().Class("settings-ok").Text(v.oauthMsg)
	default:
		return app.Text("")
	}
}

// renderBody shows, in order: a loading state, then a disabled state, then the
// source cards (or an empty note) — rows-first so a real error never reads as
// "no sources".
func (v *sourcesView) renderBody() app.UI {
	if !v.loaded {
		return app.Div().Class("status").Text("Loading…")
	}
	if !v.enabled {
		return app.Div().Class("settings-note").Text("Source management is unavailable in this build.")
	}
	if len(v.sources) == 0 {
		return app.Div().Class("settings-note").Text("No ingestion sources are configured.")
	}
	return app.Div().Body(
		app.Range(v.sources).Slice(func(i int) app.UI {
			return v.renderSource(v.sources[i])
		}),
	)
}

func (v *sourcesView) renderSource(s sourceStatus) app.UI {
	pillClass, pillText := "status-pill disconnected", "Not connected"
	switch {
	case !s.Active:
		pillClass, pillText = "status-pill inactive", "Inactive"
	case s.Connected:
		pillClass, pillText = "status-pill connected", "Connected"
	}

	body := []app.UI{
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text(sourceTitle(s)),
			app.Span().Class(pillClass).Text(pillText),
		),
		app.P().Class("settings-help").Text(sourceArchetypeHelp(s.Archetype)),
	}
	if !s.Active {
		body = append(body, app.P().Class("settings-help").Text(
			"Not active in this run. Set its configuration below — once the required values are in place, restart kasas to activate it."))
	}

	// Credential and sync controls need a running source instance, so they are
	// offered only for active sources. A multi-credential source (e.g. Teller)
	// shows its connected credentials with per-entry remove plus an "add another"
	// form; a single-credential source shows one replace input.
	if s.Active {
		switch {
		case s.MultiCredential:
			body = append(body, v.renderMultiCredential(s))
		case s.Credentialed:
			body = append(body, v.renderCredentialForm(s))
		}
		// Browser OAuth connect for sources that support it.
		if s.OAuth {
			body = append(body, v.renderConnectButton(s))
		}
	}

	// Editable, persisted configuration (applies after a restart).
	if len(s.Config) > 0 {
		body = append(body,
			app.Div().Class("settings-divider"),
			app.H3().Class("setting-group-title").Text("Configuration"),
		)
		for i := range s.Config {
			body = append(body, v.renderSettingRow(s.Config[i]))
		}
	}

	// Per-source sync control.
	if s.Active {
		body = append(body,
			app.Div().Class("settings-divider"),
			app.Div().Class("form-row").Body(
				app.Button().
					Class("btn btn-primary").
					Text(sourceSyncLabel(v.syncingType == s.Type)).
					Disabled(v.syncingType == s.Type).
					OnClick(func(ctx app.Context, _ app.Event) { v.onSyncSource(ctx, s.Type) }),
				v.renderSyncMsg(s.Type),
			),
		)
	}

	return app.Section().Class("card settings-section").Body(body...)
}

func (v *sourcesView) renderCredentialForm(s sourceStatus) app.UI {
	label, help := "Credential", ""
	if len(s.Credentials) > 0 {
		label = s.Credentials[0].Title
		help = s.Credentials[0].Help
	}
	rows := []app.UI{}
	if help != "" {
		rows = append(rows, app.P().Class("settings-help").Text(help))
	}
	rows = append(rows, app.Div().Class("form-row").Body(
		app.Input().
			ID(credInputID(s.Type)).
			Class("settings-input").
			Type("password").
			Placeholder(label).
			AutoComplete(false),
		app.Button().
			Class("btn").
			Text(saveLabel(v.savingType == s.Type)).
			Disabled(v.savingType == s.Type).
			OnClick(func(ctx app.Context, _ app.Event) { v.onSaveCredential(ctx, s.Type) }),
	))
	if v.saveMsg != "" && v.savingType == "" {
		rows = append(rows, app.Div().Class("settings-ok").Text(v.saveMsg))
	}
	return app.Div().Body(rows...)
}

// renderMultiCredential shows a multi-credential source's connected credentials
// (each masked, with a Remove button when removable) plus a form to add another —
// e.g. one Teller access token per linked bank.
func (v *sourcesView) renderMultiCredential(s sourceStatus) app.UI {
	rows := []app.UI{}

	if len(s.CredentialEntries) > 0 {
		items := make([]app.UI, 0, len(s.CredentialEntries))
		for _, e := range s.CredentialEntries {
			e := e
			cells := []app.UI{app.Span().Class("cred-label").Text(e.Label)}
			if e.Removable {
				cells = append(cells, app.Button().
					Class("btn btn-sm").
					Text(removeLabel(v.removingID == e.ID)).
					Disabled(v.removingID == e.ID).
					OnClick(func(ctx app.Context, _ app.Event) { v.onRemoveCredential(ctx, s.Type, e.ID) }))
			} else {
				cells = append(cells, app.Span().Class("settings-help").Text("from config"))
			}
			items = append(items, app.Li().Class("cred-item").Body(cells...))
		}
		rows = append(rows, app.Ul().Class("cred-list").Body(items...))
	} else {
		rows = append(rows, app.P().Class("settings-help").Text("Nothing connected yet."))
	}

	// "Add another bank" form — reuses the credential input; the server appends.
	label, help := "Access token", ""
	if len(s.Credentials) > 0 {
		label = s.Credentials[0].Title
		help = s.Credentials[0].Help
	}
	if help != "" {
		rows = append(rows, app.P().Class("settings-help").Text(help))
	}
	rows = append(rows, app.Div().Class("form-row").Body(
		app.Input().
			ID(credInputID(s.Type)).
			Class("settings-input").
			Type("password").
			Placeholder(label).
			AutoComplete(false),
		app.Button().
			Class("btn").
			Text(addBankLabel(v.savingType == s.Type)).
			Disabled(v.savingType == s.Type).
			OnClick(func(ctx app.Context, _ app.Event) { v.onSaveCredential(ctx, s.Type) }),
	))
	if v.saveMsg != "" && v.savingType == "" {
		rows = append(rows, app.Div().Class("settings-ok").Text(v.saveMsg))
	}
	return app.Div().Body(rows...)
}

// onRemoveCredential disconnects one credential of a multi-credential source, then
// reloads the source list so the entry disappears.
func (v *sourcesView) onRemoveCredential(ctx app.Context, typ, id string) {
	if v.removingID != "" {
		return
	}
	v.removingID = id
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		_, err := v.client.removeSourceCredential(context.Background(), typ, id)
		ctx.Dispatch(func(ctx app.Context) {
			v.removingID = ""
			if err != nil {
				v.errMsg = "Could not remove credential: " + err.Error()
				ctx.Update()
				return
			}
			v.loadSources(ctx)
			ctx.Update()
		})
	})
}

func (v *sourcesView) renderConnectButton(s sourceStatus) app.UI {
	return app.Div().Class("form-row").Body(
		app.Button().
			Class("btn btn-primary").
			Text("Connect "+s.Title).
			OnClick(func(ctx app.Context, _ app.Event) { v.onConnect(ctx, s.Type) }),
		app.Span().Class("settings-help").Text("Opens the provider's sign-in to authorize access."),
	)
}

func (v *sourcesView) renderSyncMsg(typ string) app.UI {
	if v.syncingType == typ {
		return app.Span().Class("sync-status").Text("Starting sync…")
	}
	if v.syncMsg != "" && strings.Contains(v.syncMsg, " "+typ+" ") {
		return app.Span().Class("sync-status ok").Text(v.syncMsg)
	}
	return app.Text("")
}

func sourceTitle(s sourceStatus) string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Type
}

func sourceSyncLabel(syncing bool) string {
	if syncing {
		return "Syncing…"
	}
	return "Sync now"
}

// addBankLabel is the add button's text for a multi-credential source.
func addBankLabel(busy bool) string {
	if busy {
		return "Adding…"
	}
	return "Add"
}

// removeLabel is a credential entry's remove button text.
func removeLabel(busy bool) string {
	if busy {
		return "Removing…"
	}
	return "Remove"
}

// sourceArchetypeHelp is a one-line description of how a source delivers data.
func sourceArchetypeHelp(archetype string) string {
	switch archetype {
	case "pull":
		return "Pulled from the provider on the sync schedule."
	case "file":
		return "Imported from CSV files in the configured folders on the sync schedule."
	case "webhook":
		return "Received from inbound webhooks."
	case "manual":
		return "Entered by hand."
	case "enrichment":
		return "Annotates existing transactions."
	default:
		return "An ingestion source."
	}
}
