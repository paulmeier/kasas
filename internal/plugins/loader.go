package plugins

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// manifestFile is the per-plugin manifest filename.
const manifestFile = "plugin.toml"

// Discovered is one plugin directory found on disk. Err is non-nil when the
// directory has a manifest that failed to parse/validate or whose entrypoint is
// missing; such a plugin is still surfaced (so the operator sees the error in the
// UI/logs) but is never loaded. Name is always the directory name.
type Discovered struct {
	Name     string
	Dir      string
	Manifest Manifest
	Err      error
}

// Valid reports whether the plugin parsed cleanly and can be loaded.
func (d Discovered) Valid() bool { return d.Err == nil && d.Dir != "" }

// Discover scans dir for plugin subdirectories. Each immediate subdirectory that
// contains a plugin.toml is a plugin. A missing dir is not an error (no plugins
// installed yet). A subdirectory without a manifest is skipped silently; one with
// a broken manifest is returned with Err set rather than aborting the whole scan,
// so a single bad plugin never hides the others.
func Discover(dir string) ([]Discovered, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []Discovered
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		pdir := filepath.Join(dir, name)

		data, rerr := os.ReadFile(filepath.Join(pdir, manifestFile))
		if rerr != nil {
			if errors.Is(rerr, fs.ErrNotExist) {
				continue // a directory without a manifest is not a plugin
			}
			out = append(out, Discovered{Name: name, Dir: pdir, Err: rerr})
			continue
		}

		m, perr := ParseManifest(data)
		if perr != nil {
			out = append(out, Discovered{Name: name, Dir: pdir, Err: perr})
			continue
		}
		if m.Name != name {
			out = append(out, Discovered{Name: name, Dir: pdir, Err: fmt.Errorf("manifest name %q must match directory name %q", m.Name, name)})
			continue
		}
		if _, serr := os.Stat(filepath.Join(pdir, m.Entrypoint)); serr != nil {
			out = append(out, Discovered{Name: name, Dir: pdir, Manifest: m, Err: fmt.Errorf("entrypoint %q not found", m.Entrypoint)})
			continue
		}
		out = append(out, Discovered{Name: name, Dir: pdir, Manifest: m})
	}
	return out, nil
}
