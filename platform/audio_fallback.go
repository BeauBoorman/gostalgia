//go:build !darwin && !linux && !windows

package platform

// No host audio player is known for this platform; the adapter reports
// unsupported honestly rather than pretending to play.
func defaultAudioPlayer() AudioPlayer {
	return unsupportedAudio{}
}
