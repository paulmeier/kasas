// Package plugins is kasas's plugin runtime: it loads third-party plugins from a
// directory and runs them in a sandboxed language VM, reacting to committed events
// off the event bus (the async, post-commit counterpart to the rules engine and
// webhooks). A plugin declares the lifecycle hooks it implements and the
// capabilities it needs; the manager delivers matching events to a per-plugin
// worker, and a capability-checked host facade is the only way a plugin can read or
// mutate ledger data — so enforcement is identical regardless of the language a
// plugin is written in.
//
// Three runtimes ship today behind the Runtime/Instance adapter seam: Lua (via
// gopher-lua), JavaScript/TypeScript (via goja, with esbuild stripping TypeScript
// types at load), and WASM (via wazero — the home for plugins written in Go, or
// any other language that compiles to wasip1). All three are pure Go, preserving
// the single-static-binary build.
package plugins

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/paulmeier/kasas/internal/events"
)

// Hook is a lifecycle point a plugin can subscribe to. Most map to exactly one
// event type (see hookTrigger); the manager only delivers an event to a plugin
// that declared the corresponding hook. HookUninstall is the exception: it is a
// LIFECYCLE hook with no triggering event, invoked directly when the plugin is
// uninstalled so it can clean up anything it created.
type Hook string

const (
	HookTransactionCreate Hook = "OnTransactionCreate" // <- events.TypeTransactionCreated
	HookTransactionUpdate Hook = "OnTransactionUpdate" // <- events.TypeTransactionUpdated
	HookSyncComplete      Hook = "OnSyncComplete"      // <- events.TypeSyncCompleted
	// HookUninstall runs once, synchronously, when a plugin is uninstalled — before
	// its files are removed — so the plugin can undo anything it created (labels,
	// extensions). It is not driven by the event bus (absent from hookTrigger), so it
	// is never dispatched as part of normal operation. A plugin owns its own cleanup;
	// kasas just runs this hook.
	HookUninstall Hook = "OnUninstall"
	// HookPageRender and HookPageAction back a plugin's dashboard page (the [ui]
	// manifest block). Like HookUninstall they are request-driven, not event-driven
	// (absent from hookTrigger): the dashboard asks the API to render the page, the
	// manager invokes the hook on the plugin's worker, and the hook RETURNS a
	// declarative page document (see pagedoc.go) instead of mutating anything.
	// OnPageRender produces the page; OnPageAction handles a button press declared
	// by a previous render and returns the refreshed page.
	HookPageRender Hook = "OnPageRender"
	HookPageAction Hook = "OnPageAction"
)

// Capability is a permission a plugin must declare and be granted before the host
// will let it perform the corresponding action. Enforcement lives in the host
// facade (host.go), so every runtime inherits it.
type Capability string

const (
	CapTransactionsRead Capability = "transactions:read" // read the triggering txn + run searches
	CapLabelsWrite      Capability = "labels:write"      // apply/remove labels
	CapExtensionsWrite  Capability = "extensions:write"  // set/remove schema extensions
	// CapUIPage lets a plugin expose a dashboard page (a sidebar entry plus a
	// declaratively rendered page). It gates the render/action endpoints rather than
	// a host method: revoking it makes the page disappear without touching the
	// plugin's event hooks.
	CapUIPage Capability = "ui:page"
)

// Runtime values for the manifest `runtime` field. Each maps to a registered
// Runtime implementation (NewLuaRuntime / NewJSRuntime / NewWasmRuntime).
const (
	RuntimeLua  = "lua"
	RuntimeJS   = "js"
	RuntimeWasm = "wasm"
)

// knownRuntimes is the set of accepted `runtime` values. An unknown runtime is a
// hard manifest error so a typo can't silently fail to load.
var knownRuntimes = map[string]bool{
	RuntimeLua:  true,
	RuntimeJS:   true,
	RuntimeWasm: true,
}

// defaultEntrypoints is the per-runtime fallback source file when a manifest omits
// `entrypoint`. A JS plugin written in TypeScript sets `entrypoint = "main.ts"`
// explicitly; the JS runtime picks the esbuild loader by file extension.
var defaultEntrypoints = map[string]string{
	RuntimeLua:  "main.lua",
	RuntimeJS:   "main.js",
	RuntimeWasm: "main.wasm",
}

// supportedRuntimes is the sorted, human-readable list of accepted runtimes, used in
// the "unsupported runtime" error.
func supportedRuntimes() string {
	rs := make([]string, 0, len(knownRuntimes))
	for r := range knownRuntimes {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	return strings.Join(rs, ", ")
}

// hookTrigger maps each hook to the bus event type that fires it. It is the
// plugin analogue of webhooks.Matches: the manager routes an event to a plugin
// only when the plugin declared the hook this event triggers.
var hookTrigger = map[Hook]string{
	HookTransactionCreate: events.TypeTransactionCreated,
	HookTransactionUpdate: events.TypeTransactionUpdated,
	HookSyncComplete:      events.TypeSyncCompleted,
}

// knownHooks / knownCapabilities back manifest validation: an unknown hook or
// capability is a hard error so a typo can't silently subscribe to nothing or be
// mistaken for a real grant.
var knownHooks = map[Hook]bool{
	HookTransactionCreate: true,
	HookTransactionUpdate: true,
	HookSyncComplete:      true,
	HookUninstall:         true, // accepted and resolved, but never event-dispatched
	HookPageRender:        true, // request-driven (dashboard page), never event-dispatched
	HookPageAction:        true, // request-driven (dashboard page), never event-dispatched
}

var knownCapabilities = map[Capability]bool{
	CapTransactionsRead: true,
	CapLabelsWrite:      true,
	CapExtensionsWrite:  true,
	CapUIPage:           true,
}

// knownUIIcons is the curated set of sidebar icon names a plugin page may pick
// from. Icons are chosen by NAME so a plugin can never inject markup through the
// sidebar; the dashboard owns the actual SVGs (see dashboard/plugin_page.go) and
// falls back to "puzzle" for anything it doesn't recognize. The kasas-plugins
// registry validates against the same list.
var knownUIIcons = map[string]bool{
	"bell":     true,
	"calendar": true,
	"chart":    true,
	"coin":     true,
	"flag":     true,
	"gauge":    true,
	"heart":    true,
	"list":     true,
	"puzzle":   true,
	"star":     true,
}

// nameRE constrains a plugin name to a filesystem- and identity-safe slug; the
// name must also equal the plugin's directory name (the loader enforces that).
var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// Manifest is the parsed plugin.toml. The code and manifest are the source of
// truth for a plugin's identity and behavior; the DB row holds only mutable
// operator state (enabled, granted capabilities, config overrides, health).
type Manifest struct {
	Name         string         `toml:"name"`
	Version      string         `toml:"version"`
	Description  string         `toml:"description"`
	Author       string         `toml:"author"`
	Runtime      string         `toml:"runtime"`
	Entrypoint   string         `toml:"entrypoint"`
	Hooks        []Hook         `toml:"hooks"`
	Capabilities []Capability   `toml:"capabilities"`
	Config       map[string]any `toml:"config"`
	// UI, when present, declares the plugin's dashboard page: a sidebar entry
	// (title + curated icon) routing to /ext/<name>, rendered by the OnPageRender
	// hook. Requires the ui:page capability so the operator can revoke the page.
	UI *UIManifest `toml:"ui"`
}

// UIManifest is the manifest's optional [ui] block.
type UIManifest struct {
	// Title is the sidebar label and default page heading.
	Title string `toml:"title"`
	// Icon names a sidebar glyph from the curated set (knownUIIcons); empty
	// defaults to "puzzle".
	Icon string `toml:"icon"`
}

// maxUITitleLen bounds the sidebar label so a plugin can't bloat the nav.
const maxUITitleLen = 40

// ParseManifest decodes and validates a plugin.toml. It normalizes a few fields
// (defaulting the entrypoint), so the returned manifest is ready to use.
func ParseManifest(data []byte) (Manifest, error) {
	var m Manifest
	if err := toml.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("parse manifest: %w", err)
	}
	if err := m.normalizeAndValidate(); err != nil {
		return Manifest{}, err
	}
	return m, nil
}

func (m *Manifest) normalizeAndValidate() error {
	m.Name = strings.TrimSpace(m.Name)
	m.Runtime = strings.TrimSpace(m.Runtime)
	m.Entrypoint = strings.TrimSpace(m.Entrypoint)
	if m.Config == nil {
		m.Config = map[string]any{}
	}

	if !nameRE.MatchString(m.Name) {
		return fmt.Errorf("invalid plugin name %q (must match %s)", m.Name, nameRE.String())
	}
	if !knownRuntimes[m.Runtime] {
		return fmt.Errorf("unsupported runtime %q (supported: %s)", m.Runtime, supportedRuntimes())
	}
	// Default the entrypoint per runtime (main.lua / main.js) now that the runtime is
	// known to be valid.
	if m.Entrypoint == "" {
		m.Entrypoint = defaultEntrypoints[m.Runtime]
	}
	if len(m.Hooks) == 0 {
		return fmt.Errorf("plugin must declare at least one hook")
	}
	for _, h := range m.Hooks {
		if !knownHooks[h] {
			return fmt.Errorf("unknown hook %q", h)
		}
	}
	for _, c := range m.Capabilities {
		if !knownCapabilities[c] {
			return fmt.Errorf("unknown capability %q", c)
		}
	}
	// Entrypoint must be a plain filename within the plugin dir, never a path that
	// could escape it.
	if strings.ContainsAny(m.Entrypoint, `/\`) || m.Entrypoint == ".." {
		return fmt.Errorf("entrypoint %q must be a file name inside the plugin directory", m.Entrypoint)
	}
	return m.validateUI()
}

// validateUI enforces the dashboard-page contract: the [ui] block, the
// OnPageRender hook, and the ui:page capability come as a unit, so a plugin can
// never end up with a sidebar entry that has no renderer (or a render hook the
// operator can't revoke).
func (m *Manifest) validateUI() error {
	declares := func(h Hook) bool {
		for _, x := range m.Hooks {
			if x == h {
				return true
			}
		}
		return false
	}
	requests := func(c Capability) bool {
		for _, x := range m.Capabilities {
			if x == c {
				return true
			}
		}
		return false
	}

	if m.UI == nil {
		if declares(HookPageRender) || declares(HookPageAction) {
			return fmt.Errorf("hooks %s/%s require a [ui] block", HookPageRender, HookPageAction)
		}
		if requests(CapUIPage) {
			return fmt.Errorf("capability %q requires a [ui] block", CapUIPage)
		}
		return nil
	}

	m.UI.Title = strings.TrimSpace(m.UI.Title)
	m.UI.Icon = strings.TrimSpace(m.UI.Icon)
	if m.UI.Title == "" || len(m.UI.Title) > maxUITitleLen {
		return fmt.Errorf("ui.title is required and must be at most %d characters", maxUITitleLen)
	}
	if m.UI.Icon == "" {
		m.UI.Icon = "puzzle"
	}
	if !knownUIIcons[m.UI.Icon] {
		return fmt.Errorf("unknown ui.icon %q (supported: %s)", m.UI.Icon, supportedUIIcons())
	}
	if !declares(HookPageRender) {
		return fmt.Errorf("a [ui] block requires the %s hook", HookPageRender)
	}
	if !requests(CapUIPage) {
		return fmt.Errorf("a [ui] block requires the %q capability", CapUIPage)
	}
	return nil
}

// supportedUIIcons is the sorted, human-readable icon list for the validation error.
func supportedUIIcons() string {
	ns := make([]string, 0, len(knownUIIcons))
	for n := range knownUIIcons {
		ns = append(ns, n)
	}
	sort.Strings(ns)
	return strings.Join(ns, ", ")
}
