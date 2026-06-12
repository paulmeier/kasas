package market

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SeriesSettingKey is the settings-table key the configured series list is
// persisted under (as a JSON array). It is stored directly rather than through a
// settings Definition so changes apply live without tripping the restart banner.
const SeriesSettingKey = "market.series"

// ParseSpecs decodes the market.series setting (a JSON array of series specs) into
// normalized specs. An empty/blank value is an empty list (no configured series).
func ParseSpecs(raw string) ([]SeriesSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var specs []SeriesSpec
	if err := json.Unmarshal([]byte(raw), &specs); err != nil {
		return nil, fmt.Errorf("market.series is not a JSON array of series: %w", err)
	}
	out := make([]SeriesSpec, 0, len(specs))
	seen := map[string]bool{}
	for _, sp := range specs {
		ns, err := NormalizeSpec(sp)
		if err != nil {
			return nil, err
		}
		if seen[ns.ID] {
			return nil, fmt.Errorf("duplicate series id %q", ns.ID)
		}
		seen[ns.ID] = true
		out = append(out, ns)
	}
	return out, nil
}

// MarshalSpecs encodes specs as the JSON array stored in the market.series setting.
func MarshalSpecs(specs []SeriesSpec) (string, error) {
	if len(specs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(specs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// NormalizeSpec validates and canonicalizes a single spec (trimming, defaulting,
// upper-casing the currency, defaulting the kind to equity).
func NormalizeSpec(spec SeriesSpec) (SeriesSpec, error) {
	spec.ID = strings.TrimSpace(spec.ID)
	spec.Symbol = strings.TrimSpace(spec.Symbol)
	spec.Currency = strings.ToUpper(strings.TrimSpace(spec.Currency))
	spec.Name = strings.TrimSpace(spec.Name)
	if spec.ID == "" {
		return spec, errors.New("series id is required")
	}
	if !validID(spec.ID) {
		return spec, fmt.Errorf("series id %q must be lowercase letters, digits, '-' or '_'", spec.ID)
	}
	if spec.Symbol == "" {
		return spec, errors.New("series symbol is required")
	}
	if spec.Kind == "" {
		spec.Kind = KindEquity
	}
	if !ValidKind(spec.Kind) {
		return spec, fmt.Errorf("unknown series kind %q (want equity, fund, index, fx, or crypto)", spec.Kind)
	}
	if spec.Currency == "" {
		spec.Currency = "USD"
	}
	return spec, nil
}

// validID reports whether id is a clean internal id: lowercase letters, digits,
// '-' or '_'. It is used in URL paths, so it must not contain slashes or spaces.
func validID(id string) bool {
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= '0' && r <= '9':
		case r == '-' || r == '_':
		default:
			return false
		}
	}
	return id != ""
}
