package muse

import (
	"encoding/binary"
	"math"
	"testing"
)

// wavFile builds a WAV with one fmt and one data chunk.
func wavFile(format, channels, rate, bits int, data []byte) []byte {
	b := make([]byte, 44+len(data))
	copy(b[0:4], "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(36+len(data)))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], uint16(format))
	binary.LittleEndian.PutUint16(b[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(b[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:32], uint32(rate*channels*bits/8))
	binary.LittleEndian.PutUint16(b[32:34], uint16(channels*bits/8))
	binary.LittleEndian.PutUint16(b[34:36], uint16(bits))
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(len(data)))
	copy(b[44:], data)
	return b
}

func readSpeechWAV(t *testing.T, b []byte) (rate, channels, bits, samples int) {
	t.Helper()
	if string(b[0:4]) != "RIFF" || string(b[36:40]) != "data" {
		t.Fatalf("not a canonical WAV header")
	}
	channels = int(binary.LittleEndian.Uint16(b[22:24]))
	rate = int(binary.LittleEndian.Uint32(b[24:28]))
	bits = int(binary.LittleEndian.Uint16(b[34:36]))
	samples = int(binary.LittleEndian.Uint32(b[40:44])) / 2
	return
}

func TestPushToTalkWAVResamplesTheMacOSRecording(t *testing.T) {
	// one second of 22.05 kHz mono 16-bit audio, as afconvert writes it
	data := make([]byte, 22050*2)
	for i := 0; i < 22050; i++ {
		binary.LittleEndian.PutUint16(data[2*i:], uint16(int16(8000*math.Sin(float64(i)/10))))
	}
	out, err := pushToTalkWAV(wavFile(1, 1, 22050, 16, data))
	if err != nil {
		t.Fatal(err)
	}
	rate, ch, bits, n := readSpeechWAV(t, out)
	if rate != 16000 || ch != 1 || bits != 16 {
		t.Fatalf("got %d Hz, %d ch, %d bit; want 16000 Hz mono 16-bit", rate, ch, bits)
	}
	if n < 15990 || n > 16010 {
		t.Fatalf("got %d samples for one second, want about 16000", n)
	}
}

func TestPushToTalkWAVPassesSpeechRateThrough(t *testing.T) {
	in := wavFile(1, 1, 16000, 16, make([]byte, 3200))
	out, err := pushToTalkWAV(in)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(in) {
		t.Fatal("16 kHz mono 16-bit audio should be sent as it is")
	}
}

func TestPushToTalkWAVMixesStereoFloatDown(t *testing.T) {
	data := make([]byte, 0, 8*8)
	for i := 0; i < 8; i++ { // 8 stereo frames of float32 at 48 kHz
		for c := 0; c < 2; c++ {
			data = binary.LittleEndian.AppendUint32(data, math.Float32bits(0.5))
		}
	}
	out, err := pushToTalkWAV(wavFile(3, 2, 48000, 32, data))
	if err != nil {
		t.Fatal(err)
	}
	rate, ch, bits, _ := readSpeechWAV(t, out)
	if rate != 16000 || ch != 1 || bits != 16 {
		t.Fatalf("got %d Hz, %d ch, %d bit", rate, ch, bits)
	}
	if s := int16(binary.LittleEndian.Uint16(out[44:])); s < 16000 || s > 16500 {
		t.Fatalf("first sample %d, want about 0.5 full scale", s)
	}
}

func TestPushToTalkWAVRejectsNonWAV(t *testing.T) {
	if _, err := pushToTalkWAV([]byte("not a wav file at all, just text.....")); err == nil {
		t.Fatal("expected an error")
	}
}
