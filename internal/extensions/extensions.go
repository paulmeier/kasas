// Package extensions is the pure model for kasas transaction "schema extensions":
// arbitrary, namespaced metadata stored as a JSON object per transaction, e.g.
// '{"tax.category":"meal","forecast.recurring":true,"custom.myapp.score":88}'.
//
// Unlike labels (strict, lowercase key->value STRINGS for user categorization,
// see internal/labels), extensions are app-owned: keys are namespaced (dotted,
// case-preserved) and values are ARBITRARY JSON (string, number, boolean, null,
// object, array). They let independent apps attach their own data to a
// transaction without a kasas schema change.
//
// It is dependency-light (standard library only) so it can be shared by every
// surface: the REST API and MCP writers, the search matcher, and the WASM
// dashboard. It knows nothing about the database or HTTP layers.
//
// Two decode paths exist on purpose. Decode returns map[string]json.RawMessage
// (lossless — the write/storage path), while Values returns map[string]any (the
// REST/MCP output boundary and history snapshots), mirroring how the event stream
// decodes its Data payload to `any` because the MCP SDK rejects json.RawMessage in
// tool output schemas.
package extensions

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"
)

const (
	// MaxKeyLen caps each extension key length (runes). Larger than a label key so
	// dotted namespaces like "custom.myapp.score" fit comfortably.
	MaxKeyLen = 100
	// MaxCount caps the number of keys per transaction.
	MaxCount = 50
	// MaxValueBytes caps a single encoded JSON value, guarding against a
	// pathological nested blob bloating a transaction row.
	MaxValueBytes = 8 << 10
)

// Decode parses a stored JSON extensions object into a map of raw JSON values
// (lossless). It never returns nil (so callers and DTOs marshal to {} rather than
// null) and tolerates a malformed or non-object value by returning an empty map.
func Decode(stored string) map[string]json.RawMessage {
	out := map[string]json.RawMessage{}
	if stored == "" {
		return out
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return map[string]json.RawMessage{}
	}
	if out == nil {
		return map[string]json.RawMessage{}
	}
	return out
}

// Values decodes a stored extensions object into a map of plain Go values
// (string, float64, bool, nil, []any, map[string]any) for the REST/MCP output
// boundary and history snapshots. Never returns nil.
func Values(stored string) map[string]any {
	out := map[string]any{}
	if stored == "" {
		return out
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		return map[string]any{}
	}
	return out
}

// Encode marshals an extensions map into the stored JSON object form. A nil map
// encodes as "{}" (not "null").
func Encode(ext map[string]json.RawMessage) (string, error) {
	if ext == nil {
		ext = map[string]json.RawMessage{}
	}
	b, err := json.Marshal(ext)
	return string(b), err
}

// NormalizeKey canonicalizes an extension key: trimmed, stripped of characters
// that would break a JSON path ('"', '\\', and control characters), then
// rune-capped. Case is PRESERVED (namespaces are identifiers — "myapp.Score" is
// not "myapp.score"), which diverges from label keys; dots are kept since they
// form the namespace. Returns "" for an empty or fully-stripped key.
func NormalizeKey(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Map(func(r rune) rune {
		if r == '"' || r == '\\' || r < 0x20 {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > MaxKeyLen {
		s = strings.TrimSpace(string(r[:MaxKeyLen]))
	}
	return s
}

// Normalize normalizes every key, drops any entry with an empty key or an
// empty/invalid/oversized JSON value, compacts the kept values to canonical form,
// and caps the number of keys. The result is never nil. Keys are processed in
// sorted order so capping and collision resolution (last value for a normalized
// key wins) are deterministic.
func Normalize(in map[string]json.RawMessage) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage, len(in))
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := NormalizeKey(k)
		if nk == "" {
			continue
		}
		v := bytes.TrimSpace(in[k])
		if len(v) == 0 || !json.Valid(v) || len(v) > MaxValueBytes {
			continue
		}
		if _, exists := out[nk]; !exists && len(out) >= MaxCount {
			continue
		}
		out[nk] = compact(v)
	}
	return out
}

// compact removes insignificant whitespace from an already-valid JSON value so
// stored values are canonical (and StringifyValue output is stable).
func compact(v json.RawMessage) json.RawMessage {
	var buf bytes.Buffer
	if err := json.Compact(&buf, v); err != nil {
		return v
	}
	return json.RawMessage(buf.Bytes())
}

// Namespace returns the part of a key before its first dot — the app/namespace
// that owns it (e.g. "tax" for "tax.category"). A key with no dot is its own
// namespace.
func Namespace(key string) string {
	if i := strings.IndexByte(key, '.'); i >= 0 {
		return key[:i]
	}
	return key
}

// StringifyValue renders a raw JSON value as a plain string for search matching
// and history diffs: a JSON string yields its unquoted text; any other value
// (number, boolean, null, object, array) yields its compact JSON. So a search
// like ext:tax.category=meal matches the stored "meal", and ext:x.score=88
// matches the stored 88.
func StringifyValue(v json.RawMessage) string {
	s := strings.TrimSpace(string(v))
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		var str string
		if err := json.Unmarshal(v, &str); err == nil {
			return str
		}
	}
	return s
}
