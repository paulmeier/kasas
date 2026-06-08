package dashboard

import (
	"strings"
	"testing"
)

// TestSourcesViewRendersCards checks a card per source with the right controls:
// a connected/disconnected pill, a credential input for credentialed sources, a
// Connect button for OAuth sources, and a per-source Sync control.
func TestSourcesViewRendersCards(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{
			{
				Type: "simplefin", Archetype: "pull", Title: "SimpleFIN",
				Connected: true, Credentialed: true,
				Credentials: []credentialField{{Key: "setup_token", Title: "Setup token", Help: "Paste your bridge token."}},
			},
			{Type: "csv", Archetype: "file", Title: "CSV files", Connected: false, OAuth: true},
		},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"Sources",
		"SimpleFIN",
		"CSV files",
		"Connected",
		"Not connected",
		"Sync now",
		`id="source-cred-simplefin"`, // credentialed source gets an input
		"Setup token",                // its credential field title
		"Connect CSV files",          // OAuth source gets a connect button
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("sources render missing %q\nHTML:\n%s", want, html)
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
