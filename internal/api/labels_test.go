package api

import (
	"reflect"
	"testing"
)

// The label model (Normalize/Decode/Encode/NormalizeKey + the length/count caps)
// is owned and tested by internal/labels. labelCounts is api-specific (it builds
// the LabelDTO vocabulary), so it is tested here.
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
