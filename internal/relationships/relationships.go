// Package relationships is the pure model for kasas transaction relationships:
// explicit directed edges from one transaction to another, stored as a JSON array
// per transaction, e.g. '[{"kind":"refund_of","target":"txn_123"}]'.
//
// Each edge is asserted OUTBOUND from the transaction that owns the array (the
// "from"/subject side). A transaction's full neighborhood is its own outbound
// edges plus the inbound edges of every other transaction whose target is this
// one; the inbound direction is derived by scanning (see ListRelatedTransactions),
// not stored, so an edge has a single home and can never disagree with itself.
//
// This is parallel to labels (strict key->value strings, see internal/labels) and
// extensions (namespaced key->arbitrary-JSON, see internal/extensions): all three
// are per-transaction JSON the poller never touches. Unlike extensions, an edge
// has a FIXED shape — two plain strings — so there is no json.RawMessage/any split.
//
// kind is a freeform but normalized verb (a lowercase identifier such as
// refund_of, transfer_to, withholding_for); kasas is a substrate for many apps and
// does not hard-enumerate the vocabulary. target is an opaque transaction id, kept
// verbatim (case-sensitive).
//
// It is dependency-light (standard library only) so it can be shared by every
// surface: the REST API and MCP writers, the event emitter, the search matcher,
// and the WASM dashboard. It knows nothing about the database or HTTP layers.
package relationships

import (
	"encoding/json"
	"sort"
	"strings"
)

const (
	// MaxKindLen caps a relationship kind length (runes) after normalization.
	MaxKindLen = 64
	// MaxCount caps the number of outbound edges per transaction.
	MaxCount = 100

	// DirectionOutbound marks an edge asserted FROM the focal transaction.
	DirectionOutbound = "outbound"
	// DirectionInbound marks an edge asserted BY another transaction whose target
	// is the focal transaction.
	DirectionInbound = "inbound"
)

// Relationship is one directed edge: the owning transaction relates to Target by
// Kind (e.g. {Kind:"refund_of", Target:"txn_123"} means "this transaction is a
// refund of txn_123").
type Relationship struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
}

// Key is a stable identity for an edge (kind + target), used for de-duplication
// and diffing. The NUL separator can't appear in a normalized kind or a target.
func (r Relationship) Key() string { return r.Kind + "\x00" + r.Target }

// Decode parses a stored JSON relationships array into a slice. It never returns
// nil (so callers and DTOs marshal to [] rather than null) and tolerates a
// malformed or non-array value by returning an empty slice.
func Decode(stored string) []Relationship {
	out := []Relationship{}
	if stored == "" {
		return out
	}
	if err := json.Unmarshal([]byte(stored), &out); err != nil {
		return []Relationship{}
	}
	if out == nil {
		return []Relationship{}
	}
	return out
}

// Encode marshals a relationships slice into the stored JSON array form. A nil
// slice encodes as "[]" (not "null").
func Encode(rels []Relationship) (string, error) {
	if rels == nil {
		rels = []Relationship{}
	}
	b, err := json.Marshal(rels)
	return string(b), err
}

// NormalizeKind canonicalizes a relationship kind to a lowercase identifier:
// trimmed and lowercased, with runs of separators (space, '-', '.', '_')
// collapsed to a single underscore, all other characters dropped, leading/trailing
// underscores removed, and the result rune-capped. So "Refund Of", "refund-of" and
// "refund_of" all normalize to "refund_of". Returns "" for an empty or
// fully-stripped kind.
func NormalizeKind(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	pendingSep := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if pendingSep && b.Len() > 0 {
				b.WriteByte('_')
			}
			pendingSep = false
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '.' || r == '_':
			pendingSep = true
		default:
			// drop anything else
		}
	}
	out := b.String()
	if r := []rune(out); len(r) > MaxKindLen {
		out = strings.TrimRight(string(r[:MaxKindLen]), "_")
	}
	return out
}

// Normalize normalizes every edge's kind, trims its target, drops edges with an
// empty kind or target, de-duplicates by (kind, target), caps the count, and sorts
// deterministically (kind, then target) so stored arrays are canonical and diffs
// are stable. The result is never nil. Self-edges (target == owner) are NOT
// rejected here — that needs the owner id and is enforced by the API layer.
func Normalize(in []Relationship) []Relationship {
	out := make([]Relationship, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, r := range in {
		e := Relationship{Kind: NormalizeKind(r.Kind), Target: strings.TrimSpace(r.Target)}
		if e.Kind == "" || e.Target == "" {
			continue
		}
		if _, dup := seen[e.Key()]; dup {
			continue
		}
		if len(out) >= MaxCount {
			continue
		}
		seen[e.Key()] = struct{}{}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Target < out[j].Target
	})
	return out
}
