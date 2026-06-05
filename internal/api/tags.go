package api

import (
	"encoding/json"
	"sort"
	"strings"
)

// Tags are stored on a transaction as a JSON array of strings (see the
// 00002 migration). These helpers convert to/from that storage form and
// enforce the normalization rules: trimmed, non-empty, length-capped, and
// de-duplicated case-insensitively (the first spelling of a tag wins).
const (
	maxTagLen   = 50 // per-tag length cap (runes)
	maxTagCount = 50 // per-transaction tag cap
)

// decodeTags parses a stored JSON tags array into a slice. It never returns
// nil (so the DTO marshals to [] rather than null) and tolerates a malformed or
// empty value by returning an empty slice.
func decodeTags(stored string) []string {
	out := []string{}
	if stored == "" {
		return out
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return []string{}
	}
	if out == nil {
		return []string{}
	}
	return out
}

// encodeTags marshals a normalized tag slice into the stored JSON array form.
func encodeTags(tags []string) (string, error) {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	return string(b), err
}

// normalizeTags trims each tag, drops empties, caps length (by rune) and count,
// and removes case-insensitive duplicates while preserving input order and the
// first spelling encountered.
func normalizeTags(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, t := range in {
		t = strings.TrimSpace(t)
		if r := []rune(t); len(r) > maxTagLen {
			t = strings.TrimSpace(string(r[:maxTagLen]))
		}
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
		if len(out) >= maxTagCount {
			break
		}
	}
	return out
}

// distinctTags explodes the stored JSON tag arrays into a single vocabulary,
// de-duplicated case-insensitively (first spelling wins) and sorted
// case-insensitively. It powers the dashboard's typeahead.
func distinctTags(sets []string) []string {
	seen := make(map[string]string) // lower-cased key -> display spelling
	for _, set := range sets {
		for _, t := range decodeTags(set) {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			key := strings.ToLower(t)
			if _, ok := seen[key]; !ok {
				seen[key] = t
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, display := range seen {
		out = append(out, display)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}
