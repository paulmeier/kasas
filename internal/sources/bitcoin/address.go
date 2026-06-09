package bitcoin

import (
	"fmt"
	"strings"
)

// bech32Charset is the bech32/bech32m data alphabet (BIP-173). A native-segwit
// address is "bc1" followed only by these characters.
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// base58Charset is the Bitcoin base58 alphabet (no 0, O, I, l). Legacy (1…) and
// P2SH (3…) addresses are base58check-encoded with this alphabet.
const base58Charset = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// normalizeBTC validates a Bitcoin address structurally and returns its canonical
// form: bech32 addresses ("bc1…", mainnet native segwit) are lowercased; legacy
// ("1…") and P2SH ("3…") base58 addresses are returned as-is (their case is
// significant). This is a format check — length, prefix, and alphabet — not a full
// base58check / bech32 checksum verification; the node is the final arbiter, so a
// well-formed-but-nonexistent address simply yields no transactions.
func normalizeBTC(addr string) (string, error) {
	a := strings.TrimSpace(addr)
	if a == "" {
		return "", fmt.Errorf("a Bitcoin address is required")
	}

	if low := strings.ToLower(a); strings.HasPrefix(low, "bc1") {
		if len(low) < 14 || len(low) > 90 || !onlyChars(low[3:], bech32Charset) {
			return "", fmt.Errorf("invalid bech32 Bitcoin address %q", addr)
		}
		return low, nil
	}

	if strings.HasPrefix(a, "1") || strings.HasPrefix(a, "3") {
		if len(a) < 26 || len(a) > 35 || !onlyChars(a, base58Charset) {
			return "", fmt.Errorf("invalid Bitcoin address %q", addr)
		}
		return a, nil
	}

	return "", fmt.Errorf("unrecognized Bitcoin address %q (expected 1…, 3…, or bc1…)", addr)
}

// onlyChars reports whether every character of s is in the allowed set.
func onlyChars(s, allowed string) bool {
	for _, r := range s {
		if !strings.ContainsRune(allowed, r) {
			return false
		}
	}
	return true
}
