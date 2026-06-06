package apikeys

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerate(t *testing.T) {
	full, prefix, hash, err := Generate()
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(full, "kasas_"), "key should carry the kasas_ prefix")
	assert.True(t, strings.HasPrefix(full, prefix), "prefix should be a leading fragment of the key")
	assert.Equal(t, Hash(full), hash, "returned hash should be the SHA-256 of the full key")
	assert.NotContains(t, hash, full, "the hash must not embed the secret")
	assert.Less(t, len(prefix), len(full), "the prefix must reveal only part of the key")
}

func TestGenerateUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		full, _, hash, err := Generate()
		require.NoError(t, err)
		require.False(t, seen[full], "generated a duplicate key")
		require.False(t, seen[hash], "generated a duplicate hash")
		seen[full] = true
		seen[hash] = true
	}
}

func TestHashStable(t *testing.T) {
	const key = "kasas_abc123"
	assert.Equal(t, Hash(key), Hash(key), "hashing is deterministic")
	assert.NotEqual(t, Hash(key), Hash(key+"x"), "different keys hash differently")
	assert.Len(t, Hash(key), 64, "SHA-256 hex is 64 characters")
}

func TestParseScope(t *testing.T) {
	cases := []struct {
		in      string
		want    Scope
		wantErr bool
	}{
		{"", ScopeRead, false}, // default is least privilege
		{"read", ScopeRead, false},
		{"read_write", ScopeReadWrite, false},
		{"  read_write  ", ScopeReadWrite, false},
		{"admin", "", true},
		{"write", "", true},
	}
	for _, tc := range cases {
		got, err := ParseScope(tc.in)
		if tc.wantErr {
			assert.Error(t, err, "ParseScope(%q)", tc.in)
			continue
		}
		require.NoError(t, err, "ParseScope(%q)", tc.in)
		assert.Equal(t, tc.want, got, "ParseScope(%q)", tc.in)
	}
}

func TestScopeSatisfies(t *testing.T) {
	assert.True(t, ScopeReadWrite.Satisfies(ScopeRead), "read_write covers read")
	assert.True(t, ScopeReadWrite.Satisfies(ScopeReadWrite), "read_write covers read_write")
	assert.True(t, ScopeRead.Satisfies(ScopeRead), "read covers read")
	assert.False(t, ScopeRead.Satisfies(ScopeReadWrite), "read does not cover read_write")
}
