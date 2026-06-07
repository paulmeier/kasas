// Package provenance assembles a transaction's lineage — where it came from and
// how it reached its current state — into a single read-only view. Every field is
// derived from data the ledger already keeps (the transaction row, its account's
// organization, and its version history) except source, which cannot be
// reconstructed from a transaction's contents and is recorded at ingest, then
// supplied by the caller off the row. It is a pure projection: it stores nothing
// and emits no events, and is shared verbatim by the REST handler and the MCP tool.
package provenance

import (
	"sort"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/events"
)

// Provenance is the lineage of one transaction: its origin identity, when it was
// first and last seen, and the ordered transformations it has undergone. It is the
// body of GET /api/v1/transactions/{id}/provenance and the get_transaction_provenance
// MCP tool.
type Provenance struct {
	TransactionID       string           `json:"transaction_id"`
	Source              string           `json:"source"`
	SourceTransactionID string           `json:"source_transaction_id"`
	AccountID           string           `json:"account_id"`
	Institution         string           `json:"institution,omitempty"`
	ImportedAt          time.Time        `json:"imported_at"`
	LastSeen            time.Time        `json:"last_seen"`
	Transformations     []Transformation `json:"transformations"`
}

// Transformation is one entry in a transaction's lineage: a change of a given kind
// at a point in time, with a compact human-readable summary. Kinds mirror the
// history change kinds (imported, synced, labeled, extended).
type Transformation struct {
	Kind       string    `json:"kind"`
	OccurredAt time.Time `json:"occurred_at"`
	Summary    string    `json:"summary"`
}

// Version is one stored history snapshot fed into Build: the change kind, when it
// occurred, and the full transaction payload at that point. Callers pass them
// oldest-first (the order ListTransactionVersions returns).
type Version struct {
	Kind       string
	OccurredAt time.Time
	Snapshot   events.TransactionPayload
}

// Input is everything Build needs, all sourced from existing storage by the caller:
// the transaction's id/account/source off the row, the institution from the org
// join (optional), last_seen from synced_at, and the version history.
type Input struct {
	TransactionID string
	Source        string
	AccountID     string
	Institution   string
	LastSeen      time.Time
	Versions      []Version
}

// Build assembles a Provenance from already-loaded inputs. It is pure: no I/O, no
// clock. imported_at is the timestamp of the earliest recorded version; for a
// transaction that predates history and has not changed since (no versions), it
// falls back to last_seen — the best available signal. Each transformation's
// summary is computed from the diff against the previous snapshot, except the
// initial import, which reads "imported from <source>".
func Build(in Input) Provenance {
	p := Provenance{
		TransactionID:       in.TransactionID,
		Source:              in.Source,
		SourceTransactionID: in.TransactionID,
		AccountID:           in.AccountID,
		Institution:         in.Institution,
		LastSeen:            in.LastSeen,
		Transformations:     make([]Transformation, 0, len(in.Versions)),
	}
	if len(in.Versions) > 0 {
		p.ImportedAt = in.Versions[0].OccurredAt
	} else {
		p.ImportedAt = in.LastSeen
	}
	var prev events.TransactionPayload
	for _, v := range in.Versions {
		p.Transformations = append(p.Transformations, Transformation{
			Kind:       v.Kind,
			OccurredAt: v.OccurredAt,
			Summary:    summarize(in.Source, v.Kind, events.DiffSnapshots(prev, v.Snapshot)),
		})
		prev = v.Snapshot
	}
	return p
}

// summarize renders a compact, deterministic one-line description of a single
// transformation. The initial import is described by its source rather than its
// (against-zero) field diff; every other kind is described by what actually
// changed. An empty diff falls back to the bare kind.
func summarize(source, kind string, d events.VersionDiff) string {
	if kind == events.ChangeImported {
		return "imported from " + source
	}
	var parts []string
	for _, f := range d.Fields {
		parts = append(parts, f.Field+" "+f.From+" → "+f.To)
	}
	for _, k := range sortedKeys(d.LabelsAdded) {
		parts = append(parts, "+"+k+":"+d.LabelsAdded[k])
	}
	for _, k := range sortedKeys(d.LabelsChanged) {
		c := d.LabelsChanged[k]
		parts = append(parts, "~"+k+":"+c[0]+"→"+c[1])
	}
	for _, k := range sortedKeys(d.LabelsRemoved) {
		parts = append(parts, "-"+k+":"+d.LabelsRemoved[k])
	}
	for _, k := range sortedKeys(d.ExtensionsAdded) {
		parts = append(parts, "+ext:"+k)
	}
	for _, k := range sortedKeys(d.ExtensionsChanged) {
		parts = append(parts, "~ext:"+k)
	}
	for _, k := range sortedKeys(d.ExtensionsRemoved) {
		parts = append(parts, "-ext:"+k)
	}
	if len(parts) == 0 {
		return kind
	}
	return strings.Join(parts, "; ")
}

// sortedKeys returns a map's keys in ascending order, so a summary built from the
// (otherwise unordered) diff maps is deterministic.
func sortedKeys[V any](m map[string]V) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
