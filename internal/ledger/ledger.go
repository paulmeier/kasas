// Package ledger holds the shared transactional write-model for deleting ledger
// rows with full cleanup: stripping inbound relationship edges, removing history
// versions, and emitting the deletion events. It lives below the API so both the
// manual-entry handlers (internal/api) and the plugin manager (internal/plugins,
// for uninstall-time source purges, ADR 0005) can reuse one correct delete path
// without an import cycle. It depends only on db, events, and relationships.
package ledger

import (
	"context"
	"math"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/relationships"
)

// DeleteTransactionTx removes one already-fetched transaction within an open
// recorder transaction: it strips inbound relationship edges that other rows assert
// against it, deletes its history versions (transaction_versions has no FK cascade),
// deletes the row, and emits transaction.deleted carrying the last snapshot. The
// caller owns any read-only/source gate. Shared by the manual-delete handlers, the
// account-delete cascade, and the plugin-source purge, so every path leaves the same
// clean trail.
func DeleteTransactionTx(ctx context.Context, q db.Querier, rec *events.Recorder, txn db.Transaction) error {
	if err := stripInboundRelationships(ctx, q, rec, txn.ID); err != nil {
		return err
	}
	if _, err := q.DeleteTransactionVersionsByTransaction(ctx, txn.ID); err != nil {
		return err
	}
	if _, err := q.DeleteTransaction(ctx, txn.ID); err != nil {
		return err
	}
	return rec.Emit(ctx, q, events.TypeTransactionDeleted, events.EntityTransaction, txn.ID, events.TransactionSnapshot(txn))
}

// stripInboundRelationships removes every outbound edge that other transactions
// assert against targetID, emitting relationship.removed for each change.
// Relationships are stored only on the subject (outbound) side, so the edges
// pointing AT a deleted transaction live on other rows and would otherwise dangle.
func stripInboundRelationships(ctx context.Context, q db.Querier, rec *events.Recorder, targetID string) error {
	rows, err := q.ListRelatedTransactions(ctx)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.ID == targetID {
			continue // its own outbound edges vanish with the row
		}
		oldRels := relationships.Decode(row.Relationships)
		kept := make([]relationships.Relationship, 0, len(oldRels))
		for _, e := range oldRels {
			if e.Target != targetID {
				kept = append(kept, e)
			}
		}
		if len(kept) == len(oldRels) {
			continue // nothing targeted the deleted transaction
		}
		newRels := relationships.Normalize(kept)
		encoded, eerr := relationships.Encode(newRels)
		if eerr != nil {
			return eerr
		}
		if _, uerr := q.UpdateTransactionRelationships(ctx, db.UpdateTransactionRelationshipsParams{ID: row.ID, Relationships: encoded}); uerr != nil {
			return uerr
		}
		if derr := rec.EmitRelationshipDiff(ctx, q, row.ID, oldRels, newRels); derr != nil {
			return derr
		}
	}
	return nil
}

// PurgeResult reports how many accounts and transactions a PurgeSource removed.
type PurgeResult struct {
	Accounts     int
	Transactions int
}

// PurgeSource deletes every account stamped with the given source and all of their
// transactions, in a single recorder transaction, child-before-parent
// (transaction.deleted then account.deleted) so a replaying consumer tears down
// leaves first. It reuses DeleteTransactionTx so each removed row leaves the same
// clean trail (versions + inbound edges) as a manual delete.
//
// It is the uninstall-time cleanup for a source:provide plugin (ADR 0005): a plugin
// owns its rows through dedup, so removing the plugin removes them — the analog of a
// bank source's rows vanishing when you disconnect it. Plugin transactions always
// live in plugin-stamped accounts (the adapter namespaces both), so enumerating
// accounts by source covers every produced row.
func PurgeSource(ctx context.Context, store db.Store, emitter *events.Emitter, src string) (PurgeResult, error) {
	var res PurgeResult
	err := emitter.Record(ctx, store, func(q db.Querier, rec *events.Recorder) error {
		accounts, lerr := q.ListAccounts(ctx)
		if lerr != nil {
			return lerr
		}
		for _, acct := range accounts {
			if acct.Source != src {
				continue
			}
			children, cerr := q.ListTransactionsByAccount(ctx, db.ListTransactionsByAccountParams{
				AccountID: acct.ID, Since: 0, Until: 0, RowLimit: math.MaxInt32, RowOffset: 0,
			})
			if cerr != nil {
				return cerr
			}
			for _, child := range children {
				if derr := DeleteTransactionTx(ctx, q, rec, child); derr != nil {
					return derr
				}
				res.Transactions++
			}
			if _, derr := q.DeleteAccount(ctx, acct.ID); derr != nil {
				return derr
			}
			if eerr := rec.Emit(ctx, q, events.TypeAccountDeleted, events.EntityAccount, acct.ID, events.AccountSnapshot(acct)); eerr != nil {
				return eerr
			}
			res.Accounts++
		}
		return nil
	})
	if err != nil {
		return PurgeResult{}, err
	}
	return res, nil
}
