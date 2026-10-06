//go:build windows

package platform

import (
	"context"
	"fmt"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

type windowsClipboard struct{}

func defaultHostClipboard() HostClipboardAdapter {
	return &windowsClipboard{}
}

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	modUser32   = syscall.NewLazyDLL("user32.dll")
	modKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procEmptyClipboard             = modUser32.NewProc("EmptyClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procSetClipboardData           = modUser32.NewProc("SetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")

	procGlobalAlloc  = modKernel32.NewProc("GlobalAlloc")
	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procGlobalSize   = modKernel32.NewProc("GlobalSize")
	procRtlMoveMem   = modKernel32.NewProc("RtlMoveMemory")
)

func (w *windowsClipboard) Available() bool {
	return modUser32.Load() == nil && modKernel32.Load() == nil
}

func (w *windowsClipboard) ReadText(ctx context.Context) (string, error) {
	if !w.Available() {
		return "", ErrClipboardUnsupported
	}

	r, _, _ := procIsClipboardFormatAvailable.Call(uintptr(cfUnicodeText))
	if r == 0 {
		return "", nil // Clipboard is empty or contains non-text format
	}

	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		return "", fmt.Errorf("platform: OpenClipboard failed: %w", err)
	}
	defer procCloseClipboard.Call()

	hMem, _, err := procGetClipboardData.Call(uintptr(cfUnicodeText))
	if hMem == 0 {
		return "", nil
	}

	size, _, _ := procGlobalSize.Call(hMem)
	if size == 0 {
		return "", nil
	}

	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return "", fmt.Errorf("platform: GlobalLock failed: %w", err)
	}
	defer procGlobalUnlock.Call(hMem)

	buf := make([]uint16, size/2)
	if len(buf) == 0 {
		return "", nil
	}

	_, _, _ = procRtlMoveMem.Call(uintptr(unsafe.Pointer(&buf[0])), ptr, size)

	// Trim at first null terminator
	for i, v := range buf {
		if v == 0 {
			buf = buf[:i]
			break
		}
	}

	return string(utf16.Decode(buf)), nil
}

func (w *windowsClipboard) WriteText(ctx context.Context, text string) error {
	if !w.Available() {
		return ErrClipboardUnsupported
	}

	u16s := utf16.Encode([]rune(text))
	u16s = append(u16s, 0) // Null terminate
	bytesNeeded := len(u16s) * 2

	hMem, _, err := procGlobalAlloc.Call(uintptr(gmemMoveable), uintptr(bytesNeeded))
	if hMem == 0 {
		return fmt.Errorf("platform: GlobalAlloc failed: %w", err)
	}

	ptr, _, err := procGlobalLock.Call(hMem)
	if ptr == 0 {
		return fmt.Errorf("platform: GlobalLock failed: %w", err)
	}

	_, _, _ = procRtlMoveMem.Call(ptr, uintptr(unsafe.Pointer(&u16s[0])), uintptr(bytesNeeded))
	procGlobalUnlock.Call(hMem)

	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("platform: OpenClipboard failed: %w", err)
	}
	defer procCloseClipboard.Call()

	procEmptyClipboard.Call()
	r, _, err = procSetClipboardData.Call(uintptr(cfUnicodeText), hMem)
	if r == 0 {
		return fmt.Errorf("platform: SetClipboardData failed: %w", err)
	}

	return nil
}
