package provenance

import (
	"testing"
	"time"

	"github.com/paulmeier/kasas/internal/events"
)

func ts(sec int64) time.Time { return time.Unix(sec, 0).UTC() }

func snap(amount string, labels map[string]string, ext map[string]any) events.TransactionPayload {
	if labels == nil {
		labels = map[string]string{}
	}
	if ext == nil {
		ext = map[string]any{}
	}
	return events.TransactionPayload{ID: "abc", AccountID: "acct-1", Amount: amount, Labels: labels, Extensions: ext}
}

// TestBuildIdentityAndTiming covers the origin fields and the imported_at rule:
// it is the earliest version's timestamp, last_seen is passed through, and the
// initial import reads "imported from <source>".
func TestBuildIdentityAndTiming(t *testing.T) {
	p := Build(Input{
		TransactionID: "abc",
		Source:        "simplefin",
		AccountID:     "acct-1",
		Institution:   "Chase",
		LastSeen:      ts(2000),
		Versions: []Version{
			{Kind: events.ChangeImported, OccurredAt: ts(1000), Snapshot: snap("-5.00", nil, nil)},
		},
	})

	if p.Source != "simplefin" {
		t.Errorf("source = %q, want simplefin", p.Source)
	}
	if p.SourceTransactionID != "abc" {
		t.Errorf("source_transaction_id = %q, want abc", p.SourceTransactionID)
	}
	if p.AccountID != "acct-1" || p.Institution != "Chase" {
		t.Errorf("account/institution = %q/%q", p.AccountID, p.Institution)
	}
	if !p.ImportedAt.Equal(ts(1000)) {
		t.Errorf("imported_at = %v, want %v", p.ImportedAt, ts(1000))
	}
	if !p.LastSeen.Equal(ts(2000)) {
		t.Errorf("last_seen = %v, want %v", p.LastSeen, ts(2000))
	}
	if len(p.Transformations) != 1 || p.Transformations[0].Summary != "imported from simplefin" {
		t.Fatalf("transformations = %+v", p.Transformations)
	}
}

// TestBuildTransformationSummaries exercises every summary branch: imported,
// synced (a scalar field), labeled (a label add), and extended (an extension add),
// in order, and confirms they are deterministic.
func TestBuildTransformationSummaries(t *testing.T) {
	v1 := snap("-10.00", nil, nil)
	v2 := snap("-12.00", nil, nil)                                   // bridge corrected the amount
	v3 := snap("-12.00", map[string]string{"category": "food"}, nil) // a label applied
	v4 := snap("-12.00", map[string]string{"category": "food"},      // an extension set
		map[string]any{"tax.bucket": "meal"})

	p := Build(Input{
		TransactionID: "abc", Source: "simplefin", AccountID: "acct-1", LastSeen: ts(4000),
		Versions: []Version{
			{Kind: events.ChangeImported, OccurredAt: ts(1000), Snapshot: v1},
			{Kind: events.ChangeSynced, OccurredAt: ts(2000), Snapshot: v2},
			{Kind: events.ChangeLabeled, OccurredAt: ts(3000), Snapshot: v3},
			{Kind: events.ChangeExtended, OccurredAt: ts(4000), Snapshot: v4},
		},
	})

	want := []struct{ kind, summary string }{
		{events.ChangeImported, "imported from simplefin"},
		{events.ChangeSynced, "amount -10.00 → -12.00"},
		{events.ChangeLabeled, "+category:food"},
		{events.ChangeExtended, "+ext:tax.bucket"},
	}
	if len(p.Transformations) != len(want) {
		t.Fatalf("got %d transformations, want %d", len(p.Transformations), len(want))
	}
	for i, w := range want {
		got := p.Transformations[i]
		if got.Kind != w.kind || got.Summary != w.summary {
			t.Errorf("transformation %d = {%q, %q}, want {%q, %q}", i, got.Kind, got.Summary, w.kind, w.summary)
		}
	}
	if !p.ImportedAt.Equal(ts(1000)) {
		t.Errorf("imported_at = %v, want earliest version %v", p.ImportedAt, ts(1000))
	}
}

// TestBuildNoVersionsFallsBackToLastSeen covers a transaction that predates history
// and has not changed since: no versions, so imported_at falls back to last_seen and
// the transformations list is empty (never nil).
func TestBuildNoVersionsFallsBackToLastSeen(t *testing.T) {
	p := Build(Input{TransactionID: "abc", Source: "simplefin", LastSeen: ts(5000)})

	if !p.ImportedAt.Equal(ts(5000)) {
		t.Errorf("imported_at = %v, want last_seen fallback %v", p.ImportedAt, ts(5000))
	}
	if p.Transformations == nil || len(p.Transformations) != 0 {
		t.Errorf("transformations = %+v, want empty non-nil slice", p.Transformations)
	}
}

// TestBuildNonSimpleFINSource confirms source is passed through verbatim (a future
// ingestion path is not hardcoded to "simplefin").
func TestBuildNonSimpleFINSource(t *testing.T) {
	p := Build(Input{
		TransactionID: "x", Source: "plaid", LastSeen: ts(1),
		Versions: []Version{
			{Kind: events.ChangeImported, OccurredAt: ts(1), Snapshot: snap("1.00", nil, nil)},
		},
	})

	if p.Source != "plaid" {
		t.Errorf("source = %q, want plaid", p.Source)
	}
	if p.Transformations[0].Summary != "imported from plaid" {
		t.Errorf("summary = %q, want imported from plaid", p.Transformations[0].Summary)
	}
}
