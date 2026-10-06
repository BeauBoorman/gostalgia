# Processes

The environment's process model lives in `internal/process`.

## The two kinds

| | `inproc` | `child` |
|---|---|---|
| What it is | Application/service logic running inside the runtime | A real host child process |
| Mechanism | goroutine + cancellable context | `os/exec` + `CommandContext` |
| Isolation | logical only (a panic can affect the runtime) | host OS process |
| Exit status | error returned by `Run` | exit code, signal status |
| Stop semantics | context cancellation + timeout | context-driven kill + timeout |

The distinction is explicit in the API (`process.Kind`), in `Spec`, in `Info`,
in IPC payloads, and in `gctl ps` output. Nothing treats a goroutine as an OS
process.

## Lifecycle

States: `starting → running → stopping → stopped` (or `failed`).

- `Manager.StartInProc(ctx, spec, run)` — starts immediately; `run` must honor
  `p.Context()`. `Launch` returns once the process is running.
- `Manager.StartChild(ctx, spec)` — spawns `spec.Args`, inheriting or replacing
  the environment (`spec.Env`), optional working directory.
- `Manager.Stop(id, timeout)` — sets `stopping`, cancels, waits up to the
  timeout. A process that ignores its context leaves Stop with an error and is
  reported as failed at shutdown.
- Exited processes remain listed (with state, exit code, and error) until
  reaping; supervision and crash-loop protection are tracked in Milestone 3
  ([#31](https://github.com/drawmeanelephant/gostalgia/issues/31)).

## Process inspection and IPC (`proc/list`)

The `proc/list` IPC endpoint already returns complete `process.Info` snapshots,
including:
- `id`, `name`, `kind`, and `state`
- `session` and `user` ownership
- `caps` (granted capabilities snapshot)
- `started_at` and `exited_at` timestamps
- `error` (failure description)
- `exit_code` (for both child processes and exited in-proc runs)

### Diagnostics and presentation status

While `exit_code` and lifecycle timestamps are returned over IPC today, they are
distinct from current presentation and diagnostics:
- **Presentation gap:** `gctl ps` and the shell `ps` command currently format
  only PID, name, kind, state, and caps. Richer presentation and exit status
  display are scheduled for Milestone 3 ([#30](https://github.com/drawmeanelephant/gostalgia/issues/30),
  [#33](https://github.com/drawmeanelephant/gostalgia/issues/33)).
- **Log capture gap:** Output from child processes (`stdout`/`stderr`) is not yet
  captured into bounded ring buffers; capturing bounded logs and surfacing them
  via IPC and tools is tracked in [#30](https://github.com/drawmeanelephant/gostalgia/issues/30).
- **Supervision gap:** Processes are not yet automatically restarted or reaped,
  and crash loops are not yet guarded; tracked in
  [#31](https://github.com/drawmeanelephant/gostalgia/issues/31).

## Events

Every state transition publishes `proc.state`
(`process.Event{ID, Name, Kind, State, Err}`) on the event bus. The transition
to a terminal state is guaranteed to be published before `Process.Done()`
closes, so an observer that sees `Done()` can rely on the final event having
been delivered.

## Ownership

A `Spec` carries the owning session and user and the capability list granted to
the process. `Process.Context()` replaces inherited caller capabilities with
that spec grant, and `Info.Caps` exposes a defensive snapshot over IPC.
Applications get their capabilities from their manifest. SDK lifecycle panics
are converted to errors and cleanup always runs; the process manager itself
still relies on direct non-app callbacks to behave. See
[applications.md](applications.md).
