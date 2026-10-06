package app

import (
	"encoding/json"
	"fmt"

	"gostalgia/internal/vfs"
)

// SeedManifests writes builtin declarations into the environment VFS, if
// absent. Filesystem wiring belongs to the runtime, not application packages.
func SeedManifests(env vfs.FS, manifests []Manifest) error {
	for _, m := range manifests {
		b, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return fmt.Errorf("apps: marshal %s: %w", m.ID, err)
		}
		b = append(b, '\n')
		path := "/apps/manifests/" + m.ID + ".json"
		if _, err := env.Stat(path); err == nil {
			continue
		}
		if err := env.WriteFile(path, b, 0o644); err != nil {
			return fmt.Errorf("apps: seed %s: %w", path, err)
		}
	}
	return nil
}
