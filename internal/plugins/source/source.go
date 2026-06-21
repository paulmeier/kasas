// Package pluginsource adapts a source:provide plugin (ADR 0005) into a
// source.Source the ingestion engine drives like any built-in source. The adapter
// is host code: it is the single point that enforces the two invariants a plugin
// cannot be trusted with — it namespaces every id the plugin returns (so a plugin
// row can never collide with another source's, since dedup is by the global
// transactions.id) and it stamps the provenance Source as plugin:<name> (so a
// plugin can never masquerade as simplefin or forge another source's rows). The
// plugin only ever returns data; the engine writes it.
//
// It lives in its own package rather than internal/plugins so the manager stays
// free of any poller dependency: the adapter depends on internal/source and a
// narrow Producer seam (satisfied by *plugins.Manager), and the wiring in main
// constructs the adapter + a poller and registers it with the engine.
package pluginsource

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/paulmeier/kasas/internal/plugins"
	"github.com/paulmeier/kasas/internal/source"
)

// Producer is the seam the adapter uses to run a plugin's OnFetch hook. It is
// satisfied by *plugins.Manager, which serializes the call on the plugin's
// non-reentrant VM worker and enforces the source:provide capability.
type Producer interface {
	Produce(ctx context.Context, name string, hook plugins.Hook, payload json.RawMessage) (json.RawMessage, error)
}

// Source is the plugin-backed ingestion source. Its type (the engine key) and the
// provenance stamp are both plugin:<name>, derived from the registered plugin name
// — one identity the plugin cannot forge.
type Source struct {
	name     string // plugin name; stamp/key = "plugin:" + name
	manifest plugins.Manifest
	producer Producer
}

var (
	_ source.Source = (*Source)(nil)
	_ source.Puller = (*Source)(nil)
)

// New builds the adapter for a plugin. The manifest must declare a [source] block
// (the caller registers a source only for source:provide plugins).
func New(name string, manifest plugins.Manifest, producer Producer) *Source {
	return &Source{name: name, manifest: manifest, producer: producer}
}

// SourceType returns the engine key / provenance stamp for a plugin source. It
// delegates to plugins.SourceType, the canonical definition.
func SourceType(name string) string { return plugins.SourceType(name) }

// Descriptor reports the source's metadata. Type (and the provenance stamp) is
// plugin:<name>; the manifest's [source].type is the human Title; the [net].allow
// hosts surface as Egress so a plugin source's network reach is as visible as any
// other source's.
func (s *Source) Descriptor() source.Descriptor {
	var egress []string
	if s.manifest.Net != nil {
		egress = s.manifest.Net.Allow
	}
	title := s.name
	if s.manifest.Source != nil && s.manifest.Source.Type != "" {
		title = s.manifest.Source.Type
	}
	return source.Descriptor{
		Type:      SourceType(s.name),
		Archetype: source.ArchetypePull,
		Title:     title,
		Egress:    egress,
	}
}

// fetchRequest is the JSON payload handed to the plugin's OnFetch hook.
type fetchRequest struct {
	Since  int64  `json:"since"`  // unix seconds; 0 means "everything"
	Cursor string `json:"cursor"` // opaque resume token from the prior fetch (empty today)
}

// Fetch runs the plugin's OnFetch hook and returns the normalized batch. The
// returned ids are namespaced by plugin and the provenance Source is forced to
// plugin:<name> — the plugin's own `source` field is a human label and is ignored.
func (s *Source) Fetch(ctx context.Context, since time.Time, cursor string) (*source.ImportBatch, error) {
	var sinceUnix int64
	if !since.IsZero() {
		sinceUnix = since.Unix()
	}
	payload, err := json.Marshal(fetchRequest{Since: sinceUnix, Cursor: cursor})
	if err != nil {
		return nil, fmt.Errorf("encode fetch request: %w", err)
	}

	raw, err := s.producer.Produce(ctx, s.name, plugins.HookFetch, payload)
	if err != nil {
		return nil, err
	}
	return s.normalize(raw)
}

// wireBatch / wireAccount / wireTxn decode the plugin's returned batch leniently:
// numeric fields are float64 because some runtimes (gopher-lua) serialize integers
// in exponential form (1.7e+09), which will not unmarshal into an int64.
type wireBatch struct {
	Source   string        `json:"source"`
	Cursor   string        `json:"cursor"`
	Accounts []wireAccount `json:"accounts"`
}

type wireOrg struct {
	ID     string `json:"id"`
	Domain string `json:"domain"`
	Name   string `json:"name"`
	URL    string `json:"url"`
}

type wireAccount struct {
	ExternalID   string    `json:"external_id"`
	Org          wireOrg   `json:"org"`
	Name         string    `json:"name"`
	Currency     string    `json:"currency"`
	Balance      string    `json:"balance"`
	BalanceDate  float64   `json:"balance_date"`
	Transactions []wireTxn `json:"transactions"`
}

type wireTxn struct {
	ExternalID  string  `json:"external_id"`
	Amount      string  `json:"amount"`
	Date        float64 `json:"date"`
	Description string  `json:"description"`
	Payee       string  `json:"payee"`
	Memo        string  `json:"memo"`
	Pending     bool    `json:"pending"`
}

// normalize decodes the plugin's batch, enforces the host-owned invariants
// (namespaced ids, forced provenance), and validates that every account and
// transaction carries a non-empty id (so a buggy plugin fails loudly rather than
// silently collapsing rows into one namespaced key).
func (s *Source) normalize(raw json.RawMessage) (*source.ImportBatch, error) {
	var wb wireBatch
	if err := json.Unmarshal(raw, &wb); err != nil {
		return nil, fmt.Errorf("decode plugin batch: %w", err)
	}

	ns := SourceType(s.name) + ":" // "plugin:<name>:"
	batch := &source.ImportBatch{
		Source: SourceType(s.name), // host-owned provenance stamp; ignore wb.Source
		Cursor: wb.Cursor,
	}
	for ai, wa := range wb.Accounts {
		if wa.ExternalID == "" {
			return nil, fmt.Errorf("plugin batch account %d has an empty external_id", ai)
		}
		orgID := SourceType(s.name) // a shared org for accounts that declare none
		if wa.Org.ID != "" {
			orgID = ns + wa.Org.ID
		}
		acct := source.ImportAccount{
			ExternalID: ns + wa.ExternalID,
			Org: source.ImportOrg{
				ID:     orgID,
				Domain: wa.Org.Domain,
				Name:   wa.Org.Name,
				URL:    wa.Org.URL,
			},
			Name:        wa.Name,
			Currency:    wa.Currency,
			Balance:     wa.Balance,
			BalanceDate: int64(math.Round(wa.BalanceDate)),
		}
		for ti, wt := range wa.Transactions {
			if wt.ExternalID == "" {
				return nil, fmt.Errorf("plugin batch account %q transaction %d has an empty external_id", wa.ExternalID, ti)
			}
			acct.Transactions = append(acct.Transactions, source.ImportTxn{
				ExternalID:  ns + wt.ExternalID,
				Amount:      wt.Amount,
				Date:        int64(math.Round(wt.Date)),
				Description: wt.Description,
				Payee:       wt.Payee,
				Memo:        wt.Memo,
				Pending:     wt.Pending,
			})
		}
		batch.Accounts = append(batch.Accounts, acct)
	}
	return batch, nil
}
