package dashboard

import (
	"strings"
	"testing"
)

// TestSourceDetailRendersCredentialForm checks an active, credentialed source's
// detail page shows its header (title + connected pill), a credential input with
// the field's title, the egress "Contacts" line, and a per-source Sync control.
func TestSourceDetailRendersCredentialForm(t *testing.T) {
	v := &sourceDetailView{
		loaded:  true,
		enabled: true,
		typ:     "simplefin",
		sources: []sourceStatus{{
			Type: "simplefin", Archetype: "pull", Title: "SimpleFIN", Active: true,
			Connected: true, Credentialed: true,
			Credentials: []credentialField{{Key: "setup_token", Title: "Setup token", Help: "Paste your bridge token."}},
			Egress:      []string{"bridge.simplefin.org"},
		}},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"SimpleFIN",
		"Connected",
		"Sync now",
		`id="source-cred-simplefin"`,
		"Setup token",
		"Paste your bridge token.",
		"Contacts bridge.simplefin.org", // egress line
		`href="/sources"`,               // back link
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("source detail missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSourceDetailOAuthConnect checks an OAuth source gets a browser "Connect"
// button.
func TestSourceDetailOAuthConnect(t *testing.T) {
	v := &sourceDetailView{
		loaded:  true,
		enabled: true,
		typ:     "csv",
		sources: []sourceStatus{{Type: "csv", Archetype: "file", Title: "CSV files", Active: true, OAuth: true}},
	}
	html := printUI(t, v.Render())
	if !strings.Contains(html, "Connect CSV files") {
		t.Fatalf("OAuth source detail should render a connect button\nHTML:\n%s", html)
	}
}

// TestSourceDetailMultiCredential checks a multi-credential source (Teller) lists
// each connected credential — masked, with a Remove button for removable ones and
// a "from config" note otherwise — plus an "add another" input.
func TestSourceDetailMultiCredential(t *testing.T) {
	v := &sourceDetailView{
		loaded:  true,
		enabled: true,
		typ:     "teller",
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
		"••••aabb",
		"••••ccdd",
		"Remove",
		"from config",
		`id="source-cred-teller"`,
		">Add<",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("multi-credential detail missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSourceDetailConfigAndInactive checks per-source configuration renders as
// editable rows (with override/restart chips and secret handling), and that an
// inactive source explains how to activate it while hiding the credential and
// sync controls that need a running source.
func TestSourceDetailConfigAndInactive(t *testing.T) {
	v := &sourceDetailView{
		loaded:  true,
		enabled: true,
		typ:     "plaid",
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
		"run a sync from this page",
		"Configuration",
		`id="setting-input-plaid.client_id"`,
		"overridden",
		"restart pending",
		"Reset",
		"paste to replace",
		"sandbox",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("source config detail missing %q\nHTML:\n%s", want, html)
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

// TestSourceDetailInactiveAddressHint checks an inactive address-watching source
// (Bitcoin/Ethereum) tells the user the watched-address form appears once active.
func TestSourceDetailInactiveAddressHint(t *testing.T) {
	v := &sourceDetailView{
		loaded:  true,
		enabled: true,
		typ:     "bitcoin",
		sources: []sourceStatus{{
			Type: "bitcoin", Archetype: "pull", Title: "Bitcoin", Active: false,
			Credentialed: true,
			Credentials:  []credentialField{{Key: "address", Title: "Bitcoin address"}},
			Config:       []settingItem{{Key: "bitcoin.api_url", Title: "API URL", Kind: "string", Source: "bitcoin"}},
		}},
	}
	html := printUI(t, v.Render())

	for _, want := range []string{
		"restart kasas to activate",
		"add its watched addresses",
		"run a sync from this page",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("inactive address detail missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSourceDetailUnknown checks navigating to an unrecognised source type shows
// a not-found note with a way back, rather than a blank page.
func TestSourceDetailUnknown(t *testing.T) {
	v := &sourceDetailView{loaded: true, enabled: true, typ: "nope", sources: []sourceStatus{
		{Type: "simplefin", Title: "SimpleFIN", Active: true},
	}}
	html := printUI(t, v.Render())
	if !strings.Contains(html, "Unknown source") {
		t.Fatalf("unknown source detail should explain the state\nHTML:\n%s", html)
	}
}
