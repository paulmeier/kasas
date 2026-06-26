package dashboard

import (
	"context"
	"strings"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// sourcesView is the Sources page: a compact list of every ingestion source —
// active AND inactive — each shown as a row (type icon, title, blurb, connection
// status) that opens a per-source detail page at /sources/<type>. A "Sync all"
// control triggers a full sync, and a "Recent syncs" panel summarises the last
// few runs. Per-source configuration, credentials, and sync live on the detail
// page (sourceDetailView). An inactive source (registered but missing its
// activating config, like Plaid without app credentials) is listed too so it can
// be configured on its detail page and activated by a restart.
type sourcesView struct {
	app.Compo
	chrome        // shared sidebar + API client + version badge
	restartPrompt // "restart required" banner + in-place restart

	sources []sourceStatus
	history []syncRun
	enabled bool
	loaded  bool
	errMsg  string

	syncingAll bool
	syncAllMsg string

	// Post-OAuth banner, read from the callback's redirect query params: a
	// source's OAuth callback redirects the browser back to
	// /sources?connected=<type> (or ?error=<msg>).
	oauthMsg string
	oauthErr string
}

func (v *sourcesView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.readOAuthResult()
	v.loadSources(ctx)
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
		// Sync history is best-effort: a failure leaves the panel empty rather
		// than failing the whole page.
		hist, _ := v.client.syncHistory(context.Background(), 10)
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
			v.history = hist
			ctx.Update()
		})
	})
}

// onSyncAll triggers a sync of every active source.
func (v *sourcesView) onSyncAll(ctx app.Context) {
	if v.syncingAll {
		return
	}
	v.syncingAll = true
	v.syncAllMsg = ""
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		err := v.client.triggerSync(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.syncingAll = false
			if err != nil {
				v.errMsg = "Sync failed: " + err.Error()
				ctx.Update()
				return
			}
			v.syncAllMsg = "Sync started. Watch the Transactions page for new data."
			ctx.Update()
		})
	})
}

func (v *sourcesView) Render() app.UI {
	return v.renderShell(navSources,
		v.renderRestartBanner(func() *apiClient { return v.client }),
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Sources"),
			app.Span().Class("page-subtitle").Text("Connect and configure where your data comes from"),
		),
		v.renderError(),
		v.renderOAuthBanner(),
		v.renderControls(),
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

// renderControls is the "Sync all" toolbar above the source list, hidden when
// source management is disabled.
func (v *sourcesView) renderControls() app.UI {
	if v.loaded && !v.enabled {
		return app.Text("")
	}
	body := []app.UI{
		app.Button().
			Class("btn btn-primary").
			Text(syncAllLabel(v.syncingAll)).
			Disabled(v.syncingAll).
			OnClick(func(ctx app.Context, _ app.Event) { v.onSyncAll(ctx) }),
	}
	if v.syncAllMsg != "" {
		body = append(body, app.Span().Class("sync-status ok").Text(v.syncAllMsg))
	}
	return app.Div().Class("controls").Body(body...)
}

// renderBody shows, in order: a loading state, then a disabled state, then the
// source rows + recent syncs (or an empty note) — rows-first so a real error
// never reads as "no sources".
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
		app.Div().Class("source-list").Body(
			app.Range(v.sources).Slice(func(i int) app.UI {
				return v.renderSourceRow(v.sources[i])
			}),
		),
		v.renderRecentSyncs(),
	)
}

// renderSourceRow is one clickable list row linking to the source's detail page.
func (v *sourcesView) renderSourceRow(s sourceStatus) app.UI {
	pillClass, pillText := sourceStatusPill(s)
	return app.A().Class("source-row").Href("/sources/"+s.Type).Body(
		app.Span().Class("source-row-icon").Body(sourceTypeIcon(s.Type)),
		app.Span().Class("source-row-main").Body(
			app.Span().Class("source-row-title").Text(sourceTitle(s)),
			app.Span().Class("source-row-blurb").Text(sourceBlurb(s)),
		),
		app.Span().Class("source-row-status").Body(
			app.Span().Class(pillClass).Text(pillText),
			app.Span().Class("source-chevron").Body(iconChevronRight()),
		),
	)
}

// renderRecentSyncs lists the last few sync runs with a status pill and time,
// mirroring the desktop dashboard's Sources page.
func (v *sourcesView) renderRecentSyncs() app.UI {
	if len(v.history) == 0 {
		return app.Text("")
	}
	rows := make([]app.UI, 0, len(v.history)+1)
	rows = append(rows, app.Div().Class("recent-syncs-title").Text("Recent syncs"))
	for _, r := range v.history {
		rows = append(rows, renderRecentSyncRow(r))
	}
	return app.Div().Class("recent-syncs card").Body(rows...)
}

func renderRecentSyncRow(r syncRun) app.UI {
	pillClass, pillText := syncStatusPill(r.Status)
	when := ""
	if !r.StartedAt.IsZero() {
		when = r.StartedAt.Format("2006-01-02 15:04") + " UTC"
	}
	left := []app.UI{app.Span().Class(pillClass).Text(pillText)}
	if r.Error != "" {
		left = append(left, app.Span().Class("recent-sync-error").Title(r.Error).Text(r.Error))
	}
	return app.Div().Class("recent-sync-row").Body(
		app.Div().Class("recent-sync-left").Body(left...),
		app.Span().Class("recent-sync-when").Text(when),
	)
}

// --- shared source presentation (used by the list rows and the detail page) ---

// sourceStatusPill is the four-state connection pill: an inactive source is grey,
// a connected one green, a credential/OAuth source awaiting its secret amber
// ("Needs credentials"), and an active source needing no secret blue ("Active").
func sourceStatusPill(s sourceStatus) (class, text string) {
	switch {
	case !s.Active:
		return "status-pill inactive", "Inactive"
	case s.Connected:
		return "status-pill connected", "Connected"
	case s.Credentialed || s.OAuth:
		return "status-pill needs-creds", "Needs credentials"
	default:
		return "status-pill active", "Active"
	}
}

// syncStatusPill maps a sync run's status to a pill: green for a completed run,
// red for a failure, grey for anything in between (running/pending).
func syncStatusPill(status string) (class, text string) {
	switch status {
	case "success", "completed":
		return "status-pill connected", status
	case "error", "failed":
		return "status-pill failed", status
	default:
		return "status-pill disconnected", status
	}
}

func sourceTitle(s sourceStatus) string {
	if strings.TrimSpace(s.Title) != "" {
		return s.Title
	}
	return s.Type
}

// sourceBlurb is the one-line description under a source's title in the list,
// falling back to the bare archetype word for an unrecognised type.
func sourceBlurb(s sourceStatus) string {
	switch s.Type {
	case "simplefin", "plaid", "teller":
		return "Bank aggregator"
	case "bitcoin", "ethereum":
		return "On-chain wallets"
	case "csv":
		return "Manual file import"
	case "market":
		return "Market & reference data"
	case "webhook":
		return "Pushed via inbound webhook"
	}
	return s.Archetype
}

func syncAllLabel(busy bool) string {
	if busy {
		return "Syncing…"
	}
	return "Sync all"
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

// sourceCredentialNoun names what an inactive source will let you add once it is
// activated — "watched addresses" for an address-watching source (Bitcoin,
// Ethereum), "credentials" otherwise — so the inactive detail page points at the
// right next step.
func sourceCredentialNoun(s sourceStatus) string {
	for _, c := range s.Credentials {
		if strings.Contains(strings.ToLower(c.Title), "address") {
			return "watched addresses"
		}
	}
	return "credentials"
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
	case "reference":
		return "External market & reference data, fetched on demand and cached."
	default:
		return "An ingestion source."
	}
}

// --- source type icons (one per known type, with a neutral fallback) ---

// sourceTypeIcon returns the inline-SVG glyph for a source type, rendered via
// app.Raw (a single <svg> root). Mirrors the desktop dashboard's per-type icons.
func sourceTypeIcon(typ string) app.UI {
	const open = `<svg class="source-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">`
	const close = `</svg>`
	// A plugin-provided source (ADR 0005) is typed plugin:<name>; show the puzzle
	// glyph that marks the plugin subsystem rather than the generic link fallback.
	if strings.HasPrefix(typ, "plugin:") {
		const puzzle = `<path d="M19.439 7.85c-.049.322.059.648.289.878l1.568 1.568c.47.47.706 1.087.706 1.704s-.235 1.233-.706 1.704l-1.611 1.611a.98.98 0 0 1-.837.276c-.47-.07-.802-.48-.968-.925a2.501 2.501 0 1 0-3.214 3.214c.446.166.855.497.925.968a.979.979 0 0 1-.276.837l-1.61 1.61a2.404 2.404 0 0 1-1.705.707 2.402 2.402 0 0 1-1.704-.706l-1.568-1.568a1.026 1.026 0 0 0-.877-.29c-.493.074-.84.504-1.02.968a2.5 2.5 0 1 1-3.237-3.237c.464-.18.894-.527.967-1.02a1.026 1.026 0 0 0-.289-.877l-1.568-1.568A2.402 2.402 0 0 1 1.998 12c0-.617.236-1.234.706-1.704L4.23 8.77c.24-.24.581-.353.917-.303.515.077.877.528 1.073 1.01a2.5 2.5 0 1 0 3.259-3.259c-.482-.196-.933-.558-1.01-1.073-.05-.336.062-.676.303-.917l1.525-1.525A2.402 2.402 0 0 1 12 1.998c.617 0 1.234.236 1.704.706l1.568 1.568c.23.23.556.338.877.29.493-.074.84-.504 1.02-.968a2.5 2.5 0 1 1 3.237 3.237c-.464.18-.894.527-.967 1.02Z"/>`
		return app.Raw(open + puzzle + close)
	}
	var paths string
	switch typ {
	case "simplefin": // landmark / bank building
		paths = `<line x1="3" x2="21" y1="22" y2="22"/><line x1="6" x2="6" y1="18" y2="11"/><line x1="10" x2="10" y1="18" y2="11"/><line x1="14" x2="14" y1="18" y2="11"/><line x1="18" x2="18" y1="18" y2="11"/><polygon points="12 2 20 7 4 7"/>`
	case "plaid": // credit card
		paths = `<rect width="20" height="14" x="2" y="5" rx="2"/><line x1="2" x2="22" y1="10" y2="10"/>`
	case "teller": // key
		paths = `<circle cx="7.5" cy="15.5" r="5.5"/><path d="m21 2-9.6 9.6"/><path d="m15.5 7.5 3 3L22 7l-3-3"/>`
	case "bitcoin": // coin with a B
		paths = `<circle cx="12" cy="12" r="10"/><path d="M9.5 8h4a2 2 0 1 1 0 4h-4zm0 4h4.5a2 2 0 1 1 0 4H9.5zM10 6.5v2m0 7v2m3-11v2m0 7v2"/>`
	case "ethereum": // diamond
		paths = `<path d="M12 2 6 12l6 3.5L18 12 12 2Z"/><path d="m6 13.5 6 8.5 6-8.5-6 3.5-6-3.5Z"/>`
	case "csv": // file with lines
		paths = `<path d="M15 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V7Z"/><path d="M14 2v5h5"/><line x1="8" x2="16" y1="13" y2="13"/><line x1="8" x2="16" y1="17" y2="17"/>`
	case "market": // line chart trending up
		paths = `<path d="M3 3v16a2 2 0 0 0 2 2h16"/><path d="m19 9-5 5-4-4-3 3"/>`
	case "webhook": // webhook (lucide)
		paths = `<path d="M18 16.98h-5.99c-1.1 0-1.95.94-2.48 1.9A4 4 0 0 1 2 17c.01-.7.2-1.4.57-2"/><path d="m6 17 3.13-5.78c.53-.97.1-2.18-.5-3.1a4 4 0 1 1 6.89-4.06"/><path d="m12 6 3.13 5.73C15.66 12.7 16.9 13 18 13a4 4 0 0 1 0 8"/>`
	default: // link / chain
		paths = `<path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71"/><path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71"/>`
	}
	return app.Raw(open + paths + close)
}

func iconChevronRight() app.UI {
	return app.Raw(`<svg class="source-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>`)
}

func iconArrowLeft() app.UI {
	return app.Raw(`<svg class="source-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m12 19-7-7 7-7"/><path d="M19 12H5"/></svg>`)
}
