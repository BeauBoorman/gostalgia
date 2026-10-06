//go:build linux

package platform

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type linuxClipboard struct{}

func defaultHostClipboard() HostClipboardAdapter {
	return &linuxClipboard{}
}

func (l *linuxClipboard) getReadCmd(ctx context.Context) *exec.Cmd {
	if p, err := exec.LookPath("wl-paste"); err == nil {
		return exec.CommandContext(ctx, p, "--no-newline")
	}
	if p, err := exec.LookPath("xclip"); err == nil {
		return exec.CommandContext(ctx, p, "-selection", "clipboard", "-o")
	}
	if p, err := exec.LookPath("xsel"); err == nil {
		return exec.CommandContext(ctx, p, "--clipboard", "--output")
	}
	return nil
}

func (l *linuxClipboard) getWriteCmd(ctx context.Context) *exec.Cmd {
	if p, err := exec.LookPath("wl-copy"); err == nil {
		return exec.CommandContext(ctx, p)
	}
	if p, err := exec.LookPath("xclip"); err == nil {
		return exec.CommandContext(ctx, p, "-selection", "clipboard")
	}
	if p, err := exec.LookPath("xsel"); err == nil {
		return exec.CommandContext(ctx, p, "--clipboard", "--input")
	}
	return nil
}

func (l *linuxClipboard) Available() bool {
	return l.getReadCmd(context.Background()) != nil && l.getWriteCmd(context.Background()) != nil
}

func (l *linuxClipboard) ReadText(ctx context.Context) (string, error) {
	cmd := l.getReadCmd(ctx)
	if cmd == nil {
		return "", ErrClipboardUnsupported
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("platform: read host clipboard: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (l *linuxClipboard) WriteText(ctx context.Context, text string) error {
	cmd := l.getWriteCmd(ctx)
	if cmd == nil {
		return ErrClipboardUnsupported
	}
	cmd.Stdin = strings.NewReader(text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("platform: write host clipboard: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
