package extensions_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/paulmeier/kasas/internal/extensions"
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   map[string]json.RawMessage
		want map[string]json.RawMessage
	}{
		{
			"keeps arbitrary JSON value types",
			map[string]json.RawMessage{"tax.category": raw(`"meal"`), "forecast.recurring": raw(`true`), "custom.myapp.score": raw(`88`)},
			map[string]json.RawMessage{"tax.category": raw(`"meal"`), "forecast.recurring": raw(`true`), "custom.myapp.score": raw(`88`)},
		},
		{"trims and compacts the value", map[string]json.RawMessage{"a.b": raw(`  { "x" : 1 }  `)}, map[string]json.RawMessage{"a.b": raw(`{"x":1}`)}},
		{"preserves key case (namespaces are identifiers)", map[string]json.RawMessage{" MyApp.Score ": raw(`1`)}, map[string]json.RawMessage{"MyApp.Score": raw(`1`)}},
		{"drops empty key", map[string]json.RawMessage{"   ": raw(`1`)}, map[string]json.RawMessage{}},
		{"drops invalid JSON value", map[string]json.RawMessage{"a": raw(`not json`)}, map[string]json.RawMessage{}},
		{"drops empty value", map[string]json.RawMessage{"a": raw(``)}, map[string]json.RawMessage{}},
		{"strips path-breaking chars from key", map[string]json.RawMessage{`a"b\c`: raw(`1`)}, map[string]json.RawMessage{"abc": raw(`1`)}},
		{"nil is empty", nil, map[string]json.RawMessage{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := extensions.Normalize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Normalize(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeCapsKeyLengthValueSizeAndCount(t *testing.T) {
	long := strings.Repeat("x", extensions.MaxKeyLen+10)
	got := extensions.Normalize(map[string]json.RawMessage{long: raw(`1`)})
	for k := range got {
		if len([]rune(k)) != extensions.MaxKeyLen {
			t.Fatalf("key length = %d runes, want capped at %d", len([]rune(k)), extensions.MaxKeyLen)
		}
	}

	// An oversized value is dropped.
	big := raw(`"` + strings.Repeat("y", extensions.MaxValueBytes) + `"`)
	if got := extensions.Normalize(map[string]json.RawMessage{"a": big}); len(got) != 0 {
		t.Fatalf("oversized value should be dropped, got %v", got)
	}

	many := make(map[string]json.RawMessage, extensions.MaxCount+5)
	for i := 0; i < extensions.MaxCount+5; i++ {
		many[string(rune('a'+i/26))+string(rune('a'+i%26))] = raw(`1`)
	}
	if got := extensions.Normalize(many); len(got) != extensions.MaxCount {
		t.Fatalf("extension count = %d, want capped at %d", len(got), extensions.MaxCount)
	}
}

func TestDecodeAndValues(t *testing.T) {
	stored := `{"tax.category":"meal","custom.myapp.score":88}`
	dec := extensions.Decode(stored)
	if string(dec["tax.category"]) != `"meal"` || string(dec["custom.myapp.score"]) != `88` {
		t.Fatalf("Decode = %v", dec)
	}
	vals := extensions.Values(stored)
	if vals["tax.category"] != "meal" {
		t.Fatalf("Values string = %v", vals["tax.category"])
	}
	if vals["custom.myapp.score"].(float64) != 88 {
		t.Fatalf("Values number = %v", vals["custom.myapp.score"])
	}
	// Never nil and tolerant of garbage.
	for _, s := range []string{"", "{}", "not json", "null", `["a"]`} {
		if extensions.Decode(s) == nil || extensions.Values(s) == nil {
			t.Fatalf("decoders must never return nil (input %q)", s)
		}
	}
}

func TestEncodeRoundTrip(t *testing.T) {
	in := map[string]json.RawMessage{"tax.category": raw(`"meal"`), "forecast.recurring": raw(`true`)}
	encoded, err := extensions.Encode(in)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !reflect.DeepEqual(extensions.Decode(encoded), in) {
		t.Fatalf("round trip = %v, want %v", extensions.Decode(encoded), in)
	}
	if encoded, _ := extensions.Encode(nil); encoded != "{}" {
		t.Fatalf("Encode(nil) = %q, want %q", encoded, "{}")
	}
}

func TestStringifyValue(t *testing.T) {
	tests := map[string]string{
		`"meal"`:       "meal",
		`88`:           "88",
		`true`:         "true",
		`null`:         "null",
		`{"x":1}`:      `{"x":1}`,
		`[1,2,3]`:      `[1,2,3]`,
		`"with space"`: "with space",
	}
	for in, want := range tests {
		if got := extensions.StringifyValue(raw(in)); got != want {
			t.Fatalf("StringifyValue(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestNamespace(t *testing.T) {
	tests := map[string]string{
		"tax.category":       "tax",
		"custom.myapp.score": "custom",
		"flat":               "flat",
		"":                   "",
	}
	for in, want := range tests {
		if got := extensions.Namespace(in); got != want {
			t.Fatalf("Namespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeKey(t *testing.T) {
	if got := extensions.NormalizeKey("  tax.Category  "); got != "tax.Category" {
		t.Fatalf("NormalizeKey = %q, want %q (case preserved)", got, "tax.Category")
	}
	if got := extensions.NormalizeKey(`x"y\z`); got != "xyz" {
		t.Fatalf("NormalizeKey strip = %q, want %q", got, "xyz")
	}
	if got := extensions.NormalizeKey("   "); got != "" {
		t.Fatalf("NormalizeKey(blank) = %q, want empty", got)
	}
}
