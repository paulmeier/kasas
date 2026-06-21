package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// This file is the manager's surface for plugin ingestion sources (ADR 0005): a
// source:provide plugin's OnFetch producer hook. Like page rendering, the call is
// funnelled through the plugin's existing worker queue, so it serializes with event
// hooks and the non-reentrant VM is never touched from a second goroutine — here the
// caller is the ingestion engine's sync goroutine (via the pluginSource adapter)
// rather than an HTTP request.

// Produce runs a plugin source's OnFetch hook with the given request payload
// ({"since":<unix>,"cursor":"..."}) and returns the raw ImportBatch JSON it
// produced. The result is UNTRUSTED — the pluginSource adapter namespaces ids and
// stamps plugin:<name> provenance before the engine persists it. It errors with
// ErrCapabilityDenied if the plugin lacks source:provide (the operator revoked the
// grant) and ErrHookNotImpl if it does not declare the hook.
func (m *Manager) Produce(ctx context.Context, name string, hook Hook, payload json.RawMessage) (json.RawMessage, error) {
	if m == nil {
		return nil, ErrDisabled
	}
	m.mu.RLock()
	p := m.plugins[name]
	m.mu.RUnlock()
	if p == nil {
		return nil, ErrPluginNotFound
	}
	if !p.caps.has(CapSourceProvide) {
		return nil, ErrCapabilityDenied
	}
	if !manifestDeclares(p.manifest, hook) {
		return nil, ErrHookNotImpl
	}

	rep, err := m.enqueueProduce(ctx, p, job{hook: hook, payload: payload, produce: make(chan produceReply, 1)})
	if err != nil {
		return nil, err
	}
	return rep.batch, rep.err
}

// produce runs one producer job on the worker: it invokes the value-returning
// OnFetch hook under the per-hook timeout and answers on the job's produce channel.
// Health and metrics are recorded exactly like a page render, so a producer that
// errors shows up on the Plugins page. The returned batch is left UNVALIDATED here —
// the adapter and the engine's persist are the containment point (ADR 0005).
func (m *Manager) produce(ctx context.Context, p *plugin, j job) {
	cctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()

	raw, err := p.inst.Produce(cctx, j.hook, j.payload)
	pluginInvocations.WithLabelValues(p.name, string(j.hook)).Inc()
	// Mirror render's bookkeeping: an unimplemented hook is rejected at load (so this
	// is defensive) and a shutdown-cancelled run is not a plugin failure; neither
	// should flip the plugin's health.
	if err != nil && (errors.Is(err, ErrHookNotImpl) || ctx.Err() != nil) {
		j.produce <- produceReply{err: err}
		return
	}
	if err != nil {
		pluginErrors.WithLabelValues(p.name).Inc()
		m.logger.Warn("plugin producer hook failed", "plugin", p.name, "hook", j.hook, "error", err)
	}
	m.recordStatus(ctx, p, err)
	j.produce <- produceReply{batch: raw, err: err} // buffered: never blocks the worker
}

// enqueueProduce queues a producer job on the plugin's worker and waits for the
// answer, mirroring enqueueRender: a producer call has the engine's sync goroutine
// waiting, so it blocks until the queue accepts it or the context gives up. The
// recover converts the send-on-closed-queue panic of a concurrently unloaded plugin
// into a clean error.
func (m *Manager) enqueueProduce(ctx context.Context, p *plugin, j job) (rep produceReply, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin %q was unloaded", p.name)
		}
	}()
	select {
	case p.jobs <- j:
	case <-ctx.Done():
		return produceReply{}, ctx.Err()
	case <-p.done:
		return produceReply{}, fmt.Errorf("plugin %q was unloaded", p.name)
	}
	select {
	case rep = <-j.produce:
		return rep, nil
	case <-ctx.Done():
		return produceReply{}, ctx.Err()
	case <-p.done:
		// The worker drains the queue before done closes, so an accepted job is
		// always answered; when done and the (buffered) reply are ready together the
		// select picks randomly, so re-check the reply before reporting.
		select {
		case rep = <-j.produce:
			return rep, nil
		default:
			return produceReply{}, fmt.Errorf("plugin %q was unloaded", p.name)
		}
	}
}
