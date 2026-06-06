package dashboard

import (
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// marker is a sentinel page-content node used to assert whether renderShell
// rendered the page body (vs. a gate screen).
func marker() app.UI { return app.Span().Text("MARKER") }

// settingsWithSecurity builds a settingsView whose config has loaded with the
// given security state.
func settingsWithSecurity(sec securityConfig) *settingsView {
	cfg := sampleConfig()
	cfg.Security = sec
	return &settingsView{cfgLoaded: true, cfg: cfg}
}

// TestSecurityPanelUnsecured: with no token, the panel offers Generate + a custom
// input, shows "Unsecured", and hides Revoke.
func TestSecurityPanelUnsecured(t *testing.T) {
	v := settingsWithSecurity(securityConfig{AuthRequired: false, TokenSource: "none"})
	html := printUI(t, v.renderSecurity())

	for _, want := range []string{"Dashboard security", "Unsecured", "Generate token", `id="kasas-security-token-input"`} {
		if !strings.Contains(html, want) {
			t.Fatalf("unsecured security panel missing %q\nHTML:\n%s", want, html)
		}
	}
	if strings.Contains(html, "Revoke") {
		t.Fatalf("unsecured panel must not offer Revoke\nHTML:\n%s", html)
	}
}

// TestSecurityPanelStored: a stored token shows "Secured" and offers Revoke.
func TestSecurityPanelStored(t *testing.T) {
	v := settingsWithSecurity(securityConfig{AuthRequired: true, TokenSource: "stored"})
	html := printUI(t, v.renderSecurity())

	for _, want := range []string{"Secured", "Generate token", "Revoke"} {
		if !strings.Contains(html, want) {
			t.Fatalf("stored security panel missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestSecurityPanelConfigManaged: a config-managed token shows the note and hides
// the management controls.
func TestSecurityPanelConfigManaged(t *testing.T) {
	v := settingsWithSecurity(securityConfig{AuthRequired: true, TokenSource: "config"})
	html := printUI(t, v.renderSecurity())

	if !strings.Contains(html, "Secured") {
		t.Fatalf("config-managed panel must show Secured\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "via configuration") {
		t.Fatalf("config-managed panel must explain it is config-managed\nHTML:\n%s", html)
	}
	for _, unwanted := range []string{"Generate token", "Revoke", `id="kasas-security-token-input"`} {
		if strings.Contains(html, unwanted) {
			t.Fatalf("config-managed panel must not offer %q\nHTML:\n%s", unwanted, html)
		}
	}
}

// TestRenderTokenReveal: a freshly minted token is rendered once with a Copy
// button.
func TestRenderTokenReveal(t *testing.T) {
	v := settingsWithSecurity(securityConfig{AuthRequired: true, TokenSource: "stored"})
	v.newToken = "freshly-minted-token-xyz"
	html := printUI(t, v.renderSecurity())

	if !strings.Contains(html, "freshly-minted-token-xyz") {
		t.Fatalf("token reveal must show the new token\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "Copy") {
		t.Fatalf("token reveal must offer Copy\nHTML:\n%s", html)
	}
}

// TestRenderShellLoginGate: when a token is required but ours is not accepted,
// renderShell shows the login screen instead of the app shell.
func TestRenderShellLoginGate(t *testing.T) {
	c := &chrome{authChecked: true, authRequired: true, authed: false}
	html := printUI(t, c.renderShell(navDashboard, marker()))

	if !strings.Contains(html, `id="kasas-login-token"`) {
		t.Fatalf("login gate must render the token input\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "Unlock") {
		t.Fatalf("login gate must render the Unlock button\nHTML:\n%s", html)
	}
	if strings.Contains(html, "nav-item") {
		t.Fatalf("login gate must not render the sidebar\nHTML:\n%s", html)
	}
	if strings.Contains(html, "MARKER") {
		t.Fatalf("login gate must not render page content\nHTML:\n%s", html)
	}
}

// TestRenderShellAuthedShowsContent: an authenticated session renders the normal
// shell with the page content and no unsecured banner.
func TestRenderShellAuthedShowsContent(t *testing.T) {
	c := &chrome{authChecked: true, authRequired: true, authed: true}
	html := printUI(t, c.renderShell(navDashboard, marker()))

	if !strings.Contains(html, "MARKER") {
		t.Fatalf("authed shell must render page content\nHTML:\n%s", html)
	}
	if strings.Contains(html, "not secured") {
		t.Fatalf("authed shell must not show the unsecured banner\nHTML:\n%s", html)
	}
}

// TestRenderShellUnsecuredBanner: when no token is required, the shell prepends
// the unsecured warning to every page.
func TestRenderShellUnsecuredBanner(t *testing.T) {
	c := &chrome{authChecked: true, authRequired: false, authed: true}
	html := printUI(t, c.renderShell(navDashboard, marker()))

	if !strings.Contains(html, "not secured") {
		t.Fatalf("unsecured shell must show the warning banner\nHTML:\n%s", html)
	}
	if !strings.Contains(html, "MARKER") {
		t.Fatalf("unsecured shell must still render page content\nHTML:\n%s", html)
	}
}
