package events

import (
	"encoding/json"
	"strconv"
	"time"
)

// FieldChange is one scalar field that differs between two transaction versions.
type FieldChange struct {
	Field string `json:"field"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// VersionDiff describes how one transaction version differs from the one before it:
// scalar field changes plus label and schema-extension additions, removals, and
// value changes. The first version of a transaction diffs against the zero value (a
// "birth" diff), so every consumer can render index 0 the same way as any later
// version. The maps are never nil (they marshal to {} rather than null). Extension
// values are compared as strings (a JSON string by its text, any other JSON value
// by its compact encoding), mirroring extensions.StringifyValue.
type VersionDiff struct {
	Fields            []FieldChange        `json:"fields"`
	LabelsAdded       map[string]string    `json:"labels_added"`
	LabelsRemoved     map[string]string    `json:"labels_removed"`
	LabelsChanged     map[string][2]string `json:"labels_changed"` // key -> {from, to}
	ExtensionsAdded   map[string]string    `json:"extensions_added"`
	ExtensionsRemoved map[string]string    `json:"extensions_removed"`
	ExtensionsChanged map[string][2]string `json:"extensions_changed"` // key -> {from, to}
}

// DiffSnapshots computes the difference from prev to next. Amounts are compared as
// strings (never parsed to float — the stored decimal string is authoritative);
// pending renders as "true"/"false"; the date is RFC3339, or "" when unset so a
// birth diff reads from "" rather than the zero time. Nil label maps are treated as
// empty. Scalar fields are emitted in a fixed order, so the result is deterministic.
func DiffSnapshots(prev, next TransactionPayload) VersionDiff {
	d := VersionDiff{
		LabelsAdded:       map[string]string{},
		LabelsRemoved:     map[string]string{},
		LabelsChanged:     map[string][2]string{},
		ExtensionsAdded:   map[string]string{},
		ExtensionsRemoved: map[string]string{},
		ExtensionsChanged: map[string][2]string{},
	}
	add := func(field, from, to string) {
		if from != to {
			d.Fields = append(d.Fields, FieldChange{Field: field, From: from, To: to})
		}
	}
	add("account_id", prev.AccountID, next.AccountID)
	add("amount", prev.Amount, next.Amount)
	add("pending", strconv.FormatBool(prev.Pending), strconv.FormatBool(next.Pending))
	add("date", fmtDiffTime(prev.Date), fmtDiffTime(next.Date))
	add("description", prev.Description, next.Description)
	add("payee", prev.Payee, next.Payee)
	add("memo", prev.Memo, next.Memo)

	for k, v := range next.Labels {
		old, ok := prev.Labels[k]
		switch {
		case !ok:
			d.LabelsAdded[k] = v
		case old != v:
			d.LabelsChanged[k] = [2]string{old, v}
		}
	}
	for k, v := range prev.Labels {
		if _, ok := next.Labels[k]; !ok {
			d.LabelsRemoved[k] = v
		}
	}

	for k, v := range next.Extensions {
		nv := stringifyExt(v)
		old, ok := prev.Extensions[k]
		switch {
		case !ok:
			d.ExtensionsAdded[k] = nv
		default:
			if ov := stringifyExt(old); ov != nv {
				d.ExtensionsChanged[k] = [2]string{ov, nv}
			}
		}
	}
	for k, v := range prev.Extensions {
		if _, ok := next.Extensions[k]; !ok {
			d.ExtensionsRemoved[k] = stringifyExt(v)
		}
	}
	return d
}

func fmtDiffTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// stringifyExt renders a decoded extension value (the any from a JSON object) as a
// stable string for diffing: a JSON string yields its text, any other value its
// compact JSON. Mirrors extensions.StringifyValue (which works on the raw bytes).
func stringifyExt(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}
