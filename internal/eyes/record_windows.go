//go:build windows

package eyes

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modOle32             = windows.NewLazySystemDLL("ole32.dll")
	procCoCreateInstance = modOle32.NewProc("CoCreateInstance")

	// CLSID_MMDeviceEnumerator: BCDE0395-E52F-467C-8E3D-C4579291692E
	clsidMMDeviceEnumerator = windows.GUID{
		Data1: 0xBCDE0395,
		Data2: 0xE52F,
		Data3: 0x467C,
		Data4: [8]byte{0x8E, 0x3D, 0xC4, 0x57, 0x92, 0x91, 0x69, 0x2E},
	}
	// IID_IMMDeviceEnumerator: A95664D2-9614-4F35-A746-DE8DB63617E6
	iidIMMDeviceEnumerator = windows.GUID{
		Data1: 0xA95664D2,
		Data2: 0x9614,
		Data3: 0x4F35,
		Data4: [8]byte{0xA7, 0x46, 0xDE, 0x8D, 0xB6, 0x36, 0x17, 0xE6},
	}
	// IID_IAudioClient: 1CB9AD4C-DBFA-4c32-B178-C2F568A703B2
	iidIAudioClient = windows.GUID{
		Data1: 0x1CB9AD4C,
		Data2: 0xDBFA,
		Data3: 0x4C32,
		Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2},
	}
	// IID_IAudioCaptureClient: C8ADBD64-E71E-48a0-A4DE-185C395CD317
	iidIAudioCaptureClient = windows.GUID{
		Data1: 0xC8ADBD64,
		Data2: 0xE71E,
		Data3: 0x48A0,
		Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17},
	}
)

const (
	clsctxAll                  uint32 = 0x17
	audclntSharemodeShared     uint32 = 0
	audclntStreamflagsLoopback uint32 = 0x00020000

	audclntBufferflagsSilent uint32 = 0x2
	audclntSBufferEmpty      uint32 = 0x08890001
)

type vtable *[64]uintptr

func comMethod(p unsafe.Pointer, index int) uintptr {
	vt := *(*vtable)(p)
	return vt[index]
}

func coCreateInstance(clsid *windows.GUID, unkOuter uintptr, clsContext uint32, iid *windows.GUID, ppv *unsafe.Pointer) error {
	r1, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(clsid)),
		unkOuter,
		uintptr(clsContext),
		uintptr(unsafe.Pointer(iid)),
		uintptr(unsafe.Pointer(ppv)),
	)
	if int32(r1) < 0 {
		return fmt.Errorf("CoCreateInstance: HRESULT 0x%08X", uint32(r1))
	}
	return nil
}

func comCall(p unsafe.Pointer, methodIndex int, args ...uintptr) error {
	proc := comMethod(p, methodIndex)
	callArgs := make([]uintptr, 0, len(args)+1)
	callArgs = append(callArgs, uintptr(p))
	callArgs = append(callArgs, args...)
	r1, _, _ := syscall.SyscallN(proc, callArgs...)
	if int32(r1) < 0 {
		return fmt.Errorf("COM method %d: HRESULT 0x%08X", methodIndex, uint32(r1))
	}
	return nil
}

func comRelease(p unsafe.Pointer) {
	if p == nil {
		return
	}
	proc := comMethod(p, 2) // IUnknown::Release
	syscall.SyscallN(proc, uintptr(p))
}

// WindowsAudioRecorder implements OS for Windows using WASAPI loopback capture.
type WindowsAudioRecorder struct {
	TargetBits int // 16 (default) or 32
}

// WindowsRecorder is an alias for WindowsAudioRecorder.
type WindowsRecorder = WindowsAudioRecorder

func (w WindowsAudioRecorder) Windows() ([]Window, error) {
	return nil, errors.New("eyes: window listing not supported on Windows")
}

func (w WindowsAudioRecorder) Capture(ctx context.Context, id int, path string) error {
	return errors.New("eyes: window capture not supported on Windows")
}

func (w WindowsAudioRecorder) Record(ctx context.Context, bundle string, seconds float64, path string) error {
	if seconds <= 0 {
		return errors.New("record 0-60 seconds")
	}
	targetBits := w.TargetBits
	if targetBits == 0 {
		targetBits = 16
	}
	return recordWASAPILoopback(ctx, seconds, path, targetBits)
}

func recordWASAPILoopback(ctx context.Context, seconds float64, path string, targetBits int) error {
	type result struct {
		err error
	}
	done := make(chan result, 1)

	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hr := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
		if hr != nil {
			var errno syscall.Errno
			if errors.As(hr, &errno) && errno != 0x80010106 { // RPC_E_CHANGED_MODE is safe to proceed
				done <- result{err: fmt.Errorf("CoInitializeEx: %w", hr)}
				return
			}
		} else {
			defer windows.CoUninitialize()
		}

		err := captureLoopback(ctx, seconds, path, targetBits)
		done <- result{err: err}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case res := <-done:
		return res.err
	}
}

func captureLoopback(ctx context.Context, seconds float64, path string, targetBits int) error {
	// 1. Create MMDeviceEnumerator
	var enumerator unsafe.Pointer
	err := coCreateInstance(&clsidMMDeviceEnumerator, 0, clsctxAll, &iidIMMDeviceEnumerator, &enumerator)
	if err != nil {
		return fmt.Errorf("device enumerator: %w", err)
	}
	defer comRelease(enumerator)

	// 2. GetDefaultAudioEndpoint (eRender = 0, eConsole = 0)
	var device unsafe.Pointer
	err = comCall(enumerator, 4, 0, 0, uintptr(unsafe.Pointer(&device)))
	if err != nil {
		// Retry with eMultimedia = 1
		err = comCall(enumerator, 4, 0, 1, uintptr(unsafe.Pointer(&device)))
		if err != nil {
			return fmt.Errorf("get default audio endpoint: %w", err)
		}
	}
	defer comRelease(device)

	// 3. Activate IAudioClient
	var audioClient unsafe.Pointer
	err = comCall(device, 3, uintptr(unsafe.Pointer(&iidIAudioClient)), uintptr(clsctxAll), 0, uintptr(unsafe.Pointer(&audioClient)))
	if err != nil {
		return fmt.Errorf("activate audio client: %w", err)
	}
	defer comRelease(audioClient)

	// 4. GetMixFormat
	var pFormat unsafe.Pointer
	err = comCall(audioClient, 8, uintptr(unsafe.Pointer(&pFormat)))
	if err != nil {
		return fmt.Errorf("get mix format: %w", err)
	}
	defer windows.CoTaskMemFree(pFormat)

	formatBytes := unsafe.Slice((*byte)(pFormat), 40)
	wFormatTag := binary.LittleEndian.Uint16(formatBytes[0:2])
	nChannels := int(binary.LittleEndian.Uint16(formatBytes[2:4]))
	nSamplesPerSec := int(binary.LittleEndian.Uint32(formatBytes[4:8]))
	nBlockAlign := int(binary.LittleEndian.Uint16(formatBytes[12:14]))
	wBitsPerSample := int(binary.LittleEndian.Uint16(formatBytes[14:16]))
	cbSize := int(binary.LittleEndian.Uint16(formatBytes[16:18]))

	isFloat := false
	if wFormatTag == 3 {
		isFloat = true
	} else if wFormatTag == 0xFFFE && cbSize >= 22 {
		subFormat := binary.LittleEndian.Uint32(formatBytes[24:28])
		if subFormat == 3 {
			isFloat = true
		}
	}

	// 5. Initialize IAudioClient in loopback shared mode (1s buffer = 10,000,000 hns)
	var initErr error
	if unsafe.Sizeof(uintptr(0)) == 8 {
		initErr = comCall(audioClient, 3,
			uintptr(audclntSharemodeShared),
			uintptr(audclntStreamflagsLoopback),
			uintptr(10000000),
			0,
			uintptr(pFormat),
			0,
		)
	} else {
		initErr = comCall(audioClient, 3,
			uintptr(audclntSharemodeShared),
			uintptr(audclntStreamflagsLoopback),
			uintptr(10000000&0xFFFFFFFF),
			uintptr(10000000>>32),
			0,
			0,
			uintptr(pFormat),
			0,
		)
	}
	if initErr != nil {
		return fmt.Errorf("initialize audio client: %w", initErr)
	}

	// 6. GetService IAudioCaptureClient
	var captureClient unsafe.Pointer
	err = comCall(audioClient, 14, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&captureClient)))
	if err != nil {
		return fmt.Errorf("get capture client: %w", err)
	}
	defer comRelease(captureClient)

	// 7. Start audio client
	err = comCall(audioClient, 10)
	if err != nil {
		return fmt.Errorf("start audio client: %w", err)
	}
	defer comCall(audioClient, 11) // Stop (index 11)

	// 8. Capture audio packets until target duration is met
	buf := NewAudioCaptureBuffer(nChannels, nSamplesPerSec, wBitsPerSample, isFloat, seconds)
	procGetBuffer := comMethod(captureClient, 3)
	procReleaseBuffer := comMethod(captureClient, 4)
	procGetNextPacketSize := comMethod(captureClient, 5)

	deadline := time.Now().Add(time.Duration(seconds*float64(time.Second)) + 3*time.Second)

	for buf.Frames() < buf.TargetFrames {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			break
		}

		var packetLength uint32
		r1, _, _ := syscall.SyscallN(procGetNextPacketSize, uintptr(captureClient), uintptr(unsafe.Pointer(&packetLength)))
		if int32(r1) < 0 {
			return fmt.Errorf("GetNextPacketSize: HRESULT 0x%08X", uint32(r1))
		}

		if packetLength == 0 {
			time.Sleep(5 * time.Millisecond)
			continue
		}

		var pData unsafe.Pointer
		var numFramesRead uint32
		var flags uint32
		var devPos, qpcPos uint64

		r1, _, _ = syscall.SyscallN(procGetBuffer,
			uintptr(captureClient),
			uintptr(unsafe.Pointer(&pData)),
			uintptr(unsafe.Pointer(&numFramesRead)),
			uintptr(unsafe.Pointer(&flags)),
			uintptr(unsafe.Pointer(&devPos)),
			uintptr(unsafe.Pointer(&qpcPos)),
		)
		if int32(r1) < 0 {
			if uint32(r1) == audclntSBufferEmpty {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			return fmt.Errorf("GetBuffer: HRESULT 0x%08X", uint32(r1))
		}

		if numFramesRead > 0 {
			isSilent := (flags & audclntBufferflagsSilent) != 0
			var pdataSlice []byte
			if !isSilent && pData != nil {
				pdataSlice = unsafe.Slice((*byte)(pData), int(numFramesRead)*nBlockAlign)
			}
			buf.AddPacket(pdataSlice, int(numFramesRead), isSilent)
		}

		syscall.SyscallN(procReleaseBuffer, uintptr(captureClient), uintptr(numFramesRead))
	}

	return buf.Save(path, targetBits)
}
