package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

func printUI(t *testing.T, ui app.UI) string {
	t.Helper()
	var buf bytes.Buffer
	app.PrintHTML(&buf, ui)
	return buf.String()
}

func sampleConfig() configData {
	return configData{
		Server:    serverConfig{Addr: ":8080"},
		Log:       logConfig{Level: "info", Format: "json"},
		Database:  databaseConfig{Driver: "sqlite", Path: "/data/kasas.db"},
		SimpleFIN: simplefinConfig{Connected: true},
		Sync:      syncConfig{Enabled: true, Interval: "6h0m0s", LookbackDays: 90, RunOnStart: true},
		Vault:     vaultConfig{Enabled: false},
		Secrets:   secretsConfig{File: "/data/secrets.json"},
		MCP:       mcpConfig{Enabled: true},
		Dashboard: dashboardConfig{Enabled: true},
		Update:    updateConfig{Check: true, AllowApply: true, Repository: "paulmeier/kasas"},
	}
}

func sampleSettings() []settingItem {
	return []settingItem{
		{Key: "sync.enabled", Title: "Background sync", Kind: "bool", Section: "Sync", Value: "true"},
		{Key: "sync.interval", Title: "Interval", Kind: "duration", Section: "Sync", Value: "6h0m0s"},
		{Key: "plugins.enabled", Title: "Plugin system", Kind: "bool", Section: "Plugins", Value: "true", Overridden: true, RestartRequired: true},
		{Key: "log.level", Title: "Log level", Kind: "string", Section: "Logging", Value: "info", Enum: []string{"debug", "info", "warn", "error"}},
		// A per-source setting must NOT render on the Settings page.
		{Key: "plaid.client_id", Title: "Client ID", Kind: "string", Source: "plaid", Value: ""},
	}
}

// TestSettingsViewRenders checks the page renders the editable setting
// sections, their override/restart state, and the read-only bootstrap cards —
// and that SimpleFIN now lives only on the Sources page.
func TestSettingsViewRenders(t *testing.T) {
	v := &settingsView{
		cfgLoaded:       true,
		cfg:             sampleConfig(),
		settingsLoaded:  true,
		settingsEnabled: true,
		items:           sampleSettings(),
	}
	html := printUI(t, v.Render())

	wantContains := []string{
		"How kasas works",                  // editable settings heading
		"Plugin system",                    // a bool setting rendered
		"overridden",                       // override chip
		"restart pending",                  // restart chip
		`id="setting-input-sync.interval"`, // editable input for a duration
		"Sync now",                         // run-now control inside the Sync card
		"Bootstrap configuration",          // read-only section heading
		"/data/kasas.db",                   // a bootstrap config value rendered
	}
	for _, want := range wantContains {
		if !strings.Contains(html, want) {
			t.Fatalf("settings render missing %q\nHTML:\n%s", want, html)
		}
	}

	wantAbsent := []string{
		"SimpleFIN connection",       // moved to the Sources page
		`id="simplefin-token-input"`, // the old credential input
		"plaid.client_id",            // per-source settings live on the Sources page
	}
	for _, bad := range wantAbsent {
		if strings.Contains(html, bad) {
			t.Fatalf("settings render must not contain %q\nHTML:\n%s", bad, html)
		}
	}
}

// TestSettingsViewRestartBanner checks the pending-restart banner renders with
// its restart action when a stored change awaits a restart.
func TestSettingsViewRestartBanner(t *testing.T) {
	v := &settingsView{cfgLoaded: true, cfg: sampleConfig(), settingsLoaded: true, settingsEnabled: true}
	v.restartNeeded = true
	html := printUI(t, v.Render())

	if !strings.Contains(html, "waiting for a restart") {
		t.Fatalf("restart banner missing\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "Restart kasas") {
		t.Fatalf("restart button missing\nHTML:\n%s", html)
	}
}
