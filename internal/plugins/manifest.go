// Package plugins is kasas's plugin runtime: it loads third-party plugins from a
// directory and runs them in a sandboxed language VM, reacting to committed events
// off the event bus (the async, post-commit counterpart to the rules engine and
// webhooks). A plugin declares the lifecycle hooks it implements and the
// capabilities it needs; the manager delivers matching events to a per-plugin
// worker, and a capability-checked host facade is the only way a plugin can read or
// mutate ledger data — so enforcement is identical regardless of the language a
// plugin is written in.
//
// Two runtimes ship today behind the Runtime/Instance adapter seam: Lua (via
// gopher-lua) and JavaScript/TypeScript (via goja, with esbuild stripping TypeScript
// types at load). Go (wazero/WASM) can slot in later as another Runtime
// implementation without touching the manager.
package plugins

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"

	"github.com/paulmeier/kasas/internal/events"
)

// Hook is a lifecycle point a plugin can subscribe to. Each maps to exactly one
// event type (see hookTrigger); the manager only delivers an event to a plugin
// that declared the corresponding hook.
type Hook string

const (
	HookTransactionCreate Hook = "OnTransactionCreate" // <- events.TypeTransactionCreated
	HookTransactionUpdate Hook = "OnTransactionUpdate" // <- events.TypeTransactionUpdated
	HookSyncComplete      Hook = "OnSyncComplete"      // <- events.TypeSyncCompleted
)

// Capability is a permission a plugin must declare and be granted before the host
// will let it perform the corresponding action. Enforcement lives in the host
// facade (host.go), so every runtime inherits it.
type Capability string

const (
	CapTransactionsRead Capability = "transactions:read" // read the triggering txn + run searches
	CapLabelsWrite      Capability = "labels:write"      // apply/remove labels
	CapExtensionsWrite  Capability = "extensions:write"  // set/remove schema extensions
)

// Runtime values for the manifest `runtime` field. Each maps to a registered
// Runtime implementation (NewLuaRuntime / NewJSRuntime); WASM adds its own when it
// lands.
const (
	RuntimeLua = "lua"
	RuntimeJS  = "js"
)

// knownRuntimes is the set of accepted `runtime` values. An unknown runtime is a
// hard manifest error so a typo can't silently fail to load.
var knownRuntimes = map[string]bool{
	RuntimeLua: true,
	RuntimeJS:  true,
}

// defaultEntrypoints is the per-runtime fallback source file when a manifest omits
// `entrypoint`. A JS plugin written in TypeScript sets `entrypoint = "main.ts"`
// explicitly; the JS runtime picks the esbuild loader by file extension.
var defaultEntrypoints = map[string]string{
	RuntimeLua: "main.lua",
	RuntimeJS:  "main.js",
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
}

var knownCapabilities = map[Capability]bool{
	CapTransactionsRead: true,
	CapLabelsWrite:      true,
	CapExtensionsWrite:  true,
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
}

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
	return nil
}
