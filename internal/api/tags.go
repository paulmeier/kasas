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

// tagCounts explodes the stored JSON tag arrays into the global tag vocabulary,
// each annotated with the number of transactions that carry it. Each input
// string is one transaction's tags array, so a tag's count is the number of
// arrays it appears in. Tags are de-duplicated case-insensitively (first
// spelling wins) and the result is sorted case-insensitively. It powers the Tags
// page (names + counts) and the dashboard typeahead (names only).
func tagCounts(sets []string) []TagDTO {
	type entry struct {
		name  string
		count int
	}
	seen := make(map[string]*entry) // lower-cased key -> display + count
	for _, set := range sets {
		// A transaction counts once per distinct tag it carries, guarding against
		// a stored array that somehow repeats a tag.
		counted := make(map[string]bool)
		for _, t := range decodeTags(set) {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			key := strings.ToLower(t)
			e, ok := seen[key]
			if !ok {
				e = &entry{name: t}
				seen[key] = e
			}
			if !counted[key] {
				counted[key] = true
				e.count++
			}
		}
	}
	out := make([]TagDTO, 0, len(seen))
	for _, e := range seen {
		out = append(out, TagDTO{Name: e.name, TransactionCount: e.count})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// containsFold reports whether tags contains s, comparing case-insensitively.
func containsFold(tags []string, s string) bool {
	for _, t := range tags {
		if strings.EqualFold(t, s) {
			return true
		}
	}
	return false
}

// removeFold returns tags with every case-insensitive match of s removed,
// preserving the order and spelling of the remaining tags.
func removeFold(tags []string, s string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if !strings.EqualFold(t, s) {
			out = append(out, t)
		}
	}
	return out
}
