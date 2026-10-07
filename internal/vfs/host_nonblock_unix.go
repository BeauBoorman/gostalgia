//go:build unix

package vfs

import "syscall"

// openNonblock keeps os.Root.OpenFile from blocking on special files:
// opening a FIFO for read waits for a writer on the other end (and a
// FIFO write-open waits for a reader). With the flag set, the open
// returns immediately and the opened handle's own Stat decides whether
// the file is usable — which also closes the check-vs-open swap race.
const openNonblock = syscall.O_NONBLOCK
