package eyes

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/winterstim/tonelab-muse/pkg/ears"
)

func TestEncodeWAV16BitPCM(t *testing.T) {
	rate := 48000
	channels := 2
	bits := 16
	numFrames := 480 // 10ms

	raw := make([]byte, numFrames*channels*2)
	for i := 0; i < numFrames; i++ {
		// Left: 0.5 -> 16383, Right: -0.5 -> -16384 (0xC000)
		binary.LittleEndian.PutUint16(raw[i*4:i*4+2], 16383)
		binary.LittleEndian.PutUint16(raw[i*4+2:i*4+4], 0xC000)
	}

	encoded, err := EncodeWAV(raw, rate, channels, bits, false)
	if err != nil {
		t.Fatalf("EncodeWAV failed: %v", err)
	}

	if len(encoded) != 44+len(raw) {
		t.Fatalf("expected length %d, got %d", 44+len(raw), len(encoded))
	}
	if string(encoded[0:4]) != "RIFF" || string(encoded[8:12]) != "WAVE" {
		t.Fatalf("invalid RIFF/WAVE header")
	}
	if string(encoded[12:16]) != "fmt " {
		t.Fatalf("missing fmt chunk")
	}
	formatTag := binary.LittleEndian.Uint16(encoded[20:22])
	if formatTag != 1 {
		t.Fatalf("expected format tag 1 (PCM), got %d", formatTag)
	}
	chans := binary.LittleEndian.Uint16(encoded[22:24])
	if chans != uint16(channels) {
		t.Fatalf("expected %d channels, got %d", channels, chans)
	}
	sRate := binary.LittleEndian.Uint32(encoded[24:28])
	if sRate != uint32(rate) {
		t.Fatalf("expected sample rate %d, got %d", rate, sRate)
	}
	bPerSample := binary.LittleEndian.Uint16(encoded[34:36])
	if bPerSample != uint16(bits) {
		t.Fatalf("expected %d bits, got %d", bits, bPerSample)
	}

	// Verify it decodes cleanly with ears.ReadWAV
	dir := t.TempDir()
	wavPath := filepath.Join(dir, "test16.wav")
	if err := os.WriteFile(wavPath, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	audio, err := ears.ReadWAV(wavPath)
	if err != nil {
		t.Fatalf("ears.ReadWAV failed: %v", err)
	}
	if audio.Rate != rate {
		t.Fatalf("expected decoded rate %d, got %d", rate, audio.Rate)
	}
	if len(audio.Samples) != numFrames {
		t.Fatalf("expected %d mono samples, got %d", numFrames, len(audio.Samples))
	}
	// Mono mix of 0.5 and -0.5 is approximately 0.0
	for i, s := range audio.Samples {
		if math.Abs(s) > 0.01 {
			t.Fatalf("sample %d: expected ~0.0, got %f", i, s)
		}
	}
}

func TestEncodeWAV32BitFloat(t *testing.T) {
	rate := 44100
	channels := 2
	bits := 32
	numFrames := 441 // 10ms

	raw := make([]byte, numFrames*channels*4)
	for i := 0; i < numFrames; i++ {
		// Left: 0.75, Right: 0.25 -> mono average 0.5
		binary.LittleEndian.PutUint32(raw[i*8:i*8+4], math.Float32bits(0.75))
		binary.LittleEndian.PutUint32(raw[i*8+4:i*8+8], math.Float32bits(0.25))
	}

	encoded, err := EncodeWAV(raw, rate, channels, bits, true)
	if err != nil {
		t.Fatalf("EncodeWAV failed: %v", err)
	}

	formatTag := binary.LittleEndian.Uint16(encoded[20:22])
	if formatTag != 3 {
		t.Fatalf("expected format tag 3 (IEEE Float), got %d", formatTag)
	}

	dir := t.TempDir()
	wavPath := filepath.Join(dir, "test32.wav")
	if err := os.WriteFile(wavPath, encoded, 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	audio, err := ears.ReadWAV(wavPath)
	if err != nil {
		t.Fatalf("ears.ReadWAV failed: %v", err)
	}
	if audio.Rate != rate {
		t.Fatalf("expected decoded rate %d, got %d", rate, audio.Rate)
	}
	if len(audio.Samples) != numFrames {
		t.Fatalf("expected %d mono samples, got %d", numFrames, len(audio.Samples))
	}
	for i, s := range audio.Samples {
		if math.Abs(s-0.5) > 0.001 {
			t.Fatalf("sample %d: expected ~0.5, got %f", i, s)
		}
	}
}

func TestFloat32ToPCM16(t *testing.T) {
	floats := []float32{1.0, -1.0, 0.0, 0.5, -0.5, 2.0, -2.0}
	raw := make([]byte, len(floats)*4)
	for i, f := range floats {
		binary.LittleEndian.PutUint32(raw[i*4:(i+1)*4], math.Float32bits(f))
	}

	pcm := Float32ToPCM16(raw)
	if len(pcm) != len(floats)*2 {
		t.Fatalf("expected %d bytes, got %d", len(floats)*2, len(pcm))
	}

	// 1.0 -> 32767
	v0 := int16(binary.LittleEndian.Uint16(pcm[0:2]))
	if v0 != 32767 {
		t.Errorf("1.0 -> expected 32767, got %d", v0)
	}
	// -1.0 -> -32768
	v1 := int16(binary.LittleEndian.Uint16(pcm[2:4]))
	if v1 != -32768 {
		t.Errorf("-1.0 -> expected -32768, got %d", v1)
	}
	// 0.0 -> 0
	v2 := int16(binary.LittleEndian.Uint16(pcm[4:6]))
	if v2 != 0 {
		t.Errorf("0.0 -> expected 0, got %d", v2)
	}
	// Clamped: 2.0 -> 32767
	v5 := int16(binary.LittleEndian.Uint16(pcm[10:12]))
	if v5 != 32767 {
		t.Errorf("2.0 -> expected 32767, got %d", v5)
	}
	// Clamped: -2.0 -> -32768
	v6 := int16(binary.LittleEndian.Uint16(pcm[12:14]))
	if v6 != -32768 {
		t.Errorf("-2.0 -> expected -32768, got %d", v6)
	}
}

func TestAudioCaptureBufferAccumulationAndCapping(t *testing.T) {
	channels := 2
	rate := 48000
	bits := 16
	targetSeconds := 0.25 // 12000 frames
	targetFrames := int(targetSeconds * float64(rate))

	buf := NewAudioCaptureBuffer(channels, rate, bits, false, targetSeconds)
	if buf.TargetFrames != targetFrames {
		t.Fatalf("expected target frames %d, got %d", targetFrames, buf.TargetFrames)
	}

	packetBytes := 5000 * channels * 2
	packet := make([]byte, packetBytes)

	// Packet 1: 5000 frames
	acc, full := buf.AddPacket(packet, 5000, false)
	if acc != 5000 || full {
		t.Fatalf("packet 1: expected 5000 accepted, not full; got %d, full=%v", acc, full)
	}
	if buf.Frames() != 5000 {
		t.Fatalf("expected 5000 frames, got %d", buf.Frames())
	}

	// Packet 2: 5000 frames
	acc, full = buf.AddPacket(packet, 5000, false)
	if acc != 5000 || full {
		t.Fatalf("packet 2: expected 5000 accepted, not full; got %d, full=%v", acc, full)
	}
	if buf.Frames() != 10000 {
		t.Fatalf("expected 10000 frames, got %d", buf.Frames())
	}

	// Packet 3: 5000 frames (only 2000 remaining to reach 12000)
	acc, full = buf.AddPacket(packet, 5000, false)
	if acc != 2000 || !full {
		t.Fatalf("packet 3: expected 2000 accepted and full=true; got %d, full=%v", acc, full)
	}
	if buf.Frames() != targetFrames {
		t.Fatalf("expected %d frames, got %d", targetFrames, buf.Frames())
	}

	// Packet 4: should accept 0 and remain full
	acc, full = buf.AddPacket(packet, 1000, false)
	if acc != 0 || !full {
		t.Fatalf("packet 4: expected 0 accepted and full=true; got %d, full=%v", acc, full)
	}
}

func TestAudioCaptureBufferSilentPackets(t *testing.T) {
	channels := 2
	rate := 44100
	bits := 16
	targetSeconds := 0.1 // 4410 frames

	buf := NewAudioCaptureBuffer(channels, rate, bits, false, targetSeconds)
	acc, full := buf.AddPacket(nil, 4410, true)
	if acc != 4410 || !full {
		t.Fatalf("expected 4410 accepted and full=true; got %d, full=%v", acc, full)
	}

	for i, b := range buf.Bytes() {
		if b != 0 {
			t.Fatalf("byte %d non-zero in silent buffer: %d", i, b)
		}
	}

	dir := t.TempDir()
	outPath := filepath.Join(dir, "silent.wav")
	if err := buf.Save(outPath, 16); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	audio, err := ears.ReadWAV(outPath)
	if err != nil {
		t.Fatalf("ears.ReadWAV failed: %v", err)
	}
	if len(audio.Samples) != 4410 {
		t.Fatalf("expected 4410 samples, got %d", len(audio.Samples))
	}
	for i, s := range audio.Samples {
		if s != 0.0 {
			t.Fatalf("sample %d non-zero: %f", i, s)
		}
	}
}

func TestAudioCaptureBufferFloatTo16BitSave(t *testing.T) {
	channels := 2
	rate := 48000
	bits := 32
	targetSeconds := 0.05 // 2400 frames

	buf := NewAudioCaptureBuffer(channels, rate, bits, true, targetSeconds)

	raw := make([]byte, 2400*channels*4)
	for i := 0; i < 2400; i++ {
		binary.LittleEndian.PutUint32(raw[i*8:i*8+4], math.Float32bits(0.4))
		binary.LittleEndian.PutUint32(raw[i*8+4:i*8+8], math.Float32bits(0.4))
	}
	buf.AddPacket(raw, 2400, false)

	dir := t.TempDir()
	outPath := filepath.Join(dir, "converted.wav")
	if err := buf.Save(outPath, 16); err != nil {
		t.Fatalf("Save with targetBits=16 failed: %v", err)
	}

	audio, err := ears.ReadWAV(outPath)
	if err != nil {
		t.Fatalf("ears.ReadWAV failed: %v", err)
	}
	if len(audio.Samples) != 2400 {
		t.Fatalf("expected 2400 samples, got %d", len(audio.Samples))
	}
	for i, s := range audio.Samples {
		if math.Abs(s-0.4) > 0.01 {
			t.Fatalf("sample %d: expected ~0.4, got %f", i, s)
		}
	}
}

func TestAudioCaptureBufferEmptySaveError(t *testing.T) {
	buf := NewAudioCaptureBuffer(2, 48000, 16, false, 1.0)
	err := buf.Save(filepath.Join(t.TempDir(), "empty.wav"), 16)
	if err == nil {
		t.Fatal("expected error on empty buffer, got nil")
	}
}

type fakeTransport struct {
	playCalls int
	stopCalls int
}

func (f *fakeTransport) Transport(ctx context.Context, play bool) error {
	if play {
		f.playCalls++
	} else {
		f.stopCalls++
	}
	return nil
}

type fakeRecordOS struct {
	recordedBundle  string
	recordedSeconds float64
	recordedPath    string
}

func (f *fakeRecordOS) Windows() ([]Window, error) { return nil, nil }
func (f *fakeRecordOS) Capture(context.Context, int, string) error {
	return nil
}
func (f *fakeRecordOS) Record(ctx context.Context, bundle string, seconds float64, path string) error {
	f.recordedBundle = bundle
	f.recordedSeconds = seconds
	f.recordedPath = path
	return SaveWAV(path, make([]byte, 400), 44100, 2, 16, false)
}

func TestAppRendererWithOS(t *testing.T) {
	dir := t.TempDir()
	ft := &fakeTransport{}
	fos := &fakeRecordOS{}
	ar := &AppRenderer{
		T:      ft,
		OS:     fos,
		Bundle: "com.ableton.live",
		Dir:    dir,
	}

	path, err := ar.Render(context.Background(), 1, 0.5)
	if err != nil {
		t.Fatalf("Render failed: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty path")
	}
	if fos.recordedBundle != "com.ableton.live" {
		t.Fatalf("expected bundle com.ableton.live, got %s", fos.recordedBundle)
	}
	if fos.recordedSeconds != 0.5 {
		t.Fatalf("expected 0.5 seconds, got %f", fos.recordedSeconds)
	}
	if ft.playCalls != 1 || ft.stopCalls < 2 {
		t.Fatalf("expected transport calls, got play=%d stop=%d", ft.playCalls, ft.stopCalls)
	}
}
