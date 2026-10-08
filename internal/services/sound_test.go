package services

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"gostalgia/internal/ipc"
	"gostalgia/internal/security"
	"gostalgia/internal/service"
	"gostalgia/platform"
)

// fakeAudioPlayer records clips instead of touching host audio hardware.
type fakeAudioPlayer struct {
	mu    sync.Mutex
	avail bool
	clips [][]byte
	err   error
}

func (f *fakeAudioPlayer) Name() string {
	if !f.avail {
		return ""
	}
	return "fake"
}
func (f *fakeAudioPlayer) Available() bool { return f.avail }
func (f *fakeAudioPlayer) PlayWAV(wav []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.clips = append(f.clips, append([]byte(nil), wav...))
	return nil
}

func (f *fakeAudioPlayer) clip(i int) []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]byte(nil), f.clips[i]...)
}

func (f *fakeAudioPlayer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.clips)
}

func setupSound(t *testing.T, avail bool) (*ipc.Router, *fakeAudioPlayer) {
	t.Helper()
	fake := &fakeAudioPlayer{avail: avail}
	orig := platform.GetAudioPlayer()
	platform.SetAudioPlayer(fake)
	t.Cleanup(func() { platform.SetAudioPlayer(orig) })

	r := ipc.NewRouter()
	ctx := &service.Context{Router: r}
	svc := NewSound()
	if err := svc.Init(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop(context.Background()) })
	return r, fake
}

func soundCall(r *ipc.Router, caps []string, params any) ipc.Response {
	b, _ := json.Marshal(params)
	ctx := ipc.WithCapabilities(context.Background(), security.NewCapabilities(caps...))
	return r.Dispatch(ctx, ipc.Request{ID: 1, Method: "sound/play", Params: b})
}

func TestSoundPlayPreset(t *testing.T) {
	r, fake := setupSound(t, true)
	resp := soundCall(r, []string{security.CapSound}, map[string]string{"sound": "bark"})
	if !resp.OK {
		t.Fatalf("sound/play rejected: %s", resp.Error)
	}
	var out struct {
		Played     bool   `json:"played"`
		Player     string `json:"player"`
		Notes      int    `json:"notes"`
		DurationMs int    `json:"duration_ms"`
		Bytes      int    `json:"bytes"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Played || out.Player != "fake" || out.Notes != 3 || out.DurationMs != 260 {
		t.Fatalf("unexpected result: %+v", out)
	}
	if fake.count() != 1 {
		t.Fatalf("adapter saw %d clips, want 1", fake.count())
	}

	// The adapter received a complete, well-formed WAV container.
	wav := fake.clip(0)
	if len(wav) != out.Bytes {
		t.Fatalf("bytes reported %d, clip is %d", out.Bytes, len(wav))
	}
	if string(wav[:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		t.Fatal("clip is not a RIFF/WAVE container")
	}
	dataLen := binary.LittleEndian.Uint32(wav[40:44])
	if int(dataLen) != len(wav)-44 {
		t.Fatalf("data chunk = %d bytes, want %d", dataLen, len(wav)-44)
	}
	wantSamples := out.DurationMs * soundSampleRate / 1000
	if int(dataLen) != wantSamples*2 {
		t.Fatalf("data chunk = %d bytes, want %d samples", dataLen, wantSamples)
	}
}

func TestSoundPlayNotes(t *testing.T) {
	r, fake := setupSound(t, true)
	resp := soundCall(r, []string{security.CapSound}, map[string]any{
		"notes": []map[string]any{
			{"frequency_hz": 440, "duration_ms": 100, "wave": "sine"},
			{"frequency_hz": 0, "duration_ms": 50, "wave": "sine"},
			{"frequency_hz": 660, "duration_ms": 100, "wave": "triangle"},
		},
	})
	if !resp.OK {
		t.Fatalf("sound/play rejected: %s", resp.Error)
	}
	if fake.count() != 1 {
		t.Fatalf("adapter saw %d clips, want 1", fake.count())
	}
	var out struct {
		DurationMs int `json:"duration_ms"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.DurationMs != 250 {
		t.Fatalf("duration_ms = %d, want 250", out.DurationMs)
	}
}

func TestSoundPlayMissingCapability(t *testing.T) {
	r, fake := setupSound(t, true)
	resp := soundCall(r, []string{security.CapIPC}, map[string]string{"sound": "beep"})
	if resp.OK {
		t.Fatal("sound/play succeeded without the sound capability")
	}
	if !strings.Contains(resp.Error, "missing capability") {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
	if fake.count() != 0 {
		t.Fatal("denied request still reached the adapter")
	}
}

func TestSoundPlayUnsupportedHost(t *testing.T) {
	r, fake := setupSound(t, false)
	resp := soundCall(r, []string{security.CapSound}, map[string]string{"sound": "beep"})
	if resp.OK {
		t.Fatal("sound/play reported success on an unsupported host")
	}
	if !strings.Contains(resp.Error, "unsupported") {
		t.Fatalf("error should report unsupported honestly, got: %s", resp.Error)
	}
	if fake.count() != 0 {
		t.Fatal("unsupported host still received a clip")
	}
}

func TestSoundPlayValidation(t *testing.T) {
	r, fake := setupSound(t, true)
	note := func(freq float64, ms int, wave string) map[string]any {
		return map[string]any{"frequency_hz": freq, "duration_ms": ms, "wave": wave}
	}
	longSeq := make([]map[string]any, 0, maxSoundNotes+1)
	for i := 0; i < maxSoundNotes+1; i++ {
		longSeq = append(longSeq, note(440, 100, "sine"))
	}
	tooLong := make([]map[string]any, 0, 6)
	for i := 0; i < 6; i++ {
		tooLong = append(tooLong, note(440, 2000, "sine"))
	}

	tests := []struct {
		name   string
		params any
		want   string
	}{
		{"empty params", map[string]any{}, "required"},
		{"sound and notes", map[string]any{"sound": "beep", "notes": []any{note(440, 100, "sine")}}, "not both"},
		{"bad name chars", map[string]any{"sound": "../evil"}, "invalid sound name"},
		{"long name", map[string]any{"sound": strings.Repeat("b", maxSoundName+1)}, "invalid sound name"},
		{"unknown preset", map[string]any{"sound": "meow"}, "unknown sound"},
		{"too many notes", map[string]any{"notes": longSeq}, "note"},
		{"total duration", map[string]any{"notes": tooLong}, "exceeds"},
		{"bad wave", map[string]any{"notes": []any{note(440, 100, "noise")}}, "wave"},
		{"missing wave", map[string]any{"notes": []any{map[string]any{"frequency_hz": 440, "duration_ms": 100}}}, "wave"},
		{"short note", map[string]any{"notes": []any{note(440, 1, "sine")}}, "duration_ms"},
		{"long note", map[string]any{"notes": []any{note(440, maxSoundNoteMs+1, "sine")}}, "duration_ms"},
		{"low frequency", map[string]any{"notes": []any{note(5, 100, "sine")}}, "frequency_hz"},
		{"high frequency", map[string]any{"notes": []any{note(20000, 100, "sine")}}, "frequency_hz"},
		{"negative frequency", map[string]any{"notes": []any{note(-440, 100, "sine")}}, "frequency_hz"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp := soundCall(r, []string{security.CapSound}, tc.params)
			if resp.OK {
				t.Fatalf("params %+v accepted", tc.params)
			}
			if !strings.Contains(resp.Error, tc.want) {
				t.Fatalf("error %q does not contain %q", resp.Error, tc.want)
			}
		})
	}
	if fake.count() != 0 {
		t.Fatalf("rejected requests reached the adapter: %d clips", fake.count())
	}
}

func TestSoundPlayPlayerFailure(t *testing.T) {
	r, fake := setupSound(t, true)
	fake.err = errors.New("player exploded")
	resp := soundCall(r, []string{security.CapSound}, map[string]string{"sound": "beep"})
	if resp.OK {
		t.Fatal("adapter failure reported as success")
	}
	if !strings.Contains(resp.Error, "player exploded") {
		t.Fatalf("unexpected error: %s", resp.Error)
	}
}

func TestSoundStatus(t *testing.T) {
	r, _ := setupSound(t, true)
	ctx := ipc.WithCapabilities(context.Background(), security.NewCapabilities(security.CapIPC))
	resp := r.Dispatch(ctx, ipc.Request{ID: 1, Method: "sound/status"})
	if !resp.OK {
		t.Fatalf("sound/status rejected: %s", resp.Error)
	}
	var out struct {
		Supported   bool     `json:"supported"`
		Player      string   `json:"player"`
		SampleRate  int      `json:"sample_rate"`
		MaxNotes    int      `json:"max_notes"`
		MaxDuration int      `json:"max_duration_ms"`
		Presets     []string `json:"presets"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if !out.Supported || out.Player != "fake" {
		t.Fatalf("status = %+v", out)
	}
	if out.SampleRate != soundSampleRate || out.MaxNotes != maxSoundNotes || out.MaxDuration != maxSoundDurationMs {
		t.Fatalf("status bounds = %+v", out)
	}
	if len(out.Presets) == 0 {
		t.Fatal("status reports no presets")
	}
}

func TestSoundStatusUnsupported(t *testing.T) {
	r, _ := setupSound(t, false)
	ctx := ipc.WithCapabilities(context.Background(), security.NewCapabilities(security.CapIPC))
	resp := r.Dispatch(ctx, ipc.Request{ID: 1, Method: "sound/status"})
	if !resp.OK {
		t.Fatalf("sound/status rejected: %s", resp.Error)
	}
	var out struct {
		Supported bool   `json:"supported"`
		Player    string `json:"player"`
	}
	if err := json.Unmarshal(resp.Data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Supported || out.Player != "" {
		t.Fatalf("status = %+v, want supported:false", out)
	}
}

func TestSynthesizePCMBounds(t *testing.T) {
	notes := []soundNote{
		{FrequencyHz: 440, DurationMs: 100, Wave: "sine"},
		{FrequencyHz: 0, DurationMs: 50, Wave: "sine"},
		{FrequencyHz: 220, DurationMs: 100, Wave: "sawtooth"},
	}
	pcm := synthesizePCM(notes)
	want := 250 * soundSampleRate / 1000
	if len(pcm) != want {
		t.Fatalf("pcm length = %d, want %d", len(pcm), want)
	}

	// The rest is true silence — no DC offset.
	restStart := 100 * soundSampleRate / 1000
	restEnd := restStart + 50*soundSampleRate/1000
	for i := restStart; i < restEnd; i++ {
		if pcm[i] != 0 {
			t.Fatalf("rest sample %d = %d, want 0", i, pcm[i])
		}
	}

	// Sine content sits inside the amplitude bound, and the envelope
	// starts silent at the note edge.
	if pcm[0] != 0 {
		t.Errorf("first sample = %d, want 0 (attack envelope)", pcm[0])
	}
	maxAmp := int16(0)
	for _, s := range pcm[:restStart] {
		if s < 0 {
			s = -s
		}
		if s > maxAmp {
			maxAmp = s
		}
	}
	if maxAmp == 0 || float64(maxAmp) > soundAmplitude*32767+1 {
		t.Errorf("sine peak amplitude = %d, out of bounds", maxAmp)
	}
}

func TestSynthesizeAllWaveShapes(t *testing.T) {
	for _, wave := range []string{"sine", "square", "triangle", "sawtooth"} {
		pcm := synthesizePCM([]soundNote{{FrequencyHz: 440, DurationMs: 100, Wave: wave}})
		if len(pcm) != 100*soundSampleRate/1000 {
			t.Fatalf("%s: length = %d", wave, len(pcm))
		}
		var nonzero bool
		for _, s := range pcm {
			if s != 0 {
				nonzero = true
			}
		}
		if !nonzero {
			t.Errorf("%s: synthesized silence", wave)
		}
	}
}
