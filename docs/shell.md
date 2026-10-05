# Charm terminal experience

```sh
go run ./cmd/gostalgia                         # default: own boot + shell
go run ./cmd/gostalgia shell --root /tmp/gs     # explicit root
```

Use a real interactive terminal (30×10 minimum, 80×24 recommended). Bubble Tea
owns its alternate screen and event loop; Lip Gloss draws a rounded indigo
frame, icy-cyan wordmark, gold DOS prompt, and app shelf. `C:` is the
**environment VFS**, not your host drive. Runtime logs stay in
`<root>/logs/gostalgia.log`, not on top of the TUI. `--verbose` enables debug
file logging. The shell owns the runtime: exit, Ctrl-C, or Ctrl-D restores the
terminal and shuts down apps/services. External IPC shutdown also exits it.
A booted root cannot be opened a second time; use gctl to control a headless
runtime (`gostalgia boot --root /tmp/gs`). Shell attachment is not implemented.

## Prompt commands

| Command | Meaning |
|---|---|
| `help`, `?` | Command reference |
| `apps` | IDs, versions, live/ready states, manifest grants |
| `launch ID`, `run ID` | Launch a registered app; single-instance errors are visible |
| `stop ID` | Wait for app cleanup and retract routes |
| `echo MESSAGE` | Invoke the sole demo app (must be running) |
| `call METHOD [JSON]` | Raw service/app IPC, e.g. `call app/com.gostalgia.echo/identity` |
| `dir [PATH]`, `ls [PATH]` | List the current/given VFS directory |
| `cd PATH` | Validate and change the current directory |
| `type PATH`, `cat PATH` | Read a VFS file |
| `ps`, `status` | Process table with capability grants / runtime status |
| `cls`, `clear` | Clear transcript |
| `exit`, `quit` | Leave shell and shut down owned runtime |
| `shutdown` | Request runtime shutdown over IPC |

Commands are case-insensitive, IDs and method names are case-sensitive. Paths
accept Unix or DOS separators: `cd C:\users\guest\documents`. Relative paths
and `..` work inside the VFS. Quote paths/messages containing spaces; use single
or double quotes. Backslashes remain literal in path arguments. Raw `call`
JSON is preserved, not passed through path/quote parsing.

## Keyboard

- **F2**: switch between prompt and app shelf. **↑/↓** selects an app;
  **Enter** launches it; **F3** stops it; **Esc** returns to the prompt. The
  shelf displays description and manifest permissions.
- **↑/↓** at prompt: history (100 commands, current draft restored).
- **Tab**: complete an unambiguous command or ID after launch/run/stop.
- **Left/Right, Home/End, Ctrl-A/E**: move cursor. Backspace/Delete edits;
  Ctrl-U clears input. Unicode and bracketed paste are supported.
- **PgUp/PgDown**: scroll transcript. Output is bounded to 400 lines;
  each result is capped at 12,000 runes. Long input stays cursor-centered.
- **Ctrl-C/D**: exit. A pending IPC command doesn't block the event loop;
  while it runs, command editing is disabled (exit/resize/scroll still work).

All commands use the same authenticated socket API as gctl, with a ten-second
command deadline. The shell is a **trusted operator**. Apps called by the shell
still execute with their own manifest permissions, never operator privileges.
Terminal controls from file/app output are filtered so OSC/ANSI output cannot
manipulate the terminal. Small layouts display a resize message; normal layouts
are clipped to the viewport and never require the core to know terminal size.

## Verification and boundaries

`internal/experience/shell` is the only production package importing Charm.
The runtime, SDK, and Echo demo remain standard-library-only. Module
transitives are documented in [architecture.md §4.1](architecture.md#41-dependency-policy).

Shell tests exercise key editing, Unicode, history, completion, quoting, DOS
paths, bad usage, transcript bounds, viewport dimensions, and control-character
filtering. An actual Bubble Tea program is driven through its key-message
interface against a booted runtime and authenticated socket: app listing,
Echo, scoped identity, stop/relaunch reset, VFS navigation, shelf control, and
terminal rendering. The app SDK spec is [applications.md](applications.md).
