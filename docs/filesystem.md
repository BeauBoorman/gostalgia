# Filesystem

The environment filesystem lives in `internal/vfs`.

## The model

Applications and services speak **environment paths** (`/users/guest/documents`,
`/apps/manifests`); host paths never appear in any API, IPC payload, or error
message. `/` maps onto `<root>/vfs` on the host, and a mount table lets
subtrees shadow the root.

## Interfaces

### Core Filesystem (`FS`)

```go
type FS interface {
    fs.FS          // Open
    fs.StatFS      // Stat
    fs.ReadDirFS   // ReadDir
    fs.ReadFileFS  // ReadFile
    MkdirAll(path string) error
    WriteFile(path string, data []byte, perm fs.FileMode) error
    Remove(path string) error
    Rename(oldPath, newPath string) error
    RemoveAll(path string) error
    SaveAtomic(path string, data []byte, perm fs.FileMode) error
}
```

### Document Operations (`DocumentFS`)

Higher-level document manipulation operations are provided by `*vfs.VFS` and the `DocumentFS` interface:

```go
type DocumentFS interface {
    FS
    Copy(src, dst string, overwrite bool) error
    Move(src, dst string, overwrite bool) error
    Trash(path string) (*TrashEntry, error)
    Restore(trashID, dst string, overwrite bool) (string, error)
    ListTrash() ([]TrashEntry, error)
    EmptyTrash() (int, error)
    PurgeTrash(trashID string) error
}
```

Two path forms are accepted where documented:

- **Environment form** (`/users/guest`, `/tmp/x`) — at the `*VFS` layer and in
  IPC params. Leading slash, `/`-separated. Dot segments (`.`/`..`),
  backslashes, and interior empty elements are rejected outright.
- **io/fs form** (`users/guest`) — the raw backends (`HostFS`, `MemFS`), which
  enforce `fs.ValidPath` strictly so they work with `testing/fstest`,
  `fs.WalkDir`, and the rest of the stdlib.

## Document Operations & Recoverable Saves

### Atomic Saves (`SaveAtomic`)

To prevent data loss and corrupted documents during crashes or write failures:
1. Data is written to a unique staging file (`<file>.tmp.<timestamp>.<seq>`) in the target directory (ensuring same-mount placement).
2. The file is flushed and synced (`Sync()`) to storage.
3. The staging file is atomically renamed over the destination.
4. **Recovery fallback:** If the atomic rename fails, the staged data is preserved as a recoverable artifact (`<file>.recover`) rather than discarded, and a structured `*vfs.Error` with `RecoverPath` is returned.

Size bounds: documents are capped at `MaxDocumentSize` (16 MiB). IPC writes and saves are bounded to `MaxIPCWriteLimit` (4 MiB).

### Copy & Move

- **Copy:** Supports both single files and recursive directory hierarchies. Collision detection honors the `overwrite` flag; copying a directory into its own descendant is prevented. Cross-mount copies (e.g. between root and `/tmp`) work transparently.
- **Move:** Same-mount moves use backend atomic rename. Cross-mount moves copy the entire source tree to the destination and cleanly remove the source upon successful copy.
- **Rename:** Strictly same-mount. Cross-mount rename requests fail explicitly with `ErrCrossMount` rather than silently degrading atomicity.

### Trash & Restore Lifecycle

Rather than immediate unrecoverable deletion, user document flows can use the trash lifecycle:
- **Trash Directory:** Standardized at `/users/guest/.trash` (customizable via `SetTrashDir`).
- **Structure:**
  - `/users/guest/.trash/files/<id>/<basename>`: the preserved file or directory hierarchy.
  - `/users/guest/.trash/info/<id>.json`: metadata containing original path, trashed timestamp, file size, and directory flag.
- **Restore:** Restores the item to its original path (or an explicit alternative destination). Parent directories are created as needed. Re-identifies collisions if `overwrite=false`.
- **Empty & Purge:** `EmptyTrash()` empties all trashed entries and files; `PurgeTrash(id)` permanently removes an individual entry.
- **Safety checks:** Deleting or trashing the root, active mount points, or the trash directory itself is prohibited.

## Structured Errors

Filesystem operations return structured `*vfs.Error` values containing machine-readable error codes:
- `ErrNotFound` (`not_found`): file or directory does not exist.
- `ErrExist` (`already_exists`): destination exists and `overwrite` was false.
- `ErrPermission` (`permission_denied`): capability check failed or read-only mount.
- `ErrConfinement` (`confinement_violated`): attempt to traverse outside the VFS root.
- `ErrInvalid` (`invalid_argument`): illegal path, empty name, or invalid ID.
- `ErrIsDir` (`is_directory`): expected file but got directory.
- `ErrNotDir` (`not_directory`): expected directory but got non-directory.
- `ErrCrossMount` (`cross_mount`): atomic rename attempted across distinct mount points.
- `ErrTooLarge` (`too_large`): document or payload exceeded size bounds.
- `ErrIO` (`io_error`): hardware or underlying system I/O error.

## Backends

- **HostFS** — a directory on the host, opened through `os.Root`. Path escapes
  via `..` or symlinks fail closed; errors reference the environment path, not
  the host path. Backslash names are rejected on every platform (they are
  separators on Windows, characters on Unix — one portable contract).
- **MemFS** — in-memory FS, used for `/tmp` (the environment's tmpfs) and as
  the platform test double.

### Windows metadata caveat

HostFS metadata comes from one source per file kind, so that `entry.Info()`,
`Stat()`, and `Open()+File.Stat()` agree: regular files and directories are
served through the open handle's `Stat`; special files (FIFOs, sockets,
devices, symlinks) are never opened — `Open` on a FIFO blocks until a writer
arrives on the other end — and report `Lstat` instead, which is what their
directory entries report too. For *directories* on
Windows, the OS itself reports inconsistent values across those calls for
freshly created directories (the first query of a new directory reports the
query time rather than its mtime), so exact cross-call equality is impossible
to guarantee there — `testing/fstest.TestFS` cannot pass for any host-backed
FS on Windows, including stdlib `os.DirFS`. Consequences:

- `TestHostFSMatchesFstest` runs on unix; on Windows a structural
  consistency suite (`TestHostFSConsistency`) runs instead.
- Directory `ModTime` values on Windows should be treated as advisory until
  the OS behavior is worked around (revisit with platform integration, [#44](https://github.com/drawmeanelephant/gostalgia/issues/44)).

## Mounts

`VFS.Mount("/tmp", vfs.NewMem())` shadows the root's `tmp` subtree; longest
prefix wins; `Unmount` restores the root view.

## Layout

```text
/               ← <root>/vfs on the host
├── users/<user>/
│   ├── documents/
│   ├── downloads/
│   ├── desktop/
│   ├── config/
│   └── .trash/        trash store (files/ and info/)
├── apps/manifests/    application manifests (JSON)
├── data/
├── mounts/            (reserved)
└── tmp/               ← memfs mount (ephemeral)
```

## IPC Surface (`fs/*`)

The filesystem is exposed to applications and shell commands over IPC:

| Method | Capability Required | Description |
|---|---|---|
| `fs/list` | `fs.read` | Lists directory entries with sizes, modes, and directory indicators. |
| `fs/stat` | `fs.read` | Returns path metadata (size, mod_time, is_dir, mode). |
| `fs/read` | `fs.read` | Reads file data in base64. Supports bounded reads with `offset` and `limit` (max 4 MiB). Files > 4 MiB require bounded parameters. |
| `fs/write` | `fs.write` | Writes base64 data to a file (bounded to 4 MiB). |
| `fs/save` | `fs.write` | Atomic recoverable save with collision detection (`overwrite` flag, perm). |
| `fs/mkdir` | `fs.write` | Creates directories recursively (`MkdirAll`). |
| `fs/remove` | `fs.write` | Removes a file, empty directory, or tree (`recursive: true`). |
| `fs/rename` | `fs.write` | Atomic same-mount rename with collision detection. |
| `fs/copy` | `fs.read` + `fs.write` | Copies file or directory tree with collision detection. |
| `fs/move` | `fs.write` | Moves file or directory with collision detection across mounts. |
| `fs/trash` | `fs.write` | Moves target to `.trash/files` and registers metadata in `.trash/info`. |
| `fs/restore`| `fs.write` | Restores trashed item by ID with collision handling. |
| `fs/trash/list` | `fs.read` | Lists all trashed items and metadata. |
| `fs/trash/empty` | `fs.write`| Permanently deletes all trashed entries. |

## Testing

Both backends pass `testing/fstest.TestFS`. Dedicated suites verify:
- Atomic save replacement, staging cleanup on failure, and `.recover` artifact creation.
- Recursive directory and file copy and move across mounts.
- Trash, list, restore, and purge lifecycle with metadata preservation.
- Confinement enforcement (rejection of dot segments, escapes, symlink traversal, and mount overrides).
- Payload bounds and structured error code propagation over IPC.
