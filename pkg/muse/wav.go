package muse

import (
	"encoding/binary"
	"errors"
	"math"
)

// speechRate is what the transcriber takes for push-to-talk audio. 24 kHz
// is also accepted and passes through untouched.
const speechRate = 16000

// pushToTalkWAV turns any PCM or float WAV into the format the transcriber
// takes: mono 16-bit at 16 or 24 kHz. Audio already in that format is
// returned as it is; anything else (another rate, stereo, 8/24/32-bit,
// float) is mixed down to mono and resampled to 16 kHz.
func pushToTalkWAV(in []byte) ([]byte, error) {
	if len(in) < 12 || string(in[0:4]) != "RIFF" || string(in[8:12]) != "WAVE" {
		return nil, errors.New("send a WAV recording")
	}
	var format, channels, rate, bits int
	var data []byte
	haveFmt := false
	for p := 12; p+8 <= len(in); {
		id := string(in[p : p+4])
		size := int(binary.LittleEndian.Uint32(in[p+4 : p+8]))
		start := p + 8
		end := start + size
		if end > len(in) || end < start {
			end = len(in)
		}
		body := in[start:end]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("WAV format chunk is too short")
			}
			format = int(binary.LittleEndian.Uint16(body[0:2]))
			channels = int(binary.LittleEndian.Uint16(body[2:4]))
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
			bits = int(binary.LittleEndian.Uint16(body[14:16]))
			if format == 0xFFFE && len(body) >= 26 { // extensible: the sub-format holds the type
				format = int(binary.LittleEndian.Uint16(body[24:26]))
			}
			haveFmt = true
		case "data":
			data = body
		}
		p = start + size + size%2
	}
	if !haveFmt || data == nil {
		return nil, errors.New("WAV has no format or audio data")
	}
	if channels < 1 || rate < 1 {
		return nil, errors.New("WAV has no channels or sample rate")
	}
	if format == 1 && bits == 16 && channels == 1 && (rate == speechRate || rate == 24000) {
		return in, nil
	}
	samples, err := decodeSamples(data, format, channels, bits)
	if err != nil {
		return nil, err
	}
	return encodeSpeechWAV(resample(samples, rate, speechRate)), nil
}

// decodeSamples mixes every frame down to one channel in [-1, 1].
func decodeSamples(data []byte, format, channels, bits int) ([]float64, error) {
	bytesPer := bits / 8
	if bytesPer < 1 || (format == 3 && bits != 32) || (format != 1 && format != 3) {
		return nil, errors.New("WAV sample format is not supported: send PCM or float audio")
	}
	frame := bytesPer * channels
	out := make([]float64, 0, len(data)/frame)
	for f := 0; f+frame <= len(data); f += frame {
		sum := 0.0
		for c := 0; c < channels; c++ {
			s := data[f+c*bytesPer:]
			var v float64
			switch {
			case format == 3:
				v = float64(math.Float32frombits(binary.LittleEndian.Uint32(s)))
			case bits == 8:
				v = (float64(s[0]) - 128) / 128
			case bits == 16:
				v = float64(int16(binary.LittleEndian.Uint16(s))) / 32768
			case bits == 24:
				v = float64(int32(uint32(s[0])<<8|uint32(s[1])<<16|uint32(s[2])<<24)>>8) / 8388608
			case bits == 32:
				v = float64(int32(binary.LittleEndian.Uint32(s))) / 2147483648
			default:
				return nil, errors.New("WAV bit depth is not supported")
			}
			sum += v
		}
		out = append(out, sum/float64(channels))
	}
	return out, nil
}

// resample converts by linear interpolation, which is plenty for speech.
func resample(in []float64, from, to int) []float64 {
	if from == to || len(in) == 0 {
		return in
	}
	n := int(float64(len(in)) * float64(to) / float64(from))
	out := make([]float64, n)
	step := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * step
		k := int(pos)
		frac := pos - float64(k)
		next := k + 1
		if next >= len(in) {
			next = len(in) - 1
		}
		out[i] = in[k]*(1-frac) + in[next]*frac
	}
	return out
}

// encodeSpeechWAV writes mono 16-bit PCM at speechRate.
func encodeSpeechWAV(samples []float64) []byte {
	dataLen := len(samples) * 2
	b := make([]byte, 44+dataLen)
	copy(b[0:4], "RIFF")
	binary.LittleEndian.PutUint32(b[4:8], uint32(36+dataLen))
	copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt ")
	binary.LittleEndian.PutUint32(b[16:20], 16)
	binary.LittleEndian.PutUint16(b[20:22], 1)
	binary.LittleEndian.PutUint16(b[22:24], 1)
	binary.LittleEndian.PutUint32(b[24:28], speechRate)
	binary.LittleEndian.PutUint32(b[28:32], speechRate*2)
	binary.LittleEndian.PutUint16(b[32:34], 2)
	binary.LittleEndian.PutUint16(b[34:36], 16)
	copy(b[36:40], "data")
	binary.LittleEndian.PutUint32(b[40:44], uint32(dataLen))
	for i, s := range samples {
		v := math.Round(math.Max(-1, math.Min(1, s)) * 32767)
		binary.LittleEndian.PutUint16(b[44+2*i:], uint16(int16(v)))
	}
	return b
}
