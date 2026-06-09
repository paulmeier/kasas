package ethereum

import (
	"fmt"
	"strings"
)

// normalizeETH validates an Ethereum address and returns its canonical lowercase form.
// An address is "0x" followed by 40 hex characters. Lowercasing makes storage, dedup,
// and ids stable regardless of EIP-55 mixed-case checksum casing the user may paste; it
// also matches the lowercase addresses Etherscan returns, so direction comparisons hit.
// This is a format check, not an EIP-55 checksum verification.
func normalizeETH(addr string) (string, error) {
	a := strings.ToLower(strings.TrimSpace(addr))
	if a == "" {
		return "", fmt.Errorf("an Ethereum address is required")
	}
	if len(a) != 42 || !strings.HasPrefix(a, "0x") {
		return "", fmt.Errorf("invalid Ethereum address %q (expected 0x + 40 hex characters)", addr)
	}
	for _, r := range a[2:] {
		if !isHex(r) {
			return "", fmt.Errorf("invalid Ethereum address %q (expected 0x + 40 hex characters)", addr)
		}
	}
	return a, nil
}

// isHex reports whether r is a hexadecimal digit.
func isHex(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
