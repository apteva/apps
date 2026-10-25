package main

import (
	"bytes"
	"compress/gzip"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	mp3 "github.com/hajimehoshi/go-mp3"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// LAME is bundled as a WASI reactor: no host library, subprocess, network,
// filesystem access or runtime download. Each conversion has isolated memory.
//
//go:embed native/lame/encoder.wasm.gz
var recordingMP3WASM []byte

var recordingMP3Runtime struct {
	sync.Once
	runtime  wazero.Runtime
	compiled wazero.CompiledModule
	err      error
}

func recordingPlaybackFormat(r *recordingRow) string {
	if r.StorageStatus == "stored" && r.StorageFileID > 0 && r.StorageFormat != "" {
		return r.StorageFormat
	}
	return r.Format
}

// Only our own converted MP3 is known to use the decoder-compatible MPEG
// profile. Keep legacy/provider MP3 original-only (it can be MPEG-2.5).
func recordingSupportsVariants(r *recordingRow) bool {
	format := recordingPlaybackFormat(r)
	return format == "wav" || (format == "mp3" && r.Format == "wav" && r.StorageFormat == "mp3" && r.StorageStatus == "stored" && r.StorageFileID > 0)
}

func prepareRecordingStorage(ctx context.Context, path, sourceFormat, targetFormat string, channels int) (string, string, error) {
	if targetFormat == "" {
		targetFormat = "mp3"
	}
	if sourceFormat == targetFormat {
		return path, sourceFormat, nil
	}
	if sourceFormat == "wav" && targetFormat == "mp3" {
		out, err := encodeRecordingMP3(ctx, path, channels)
		return out, "mp3", err
	}
	if sourceFormat == "mp3" && targetFormat == "wav" {
		out, err := decodeRecordingMP3(ctx, path, channels)
		return out, "wav", err
	}
	return "", "", fmt.Errorf("unsupported recording conversion: %s to %s", sourceFormat, targetFormat)
}

func initRecordingMP3() {
	r := &recordingMP3Runtime
	r.runtime = wazero.NewRuntimeWithConfig(context.Background(), wazero.NewRuntimeConfig().WithMemoryLimitPages(512).WithCloseOnContextDone(true))
	if _, r.err = wasi_snapshot_preview1.Instantiate(context.Background(), r.runtime); r.err != nil {
		return
	}
	zip, err := gzip.NewReader(bytes.NewReader(recordingMP3WASM))
	if err != nil {
		r.err = err
		return
	}
	defer zip.Close()
	code, err := io.ReadAll(io.LimitReader(zip, 2<<20))
	if err != nil {
		r.err = err
		return
	}
	r.compiled, r.err = r.runtime.CompileModule(context.Background(), code)
}

func encodeRecordingMP3(ctx context.Context, path string, expectedChannels ...int) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := inspectPCM16WAV(source)
	if err != nil {
		return "", err
	}
	if len(expectedChannels) > 0 && info.Channels != expectedChannels[0] {
		return "", errors.New("recording channel metadata does not match source WAV; original retained")
	}
	stat, err := source.Stat()
	if err != nil {
		return "", err
	}
	if info.DataOffset+info.DataSize > stat.Size() {
		return "", io.ErrUnexpectedEOF
	}
	recordingMP3Runtime.Do(initRecordingMP3)
	if recordingMP3Runtime.err != nil {
		return "", recordingMP3Runtime.err
	}
	module, err := recordingMP3Runtime.runtime.InstantiateModule(ctx, recordingMP3Runtime.compiled, wazero.NewModuleConfig().WithName("").WithStartFunctions("_initialize"))
	if err != nil {
		return "", err
	}
	defer module.Close(context.Background())
	call := func(name string, args ...uint64) (int32, error) {
		result, err := module.ExportedFunction(name).Call(ctx, args...)
		if err != nil {
			return 0, err
		}
		n := int32(result[0])
		if n < 0 {
			return 0, fmt.Errorf("MP3 encoder %s failed (%d)", name, n)
		}
		return n, nil
	}
	if _, err = call("start", uint64(info.SampleRate), uint64(info.Channels)); err != nil {
		return "", err
	}
	in, err := call("input_ptr")
	if err != nil {
		return "", err
	}
	out, err := call("output_ptr")
	if err != nil {
		return "", err
	}
	dest, err := newRecordingTemporaryFile("telephony-storage-*.mp3")
	if err != nil {
		return "", err
	}
	good := false
	defer func() {
		dest.Close()
		if !good {
			os.Remove(dest.Name())
		}
	}()
	var written int64
	write := func(n int32) error {
		if n > 65536 {
			return errors.New("MP3 encoder output exceeds buffer")
		}
		written += int64(n)
		if written > recordingDownloadLimit(ctx) {
			return &recordingSizeLimitError{recordingDownloadLimit(ctx)}
		}
		data, ok := module.Memory().Read(uint32(out), uint32(n))
		if !ok {
			return errors.New("invalid MP3 output")
		}
		_, err := dest.Write(data)
		return err
	}
	buffer := make([]byte, 16384)
	for remaining := info.DataSize; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n := min(int64(len(buffer)), remaining)
		if _, err := io.ReadFull(source, buffer[:n]); err != nil {
			return "", err
		}
		if !module.Memory().Write(uint32(in), buffer[:n]) {
			return "", errors.New("invalid MP3 input")
		}
		produced, err := call("encode", uint64(n/int64(info.Channels*2)), uint64(info.Channels))
		if err != nil {
			return "", err
		}
		if err := write(produced); err != nil {
			return "", err
		}
		remaining -= n
	}
	n, err := call("finish")
	if err != nil {
		return "", err
	}
	if err := write(n); err != nil {
		return "", err
	}
	// Replace the reserved first frame with LAME's final duration/seek/delay tag.
	n, err = call("tag")
	if err != nil {
		return "", err
	}
	if n > 0 {
		if int64(n) > written {
			return "", errors.New("invalid MP3 seek tag")
		}
		if _, err := dest.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		written -= int64(n) // tag replaces bytes; it does not grow the file
		if err := write(n); err != nil {
			return "", err
		}
	}
	if err := dest.Sync(); err != nil {
		return "", err
	}
	if err := dest.Close(); err != nil {
		return "", err
	}
	good = true
	return dest.Name(), nil
}

// Decode incrementally to disk, retaining left/right identity. go-mp3 always
// produces stereo PCM; collapse its duplicated mono only for mono recordings.
// Expanded files have an independent 2 GiB ceiling, since
// the compressed Storage limit is not a limit on decoded PCM size.
const maxDecodedRecordingBytes int64 = 2 << 30

func decodeRecordingMP3(ctx context.Context, path string, channels int) (string, error) {
	if channels != 1 && channels != 2 {
		return "", errors.New("recording channel count unavailable")
	}
	source, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	decoder, err := mp3.NewDecoder(&localRecordingContextReader{ctx, source})
	if err != nil {
		return "", err
	}
	dest, err := newRecordingTemporaryFile("telephony-decoded-*.wav")
	if err != nil {
		return "", err
	}
	good := false
	defer func() {
		dest.Close()
		if !good {
			os.Remove(dest.Name())
		}
	}()
	if _, err := dest.Write(make([]byte, 44)); err != nil {
		return "", err
	}
	buffer := make([]byte, 64<<10)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, readErr := io.ReadFull(decoder, buffer)
		if n%4 != 0 {
			return "", errors.New("incomplete decoded MP3 frame")
		}
		if channels == 1 {
			for i := 0; i < n/4; i++ {
				copy(buffer[i*2:i*2+2], buffer[i*4:i*4+2])
			}
			n /= 2
		}
		size += int64(n)
		if size > maxDecodedRecordingBytes {
			return "", &recordingSizeLimitError{maxDecodedRecordingBytes}
		}
		if _, err := dest.Write(buffer[:n]); err != nil {
			return "", err
		}
		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			break
		}
		if readErr != nil {
			return "", readErr
		}
	}
	if size == 0 {
		return "", errors.New("empty MP3 recording")
	}
	if _, err := dest.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	if channels == 2 {
		err = writeDualLocalWAVHeader(dest, decoder.SampleRate(), size)
	} else {
		err = writeMonoWAVHeader(dest, decoder.SampleRate(), size)
	}
	if err != nil {
		return "", err
	}
	if err = dest.Close(); err != nil {
		return "", err
	}
	good = true
	return dest.Name(), nil
}
