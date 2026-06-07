package relationships_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/paulmeier/kasas/internal/relationships"
)

func rel(kind, target string) relationships.Relationship {
	return relationships.Relationship{Kind: kind, Target: target}
}

func TestNormalizeKind(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"refund_of", "refund_of"},
		{"  Refund_Of  ", "refund_of"},
		{"refund-of", "refund_of"},
		{"Refund Of", "refund_of"},
		{"transfer->to", "transfer_to"},
		{"a..b__c--d", "a_b_c_d"},
		{"_leading_and_trailing_", "leading_and_trailing"},
		{"UPPER123", "upper123"},
		{"emoji🎉here", "emojihere"},
		{"   ", ""},
		{"---", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := relationships.NormalizeKind(tc.in); got != tc.want {
				t.Fatalf("NormalizeKind(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeKindCapsLength(t *testing.T) {
	long := strings.Repeat("x", relationships.MaxKindLen+10)
	got := relationships.NormalizeKind(long)
	if len([]rune(got)) != relationships.MaxKindLen {
		t.Fatalf("kind length = %d runes, want capped at %d", len([]rune(got)), relationships.MaxKindLen)
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   []relationships.Relationship
		want []relationships.Relationship
	}{
		{
			"normalizes kind, trims target, sorts",
			[]relationships.Relationship{rel("Transfer To", " txn_b "), rel("refund-of", "txn_a")},
			[]relationships.Relationship{rel("refund_of", "txn_a"), rel("transfer_to", "txn_b")},
		},
		{
			"dedupes by (kind,target)",
			[]relationships.Relationship{rel("refund_of", "txn_a"), rel("refund_of", "txn_a")},
			[]relationships.Relationship{rel("refund_of", "txn_a")},
		},
		{
			"same kind to different targets are distinct",
			[]relationships.Relationship{rel("withholding_for", "txn_b"), rel("withholding_for", "txn_a")},
			[]relationships.Relationship{rel("withholding_for", "txn_a"), rel("withholding_for", "txn_b")},
		},
		{"drops empty kind", []relationships.Relationship{rel("   ", "txn_a")}, []relationships.Relationship{}},
		{"drops empty target", []relationships.Relationship{rel("refund_of", "  ")}, []relationships.Relationship{}},
		{"nil is empty", nil, []relationships.Relationship{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := relationships.Normalize(tc.in); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("Normalize(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestNormalizeCapsCount(t *testing.T) {
	in := make([]relationships.Relationship, 0, relationships.MaxCount+5)
	for i := 0; i < relationships.MaxCount+5; i++ {
		in = append(in, rel("refund_of", fmt.Sprintf("txn_%03d", i)))
	}
	if got := relationships.Normalize(in); len(got) != relationships.MaxCount {
		t.Fatalf("count = %d, want capped at %d", len(got), relationships.MaxCount)
	}
}

func TestDecodeEncodeRoundTrip(t *testing.T) {
	stored := `[{"kind":"refund_of","target":"txn_a"},{"kind":"transfer_to","target":"txn_b"}]`
	got := relationships.Decode(stored)
	want := []relationships.Relationship{rel("refund_of", "txn_a"), rel("transfer_to", "txn_b")}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode(%q) = %v, want %v", stored, got, want)
	}
	enc, err := relationships.Encode(got)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if enc != stored {
		t.Fatalf("Encode round-trip = %q, want %q", enc, stored)
	}
}

func TestDecodeTolerant(t *testing.T) {
	for _, in := range []string{"", "null", "not json", `{"kind":"x"}`} {
		got := relationships.Decode(in)
		if got == nil {
			t.Fatalf("Decode(%q) returned nil, want empty slice", in)
		}
		if in == "not json" && len(got) != 0 {
			t.Fatalf("Decode(%q) = %v, want empty", in, got)
		}
	}
}

func TestEncodeNilIsEmptyArray(t *testing.T) {
	enc, err := relationships.Encode(nil)
	if err != nil {
		t.Fatalf("Encode(nil): %v", err)
	}
	if enc != "[]" {
		t.Fatalf("Encode(nil) = %q, want %q", enc, "[]")
	}
}
