//go:build darwin

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type darwinClipboard struct{}

func defaultHostClipboard() HostClipboardAdapter {
	return &darwinClipboard{}
}

func (d *darwinClipboard) Available() bool {
	if _, err := os.Stat("/usr/bin/pbpaste"); err != nil {
		return false
	}
	if _, err := os.Stat("/usr/bin/pbcopy"); err != nil {
		return false
	}
	return true
}

func (d *darwinClipboard) ReadText(ctx context.Context) (string, error) {
	if !d.Available() {
		return "", ErrClipboardUnsupported
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/pbpaste")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("platform: read host clipboard: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (d *darwinClipboard) WriteText(ctx context.Context, text string) error {
	if !d.Available() {
		return ErrClipboardUnsupported
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/pbcopy")
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("platform: write host clipboard: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
