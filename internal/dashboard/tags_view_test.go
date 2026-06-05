package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestTagsViewRendersRowsAndCounts checks each tag and its transaction count
// appear in the table.
func TestTagsViewRendersRowsAndCounts(t *testing.T) {
	v := &tagsView{tags: []tagCount{
		{Name: "coffee", Count: 3},
		{Name: "rent", Count: 1},
	}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderContent())
	html := buf.String()

	for _, want := range []string{"coffee", "rent", ">3<", ">1<"} {
		if !strings.Contains(html, want) {
			t.Fatalf("tags table missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestTagsViewEmptyState checks the empty (not-loading, no-tags) state guides the
// user to the Dashboard rather than showing a bare table.
func TestTagsViewEmptyState(t *testing.T) {
	v := &tagsView{} // no tags, not loading
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderContent())
	if html := buf.String(); !strings.Contains(html, "No tags yet") {
		t.Fatalf("expected the empty state, got:\n%s", html)
	}
}
