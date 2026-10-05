# Platform layer

`platform/` contains only what the standard library does not already
abstract, kept deliberately thin.

## Current contents

| Concern | macOS/Linux | Windows |
|---|---|---|
| IPC listener | unix domain socket (`$TMPDIR/gostalgia-<hash>.sock`, derived from the root to respect the 104-byte socket path limit) | loopback TCP, ephemeral port |
| IPC dial | `unix://` scheme | `tcp://` scheme |

Both are selected by build tags (`//go:build unix`, `//go:build windows`).
Everything else — signals, filesystem, exec, clocks, randomness — is already
portable in the standard library and is used directly.

## Rules

1. No `GOOS` conditionals outside `platform/` (and, rarely, build-tagged files
   next to the subsystem that owns the concern).
2. Platform adapters implement the same functions on every target; callers
   stay portable.
3. If an adapter file grows, the concern probably belongs in the subsystem it
   serves — move the portable part back out.

## Planned additions

- Windowing/input/audio adapters once the desktop milestone picks a toolkit.
- Named-pipe listener option for Windows.
- Default-path resolution per platform if conventions diverge.
