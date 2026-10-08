//go:build darwin

package platform

import (
	"os"
	"os/exec"
	"syscall"
)

// afplayPath pins the absolute binary like the clipboard adapter pins
// /usr/bin/pbpaste: no PATH lookup at probe or spawn time.
const afplayPath = "/usr/bin/afplay"

// darwinAudio plays clips through afplay, present on every macOS install.
type darwinAudio struct{}

func defaultAudioPlayer() AudioPlayer {
	return darwinAudio{}
}

func (darwinAudio) Name() string {
	if _, err := os.Stat(afplayPath); err != nil {
		return ""
	}
	return "afplay"
}

func (darwinAudio) Available() bool {
	_, err := os.Stat(afplayPath)
	return err == nil
}

func (a darwinAudio) PlayWAV(wav []byte) error {
	if !a.Available() {
		return ErrAudioUnsupported
	}
	return playWAVDetached(func(file string) *exec.Cmd {
		cmd := exec.Command(afplayPath, file)
		// Own process group: a fire-and-forget clip survives signals
		// addressed to the runtime's group.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}, wav)
}
