package main

import (
	"math"
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
	return 0
}
