package poller

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/extensions"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/rules"
	"github.com/paulmeier/kasas/internal/search"
	"github.com/paulmeier/kasas/internal/source"
)

// persister writes a source.ImportBatch into the database: the transactional
// upsert of accounts and transactions, idempotent dedup, event recording, rule
// auto-labeling, and history baselines. It is the single, shared ingestion-persist
// path — every source's batch (SimpleFIN, CSV, on-chain, and plugin producers
// alike) lands through here, so the load-bearing guarantees are written once. A
// Poller owns one for its scheduled syncs; a one-off batch (e.g. a plugin reactive
// producer) can use one directly without a Poller, Source, or schedule.
type persister struct {
	store   db.Store
	emitter *events.Emitter
	logger  *slog.Logger
}

// newPersister constructs a persister over the given store, emitter (nil disables
// event recording), and logger.
func newPersister(store db.Store, emitter *events.Emitter, logger *slog.Logger) *persister {
	if logger == nil {
		logger = slog.Default()
	}
	return &persister{store: store, emitter: emitter, logger: logger}
}

// Persist writes accounts and transactions in a single transaction, recording an
// event for each meaningful change (account.created/updated, transaction.created/
// updated, and label.applied from auto-labeling) so a change and its event commit
// atomically. With the emitter disabled it is a plain transaction with no events.
// Each transaction is stamped with the batch's Source as its provenance.
func (ps *persister) Persist(ctx context.Context, batch *source.ImportBatch, syncedAt int64) (SyncResult, error) {
	var res SyncResult

	err := ps.emitter.Record(ctx, ps.store, func(q db.Querier, rec *events.Recorder) error {
		// Compile the enabled labeling rules once per sync; matching rules are
		// applied to each brand-new transaction below.
		compiledRules, err := ps.loadEnabledRules(ctx, q)
		if err != nil {
			return err
		}

		for _, acct := range batch.Accounts {
			org := acct.Org
			if err := q.UpsertOrganization(ctx, db.UpsertOrganizationParams{
				ID:      org.ID,
				Domain:  org.Domain,
				Name:    org.Name,
				SfinUrl: org.URL,
			}); err != nil {
				return fmt.Errorf("upsert organization %q: %w", org.ID, err)
			}

			// Learn whether this account is new (and otherwise what changed) before
			// the upsert, so we can emit account.created vs account.updated.
			prevAcct, getErr := q.GetAccount(ctx, acct.ExternalID)
			isNewAccount := errors.Is(getErr, sql.ErrNoRows)
			if getErr != nil && !isNewAccount {
				return fmt.Errorf("get account %q: %w", acct.ExternalID, getErr)
			}

			newAcct := db.Account{
				ID:          acct.ExternalID,
				OrgID:       org.ID,
				Name:        acct.Name,
				Currency:    acct.Currency,
				Balance:     acct.Balance,
				BalanceDate: acct.BalanceDate,
				SyncedAt:    syncedAt,
				Source:      batch.Source,
			}
			if err := q.UpsertAccount(ctx, db.UpsertAccountParams(newAcct)); err != nil {
				return fmt.Errorf("upsert account %q: %w", acct.ExternalID, err)
			}
			res.Accounts++

			switch {
			case isNewAccount:
				if err := rec.Emit(ctx, q, events.TypeAccountCreated, events.EntityAccount, acct.ExternalID, events.AccountSnapshot(newAcct)); err != nil {
					return err
				}
			case accountChanged(prevAcct, newAcct):
				if err := rec.Emit(ctx, q, events.TypeAccountUpdated, events.EntityAccount, acct.ExternalID, events.AccountSnapshot(newAcct)); err != nil {
					return err
				}
			}

			for _, t := range acct.Transactions {
				// Insert is ON CONFLICT DO NOTHING, so n==1 means a brand-new
				// transaction and n==0 means it already exists. For existing rows we
				// refresh the bridge-owned fields (so a pending charge that posts, or
				// a corrected amount, flows in) via UpdateTransactionFromSync, which
				// deliberately leaves the labels column untouched — user labels are
				// never clobbered by a sync.
				n, err := q.InsertTransaction(ctx, db.InsertTransactionParams{
					ID:          t.ExternalID,
					AccountID:   acct.ExternalID,
					Amount:      t.Amount,
					Pending:     boolToInt(t.Pending),
					Date:        t.Date,
					Description: t.Description,
					Payee:       t.Payee,
					Memo:        t.Memo,
					SyncedAt:    syncedAt,
					Source:      batch.Source,
				})
				if err != nil {
					return fmt.Errorf("insert transaction %q: %w", t.ExternalID, err)
				}
				if n > 0 {
					res.NewTransactions += int(n)
					// A brand-new row starts with no labels or extensions ('{}').
					created := newTransactionRow(acct, t, syncedAt, "{}", "{}")
					if err := rec.Emit(ctx, q, events.TypeTransactionCreated, events.EntityTransaction, t.ExternalID, events.TransactionSnapshot(created)); err != nil {
						return err
					}
					// Apply any matching rules (emitting label.applied / extension.set
					// per applied key). Re-synced (existing) rows are left alone so a
					// sync never clobbers labels or extensions.
					labelsJSON, extJSON, modified, err := ps.applyRulesToNewTxn(ctx, q, rec, compiledRules, acct, t, syncedAt)
					if err != nil {
						return fmt.Errorf("apply rules to transaction %q: %w", t.ExternalID, err)
					}
					if modified {
						res.AutoLabeled++
					}
					// Record v1 of the transaction's history, folding in any birth
					// labels/extensions the rules applied. (The transaction.created event
					// above fires empty; the version captures the settled state.)
					settled := newTransactionRow(acct, t, syncedAt, labelsJSON, extJSON)
					if err := rec.Version(ctx, q, t.ExternalID, events.TransactionSnapshot(settled), events.ChangeImported); err != nil {
						return fmt.Errorf("record imported version for %q: %w", t.ExternalID, err)
					}
					continue
				}
				// Existing row: fetch it first so we can tell whether this refresh
				// actually changed a bridge field, and thus whether to emit.
				prevTxn, err := q.GetTransaction(ctx, t.ExternalID)
				if err != nil {
					return fmt.Errorf("get transaction %q: %w", t.ExternalID, err)
				}
				if _, err := q.UpdateTransactionFromSync(ctx, db.UpdateTransactionFromSyncParams{
					ID:          t.ExternalID,
					AccountID:   acct.ExternalID,
					Amount:      t.Amount,
					Pending:     boolToInt(t.Pending),
					Date:        t.Date,
					Description: t.Description,
					Payee:       t.Payee,
					Memo:        t.Memo,
					SyncedAt:    syncedAt,
				}); err != nil {
					return fmt.Errorf("refresh transaction %q: %w", t.ExternalID, err)
				}
				res.UpdatedTransactions++
				// The refresh preserves existing labels and extensions; carry them in the snapshot.
				updated := newTransactionRow(acct, t, syncedAt, prevTxn.Labels, prevTxn.Extensions)
				if transactionBridgeChanged(prevTxn, updated) {
					if err := rec.Emit(ctx, q, events.TypeTransactionUpdated, events.EntityTransaction, t.ExternalID, events.TransactionSnapshot(updated)); err != nil {
						return err
					}
					// Append a synced version, synthesizing a v1 baseline from the prior
					// state if this transaction predates history.
					if err := rec.VersionChange(ctx, q, t.ExternalID, events.TransactionSnapshot(prevTxn), events.TransactionSnapshot(updated), events.ChangeSynced); err != nil {
						return err
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return res, nil
}

// loadEnabledRules reads the enabled labeling rules and compiles each one's
// query. A rule whose stored query fails to parse (the API rejects invalid
// queries on write, so this is defensive) is logged and skipped rather than
// failing the whole sync.
func (ps *persister) loadEnabledRules(ctx context.Context, q db.Querier) ([]rules.Compiled, error) {
	rows, err := q.ListEnabledRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled rules: %w", err)
	}
	compiled := make([]rules.Compiled, 0, len(rows))
	for _, r := range rows {
		c, cerr := rules.Compile(rules.NewRule(r.ID, r.Name, r.Query, r.Labels, r.Extensions, r.Enabled != 0))
		if cerr != nil {
			ps.logger.Warn("skipping rule with invalid query", "rule_id", r.ID, "name", r.Name, "error", cerr)
			continue
		}
		compiled = append(compiled, c)
	}
	return compiled, nil
}

// applyRulesToNewTxn applies the compiled rules to one just-inserted transaction
// (whose labels and extensions are still empty), writing the merged label and
// extension sets when any rule matched and emitting a label.applied / extension.set
// event per applied key (the diff is against an empty set, so every applied key is
// "added"). It returns the final labels and extensions JSON — the new-row default
// '{}' when nothing matched, so the caller always has valid snapshot inputs — and
// whether the transaction was modified.
func (ps *persister) applyRulesToNewTxn(ctx context.Context, q db.Querier, rec *events.Recorder, compiled []rules.Compiled, acct source.ImportAccount, t source.ImportTxn, syncedAt int64) (labelsJSON, extJSON string, modified bool, err error) {
	labelsJSON, extJSON = "{}", "{}"
	if len(compiled) == 0 {
		return labelsJSON, extJSON, false, nil
	}
	rec0 := newSearchRecord(acct, t, syncedAt)

	if merged, changed := rules.Apply(compiled, rec0, map[string]string{}); changed {
		encoded, eerr := labels.Encode(merged)
		if eerr != nil {
			return "{}", "{}", false, eerr
		}
		if _, eerr := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: t.ExternalID, Labels: encoded}); eerr != nil {
			return "{}", "{}", false, eerr
		}
		if eerr := rec.EmitLabelDiff(ctx, q, t.ExternalID, map[string]string{}, merged); eerr != nil {
			return "{}", "{}", false, eerr
		}
		labelsJSON, modified = encoded, true
	}

	if merged, changed := rules.ApplyExtensions(compiled, rec0, map[string]json.RawMessage{}); changed {
		encoded, eerr := extensions.Encode(merged)
		if eerr != nil {
			return "{}", "{}", false, eerr
		}
		if _, eerr := q.UpdateTransactionExtensions(ctx, db.UpdateTransactionExtensionsParams{ID: t.ExternalID, Extensions: encoded}); eerr != nil {
			return "{}", "{}", false, eerr
		}
		if eerr := rec.EmitExtensionDiff(ctx, q, t.ExternalID, map[string]json.RawMessage{}, merged); eerr != nil {
			return "{}", "{}", false, eerr
		}
		extJSON, modified = encoded, true
	}
	return labelsJSON, extJSON, modified, nil
}

// accountChanged reports whether any consumer-meaningful field of an account
// differs (synced_at, which changes every sync, is intentionally ignored).
func accountChanged(prev, next db.Account) bool {
	return prev.Name != next.Name ||
		prev.Currency != next.Currency ||
		prev.Balance != next.Balance ||
		prev.BalanceDate != next.BalanceDate ||
		prev.OrgID != next.OrgID
}

// transactionBridgeChanged reports whether a re-sync actually changed a
// bridge-owned field. synced_at (always changes) and labels (never touched by a
// sync) are excluded, so a no-op refresh emits no transaction.updated event.
func transactionBridgeChanged(prev, next db.Transaction) bool {
	return prev.AccountID != next.AccountID ||
		prev.Amount != next.Amount ||
		prev.Pending != next.Pending ||
		prev.Date != next.Date ||
		prev.Description != next.Description ||
		prev.Payee != next.Payee ||
		prev.Memo != next.Memo
}

// newTransactionRow assembles a db.Transaction from a normalized import transaction
// for use as an event snapshot, carrying the given labels and extensions JSON (the
// new-row default '{}' for an insert, or the existing values for a refresh). The
// source/provenance stamp is omitted because event and version snapshots do not
// surface it.
func newTransactionRow(acct source.ImportAccount, t source.ImportTxn, syncedAt int64, labelsJSON, extJSON string) db.Transaction {
	return db.Transaction{
		ID:          t.ExternalID,
		AccountID:   acct.ExternalID,
		Amount:      t.Amount,
		Pending:     boolToInt(t.Pending),
		Date:        t.Date,
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		SyncedAt:    syncedAt,
		Labels:      labelsJSON,
		Extensions:  extJSON,
	}
}

// newSearchRecord adapts a normalized import account + transaction into the search
// engine's neutral Record so rules can match on any field. A freshly inserted
// transaction carries no labels yet.
func newSearchRecord(acct source.ImportAccount, t source.ImportTxn, syncedAt int64) search.Record {
	return search.Record{
		ID:          t.ExternalID,
		AccountID:   acct.ExternalID,
		AccountName: acct.Name,
		Amount:      parseAmount(t.Amount),
		AmountRaw:   t.Amount,
		Pending:     t.Pending,
		Date:        time.Unix(t.Date, 0).UTC(),
		Description: t.Description,
		Payee:       t.Payee,
		Memo:        t.Memo,
		Labels:      map[string]string{},
		SyncedAt:    time.Unix(syncedAt, 0).UTC(),
	}
}

// parseAmount parses a decimal amount string into a float for amount: rule
// comparisons, tolerating thousands separators. Unparseable values become 0,
// matching kasas's lenient amount handling elsewhere.
func parseAmount(s string) float64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
