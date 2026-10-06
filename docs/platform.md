# Platform layer

`platform/` contains only what the standard library does not already
abstract, kept deliberately thin.

## Current contents

| Concern | macOS/Linux | Windows |
|---|---|---|
| IPC listener | unix domain socket (`$TMPDIR/gostalgia-<hash>.sock`, derived from the root to respect the 104-byte socket path limit) | loopback TCP, ephemeral port |
| IPC dial | `unix://` scheme | `tcp://` scheme |
| Shutdown signal set | `os.Interrupt`, `SIGTERM` | `os.Interrupt` only (Ctrl-C / console close) |

Both are selected by build tags (`//go:build unix`, `//go:build windows`).
Signals are the exception to portability, not an accident of it: the set of
terminating signals a host delivers differs per platform, so the requested
set lives behind build tags here. On Windows, termination paths other than
Ctrl-C / console close (`taskkill /f`, job-object teardown) deliver no
signal at all and give the process no callback — a **known gap** until
platform parity and host integration ([#38](https://github.com/drawmeanelephant/gostalgia/issues/38),
[#44](https://github.com/drawmeanelephant/gostalgia/issues/44)) add service/job-object
integration; until then, Windows shutdown is graceful via Ctrl-C or IPC
only. Everything else — filesystem, exec, clocks, randomness — is already
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
