package api

import (
	"sort"

	"github.com/paulmeier/kasas/internal/labels"
)

// Labels are stored on a transaction as a JSON object of key->value string pairs
// (see the 00003 migration), e.g. '{"category":"food","person":"dad"}'. The
// canonical model — JSON encode/decode and the normalization rules (trimmed,
// lowercased keys; trimmed values; length/count caps; no valueless labels) —
// lives in internal/labels so the REST API, the poller's auto-labeling, and the
// rules engine all agree on the stored form. The unexported helpers here are
// thin aliases kept so the rest of the api package (and its tests) read the same
// as before.

func decodeLabels(stored string) map[string]string           { return labels.Decode(stored) }
func encodeLabels(in map[string]string) (string, error)      { return labels.Encode(in) }
func normalizeKey(s string) string                           { return labels.NormalizeKey(s) }
func normalizeValue(s string) string                         { return labels.NormalizeValue(s) }
func normalizeLabels(in map[string]string) map[string]string { return labels.Normalize(in) }

// labelCounts explodes the stored JSON label objects into the global label
// vocabulary: one entry per distinct (key, value) pair, annotated with the number
// of transactions carrying it. Each input string is one transaction's labels
// object, and an object has unique keys, so each set contributes at most once per
// pair. The result is sorted by key, then value. Built in Go (not SQL) to stay
// portable across SQLite and Postgres; it powers the Labels page and the
// dashboard typeahead.
func labelCounts(sets []string) []LabelDTO {
	type pair struct{ key, value string }
	counts := make(map[pair]int)
	for _, set := range sets {
		for k, v := range decodeLabels(set) {
			counts[pair{key: k, value: v}]++
		}
	}
	out := make([]LabelDTO, 0, len(counts))
	for p, n := range counts {
		out = append(out, LabelDTO{Key: p.key, Value: p.value, TransactionCount: n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].Value < out[j].Value
	})
	return out
}
