package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

func renderSidebarHTML(t *testing.T, c *chrome, active navItem) string {
	t.Helper()
	var buf bytes.Buffer
	app.PrintHTML(&buf, c.renderSidebar(active))
	return buf.String()
}

// TestSidebarRendersNav checks the sidebar shows all three destinations with the
// client-side route links go-app needs to navigate without a full reload.
func TestSidebarRendersNav(t *testing.T) {
	html := renderSidebarHTML(t, &chrome{}, navDashboard)
	for _, label := range []string{"Dashboard", "Tags", "Rules"} {
		if !strings.Contains(html, ">"+label+"<") {
			t.Fatalf("sidebar missing nav label %q\nHTML:\n%s", label, html)
		}
	}
	for _, href := range []string{`href="/"`, `href="/tags"`, `href="/rules"`} {
		if !strings.Contains(html, href) {
			t.Fatalf("sidebar missing %s\nHTML:\n%s", href, html)
		}
	}
}

// TestSidebarActiveItem checks exactly one item is marked active and that it
// tracks the requested page.
func TestSidebarActiveItem(t *testing.T) {
	dash := renderSidebarHTML(t, &chrome{}, navDashboard)
	tags := renderSidebarHTML(t, &chrome{}, navTags)

	if got := strings.Count(dash, "nav-item active"); got != 1 {
		t.Fatalf("Dashboard render: %d active items, want 1\nHTML:\n%s", got, dash)
	}
	if got := strings.Count(tags, "nav-item active"); got != 1 {
		t.Fatalf("Tags render: %d active items, want 1\nHTML:\n%s", got, tags)
	}
	if dash == tags {
		t.Fatal("active item did not move between Dashboard and Tags")
	}
}

// TestSidebarCollapsedClass checks the collapsed state toggles the class that CSS
// uses to shrink the sidebar to the icon rail.
func TestSidebarCollapsedClass(t *testing.T) {
	if html := renderSidebarHTML(t, &chrome{}, navDashboard); strings.Contains(html, "sidebar collapsed") {
		t.Fatalf("expanded sidebar must not carry the collapsed class\nHTML:\n%s", html)
	}
	if html := renderSidebarHTML(t, &chrome{collapsed: true}, navDashboard); !strings.Contains(html, "sidebar collapsed") {
		t.Fatalf("collapsed sidebar must carry the collapsed class\nHTML:\n%s", html)
	}
}
