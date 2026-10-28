//go:build windows

package eyes

func defaultOS() OS {
	return WindowsAudioRecorder{}
}
