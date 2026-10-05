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
- Exited processes remain listed (with state and exit status) until reaping —
  reaping/supervision is future work (architecture.md, milestone 3).

## Events

Every state transition publishes `proc.state`
(`process.Event{ID, Name, Kind, State, Err}`) on the event bus. The transition
to a terminal state is guaranteed to be published before `Process.Done()`
closes, so an observer that sees `Done()` can rely on the final event having
been delivered.

## Ownership

A `Spec` carries the owning session and user and the capability list granted to
the process. Applications get their capabilities from their manifest
(see applications.md).
