package main

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

type AudioAnalysis struct {
	DurationSeconds float64 `json:"duration_seconds,omitempty"`
	PeakDB          float64 `json:"peak_db,omitempty"`
	RMSDB           float64 `json:"rms_db,omitempty"`
	SampleRate      int     `json:"sample_rate,omitempty"`
	Channels        int     `json:"channels,omitempty"`
	Codec           string  `json:"codec,omitempty"`
}

func analyzeGeneratedAudio(m generatedMedia) *AudioAnalysis {
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(m.MimeType)), "audio/") {
		return nil
	}
	b, err := mediaBytes(m)
	if err != nil || len(b) == 0 {
		return nil
	}
	return analyzeAudioBytes(b, m.Ext)
}

func analyzeAudioBytes(b []byte, ext string) *AudioAnalysis {
	if len(b) == 0 {
		return nil
	}
	if strings.TrimSpace(ext) == "" {
		ext = "bin"
	}
	f, err := os.CreateTemp("", "media-studio-audio-*."+strings.TrimPrefix(ext, "."))
	if err != nil {
		return nil
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return nil
	}
	if err := f.Close(); err != nil {
		return nil
	}
	a := probeAudioStream(path)
	if a == nil {
		a = &AudioAnalysis{}
	}
	if a.DurationSeconds <= 0 {
		a.DurationSeconds = probeDurationPath(path)
	}
	peak, rms := probeAudioVolume(path)
	if !math.IsNaN(peak) {
		a.PeakDB = peak
	}
	if !math.IsNaN(rms) {
		a.RMSDB = rms
	}
	if a.DurationSeconds <= 0 && a.PeakDB == 0 && a.RMSDB == 0 && a.SampleRate == 0 && a.Channels == 0 && a.Codec == "" {
		return nil
	}
	return a
}

func probeAudioStream(path string) *AudioAnalysis {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-select_streams", "a:0",
		"-show_entries", "stream=codec_name,sample_rate,channels:format=duration",
		"-of", "json",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var body struct {
		Streams []struct {
			CodecName  string `json:"codec_name"`
			SampleRate string `json:"sample_rate"`
			Channels   int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil
	}
	a := &AudioAnalysis{}
	if len(body.Streams) > 0 {
		s := body.Streams[0]
		a.Codec = s.CodecName
		a.SampleRate, _ = strconv.Atoi(strings.TrimSpace(s.SampleRate))
		a.Channels = s.Channels
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(body.Format.Duration), 64); err == nil && d > 0 {
		a.DurationSeconds = d
	}
	return a
}

func probeDurationPath(path string) float64 {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format=duration",
		"-of", "default=noprint_wrappers=1:nokey=1",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return v
}

func probeAudioVolume(path string) (float64, float64) {
	cmd := exec.Command("ffmpeg", "-hide_banner", "-nostats", "-i", path, "-af", "volumedetect", "-f", "null", "-")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return math.NaN(), math.NaN()
	}
	return parseVolumeDetect(stderr.String())
}

func parseVolumeDetect(s string) (float64, float64) {
	peak := math.NaN()
	rms := math.NaN()
	meanRe := regexp.MustCompile(`mean_volume:\s*([-0-9.]+)\s*dB`)
	maxRe := regexp.MustCompile(`max_volume:\s*([-0-9.]+)\s*dB`)
	if m := meanRe.FindStringSubmatch(s); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			rms = v
		}
	}
	if m := maxRe.FindStringSubmatch(s); len(m) == 2 {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil {
			peak = v
		}
	}
	return peak, rms
}
