// Package poller drives an ingestion source on a schedule and persists its data
// to the local database. It owns the generic ingestion engine — scheduling, the
// transactional persist, idempotent upserts, event/rule/history recording — while
// a [source.Source] (e.g. the SimpleFIN bridge) owns talking to one provider and
// shaping its data into a neutral source.ImportBatch.
package poller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/rules"
	"github.com/paulmeier/kasas/internal/search"
	"github.com/paulmeier/kasas/internal/source"
)

var (
	syncTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "kasas_sync_total",
		Help: "Total number of sync runs, labelled by outcome.",
	}, []string{"status"})
	syncDuration = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "kasas_sync_duration_seconds",
		Help:    "Duration of sync runs in seconds.",
		Buckets: prometheus.DefBuckets,
	})
	txInserted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kasas_transactions_inserted_total",
		Help: "Total number of new transactions inserted across all syncs.",
	})
	txUpdated = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kasas_transactions_updated_total",
		Help: "Total number of existing transactions refreshed (bridge fields, not labels) across all syncs.",
	})
	rulesApplied = promauto.NewCounter(prometheus.CounterOpts{
		Name: "kasas_rules_applied_total",
		Help: "Total number of newly-synced transactions auto-labeled by a matching rule.",
	})
	lastSuccess = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "kasas_last_successful_sync_timestamp_seconds",
		Help: "Unix timestamp of the last successful sync.",
	})
	accountsGauge = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "kasas_accounts",
		Help: "Number of accounts observed in the most recent sync.",
	})
)

// Options configures a Poller.
type Options struct {
	Store        db.Store
	Source       source.Source // the ingestion source; must implement source.Puller to sync
	Logger       *slog.Logger
	Emitter      *events.Emitter // nil disables event recording (events.enabled=false)
	Interval     time.Duration
	LookbackDays int
}

// Poller drives an ingestion source's sync loop and persists the results.
type Poller struct {
	source       source.Source
	puller       source.Puller       // source.(Puller); nil if the source cannot be polled
	cred         source.Credentialed // source.(Credentialed); nil if it has no runtime credential
	store        db.Store
	logger       *slog.Logger
	emitter      *events.Emitter
	interval     time.Duration
	lookbackDays int

	mu    sync.Mutex // serializes syncs (background + on-demand)
	sched gocron.Scheduler
}

// New constructs a Poller. The source's optional capabilities (polling, runtime
// credentials) are detected once here by type assertion.
func New(opts Options) *Poller {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	p := &Poller{
		source:       opts.Source,
		store:        opts.Store,
		logger:       logger,
		emitter:      opts.Emitter,
		interval:     opts.Interval,
		lookbackDays: opts.LookbackDays,
	}
	p.puller, _ = opts.Source.(source.Puller)
	p.cred, _ = opts.Source.(source.Credentialed)
	return p
}

// SyncResult summarizes a completed sync.
type SyncResult struct {
	Accounts            int           `json:"accounts"`
	NewTransactions     int           `json:"new_transactions"`
	UpdatedTransactions int           `json:"updated_transactions"`
	AutoLabeled         int           `json:"auto_labeled"` // new transactions a rule labeled
	Duration            time.Duration `json:"duration"`
}

// Start schedules recurring syncs. Call Stop to release resources.
func (p *Poller) Start(ctx context.Context) error {
	s, err := gocron.NewScheduler()
	if err != nil {
		return fmt.Errorf("create scheduler: %w", err)
	}
	_, err = s.NewJob(
		gocron.DurationJob(p.interval),
		gocron.NewTask(func() {
			if _, err := p.Sync(context.Background()); err != nil {
				p.logger.Error("scheduled sync failed", "error", err)
			}
		}),
	)
	if err != nil {
		return fmt.Errorf("schedule sync job: %w", err)
	}
	p.sched = s
	s.Start()
	p.logger.Info("poller started", "interval", p.interval.String())
	return nil
}

// Stop halts the scheduler.
func (p *Poller) Stop(ctx context.Context) error {
	if p.sched == nil {
		return nil
	}
	return p.sched.Shutdown()
}

// Sync performs a single sync. It is safe for concurrent callers; runs are
// serialized so the background schedule and on-demand API triggers never
// overlap.
func (p *Poller) Sync(ctx context.Context) (result SyncResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	start := time.Now()
	entry, logErr := p.store.CreateSyncLog(ctx, db.CreateSyncLogParams{
		StartedAt: start.Unix(),
		Status:    "running",
	})
	if logErr != nil {
		return SyncResult{}, fmt.Errorf("create sync log: %w", logErr)
	}

	defer func() {
		status := "success"
		var syncErr sql.NullString
		if err != nil {
			status = "error"
			syncErr = sql.NullString{String: err.Error(), Valid: true}
		}
		if cErr := p.store.CompleteSyncLog(ctx, db.CompleteSyncLogParams{
			CompletedAt: sql.NullInt64{Int64: time.Now().Unix(), Valid: true},
			Status:      status,
			Error:       syncErr,
			ID:          entry.ID,
		}); cErr != nil {
			p.logger.Error("failed to finalize sync log", "error", cErr, "sync_id", entry.ID)
		}

		syncTotal.WithLabelValues(status).Inc()
		syncDuration.Observe(time.Since(start).Seconds())
		if err == nil {
			lastSuccess.Set(float64(time.Now().Unix()))
		}
	}()

	if p.puller == nil {
		return SyncResult{}, errors.New("configured ingestion source does not support polling")
	}

	var since time.Time
	if p.lookbackDays > 0 {
		since = start.AddDate(0, 0, -p.lookbackDays)
	}

	batch, err := p.puller.Fetch(ctx, since, "")
	if err != nil {
		return SyncResult{}, err
	}

	result, err = p.persist(ctx, batch, time.Now().Unix())
	if err != nil {
		return SyncResult{}, err
	}
	result.Duration = time.Since(start)
	p.emitSyncCompleted(ctx, entry.ID, result)

	accountsGauge.Set(float64(result.Accounts))
	txInserted.Add(float64(result.NewTransactions))
	txUpdated.Add(float64(result.UpdatedTransactions))
	rulesApplied.Add(float64(result.AutoLabeled))
	p.logger.Info("sync complete",
		"source", batch.Source,
		"accounts", result.Accounts,
		"new_transactions", result.NewTransactions,
		"updated_transactions", result.UpdatedTransactions,
		"auto_labeled", result.AutoLabeled,
		"duration", result.Duration.String(),
	)
	return result, nil
}

// persist writes accounts and transactions in a single transaction, recording an
// event for each meaningful change (account.created/updated, transaction.created/
// updated, and label.applied from auto-labeling) so a change and its event commit
// atomically. With the emitter disabled it is a plain transaction with no events.
// Each transaction is stamped with the batch's Source as its provenance.
func (p *Poller) persist(ctx context.Context, batch *source.ImportBatch, syncedAt int64) (SyncResult, error) {
	var res SyncResult

	err := p.emitter.Record(ctx, p.store, func(q db.Querier, rec *events.Recorder) error {
		// Compile the enabled labeling rules once per sync; matching rules are
		// applied to each brand-new transaction below.
		compiledRules, err := p.loadEnabledRules(ctx, q)
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
					// A brand-new row starts with no labels ('{}').
					created := newTransactionRow(acct, t, syncedAt, "{}")
					if err := rec.Emit(ctx, q, events.TypeTransactionCreated, events.EntityTransaction, t.ExternalID, events.TransactionSnapshot(created)); err != nil {
						return err
					}
					// Apply any matching rules (emitting label.applied per label).
					// Re-synced (existing) rows are left alone so a sync never
					// clobbers labels.
					labelsJSON, labeled, err := p.applyRulesToNewTxn(ctx, q, rec, compiledRules, acct, t, syncedAt)
					if err != nil {
						return fmt.Errorf("apply rules to transaction %q: %w", t.ExternalID, err)
					}
					if labeled {
						res.AutoLabeled++
					}
					// Record v1 of the transaction's history, folding in any birth
					// labels the rules applied. (The transaction.created event above
					// fires with empty labels; the version captures the settled state.)
					settled := newTransactionRow(acct, t, syncedAt, labelsJSON)
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
				// The refresh preserves existing labels; carry them in the snapshot.
				updated := newTransactionRow(acct, t, syncedAt, prevTxn.Labels)
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
func (p *Poller) loadEnabledRules(ctx context.Context, q db.Querier) ([]rules.Compiled, error) {
	rows, err := q.ListEnabledRules(ctx)
	if err != nil {
		return nil, fmt.Errorf("list enabled rules: %w", err)
	}
	compiled := make([]rules.Compiled, 0, len(rows))
	for _, r := range rows {
		c, cerr := rules.Compile(rules.NewRule(r.ID, r.Name, r.Query, r.Labels, r.Enabled != 0))
		if cerr != nil {
			p.logger.Warn("skipping rule with invalid query", "rule_id", r.ID, "name", r.Name, "error", cerr)
			continue
		}
		compiled = append(compiled, c)
	}
	return compiled, nil
}

// applyRulesToNewTxn applies the compiled rules to one just-inserted transaction
// (whose labels are still empty) and writes the merged label set when any rule
// matched, emitting a label.applied event per applied label. It returns the final
// labels JSON (the new-row default '{}' when nothing matched, so the caller always
// has a valid snapshot input) and whether the transaction was labeled.
func (p *Poller) applyRulesToNewTxn(ctx context.Context, q db.Querier, rec *events.Recorder, compiled []rules.Compiled, acct source.ImportAccount, t source.ImportTxn, syncedAt int64) (string, bool, error) {
	if len(compiled) == 0 {
		return "{}", false, nil
	}
	merged, changed := rules.Apply(compiled, newSearchRecord(acct, t, syncedAt), map[string]string{})
	if !changed {
		return "{}", false, nil
	}
	encoded, err := labels.Encode(merged)
	if err != nil {
		return "{}", false, err
	}
	if _, err := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: t.ExternalID, Labels: encoded}); err != nil {
		return "{}", false, err
	}
	// The transaction had no labels before, so every merged label is newly applied.
	for k, v := range merged {
		if err := rec.Emit(ctx, q, events.TypeLabelApplied, events.EntityTransaction, t.ExternalID, events.LabelPayload{TransactionID: t.ExternalID, Key: k, Value: v}); err != nil {
			return "{}", false, err
		}
	}
	return encoded, true, nil
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
// for use as an event snapshot, carrying the given labels JSON (the new-row default
// '{}' for an insert, or the existing labels for a refresh). The source/provenance
// stamp is omitted because event and version snapshots do not surface it.
func newTransactionRow(acct source.ImportAccount, t source.ImportTxn, syncedAt int64, labelsJSON string) db.Transaction {
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
	}
}

// emitSyncCompleted records a sync.completed event summarizing the run. It runs in
// its own short transaction after the data has committed, so a failure to record it
// is logged but never fails the sync. No-op when the emitter is disabled.
func (p *Poller) emitSyncCompleted(ctx context.Context, syncLogID int64, res SyncResult) {
	if p.emitter == nil {
		return
	}
	err := p.emitter.Record(ctx, p.store, func(q db.Querier, rec *events.Recorder) error {
		return rec.Emit(ctx, q, events.TypeSyncCompleted, events.EntitySync, events.EntityID(syncLogID), events.SyncCompletedPayload{
			Accounts:            res.Accounts,
			NewTransactions:     res.NewTransactions,
			UpdatedTransactions: res.UpdatedTransactions,
			AutoLabeled:         res.AutoLabeled,
			Duration:            res.Duration.String(),
		})
	})
	if err != nil {
		p.logger.Error("failed to record sync.completed event", "error", err, "sync_id", syncLogID)
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

// CredentialConfigured reports whether the ingestion source currently has a
// usable credential stored. Sources without a runtime credential report false.
func (p *Poller) CredentialConfigured(ctx context.Context) (bool, error) {
	if p.cred == nil {
		return false, nil
	}
	return p.cred.CredentialConfigured(ctx)
}

// SetCredential stores a credential for the ingestion source so the next sync
// uses it, no restart required. It errors if the source has no runtime credential.
func (p *Poller) SetCredential(ctx context.Context, input string) error {
	if p.cred == nil {
		return errors.New("configured ingestion source does not support runtime credentials")
	}
	return p.cred.SetCredential(ctx, input)
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
