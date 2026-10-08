//go:build linux

package platform

import (
	"os/exec"
	"syscall"
)

// linuxAudioPlayers lists host players probed in preference order: PipeWire,
// PulseAudio, then bare ALSA. All accept a WAV file path and exit at end of
// playback.
var linuxAudioPlayers = []string{"pw-play", "paplay", "aplay"}

type linuxAudio struct{}

func defaultAudioPlayer() AudioPlayer {
	return linuxAudio{}
}

// probe returns the resolved path and name of the first usable player.
func (linuxAudio) probe() (path, name string) {
	for _, bin := range linuxAudioPlayers {
		if p, err := exec.LookPath(bin); err == nil {
			return p, bin
		}
	}
	return "", ""
}

func (a linuxAudio) Name() string {
	_, name := a.probe()
	return name
}

func (a linuxAudio) Available() bool {
	p, _ := a.probe()
	return p != ""
}

func (a linuxAudio) PlayWAV(wav []byte) error {
	path, _ := a.probe()
	if path == "" {
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
