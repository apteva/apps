//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"errors"
	"fmt"
	"github.com/ebitengine/purego"
	"runtime"
	"sync"
)

// Fixed system-library names only: no user paths or per-packet loading. Keep
// the library open for the process lifetime; each call owns/destroys its state.
type rtcOpusLibrary struct {
	hasLBRR        func([]byte, int32) int32
	create         func(int32, int32, int32, *int32) uintptr
	destroy        func(uintptr)
	encode         func(uintptr, []float32, int32, []byte, int32) int32
	decode         func(uintptr, []byte, int32, []int16, int32, int32) int32
	decoderCreate  func(int32, int32, *int32) uintptr
	decoderDestroy func(uintptr)
	set            func(uintptr, int32, int32) int32
	get            func(uintptr, int32, *int32) int32
}

var rtcNativeOpus struct {
	sync.Once
	lib *rtcOpusLibrary
	err error
}

func loadRTCOpusLibrary() (*rtcOpusLibrary, error) {
	rtcNativeOpus.Do(func() {
		names := []string{"libopus.so.0", "libopus.so"}
		if runtime.GOOS == "darwin" {
			names = []string{"/opt/homebrew/lib/libopus.0.dylib", "/usr/local/lib/libopus.0.dylib", "libopus.0.dylib"}
		}
		var handle uintptr
		for _, name := range names {
			h, err := purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_LOCAL)
			if err == nil {
				handle = h
				break
			}
		}
		if handle == 0 {
			rtcNativeOpus.err = errors.New("system libopus unavailable")
			return
		}
		defer func() {
			if v := recover(); v != nil {
				_ = purego.Dlclose(handle)
				rtcNativeOpus.err = fmt.Errorf("invalid system libopus: %v", v)
			}
		}()
		l := &rtcOpusLibrary{}
		purego.RegisterLibFunc(&l.create, handle, "opus_encoder_create")
		purego.RegisterLibFunc(&l.destroy, handle, "opus_encoder_destroy")
		purego.RegisterLibFunc(&l.encode, handle, "opus_encode_float")
		purego.RegisterLibFunc(&l.decode, handle, "opus_decode")
		purego.RegisterLibFunc(&l.decoderCreate, handle, "opus_decoder_create")
		purego.RegisterLibFunc(&l.decoderDestroy, handle, "opus_decoder_destroy")
		if sym, err := purego.Dlsym(handle, "opus_packet_has_lbrr"); err == nil {
			purego.RegisterFunc(&l.hasLBRR, sym)
		}
		if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" {
			// Darwin ARM64 C varargs are on the stack, after the two fixed
			// arguments. Six padding register arguments put ctl's value there.
			var set func(uintptr, int32, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, int32) int32
			var get func(uintptr, int32, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, *int32) int32
			purego.RegisterLibFunc(&set, handle, "opus_encoder_ctl")
			purego.RegisterLibFunc(&get, handle, "opus_encoder_ctl")
			l.set = func(e uintptr, req, val int32) int32 { return set(e, req, 0, 0, 0, 0, 0, 0, val) }
			l.get = func(e uintptr, req int32, val *int32) int32 { return get(e, req, 0, 0, 0, 0, 0, 0, val) }
		} else {
			purego.RegisterLibFunc(&l.set, handle, "opus_encoder_ctl")
			purego.RegisterLibFunc(&l.get, handle, "opus_encoder_ctl")
		}
		rtcNativeOpus.lib = l
	})
	return rtcNativeOpus.lib, rtcNativeOpus.err
}

type rtcNativeEncoder struct {
	lib   *rtcOpusLibrary
	state uintptr
}

func newRTCNativeEncoder(bitrate int) (rtcOpusEncoder, error) {
	l, err := loadRTCOpusLibrary()
	if err != nil {
		return nil, err
	}
	var code int32
	e := &rtcNativeEncoder{lib: l, state: l.create(48000, 1, 2048, &code)}
	if e.state == 0 || code != 0 {
		return nil, fmt.Errorf("Opus create: %d", code)
	}
	// Voice tuning and a 24 kHz bandwidth match the existing carrier PCM.
	for _, ctl := range [][2]int32{{4012, 1}, {4024, 3001}, {4004, 1104}, {4016, 0}} {
		if l.set(e.state, ctl[0], ctl[1]) != 0 {
			e.Close()
			return nil, errors.New("Opus voice control failed")
		}
	}
	if err := e.configure(bitrate, 10); err != nil {
		e.Close()
		return nil, err
	}
	// Read back FEC and bitrate so an ABI mismatch cannot silently enable
	// the wrong parameters. Fall back before exposing this codec to media.
	for _, ctl := range [][2]int32{{4013, 1}, {4003, int32(bitrate)}} {
		var value int32
		if l.get(e.state, ctl[0], &value) != 0 || value != ctl[1] {
			e.Close()
			return nil, errors.New("Opus control verification failed")
		}
	}
	return e, nil
}
func (e *rtcNativeEncoder) EncodeFloat32(pcm []float32, out []byte) (int, error) {
	if e.state == 0 || len(pcm) != 960 || len(out) == 0 {
		return 0, errors.New("invalid Opus frame")
	}
	n := e.lib.encode(e.state, pcm, 960, out, int32(len(out)))
	if n < 0 {
		return 0, fmt.Errorf("Opus encode: %d", n)
	}
	return int(n), nil
}
func (e *rtcNativeEncoder) configure(bitrate, loss int) error {
	if bitrate < 16000 || bitrate > 32000 || loss < 0 || loss > 30 {
		return errors.New("invalid Opus adaptation")
	}
	for _, ctl := range [][2]int32{{4002, int32(bitrate)}, {4014, int32(loss)}} {
		if e.lib.set(e.state, ctl[0], ctl[1]) != 0 {
			return errors.New("Opus adaptation failed")
		}
	}
	return nil
}
func (e *rtcNativeEncoder) capability() string { return "libopus_fec" }
func (e *rtcNativeEncoder) Close() {
	if e.state != 0 {
		e.lib.destroy(e.state)
		e.state = 0
	}
}

type rtcNativeDecoder struct {
	lib   *rtcOpusLibrary
	state uintptr
}

func newRTCNativeDecoder() (rtcOpusDecoder, error) {
	l, err := loadRTCOpusLibrary()
	if err != nil {
		return nil, err
	}
	var code int32
	d := &rtcNativeDecoder{lib: l, state: l.decoderCreate(24000, 1, &code)}
	if d.state == 0 || code != 0 {
		return nil, errors.New("Opus decoder initialization failed")
	}
	return d, nil
}
func (d *rtcNativeDecoder) decode(p []byte, out []int16) (int, error) { return d.run(p, out, false) }
func (d *rtcNativeDecoder) recover(p []byte, out []int16, fec bool) (int, error) {
	if !fec {
		p = nil
	}
	return d.run(p, out, fec)
}
func (d *rtcNativeDecoder) run(p []byte, out []int16, fec bool) (int, error) {
	if d.state == 0 || len(out) == 0 {
		return 0, errors.New("invalid Opus decoder state")
	}
	flag := int32(0)
	if fec {
		flag = 1
	}
	n := d.lib.decode(d.state, p, int32(len(p)), out, int32(len(out)), flag)
	if n < 0 {
		return 0, fmt.Errorf("Opus decode: %d", n)
	}
	return int(n), nil
}
func (d *rtcNativeDecoder) Close() {
	if d.state != 0 {
		d.lib.decoderDestroy(d.state)
		d.state = 0
	}
}
