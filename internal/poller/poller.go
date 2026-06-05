// Package poller fetches data from a SimpleFIN bridge on a schedule and
// persists it to the local database.
package poller

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
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
	Accounts        int           `json:"accounts"`
	NewTransactions int           `json:"new_transactions"`
	Duration        time.Duration `json:"duration"`
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
	p.logger.Info("sync complete",
		"accounts", result.Accounts,
		"new_transactions", result.NewTransactions,
		"duration", result.Duration.String(),
	)
	return result, nil
}

// persist writes accounts and transactions in a single transaction.
func (p *Poller) persist(ctx context.Context, set *AccountSet, syncedAt int64) (SyncResult, error) {
	var res SyncResult

	err := p.store.RunInTx(ctx, func(q db.Querier) error {
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
				res.NewTransactions += int(n)
			}
		}
		return nil
	})
	if err != nil {
		return SyncResult{}, err
	}
	return res, nil
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
