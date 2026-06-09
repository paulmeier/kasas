// Package onchain provides the chain-agnostic building blocks shared by kasas's
// on-chain ingestion sources (Bitcoin, Ethereum, and future chains).
//
// A watched address behaves like one credential of a multi-credential source — it
// is added and removed individually and the source fans out over the whole set on
// each sync — so the shared piece is the address-set management ([AddressBook]).
// Exact base-unit→decimal conversion ([DecimalFromBaseUnits]) and a readable
// address label ([TruncateMiddle]) round it out.
//
// Each chain supplies only what differs: how to validate/normalize one of its
// addresses ([Normalizer]) and how to label it ([Labeler]). Everything else —
// storage, dedup, the union of config-declared and runtime-added addresses, and
// stable ids for removal — lives here, so a new chain source is just its API
// client and transaction mapping.
package onchain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"

	"github.com/paulmeier/kasas/internal/source"
	"github.com/paulmeier/kasas/internal/vault"
)

// Normalizer validates and canonicalizes one address, returning its normalized form
// (e.g. a lowercased Ethereum address) or an error if the input is not a valid
// address for the chain. Normalizing makes storage, dedup, and ids stable
// regardless of the casing a user pastes.
type Normalizer func(string) (string, error)

// Labeler renders an address for display. Addresses are public, so — unlike a
// secret token — this shows a readable (typically middle-truncated) form, not a
// mask.
type Labeler func(string) string

// AddressBook manages a source's set of watched addresses as a multi-credential
// set: addresses declared in config (not removable) unioned with addresses added at
// runtime (removable), persisted newline-joined in the secret store under storeKey.
// It implements the storage half of source.Credentialed and
// source.MultiCredentialed for any chain; the embedding source delegates to it.
type AddressBook struct {
	secrets   vault.SecretStore
	storeKey  string
	config    []string // normalized config-declared addresses (not removable)
	normalize Normalizer
	label     Labeler
}

// NewAddressBook builds an AddressBook over secrets, persisting runtime additions
// under storeKey. configAddrs are the config-declared addresses; they are normalized
// here and any that fail validation are dropped (config is the operator's
// responsibility — an invalid entry is simply not watched).
func NewAddressBook(secrets vault.SecretStore, storeKey string, configAddrs []string, normalize Normalizer, label Labeler) *AddressBook {
	b := &AddressBook{secrets: secrets, storeKey: storeKey, normalize: normalize, label: label}
	b.config = b.normalizeAll(configAddrs)
	return b
}

// Resolve returns the effective watched addresses: the union of config-declared and
// runtime-stored addresses, normalized and deduped (config first, for a stable
// fan-out order).
func (b *AddressBook) Resolve(ctx context.Context) ([]string, error) {
	stored, err := b.stored(ctx)
	if err != nil {
		return nil, err
	}
	all := make([]string, 0, len(b.config)+len(stored))
	all = append(all, b.config...)
	all = append(all, stored...)
	return dedupe(all), nil
}

// Configured reports whether at least one address is watched, so a sync can run.
func (b *AddressBook) Configured(ctx context.Context) (bool, error) {
	addrs, err := b.Resolve(ctx)
	if err != nil {
		return false, err
	}
	return len(addrs) > 0, nil
}

// Add validates and appends one address to the runtime-stored set (deduped), so each
// call watches one more address. An invalid address is rejected; re-adding an
// existing one is a no-op.
func (b *AddressBook) Add(ctx context.Context, input string) error {
	addr, err := b.normalize(input)
	if err != nil {
		return err
	}
	stored, err := b.stored(ctx)
	if err != nil {
		return err
	}
	return b.setStored(ctx, append(stored, addr))
}

// List returns one entry per watched address — config-declared (not removable) and
// runtime-added (removable) — each labeled for display (the address is public, so
// the label is the readable address, not a mask).
func (b *AddressBook) List(ctx context.Context) ([]source.CredentialEntry, error) {
	stored, err := b.stored(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]source.CredentialEntry, 0, len(b.config)+len(stored))
	seen := make(map[string]bool)
	add := func(addr string, removable bool) {
		id := AddressID(addr)
		if seen[id] {
			return
		}
		seen[id] = true
		entries = append(entries, source.CredentialEntry{ID: id, Label: b.label(addr), Removable: removable})
	}
	for _, a := range b.config {
		add(a, false)
	}
	for _, a := range stored {
		add(a, true)
	}
	return entries, nil
}

// Remove deletes the runtime-added address with the given id. A config-declared
// address is not removable here (edit the config instead); an unknown id is an
// error.
func (b *AddressBook) Remove(ctx context.Context, id string) error {
	stored, err := b.stored(ctx)
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(stored))
	found := false
	for _, a := range stored {
		if AddressID(a) == id {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		return fmt.Errorf("no removable address with id %q", id)
	}
	return b.setStored(ctx, kept)
}

// stored reads and normalizes the runtime-managed address set from the secret store.
func (b *AddressBook) stored(ctx context.Context) ([]string, error) {
	raw, err := b.secrets.SecretValue(ctx, b.storeKey)
	if err != nil {
		return nil, fmt.Errorf("read stored addresses: %w", err)
	}
	return b.normalizeAll(SplitList(raw)), nil
}

// setStored persists the runtime-managed address set, deduped.
func (b *AddressBook) setStored(ctx context.Context, addrs []string) error {
	return b.secrets.SetSecretValue(ctx, b.storeKey, strings.Join(dedupe(addrs), "\n"))
}

// normalizeAll normalizes a list, dropping entries that fail validation, and dedupes.
func (b *AddressBook) normalizeAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if n, err := b.normalize(a); err == nil {
			out = append(out, n)
		}
	}
	return dedupe(out)
}

// AddressID derives a stable, collision-resistant id for a (normalized) address, used
// to remove it by id from the API. Because it is over the normalized form, the id a
// List entry carries always matches the one Remove is called with.
func AddressID(addr string) string {
	sum := sha256.Sum256([]byte(addr))
	return hex.EncodeToString(sum[:])[:12]
}

// SplitList parses a newline- or comma-separated address list into a slice, trimming
// whitespace and dropping empties. It is used both for the config option string
// (passed through the registry env) and the stored secret value.
func SplitList(s string) []string {
	return dedupe(strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' || r == ',' }))
}

// dedupe trims, drops empties, and removes duplicates, preserving order.
func dedupe(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool)
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// DecimalFromBaseUnits renders an integer amount of base units (e.g. satoshis or
// wei) as an exact decimal string with the given number of fractional digits. It
// uses math/big throughout — no float — so the value is never rounded. Negative and
// zero values are handled, and trailing fractional zeros are trimmed (keeping at
// least the integer part): e.g. (150000000, 8) -> "1.5", (100000, 8) -> "0.001",
// (200000000, 8) -> "2", (0, 18) -> "0".
func DecimalFromBaseUnits(v *big.Int, decimals int) string {
	if decimals <= 0 {
		return v.String()
	}
	neg := v.Sign() < 0
	abs := new(big.Int).Abs(v)

	base := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	intPart, fracPart := new(big.Int).QuoRem(abs, base, new(big.Int))

	out := intPart.String()
	if fracPart.Sign() != 0 {
		frac := fracPart.String()
		if len(frac) < decimals {
			frac = strings.Repeat("0", decimals-len(frac)) + frac
		}
		out += "." + strings.TrimRight(frac, "0")
	}
	if neg && (intPart.Sign() != 0 || fracPart.Sign() != 0) {
		out = "-" + out
	}
	return out
}

// TruncateMiddle shortens s to head + "…" + tail characters when it is longer than
// that, for a readable address label (e.g. "bc1qar0s…ej5mdq"). Shorter strings are
// returned unchanged.
func TruncateMiddle(s string, head, tail int) string {
	if len(s) <= head+tail+1 {
		return s
	}
	return s[:head] + "…" + s[len(s)-tail:]
}
