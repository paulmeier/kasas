package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestPluginsListRenders checks a plugin row shows its identity, declared hooks,
// granted capabilities, the running status pill, and the reload action.
func TestPluginsListRenders(t *testing.T) {
	v := &pluginsView{plugins: []plugin{{
		ID: 1, Name: "budgeting", Runtime: "lua", Version: "0.1.0", Description: "Tag spending",
		Enabled: true, Loaded: true, OnDisk: true, State: "loaded",
		Hooks: []string{"OnTransactionCreate"}, Granted: []string{"labels:write"},
	}}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	html := buf.String()
	for _, want := range []string{"budgeting", "lua", "OnTransactionCreate", "labels:write", "Running", ">Reload<", "rules-table"} {
		if !strings.Contains(html, want) {
			t.Fatalf("plugins table missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestPluginsErrorState checks a failed plugin shows the error pill.
func TestPluginsErrorState(t *testing.T) {
	v := &pluginsView{plugins: []plugin{{
		ID: 1, Name: "broken", Runtime: "lua", OnDisk: true, State: "error", LastError: "boom",
	}}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	if html := buf.String(); !strings.Contains(html, ">Error<") {
		t.Fatalf("expected an error pill, got:\n%s", html)
	}
}

func TestPluginsEmptyState(t *testing.T) {
	v := &pluginsView{enabled: true} // enabled, no plugins, not loading
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	if html := buf.String(); !strings.Contains(html, "No plugins installed") {
		t.Fatalf("expected the empty state, got:\n%s", html)
	}
}

// TestPluginsDisabledState checks that when the plugin system is disabled the page
// says so (rather than showing an error or the "no plugins installed" hint).
func TestPluginsDisabledState(t *testing.T) {
	v := &pluginsView{} // enabled defaults to false: system disabled
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderList())
	if html := buf.String(); !strings.Contains(html, "plugin system is disabled") {
		t.Fatalf("expected the disabled state, got:\n%s", html)
	}
}
