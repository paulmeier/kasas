package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

func renderBlocksHTML(t *testing.T, v *pluginPageView) string {
	t.Helper()
	var buf bytes.Buffer
	app.PrintHTML(&buf, app.Div().Body(v.renderBlocks()...))
	return buf.String()
}

// TestPluginPageRendersBlocks checks every block type renders its content
// through text nodes (the declarative page contract).
func TestPluginPageRendersBlocks(t *testing.T) {
	v := &pluginPageView{name: "pager", loaded: true, doc: pageDoc{
		Title: "Pager",
		Blocks: []pageBlock{
			{Type: "heading", Text: "Overview"},
			{Type: "text", Text: "Some explanation"},
			{Type: "stat", Label: "Tagged", Value: "42", Hint: "this month"},
			{Type: "keyvalue", Items: []pageKV{{Key: "Keyword", Value: "coffee"}}},
			{Type: "table", Columns: []string{"Date", "Payee"}, Rows: [][]string{{"2026-06-01", "Cafe"}}},
			{Type: "actions", Actions: []pageAction{{ID: "rescan", Label: "Re-scan", Style: "primary"}}},
			{Type: "divider"},
		},
	}}
	html := renderBlocksHTML(t, v)
	for _, want := range []string{
		">Overview<", ">Some explanation<", ">Tagged<", ">42<", ">this month<",
		">Keyword<", ">coffee<", ">Date<", ">Payee<", ">2026-06-01<", ">Cafe<",
		">Re-scan<", "btn-primary", "ext-divider",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("plugin page missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestPluginPageContentIsEscaped checks markup in plugin-provided strings is
// escaped, never injected (the no-XSS guarantee of the declarative schema).
func TestPluginPageContentIsEscaped(t *testing.T) {
	v := &pluginPageView{name: "pager", loaded: true, doc: pageDoc{
		Blocks: []pageBlock{
			{Type: "text", Text: `<script>alert(1)</script>`},
			{Type: "stat", Label: `<img src=x>`, Value: `<b>9</b>`},
		},
	}}
	html := renderBlocksHTML(t, v)
	for _, banned := range []string{"<script>", "<img", "<b>9</b>"} {
		if strings.Contains(html, banned) {
			t.Fatalf("plugin-provided markup leaked unescaped: %q\nHTML:\n%s", banned, html)
		}
	}
}

// TestPluginPageShortRowsPad checks a row narrower than the column set renders
// empty trailing cells rather than panicking or skewing columns.
func TestPluginPageShortRowsPad(t *testing.T) {
	v := &pluginPageView{name: "pager", loaded: true, doc: pageDoc{
		Blocks: []pageBlock{
			{Type: "table", Columns: []string{"A", "B"}, Rows: [][]string{{"only-a"}}},
		},
	}}
	html := renderBlocksHTML(t, v)
	if !strings.Contains(html, ">only-a<") {
		t.Fatalf("table missing cell content\nHTML:\n%s", html)
	}
	if got := strings.Count(html, "<td"); got != 2 {
		t.Fatalf("expected 2 cells (short row padded), got %d\nHTML:\n%s", got, html)
	}
}

// TestSidebarRendersPluginPages checks plugin-contributed entries render after
// the built-ins with their route, title, and active state.
func TestSidebarRendersPluginPages(t *testing.T) {
	c := &chrome{
		extPages:  []pluginPage{{Name: "coffee-budget", Title: "Coffee Budget", Icon: "chart"}},
		activeExt: "coffee-budget",
	}
	html := renderSidebarHTML(t, c, navExtension)
	if !strings.Contains(html, `href="/ext/coffee-budget"`) {
		t.Fatalf("sidebar missing the plugin page link\nHTML:\n%s", html)
	}
	if !strings.Contains(html, ">Coffee Budget<") {
		t.Fatalf("sidebar missing the plugin page title\nHTML:\n%s", html)
	}
	if got := strings.Count(html, "nav-item active"); got != 1 {
		t.Fatalf("expected exactly one active item, got %d\nHTML:\n%s", got, html)
	}
}

// TestSidebarPluginPageNotActiveElsewhere checks a plugin entry is not marked
// active when a built-in page is the current one.
func TestSidebarPluginPageNotActiveElsewhere(t *testing.T) {
	c := &chrome{extPages: []pluginPage{{Name: "pager", Title: "Pager", Icon: "list"}}}
	html := renderSidebarHTML(t, c, navTransactions)
	if got := strings.Count(html, "nav-item active"); got != 1 {
		t.Fatalf("expected exactly one active item, got %d\nHTML:\n%s", got, html)
	}
}

// TestExtIconFallsBack checks unknown icon names fall back to the puzzle glyph
// instead of rendering nothing (or trusting plugin input).
func TestExtIconFallsBack(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, app.Div().Body(extIcon("definitely-not-an-icon")))
	if !strings.Contains(buf.String(), "nav-icon") {
		t.Fatalf("fallback icon missing\nHTML:\n%s", buf.String())
	}
}

// TestMarketplacePageBadge checks a catalog row badges plugins that add a
// dashboard page, and only those.
func TestMarketplacePageBadge(t *testing.T) {
	v := &marketplaceView{available: true}
	withPage := registryPlugin{Name: "pager", Runtime: "lua",
		UI: &pluginPage{Title: "Pager", Icon: "chart"}}
	withoutPage := registryPlugin{Name: "plain", Runtime: "lua"}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderRow(withPage))
	if html := buf.String(); !strings.Contains(html, ">dashboard page<") {
		t.Fatalf("expected the dashboard-page badge\nHTML:\n%s", html)
	}

	buf.Reset()
	app.PrintHTML(&buf, v.renderRow(withoutPage))
	if html := buf.String(); strings.Contains(html, ">dashboard page<") {
		t.Fatalf("plain plugin must not carry the badge\nHTML:\n%s", html)
	}
}

// TestTrustTierOfDefaults checks an absent tier (an older index predating ADR
// 0003) is treated as verified, and known tiers pass through.
func TestTrustTierOfDefaults(t *testing.T) {
	if got := trustTierOf(registryPlugin{Tier: ""}); got != tierVerified {
		t.Fatalf("absent tier should default to verified, got %q", got)
	}
	if got := trustTierOf(registryPlugin{Tier: "connected"}); got != tierConnected {
		t.Fatalf("connected tier should pass through, got %q", got)
	}
	if got := trustTierOf(registryPlugin{Tier: "bogus"}); got != tierVerified {
		t.Fatalf("an unknown tier should default to verified, got %q", got)
	}
}

// TestMarketplaceTierGrouping checks the catalog renders one labelled section per
// trust tier (ADR 0003), with the escalating badges and the per-tier risk note.
func TestMarketplaceTierGrouping(t *testing.T) {
	v := &marketplaceView{available: true, plugins: []registryPlugin{
		{Name: "labeler", Runtime: "lua", Tier: "verified", Capabilities: []string{"labels:write"}},
		{Name: "enricher", Runtime: "lua", Tier: "connected", Capabilities: []string{"transactions:read", "net:fetch"},
			Net: &registryNet{Allow: []string{"api.merchant.example.com"}}},
	}}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderTierGroups())
	html := buf.String()
	for _, want := range []string{">verified<", ">connected<", "cannot reach the network", "Can reach the network"} {
		if !strings.Contains(html, want) {
			t.Fatalf("expected %q in the grouped catalog\nHTML:\n%s", want, html)
		}
	}
}

// TestMarketplaceConnectedHostsShown checks a Connected plugin surfaces its exact
// egress hosts in its row, and a Verified plugin shows none.
func TestMarketplaceConnectedHostsShown(t *testing.T) {
	v := &marketplaceView{available: true}
	connected := registryPlugin{Name: "enricher", Runtime: "lua", Tier: "connected",
		Capabilities: []string{"net:fetch"}, Net: &registryNet{Allow: []string{"paperless.lan"}}}
	verified := registryPlugin{Name: "labeler", Runtime: "lua", Tier: "verified",
		Capabilities: []string{"labels:write"}}

	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderRow(connected))
	if html := buf.String(); !strings.Contains(html, "paperless.lan") || !strings.Contains(html, "net-host") {
		t.Fatalf("expected the connected plugin's egress host badge\nHTML:\n%s", html)
	}

	buf.Reset()
	app.PrintHTML(&buf, v.renderRow(verified))
	if html := buf.String(); strings.Contains(html, "net-host") {
		t.Fatalf("a verified plugin must show no egress hosts\nHTML:\n%s", html)
	}
}
