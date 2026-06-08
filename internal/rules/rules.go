// Package rules implements kasas's transaction auto-tagging rules: each rule
// pairs a condition — a query in the kasas search language (internal/search) —
// with an action — a set of labels and/or schema extensions to apply to every
// matching transaction.
//
// Like internal/search it is dependency-light (only the search, labels, and
// extensions packages, all standard-library-only) and knows nothing about the
// database or HTTP layers, so the same engine runs in the poller (auto-applying
// to each newly synced transaction) and behind the REST/MCP "run over existing
// transactions" endpoints. Callers adapt their stored rule rows into a [Rule],
// [Compile] them once, and ask [Apply]/[ApplyExtensions] to merge the matching
// rules' labels and extensions into a transaction.
package rules

import (
	"bytes"
	"encoding/json"
	"maps"

	"github.com/paulmeier/kasas/internal/extensions"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/search"
)

// Rule is one rule: if a transaction matches Query (the kasas search syntax)
// then Labels and Extensions are applied to it. Enabled rules auto-apply to
// newly synced transactions; any rule can be run on demand over existing ones.
type Rule struct {
	ID         int64
	Name       string
	Query      string
	Labels     map[string]string          // key:value pairs to apply on match (already normalized)
	Extensions map[string]json.RawMessage // namespaced metadata to apply on match (already normalized)
	Enabled    bool
}

// NewRule builds a Rule from stored primitive fields, decoding the JSON labels
// and extensions objects. It takes primitives rather than a database row so the
// package stays free of any database dependency.
func NewRule(id int64, name, query, labelsJSON, extensionsJSON string, enabled bool) Rule {
	return Rule{
		ID:         id,
		Name:       name,
		Query:      query,
		Labels:     labels.Decode(labelsJSON),
		Extensions: extensions.Decode(extensionsJSON),
		Enabled:    enabled,
	}
}

// Compiled is a Rule with its query parsed, ready to match. Compile once (per
// sync or per run) and reuse across many transactions.
type Compiled struct {
	Rule  Rule
	query *search.Query
}

// Compile parses a rule's query. A rule with an invalid query (which the API
// rejects on write) returns an error so callers can log and skip it.
func Compile(r Rule) (Compiled, error) {
	q, err := search.Parse(r.Query)
	if err != nil {
		return Compiled{}, err
	}
	return Compiled{Rule: r, query: q}, nil
}

// Matches reports whether the rule's condition matches the record.
func (c Compiled) Matches(rec search.Record) bool {
	return c.query.Match(rec)
}

// Apply merges the labels of every rule whose condition matches rec into a copy
// of current, with later rules winning on key conflicts (callers should pass
// rules in a deterministic order, e.g. by id). A matching rule is authoritative
// for its own keys, so it overwrites a different existing value. Apply does NOT
// filter by Rule.Enabled — the caller decides which rules to include (the poller
// compiles only enabled rules; an explicit "run this rule" may include a
// disabled one).
//
// The merged set is normalized so the per-transaction caps hold even when
// several rules contribute. Apply returns the new label set and whether it
// differs from current; when nothing changed it returns current unchanged so
// callers can skip the write.
func Apply(compiled []Compiled, rec search.Record, current map[string]string) (map[string]string, bool) {
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string]string{}
	}
	for _, c := range compiled {
		if !c.Matches(rec) {
			continue
		}
		for k, v := range c.Rule.Labels {
			merged[k] = v // overwrite: a matching rule is authoritative for its keys
		}
	}
	normalized := labels.Normalize(merged)
	if maps.Equal(normalized, current) {
		return current, false
	}
	return normalized, true
}

// ApplyExtensions is the schema-extensions counterpart to [Apply]: it merges the
// extensions of every rule whose condition matches rec into a copy of current,
// with later rules winning on key conflicts (callers should pass rules in a
// deterministic order, e.g. by id). A matching rule is authoritative for its own
// keys, so it overwrites a different existing value. Like Apply it does NOT filter
// by Rule.Enabled — the caller decides which rules to include.
//
// The merged set is normalized (re-enforcing the per-transaction caps and
// compacting values) and compared against current by raw JSON bytes; stored
// extension values are already compacted, so an unchanged set compares equal and
// the caller can skip the write.
func ApplyExtensions(compiled []Compiled, rec search.Record, current map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	merged := maps.Clone(current)
	if merged == nil {
		merged = map[string]json.RawMessage{}
	}
	for _, c := range compiled {
		if !c.Matches(rec) {
			continue
		}
		for k, v := range c.Rule.Extensions {
			merged[k] = v // overwrite: a matching rule is authoritative for its keys
		}
	}
	normalized := extensions.Normalize(merged)
	if maps.EqualFunc(normalized, current, func(a, b json.RawMessage) bool { return bytes.Equal(a, b) }) {
		return current, false
	}
	return normalized, true
}
