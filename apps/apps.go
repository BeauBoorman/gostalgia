// Package apps wires the environment's builtin applications: their
// manifest + factory registrations. The runtime seeds manifest files.
package apps

import (
	"fmt"

	"gostalgia/apps/calculator"
	"gostalgia/apps/compendium"
	"gostalgia/apps/echo"
	"gostalgia/apps/files"
	"gostalgia/apps/notes"
	"gostalgia/apps/petwatch"
	"gostalgia/apps/settings"
	"gostalgia/sdk"
)

// Registrar is bootstrap wiring, not a runtime manager exposed to an app.
type Registrar interface {
	RegisterBuiltin(sdk.Manifest, sdk.Factory) error
}

// Register registers every builtin application with the registry.
func Register(r Registrar) error {
	if err := r.RegisterBuiltin(echo.Manifest(), echo.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", echo.ID, err)
	}
	if err := r.RegisterBuiltin(notes.Manifest(), notes.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", notes.ID, err)
	}
	if err := r.RegisterBuiltin(files.Manifest(), files.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", files.ID, err)
	}
	if err := r.RegisterBuiltin(settings.Manifest(), settings.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", settings.ID, err)
	}
	if err := r.RegisterBuiltin(calculator.Manifest(), calculator.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", calculator.ID, err)
	}
	if err := r.RegisterBuiltin(compendium.Manifest(), compendium.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", compendium.ID, err)
	}
	if err := r.RegisterBuiltin(petwatch.Manifest(), petwatch.Factory); err != nil {
		return fmt.Errorf("apps: register %s: %w", petwatch.ID, err)
	}
	return nil
}

// Manifests returns fresh builtin declarations, without filesystem access.
func Manifests() []sdk.Manifest {
	return []sdk.Manifest{echo.Manifest(), notes.Manifest(), files.Manifest(), settings.Manifest(), calculator.Manifest(), compendium.Manifest(), petwatch.Manifest()}
}
