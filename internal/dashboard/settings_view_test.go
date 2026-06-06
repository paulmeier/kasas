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

// TestSettingsViewRenders checks the page renders both panels: the SimpleFIN
// connection form (status, credential input, sync button) and the read-only
// configuration cards.
func TestSettingsViewRenders(t *testing.T) {
	v := &settingsView{cfgLoaded: true, connected: true, cfg: sampleConfig()}
	html := printUI(t, v.Render())

	wantContains := []string{
		"SimpleFIN connection",
		"Connected",
		`id="simplefin-token-input"`, // credential input
		"Sync now",
		"Configuration",   // read-only section heading
		"/data/kasas.db",  // a config value rendered
		"paulmeier/kasas", // another config value rendered
	}
	for _, want := range wantContains {
		if !strings.Contains(html, want) {
			t.Fatalf("settings render missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSettingsViewDisconnected checks the status pill reflects a missing credential.
func TestSettingsViewDisconnected(t *testing.T) {
	cfg := sampleConfig()
	cfg.SimpleFIN.Connected = false
	v := &settingsView{cfgLoaded: true, connected: false, cfg: cfg}
	html := printUI(t, v.Render())

	if !strings.Contains(html, "Not connected") {
		t.Fatalf("disconnected settings must show 'Not connected'\nHTML:\n%s", html)
	}
}
