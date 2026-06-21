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
	"sync"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
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
		Help: "Total number of newly-synced transactions modified (labeled and/or extended) by a matching rule.",
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
	warmer       source.Warmer       // source.(Warmer); nil unless it is a reference (cache) source
	cred         source.Credentialed // source.(Credentialed); nil if it has no runtime credential
	store        db.Store
	logger       *slog.Logger
	emitter      *events.Emitter
	persister    *persister // shared ingestion-persist path
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
		persister:    newPersister(opts.Store, opts.Emitter, logger),
		interval:     opts.Interval,
		lookbackDays: opts.LookbackDays,
	}
	p.puller, _ = opts.Source.(source.Puller)
	p.warmer, _ = opts.Source.(source.Warmer)
	p.cred, _ = opts.Source.(source.Credentialed)
	return p
}

// SyncResult summarizes a completed sync.
type SyncResult struct {
	Accounts            int           `json:"accounts"`
	NewTransactions     int           `json:"new_transactions"`
	UpdatedTransactions int           `json:"updated_transactions"`
	AutoLabeled         int           `json:"auto_labeled"` // new transactions a rule modified (labeled and/or extended)
	Duration            time.Duration `json:"duration"`
}

// Start schedules recurring syncs. Call Stop to release resources. A
// non-positive interval disables the schedule entirely (the source is then only
// synced on demand) — used by the market source, whose primary path is the
// read-through cache, so it does not warm on a timer unless an operator opts in.
func (p *Poller) Start(ctx context.Context) error {
	if p.interval <= 0 {
		p.logger.Info("poller scheduling disabled (on-demand only)", "source", p.source.Descriptor().Type)
		return nil
	}
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

// onDemandCache reports whether this source is a read-through cache that warms on
// access rather than on a bulk sync: a Warmer with no Puller and no schedule
// (interval <= 0). [Engine.Sync] ("sync all") skips such sources so the dashboard's
// "Sync all" and the global /sync trigger never eagerly pull data nothing is
// displaying — the market source serves through its read-through cache instead.
// They stay warmable explicitly via [Engine.SyncSource] ("Sync now"), or on a timer
// if an operator sets a refresh interval (interval > 0, opting back in).
func (p *Poller) onDemandCache() bool {
	return p.puller == nil && p.warmer != nil && p.interval <= 0
}

// Sync performs a single sync. It is safe for concurrent callers; runs are
// serialized so the background schedule and on-demand API triggers never
// overlap.
func (p *Poller) Sync(ctx context.Context) (result SyncResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// A source with a runtime credential that isn't configured yet is skipped (a
	// clean no-op, not a logged failure): in a multi-source setup the other sources
	// still sync, and a credential can be added at runtime without a restart.
	if p.cred != nil {
		configured, cerr := p.cred.CredentialConfigured(ctx)
		if cerr == nil && !configured {
			p.logger.Debug("skipping sync: source not connected", "source", p.source.Descriptor().Type)
			return SyncResult{}, nil
		}
	}

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

	// A reference source (e.g. market data) warms a read-through cache instead of
	// ingesting transactions: it never produces accounts/transactions, so it skips
	// the whole persist path. The run is still recorded to sync_log (via the defer
	// above); the source owns its own storage and event emission.
	if p.puller == nil {
		if p.warmer != nil {
			if err = p.warmer.Warm(ctx); err != nil {
				return SyncResult{}, err
			}
			result.Duration = time.Since(start)
			p.logger.Info("cache warm complete", "source", p.source.Descriptor().Type, "duration", result.Duration.String())
			return result, nil
		}
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

	result, err = p.persister.Persist(ctx, batch, time.Now().Unix())
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
