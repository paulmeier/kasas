package source

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/paulmeier/kasas/internal/vault"
)

// Env carries the shared infrastructure the engine hands a source at
// construction. A factory reads what it needs and ignores the rest.
type Env struct {
	// Logger is the source's logger (never nil when constructed via New).
	Logger *slog.Logger
	// Secrets is the credential store. A source persists/reads its own
	// credential here. (Today this is the shared SimpleFIN-era store; per-source
	// credential scoping is a planned follow-up.)
	Secrets vault.SecretStore
	// Options are the source's non-secret config values, keyed by ConfigField /
	// CredentialField key. For SimpleFIN these are "access_url" and "setup_token".
	Options map[string]string
}

// Opt returns the named option, or "" if unset.
func (e Env) Opt(key string) string { return e.Options[key] }

// Factory constructs a source from an Env. Registered by each source's package.
type Factory func(Env) (Source, error)

type registration struct {
	desc    Descriptor
	factory Factory
}

var (
	regMu    sync.RWMutex
	registry = map[string]registration{}
)

// Register adds a source type to the registry. Sources call this from an init()
// so importing the package makes the type available. It panics on a missing type,
// a nil factory, or a duplicate registration — all programmer errors surfaced at
// startup rather than silently dropped.
func Register(desc Descriptor, f Factory) {
	if desc.Type == "" {
		panic("source: Register with empty Descriptor.Type")
	}
	if f == nil {
		panic("source: Register with nil Factory for type " + desc.Type)
	}
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[desc.Type]; dup {
		panic("source: duplicate Register for type " + desc.Type)
	}
	registry[desc.Type] = registration{desc: desc, factory: f}
}

// New constructs a registered source of the given type with env.
func New(typ string, env Env) (Source, error) {
	regMu.RLock()
	r, ok := registry[typ]
	regMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source: unknown type %q", typ)
	}
	if env.Logger == nil {
		env.Logger = slog.Default()
	}
	return r.factory(env)
}

// Descriptors returns the metadata of every registered source, sorted by type,
// for listing available sources across the API surfaces.
func Descriptors() []Descriptor {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Descriptor, 0, len(registry))
	for _, r := range registry {
		out = append(out, r.desc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// Registered reports whether a source type is registered. Mainly for tests.
func Registered(typ string) bool {
	regMu.RLock()
	defer regMu.RUnlock()
	_, ok := registry[typ]
	return ok
}
