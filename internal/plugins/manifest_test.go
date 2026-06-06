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
	assert.Equal(t, defaultEntrypoint, m.Entrypoint, "entrypoint defaults to main.lua")
	assert.ElementsMatch(t, []Hook{HookTransactionCreate, HookSyncComplete}, m.Hooks)
	assert.ElementsMatch(t, []Capability{CapLabelsWrite}, m.Capabilities)
	assert.Equal(t, "coffee", m.Config["keyword"])
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
