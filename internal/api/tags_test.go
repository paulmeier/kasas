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

func TestDistinctTags(t *testing.T) {
	sets := []string{
		`["Food","coffee"]`,
		`["food","Rent"]`, // "food" duplicates "Food" case-insensitively
		`["coffee"]`,
		`[]`,
	}
	// Sorted case-insensitively; first spelling ("Food") wins.
	want := []string{"coffee", "Food", "Rent"}
	if got := distinctTags(sets); !reflect.DeepEqual(got, want) {
		t.Fatalf("distinctTags = %q, want %q", got, want)
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
