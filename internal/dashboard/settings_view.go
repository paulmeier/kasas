package dashboard

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// tokenInputID is the stable DOM id of the SimpleFIN credential input, so its
// value can be read on save and cleared imperatively afterwards (go-app drops
// empty value attributes, so a controlled Value("") could not clear it).
const tokenInputID = "simplefin-token-input"

// settingsView is the Settings page: a SimpleFIN connection panel (set the
// credential at runtime + force a sync, with live status) and a read-only view of
// the effective configuration (secrets redacted). Config other than the SimpleFIN
// credential is loaded from the config file / KASAS_ env at startup and is not
// editable here — changing it means editing the config and restarting.
type settingsView struct {
	app.Compo
	chrome // shared sidebar + API client + version badge

	cfg        configData
	cfgLoaded  bool
	connected  bool
	loadingCfg bool

	// SimpleFIN credential save state.
	saving  bool
	saveMsg string

	// Force-sync state.
	latest  *syncRun
	syncing bool
	syncMsg string

	errMsg string
}

func (v *settingsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.loadConfig(ctx)
	v.loadLatestSync(ctx)
}

func (v *settingsView) loadConfig(ctx app.Context) {
	v.loadingCfg = true
	ctx.Async(func() {
		cfg, err := v.client.config(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.loadingCfg = false
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.cfg = cfg
			v.cfgLoaded = true
			v.connected = cfg.SimpleFIN.Connected
			ctx.Update()
		})
	})
}

func (v *settingsView) loadLatestSync(ctx app.Context) {
	ctx.Async(func() {
		run, err := v.client.latestSync(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				return // best-effort; the connection panel still works
			}
			v.latest = run
			ctx.Update()
		})
	})
}

// onSaveToken stores the SimpleFIN setup token or access URL entered in the input.
// The value is read straight from the DOM (the input is uncontrolled) and cleared
// on success so the secret does not linger in the field.
func (v *settingsView) onSaveToken(ctx app.Context, _ app.Event) { v.submitToken(ctx) }

func (v *settingsView) onTokenKeyDown(ctx app.Context, e app.Event) {
	if e.Get("key").String() == "Enter" {
		e.PreventDefault()
		v.submitToken(ctx)
	}
}

func (v *settingsView) submitToken(ctx app.Context) {
	if v.saving {
		return
	}
	token := tokenInputValue()
	if token == "" {
		v.errMsg = "Enter a SimpleFIN setup token or access URL."
		v.saveMsg = ""
		ctx.Update()
		return
	}
	v.saving = true
	v.saveMsg = ""
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		connected, err := v.client.setSimpleFINToken(context.Background(), token)
		ctx.Dispatch(func(ctx app.Context) {
			v.saving = false
			if err != nil {
				v.errMsg = "Could not save credential: " + err.Error()
				ctx.Update()
				return
			}
			v.connected = connected
			v.cfg.SimpleFIN.Connected = connected
			v.saveMsg = "SimpleFIN credential saved. Run a sync to pull your data."
			clearTokenInput()
			ctx.Update()
		})
	})
}

// onSyncNow triggers a sync and then polls the sync status until a run newer than
// the last one finishes, so the panel reflects live progress.
func (v *settingsView) onSyncNow(ctx app.Context, _ app.Event) {
	if v.syncing {
		return
	}
	var priorID int64
	if v.latest != nil {
		priorID = v.latest.ID
	}
	v.syncing = true
	v.syncMsg = "Starting sync…"
	v.errMsg = ""
	ctx.Update()

	ctx.Async(func() {
		if err := v.client.triggerSync(context.Background()); err != nil {
			ctx.Dispatch(func(ctx app.Context) {
				v.syncing = false
				v.syncMsg = ""
				v.errMsg = "Sync failed to start: " + err.Error()
				ctx.Update()
			})
			return
		}
		v.pollSync(ctx, priorID)
	})
}

// pollSync waits for a sync run with an id newer than priorID to appear and
// complete, dispatching status updates as it goes. Runs inside the onSyncNow
// goroutine (already off the UI thread).
func (v *settingsView) pollSync(ctx app.Context, priorID int64) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(1500 * time.Millisecond)
		run, err := v.client.latestSync(context.Background())
		if err != nil || run == nil || run.ID == priorID {
			continue // the new run is not visible yet
		}
		done := run.Status != "running"
		ctx.Dispatch(func(ctx app.Context) {
			v.latest = run
			if done {
				v.syncing = false
				v.syncMsg = ""
			} else {
				v.syncMsg = "Sync in progress…"
			}
			ctx.Update()
		})
		if done {
			// A first successful sync may have just connected us; refresh config so
			// the connection state and any other derived display stays accurate.
			if cfg, err := v.client.config(context.Background()); err == nil {
				ctx.Dispatch(func(ctx app.Context) {
					v.cfg = cfg
					v.connected = cfg.SimpleFIN.Connected
					ctx.Update()
				})
			}
			return
		}
	}
	ctx.Dispatch(func(ctx app.Context) {
		v.syncing = false
		v.syncMsg = "Sync is taking a while — check back shortly."
		ctx.Update()
	})
}

// tokenInputValue reads and trims the credential input's current value from the
// DOM. Returns "" when the element is absent (e.g. during a host/test render).
func tokenInputValue() string {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return ""
	}
	el := doc.Call("getElementById", tokenInputID)
	if !el.Truthy() {
		return ""
	}
	return strings.TrimSpace(el.Get("value").String())
}

// clearTokenInput empties the credential input imperatively.
func clearTokenInput() {
	doc := app.Window().Get("document")
	if !doc.Truthy() {
		return
	}
	el := doc.Call("getElementById", tokenInputID)
	if el.Truthy() {
		el.Set("value", "")
	}
}

func (v *settingsView) Render() app.UI {
	return v.renderShell(navSettings,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Settings"),
			app.Span().Class("page-subtitle").Text("Connect SimpleFIN, run a sync, and review your configuration"),
		),
		v.renderError(),
		v.renderSimpleFIN(),
		v.renderConfig(),
	)
}

func (v *settingsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
}

// renderSimpleFIN renders the connection panel: status, the credential form, and
// the force-sync control with live status.
func (v *settingsView) renderSimpleFIN() app.UI {
	pillClass := "status-pill disconnected"
	pillText := "Not connected"
	if v.connected {
		pillClass = "status-pill connected"
		pillText = "Connected"
	}

	body := []app.UI{
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("SimpleFIN connection"),
			app.Span().Class(pillClass).Text(pillText),
		),
		app.P().Class("settings-help").Text(
			"Paste a SimpleFIN setup token (the one-time base64 token from your bridge) " +
				"or a full access URL. It is stored securely and used on the next sync — no restart needed."),
		app.Div().Class("form-row").Body(
			app.Input().
				ID(tokenInputID).
				Class("settings-input").
				Type("password").
				Placeholder("Setup token or access URL").
				AutoComplete(false).
				OnKeyDown(v.onTokenKeyDown),
			app.Button().
				Class("btn btn-primary").
				Text(saveLabel(v.saving)).
				Disabled(v.saving).
				OnClick(v.onSaveToken),
		),
	}
	if v.saveMsg != "" {
		body = append(body, app.Div().Class("settings-ok").Text(v.saveMsg))
	}

	body = append(body,
		app.Div().Class("settings-divider"),
		app.Div().Class("form-row").Body(
			app.Button().
				Class("btn btn-primary").
				Text(syncLabel(v.syncing)).
				Disabled(v.syncing).
				OnClick(v.onSyncNow),
			v.renderSyncStatus(),
		),
	)

	return app.Section().Class("card settings-section").Body(body...)
}

func saveLabel(saving bool) string {
	if saving {
		return "Saving…"
	}
	return "Save"
}

func syncLabel(syncing bool) string {
	if syncing {
		return "Syncing…"
	}
	return "Sync now"
}

// renderSyncStatus shows the in-progress message while syncing, otherwise a
// summary of the most recent run.
func (v *settingsView) renderSyncStatus() app.UI {
	if v.syncing {
		msg := v.syncMsg
		if msg == "" {
			msg = "Sync in progress…"
		}
		return app.Span().Class("sync-status").Text(msg)
	}
	if v.syncMsg != "" {
		return app.Span().Class("sync-status").Text(v.syncMsg)
	}
	if v.latest == nil {
		return app.Span().Class("sync-status muted").Text("No sync has run yet.")
	}

	r := v.latest
	switch r.Status {
	case "success":
		when := ""
		if !r.CompletedAt.IsZero() {
			when = " · " + r.CompletedAt.Format("2006-01-02 15:04") + " UTC"
		}
		return app.Span().Class("sync-status ok").Text("Last sync succeeded" + when)
	case "error":
		txt := "Last sync failed"
		if r.Error != "" {
			txt += ": " + r.Error
		}
		return app.Span().Class("sync-status err").Text(txt)
	case "running":
		return app.Span().Class("sync-status").Text("Sync in progress…")
	default:
		return app.Span().Class("sync-status muted").Text("Last sync: " + r.Status)
	}
}

// renderConfig renders the read-only effective configuration as a grid of cards.
func (v *settingsView) renderConfig() app.UI {
	head := app.Div().Class("settings-section-head").Body(
		app.H2().Class("settings-title").Text("Configuration"),
	)
	note := app.P().Class("settings-help").Text(
		"Loaded from the config file or KASAS_ environment variables at startup. " +
			"To change these, edit your config and restart kasas. Secrets are not shown.")

	if !v.cfgLoaded {
		status := "Loading…"
		if !v.loadingCfg && v.errMsg != "" {
			status = "Configuration unavailable."
		}
		return app.Section().Class("settings-section").Body(head, note, app.Div().Class("status").Text(status))
	}

	c := v.cfg
	cards := []app.UI{
		configCard("Server", configRow("Address", c.Server.Addr)),
		configCard("Logging",
			configRow("Level", c.Log.Level),
			configRow("Format", c.Log.Format),
		),
		configCard("Database", v.databaseRows(c.Database)...),
		configCard("Sync",
			configRow("Enabled", enabledText(c.Sync.Enabled)),
			configRow("Interval", c.Sync.Interval),
			configRow("Lookback", lookbackText(c.Sync.LookbackDays)),
			configRow("Run on start", yesNo(c.Sync.RunOnStart)),
		),
		configCard("Secret store", v.vaultRows(c.Vault, c.Secrets)...),
		configCard("MCP server", configRow("Enabled", enabledText(c.MCP.Enabled))),
		configCard("Dashboard", configRow("Enabled", enabledText(c.Dashboard.Enabled))),
		configCard("Updates",
			configRow("Check", yesNo(c.Update.Check)),
			configRow("Allow apply", yesNo(c.Update.AllowApply)),
			configRow("Repository", c.Update.Repository),
		),
	}

	return app.Section().Class("settings-section").Body(
		head,
		note,
		app.Div().Class("config-grid").Body(cards...),
	)
}

func (v *settingsView) databaseRows(d databaseConfig) []app.UI {
	rows := []app.UI{configRow("Driver", d.Driver)}
	if d.Driver == "postgres" {
		rows = append(rows, configRow("DSN", orNone(d.DSN)))
	} else {
		rows = append(rows, configRow("Path", d.Path))
	}
	return rows
}

func (v *settingsView) vaultRows(vlt vaultConfig, sec secretsConfig) []app.UI {
	if !vlt.Enabled {
		return []app.UI{
			configRow("Backend", "Local file"),
			configRow("File", sec.File),
		}
	}
	return []app.UI{
		configRow("Backend", "HashiCorp Vault"),
		configRow("Address", vlt.Address),
		configRow("Mount", vlt.Mount),
		configRow("Path", vlt.Path),
		configRow("Key", vlt.AccessURLKey),
		configRow("Token", configuredText(vlt.TokenSet)),
	}
}

// configRow is one key/value line inside a config card.
func configRow(key, val string) app.UI {
	return app.Div().Class("config-row").Body(
		app.Span().Class("config-key").Text(key),
		app.Span().Class("config-val").Text(orNone(val)),
	)
}

// configCard is a titled card holding a set of config rows.
func configCard(title string, rows ...app.UI) app.UI {
	body := make([]app.UI, 0, len(rows)+1)
	body = append(body, app.Div().Class("config-card-title").Text(title))
	body = append(body, rows...)
	return app.Div().Class("card config-card").Body(body...)
}

func enabledText(b bool) string {
	if b {
		return "Enabled"
	}
	return "Disabled"
}

func yesNo(b bool) string {
	if b {
		return "Yes"
	}
	return "No"
}

func configuredText(b bool) string {
	if b {
		return "Configured"
	}
	return "Not set"
}

func lookbackText(days int) string {
	if days <= 0 {
		return "All available"
	}
	if days == 1 {
		return "1 day"
	}
	return strconv.Itoa(days) + " days"
}

// orNone renders an empty value as a muted dash so empty rows are not ambiguous.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
