package onchain

import (
	"context"
	"errors"
	"math/big"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/paulmeier/kasas/internal/vault"
)

// testNormalize lowercases and trims, rejecting the empty string and the literal
// "bad" so validation paths can be exercised.
func testNormalize(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || strings.EqualFold(s, "bad") {
		return "", errors.New("invalid address")
	}
	return strings.ToLower(s), nil
}

func testLabel(s string) string { return "L:" + s }

func newBook(t *testing.T, config ...string) (*AddressBook, vault.SecretStore) {
	t.Helper()
	store := vault.NewFileStore(filepath.Join(t.TempDir(), "secrets.json"))
	return NewAddressBook(store, "test_addresses", config, testNormalize, testLabel), store
}

func TestDecimalFromBaseUnits(t *testing.T) {
	cases := []struct {
		name     string
		value    string // decimal integer (base units)
		decimals int
		want     string
	}{
		{"btc whole", "200000000", 8, "2"},
		{"btc one-and-a-half", "150000000", 8, "1.5"},
		{"btc small", "100000", 8, "0.001"},
		{"btc one sat", "1", 8, "0.00000001"},
		{"btc negative half", "-50000000", 8, "-0.5"},
		{"zero", "0", 8, "0"},
		{"eth whole", "1000000000000000000", 18, "1"},
		{"eth one-and-a-half", "1500000000000000000", 18, "1.5"},
		{"eth gas fee", "420000000000000", 18, "0.00042"},
		{"eth negative tiny", "-21000000000000", 18, "-0.000021"},
		{"no decimals passes through", "42", 0, "42"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := new(big.Int).SetString(tc.value, 10)
			require.True(t, ok)
			assert.Equal(t, tc.want, DecimalFromBaseUnits(v, tc.decimals))
		})
	}
}

func TestTruncateMiddle(t *testing.T) {
	assert.Equal(t, "abcdef…klmnop", TruncateMiddle("abcdefghijklmnop", 6, 6))
	assert.Equal(t, "0x1234…cdef", TruncateMiddle("0x1234567890abcdef", 6, 4))
	assert.Equal(t, "short", TruncateMiddle("short", 8, 6), "shorter than head+tail is unchanged")
}

func TestSplitList(t *testing.T) {
	assert.Equal(t, []string{"a", "b", "c"}, SplitList("a\nb\nc"))
	assert.Equal(t, []string{"a", "b"}, SplitList(" a , b "), "comma or newline, trimmed")
	assert.Equal(t, []string{"a"}, SplitList("a\n\na\n"), "dedupes and drops empties")
	assert.Empty(t, SplitList("   "))
}

func TestAddressID(t *testing.T) {
	id := AddressID("0xabc")
	assert.Len(t, id, 12)
	assert.Equal(t, id, AddressID("0xabc"), "stable")
	assert.NotEqual(t, id, AddressID("0xdef"), "distinct per address")
}

func TestAddressBookConfiguredAndResolve(t *testing.T) {
	ctx := context.Background()

	t.Run("empty until an address is added", func(t *testing.T) {
		book, _ := newBook(t)
		ok, err := book.Configured(ctx)
		require.NoError(t, err)
		assert.False(t, ok)
	})

	t.Run("config addresses are normalized and validated", func(t *testing.T) {
		book, _ := newBook(t, "ABC", "bad", "  DEF  ")
		got, err := book.Resolve(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"abc", "def"}, got, "lowercased, invalid dropped")
	})

	t.Run("unions config and stored, deduped, config first", func(t *testing.T) {
		book, store := newBook(t, "shared", "config_only")
		require.NoError(t, store.SetSecretValue(ctx, "test_addresses", "shared\nstored_only"))
		got, err := book.Resolve(ctx)
		require.NoError(t, err)
		assert.Equal(t, []string{"shared", "config_only", "stored_only"}, got)
	})
}

func TestAddressBookAdd(t *testing.T) {
	ctx := context.Background()
	book, store := newBook(t)

	require.NoError(t, book.Add(ctx, "  ABC  "))
	require.NoError(t, book.Add(ctx, "def"))

	stored, err := store.SecretValue(ctx, "test_addresses")
	require.NoError(t, err)
	assert.Equal(t, "abc\ndef", stored, "normalized and accumulated, not replaced")

	require.NoError(t, book.Add(ctx, "ABC"), "re-adding (different case) is a no-op after normalization")
	stored, _ = store.SecretValue(ctx, "test_addresses")
	assert.Equal(t, "abc\ndef", stored)

	require.Error(t, book.Add(ctx, "bad"), "an invalid address is rejected")
	require.Error(t, book.Add(ctx, "   "), "an empty address is rejected")
}

func TestAddressBookListAndRemove(t *testing.T) {
	ctx := context.Background()
	book, _ := newBook(t, "config_addr")
	require.NoError(t, book.Add(ctx, "run_a"))
	require.NoError(t, book.Add(ctx, "run_b"))

	entries, err := book.List(ctx)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	byID := map[string]struct {
		removable bool
		label     string
	}{}
	for _, e := range entries {
		byID[e.ID] = struct {
			removable bool
			label     string
		}{e.Removable, e.Label}
	}
	assert.False(t, byID[AddressID("config_addr")].removable, "config address is not removable")
	assert.True(t, byID[AddressID("run_a")].removable)
	assert.Equal(t, "L:run_a", byID[AddressID("run_a")].label, "label uses the chain labeler")

	require.NoError(t, book.Remove(ctx, AddressID("run_a")))
	entries, _ = book.List(ctx)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.NotEqual(t, AddressID("run_a"), e.ID, "removed address is gone")
	}

	require.Error(t, book.Remove(ctx, AddressID("config_addr")), "a config address is not removable")
	require.Error(t, book.Remove(ctx, "deadbeefdead"), "removing an unknown id errors")
}
