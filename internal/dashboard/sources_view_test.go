package dashboard

import (
	"strings"
	"testing"
	"time"
)

// TestSourcesViewRendersRows checks the Sources list renders one clickable row
// per source — icon, title, blurb, and the four-state status pill — linking to
// the source's detail page, plus the "Sync all" control. Per-source controls
// (credentials, config, sync) live on the detail page, not the list.
func TestSourcesViewRendersRows(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{
			{Type: "simplefin", Archetype: "pull", Title: "SimpleFIN", Active: true, Connected: true, Credentialed: true},
			{Type: "csv", Archetype: "file", Title: "CSV files", Active: true, Connected: false, OAuth: true},
			{Type: "manual", Archetype: "manual", Title: "Manual", Active: true, Connected: false},
			{Type: "plaid", Archetype: "pull", Title: "Plaid", Active: false, Credentialed: true},
		},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"Sources",
		"Sync all",
		"SimpleFIN",
		"CSV files",
		`href="/sources/simplefin"`, // row links to the detail page
		`href="/sources/csv"`,
		"Bank aggregator",    // simplefin blurb
		"Manual file import", // csv blurb
		"Connected",          // active + connected
		"Needs credentials",  // active, not connected, credentialed/oauth (csv)
		">Active<",           // active, not connected, no creds (manual)
		"Inactive",           // not active (plaid)
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("sources list missing %q\nHTML:\n%s", want, html)
		}
	}

	// The list must NOT render any per-source editing controls — those moved to
	// the detail page.
	for _, bad := range []string{
		`id="source-cred-simplefin"`,
		"Sync now",
		"Configuration",
	} {
		if strings.Contains(html, bad) {
			t.Fatalf("sources list must not render per-source control %q\nHTML:\n%s", bad, html)
		}
	}
}

// TestSourcesViewRecentSyncs checks the "Recent syncs" panel lists runs with a
// status pill and (for a failure) its error.
func TestSourcesViewRecentSyncs(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{{Type: "simplefin", Archetype: "pull", Title: "SimpleFIN", Active: true, Connected: true}},
		history: []syncRun{
			{ID: 2, StartedAt: time.Date(2026, 6, 20, 10, 0, 0, 0, time.UTC), Status: "success"},
			{ID: 1, StartedAt: time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC), Status: "error", Error: "bridge unreachable"},
		},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"Recent syncs",
		"2026-06-20 10:00 UTC",
		">success<",
		">error<",
		"bridge unreachable",
		"status-pill failed", // the failed run gets the red pill
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("recent syncs missing %q\nHTML:\n%s", want, html)
		}
	}
}

func TestSourcesViewDisabled(t *testing.T) {
	v := &sourcesView{loaded: true, enabled: false}
	html := printUI(t, v.Render())
	if !strings.Contains(html, "unavailable") {
		t.Fatalf("disabled sources page should explain the state\nHTML:\n%s", html)
	}
}

func TestSourcesViewOAuthBanner(t *testing.T) {
	v := &sourcesView{loaded: true, enabled: true, oauthMsg: "Connected csv."}
	html := printUI(t, v.Render())
	if !strings.Contains(html, "Connected csv.") {
		t.Fatalf("expected the post-OAuth success banner\nHTML:\n%s", html)
	}
}
