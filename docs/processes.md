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

## Process inspection and IPC (`proc/list`, `proc/info`, `proc/logs`)

The `proc/list` IPC endpoint returns complete `process.Info` snapshots,
including:
- `id`, `name`, `kind`, and `state`
- `session` and `user` ownership
- `caps` (granted capabilities snapshot)
- `started_at` and `exited_at` timestamps
- `error` (failure description)
- `exit_code` (for both child processes and exited in-proc runs)

The `proc/info` IPC endpoint returns this snapshot for a single process ID.

The `proc/logs` IPC endpoint returns captured process logs and stream diagnostics:
- Process identification (`id`, `name`, `kind`, `state`, `exit_code`)
- Lifecycle timings (`started_at`, `exited_at`, `duration`)
- `stdout`, `stderr`, and `combined` stream diagnostics containing:
  - `total_bytes`: total cumulative bytes written to the stream
  - `buffered_bytes`: current un-dropped bytes retained in the ring buffer
  - `dropped_bytes`: count of older bytes dropped due to capacity limits
  - `truncated`: boolean flag indicating if buffer overflow occurred
  - `content`: buffered stream text snapshot
- Parameters:
  - `id` (required): target process ID
  - `stream` (optional): filter by stream (`stdout`, `stderr`, or combined default)
  - `tail` (optional): limit returned content to the last N lines

### Child output capture and bounding

Child process (`KindChild`) standard output and standard error are captured via
bounded memory ring buffers (`process.RingBuffer`) defaulting to 64KB per stream
(configurable via `spec.LogLimit`).

- **Non-blocking FIFO drops:** When incoming output exceeds capacity, the oldest
  bytes are dropped, tracking exact byte counts and truncation flags without
  blocking child execution or causing unbounded memory growth.
- **WaitDelay protection:** `cmd.WaitDelay = 2 * time.Second` prevents child
  processes from leaking background I/O handles or stalling manager shutdown.
- **Child environment sanitization:** `DefaultChildEnv` deliberately whitelists
  safe system variables (`PATH`, `TMPDIR`, `HOME`, etc.) and sets `GOSTALGIA_*`
  runtime variables, explicitly filtering out sensitive host credentials,
  tokens, and private keys.

### Diagnostics and presentation

Exit codes, timings, and logs are integrated across developer and interactive tools:
- `gctl ps`: displays `PID`, `NAME`, `KIND`, `STATE`, `EXIT` code, and runtime `TIME` duration.
- `gctl logs <pid> [tail]`: displays process diagnostics, stream byte metrics, drop counters, and captured output with terminal control-character sanitization.
- Charm shell `ps`: displays process names, states with exit codes, run durations, and capability grants.
- Charm shell `logs <pid> [tail]`: displays process diagnostics, byte counts, and sanitized stdout/stderr output.

### Supervision status

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
