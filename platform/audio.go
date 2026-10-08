package platform

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

// ErrAudioUnsupported is returned when the host has no usable audio
// playback mechanism.
var ErrAudioUnsupported = errors.New("platform: host audio playback is unsupported")

// ErrAudioBusy is returned when the in-flight playback cap is saturated:
// too many detached host players are already running. It is an honest
// overload signal — callers may retry once a running clip finishes.
var ErrAudioBusy = errors.New("platform: audio playback busy")

// audioPlayTimeout bounds how long a spawned host player may live before
// the runtime kills it. Callers bound clips far below this; the cap exists
// so a hung player process cannot linger forever.
const audioPlayTimeout = 30 * time.Second

// maxAudioInFlight bounds concurrent detached host players so a caller
// looping requests cannot accumulate unbounded child processes and staging
// files. audioInFlight is the counting semaphore: a slot is held from
// spawn until the reaper observes process exit.
const maxAudioInFlight = 4

var audioInFlight = make(chan struct{}, maxAudioInFlight)

// AudioPlayer is the platform contract for host audio output. The runtime
// synthesizes a finished WAV clip in-process; the player only hands that
// clip to a host mechanism. PlayWAV is fire-and-forget: it returns once the
// host player has accepted the clip, never after playback completes, and it
// must not carry caller input — only the runtime's own rendered clip.
type AudioPlayer interface {
	// Name identifies the discovered mechanism ("afplay", "pw-play",
	// "powershell", ...) for diagnostics, or "" when unsupported.
	Name() string
	// Available reports whether a usable host player exists right now.
	Available() bool
	// PlayWAV starts playback of a complete WAV container.
	PlayWAV(wav []byte) error
}

var (
	audioMu      sync.RWMutex
	currentAudio AudioPlayer = defaultAudioPlayer()
)

// GetAudioPlayer returns the active host audio player.
func GetAudioPlayer() AudioPlayer {
	audioMu.RLock()
	defer audioMu.RUnlock()
	return currentAudio
}

// SetAudioPlayer swaps the active host audio player (useful for testing).
// Passing nil restores the platform default.
func SetAudioPlayer(p AudioPlayer) {
	audioMu.Lock()
	defer audioMu.Unlock()
	if p == nil {
		currentAudio = defaultAudioPlayer()
		return
	}
	currentAudio = p
}

// HostAudioAvailable reports whether host audio playback is supported and
// usable on this machine.
func HostAudioAvailable() bool {
	return GetAudioPlayer().Available()
}

// PlayHostAudio hands a finished WAV clip to the active host player.
func PlayHostAudio(wav []byte) error {
	return GetAudioPlayer().PlayWAV(wav)
}

// unsupportedAudio is the adapter for hosts with no playback mechanism.
type unsupportedAudio struct{}

func (unsupportedAudio) Name() string         { return "" }
func (unsupportedAudio) Available() bool      { return false }
func (unsupportedAudio) PlayWAV([]byte) error { return ErrAudioUnsupported }

// playWAVDetached writes wav to a private temp file and spawns the host
// player built by build detached: standard streams are discarded so a noisy
// player cannot corrupt the interactive shell, and a reaper goroutine waits
// for exit (or kills the process at audioPlayTimeout) before removing the
// staging file. It returns once the process has started, and never queues:
// at most maxAudioInFlight players may be live at once, with saturation
// reported as ErrAudioBusy. If the runtime exits mid-clip the detached
// player is orphaned and its private 0600 staging file is left behind
// (bounded to in-flight clips). Windows has no new-process-group parity:
// console Ctrl+C kills the child player too, though the reaper still
// removes the staging file while the runtime lives.
func playWAVDetached(build func(file string) *exec.Cmd, wav []byte) error {
	if len(wav) == 0 {
		return fmt.Errorf("platform: empty audio clip")
	}
	select {
	case audioInFlight <- struct{}{}:
	default:
		return ErrAudioBusy
	}
	f, err := os.CreateTemp("", "gostalgia-audio-*.wav")
	if err != nil {
		<-audioInFlight
		return fmt.Errorf("platform: stage audio clip: %w", err)
	}
	file := f.Name()
	if _, err := f.Write(wav); err != nil {
		f.Close()
		os.Remove(file)
		<-audioInFlight
		return fmt.Errorf("platform: stage audio clip: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(file)
		<-audioInFlight
		return fmt.Errorf("platform: stage audio clip: %w", err)
	}

	cmd := build(file)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		os.Remove(file)
		<-audioInFlight
		return fmt.Errorf("platform: start audio player: %w", err)
	}
	go func() {
		defer func() { <-audioInFlight }()
		timer := time.AfterFunc(audioPlayTimeout, func() { _ = cmd.Process.Kill() })
		defer timer.Stop()
		_ = cmd.Wait()
		_ = os.Remove(file)
	}()
	return nil
}
