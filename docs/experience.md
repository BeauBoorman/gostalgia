# Gostalgia visual language

The experience layer shares a warm, substantial terminal vocabulary instead of
per-screen style globals. It lives entirely under `internal/experience/`:

- `theme`: explicit, copyable palette, spacing, border, typography, focus, status,
  and progress tokens.
- `ui`: stateless Lip Gloss components and Unicode cell-aware layout helpers.
- `shell`: Bubble Tea interaction and authenticated IPC, using the shared kit.

The runtime, process manager, SDK, apps, and `gctl` do not import the kit or Charm.
Apps still expose environment contracts, not terminal views.

## Theme and rendering contract

`theme.Nostalgia()` is the default cream/sand surface, slate text, and amber
accent/header palette. `theme.Midnight()` is an explicitly selected slate
alternative. Both use thick panel borders, double dialog borders, filled title
rows, one-cell horizontal panel padding, and a visible focus marker.

`Theme` contains values, not shared maps or mutable global styles. Customize a
copy before constructing a kit. Colors, spacing (terminal cells), borders,
typography weights, focus marker/colors, state symbol/label/message/colors, and
progress fill/track glyphs are named tokens. Border and progress glyphs should
occupy one cell; spacing should be nonnegative.

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

The caller chooses `ui.Plain`, `ui.ANSI256`, or `ui.TrueColor`. A kit owns an
isolated renderer with an explicit profile and background setting, backed by
`io.Discard`. It does not inspect terminal state, select adaptive colors, consult
`NO_COLOR`/`COLORFGBG`, or modify Lip Gloss's global renderer. Plain output contains
no ANSI styling, but retains Unicode borders and state/focus symbols. A future
capability/settings layer can choose a mode; there is no terminal autodetection
or theme settings UI in this issue.

`shell.New` selects Nostalgia and ANSI256. `shell.NewWithTheme` accepts the theme
and mode explicitly for alternate views and deterministic testing. The shell's
existing command and keyboard behavior is unchanged.

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
and disabled editing hints. Dialogs and progress are reusable building blocks,
not new shell commands.

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

## Layout and external text

All sizes are terminal **cells**, not byte/rune counts. The kit uses Charm's ANSI
helpers for grapheme-aware truncation, wrapping, and styled-string width. CJK,
combining accents, flags, and joined emoji remain whole. Oversized graphemes
that cannot fit a viewport are omitted rather than split.

- `Truncate`: one line with an ellipsis if needed.
- `Tail`: rightmost whole graphemes, useful for a long DOS path or prompt input.
- `Wrap`: hard-wrap text by cells.
- `Fit`: clip and pad to an exact rectangle without wrapping.

Rectangular components occupy exactly their supplied positive outer bounds.
Nonpositive bounds produce an empty string; tiny panels fall back to compact
text rather than drawing an overflowing frame. Tabs, progress, and help bars
occupy one row; badges are inline and may be shorter than their limit.

80×24 is the recommended shell viewport. Components are tested through 240×80
and at tiny/zero sizes. The shell keeps its existing 30×10 minimum for interaction
and displays a bounded resize message below it. Terminal emulators may differ
on unusual emoji widths; the rendering contract uses the pinned grapheme-width
implementation, not a live terminal width query.

`Sanitize` removes terminal escapes (including OSC/DCS) and controls from external
file/app/IPC text. It preserves newlines, replaces tabs with spaces, and repairs
invalid UTF-8. Component labels, card descriptions, notice messages, and dialog
messages are sanitized internally. `Panel.Body`, text styling helpers, and
layout helpers accept **trusted text or composed ANSI output**; sanitize external
data before handing it to them. Do not sanitize component output again.

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
custom tokens, both palettes, and all three explicit color modes. ANSI snapshots
spell escapes as `\x1b` for readable diffs. `.gitattributes` preserves LF endings
in golden fixtures, including Windows checkouts with `core.autocrlf=true`.
Shell goldens cover home, populated shelf, empty shelf, busy, and error views.
Snapshot comparisons ignore trailing spaces; separate unit tests check exact
rectangular cell dimensions.

Other tests cover clipping/wrapping, combining/CJK/emoji text, narrow action/tab
visibility, progress clamping, control filtering, host-environment independence,
theme-copy isolation, and concurrent rendering. The existing actual Bubble Tea
program test drives app lifecycle and VFS commands over authenticated IPC.

Regenerate goldens deliberately, then inspect their diff:

```sh
go test ./internal/experience/ui -run 'Test(Component|Color)Snapshots' -update
go test ./internal/experience/shell -run TestShellVisualSnapshots -update-visual
```
