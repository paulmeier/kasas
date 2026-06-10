package settings

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/paulmeier/kasas/internal/config"
	"github.com/paulmeier/kasas/internal/db"
	"github.com/paulmeier/kasas/internal/vault"
)

// ErrUnknownKey reports a settings key that is not editable (not in the
// registry). The API maps it to a 404.
var ErrUnknownKey = errors.New("unknown setting")

// Status is one setting's full state for the API and dashboard: its definition
// metadata plus the stored/effective values. Value is the value that will be
// effective at the NEXT boot (the stored override when present, otherwise the
// config file / environment value); for a secret it is always empty and Set
// reports whether one is configured. RestartRequired means the next-boot value
// differs from what the running process was built with.
type Status struct {
	Key             string   `json:"key"`
	Title           string   `json:"title"`
	Help            string   `json:"help,omitempty"`
	Kind            Kind     `json:"kind"`
	Secret          bool     `json:"secret,omitempty"`
	Source          string   `json:"source,omitempty"`
	Section         string   `json:"section,omitempty"`
	Enum            []string `json:"enum,omitempty"`
	Value           string   `json:"value"`
	Set             bool     `json:"set"`
	Overridden      bool     `json:"overridden"`
	RestartRequired bool     `json:"restart_required"`
}

// Service reads and writes persisted setting overrides. It holds two resolved
// configs: base is the config file / environment alone, and boot is what the
// running process was actually built with (base + the overrides present at
// startup). Comparing a stored value against boot yields "restart required";
// comparing against base yields "overridden".
type Service struct {
	store   db.Store
	secrets vault.SecretStore
	base    *config.Config
	boot    *config.Config
	now     func() time.Time
}

// NewService constructs a Service. base must be the pre-override config and
// boot the effective one the process is running with.
func NewService(store db.Store, secrets vault.SecretStore, base, boot *config.Config) *Service {
	return &Service{store: store, secrets: secrets, base: base, boot: boot, now: time.Now}
}

// List returns the status of every editable setting plus whether any change is
// waiting for a restart.
func (s *Service) List(ctx context.Context) ([]Status, bool, error) {
	overrides, err := LoadOverrides(ctx, s.store, s.secrets)
	if err != nil {
		return nil, false, err
	}
	out := make([]Status, 0, len(definitions))
	restart := false
	for _, def := range definitions {
		st := s.statusFor(def, overrides)
		restart = restart || st.RestartRequired
		out = append(out, st)
	}
	return out, restart, nil
}

// RestartRequired reports whether any stored setting differs from the value the
// running process was built with.
func (s *Service) RestartRequired(ctx context.Context) (bool, error) {
	_, restart, err := s.List(ctx)
	return restart, err
}

// Set validates and persists one setting override. The value is parsed and the
// resulting full configuration validated before anything is written, so a bad
// value can never be stored. The stored form is the definition's canonical
// string (e.g. durations normalize "90m" -> "1h30m0s"). It returns the
// setting's new status plus whether any setting now awaits a restart.
func (s *Service) Set(ctx context.Context, key, value string) (Status, bool, error) {
	def, ok := Lookup(key)
	if !ok {
		return Status{}, false, fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	overrides, err := LoadOverrides(ctx, s.store, s.secrets)
	if err != nil {
		return Status{}, false, err
	}

	// Build the candidate next-boot config: base + every other stored override +
	// this value, then validate the combination.
	cand := Clone(s.base)
	for _, d := range definitions {
		if d.Key == key {
			continue
		}
		if raw, ok := overrides[d.Key]; ok {
			_ = d.Set(cand, raw) // a stale bad override is skipped at boot too
		}
	}
	if err := def.Set(cand, value); err != nil {
		return Status{}, false, err
	}
	if err := cand.Validate(); err != nil {
		return Status{}, false, err
	}

	normalized := def.Get(cand)
	if def.Secret {
		if err := s.secrets.SetSecretValue(ctx, secretKeyPrefix+def.Key, normalized); err != nil {
			return Status{}, false, fmt.Errorf("store secret setting: %w", err)
		}
	} else {
		if err := s.store.UpsertSetting(ctx, db.UpsertSettingParams{
			Key: def.Key, Value: normalized, UpdatedAt: s.now().Unix(),
		}); err != nil {
			return Status{}, false, fmt.Errorf("store setting: %w", err)
		}
	}

	if normalized == "" && def.Secret {
		// The secret store deletes on empty, which is a reset.
		delete(overrides, def.Key)
	} else {
		overrides[def.Key] = normalized
	}
	return s.statusFor(def, overrides), s.anyRestart(overrides), nil
}

// Reset removes a stored override so the config file / environment value is
// effective again at the next boot. Resetting a key with no override is a
// no-op. It returns the setting's new status plus whether any setting still
// awaits a restart.
func (s *Service) Reset(ctx context.Context, key string) (Status, bool, error) {
	def, ok := Lookup(key)
	if !ok {
		return Status{}, false, fmt.Errorf("%w: %s", ErrUnknownKey, key)
	}
	if def.Secret {
		if err := s.secrets.SetSecretValue(ctx, secretKeyPrefix+def.Key, ""); err != nil {
			return Status{}, false, fmt.Errorf("clear secret setting: %w", err)
		}
	} else {
		if _, err := s.store.DeleteSetting(ctx, def.Key); err != nil {
			return Status{}, false, fmt.Errorf("delete setting: %w", err)
		}
	}
	overrides, err := LoadOverrides(ctx, s.store, s.secrets)
	if err != nil {
		return Status{}, false, err
	}
	return s.statusFor(def, overrides), s.anyRestart(overrides), nil
}

// statusFor assembles one setting's status from the current overrides.
func (s *Service) statusFor(def Definition, overrides map[string]string) Status {
	next, overridden := overrides[def.Key]
	if !overridden {
		next = def.Get(s.base)
	}
	bootVal := def.Get(s.boot)

	st := Status{
		Key:             def.Key,
		Title:           def.Title,
		Help:            def.Help,
		Kind:            def.Kind,
		Secret:          def.Secret,
		Source:          def.Source,
		Section:         def.Section,
		Enum:            def.Enum,
		Value:           next,
		Set:             next != "",
		Overridden:      overridden,
		RestartRequired: next != bootVal,
	}
	if def.Secret {
		st.Value = "" // never echo a secret
	}
	return st
}

func (s *Service) anyRestart(overrides map[string]string) bool {
	for _, def := range definitions {
		if s.statusFor(def, overrides).RestartRequired {
			return true
		}
	}
	return false
}
