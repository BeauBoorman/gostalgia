// Package apps wires the environment's builtin applications: their
// manifest + factory registrations. The runtime seeds manifest files.
package apps

import (
	"fmt"

	"gostalgia/apps/echo"
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
	return nil
}

// Manifests returns fresh builtin declarations, without filesystem access.
func Manifests() []sdk.Manifest {
	return []sdk.Manifest{echo.Manifest()}
}
