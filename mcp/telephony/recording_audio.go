package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

const (
	recordingVariantMix      = "mix"
	recordingVariantCaller   = "caller"
	recordingVariantAgent    = "agent"
	recordingVariantOriginal = "original"
)

type wavPCMInfo struct {
	Channels   int
	SampleRate int
	DataOffset int64
	DataSize   int64
}

func normalizedRecordingVariant(value string) (string, error) {
	if value == "" {
		return recordingVariantMix, nil
	}
	switch value {
	case recordingVariantMix, recordingVariantCaller, recordingVariantAgent, recordingVariantOriginal:
		return value, nil
	default:
		return "", errors.New("variant must be mix, caller, agent, or original")
	}
}

func inspectPCM16WAV(file *os.File) (wavPCMInfo, error) {
	var info wavPCMInfo
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return info, err
	}
	header := make([]byte, 12)
	if _, err := io.ReadFull(file, header); err != nil {
		return info, err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return info, errors.New("recording is not a RIFF/WAVE file")
	}
	foundFormat := false
	for {
		chunk := make([]byte, 8)
		if _, err := io.ReadFull(file, chunk); err != nil {
			return info, errors.New("recording has no PCM audio data")
		}
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		payloadOffset, _ := file.Seek(0, io.SeekCurrent)
		switch string(chunk[:4]) {
		case "fmt ":
			if size < 16 || size > 4096 {
				return info, errors.New("unsupported WAVE format chunk")
			}
			format := make([]byte, size)
			if _, err := io.ReadFull(file, format); err != nil {
				return info, err
			}
			if binary.LittleEndian.Uint16(format[0:2]) != 1 || binary.LittleEndian.Uint16(format[14:16]) != 16 {
				return info, errors.New("recording must be uncompressed 16-bit PCM")
			}
			info.Channels = int(binary.LittleEndian.Uint16(format[2:4]))
			info.SampleRate = int(binary.LittleEndian.Uint32(format[4:8]))
			if info.Channels < 1 || info.Channels > 2 || info.SampleRate <= 0 {
				return info, errors.New("recording must contain one or two PCM channels")
			}
			foundFormat = true
		case "data":
			if !foundFormat {
				return info, errors.New("WAVE data precedes its format metadata")
			}
			info.DataOffset = payloadOffset
			info.DataSize = size
			frameBytes := int64(info.Channels * 2)
			info.DataSize -= info.DataSize % frameBytes
			if info.DataSize <= 0 {
				return info, errors.New("recording contains no PCM samples")
			}
			return info, nil
		default:
			if _, err := file.Seek(size, io.SeekCurrent); err != nil {
				return info, err
			}
		}
		if string(chunk[:4]) == "fmt " {
			// The format payload was consumed above.
		} else if _, err := file.Seek(payloadOffset+size, io.SeekStart); err != nil {
			return info, err
		}
		if size%2 != 0 {
			if _, err := file.Seek(1, io.SeekCurrent); err != nil {
				return info, err
			}
		}
	}
}

func forEachWAVFrame(file *os.File, info wavPCMInfo, visit func([]int16) error, contexts ...context.Context) error {
	if _, err := file.Seek(info.DataOffset, io.SeekStart); err != nil {
		return err
	}
	frameBytes := info.Channels * 2
	bufferSize := (64 * 1024 / frameBytes) * frameBytes
	buffer := make([]byte, bufferSize)
	remaining := info.DataSize
	samples := make([]int16, info.Channels)
	for remaining > 0 {
		if len(contexts) > 0 && contexts[0].Err() != nil {
			return contexts[0].Err()
		}
		n := int64(len(buffer))
		if remaining < n {
			n = remaining
		}
		if _, err := io.ReadFull(file, buffer[:n]); err != nil {
			return err
		}
		for offset := 0; offset < int(n); offset += frameBytes {
			for channel := range samples {
				samples[channel] = int16(binary.LittleEndian.Uint16(buffer[offset+channel*2:]))
			}
			if err := visit(samples); err != nil {
				return err
			}
		}
		remaining -= n
	}
	return nil
}

func recordingChannelGains(file *os.File, info wavPCMInfo, contexts ...context.Context) ([]float64, error) {
	sums := make([]float64, info.Channels)
	counts := make([]int64, info.Channels)
	err := forEachWAVFrame(file, info, func(samples []int16) error {
		for channel, sample := range samples {
			value := float64(sample)
			if math.Abs(value) < 256 {
				continue
			}
			sums[channel] += value * value
			counts[channel]++
		}
		return nil
	}, contexts...)
	if err != nil {
		return nil, err
	}
	gains := make([]float64, info.Channels)
	for channel := range gains {
		gains[channel] = 1
		if counts[channel] == 0 {
			continue
		}
		rms := math.Sqrt(sums[channel] / float64(counts[channel]))
		gains[channel] = math.Max(0.5, math.Min(12, 6000/rms))
	}
	return gains, nil
}

func writeMonoWAVHeader(out io.Writer, sampleRate int, dataSize int64) error {
	if dataSize < 0 || dataSize > math.MaxUint32-36 {
		return errors.New("recording is too large for the WAVE format")
	}
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(dataSize+36))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16)
	binary.LittleEndian.PutUint16(header[20:22], 1)
	binary.LittleEndian.PutUint16(header[22:24], 1)
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(header[32:34], 2)
	binary.LittleEndian.PutUint16(header[34:36], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))
	_, err := out.Write(header)
	return err
}

func compressRecordingSample(value float64) int16 {
	const threshold = 28000.0
	abs := math.Abs(value)
	if abs > threshold {
		abs = threshold + (abs-threshold)/4
		if abs > 32767 {
			abs = 32767
		}
		value = math.Copysign(abs, value)
	}
	return int16(math.Round(value))
}

func buildRecordingVariant(sourcePath, variant string, contexts ...context.Context) (string, error) {
	variant, err := normalizedRecordingVariant(variant)
	if err != nil {
		return "", err
	}
	if variant == recordingVariantOriginal {
		return sourcePath, nil
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return "", err
	}
	defer source.Close()
	info, err := inspectPCM16WAV(source)
	if err != nil {
		return "", err
	}
	if (variant == recordingVariantCaller || variant == recordingVariantAgent) && info.Channels < 2 {
		return "", fmt.Errorf("%s channel is unavailable in a mono recording", variant)
	}
	gains, err := recordingChannelGains(source, info, contexts...)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp("", "apteva-telephony-playback-*.wav")
	if err != nil {
		return "", err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		_ = tmp.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	frames := info.DataSize / int64(info.Channels*2)
	if err := writeMonoWAVHeader(tmp, info.SampleRate, frames*2); err != nil {
		return "", err
	}
	buffered := bufio.NewWriterSize(tmp, 64*1024)
	encoded := make([]byte, 2)
	err = forEachWAVFrame(source, info, func(samples []int16) error {
		var value float64
		switch variant {
		case recordingVariantCaller:
			value = float64(samples[0]) * gains[0]
		case recordingVariantAgent:
			value = float64(samples[1]) * gains[1]
		default:
			for channel, sample := range samples {
				value += float64(sample) * gains[channel]
			}
		}
		binary.LittleEndian.PutUint16(encoded, uint16(compressRecordingSample(value)))
		_, err := buffered.Write(encoded)
		return err
	}, contexts...)
	if err != nil {
		return "", err
	}
	if err := buffered.Flush(); err != nil {
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}
