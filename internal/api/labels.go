package api

import (
	"encoding/json"
	"sort"
	"strings"
)

// Labels are stored on a transaction as a JSON object of key->value string pairs
// (see the 00003 migration), e.g. '{"category":"food","person":"dad"}'. These
// helpers convert to/from that storage form and enforce the normalization rules:
// keys are trimmed, lowercased (canonical, so JSON querying is predictable) and
// stripped of characters that would break a JSON path; values are trimmed (case
// preserved, since value matching is exact); both are length-capped; and any pair
// with an empty key or value is dropped — labels are strictly key:value, there
// are no valueless labels.
const (
	maxLabelLen   = 50 // per key/value length cap (runes)
	maxLabelCount = 50 // per-transaction label cap
)

// decodeLabels parses a stored JSON labels object into a map. It never returns
// nil (so the DTO marshals to {} rather than null) and tolerates a malformed or
// non-object value by returning an empty map.
func decodeLabels(stored string) map[string]string {
	out := map[string]string{}
	if stored == "" {
		return out
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return map[string]string{}
	}
	if out == nil {
		return map[string]string{}
	}
	return out
}

// encodeLabels marshals a normalized labels map into the stored JSON object form.
func encodeLabels(labels map[string]string) (string, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	b, err := json.Marshal(labels)
	return string(b), err
}

// normalizeKey canonicalizes a label key: trimmed, lowercased, stripped of
// characters that would break a JSON path ('"', '\\', and control characters),
// then rune-capped. It is also used to canonicalize filter/delete key inputs so
// they match stored keys. Returns "" for an empty or fully-stripped key.
func normalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLabelLen {
		s = strings.TrimSpace(string(r[:maxLabelLen]))
	}
	return s
}

// normalizeValue canonicalizes a label value: trimmed and rune-capped. Case is
// preserved (value matching is exact). Also used for filter/delete value inputs.
func normalizeValue(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > maxLabelLen {
		s = strings.TrimSpace(string(r[:maxLabelLen]))
	}
	return s
}

// normalizeLabels normalizes every key and value, drops any pair with an empty
// key or value (strict key:value), and caps the number of keys. The result is
// never nil. Keys are processed in sorted order so capping and collision
// resolution (last normalized value for a key wins) are deterministic.
func normalizeLabels(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := normalizeKey(k)
		nv := normalizeValue(in[k])
		if nk == "" || nv == "" {
			continue
		}
		if _, exists := out[nk]; !exists && len(out) >= maxLabelCount {
			continue
		}
		out[nk] = nv
	}
	return out
}

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
