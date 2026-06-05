package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTags(t *testing.T) {
	tests := []struct {
		name string
		in   []string
		want []string
	}{
		{"trims and drops empties", []string{"  food ", "", "   ", "rent"}, []string{"food", "rent"}},
		{"case-insensitive dedupe keeps first spelling", []string{"Food", "food", "FOOD"}, []string{"Food"}},
		{"preserves order", []string{"c", "a", "b"}, []string{"c", "a", "b"}},
		{"nil is empty", nil, []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeTags(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("normalizeTags(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeTagsCapsLengthAndCount(t *testing.T) {
	long := strings.Repeat("x", maxTagLen+10)
	got := normalizeTags([]string{long})
	if len([]rune(got[0])) != maxTagLen {
		t.Fatalf("tag length = %d runes, want capped at %d", len([]rune(got[0])), maxTagLen)
	}

	many := make([]string, maxTagCount+5)
	for i := range many {
		many[i] = string(rune('a' + i)) // distinct single-char tags
	}
	if got := normalizeTags(many); len(got) != maxTagCount {
		t.Fatalf("tag count = %d, want capped at %d", len(got), maxTagCount)
	}
}

func TestDecodeTags(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{}},
		{"[]", []string{}},
		{`["a","b"]`, []string{"a", "b"}},
		{"not json", []string{}},
		{"null", []string{}},
	}
	for _, tc := range tests {
		if got := decodeTags(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("decodeTags(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	// Never nil, so the DTO marshals to [] rather than null.
	if decodeTags("garbage") == nil {
		t.Fatal("decodeTags must never return nil")
	}
}

func TestTagCounts(t *testing.T) {
	sets := []string{
		`["Food","coffee"]`,
		`["food","Rent"]`, // "food" duplicates "Food" case-insensitively
		`["coffee"]`,
		`[]`,
	}
	// Sorted case-insensitively; first spelling ("Food") wins; the count is the
	// number of transactions (input arrays) each tag appears in.
	want := []TagDTO{
		{Name: "coffee", TransactionCount: 2},
		{Name: "Food", TransactionCount: 2},
		{Name: "Rent", TransactionCount: 1},
	}
	if got := tagCounts(sets); !reflect.DeepEqual(got, want) {
		t.Fatalf("tagCounts = %+v, want %+v", got, want)
	}
}

func TestTagCountsDeduplicatesWithinTransaction(t *testing.T) {
	// A single transaction repeating a tag (case-insensitively) counts once.
	got := tagCounts([]string{`["food","Food","FOOD"]`})
	want := []TagDTO{{Name: "food", TransactionCount: 1}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tagCounts = %+v, want %+v", got, want)
	}
}

func TestContainsFold(t *testing.T) {
	tags := []string{"Food", "Rent"}
	if !containsFold(tags, "food") {
		t.Fatal("containsFold should match case-insensitively")
	}
	if containsFold(tags, "coffee") {
		t.Fatal("containsFold should not match an absent tag")
	}
}

func TestRemoveFold(t *testing.T) {
	got := removeFold([]string{"Food", "rent", "FOOD"}, "food")
	if want := []string{"rent"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("removeFold = %q, want %q", got, want)
	}
	// Removing an absent tag leaves the slice unchanged (but never nil).
	if got := removeFold([]string{"a"}, "b"); !reflect.DeepEqual(got, []string{"a"}) {
		t.Fatalf("removeFold absent = %q, want [a]", got)
	}
}

func TestEncodeTagsRoundTrip(t *testing.T) {
	tags := []string{"food", "rent"}
	encoded, err := encodeTags(tags)
	if err != nil {
		t.Fatalf("encodeTags: %v", err)
	}
	if !reflect.DeepEqual(decodeTags(encoded), tags) {
		t.Fatalf("round trip = %q, want %q", decodeTags(encoded), tags)
	}
	// nil encodes as an empty JSON array, not null.
	if encoded, _ := encodeTags(nil); encoded != "[]" {
		t.Fatalf("encodeTags(nil) = %q, want %q", encoded, "[]")
	}
}
