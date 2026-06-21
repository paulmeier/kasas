package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/events"
	"github.com/paulmeier/kasas/internal/plugins"
	pluginsource "github.com/paulmeier/kasas/internal/plugins/source"
	"github.com/paulmeier/kasas/internal/poller"
)

// pluginSourceRegistrar wires a source:provide plugin into the ingestion engine
// (ADR 0005). It satisfies plugins.SourceRegistrar: on RegisterSource it builds a
// pluginSource adapter — driven by the plugin manager as the Producer — wraps it in
// a poller on the standard sync cadence, and adds it to the engine; on
// UnregisterSource it removes the poller (stopping its schedule). The engine then
// drives the plugin source like any built-in one: it appears on the Sources page,
// syncs on the schedule, and its rows are stamped plugin:<name>.
type pluginSourceRegistrar struct {
	engine       *poller.Engine
	producer     pluginsource.Producer
	store        db.Store
	emitter      *events.Emitter
	logger       *slog.Logger
	interval     time.Duration
	lookbackDays int
}

func newPluginSourceRegistrar(engine *poller.Engine, producer pluginsource.Producer, store db.Store, emitter *events.Emitter, logger *slog.Logger, interval time.Duration, lookbackDays int) *pluginSourceRegistrar {
	return &pluginSourceRegistrar{
		engine:       engine,
		producer:     producer,
		store:        store,
		emitter:      emitter,
		logger:       logger,
		interval:     interval,
		lookbackDays: lookbackDays,
	}
}

func (r *pluginSourceRegistrar) RegisterSource(ctx context.Context, name string, m plugins.Manifest) error {
	src := pluginsource.New(name, m, r.producer)
	p := poller.New(poller.Options{
		Store:        r.store,
		Source:       src,
		Logger:       r.logger,
		Emitter:      r.emitter,
		Interval:     r.interval,
		LookbackDays: r.lookbackDays,
	})
	return r.engine.AddPoller(ctx, p)
}

func (r *pluginSourceRegistrar) UnregisterSource(ctx context.Context, name string) error {
	return r.engine.RemovePoller(ctx, pluginsource.SourceType(name))
}
