// Package ears turns a rendered WAV into what the agent can reason about:
// band energies, brightness, compression and a spectrogram image for Muse
// vision. Pure Go, no DAW and no model.
package ears

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Audio is mono samples in -1..1 at Rate Hz.
type Audio struct {
	Rate    int
	Samples []float64
}

// ReadWAV reads PCM 16/24/32-bit or float32 WAV and mixes it to mono.
func ReadWAV(path string) (Audio, error) {
	f, err := os.Open(path)
	if err != nil {
		return Audio{}, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[:4]) != "RIFF" || string(hdr[8:]) != "WAVE" {
		return Audio{}, fmt.Errorf("%s: not a WAV file", path)
	}
	var format, channels, bits int
	var rate int
	for {
		var ch [8]byte
		if _, err := io.ReadFull(f, ch[:]); err != nil {
			return Audio{}, fmt.Errorf("%s: no data chunk", path)
		}
		id, size := string(ch[:4]), int64(binary.LittleEndian.Uint32(ch[4:]))
		switch id {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(f, b); err != nil {
				return Audio{}, err
			}
			format = int(binary.LittleEndian.Uint16(b[0:]))
			channels = int(binary.LittleEndian.Uint16(b[2:]))
			rate = int(binary.LittleEndian.Uint32(b[4:]))
			bits = int(binary.LittleEndian.Uint16(b[14:]))
			if format == 0xFFFE && size >= 26 { // WAVE_FORMAT_EXTENSIBLE: real format in the GUID
				format = int(binary.LittleEndian.Uint16(b[24:]))
			}
		case "data":
			if channels == 0 {
				return Audio{}, errors.New("data before fmt")
			}
			b := make([]byte, size)
			n, _ := io.ReadFull(f, b)
			return decode(b[:n], format, channels, bits, rate)
		default:
			if _, err := f.Seek(size+size%2, io.SeekCurrent); err != nil {
				return Audio{}, err
			}
		}
	}
}

func decode(b []byte, format, channels, bits, rate int) (Audio, error) {
	width := bits / 8
	if width == 0 || channels == 0 {
		return Audio{}, errors.New("bad WAV format")
	}
	var sample func([]byte) float64
	switch {
	case format == 1 && bits == 16:
		sample = func(p []byte) float64 { return float64(int16(binary.LittleEndian.Uint16(p))) / 32768 }
	case format == 1 && bits == 24:
		sample = func(p []byte) float64 {
			v := int32(p[0]) | int32(p[1])<<8 | int32(int8(p[2]))<<16
			return float64(v) / 8388608
		}
	case format == 1 && bits == 32:
		sample = func(p []byte) float64 { return float64(int32(binary.LittleEndian.Uint32(p))) / 2147483648 }
	case format == 3 && bits == 32:
		sample = func(p []byte) float64 { return float64(math.Float32frombits(binary.LittleEndian.Uint32(p))) }
	default:
		return Audio{}, fmt.Errorf("unsupported WAV: format %d, %d bits", format, bits)
	}
	frame := width * channels
	out := make([]float64, len(b)/frame)
	for i := range out {
		var sum float64
		for c := 0; c < channels; c++ {
			sum += sample(b[i*frame+c*width:])
		}
		out[i] = sum / float64(channels)
	}
	return Audio{Rate: rate, Samples: out}, nil
}
