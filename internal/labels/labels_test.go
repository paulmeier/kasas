package labels_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/paulmeier/kasas/internal/labels"
)

func TestNormalize(t *testing.T) {
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
			if got := labels.Normalize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Normalize(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeCapsLengthAndCount(t *testing.T) {
	long := strings.Repeat("x", labels.MaxLen+10)
	got := labels.Normalize(map[string]string{long: long})
	for k, v := range got {
		if len([]rune(k)) != labels.MaxLen {
			t.Fatalf("key length = %d runes, want capped at %d", len([]rune(k)), labels.MaxLen)
		}
		if len([]rune(v)) != labels.MaxLen {
			t.Fatalf("value length = %d runes, want capped at %d", len([]rune(v)), labels.MaxLen)
		}
	}

	many := make(map[string]string, labels.MaxCount+5)
	for i := 0; i < labels.MaxCount+5; i++ {
		// distinct two-char keys so capping (not collision) limits the count
		many[string(rune('a'+i/26))+string(rune('a'+i%26))] = "v"
	}
	if got := labels.Normalize(many); len(got) != labels.MaxCount {
		t.Fatalf("label count = %d, want capped at %d", len(got), labels.MaxCount)
	}
}

func TestDecode(t *testing.T) {
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
		if got := labels.Decode(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("Decode(%q) = %#v, want %#v", tc.in, got, tc.want)
		}
	}
	// Never nil, so the DTO marshals to {} rather than null.
	if labels.Decode("garbage") == nil {
		t.Fatal("Decode must never return nil")
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := map[string]string{"category": "food", "person": "dad"}
	encoded, err := labels.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !reflect.DeepEqual(labels.Decode(encoded), in) {
		t.Fatalf("round trip = %v, want %v", labels.Decode(encoded), in)
	}
	// nil encodes as an empty JSON object, not null.
	if encoded, _ := labels.Encode(nil); encoded != "{}" {
		t.Fatalf("Encode(nil) = %q, want %q", encoded, "{}")
	}
}

func TestNormalizeKey(t *testing.T) {
	if got := labels.NormalizeKey("  Category  "); got != "category" {
		t.Fatalf("NormalizeKey = %q, want %q", got, "category")
	}
	if got := labels.NormalizeKey(`x"y\z`); got != "xyz" {
		t.Fatalf("NormalizeKey strip = %q, want %q", got, "xyz")
	}
	if got := labels.NormalizeKey("   "); got != "" {
		t.Fatalf("NormalizeKey(blank) = %q, want empty", got)
	}
}
