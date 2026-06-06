package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeLabels(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]string
		want map[string]string
	}{
		{"trims key and value", map[string]string{"  category ": "  food "}, map[string]string{"category": "food"}},
		{"lowercases key, preserves value case", map[string]string{"Category": "Food"}, map[string]string{"category": "Food"}},
		{"drops empty key", map[string]string{"   ": "food"}, map[string]string{}},
		{"drops empty value (no flag labels)", map[string]string{"category": "  "}, map[string]string{}},
		{"strips path-breaking chars from key", map[string]string{`a"b\c`: "x"}, map[string]string{"abc": "x"}},
		{"nil is empty", nil, map[string]string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeLabels(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeLabels(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeLabelsCapsLengthAndCount(t *testing.T) {
	long := strings.Repeat("x", maxLabelLen+10)
	got := normalizeLabels(map[string]string{long: long})
	for k, v := range got {
		if len([]rune(k)) != maxLabelLen {
			t.Fatalf("key length = %d runes, want capped at %d", len([]rune(k)), maxLabelLen)
		}
		if len([]rune(v)) != maxLabelLen {
			t.Fatalf("value length = %d runes, want capped at %d", len([]rune(v)), maxLabelLen)
		}
	}

	many := make(map[string]string, maxLabelCount+5)
	for i := 0; i < maxLabelCount+5; i++ {
		// distinct two-char keys so capping (not collision) limits the count
		many[string(rune('a'+i/26))+string(rune('a'+i%26))] = "v"
	}
	if got := normalizeLabels(many); len(got) != maxLabelCount {
		t.Fatalf("label count = %d, want capped at %d", len(got), maxLabelCount)
	}
}

func TestDecodeLabels(t *testing.T) {
	tests := []struct {
		in   string
		want map[string]string
	}{
		{"", map[string]string{}},
		{"{}", map[string]string{}},
		{`{"category":"food","person":"dad"}`, map[string]string{"category": "food", "person": "dad"}},
		{"not json", map[string]string{}},
		{"null", map[string]string{}},
		{`["a","b"]`, map[string]string{}}, // a stray (pre-migration) array decodes to empty
	}
	for _, tc := range tests {
		if got := decodeLabels(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("decodeLabels(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	// Never nil, so the DTO marshals to {} rather than null.
	if decodeLabels("garbage") == nil {
		t.Fatal("decodeLabels must never return nil")
	}
}

func TestLabelCounts(t *testing.T) {
	sets := []string{
		`{"category":"food","tag":"coffee"}`,
		`{"category":"rent","tag":"coffee"}`,
		`{"tag":"coffee"}`,
		`{}`,
	}
	// Sorted by key, then value; the count is the number of transactions (sets)
	// each (key,value) pair appears in.
	want := []LabelDTO{
		{Key: "category", Value: "food", TransactionCount: 1},
		{Key: "category", Value: "rent", TransactionCount: 1},
		{Key: "tag", Value: "coffee", TransactionCount: 3},
	}
	if got := labelCounts(sets); !reflect.DeepEqual(got, want) {
		t.Fatalf("labelCounts = %+v, want %+v", got, want)
	}
}

func TestEncodeLabelsRoundTrip(t *testing.T) {
	labels := map[string]string{"category": "food", "person": "dad"}
	encoded, err := encodeLabels(labels)
	if err != nil {
		t.Fatalf("encodeLabels: %v", err)
	}
	if !reflect.DeepEqual(decodeLabels(encoded), labels) {
		t.Fatalf("round trip = %v, want %v", decodeLabels(encoded), labels)
	}
	// nil encodes as an empty JSON object, not null.
	if encoded, _ := encodeLabels(nil); encoded != "{}" {
		t.Fatalf("encodeLabels(nil) = %q, want %q", encoded, "{}")
	}
}

func TestNormalizeKey(t *testing.T) {
	if got := normalizeKey("  Category  "); got != "category" {
		t.Fatalf("normalizeKey = %q, want %q", got, "category")
	}
	if got := normalizeKey(`x"y\z`); got != "xyz" {
		t.Fatalf("normalizeKey strip = %q, want %q", got, "xyz")
	}
	if got := normalizeKey("   "); got != "" {
		t.Fatalf("normalizeKey(blank) = %q, want empty", got)
	}
}
