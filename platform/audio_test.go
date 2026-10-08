package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeAudioPlayer is an injected adapter: it records clips instead of
// touching host audio hardware.
type fakeAudioPlayer struct {
	mu    sync.Mutex
	name  string
	avail bool
	clips [][]byte
	err   error
}

func (f *fakeAudioPlayer) Name() string    { return f.name }
func (f *fakeAudioPlayer) Available() bool { return f.avail }
func (f *fakeAudioPlayer) PlayWAV(wav []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.clips = append(f.clips, append([]byte(nil), wav...))
	return f.err
}

func (f *fakeAudioPlayer) clipCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clips)
}

func TestAudioPlayerRegistry(t *testing.T) {
	orig := GetAudioPlayer()
	defer SetAudioPlayer(orig)

	fake := &fakeAudioPlayer{name: "fake", avail: true}
	SetAudioPlayer(fake)
	if GetAudioPlayer() != fake {
		t.Fatal("SetAudioPlayer did not install the fake")
	}
	if !HostAudioAvailable() {
		t.Fatal("HostAudioAvailable false with available fake")
	}
	if err := PlayHostAudio([]byte("RIFF-fake")); err != nil {
		t.Fatalf("PlayHostAudio: %v", err)
	}
	if fake.clipCount() != 1 {
		t.Fatalf("fake saw %d clips, want 1", fake.clipCount())
	}

	// nil restores the platform default.
	SetAudioPlayer(nil)
	if GetAudioPlayer() == fake {
		t.Fatal("SetAudioPlayer(nil) did not restore the default")
	}
}

func TestUnsupportedAudioAdapter(t *testing.T) {
	var a unsupportedAudio
	if a.Available() {
		t.Error("unsupportedAudio reports available")
	}
	if a.Name() != "" {
		t.Errorf("unsupportedAudio name = %q, want empty", a.Name())
	}
	if err := a.PlayWAV([]byte("RIFF-x")); !errors.Is(err, ErrAudioUnsupported) {
		t.Errorf("PlayWAV err = %v, want ErrAudioUnsupported", err)
	}
}

// TestAudioHelperProcess is spawned by TestPlayWAVDetached as a stand-in
// host player: it exits immediately when GO_AUDIO_HELPER is set.
func TestAudioHelperProcess(t *testing.T) {
	if os.Getenv("GO_AUDIO_HELPER") != "1" {
		return
	}
	os.Exit(0)
}

func TestPlayWAVDetached(t *testing.T) {
	if testing.Short() {
		t.Skip("process spawn")
	}
	var file string
	err := playWAVDetached(func(f string) *exec.Cmd {
		file = f
		cmd := exec.Command(os.Args[0], "-test.run=TestAudioHelperProcess")
		cmd.Env = append(os.Environ(), "GO_AUDIO_HELPER=1")
		return cmd
	}, []byte("RIFF-fake-clip"))
	if err != nil {
		t.Fatalf("playWAVDetached: %v", err)
	}
	if file == "" {
		t.Fatal("build func never saw the staging file")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("staging file missing at spawn time: %v", err)
	}

	// The reaper removes the staging file once the player exits.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(file); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("staging file %q still present after player exit", file)
}

func TestPlayWAVDetachedStartFailure(t *testing.T) {
	var file string
	err := playWAVDetached(func(f string) *exec.Cmd {
		file = f
		return exec.Command(filepath.Join(t.TempDir(), "no-such-player"))
	}, []byte("RIFF-fake-clip"))
	if err == nil {
		t.Fatal("expected start failure")
	}
	if _, statErr := os.Stat(file); !os.IsNotExist(statErr) {
		t.Fatalf("staging file leaked after start failure: %v", statErr)
	}
}

func TestPlayWAVDetachedRejectsEmptyClip(t *testing.T) {
	called := false
	err := playWAVDetached(func(string) *exec.Cmd {
		called = true
		return exec.Command("true")
	}, nil)
	if err == nil {
		t.Fatal("empty clip accepted")
	}
	if called {
		t.Fatal("build func called for an empty clip")
	}
}
