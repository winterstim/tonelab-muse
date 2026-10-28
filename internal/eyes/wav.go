package eyes

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// Float32ToPCM16 converts interleaved 32-bit IEEE float audio samples to 16-bit signed integer PCM.
func Float32ToPCM16(src []byte) []byte {
	samples := len(src) / 4
	dst := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		bits := binary.LittleEndian.Uint32(src[i*4 : (i+1)*4])
		f := math.Float32frombits(bits)
		if f > 1.0 {
			f = 1.0
		} else if f < -1.0 {
			f = -1.0
		}
		var val int16
		if f >= 0 {
			val = int16(f * 32767)
		} else {
			val = int16(f * 32768)
		}
		binary.LittleEndian.PutUint16(dst[i*2:(i+1)*2], uint16(val))
	}
	return dst
}

// PCM16ToFloat32 converts interleaved 16-bit signed integer PCM samples to 32-bit IEEE float.
func PCM16ToFloat32(src []byte) []byte {
	samples := len(src) / 2
	dst := make([]byte, samples*4)
	for i := 0; i < samples; i++ {
		raw := int16(binary.LittleEndian.Uint16(src[i*2 : (i+1)*2]))
		f := float32(raw) / 32768.0
		binary.LittleEndian.PutUint32(dst[i*4:(i+1)*4], math.Float32bits(f))
	}
	return dst
}

// EncodeWAV creates a valid RIFF WAVE byte slice from raw PCM or float audio data.
func EncodeWAV(data []byte, rate, channels, bits int, isFloat bool) ([]byte, error) {
	if channels <= 0 || rate <= 0 || (bits != 16 && bits != 24 && bits != 32) {
		return nil, fmt.Errorf("invalid audio format: %d channels, %d Hz, %d bits", channels, rate, bits)
	}
	blockAlign := channels * (bits / 8)
	byteRate := rate * blockAlign
	formatTag := uint16(1) // PCM
	if isFloat {
		if bits != 32 {
			return nil, errors.New("float WAV must be 32-bit")
		}
		formatTag = 3 // IEEE Float
	}

	dataSize := uint32(len(data))
	fileSize := 36 + dataSize

	buf := make([]byte, 44+dataSize)
	// RIFF Header
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], fileSize)
	copy(buf[8:12], "WAVE")

	// fmt subchunk
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // Subchunk1Size for PCM / basic float
	binary.LittleEndian.PutUint16(buf[20:22], formatTag)
	binary.LittleEndian.PutUint16(buf[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(buf[24:28], uint32(rate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(buf[34:36], uint16(bits))

	// data subchunk
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataSize)
	copy(buf[44:], data)

	return buf, nil
}

// SaveWAV writes encoded WAV audio data to path, creating directories as needed.
func SaveWAV(path string, data []byte, rate, channels, bits int, isFloat bool) error {
	encoded, err := EncodeWAV(data, rate, channels, bits, isFloat)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o644)
}

// AudioCaptureBuffer accumulates audio frames from capture packets and enforces target duration.
type AudioCaptureBuffer struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
	IsFloat       bool
	BlockAlign    int
	TargetFrames  int

	data        []byte
	framesCount int
}

// NewAudioCaptureBuffer initializes an AudioCaptureBuffer for a target duration in seconds.
func NewAudioCaptureBuffer(channels, sampleRate, bitsPerSample int, isFloat bool, targetSeconds float64) *AudioCaptureBuffer {
	blockAlign := channels * (bitsPerSample / 8)
	targetFrames := int(targetSeconds * float64(sampleRate))
	return &AudioCaptureBuffer{
		Channels:      channels,
		SampleRate:    sampleRate,
		BitsPerSample: bitsPerSample,
		IsFloat:       isFloat,
		BlockAlign:    blockAlign,
		TargetFrames:  targetFrames,
		data:          make([]byte, 0, targetFrames*blockAlign),
	}
}

// AddPacket appends up to numFrames of audio from packetData, or zeros if silent.
// Returns the number of frames accepted, and whether the target frame count is reached.
func (b *AudioCaptureBuffer) AddPacket(packetData []byte, numFrames int, silent bool) (accepted int, full bool) {
	if b.framesCount >= b.TargetFrames {
		return 0, true
	}
	toTake := numFrames
	if b.framesCount+toTake > b.TargetFrames {
		toTake = b.TargetFrames - b.framesCount
	}
	bytesToTake := toTake * b.BlockAlign
	if silent || len(packetData) == 0 {
		b.data = append(b.data, make([]byte, bytesToTake)...)
	} else {
		if bytesToTake > len(packetData) {
			bytesToTake = len(packetData)
			toTake = bytesToTake / b.BlockAlign
		}
		b.data = append(b.data, packetData[:bytesToTake]...)
	}
	b.framesCount += toTake
	return toTake, b.framesCount >= b.TargetFrames
}

// Frames returns the number of frames accumulated so far.
func (b *AudioCaptureBuffer) Frames() int {
	return b.framesCount
}

// Bytes returns the raw accumulated audio buffer bytes.
func (b *AudioCaptureBuffer) Bytes() []byte {
	return b.data
}

// Save writes the accumulated buffer to path as a standard 16-bit or 32-bit WAV file.
// If targetBits is 16 and the source is 32-bit float, it converts the stream to 16-bit PCM.
func (b *AudioCaptureBuffer) Save(path string, targetBits int) error {
	if len(b.data) == 0 {
		return errors.New("captured no audio: is the app playing, and is its output device working?")
	}
	data := b.data
	bits := b.BitsPerSample
	isFloat := b.IsFloat
	if (targetBits == 16 || targetBits == 0) && b.BitsPerSample == 32 && b.IsFloat {
		data = Float32ToPCM16(data)
		bits = 16
		isFloat = false
	}
	return SaveWAV(path, data, b.SampleRate, b.Channels, bits, isFloat)
}
