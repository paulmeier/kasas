package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// This file is the manager's surface for plugin dashboard pages: listing which
// loaded plugins expose one (for the sidebar) and rendering a page or running one
// of its actions. Page work is funnelled through the plugin's existing worker
// queue, so it serializes with event hooks and the non-reentrant VM is never
// touched from a second goroutine.

var (
	// ErrNoPage is returned when the plugin exists but exposes no dashboard page
	// (no [ui] block, the ui:page grant was revoked, or the requested hook is not
	// declared).
	ErrNoPage = errors.New("plugins: plugin has no dashboard page")
)

// PageInfo is one sidebar entry: a loaded plugin that exposes a dashboard page.
type PageInfo struct {
	Name  string
	Title string
	Icon  string
}

// Pages lists the dashboard pages of currently loaded plugins, sorted by name.
// Only loaded plugins appear: a disabled or failed plugin's page disappears from
// the sidebar rather than 500ing when opened.
func (m *Manager) Pages() []PageInfo {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []PageInfo
	for _, p := range m.plugins {
		if p.manifest.UI == nil || !p.caps.has(CapUIPage) {
			continue
		}
		out = append(out, PageInfo{Name: p.name, Title: p.manifest.UI.Title, Icon: p.manifest.UI.Icon})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RenderPage renders a plugin's dashboard page (req.Action == "") or runs one of
// its actions (req.Action != "") and returns the validated, normalized page
// document. The plugin is addressed by name because the page URL is /ext/<name>.
func (m *Manager) RenderPage(ctx context.Context, name string, req PageRequest) (json.RawMessage, error) {
	if m == nil {
		return nil, ErrDisabled
	}
	m.mu.RLock()
	p := m.plugins[name]
	m.mu.RUnlock()
	if p == nil {
		return nil, ErrPluginNotFound
	}
	if p.manifest.UI == nil || !p.caps.has(CapUIPage) {
		return nil, ErrNoPage
	}
	hook := HookPageRender
	if req.Action != "" {
		hook = HookPageAction
	}
	if !manifestDeclares(p.manifest, hook) {
		return nil, ErrNoPage
	}
	req.Plugin = name

	reply, err := m.enqueueRender(ctx, p, job{hook: hook, req: &req, reply: make(chan renderReply, 1)})
	if err != nil {
		return nil, err
	}
	return reply.doc, reply.err
}

// enqueueRender queues a page job on the plugin's worker and waits for the
// answer. Unlike event dispatch (which drops on a full queue, backstopped by
// replay), a page request has a caller waiting, so it blocks until the queue
// accepts it or the request context gives up. The recover converts the
// send-on-closed-queue panic of a concurrently unloaded plugin into a clean error.
func (m *Manager) enqueueRender(ctx context.Context, p *plugin, j job) (rep renderReply, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("plugin %q was unloaded", p.name)
		}
	}()
	select {
	case p.jobs <- j:
	case <-ctx.Done():
		return renderReply{}, ctx.Err()
	case <-p.done:
		return renderReply{}, fmt.Errorf("plugin %q was unloaded", p.name)
	}
	select {
	case rep = <-j.reply:
		return rep, nil
	case <-ctx.Done():
		return renderReply{}, ctx.Err()
	case <-p.done:
		// The worker drains the queue before done closes, so an accepted job is
		// always answered; when done and the (buffered) reply are ready together
		// the select picks randomly, so re-check the reply before reporting.
		select {
		case rep = <-j.reply:
			return rep, nil
		default:
			return renderReply{}, fmt.Errorf("plugin %q was unloaded", p.name)
		}
	}
}
