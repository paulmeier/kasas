package plugins

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseManifestValid(t *testing.T) {
	m, err := ParseManifest([]byte(`
name = "budgeting"
version = "1.2.3"
runtime = "lua"
hooks = ["OnTransactionCreate", "OnSyncComplete"]
capabilities = ["labels:write"]

[config]
keyword = "coffee"
threshold = 42
`))
	require.NoError(t, err)
	assert.Equal(t, "budgeting", m.Name)
	assert.Equal(t, "lua", m.Runtime)
	assert.Equal(t, "main.lua", m.Entrypoint, "entrypoint defaults to main.lua")
	assert.ElementsMatch(t, []Hook{HookTransactionCreate, HookSyncComplete}, m.Hooks)
	assert.ElementsMatch(t, []Capability{CapLabelsWrite}, m.Capabilities)
	assert.Equal(t, "coffee", m.Config["keyword"])
}

func TestParseManifestJSRuntime(t *testing.T) {
	m, err := ParseManifest([]byte(`
name = "notifier"
runtime = "js"
hooks = ["OnTransactionCreate"]
`))
	require.NoError(t, err)
	assert.Equal(t, "js", m.Runtime)
	assert.Equal(t, "main.js", m.Entrypoint, "a js plugin's entrypoint defaults to main.js")

	// TypeScript plugins set the entrypoint explicitly; it is preserved.
	ts, err := ParseManifest([]byte(`
name = "notifier"
runtime = "js"
entrypoint = "main.ts"
hooks = ["OnTransactionCreate"]
`))
	require.NoError(t, err)
	assert.Equal(t, "main.ts", ts.Entrypoint)
}

func TestParseManifestRejects(t *testing.T) {
	cases := map[string]string{
		"missing name":         `runtime = "lua"` + "\n" + `hooks = ["OnTransactionCreate"]`,
		"bad name":             `name = "Bad Name"` + "\n" + `runtime = "lua"` + "\n" + `hooks = ["OnTransactionCreate"]`,
		"unknown runtime":      `name = "p"` + "\n" + `runtime = "ruby"` + "\n" + `hooks = ["OnTransactionCreate"]`,
		"no hooks":             `name = "p"` + "\n" + `runtime = "lua"`,
		"unknown hook":         `name = "p"` + "\n" + `runtime = "lua"` + "\n" + `hooks = ["OnWhatever"]`,
		"unknown capability":   `name = "p"` + "\n" + `runtime = "lua"` + "\n" + `hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["root:all"]`,
		"path-like entrypoint": `name = "p"` + "\n" + `runtime = "lua"` + "\n" + `hooks = ["OnTransactionCreate"]` + "\n" + `entrypoint = "../escape.lua"`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(src))
			assert.Error(t, err)
		})
	}
}

func TestParseManifestMalformedTOML(t *testing.T) {
	_, err := ParseManifest([]byte(`name = "p"` + "\n" + `runtime = lua`)) // unquoted value
	assert.Error(t, err)
}

func TestParseManifestNetFetchValid(t *testing.T) {
	m, err := ParseManifest([]byte(`
name = "importer"
runtime = "lua"
hooks = ["OnTransactionCreate"]
capabilities = ["net:fetch"]

[net]
allow = ["Paperless.LAN", "api.merchant.example.com", "paperless.lan"]
`))
	require.NoError(t, err)
	require.NotNil(t, m.Net)
	// Hosts are lowercased and de-duplicated, in declared order.
	assert.Equal(t, []string{"paperless.lan", "api.merchant.example.com"}, m.Net.Allow)
}

func TestParseManifestNetFetchRejects(t *testing.T) {
	cases := map[string]string{
		"net:fetch without [net]": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["net:fetch"]`,
		"net:fetch with empty allow": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["net:fetch"]` + "\n" + `[net]` + "\n" + `allow = []`,
		"[net] without net:fetch": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `[net]` + "\n" + `allow = ["x.example.com"]`,
		"host with scheme": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["net:fetch"]` + "\n" + `[net]` + "\n" + `allow = ["https://x.example.com"]`,
		"host with port": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["net:fetch"]` + "\n" + `[net]` + "\n" + `allow = ["x.example.com:443"]`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(src))
			assert.Error(t, err)
		})
	}
}

func TestParseManifestSourceProvideValid(t *testing.T) {
	m, err := ParseManifest([]byte(`
name = "acme-card"
runtime = "lua"
hooks = ["OnFetch"]
capabilities = ["source:provide"]

[source]
type = "acme-card"
`))
	require.NoError(t, err)
	require.NotNil(t, m.Source)
	assert.Equal(t, "acme-card", m.Source.Type)
	assert.Equal(t, "pull", m.Source.Archetype, "archetype defaults to pull")
}

func TestParseManifestSourceProvideWithNetFetch(t *testing.T) {
	// A remote-pulling producer is the canonical net:fetch + source:provide combo.
	m, err := ParseManifest([]byte(`
name = "acme-card"
runtime = "wasm"
hooks = ["OnFetch"]
capabilities = ["net:fetch", "source:provide"]

[net]
allow = ["api.acme.example"]

[source]
type = "acme-card"
archetype = "pull"
`))
	require.NoError(t, err)
	require.NotNil(t, m.Source)
	require.NotNil(t, m.Net)
}

func TestParseManifestSourceProvideRejects(t *testing.T) {
	cases := map[string]string{
		"source:provide without [source]": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnFetch"]` + "\n" + `capabilities = ["source:provide"]`,
		"[source] without source:provide": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnFetch"]` + "\n" + `[source]` + "\n" + `type = "x"`,
		"source:provide without OnFetch": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnTransactionCreate"]` + "\n" + `capabilities = ["source:provide"]` + "\n" + `[source]` + "\n" + `type = "x"`,
		"OnFetch without [source]": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnFetch"]`,
		"empty source.type": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnFetch"]` + "\n" + `capabilities = ["source:provide"]` + "\n" + `[source]` + "\n" + `type = ""`,
		"unsupported archetype": `name = "p"` + "\n" + `runtime = "lua"` + "\n" +
			`hooks = ["OnFetch"]` + "\n" + `capabilities = ["source:provide"]` + "\n" + `[source]` + "\n" + `type = "x"` + "\n" + `archetype = "webhook"`,
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(src))
			assert.Error(t, err)
		})
	}
}
