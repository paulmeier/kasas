package rules_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/paulmeier/kasas/internal/rules"
	"github.com/paulmeier/kasas/internal/search"
)

// compile is a test helper: build a labels rule from primitives and compile it,
// failing the test on an unexpected parse error.
func compile(t *testing.T, id int64, query string, labels map[string]string) rules.Compiled {
	t.Helper()
	return compileFull(t, id, query, labels, nil)
}

// compileFull builds and compiles a rule that may apply labels and/or extensions.
func compileFull(t *testing.T, id int64, query string, labels map[string]string, ext map[string]json.RawMessage) rules.Compiled {
	t.Helper()
	r := rules.Rule{ID: id, Query: query, Labels: labels, Extensions: ext, Enabled: true}
	c, err := rules.Compile(r)
	if err != nil {
		t.Fatalf("compile %q: %v", query, err)
	}
	return c
}

// ext builds an extensions map from alternating key/raw-JSON-value strings.
func ext(pairs ...string) map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	for i := 0; i+1 < len(pairs); i += 2 {
		m[pairs[i]] = json.RawMessage(pairs[i+1])
	}
	return m
}

func rec(amount float64, payee string, lbls map[string]string) search.Record {
	return search.Record{Amount: amount, Payee: payee, AccountName: "Checking", Labels: lbls}
}

func TestCompileInvalidQuery(t *testing.T) {
	if _, err := rules.Compile(rules.Rule{Query: "amount:>"}); err == nil {
		t.Fatal("expected a parse error for an invalid query")
	}
}

func TestNewRuleDecodesLabelsAndExtensions(t *testing.T) {
	r := rules.NewRule(7, "Coffee", "payee:starbucks", `{"category":"coffee"}`, `{"tax.category":"meal","forecast.recurring":true}`, true)
	if r.ID != 7 || r.Name != "Coffee" || !r.Enabled {
		t.Fatalf("unexpected rule fields: %+v", r)
	}
	if !reflect.DeepEqual(r.Labels, map[string]string{"category": "coffee"}) {
		t.Fatalf("labels = %v", r.Labels)
	}
	wantExt := map[string]json.RawMessage{"tax.category": json.RawMessage(`"meal"`), "forecast.recurring": json.RawMessage(`true`)}
	if !reflect.DeepEqual(r.Extensions, wantExt) {
		t.Fatalf("extensions = %v, want %v", r.Extensions, wantExt)
	}
}

func TestApplyMatchMerges(t *testing.T) {
	c := compile(t, 1, "amount:>50", map[string]string{"status": "review"})
	got, changed := rules.Apply([]rules.Compiled{c}, rec(75, "acme", nil), map[string]string{})
	if !changed {
		t.Fatal("expected changed=true")
	}
	if !reflect.DeepEqual(got, map[string]string{"status": "review"}) {
		t.Fatalf("got %v", got)
	}
}

func TestApplyNoMatchUnchanged(t *testing.T) {
	c := compile(t, 1, "amount:>50", map[string]string{"status": "review"})
	current := map[string]string{"category": "food"}
	got, changed := rules.Apply([]rules.Compiled{c}, rec(10, "acme", current), current)
	if changed {
		t.Fatalf("expected changed=false, got %v", got)
	}
	// Returns the original map (callers skip the write).
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("got %v, want %v", got, current)
	}
}

func TestApplyOverwritesConflictingValue(t *testing.T) {
	c := compile(t, 1, "amount:>0", map[string]string{"status": "review"})
	current := map[string]string{"status": "done", "category": "food"}
	got, changed := rules.Apply([]rules.Compiled{c}, rec(5, "acme", current), current)
	if !changed {
		t.Fatal("expected changed=true (value overwritten)")
	}
	want := map[string]string{"status": "review", "category": "food"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestApplyLaterRuleWins(t *testing.T) {
	// Two matching rules set the same key; the later (higher-id, passed last) wins.
	c1 := compile(t, 1, "amount:>0", map[string]string{"tier": "low"})
	c2 := compile(t, 2, "amount:>0", map[string]string{"tier": "high"})
	got, changed := rules.Apply([]rules.Compiled{c1, c2}, rec(5, "acme", nil), nil)
	if !changed || got["tier"] != "high" {
		t.Fatalf("got %v, changed=%v; want tier=high", got, changed)
	}
}

func TestApplyMatchesOnAccountField(t *testing.T) {
	// Confirms the search engine is wired through Compiled.Matches end to end
	// (account: matching is case-insensitive).
	c := compile(t, 1, "account:checking", map[string]string{"acct": "chk"})
	got, changed := rules.Apply([]rules.Compiled{c}, rec(1, "acme", nil), nil)
	if !changed || got["acct"] != "chk" {
		t.Fatalf("got %v, changed=%v", got, changed)
	}
}

func TestApplyEnforcesCount(t *testing.T) {
	// A transaction already at the cap plus a rule adding one more key stays at
	// the cap (normalization drops deterministically).
	current := make(map[string]string, 50)
	for i := 0; i < 50; i++ {
		current[string(rune('a'+i/26))+string(rune('a'+i%26))] = "v"
	}
	c := compile(t, 1, "amount:>0", map[string]string{"zzz_new": "x"})
	got, _ := rules.Apply([]rules.Compiled{c}, rec(5, "acme", current), current)
	if len(got) > 50 {
		t.Fatalf("label count = %d, want capped at 50", len(got))
	}
}

func TestApplyEmptyRuleSetUnchanged(t *testing.T) {
	got, changed := rules.Apply(nil, rec(5, "acme", nil), nil)
	if changed || len(got) != 0 {
		t.Fatalf("got %v, changed=%v; want no change", got, changed)
	}
}

func TestApplyExtensionsMatchMerges(t *testing.T) {
	c := compileFull(t, 1, "amount:>50", nil, ext("tax.category", `"meal"`, "x.score", `88`))
	got, changed := rules.ApplyExtensions([]rules.Compiled{c}, rec(75, "acme", nil), map[string]json.RawMessage{})
	if !changed {
		t.Fatal("expected changed=true")
	}
	want := ext("tax.category", `"meal"`, "x.score", `88`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestApplyExtensionsNoMatchUnchanged(t *testing.T) {
	c := compileFull(t, 1, "amount:>50", nil, ext("tax.category", `"meal"`))
	current := ext("forecast.recurring", `true`)
	got, changed := rules.ApplyExtensions([]rules.Compiled{c}, rec(10, "acme", nil), current)
	if changed {
		t.Fatalf("expected changed=false, got %v", got)
	}
	if !reflect.DeepEqual(got, current) {
		t.Fatalf("got %v, want %v", got, current)
	}
}

func TestApplyExtensionsOverwritesConflictingValue(t *testing.T) {
	c := compileFull(t, 1, "amount:>0", nil, ext("x.score", `2`))
	current := ext("x.score", `1`, "keep.me", `"v"`)
	got, changed := rules.ApplyExtensions([]rules.Compiled{c}, rec(5, "acme", nil), current)
	if !changed {
		t.Fatal("expected changed=true (value overwritten)")
	}
	want := ext("x.score", `2`, "keep.me", `"v"`)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestApplyExtensionsLaterRuleWins(t *testing.T) {
	c1 := compileFull(t, 1, "amount:>0", nil, ext("tier.level", `"low"`))
	c2 := compileFull(t, 2, "amount:>0", nil, ext("tier.level", `"high"`))
	got, changed := rules.ApplyExtensions([]rules.Compiled{c1, c2}, rec(5, "acme", nil), nil)
	if !changed || string(got["tier.level"]) != `"high"` {
		t.Fatalf("got %v, changed=%v; want tier.level=\"high\"", got, changed)
	}
}

func TestApplyExtensionsLabelsOnlyRuleIsNoop(t *testing.T) {
	// A rule that applies only labels contributes nothing to the extensions merge.
	c := compileFull(t, 1, "amount:>0", map[string]string{"status": "review"}, nil)
	got, changed := rules.ApplyExtensions([]rules.Compiled{c}, rec(5, "acme", nil), nil)
	if changed || len(got) != 0 {
		t.Fatalf("got %v, changed=%v; want no extension change", got, changed)
	}
}
