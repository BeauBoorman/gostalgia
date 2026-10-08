//go:build darwin

package platform

import (
	"os/exec"
	"syscall"
)

// darwinAudio plays clips through afplay, present on every macOS install.
type darwinAudio struct{}

func defaultAudioPlayer() AudioPlayer {
	return darwinAudio{}
}

func (darwinAudio) Name() string {
	if _, err := exec.LookPath("afplay"); err != nil {
		return ""
	}
	return "afplay"
}

func (darwinAudio) Available() bool {
	_, err := exec.LookPath("afplay")
	return err == nil
}

func (a darwinAudio) PlayWAV(wav []byte) error {
	path, err := exec.LookPath("afplay")
	if err != nil {
		return ErrAudioUnsupported
	}
	return playWAVDetached(func(file string) *exec.Cmd {
		cmd := exec.Command(path, file)
		// Own process group: a fire-and-forget clip survives signals
		// addressed to the runtime's group.
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return cmd
	}, wav)
}
