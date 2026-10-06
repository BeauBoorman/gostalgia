package platform

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// ErrClipboardUnsupported is returned when host system clipboard integration
// is not available on the current platform or environment.
var ErrClipboardUnsupported = errors.New("platform: host clipboard integration is unsupported")

// MaxClipboardBytes bounds single clipboard payloads to 1 MiB.
const MaxClipboardBytes = 1 << 20

// HostClipboardAdapter defines the platform contract for accessing the host system clipboard.
type HostClipboardAdapter interface {
	Available() bool
	ReadText(ctx context.Context) (string, error)
	WriteText(ctx context.Context, text string) error
}

var (
	clipboardMu      sync.RWMutex
	currentClipboard HostClipboardAdapter = defaultHostClipboard()
)

// GetHostClipboard returns the active host clipboard adapter.
func GetHostClipboard() HostClipboardAdapter {
	clipboardMu.RLock()
	defer clipboardMu.RUnlock()
	return currentClipboard
}

// SetHostClipboard sets the active host clipboard adapter (useful for testing).
func SetHostClipboard(adapter HostClipboardAdapter) {
	clipboardMu.Lock()
	defer clipboardMu.Unlock()
	if adapter == nil {
		currentClipboard = defaultHostClipboard()
		return
	}
	currentClipboard = adapter
}

// HostClipboardAvailable reports whether the host clipboard is supported and accessible.
func HostClipboardAvailable() bool {
	return GetHostClipboard().Available()
}

// ReadHostClipboard reads plain text from the host clipboard using the active platform adapter.
func ReadHostClipboard(ctx context.Context) (string, error) {
	return GetHostClipboard().ReadText(ctx)
}

// WriteHostClipboard writes plain text to the host clipboard using the active platform adapter.
func WriteHostClipboard(ctx context.Context, text string) error {
	return GetHostClipboard().WriteText(ctx, text)
}

var (
	// Matches OSC sequences: ESC ] ... (BEL | ESC \ | EOF)
	oscRegex = regexp.MustCompile(`(?s)\x1b\][^\x07\x1b]*(\x07|\x1b\\|$)`)
	// Matches DCS sequences: ESC P ... (ESC \ | BEL | EOF)
	dcsRegex = regexp.MustCompile(`(?s)\x1bP[^\x07\x1b]*(\x07|\x1b\\|$)`)
	// Matches CSI escape sequences: ESC [ ... [command byte]
	csiRegex = regexp.MustCompile(`\x1b\[[0-9:;<=>?]*[ -/]*[@-~]`)
	// Matches any remaining 2-byte escape sequences: ESC followed by byte 0x40..0x5F
	escRegex = regexp.MustCompile(`\x1b[@-_]`)
)

// SanitizeClipboard strips terminal control sequences (especially OSC clipboard
// or terminal command injections), cleans control characters, normalizes Unicode,
// and enforces memory limits.
func SanitizeClipboard(s string) string {
	if s == "" {
		return ""
	}

	// 1. Strip OSC sequences (prevent arbitrary OSC sequences from untrusted inputs)
	s = oscRegex.ReplaceAllString(s, "")

	// 2. Strip DCS sequences
	s = dcsRegex.ReplaceAllString(s, "")

	// 3. Strip CSI sequences (colors, cursor control, device queries)
	s = csiRegex.ReplaceAllString(s, "")

	// 4. Strip any dangling escape sequences
	s = escRegex.ReplaceAllString(s, "")

	// 5. Replace invalid UTF-8 sequences
	s = strings.ToValidUTF8(s, "")

	// 6. Filter control characters while preserving standard whitespace (\n, \r, \t)
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t' || r == '\r':
			sb.WriteRune(r)
		case r == '\x1b':
			// Skip any lone escape
			continue
		case unicode.IsControl(r):
			// Skip all other non-printable control characters (including NUL, BEL, BS, etc.)
			continue
		default:
			sb.WriteRune(r)
		}
	}

	result := sb.String()

	// 7. Enforce max clipboard byte size
	if len(result) > MaxClipboardBytes {
		// Truncate at valid UTF-8 rune boundary
		cut := result[:MaxClipboardBytes]
		for len(cut) > 0 && !utf8.ValidString(cut) {
			cut = cut[:len(cut)-1]
		}
		result = cut
	}

	return result
}
