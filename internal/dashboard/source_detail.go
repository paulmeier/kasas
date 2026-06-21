package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// sourceDetailView is the per-source detail page behind every /sources/<type>
// route: it shows one ingestion source's editable configuration (persisted
// settings), a credential form (single or multi-credential) or a browser-OAuth
// "Connect" button, a per-source "Sync now" control, and the external hosts it
// contacts. The source type is read from the URL. It mirrors the /ext/<plugin>
// route's prerender-safe load pattern: OnNav guards app.IsServer so a hard GET of
// the route never reaches for the (nil) API client during server-side prerender.
type sourceDetailView struct {
	app.Compo
	chrome          // shared sidebar + API client + version badge
	settingsEditing // per-source config save/reset (shared with the Settings page)
	restartPrompt   // "restart required" banner + in-place restart

	typ     string // source type, from the /sources/<type> URL
	sources []sourceStatus
	enabled bool
	loaded  bool
	errMsg  string

	savingType  string // type whose credential save is in flight
	saveMsg     string
	removingID  string // credential entry id whose removal is in flight
	syncingType string // type whose sync is in flight
	syncMsg     string
}

// credInputID is the stable DOM id of a source's credential input, so its value
// can be read on save and cleared afterwards (go-app drops empty value attrs).
func credInputID(typ string) string { return "source-cred-" + typ }

func (v *sourceDetailView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.initSettingsEditing(
		func() *apiClient { return v.client },
		v.adoptSetting,
	)
	v.adoptRoute(ctx)
}

// OnNav handles client-side navigation between two /sources/<type> pages (both
// match the one regexp route, so go-app may reuse this mounted component).
// Unlike OnMount (browser-only), OnNav also fires during server-side prerender,
// where no API client exists — loading there would panic a background goroutine
// and take the whole server down (any hard GET of the route, pre-auth). Prerender
// renders the loading skeleton; the browser mount loads.
func (v *sourceDetailView) OnNav(ctx app.Context) {
	if app.IsServer {
		return
	}
	if sourceTypeFromNav(ctx) != v.typ {
		v.adoptRoute(ctx)
	}
}

// adoptRoute points the view at the source named by the current URL and loads it.
func (v *sourceDetailView) adoptRoute(ctx app.Context) {
	v.typ = sourceTypeFromNav(ctx)
	v.loaded = false
	v.errMsg = ""
	v.saveMsg = ""
	v.syncMsg = ""
	v.loadSources(ctx)
}

// sourceTypeFromNav extracts the source type from the current /sources/<type> path.
func sourceTypeFromNav(ctx app.Context) string {
	return strings.TrimPrefix(ctx.Page().URL().Path, "/sources/")
}

func (v *sourceDetailView) loadSources(ctx app.Context) {
	if v.client == nil {
		return // not initialized (prerender): never reach for the API
	}
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

// currentSource returns the addressable source matching the URL type, or nil.
func (v *sourceDetailView) currentSource() *sourceStatus {
	for i := range v.sources {
		if v.sources[i].Type == v.typ {
			return &v.sources[i]
		}
	}
	return nil
}

// adoptSetting folds a saved/reset setting back into the matching source's config
// list so the row shows the server-normalized value and state.
func (v *sourceDetailView) adoptSetting(_ app.Context, st settingItem, restartRequired bool) {
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

func (v *sourceDetailView) onSyncSource(ctx app.Context, typ string) {
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

func (v *sourceDetailView) onSaveCredential(ctx app.Context, typ string) {
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

// onConnect begins a source's browser OAuth flow: it fetches the consent URL (an
// authenticated request, since a full-page navigation cannot send the token) then
// navigates the browser there. The provider's callback redirects back to
// /sources?connected=<type>, where the list page shows the result banner.
func (v *sourceDetailView) onConnect(ctx app.Context, typ string) {
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

// onRemoveCredential disconnects one credential of a multi-credential source, then
// reloads the source list so the entry disappears.
func (v *sourceDetailView) onRemoveCredential(ctx app.Context, typ, id string) {
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

// setConnected updates a source's connected flag in place after a credential save.
func (v *sourceDetailView) setConnected(typ string, connected bool) {
	for i := range v.sources {
		if v.sources[i].Type == typ {
			v.sources[i].Connected = connected
			return
		}
	}
}

// --- rendering ---

func (v *sourceDetailView) Render() app.UI {
	return v.renderShell(navSources,
		v.renderRestartBanner(func() *apiClient { return v.client }),
		v.renderHeader(),
		v.renderError(),
		v.renderSettingMessages(),
		v.renderBody(),
	)
}

func (v *sourceDetailView) renderHeader() app.UI {
	title := v.typ
	subtitle := ""
	pill := app.Text("")
	if s := v.currentSource(); s != nil {
		title = sourceTitle(*s)
		subtitle = s.Type + " · " + s.Archetype
		pillClass, pillText := sourceStatusPill(*s)
		pill = app.Span().Class(pillClass).Text(pillText)
	}
	return app.Header().Class("page-header").Body(
		app.Div().Class("source-detail-head").Body(
			app.A().Class("source-detail-back").Href("/sources").Title("Back to sources").Body(iconArrowLeft()),
			app.Span().Class("source-detail-icon").Body(sourceTypeIcon(v.typ)),
			app.H1().Class("page-title").Text(title),
			pill,
		),
		app.Span().Class("page-subtitle").Text(subtitle),
	)
}

func (v *sourceDetailView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

// renderBody shows a loading state, then a disabled state, then an unknown-source
// note, then the source's configuration sections.
func (v *sourceDetailView) renderBody() app.UI {
	if !v.loaded {
		return app.Div().Class("status").Text("Loading…")
	}
	if !v.enabled {
		return app.Div().Class("settings-note").Text("Source management is unavailable in this build.")
	}
	s := v.currentSource()
	if s == nil {
		return app.Div().Class("settings-note").Body(
			app.Text("Unknown source “"+v.typ+"”. "),
			app.A().Class("unsecured-link").Href("/sources").Text("Back to sources"),
		)
	}
	return v.renderSource(*s)
}

func (v *sourceDetailView) renderSource(s sourceStatus) app.UI {
	body := []app.UI{
		app.P().Class("settings-help").Text(sourceArchetypeHelp(s.Archetype)),
	}
	if !s.Active {
		msg := "Not active in this run. Set its configuration below — once the required values are in place, restart kasas to activate it."
		if s.Credentialed {
			msg += " Once active, add its " + sourceCredentialNoun(s) + " and run a sync from this page."
		}
		body = append(body, app.P().Class("settings-help").Text(msg))
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

	// External hosts this source contacts (read-only).
	if len(s.Egress) > 0 {
		body = append(body, app.P().Class("source-egress").Text("Contacts "+strings.Join(s.Egress, ", ")))
	}

	return app.Section().Class("card settings-section").Body(body...)
}

func (v *sourceDetailView) renderCredentialForm(s sourceStatus) app.UI {
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
func (v *sourceDetailView) renderMultiCredential(s sourceStatus) app.UI {
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

func (v *sourceDetailView) renderConnectButton(s sourceStatus) app.UI {
	return app.Div().Class("form-row").Body(
		app.Button().
			Class("btn btn-primary").
			Text("Connect "+s.Title).
			OnClick(func(ctx app.Context, _ app.Event) { v.onConnect(ctx, s.Type) }),
		app.Span().Class("settings-help").Text("Opens the provider's sign-in to authorize access."),
	)
}

func (v *sourceDetailView) renderSyncMsg(typ string) app.UI {
	if v.syncingType == typ {
		return app.Span().Class("sync-status").Text("Starting sync…")
	}
	if v.syncMsg != "" && strings.Contains(v.syncMsg, " "+typ+" ") {
		return app.Span().Class("sync-status ok").Text(v.syncMsg)
	}
	return app.Text("")
}
