//go:build !darwin && !linux && !windows

package platform

import (
	"context"
)

type fallbackClipboard struct{}

func defaultHostClipboard() HostClipboardAdapter {
	return &fallbackClipboard{}
}

func (f *fallbackClipboard) Available() bool {
	return false
}

func (f *fallbackClipboard) ReadText(ctx context.Context) (string, error) {
	return "", ErrClipboardUnsupported
}

func (f *fallbackClipboard) WriteText(ctx context.Context, text string) error {
	return ErrClipboardUnsupported
}
