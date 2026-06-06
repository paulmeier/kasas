// Package labels is the pure model for kasas transaction (and rule) labels:
// strict key->value string pairs stored as a JSON object per transaction, e.g.
// '{"category":"food","person":"dad"}'. It defines the canonical normalization
// (trimmed, lowercased keys; trimmed values with case preserved; length and
// count caps; no valueless labels) and the JSON encode/decode.
//
// It is dependency-light (standard library only) and knows nothing about the
// database or HTTP layers, so it is shared by every writer — the REST API, the
// poller's auto-labeling, and the rules engine — and they all agree on the
// stored form.
package labels

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	// MaxLen caps each key and value length (runes).
	MaxLen = 50
	// MaxCount caps the number of keys per transaction.
	MaxCount = 50
)

// Decode parses a stored JSON labels object into a map. It never returns nil
// (so callers and DTOs marshal to {} rather than null) and tolerates a
// malformed or non-object value by returning an empty map.
func Decode(stored string) map[string]string {
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

// Encode marshals a labels map into the stored JSON object form. A nil map
// encodes as "{}" (not "null").
func Encode(labels map[string]string) (string, error) {
	if labels == nil {
		labels = map[string]string{}
	}
	b, err := json.Marshal(labels)
	return string(b), err
}

// NormalizeKey canonicalizes a label key: trimmed, lowercased, stripped of
// characters that would break a JSON path ('"', '\\', and control characters),
// then rune-capped. It is also used to canonicalize filter/delete key inputs so
// they match stored keys. Returns "" for an empty or fully-stripped key.
func NormalizeKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxLen {
		s = strings.TrimSpace(string(r[:MaxLen]))
	}
	return s
}

// NormalizeValue canonicalizes a label value: trimmed and rune-capped. Case is
// preserved (value matching is exact). Also used for filter/delete value inputs.
func NormalizeValue(s string) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxLen {
		s = strings.TrimSpace(string(r[:MaxLen]))
	}
	return s
}

// Normalize normalizes every key and value, drops any pair with an empty key or
// value (strict key:value), and caps the number of keys. The result is never
// nil. Keys are processed in sorted order so capping and collision resolution
// (last normalized value for a key wins) are deterministic.
func Normalize(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := NormalizeKey(k)
		nv := NormalizeValue(in[k])
		if nk == "" || nv == "" {
			continue
		}
		if _, exists := out[nk]; !exists && len(out) >= MaxCount {
			continue
		}
		out[nk] = nv
	}
	return out
}
