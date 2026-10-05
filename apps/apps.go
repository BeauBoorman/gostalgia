// Package apps wires the environment's builtin applications: their
// manifest + factory registrations and the seeding of manifest files into
// the VFS.
package apps

import (
	"encoding/json"
	"fmt"

	"gostalgia/apps/echo"
	"gostalgia/internal/app"
	"gostalgia/internal/vfs"
)

// Register registers every builtin application with the registry.
func Register(r *app.Registry) error {
	if err := r.RegisterBuiltin(echo.Manifest(), echo.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", echo.ID, err)
	}
	return nil
}

// SeedManifests writes each builtin application's manifest into
// /apps/manifests (if not already present), so installed applications are
// visible as data, not just code.
func SeedManifests(env vfs.FS) error {
	for _, m := range []app.Manifest{echo.Manifest()} {
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
