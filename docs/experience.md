# Gostalgia visual language

The experience layer shares a warm, substantial terminal vocabulary instead of
per-screen style globals. It lives entirely under `internal/experience/`:

- `theme`: explicit, copyable palette, spacing, border, typography, focus, status,
  and progress tokens.
- `ui`: stateless Lip Gloss components and Unicode cell-aware layout helpers.
- `taskmanager`: Bubble Tea model and table component for live process inspection,
  resource usage metrics, interactive process control, and log tailing.
- `notifications`: notification history panel, ephemeral toast popups, flood bounding,
  unseen tracking, and Do-Not-Disturb (DND) mode.
- `receipts`: structured post-mortem crash receipts with bounded, sanitized log excerpts
  and thread-safe bounded storage.
- `shell`: Bubble Tea interaction and authenticated IPC, orchestrating all experience
  subsystems using the shared kit.

The runtime, process manager, SDK, apps, and `gctl` do not import the kit or Charm.
Apps still expose environment contracts, not terminal views.

## Theme and rendering contract

`theme.Nostalgia()` is the default cream/sand surface, slate text, and amber
accent/header palette. `theme.Midnight()` is an explicitly selected slate
alternative. Both use thick panel borders, double dialog borders, filled title
rows, one-cell horizontal panel padding, and a visible focus marker.

`theme.Monochrome()` provides an accessible no-color mode featuring pure ASCII
borders (`+`, `-`, `|`), standard ASCII focus markers (`>`), and explicit bracketed
text badges.

`theme.HighContrast()` and `theme.HighContrastLight()` provide high-contrast
dark (pure black canvas, pure white text, vivid yellow accents/focus) and light
(pure white canvas, pure black text, deep navy accents/focus) token sets with
bold borders, double dialog frames, and high-contrast cues.

`Theme` contains values, not shared maps or mutable global styles. Customize a
copy before constructing a kit. Colors, spacing (terminal cells), borders,
typography weights, focus marker/colors, state symbol/label/message/colors,
progress fill/track glyphs, and the `ReducedMotion` boolean toggle are named tokens.
Border and progress glyphs occupy one cell; spacing is nonnegative.

```go
import (
    "gostalgia/internal/experience/theme"
    "gostalgia/internal/experience/ui"
)

t := theme.Nostalgia()
t.Spacing.PanelX = 2
kit := ui.New(t, ui.ANSI256)
bounds := ui.Bounds{Width: 80, Height: 24}
view := kit.Panel(ui.Panel{
    Title: "Notes",
    Body: kit.Text(ui.Sanitize("A thought worth keeping.")),
    Focused: true,
}, bounds)
```

The caller chooses `ui.Plain`, `ui.ANSI16`, `ui.ANSI256`, or `ui.TrueColor`. A kit owns an
isolated renderer with an explicit profile and background setting, backed by
`io.Discard`. It does not inspect terminal state, select adaptive colors, consult
`NO_COLOR`/`COLORFGBG`, or modify Lip Gloss's global renderer. Plain output contains
no ANSI styling, but retains borders and state/focus symbols.

`shell.New` selects Nostalgia and ANSI256. `shell.NewWithTheme` accepts the theme
and mode explicitly for alternate views and deterministic testing. Shell users
can dynamically switch themes with `theme [NAME]` or toggle reduced motion with
`motion [on|off]`, as well as select them directly from the Command Palette (`Ctrl-P`).

## Accessible terminal modes

Gostalgia guarantees full visual accessibility without relying on terminal
autodetection or host environment heuristics:

- **Monochrome & limited-color mode**:
  - `theme.Monochrome()` combined with `ui.Plain` emits zero ANSI color escapes.
  - All panel and card borders switch to clean standard ASCII characters (`+`, `-`, `|`)
    via `theme.ASCIIBorder()`, preventing terminal box-drawing glitches on legacy or
    limited-font terminals.
  - The focus cursor uses standard ASCII `> ` instead of Unicode symbols.
  - Unread indicators use `* ` instead of colored dots.
- **High-contrast tokens**:
  - `theme.HighContrast()`: pure black `#000000` canvas with pure white `#FFFFFF`
    primary text and high-visibility `#FFFF00` yellow accents and focus highlights.
  - `theme.HighContrastLight()`: pure white `#FFFFFF` canvas with deep black `#000000`
    primary text and high-visibility `#000080` navy accents.
  - Prominent focus markers (`» `) and thick/double borders ensure immediate visual
    clarity.
- **Never rely on color alone**:
  - All status indications provide explicit bracketed text cues: `[OK]`, `[FAIL]`,
    `[BUSY]`, `[READY]`, `[DISABLED]`, and `[EMPTY]`.
  - Process tables, crash receipts, notifications, app cards, and notice panels
    display text labels and bracketed glyphs alongside color cues.
- **Reduced motion**:
  - Configurable via `theme.WithReducedMotion(bool)` or the `motion [on|off]` command,
    and enabled by default in `Monochrome`, `HighContrast`, and `HighContrastLight`.
  - Suppresses indeterminate progress animations and moving ticker symbols:
    `ui.Progress` renders static bracketed progress tracks and explicit `BUSY` / `WORKING`
    text rather than animated spinners or moving markers.
  - The experience layer runs zero background animation loops or unrequested frame redraws.

## Components

| Component | Contract |
|---|---|
| `Panel` | Total outer bounds include the border, filled title, and padding. `PanelContentBounds` reports the available body rectangle. |
| `Tabs` | One padded row; keeps the active tab visible when leading tabs must be dropped. Brackets show selection; parentheses show disabled tabs. |
| `AppCard` | Name/focus header, real status badge, identity/version, and optional description. Compact layouts prioritize name and status. |
| `Badge` | Bounded inline symbol and label; an empty label uses the state label. |
| `Dialog` | Centered canvas, double border, wrapped message, selectable/disabled actions. Actions take priority over a long body. Caller owns visibility, input, confirmation, and overlay composition. |
| `Progress` | Static clamped fraction/percentage, or indeterminate text without a fabricated percentage. No tick or animation command. |
| `Notice` | Friendly default or caller-supplied empty, busy, disabled, success, and error messages. |
| `HelpBar` | One bounded contextual keybinding row; disabled keys use parentheses. Put essential bindings first. |

Named states are `Normal`, `Disabled`, `Busy`, `Success`, `Error`, and `Empty`.
Focus is independent of status; disabled panels/tabs do not appear focused.
Symbols and words communicate states without relying on color alone. These
components render state, not behavior: the caller must enforce disabled actions.
There are no decorative goroutines, clocks, spinners, or permanently running
animation loops.

The current prompt/home and app shelf share panel/title, tabs, status badge,
focus, and help vocabulary. Shelf app cards expose running PID vs. ready status;
an empty shelf has a friendly notice. Busy requests use a static working badge
and disabled editing hints. F4 app presentations from the data/action contract
(#26) use the same theme, focus tokens, loading/error states, clipping, and
contextual help. Disabled actions are not highlighted. The shell remains the
only terminal owner; apps provide SDK data, not Charm views. Existing app
snapshot/lifecycle polling is functional refresh, not decorative animation.
Dialogs and progress are reusable building blocks, not new shell commands.

## Navigation, home screen, and command palette

Issue #23 expands the shell into a cohesive navigation environment comprising
a discoverable home screen dashboard, a searchable application launcher, an
actionable command palette overlay, and deterministic focus routing under one
terminal owner.

### Home screen dashboard

The home screen (`modeHome`, default on startup, toggleable via `F1` or typing `home`)
presents three discoverable activity sections:

1. **System Activity**: Live uptime formatted humanely (e.g. `just booted`, `45m`,
   `2h 15m`), active process count, registered service count, current user, and
   environment drive indicator sourced directly via `sys/status`.
2. **Running Applications**: Quick-switch shortcuts for all currently active
   applications displaying live PIDs. An honest empty notice is displayed when
   no applications are running, hinting at `F2` to launch apps.
3. **Document Shortcuts**: Quick-open links to user files stored under
   `C:\users\guest\documents` (`/users/guest/documents`), discovered via `fs/list`.
   Selecting a document and pressing `Enter` automatically opens and prints it
   via `type <path>`.

### Searchable friendly-name app launcher

The launcher (`modeLauncher`, opened via `F2` or typing `launcher`/`shelf`)
replaces minimal static shelves with searchable friendly-name application cards:

- **Incremental Search**: Real-time text query filtering across application
  friendly names, IDs, and descriptions.
- **Card Presentation**: Uses Issue #22 `AppCard` and `Badge` components, showing
  the friendly name, ID, version, running status (`READY` vs. `LIVE / PID <pid>`),
  description, and permissions grant line.
- **Honest Empty States**: If no applications are installed or if a query has no
  matches, an honest `Notice` component is displayed with query clearing advice.
- **Actions**: `Enter` launches the selected app, `F3` sends a graceful stop
  request, and `F4` switches focus directly to the app's presentation view.

### Command palette overlay

The command palette (`modePalette`, opened with `Ctrl+P` anywhere or `/` at an
empty prompt) provides a modal overlay for keyboard-driven navigation:

- Indexes installed applications (`[APP]`), documents in the user documents
  folder (`[DOC]`), and core system commands (`[CMD]`).
- Supports fuzzy/prefix substring filtering.
- Navigated via `↑` / `↓`; pressing `Enter` immediately executes the selected
  action (launches app, opens document, or runs shell command) and dismisses
  the overlay.
- `Esc` or `Ctrl+P` dismisses the overlay without taking action.

### Predictable focus management

A strict single terminal owner contract ensures input is never dropped or ambiguous:

- `FocusPrompt`: Command prompt owns input; transcript scrollback and history
  navigation active.
- `FocusHome`: Home dashboard owns selection; `↑`/`↓` navigates running apps
  and document shortcuts, `Enter` opens the item, typing any command rune
  transitions focus directly to the command prompt.
- `FocusLauncher`: Launcher search bar and card list own selection; typing
  filters the app cards, `↑`/`↓` selects, `Enter` runs, `Esc` clears the search
  or returns to the prompt.
- `FocusApp`: Active app presentation view owns keyboard interaction; `Esc`
  retracts presentation and restores prompt focus.
- `FocusPalette`: Command palette overlay owns all keystrokes; `Esc` restores
  prior view mode.

### Single-instance launch and graceful stop

Applications follow the platform single-instance contract. When launching an
app that is already running or rejected by the runtime, the shell handles the
rejection gracefully: the error is recorded in the transcript, focus safely
transitions to the prompt to display the diagnostic message, and the terminal
state remains intact. Stopping an application via `F3` or `stop <app-id>`
issues a graceful IPC stop request.

## Task Manager, Notifications, and Crash Receipts

The experience layer integrates process observability and diagnostic inspection
directly into the shell via dedicated tabs, commands, and overlay notifications:

### Live Task Manager (`internal/experience/taskmanager`)

The live Task Manager displays real-time supervised process state and metrics in a
scrollable table:
- **Columns**: Selection pointer, `PID`, `NAME`, `STATE`, `CPU` (user/system usage),
  `RAM` (resident memory), `RESTARTS`, and `EXIT` code / runtime duration.
- **Navigation & Controls**:
  - `↑` / `↓` / `k` / `j`: Navigate through processes.
  - `l` or `Enter`: Open child log tail viewer modal (`proc/logs`).
  - `c` or `Enter`: Inspect post-mortem crash receipt for failed processes.
  - `x`: Terminate selected process (`proc/stop`).
  - `r`: Prune terminated processes (`proc/reap`).
  - `Esc`: Return to process table or exit Task Manager view.
- **Access**: Press `F5` or execute `tasks`, `taskmanager`, or `top` from the prompt.

### Notification Center & Toast Overlays (`internal/experience/notifications`)

- **Toast Overlays**: Transient popups render directly above the command prompt
  for process lifecycle milestones (clean exits, backoff restart warnings, and crashes).
  Toasts count down ticks and expire automatically without stealing prompt focus.
- **Notification Center**: Dedicated full-screen panel listing complete event history,
  timestamps, severities (`INFO`, `WARN`, `ERROR`), and associated PIDs.
  - Unseen vs. Seen distinction: events arrive unread until inspected.
  - `↑` / `↓`: Select notifications.
  - `Enter`: Mark seen and drill down to crash receipts.
  - `d`: Dismiss selected notification.
  - `c`: Clear all notification history.
  - `n`: Toggle Do-Not-Disturb mode.
- **Do-Not-Disturb (DND)**: When toggled via `n` or shell command `dnd [on|off]`,
  transient toasts are suppressed to prevent interruption while event history
  continues recording silently.
- **Flood Bounding**: Active toasts and stored notification entries are capped to
  fixed bounds to ensure zero runaway memory growth.
- **Access**: Press `F6` or execute `notifications` or `alerts` from the prompt.

### Crash Receipts (`internal/experience/receipts`)

Crash receipts provide immediate post-mortem diagnostics for abnormal exits:
- **Captured Data**: Process ID, name, exit code, state, failure reason/error,
  restart count, timestamp, and bounded log excerpts.
- **Safe Bounding & Sanitization**: Log excerpts are bounded to 25 lines and 4 KB,
  and stripped of dangerous ANSI escape sequences, DCS/OSC strings, and non-printable
  control characters using `ui.Sanitize`.
- **Bounded In-Memory Store**: A thread-safe ring buffer retains recent crash
  receipts (default capacity 50), evicting oldest receipts when full.
- **Inspection**: Drill down from the Task Manager table, press `Enter` on a crash
  notification in the Notification Center, or run `receipt [PID]` from the prompt.

### Interactive Settings & Live Configuration Updates

The shell deeply integrates with Gostalgia's configuration engine (`internal/config`)
and Settings application (`com.gostalgia.settings`):
- **Access**: Press `F7`, run `settings`, `preferences`, or `pref`, or choose Settings
  from the Command Palette (`Ctrl-P`).
- **Dynamic Keybinding Dispatch**: Shell key shortcuts (F1-F7, palette) are evaluated
  dynamically against the effective `shortcuts.*` configuration.
- **Live Theme & Motion Switching**: Changes to `theme`, `accessibility.color_mode`,
  and `accessibility.reduced_motion` apply immediately across the running shell without
  rebooting.
- **Interactive Preview & Rollback**: Live theme selections rendered in Settings apply
  in-memory preview overrides via `config/preview`, which can be committed (`save`) or
  reverted (`revert` / `cancel`) safely.
- **Configurable Startup View**: The shell reads `startup.view` to determine whether to
  land on Home dashboard, Prompt, App Launcher, Task Manager, or Notifications on boot.

## Layout and external text

All sizes are terminal **cells**, not byte/rune counts. The kit uses Charm's ANSI
helpers for grapheme-aware truncation, wrapping, and styled-string width. CJK,
combining accents, flags, and joined emoji remain whole. Oversized graphemes
that cannot fit a viewport are omitted rather than split.

- `GraphemeClusters`: breaks text into user-perceived grapheme clusters using `ansi.FirstGraphemeCluster`.
- `GraphemeWidth`: computes display cell width accounting for double-width CJK, zero-width joiners, and combining runes.
- `Truncate`: one line with an ellipsis if needed.
- `Tail`: rightmost whole graphemes, useful for a long DOS path or prompt input.
- `Wrap`: hard-wrap text by cells.
- `Fit`: clip and pad to an exact rectangle without wrapping.

### Prompt editing and cluster atomicity

Prompt line navigation and editing in `internal/experience/shell` operate at grapheme cluster boundaries:
- Cursor left/right navigation advances or retreats across whole clusters (`prevClusterRune`, `nextClusterRune`).
- Backspace deletes the preceding grapheme cluster as an atomic unit, preventing orphaned combining marks or broken modifier sequences.
- Delete removes the cluster at the cursor atomically.
- Pasted or typed multiline text has newlines converted to spaces, maintaining a predictable single-line prompt.

### Viewport resilience and small-screen degradation

- Rectangular components occupy exactly their supplied positive outer bounds.
- Nonpositive bounds produce an empty string; tiny panels fall back to compact text rather than drawing an overflowing frame.
- 80×24 is the recommended standard shell viewport. Components and shell views are tested from tiny bounds through 240×80.
- When viewport dimensions fall between 80×24 and 30×10, views remain fully usable with compact layouts and bounded text truncation.
- Below 30×10, the shell gracefully degrades to a clean, bounded notice (`GOSTALGIA / Resize to 30×10 or larger. / Ctrl-C exits.`) without crashing, clipping errors, or outer border overflow.

## Terminal control injection defense and restoration

### Escape sequence filtering

External files, app standard output, IPC data, and notification messages are untrusted.
`ui.Sanitize` strips:
- CSI escape sequences: cursor movement, clear screen, display modes, font styling (`\x1b[...m`, `\x1b[?25h`, etc.).
- OSC escape sequences: window title changes (`\x1b]0;...\a`), OSC 52 clipboard hijacking (`\x1b]52;...\a` or ST), and OSC 8 hyperlinks.
- DCS (Device Control Strings), APC (Application Program Commands), PM (Privacy Messages).
- C1 8-bit controls (`\x9b`) and low ASCII control characters (BEL, BS, VT, FF).
- Preserves newlines (`\n`), spaces, tab expansion, and valid UTF-8 combining sequences.

All CLI commands (`cat`, `type`, `dir`, `ls`, `echo`, `call`), task manager log tails, and crash receipt excerpts pass through `safe()` sanitization before rendering into the shell transcript or presentation cards.

### Deterministic terminal restoration

On normal or abnormal termination (including panics, interrupts, or signal terminations),
terminals must not be left in alternate screens, mouse tracking modes, or hidden cursor states.

`shell.RestoreTerminal()` writes explicit recovery sequences to standard output:
- `\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l`: disables standard, button, any-event, and SGR mouse tracking.
- `\x1b[?2004l`: disables bracketed paste mode.
- `\x1b[?25h`: restores cursor visibility.
- `\x1b[0m`: resets all SGR attributes (colors, bold, underline, reverse).
- `\x1b[?1049l`: exits the alternate screen buffer and returns to the normal screen.

`shell.Run` installs a deferred call to `RestoreTerminal()`, ensuring restoration runs on normal Bubble Tea program exit and error returns. `shell.Restore(io.Writer)` enables isolated, deterministic testing of recovery sequences without side effects.

## Dependencies and boundary

The coordinated Bubble Tea v1.3.10, Lip Gloss v1.1.0, and Bubbles v1.0.0 baseline
is unchanged. Issue #22 makes two already pinned transitive helpers direct:

- `github.com/charmbracelet/x/ansi v0.11.6`: ANSI-safe grapheme cell layout.
- `github.com/muesli/termenv v0.16.0`: explicit renderer profile selection, **not**
  host dark/light probes.

This is the dependency decision for those direct imports; it adds no new module
or version. Both stay inside experience. The dependency tests allow Charm under
`internal/experience/` and still verify stdlib-only runtime/SDK/apps/`gctl`
closures. Rendering needs no cgo, external binary, host shell, or service.

## Verification and snapshots

```sh
go test -count=1 ./internal/experience/... ./test/e2e
gofmt -l .
go vet ./...
go test -count=1 -race ./...
CGO_ENABLED=0 go build ./...
GOOS=windows go build ./...
GOOS=darwin go build ./...
```

Checked-in goldens cover the component vocabulary, focus and all named states,
custom tokens, both standard palettes, accessible themes (`accessible-monochrome.golden`,
`accessible-high-contrast.golden`), and all explicit color modes. ANSI snapshots
spell escapes as `\x1b` for readable diffs. `.gitattributes` preserves LF endings
in golden fixtures, including Windows checkouts with `core.autocrlf=true`.
Shell goldens cover home, populated shelf, empty shelf, busy/error, app
presentation views, and accessible views (`monochrome-prompt.golden`,
`monochrome-home.golden`, `high-contrast-prompt.golden`).
Snapshot comparisons ignore trailing spaces; separate unit tests check exact
rectangular cell dimensions.

Other tests cover clipping/wrapping, combining/CJK/emoji text, narrow action/tab
visibility, progress clamping, control filtering (CSI, OSC, DCS, C1 controls),
host-environment independence, theme-copy isolation, and concurrent rendering.
The actual Bubble Tea program test drives app lifecycle and VFS commands over
authenticated IPC.

Regenerate goldens deliberately, then inspect their diff:

```sh
go test ./internal/experience/ui -run 'Test.*Snapshots' -update
go test ./internal/experience/shell -run 'TestShell.*Snapshots' -update-visual
```
