package dashboard

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// securityTokenInputID is the DOM id of the optional custom dashboard-token input.
const securityTokenInputID = "kasas-security-token-input"

// settingsView is the Settings page: editable, permanently persisted settings
// for how kasas works (sync schedule, plugins, events, webhooks, MCP, updates,
// logging), the dashboard-security and API-key panels, and a read-only view of
// the bootstrap configuration that stays file/env-managed (server address,
// database, secret store). A changed setting overrides the config file / KASAS_
// environment and takes effect after a restart, offered by the banner up top.
// Ingestion sources — including SimpleFIN — are managed on the Sources page.
type settingsView struct {
	app.Compo
	chrome          // shared sidebar + API client + version badge
	settingsEditing // setting save/reset (shared with the Sources page)
	restartPrompt   // "restart required" banner + in-place restart

	cfg        configData
	cfgLoaded  bool
	loadingCfg bool

	// Editable settings state.
	items           []settingItem
	settingsEnabled bool
	settingsLoaded  bool

	// Force-sync state.
	latest  *syncRun
	syncing bool
	syncMsg string

	// Dashboard security panel state.
	generating  bool
	securityMsg string
	securityErr string
	newToken    string // freshly minted token, shown once

	// API keys panel state.
	apiKeys       []apiKey
	apiKeysLoaded bool
	creatingKey   bool
	newKey        string // freshly minted API key secret, shown once
	keyMsg        string
	keyErr        string

	// Postgres migration panel state (SQLite backend only).
	migrating     bool
	migrateMsg    string
	migrateErr    string
	migrateReport *migrateResult

	errMsg string
}

// migrateDSNInputID is the DOM id of the target-Postgres-DSN input.
const migrateDSNInputID = "kasas-migrate-dsn-input"

// DOM ids for the (uncontrolled) API-key create inputs.
const (
	apiKeyNameInputID   = "kasas-apikey-name-input"
	apiKeyScopeSelectID = "kasas-apikey-scope-select"
)

func (v *settingsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.initSettingsEditing(
		func() *apiClient { return v.client },
		v.adoptSetting,
	)
	v.loadConfig(ctx)
	v.loadSettings(ctx)
	v.loadLatestSync(ctx)
	v.loadApiKeys(ctx)
}

// loadSettings fetches the editable settings with their override/restart state.
func (v *settingsView) loadSettings(ctx app.Context) {
	ctx.Async(func() {
		data, err := v.client.listSettings(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.settingsLoaded = true
			if err != nil {
				v.errMsg = err.Error()
				ctx.Update()
				return
			}
			v.items = data.Settings
			v.settingsEnabled = data.Enabled
			v.restartNeeded = data.RestartRequired
			ctx.Update()
		})
	})
}

// adoptSetting folds a saved/reset setting back into the local list so the row
// shows the server-normalized value and state.
func (v *settingsView) adoptSetting(_ app.Context, st settingItem, restartRequired bool) {
	v.restartNeeded = restartRequired
	for i := range v.items {
		if v.items[i].Key == st.Key {
			v.items[i] = st
			return
		}
	}
}

// loadApiKeys fetches the provisioned API keys for the panel. Best-effort: a failure
// (e.g. an open instance with no auth) leaves the list empty.
func (v *settingsView) loadApiKeys(ctx app.Context) {
	ctx.Async(func() {
		keys, err := v.client.listApiKeys(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.apiKeysLoaded = true
			if err != nil {
				ctx.Update()
				return
			}
			v.apiKeys = keys
			ctx.Update()
		})
	})
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
			return
		}
	}
	ctx.Dispatch(func(ctx app.Context) {
		v.syncing = false
		v.syncMsg = "Sync is taking a while — check back shortly."
		ctx.Update()
	})
}

func (v *settingsView) Render() app.UI {
	return v.renderShell(navSettings,
		v.renderRestartBanner(func() *apiClient { return v.client }),
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Settings"),
			app.Span().Class("page-subtitle").Text("Configure how kasas works — changes are permanent and survive restarts"),
		),
		v.renderError(),
		v.renderSettingMessages(),
		v.renderSecurity(),
		v.renderApiKeys(),
		v.renderSettingSections(),
		v.renderMigratePostgres(),
		v.renderConfig(),
	)
}

// renderMigratePostgres renders the "Migrate to Postgres" panel, shown only on
// the SQLite backend (once already on Postgres there is nothing to migrate from).
// It copies the whole ledger into an empty Postgres database the operator names
// by DSN; the SQLite data is only read, so they can verify Postgres before
// switching database.driver and restarting.
func (v *settingsView) renderMigratePostgres() app.UI {
	if !v.cfgLoaded || v.cfg.Database.Driver != "sqlite" {
		return app.Text("")
	}

	body := []app.UI{
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("Migrate to Postgres"),
		),
		app.P().Class("settings-help").Text(
			"Copy this SQLite ledger into a Postgres database — every account, transaction, rule, event, " +
				"and setting, with ids preserved. The target must be an empty, reachable Postgres database; " +
				"kasas creates its schema there automatically. Your SQLite data is only read and stays " +
				"unchanged, so you can verify Postgres before switching."),
		app.Div().Class("form-row").Body(
			app.Input().
				ID(migrateDSNInputID).
				Class("settings-input").
				Type("text").
				Placeholder("postgres://user:pass@host:5432/kasas?sslmode=disable").
				AutoComplete(false),
			app.Button().
				Class("btn btn-primary").
				Text(migrateLabel(v.migrating)).
				Disabled(v.migrating).
				OnClick(v.onMigratePostgres),
		),
		app.P().Class("settings-help").Text(
			"When it finishes, set database.driver=postgres and database.dsn (or KASAS_DATABASE_DRIVER / " +
				"KASAS_DATABASE_DSN) and restart kasas to run on Postgres."),
	}

	if v.migrateErr != "" {
		body = append(body, app.Div().Class("error").Text("Error: "+v.migrateErr))
	}
	if v.migrateMsg != "" && v.migrateErr == "" {
		body = append(body, app.Div().Class("settings-ok").Text(v.migrateMsg))
	}
	if v.migrateReport != nil {
		body = append(body, v.renderMigrateReport())
	}

	return app.Section().Class("card settings-section").Body(body...)
}

// renderMigrateReport shows the per-table row counts from a completed migration.
func (v *settingsView) renderMigrateReport() app.UI {
	rep := v.migrateReport
	return app.Table().Class("txns rules-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Table"),
				app.Th().Class("right").Text("Rows copied"),
			),
		),
		app.TBody().Body(
			app.Range(rep.Tables).Slice(func(i int) app.UI {
				return app.Tr().Body(
					app.Td().Text(rep.Tables[i].Table),
					app.Td().Class("right").Text(strconv.FormatInt(rep.Tables[i].Rows, 10)),
				)
			}),
		),
	)
}

func migrateLabel(busy bool) string {
	if busy {
		return "Migrating…"
	}
	return "Migrate to Postgres"
}

// onMigratePostgres validates the DSN, confirms, then asks the server to copy the
// ledger into Postgres, showing the per-table result on success.
func (v *settingsView) onMigratePostgres(ctx app.Context, _ app.Event) {
	if v.migrating {
		return
	}
	dsn := strings.TrimSpace(domInputValue(migrateDSNInputID))
	if dsn == "" {
		v.migrateErr = "Enter the target Postgres DSN."
		v.migrateMsg = ""
		ctx.Update()
		return
	}
	if !app.Window().Call("confirm",
		"Copy this SQLite ledger into Postgres?\n\nThe target database must be empty. Your SQLite data is only read and left unchanged.").Bool() {
		return
	}

	v.migrating = true
	v.migrateErr = ""
	v.migrateReport = nil
	v.migrateMsg = "Migrating… this can take a while for a large ledger."
	ctx.Update()

	ctx.Async(func() {
		res, err := v.client.migrateToPostgres(context.Background(), dsn)
		ctx.Dispatch(func(ctx app.Context) {
			v.migrating = false
			if err != nil {
				v.migrateErr = err.Error()
				v.migrateMsg = ""
				ctx.Update()
				return
			}
			report := res
			v.migrateReport = &report
			v.migrateMsg = res.Message
			ctx.Update()
		})
	})
}

// renderSettingSections renders the editable app settings grouped by section
// (Sync, Events & history, Webhooks, Plugins, ...). Per-source settings carry a
// Source instead of a Section and live on the Sources page.
func (v *settingsView) renderSettingSections() app.UI {
	if !v.settingsLoaded {
		return app.Div().Class("status").Text("Loading…")
	}
	if !v.settingsEnabled {
		return app.Div().Class("settings-note").Text("Settings management is unavailable in this build.")
	}

	// Collect the app-setting sections in definition order.
	var sections []string
	bySection := map[string][]settingItem{}
	for _, it := range v.items {
		if it.Source != "" || it.Section == "" {
			continue
		}
		if _, ok := bySection[it.Section]; !ok {
			sections = append(sections, it.Section)
		}
		bySection[it.Section] = append(bySection[it.Section], it)
	}

	cards := []app.UI{
		app.P().Class("settings-help").Text(
			"Changes save immediately and permanently: they override the config file and KASAS_ environment " +
				"and still apply after restarts and updates. A change to how kasas runs takes effect at the " +
				"next restart — the banner at the top offers one when needed. Reset returns a setting to your config."),
	}
	for _, sec := range sections {
		rows := []app.UI{
			app.Div().Class("settings-section-head").Body(
				app.H2().Class("settings-title").Text(sec),
			),
		}
		for i := range bySection[sec] {
			rows = append(rows, v.renderSettingRow(bySection[sec][i]))
		}
		// The Sync section also hosts the run-now control, next to its schedule.
		if sec == "Sync" {
			rows = append(rows,
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
		}
		cards = append(cards, app.Section().Class("card settings-section").Body(rows...))
	}

	return app.Section().Class("settings-section").Body(
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("How kasas works"),
		),
		app.Div().Body(cards...),
	)
}

func (v *settingsView) renderError() app.UI {
	if v.errMsg == "" {
		return app.Text("")
	}
	return app.Div().Class("error").Text("Error: " + v.errMsg)
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

// renderSecurity renders the dashboard-token panel: status, and (unless the token
// is config-managed) controls to generate, set, or revoke it.
func (v *settingsView) renderSecurity() app.UI {
	if !v.cfgLoaded {
		// The security state rides along in the config payload, still loading.
		return app.Text("")
	}

	sec := v.cfg.Security
	pillClass, pillText := "status-pill disconnected", "Unsecured"
	if sec.AuthRequired {
		pillClass, pillText = "status-pill connected", "Secured"
	}

	body := []app.UI{
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("Dashboard security"),
			app.Span().Class(pillClass).Text(pillText),
		),
		app.P().Class("settings-help").Text(
			"A dashboard token protects the REST API, this dashboard, and the MCP server — " +
				"clients send it as \"Authorization: Bearer <token>\". The health, readiness, and " +
				"metrics endpoints stay open."),
	}

	if sec.TokenSource == "config" {
		body = append(body, app.Div().Class("settings-note").Text(
			"The token is set via configuration (dashboard.token or KASAS_DASHBOARD_TOKEN), which is "+
				"authoritative. Remove it from your config to generate or manage a token here."))
	} else {
		body = append(body, v.renderSecurityControls(sec)...)
	}

	if v.securityErr != "" {
		body = append(body, app.Div().Class("error").Text("Error: "+v.securityErr))
	}
	if v.securityMsg != "" {
		body = append(body, app.Div().Class("settings-ok").Text(v.securityMsg))
	}
	if v.newToken != "" {
		body = append(body, v.renderNewToken())
	}

	return app.Section().Class("card settings-section").Body(body...)
}

// renderSecurityControls renders the generate / set / revoke controls shown when
// the token is not config-managed.
func (v *settingsView) renderSecurityControls(sec securityConfig) []app.UI {
	rows := []app.UI{
		app.Div().Class("form-row").Body(
			app.Button().
				Class("btn btn-primary").
				Text(generateLabel(v.generating)).
				Disabled(v.generating).
				OnClick(v.onGenerateToken),
		),
		app.P().Class("settings-help").Text("Or set your own token (at least 16 characters):"),
		app.Div().Class("form-row").Body(
			app.Input().
				ID(securityTokenInputID).
				Class("settings-input").
				Type("password").
				Placeholder("Custom token").
				AutoComplete(false),
			app.Button().
				Class("btn").
				Text("Save").
				Disabled(v.generating).
				OnClick(v.onSaveCustomToken),
		),
	}
	if sec.AuthRequired && sec.TokenSource == "stored" {
		rows = append(rows,
			app.Div().Class("settings-divider"),
			app.Div().Class("form-row").Body(
				app.Button().
					Class("btn btn-danger").
					Text("Revoke token (disable security)").
					Disabled(v.generating).
					OnClick(v.onRevokeToken),
			),
		)
	}
	return rows
}

// renderNewToken shows a freshly minted token once, with a copy button.
func (v *settingsView) renderNewToken() app.UI {
	return app.Div().Class("token-reveal").Body(
		app.P().Class("settings-help").Text(
			"Save this token now — it will not be shown again. This browser is signed in; "+
				"use the token for other browsers and for REST/MCP clients."),
		app.Div().Class("form-row").Body(
			app.Input().Class("settings-input token-value").Type("text").ReadOnly(true).Value(v.newToken),
			app.Button().Class("btn").Text("Copy").OnClick(v.onCopyToken),
		),
	)
}

func generateLabel(busy bool) string {
	if busy {
		return "Generating…"
	}
	return "Generate token"
}

func (v *settingsView) onGenerateToken(ctx app.Context, _ app.Event) { v.mintToken(ctx, "") }

func (v *settingsView) onSaveCustomToken(ctx app.Context, _ app.Event) {
	custom := domInputValue(securityTokenInputID)
	if custom == "" {
		v.securityErr = "Enter a token, or use Generate."
		v.securityMsg = ""
		ctx.Update()
		return
	}
	v.mintToken(ctx, custom)
}

// mintToken generates (custom == "") or sets a dashboard token, then adopts it so
// this page stays signed in without a reload (a reload would hide the one-time
// value shown below the controls).
func (v *settingsView) mintToken(ctx app.Context, custom string) {
	if v.generating {
		return
	}
	v.generating = true
	v.securityErr = ""
	v.securityMsg = ""
	v.newToken = ""
	ctx.Update()

	ctx.Async(func() {
		res, err := v.client.setToken(context.Background(), custom)
		ctx.Dispatch(func(ctx app.Context) {
			v.generating = false
			if err != nil {
				v.securityErr = err.Error()
				ctx.Update()
				return
			}
			v.adoptToken(ctx, res.Token)
			v.cfg.Security.AuthRequired = res.AuthRequired
			v.cfg.Security.TokenSource = res.TokenSource
			v.newToken = res.Token
			v.securityMsg = "Dashboard token saved."
			clearDomInput(securityTokenInputID)
			ctx.Update()
		})
	})
}

func (v *settingsView) onRevokeToken(ctx app.Context, _ app.Event) {
	if v.generating {
		return
	}
	v.generating = true
	v.securityErr = ""
	v.securityMsg = ""
	v.newToken = ""
	ctx.Update()

	ctx.Async(func() {
		err := v.client.revokeToken(context.Background())
		ctx.Dispatch(func(ctx app.Context) {
			v.generating = false
			if err != nil {
				v.securityErr = err.Error()
				ctx.Update()
				return
			}
			v.adoptToken(ctx, "")
			v.cfg.Security.AuthRequired = false
			v.cfg.Security.TokenSource = "none"
			v.securityMsg = "Dashboard token revoked. kasas is now unsecured."
			ctx.Update()
		})
	})
}

func (v *settingsView) onCopyToken(ctx app.Context, _ app.Event) {
	if v.newToken == "" {
		return
	}
	clip := app.Window().Get("navigator").Get("clipboard")
	if !clip.Truthy() {
		return // clipboard API unavailable (e.g. non-secure context); user can select the text
	}
	clip.Call("writeText", v.newToken)
	v.securityMsg = "Token copied to clipboard."
	ctx.Update()
}

// renderApiKeys renders the API-key provisioning panel: a create form (name +
// scope), the one-time secret reveal, and the list of existing keys with revoke.
func (v *settingsView) renderApiKeys() app.UI {
	body := []app.UI{
		app.Div().Class("settings-section-head").Body(
			app.H2().Class("settings-title").Text("API keys"),
		),
		app.P().Class("settings-help").Text(
			"Per-app credentials for programmatic REST access, separate from the dashboard token. " +
				"A read key can only GET; a read-write key can also change data. Clients send the key as " +
				"\"Authorization: Bearer kasas_…\". Provisioning stays admin-only (the dashboard token)."),
		app.Div().Class("form-row").Body(
			app.Input().
				ID(apiKeyNameInputID).
				Class("settings-input").
				Type("text").
				Placeholder("Name (e.g. Budgeting app)").
				AutoComplete(false),
			app.Select().ID(apiKeyScopeSelectID).Class("account-select").Body(
				app.Option().Value("read").Text("Read-only"),
				app.Option().Value("read_write").Text("Read & write"),
			),
			app.Button().
				Class("btn btn-primary").
				Text(createKeyLabel(v.creatingKey)).
				Disabled(v.creatingKey).
				OnClick(v.onCreateApiKey),
		),
	}

	if v.keyErr != "" {
		body = append(body, app.Div().Class("error").Text("Error: "+v.keyErr))
	}
	if v.keyMsg != "" {
		body = append(body, app.Div().Class("settings-ok").Text(v.keyMsg))
	}
	if v.newKey != "" {
		body = append(body, v.renderNewKey())
	}
	body = append(body, v.renderApiKeyList())

	return app.Section().Class("card settings-section").Body(body...)
}

// renderNewKey shows a freshly minted API key once, with a copy button.
func (v *settingsView) renderNewKey() app.UI {
	return app.Div().Class("token-reveal").Body(
		app.P().Class("settings-help").Text(
			"Copy this key now — it is shown only once and stored only as a hash. "+
				"Give it to the app that needs API access."),
		app.Div().Class("form-row").Body(
			app.Input().Class("settings-input token-value").Type("text").ReadOnly(true).Value(v.newKey),
			app.Button().Class("btn").Text("Copy").OnClick(v.onCopyApiKey),
		),
	)
}

func (v *settingsView) renderApiKeyList() app.UI {
	if !v.apiKeysLoaded {
		return app.Div().Class("status").Text("Loading…")
	}
	if len(v.apiKeys) == 0 {
		return app.Div().Class("settings-note").Text("No API keys yet.")
	}
	return app.Table().Class("txns rules-table apikeys-table").Body(
		app.THead().Body(
			app.Tr().Body(
				app.Th().Text("Name"),
				app.Th().Text("Key"),
				app.Th().Text("Scope"),
				app.Th().Text("Last used"),
				app.Th().Class("right").Text(""),
			),
		),
		app.TBody().Body(
			app.Range(v.apiKeys).Slice(func(i int) app.UI {
				return v.renderApiKeyRow(v.apiKeys[i])
			}),
		),
	)
}

func (v *settingsView) renderApiKeyRow(k apiKey) app.UI {
	return app.Tr().Body(
		app.Td().Text(orNone(k.Name)),
		app.Td().Body(app.Code().Class("rule-query").Text(k.Prefix+"…")),
		app.Td().Text(scopeText(k.Scope)),
		app.Td().Text(lastUsedText(k.LastUsedAt)),
		app.Td().Class("right rule-actions").Body(
			app.Button().Type("button").Class("label-delete").Title("Revoke key").
				OnClick(func(ctx app.Context, _ app.Event) { v.onRevokeApiKey(ctx, k) }).
				Body(iconTrash()),
		),
	)
}

func createKeyLabel(busy bool) string {
	if busy {
		return "Creating…"
	}
	return "Create key"
}

func scopeText(scope string) string {
	if scope == "read_write" {
		return "Read & write"
	}
	return "Read-only"
}

// lastUsedText renders a key's last-used time, or "Never" when it has not been used.
func lastUsedText(t *time.Time) string {
	if t == nil || t.IsZero() {
		return "Never"
	}
	return t.Format("2006-01-02 15:04") + " UTC"
}

func (v *settingsView) onCreateApiKey(ctx app.Context, _ app.Event) {
	if v.creatingKey {
		return
	}
	name := domInputValue(apiKeyNameInputID)
	scope := domInputValue(apiKeyScopeSelectID)
	if scope == "" {
		scope = "read"
	}
	v.creatingKey = true
	v.keyErr, v.keyMsg, v.newKey = "", "", ""
	ctx.Update()

	ctx.Async(func() {
		key, err := v.client.createApiKey(context.Background(), name, scope)
		ctx.Dispatch(func(ctx app.Context) {
			v.creatingKey = false
			if err != nil {
				v.keyErr = err.Error()
				ctx.Update()
				return
			}
			v.newKey = key.Key
			key.Key = "" // don't retain the secret in the list row
			v.apiKeys = append([]apiKey{key}, v.apiKeys...)
			v.keyMsg = "API key created."
			clearDomInput(apiKeyNameInputID)
			ctx.Update()
		})
	})
}

func (v *settingsView) onRevokeApiKey(ctx app.Context, k apiKey) {
	label := k.Name
	if strings.TrimSpace(label) == "" {
		label = k.Prefix + "…"
	}
	if !app.Window().Call("confirm", "Revoke API key "+label+"? Apps using it lose access immediately.").Bool() {
		return
	}
	prev := v.apiKeys
	v.apiKeys = removeApiKey(v.apiKeys, k.ID)
	v.keyErr, v.keyMsg = "", ""
	ctx.Update()

	id := k.ID
	ctx.Async(func() {
		err := v.client.revokeApiKey(context.Background(), id)
		ctx.Dispatch(func(ctx app.Context) {
			if err != nil {
				v.apiKeys = prev // revert the optimistic removal
				v.keyErr = "Failed to revoke key: " + err.Error()
				ctx.Update()
				return
			}
			v.keyMsg = "API key revoked."
			ctx.Update()
		})
	})
}

func (v *settingsView) onCopyApiKey(ctx app.Context, _ app.Event) {
	if v.newKey == "" {
		return
	}
	clip := app.Window().Get("navigator").Get("clipboard")
	if !clip.Truthy() {
		return
	}
	clip.Call("writeText", v.newKey)
	v.keyMsg = "API key copied to clipboard."
	ctx.Update()
}

func removeApiKey(list []apiKey, id int64) []apiKey {
	out := make([]apiKey, 0, len(list))
	for _, k := range list {
		if k.ID == id {
			continue
		}
		out = append(out, k)
	}
	return out
}

// renderConfig renders the read-only bootstrap configuration: the few values
// that stay file/env-managed because kasas needs them before it can read its
// stored settings (the listen address, the database itself, and the secret
// store). Everything else is editable in the sections above or on the Sources
// page.
func (v *settingsView) renderConfig() app.UI {
	head := app.Div().Class("settings-section-head").Body(
		app.H2().Class("settings-title").Text("Bootstrap configuration"),
	)
	note := app.P().Class("settings-help").Text(
		"These load from the config file or KASAS_ environment variables before the database opens, " +
			"so they cannot be changed here — edit your config and restart kasas. Secrets are not shown.")

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
		configCard("Database", v.databaseRows(c.Database)...),
		configCard("Secret store", v.vaultRows(c.Vault, c.Secrets)...),
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

func configuredText(b bool) string {
	if b {
		return "Configured"
	}
	return "Not set"
}

// orNone renders an empty value as a muted dash so empty rows are not ambiguous.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
