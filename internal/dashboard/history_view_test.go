package dashboard

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/maxence-charriere/go-app/v10/pkg/app"
)

func TestHistoryModalRendersTimeline(t *testing.T) {
	h := &historyViewing{
		historyOpen:  true,
		historyTitle: "Cafe",
		historyVersions: []version{
			{
				Version:     1,
				ChangeKind:  "imported",
				OccurredAt:  time.Unix(1700000000, 0),
				Transaction: json.RawMessage(`{"payee":"Cafe","amount":"-10.00"}`),
				Diff: versionDiff{
					Fields:      []fieldChange{{Field: "amount", From: "", To: "-10.00"}},
					LabelsAdded: map[string]string{},
				},
			},
			{
				Version:     2,
				ChangeKind:  "labeled",
				OccurredAt:  time.Unix(1700001000, 0),
				Transaction: json.RawMessage(`{"payee":"Cafe","labels":{"category":"coffee"}}`),
				Diff:        versionDiff{LabelsAdded: map[string]string{"category": "coffee"}},
			},
		},
	}

	var buf bytes.Buffer
	app.PrintHTML(&buf, h.renderHistoryModal())
	html := buf.String()

	for _, want := range []string{"History", "Cafe", "v1", "v2", "imported", "labeled", "amount", "category", "coffee", "snapshot"} {
		if !strings.Contains(html, want) {
			t.Errorf("history modal HTML missing %q\n%s", want, html)
		}
	}
}

func TestHistoryModalHiddenWhenClosed(t *testing.T) {
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&historyViewing{}).renderHistoryModal())
	if html := strings.TrimSpace(buf.String()); html != "" {
		t.Errorf("a closed history modal should render nothing, got %q", html)
	}
}

func TestHistoryButtonRenders(t *testing.T) {
	// The per-row clock button is always in the DOM (revealed via CSS on hover); it
	// renders without the modal being open.
	var buf bytes.Buffer
	app.PrintHTML(&buf, (&historyViewing{}).renderHistoryButton(transaction{ID: "tx-1"}))
	if html := buf.String(); !strings.Contains(html, "history-btn") {
		t.Errorf("expected a history-btn control, got %q", html)
	}
}
