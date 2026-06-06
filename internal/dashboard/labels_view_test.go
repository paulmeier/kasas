package dashboard

import (
	"bytes"
	"strings"
	"testing"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

// TestLabelsViewRendersRowsAndCounts checks each label (as "key: value") and its
// transaction count appear in the table.
func TestLabelsViewRendersRowsAndCounts(t *testing.T) {
	v := &labelsView{labels: []labelCount{
		{Key: "tag", Value: "coffee", Count: 3},
		{Key: "category", Value: "rent", Count: 1},
	}}
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderContent())
	html := buf.String()

	for _, want := range []string{"tag: coffee", "category: rent", ">3<", ">1<"} {
		if !strings.Contains(html, want) {
			t.Fatalf("labels table missing %q\nHTML:\n%s", want, html)
		}
	}
}

// TestLabelsViewEmptyState checks the empty (not-loading, no-labels) state guides
// the user to the Dashboard rather than showing a bare table.
func TestLabelsViewEmptyState(t *testing.T) {
	v := &labelsView{} // no labels, not loading
	var buf bytes.Buffer
	app.PrintHTML(&buf, v.renderContent())
	if html := buf.String(); !strings.Contains(html, "No labels yet") {
		t.Fatalf("expected the empty state, got:\n%s", html)
	}
}
