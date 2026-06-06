// Package poller fetches data from a SimpleFIN bridge on a schedule and
// persists it to the local database.
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
	"github.com/paulmeier/kasas/internal/labels"
	"github.com/paulmeier/kasas/internal/rules"
	"github.com/paulmeier/kasas/internal/search"
	"github.com/paulmeier/kasas/internal/vault"
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
	Store           db.Store
	Secrets         vault.SecretStore
	Logger          *slog.Logger
	Interval        time.Duration
	LookbackDays    int
	ConfigAccessURL string // simplefin.access_url from config, if any
	SetupToken      string // simplefin.setup_token from config, if any
}

// Poller owns the SimpleFIN sync loop.
type Poller struct {
	client       *SimpleFINClient
	store        db.Store
	secrets      vault.SecretStore
	logger       *slog.Logger
	interval     time.Duration
	lookbackDays int
	configURL    string
	setupToken   string

	mu    sync.Mutex // serializes syncs (background + on-demand)
	sched gocron.Scheduler
}

// New constructs a Poller.
func New(opts Options) *Poller {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		client:       NewSimpleFINClient(),
		store:        opts.Store,
		secrets:      opts.Secrets,
		logger:       logger,
		interval:     opts.Interval,
		lookbackDays: opts.LookbackDays,
		configURL:    opts.ConfigAccessURL,
		setupToken:   opts.SetupToken,
	}
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

	accessURL, err := p.resolveAccessURL(ctx)
	if err != nil {
		return SyncResult{}, err
	}
	if accessURL == "" {
		return SyncResult{}, errors.New("no SimpleFIN access URL configured (set simplefin.setup_token or simplefin.access_url)")
	}

	var since time.Time
	if p.lookbackDays > 0 {
		since = start.AddDate(0, 0, -p.lookbackDays)
	}

	set, err := p.client.Fetch(ctx, accessURL, since)
	if err != nil {
		return SyncResult{}, err
	}
	for _, e := range set.Errors {
		p.logger.Warn("simplefin reported an error", "error", e)
	}

	result, err = p.persist(ctx, set, time.Now().Unix())
	if err != nil {
		return SyncResult{}, err
	}
	result.Duration = time.Since(start)

	accountsGauge.Set(float64(result.Accounts))
	txInserted.Add(float64(result.NewTransactions))
	txUpdated.Add(float64(result.UpdatedTransactions))
	rulesApplied.Add(float64(result.AutoLabeled))
	p.logger.Info("sync complete",
		"accounts", result.Accounts,
		"new_transactions", result.NewTransactions,
		"updated_transactions", result.UpdatedTransactions,
		"auto_labeled", result.AutoLabeled,
		"duration", result.Duration.String(),
	)
	return result, nil
}

// persist writes accounts and transactions in a single transaction.
func (p *Poller) persist(ctx context.Context, set *AccountSet, syncedAt int64) (SyncResult, error) {
	var res SyncResult

	err := p.store.RunInTx(ctx, func(q db.Querier) error {
		// Compile the enabled labeling rules once per sync; matching rules are
		// applied to each brand-new transaction below.
		compiledRules, err := p.loadEnabledRules(ctx, q)
		if err != nil {
			return err
		}

		for _, acct := range set.Accounts {
			org := acct.Org
			if err := q.UpsertOrganization(ctx, db.UpsertOrganizationParams{
				ID:      org.StableOrgID(),
				Domain:  org.Domain,
				Name:    org.Name,
				SfinUrl: org.SfinURL,
			}); err != nil {
				return fmt.Errorf("upsert organization %q: %w", org.StableOrgID(), err)
			}

			if err := q.UpsertAccount(ctx, db.UpsertAccountParams{
				ID:          acct.ID,
				OrgID:       org.StableOrgID(),
				Name:        acct.Name,
				Currency:    acct.Currency,
				Balance:     acct.Balance,
				BalanceDate: acct.BalanceDate,
				SyncedAt:    syncedAt,
			}); err != nil {
				return fmt.Errorf("upsert account %q: %w", acct.ID, err)
			}
			res.Accounts++

			for _, t := range acct.Transactions {
				// Insert is ON CONFLICT DO NOTHING, so n==1 means a brand-new
				// transaction and n==0 means it already exists. For existing rows we
				// refresh the bridge-owned fields (so a pending charge that posts, or
				// a corrected amount, flows in) via UpdateTransactionFromSync, which
				// deliberately leaves the labels column untouched — user labels are
				// never clobbered by a sync.
				n, err := q.InsertTransaction(ctx, db.InsertTransactionParams{
					ID:          t.ID,
					AccountID:   acct.ID,
					Amount:      t.Amount,
					Pending:     boolToInt(t.Pending),
					Date:        transactionDate(t),
					Description: t.Description,
					Payee:       t.Payee,
					Memo:        t.Memo,
					SyncedAt:    syncedAt,
				})
				if err != nil {
					return fmt.Errorf("insert transaction %q: %w", t.ID, err)
				}
				if n > 0 {
					res.NewTransactions += int(n)
					// A brand-new row starts with no labels ('{}'); apply any
					// matching rules. Re-synced (existing) rows are left alone so a
					// sync never clobbers labels.
					labeled, err := p.applyRulesToNewTxn(ctx, q, compiledRules, acct, t, syncedAt)
					if err != nil {
						return fmt.Errorf("apply rules to transaction %q: %w", t.ID, err)
					}
					if labeled {
						res.AutoLabeled++
					}
					continue
				}
				if _, err := q.UpdateTransactionFromSync(ctx, db.UpdateTransactionFromSyncParams{
					ID:          t.ID,
					AccountID:   acct.ID,
					Amount:      t.Amount,
					Pending:     boolToInt(t.Pending),
					Date:        transactionDate(t),
					Description: t.Description,
					Payee:       t.Payee,
					Memo:        t.Memo,
					SyncedAt:    syncedAt,
				}); err != nil {
					return fmt.Errorf("refresh transaction %q: %w", t.ID, err)
				}
				res.UpdatedTransactions++
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
// matched. It reports whether the transaction was labeled.
func (p *Poller) applyRulesToNewTxn(ctx context.Context, q db.Querier, compiled []rules.Compiled, acct SimpleFINAccount, t SimpleFINTransaction, syncedAt int64) (bool, error) {
	if len(compiled) == 0 {
		return false, nil
	}
	merged, changed := rules.Apply(compiled, newSearchRecord(acct, t, syncedAt), map[string]string{})
	if !changed {
		return false, nil
	}
	encoded, err := labels.Encode(merged)
	if err != nil {
		return false, err
	}
	if _, err := q.UpdateTransactionLabels(ctx, db.UpdateTransactionLabelsParams{ID: t.ID, Labels: encoded}); err != nil {
		return false, err
	}
	return true, nil
}

// newSearchRecord adapts a SimpleFIN account + transaction into the search
// engine's neutral Record so rules can match on any field. A freshly inserted
// transaction carries no labels yet.
func newSearchRecord(acct SimpleFINAccount, t SimpleFINTransaction, syncedAt int64) search.Record {
	return search.Record{
		ID:          t.ID,
		AccountID:   acct.ID,
		AccountName: acct.Name,
		Amount:      parseAmount(t.Amount),
		AmountRaw:   t.Amount,
		Pending:     t.Pending,
		Date:        time.Unix(transactionDate(t), 0).UTC(),
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

// resolveAccessURL determines the SimpleFIN access URL, preferring an already
// stored value, then a directly configured URL, then claiming a setup token.
// Resolved URLs are persisted so the (one-time) setup token is consumed once.
func (p *Poller) resolveAccessURL(ctx context.Context) (string, error) {
	stored, err := p.secrets.AccessURL(ctx)
	if err != nil {
		return "", fmt.Errorf("read stored access URL: %w", err)
	}
	if stored != "" {
		return stored, nil
	}

	if p.configURL != "" {
		if err := p.secrets.SetAccessURL(ctx, p.configURL); err != nil {
			p.logger.Warn("failed to persist configured access URL", "error", err)
		}
		return p.configURL, nil
	}

	if p.setupToken != "" {
		p.logger.Info("claiming SimpleFIN setup token")
		url, err := p.client.Claim(ctx, p.setupToken)
		if err != nil {
			return "", fmt.Errorf("claim setup token: %w", err)
		}
		if err := p.secrets.SetAccessURL(ctx, url); err != nil {
			p.logger.Warn("failed to persist claimed access URL", "error", err)
		}
		return url, nil
	}

	return "", nil
}

// CredentialConfigured reports whether a SimpleFIN access URL is currently stored
// in the secret store (i.e. kasas is connected to a bridge and a sync can run).
func (p *Poller) CredentialConfigured(ctx context.Context) (bool, error) {
	stored, err := p.secrets.AccessURL(ctx)
	if err != nil {
		return false, fmt.Errorf("read stored access URL: %w", err)
	}
	return stored != "", nil
}

// SetCredential stores a SimpleFIN credential so the next sync uses it, no restart
// required. input may be either a ready access URL (starts with http:// or
// https://, stored verbatim) or a base64 setup token (claimed for an access URL
// first; the claim is a one-time exchange). Mirrors resolveAccessURL's handling so
// the UI-driven path and the config-driven path behave identically.
func (p *Poller) SetCredential(ctx context.Context, input string) error {
	input = strings.TrimSpace(input)
	if input == "" {
		return errors.New("SimpleFIN credential is empty")
	}

	accessURL := input
	if !strings.HasPrefix(input, "http://") && !strings.HasPrefix(input, "https://") {
		p.logger.Info("claiming SimpleFIN setup token")
		claimed, err := p.client.Claim(ctx, input)
		if err != nil {
			return fmt.Errorf("claim setup token: %w", err)
		}
		accessURL = claimed
	}

	if err := p.secrets.SetAccessURL(ctx, accessURL); err != nil {
		return fmt.Errorf("store access URL: %w", err)
	}
	return nil
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func transactionDate(t SimpleFINTransaction) int64 {
	if t.Posted != 0 {
		return t.Posted
	}
	return t.TransactedAt
}
