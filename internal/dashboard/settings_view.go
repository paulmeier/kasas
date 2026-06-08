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

// securityTokenInputID is the DOM id of the optional custom dashboard-token input.
const securityTokenInputID = "kasas-security-token-input"

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

	errMsg string
}

// DOM ids for the (uncontrolled) API-key create inputs.
const (
	apiKeyNameInputID   = "kasas-apikey-name-input"
	apiKeyScopeSelectID = "kasas-apikey-scope-select"
)

func (v *settingsView) OnMount(ctx app.Context) {
	v.loadChrome(ctx)
	v.loadConfig(ctx)
	v.loadLatestSync(ctx)
	v.loadApiKeys(ctx)
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
	return domInputValue(tokenInputID)
}

// clearTokenInput empties the credential input imperatively.
func clearTokenInput() {
	clearDomInput(tokenInputID)
}

func (v *settingsView) Render() app.UI {
	return v.renderShell(navSettings,
		app.Header().Class("page-header").Body(
			app.H1().Class("page-title").Text("Settings"),
			app.Span().Class("page-subtitle").Text("Connect SimpleFIN, run a sync, and review your configuration"),
		),
		v.renderError(),
		v.renderSimpleFIN(),
		v.renderSecurity(),
		v.renderApiKeys(),
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
		app.P().Class("settings-help").Body(
			app.Text("Manage every ingestion source — including CSV file import — on the "),
			app.A().Class("settings-link").Href("/sources").Text("Sources page"),
			app.Text("."),
		),
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
		configCard("Events & history",
			configRow("Enabled", enabledText(c.Events.Enabled)),
			configRow("Event retention", retentionText(c.Events.RetentionDays)),
			configRow("History retention", retentionText(c.Events.HistoryRetentionDays)),
		),
		configCard("Webhooks",
			configRow("Enabled", enabledText(c.Webhooks.Enabled)),
			configRow("Timeout", c.Webhooks.Timeout),
			configRow("Max attempts", strconv.Itoa(c.Webhooks.MaxAttempts)),
		),
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

// retentionText renders a retention-days setting, where 0 means keep forever.
func retentionText(days int) string {
	if days <= 0 {
		return "Forever"
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
