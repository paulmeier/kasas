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
	// HookFetch is a plugin source's scheduled producer (the [source] manifest block
	// + source:provide capability, ADR 0005). Like the page hooks it is request-driven,
	// not event-driven (absent from hookTrigger): the ingestion engine calls it on the
	// sync schedule (and on demand), passing the since/cursor it threads to every
	// puller, and the hook RETURNS an ImportBatch the engine persists — it never writes
	// a row itself.
	HookFetch Hook = "OnFetch"
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
	// CapNetFetch lets a plugin make outbound HTTP(S) requests through the host
	// (kasas.fetch), but only to the hosts it declares in the manifest's [net].allow
	// block — egress is host-mediated, default-deny, and allowlisted (ADR 0002). It
	// is the only sanctioned way out of the sandbox; the runtimes never expose a raw
	// socket. Declaring it without a non-empty [net].allow is a manifest error.
	CapNetFetch Capability = "net:fetch"
	// CapSourceProvide lets a plugin originate transactions as a PRODUCER: its OnFetch
	// hook returns an ImportBatch and the ingestion engine persists it through the same
	// path as every built-in source — dedup, events, rules, history, and a provenance
	// stamp of plugin:<name> the plugin cannot forge (ADR 0005). It is the most powerful
	// capability kasas exposes (it writes to the ledger's core, not just its
	// annotations), so it is the top trust tier and comes as a unit with a [source]
	// block. There is no host method that writes a row; creation is only "return a batch
	// the engine persists". Declaring it without a [source] block is a manifest error.
	CapSourceProvide Capability = "source:provide"
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
	HookFetch:             true, // request-driven (sync schedule), never event-dispatched
}

var knownCapabilities = map[Capability]bool{
	CapTransactionsRead: true,
	CapLabelsWrite:      true,
	CapExtensionsWrite:  true,
	CapUIPage:           true,
	CapNetFetch:         true,
	CapSourceProvide:    true,
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
	// Net, when present, declares the plugin's egress allowlist: the exact hosts
	// kasas.fetch may reach. It comes as a unit with the net:fetch capability (each
	// requires the other), so the install/enable prompt can surface a specific,
	// reviewable claim ("this plugin talks to: paperless.lan") rather than a generic
	// "uses the network" warning. See ADR 0002.
	Net *NetManifest `toml:"net"`
	// Source, when present, declares that the plugin provides an ingestion source: it
	// comes as a unit with the source:provide capability and the OnFetch hook (each
	// requires the others), so the install/enable prompt can make a specific claim
	// ("this plugin adds a source: acme-card"). The engine registers it as a first-class
	// source — it appears on the Sources page, syncs on the schedule, and stamps
	// plugin:<name> provenance. See ADR 0005.
	Source *SourceManifest `toml:"source"`
}

// SourceManifest is the manifest's optional [source] block: it declares a plugin
// ingestion source (ADR 0005). It comes as a unit with the source:provide capability
// and the OnFetch hook.
type SourceManifest struct {
	// Type is the human-readable source name shown on the Sources page (e.g.
	// "acme-card"). It is a display label only — the engine keys the source and stamps
	// provenance by the plugin's own name (plugin:<name>), which the plugin cannot
	// forge, so Type can never let a plugin masquerade as a built-in source.
	Type string `toml:"type"`
	// Archetype is how the source delivers data. Only "pull" (a scheduled producer
	// whose OnFetch the engine calls on the sync schedule) is supported today; empty
	// defaults to "pull". A reactive producer (an event hook that returns a batch) is a
	// planned follow-up and declares no [source] block.
	Archetype string `toml:"archetype"`
}

// NetManifest is the manifest's optional [net] block: the egress allowlist for a
// net:fetch plugin. Egress is default-deny — a plugin may only reach the hosts
// listed here, and a redirect onto an undeclared host is refused too.
type NetManifest struct {
	// Allow is the set of hostnames the plugin may reach (no scheme, no port,
	// case-insensitive). A request whose URL host is not on this list is refused by
	// the host before any connection is made.
	Allow []string `toml:"allow"`
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
	if err := m.validateUI(); err != nil {
		return err
	}
	if err := m.validateNet(); err != nil {
		return err
	}
	return m.validateSource()
}

// requests reports whether the manifest declares capability c.
func (m *Manifest) requests(c Capability) bool {
	for _, x := range m.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// validateNet enforces the egress contract: the net:fetch capability and a
// non-empty [net].allow list come as a unit (each requires the other), so a
// net:fetch plugin always carries a specific, reviewable egress surface and a
// declared allowlist can never run without the capability that gates it. Host
// entries are normalized (lowercased, trimmed) and bounded so the list the
// dashboard surfaces at enable time is the exact list the host enforces.
func (m *Manifest) validateNet() error {
	if m.Net == nil {
		if m.requests(CapNetFetch) {
			return fmt.Errorf("capability %q requires a [net] block with a non-empty allow list", CapNetFetch)
		}
		return nil
	}
	if !m.requests(CapNetFetch) {
		return fmt.Errorf("a [net] block requires the %q capability", CapNetFetch)
	}

	seen := make(map[string]bool, len(m.Net.Allow))
	out := make([]string, 0, len(m.Net.Allow))
	for _, h := range m.Net.Allow {
		host, err := normalizeNetHost(h)
		if err != nil {
			return err
		}
		if seen[host] {
			continue
		}
		seen[host] = true
		out = append(out, host)
	}
	if len(out) == 0 {
		return fmt.Errorf("capability %q requires a non-empty [net].allow list", CapNetFetch)
	}
	if len(out) > maxNetAllowHosts {
		return fmt.Errorf("[net].allow lists %d hosts, over the limit of %d", len(out), maxNetAllowHosts)
	}
	m.Net.Allow = out
	return nil
}

// maxNetAllowHosts bounds the declared egress list so the install/enable prompt
// (which surfaces every host) stays readable and a manifest can't list thousands.
const maxNetAllowHosts = 32

// normalizeNetHost validates and canonicalizes one [net].allow entry: a bare
// hostname (or IP), no scheme, port, path, or whitespace, lowercased. Matching at
// request time is exact on the URL's hostname, so the stored form must be the
// hostname alone.
func normalizeNetHost(h string) (string, error) {
	host := strings.ToLower(strings.TrimSpace(h))
	if host == "" {
		return "", fmt.Errorf("[net].allow contains an empty host")
	}
	if strings.ContainsAny(host, "/\\ \t") || strings.Contains(host, "://") {
		return "", fmt.Errorf("[net].allow host %q must be a bare hostname (no scheme, path, or spaces)", h)
	}
	if strings.ContainsRune(host, ':') {
		// A port would never match a hostname-only comparison; reject it explicitly so
		// "host:443" fails loudly rather than silently never matching. Bracketed IPv6
		// literals are still allowed (they contain ':' but also '[').
		if !strings.HasPrefix(host, "[") {
			return "", fmt.Errorf("[net].allow host %q must not include a port", h)
		}
	}
	return host, nil
}

// maxSourceTitleLen bounds the [source].type display label so it stays readable on
// the Sources page.
const maxSourceTitleLen = 40

// archetypePull is the only [source].archetype supported today (a scheduled
// producer). Empty defaults to it.
const archetypePull = "pull"

// validateSource enforces the producer contract (ADR 0005): the [source] block, the
// source:provide capability, and the OnFetch hook come as a unit, so a plugin can
// never end up advertising a source it can't actually produce (or a producer hook
// the operator can't revoke). The archetype is constrained to "pull" — the only
// shape the engine drives today.
func (m *Manifest) validateSource() error {
	declares := func(h Hook) bool {
		for _, x := range m.Hooks {
			if x == h {
				return true
			}
		}
		return false
	}

	if m.Source == nil {
		if m.requests(CapSourceProvide) {
			return fmt.Errorf("capability %q requires a [source] block", CapSourceProvide)
		}
		if declares(HookFetch) {
			return fmt.Errorf("the %s hook requires a [source] block and the %q capability", HookFetch, CapSourceProvide)
		}
		return nil
	}
	if !m.requests(CapSourceProvide) {
		return fmt.Errorf("a [source] block requires the %q capability", CapSourceProvide)
	}
	if !declares(HookFetch) {
		return fmt.Errorf("a [source] block requires the %s hook", HookFetch)
	}

	m.Source.Type = strings.TrimSpace(m.Source.Type)
	m.Source.Archetype = strings.ToLower(strings.TrimSpace(m.Source.Archetype))
	if m.Source.Type == "" || len(m.Source.Type) > maxSourceTitleLen {
		return fmt.Errorf("source.type is required and must be at most %d characters", maxSourceTitleLen)
	}
	if m.Source.Archetype == "" {
		m.Source.Archetype = archetypePull
	}
	if m.Source.Archetype != archetypePull {
		return fmt.Errorf("unsupported source.archetype %q (only %q is supported)", m.Source.Archetype, archetypePull)
	}
	return nil
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
