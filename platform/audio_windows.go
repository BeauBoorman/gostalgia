//go:build windows

package platform

import (
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// windowsAudioShells are probed in preference order. Windows PowerShell
// ships with the OS; PowerShell Core may not be installed.
var windowsAudioShells = []string{"powershell", "pwsh"}

// windowsAudio plays clips through System.Media.SoundPlayer (the .NET
// WaveOut path) driven by a detached PowerShell process — Windows has no
// built-in command-line file player, but every install has the API.
type windowsAudio struct{}

func defaultAudioPlayer() AudioPlayer {
	return windowsAudio{}
}

func (windowsAudio) probe() (path, name string) {
	for _, bin := range windowsAudioShells {
		if p, err := exec.LookPath(bin); err == nil {
			return p, bin
		}
	}
	return "", ""
}

func (w windowsAudio) Name() string {
	_, name := w.probe()
	return name
}

func (w windowsAudio) Available() bool {
	p, _ := w.probe()
	return p != ""
}

func (w windowsAudio) PlayWAV(wav []byte) error {
	path, _ := w.probe()
	if path == "" {
		return ErrAudioUnsupported
	}
	return playWAVDetached(func(file string) *exec.Cmd {
		// PlaySync keeps the shell alive until playback ends, so the
		// staging file's lifetime is bounded by the reaper goroutine.
		script := fmt.Sprintf(
			"(New-Object System.Media.SoundPlayer '%s').PlaySync()",
			strings.ReplaceAll(file, "'", "''"),
		)
		cmd := exec.Command(path, "-NoProfile", "-NonInteractive", "-Command", script)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		return cmd
	}, wav)
}
