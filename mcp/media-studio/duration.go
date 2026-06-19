package main

import (
	"encoding/base64"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
)

func estimatedDurationSeconds(kind string, args map[string]any) float64 {
	if opts, _ := args["options"].(map[string]any); opts != nil {
		if v := floatArg(opts, "estimated_duration_seconds", 0); v > 0 {
			return v
		}
	}
	switch kind {
	case KindAudioTTS, KindAvatar:
		return estimateSpeechSecondsWithArgs(strArg(args, "prompt", ""), args)
	case KindAudioSFX, KindMusic:
		return durationArgSeconds(args)
	case KindVideo:
		if d := durationArgSeconds(args); d > 0 {
			return d
		}
		return 5
	default:
		return 0
	}
}

func estimateSpeechSecondsWithArgs(script string, args map[string]any) float64 {
	seconds := estimateSpeechSeconds(script)
	if seconds <= 0 {
		return 0
	}
	speed := 1.0
	if opts, _ := args["options"].(map[string]any); opts != nil {
		if v := floatArg(opts, "speed", 0); v > 0 {
			speed = v
		}
		if settings, _ := opts["voice_settings"].(map[string]any); settings != nil {
			if v := floatArg(settings, "speed", 0); v > 0 {
				speed = v
			}
		}
	}
	if speed <= 0 {
		speed = 1
	}
	return math.Round((seconds/speed)*10) / 10
}

func estimateSpeechSeconds(script string) float64 {
	script = strings.TrimSpace(script)
	if script == "" {
		return 0
	}
	words := 0
	inWord := false
	pause := 0.0
	for _, r := range script {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\'' {
			if !inWord {
				words++
				inWord = true
			}
			continue
		}
		inWord = false
		switch r {
		case '.', ',', ';', ':':
			pause += 0.18
		case '!', '?':
			pause += 0.3
		case '\n':
			pause += 0.45
		}
	}
	if words == 0 {
		return 0
	}
	seconds := float64(words)/155.0*60.0 + pause
	if seconds < 1.5 {
		seconds = 1.5
	}
	return math.Round(seconds*10) / 10
}

func durationArgSeconds(args map[string]any) float64 {
	if d := intArg(args, "duration", 0); d > 0 {
		return float64(d)
	}
	raw := strings.TrimSpace(strArg(args, "duration", ""))
	if raw == "" {
		return 0
	}
	raw = strings.TrimSuffix(raw, "s")
	if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 {
		return v
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d.Seconds()
	}
	return 0
}

func mediaActualDurationSeconds(m generatedMedia) float64 {
	if m.DurationMs > 0 {
		return float64(m.DurationMs) / 1000
	}
	if !durationMime(m.MimeType) {
		return 0
	}
	b, err := mediaBytes(m)
	if err != nil {
		return 0
	}
	return probeDurationSeconds(b, m.Ext)
}

func base64ActualDurationSeconds(base64Bytes, mime, ext string) float64 {
	if base64Bytes == "" || !durationMime(mime) {
		return 0
	}
	b, err := base64.StdEncoding.DecodeString(base64Bytes)
	if err != nil {
		return 0
	}
	return probeDurationSeconds(b, ext)
}

func durationMime(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	return strings.HasPrefix(mime, "audio/") || strings.HasPrefix(mime, "video/")
}

func probeDurationSeconds(bytes []byte, ext string) float64 {
	if len(bytes) == 0 {
		return 0
	}
	if strings.TrimSpace(ext) == "" {
		ext = "bin"
	}
	f, err := os.CreateTemp("", "media-studio-probe-*."+strings.TrimPrefix(ext, "."))
	if err != nil {
		return 0
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err := f.Write(bytes); err != nil {
		f.Close()
		return 0
	}
	if err := f.Close(); err != nil {
		return 0
	}
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
