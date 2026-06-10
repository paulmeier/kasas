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
				Type: "simplefin", Archetype: "pull", Title: "SimpleFIN", Active: true,
				Connected: true, Credentialed: true,
				Credentials: []credentialField{{Key: "setup_token", Title: "Setup token", Help: "Paste your bridge token."}},
			},
			{Type: "csv", Archetype: "file", Title: "CSV files", Active: true, Connected: false, OAuth: true},
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

// TestSourcesViewRendersMultiCredential checks a multi-credential source (Teller)
// lists each connected credential — masked, with a Remove button for removable ones
// and a "from config" note otherwise — plus an "add another" input.
func TestSourcesViewRendersMultiCredential(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{{
			Type: "teller", Archetype: "pull", Title: "Teller", Active: true,
			Connected: true, Credentialed: true, MultiCredential: true,
			Credentials: []credentialField{{Key: "access_token", Title: "Access token", Help: "One per bank."}},
			CredentialEntries: []credentialEntry{
				{ID: "id_runtime", Label: "••••aabb", Removable: true},
				{ID: "id_config", Label: "••••ccdd", Removable: false},
			},
		}},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"Teller",
		"••••aabb",                // a connected credential, masked
		"••••ccdd",                // the config one, masked
		"Remove",                  // removable entry gets a remove button
		"from config",             // non-removable entry is labelled
		`id="source-cred-teller"`, // the add-another input
		"Add",                     // the add button
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("multi-credential render missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSourcesViewRendersConfigAndInactive checks per-source configuration
// renders as editable rows (with override/restart chips and secret handling),
// and that an inactive source explains how to activate it while hiding the
// credential and sync controls that need a running source.
func TestSourcesViewRendersConfigAndInactive(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{{
			Type: "plaid", Archetype: "pull", Title: "Plaid", Active: false,
			Credentialed: true,
			Credentials:  []credentialField{{Key: "access_token", Title: "Access token"}},
			Config: []settingItem{
				{Key: "plaid.client_id", Title: "Client ID", Kind: "string", Source: "plaid", Value: "abc", Overridden: true, RestartRequired: true},
				{Key: "plaid.secret", Title: "Secret", Kind: "string", Source: "plaid", Secret: true, Set: true},
				{Key: "plaid.environment", Title: "Environment", Kind: "string", Source: "plaid", Value: "sandbox", Enum: []string{"sandbox", "development", "production"}},
			},
		}},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"Plaid",
		"Inactive",
		"restart kasas to activate",
		"run a sync from this card", // points at the next step once active
		"Configuration",
		`id="setting-input-plaid.client_id"`, // editable config input
		"overridden",                         // override chip
		"restart pending",                    // restart chip
		"Reset",                              // reset control for the override
		"paste to replace",                   // set secret placeholder
		"sandbox",                            // enum rendered as select options
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("source config render missing %q\nHTML:\n%s", want, html)
		}
	}

	for _, bad := range []string{
		`id="source-cred-plaid"`, // credential input needs an active source
		"Sync now",               // so does the sync control
	} {
		if strings.Contains(html, bad) {
			t.Fatalf("inactive source must not render %q\nHTML:\n%s", bad, html)
		}
	}
}

// TestSourcesViewInactiveAddressSourceHint checks that an inactive
// address-watching source (Bitcoin/Ethereum) tells the user the watched-address
// form appears here once it is activated, while still hiding the live controls.
func TestSourcesViewInactiveAddressSourceHint(t *testing.T) {
	v := &sourcesView{
		loaded:  true,
		enabled: true,
		sources: []sourceStatus{{
			Type: "bitcoin", Archetype: "pull", Title: "Bitcoin", Active: false,
			Credentialed: true,
			Credentials:  []credentialField{{Key: "address", Title: "Bitcoin address"}},
			Config: []settingItem{
				{Key: "bitcoin.api_url", Title: "API URL", Kind: "string", Source: "bitcoin"},
			},
		}},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"restart kasas to activate",
		"add its watched addresses", // address-specific phrasing
		"run a sync from this card",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("inactive address source render missing %q\nHTML:\n%s", want, html)
		}
	}
	for _, bad := range []string{
		`id="source-cred-bitcoin"`, // address input needs an active source
		"Sync now",                 // so does the sync control
	} {
		if strings.Contains(html, bad) {
			t.Fatalf("inactive source must not render %q\nHTML:\n%s", bad, html)
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
