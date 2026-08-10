package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Audio (TTS + SFX) is synchronous. Provider integrations buffer upstream
// binary responses into {_binary, base64, mimeType}.

func buildAudioTTSArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "elevenlabs":
		return buildElevenLabsTTSArgs(args)
	case "fish-audio":
		return buildFishAudioTTSArgs(args)
	case "deepgram":
		return buildDeepgramTTSArgs(args)
	case "cartesia":
		return buildCartesiaTTSArgs(args)
	case "minimax-audio":
		return buildMiniMaxTTSArgs(args)
	}
	return nil, fmt.Errorf("unsupported audio TTS provider slug: %q", providerSlug)
}

func audioToolForSlug(slug, capability string) string {
	if capability == "audio.tts" {
		switch slug {
		case "deepgram":
			return "speak"
		case "cartesia":
			return "create_speech"
		case "minimax-audio":
			return "text_to_speech"
		}
	}
	return ""
}

func buildCartesiaTTSArgs(args map[string]any) (map[string]any, error) {
	voiceID := firstNonEmpty(strArg(args, "voice", ""), strArg(args, "voice_id", ""))
	if voiceID == "" {
		return nil, fmt.Errorf("voice required for Cartesia TTS")
	}
	out := map[string]any{
		"model_id":   strArg(args, "model", "sonic-3.5"),
		"transcript": strArg(args, "prompt", ""),
		"voice":      map[string]any{"mode": "id", "id": voiceID},
		"output_format": map[string]any{
			"container":   "mp3",
			"sample_rate": 44100,
			"bit_rate":    128000,
		},
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{"language", "pronunciation_dict_id", "generation_config"} {
			if value, exists := opts[key]; exists {
				out[key] = value
			}
		}
		if value, exists := opts["output_format"]; exists {
			switch format := value.(type) {
			case map[string]any:
				out["output_format"] = format
			default:
				out["output_format"] = cartesiaOutputFormat(fmt.Sprint(format))
			}
		}
	}
	return out, nil
}

func cartesiaOutputFormat(format string) map[string]any {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return map[string]any{"container": "wav", "encoding": "pcm_s16le", "sample_rate": 44100}
	case "raw", "pcm":
		return map[string]any{"container": "raw", "encoding": "pcm_s16le", "sample_rate": 44100}
	default:
		return map[string]any{"container": "mp3", "sample_rate": 44100, "bit_rate": 128000}
	}
}

func buildMiniMaxTTSArgs(args map[string]any) (map[string]any, error) {
	voiceID := firstNonEmpty(strArg(args, "voice", ""), strArg(args, "voice_id", ""))
	if voiceID == "" {
		return nil, fmt.Errorf("voice required for MiniMax TTS")
	}
	out := map[string]any{
		"model":          strArg(args, "model", "speech-2.8-hd"),
		"text":           strArg(args, "prompt", ""),
		"stream":         false,
		"output_format":  "hex",
		"language_boost": "auto",
		"voice_setting":  map[string]any{"voice_id": voiceID},
		"audio_setting":  map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1},
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{
			"language_boost", "pronunciation_dict", "timbre_weights", "voice_modify",
		} {
			if value, exists := opts[key]; exists {
				out[key] = value
			}
		}
		if value, exists := opts["voice_setting"]; exists {
			if settings, ok := value.(map[string]any); ok {
				merged := mergeStringAnyMaps(map[string]any{}, settings)
				merged["voice_id"] = voiceID
				out["voice_setting"] = merged
			}
		}
		if value, exists := opts["audio_setting"]; exists {
			if settings, ok := value.(map[string]any); ok {
				out["audio_setting"] = mergeStringAnyMaps(
					map[string]any{"sample_rate": 32000, "bitrate": 128000, "format": "mp3", "channel": 1},
					settings,
				)
			}
		}
		if value, exists := opts["output_format"]; exists {
			switch format := value.(type) {
			case map[string]any:
				out["audio_setting"] = mergeStringAnyMaps(out["audio_setting"].(map[string]any), format)
			default:
				settings := out["audio_setting"].(map[string]any)
				settings["format"] = normalizeMiniMaxAudioFormat(fmt.Sprint(format))
			}
		}
	}
	return out, nil
}

func mergeStringAnyMaps(base, overrides map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(overrides))
	for key, value := range base {
		out[key] = value
	}
	for key, value := range overrides {
		out[key] = value
	}
	return out
}

func normalizeMiniMaxAudioFormat(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "pcm", "flac", "wav":
		return strings.ToLower(strings.TrimSpace(format))
	default:
		return "mp3"
	}
}

func buildDeepgramTTSArgs(args map[string]any) (map[string]any, error) {
	text := strArg(args, "prompt", "")
	if count := utf8.RuneCountInString(text); count > 2000 {
		return nil, fmt.Errorf("%d characters exceeds Deepgram's 2000-character TTS limit", count)
	}
	out := map[string]any{
		"text":  text,
		"model": strArg(args, "model", "aura-2-thalia-en"),
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{"encoding", "container", "sample_rate", "bit_rate"} {
			if value, exists := opts[key]; exists {
				out[key] = value
			}
		}
		if value, exists := opts["output_format"]; exists {
			applyDeepgramOutputFormat(out, fmt.Sprint(value))
		}
	}
	return out, nil
}

func applyDeepgramOutputFormat(out map[string]any, format string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		out["encoding"] = "linear16"
		out["container"] = "wav"
	case "pcm", "linear16":
		out["encoding"] = "linear16"
		out["container"] = "none"
	case "opus":
		out["encoding"] = "opus"
		out["container"] = "ogg"
	case "flac", "aac", "mp3":
		out["encoding"] = strings.ToLower(strings.TrimSpace(format))
	}
}

func buildFishAudioTTSArgs(args map[string]any) (map[string]any, error) {
	out := map[string]any{
		"model": strArg(args, "model", "s2.1-pro"),
		"text":  strArg(args, "prompt", ""),
	}
	if voiceID := firstNonEmpty(strArg(args, "voice", ""), strArg(args, "voice_id", "")); voiceID != "" {
		out["reference_id"] = voiceID
	}
	if opts, ok := args["options"].(map[string]any); ok {
		for _, key := range []string{
			"reference_id", "prosody", "temperature", "top_p", "chunk_length",
			"normalize", "format", "sample_rate", "mp3_bitrate", "opus_bitrate",
			"latency", "max_new_tokens", "repetition_penalty", "min_chunk_length",
			"condition_on_previous_chunks", "early_stop_threshold",
		} {
			if value, exists := opts[key]; exists {
				out[key] = value
			}
		}
		// output_format is Media Studio's provider-neutral spelling.
		if value, exists := opts["output_format"]; exists {
			out["format"] = normalizeFishAudioFormat(fmt.Sprint(value))
		}
	}
	if format, ok := out["format"].(string); ok {
		out["format"] = normalizeFishAudioFormat(format)
	}
	return out, nil
}

func normalizeFishAudioFormat(format string) string {
	format = strings.ToLower(strings.TrimSpace(format))
	switch {
	case strings.HasPrefix(format, "mp3"):
		return "mp3"
	case strings.HasPrefix(format, "pcm"):
		return "pcm"
	case strings.HasPrefix(format, "wav"):
		return "wav"
	case strings.HasPrefix(format, "opus"):
		return "opus"
	default:
		return format
	}
}

func buildAudioSFXArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "elevenlabs":
		return buildElevenLabsSFXArgs(args)
	}
	return nil, fmt.Errorf("unsupported audio SFX provider slug: %q", providerSlug)
}

func buildElevenLabsTTSArgs(args map[string]any) (map[string]any, error) {
	voiceID := strArg(args, "voice", "")
	if voiceID == "" {
		voiceID = strArg(args, "voice_id", "")
	}
	if voiceID == "" {
		return nil, fmt.Errorf("voice required for ElevenLabs TTS")
	}
	out := map[string]any{
		"voice_id": voiceID,
		"text":     strArg(args, "prompt", ""),
	}
	if model := strArg(args, "model", ""); model != "" {
		out["model_id"] = model
	}
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"model_id", "language_code", "voice_settings",
			"pronunciation_dictionary_locators", "seed", "previous_text",
			"next_text", "previous_request_ids", "next_request_ids",
			"apply_text_normalization", "apply_language_text_normalization",
			"use_pvc_as_ivc", "output_format", "optimize_streaming_latency", "enable_logging",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	modelID, _ := out["model_id"].(string)
	if modelID == "" {
		modelID = "eleven_multilingual_v2"
	}
	if strings.EqualFold(strings.TrimSpace(modelID), "eleven_v3") {
		delete(out, "previous_text")
		delete(out, "next_text")
		delete(out, "previous_request_ids")
		delete(out, "next_request_ids")
	}
	if enableLogging, ok := out["enable_logging"].(bool); ok && !enableLogging {
		// Zero-retention requests do not retain the history required for
		// request-id stitching. Text context remains valid.
		delete(out, "previous_request_ids")
		delete(out, "next_request_ids")
	}
	return out, nil
}

func buildElevenLabsSFXArgs(args map[string]any) (map[string]any, error) {
	out := map[string]any{
		"text": strArg(args, "prompt", ""),
	}
	if d := intArg(args, "duration", 0); d > 0 {
		out["duration_seconds"] = d
	}
	if model := strArg(args, "model", ""); model != "" {
		out["model_id"] = model
	}
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"duration_seconds", "prompt_influence", "loop", "model_id", "output_format",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

func normalizeAudioResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "elevenlabs", "fish-audio", "deepgram", "cartesia":
		return normalizeBinaryAudioResponse(raw)
	case "minimax-audio":
		return normalizeMiniMaxAudioResponse(raw)
	}
	return nil, "", "", fmt.Errorf("unsupported audio provider slug: %q", slug)
}

func normalizeMiniMaxAudioResponse(raw json.RawMessage) ([]generatedMedia, string, string, error) {
	var body struct {
		Data *struct {
			Audio  string `json:"audio"`
			Status int    `json:"status"`
		} `json:"data"`
		ExtraInfo struct {
			AudioLength int64  `json:"audio_length"`
			AudioFormat string `json:"audio_format"`
		} `json:"extra_info"`
		BaseResp struct {
			StatusCode int    `json:"status_code"`
			StatusMsg  string `json:"status_msg"`
		} `json:"base_resp"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", "", err
	}
	if body.BaseResp.StatusCode != 0 {
		return nil, "", "", fmt.Errorf("MiniMax status %d: %s", body.BaseResp.StatusCode, body.BaseResp.StatusMsg)
	}
	if body.Data == nil || strings.TrimSpace(body.Data.Audio) == "" {
		return nil, "", "", fmt.Errorf("MiniMax response missing data.audio")
	}
	format := normalizeMiniMaxAudioFormat(body.ExtraInfo.AudioFormat)
	media := generatedMedia{
		MimeType:   audioMimeFromExt(format),
		Ext:        format,
		DurationMs: body.ExtraInfo.AudioLength,
	}
	audio := strings.TrimSpace(body.Data.Audio)
	if strings.HasPrefix(audio, "https://") || strings.HasPrefix(audio, "http://") {
		media.UpstreamURL = audio
	} else {
		bytes, err := hex.DecodeString(audio)
		if err != nil {
			return nil, "", "", fmt.Errorf("MiniMax data.audio is not valid hex: %w", err)
		}
		media.B64 = base64.StdEncoding.EncodeToString(bytes)
	}
	return []generatedMedia{media}, "", "", nil
}

func audioMimeFromExt(ext string) string {
	switch strings.ToLower(strings.TrimSpace(ext)) {
	case "wav":
		return "audio/wav"
	case "flac":
		return "audio/flac"
	case "pcm", "raw":
		return "audio/pcm"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	default:
		return "audio/mpeg"
	}
}

func normalizeBinaryAudioResponse(raw json.RawMessage) ([]generatedMedia, string, string, error) {
	var env struct {
		Binary   bool   `json:"_binary"`
		Base64   string `json:"base64"`
		MimeType string `json:"mimeType"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", "", err
	}
	if !env.Binary || env.Base64 == "" {
		return nil, "", "", fmt.Errorf("audio response missing binary payload (got: %s)", truncate(string(raw), 200))
	}
	mt := env.MimeType
	if mt == "" {
		mt = "audio/mpeg"
	}
	return []generatedMedia{{
		B64:      env.Base64,
		MimeType: mt,
		Ext:      audioExtFromMime(mt),
	}}, "", "", nil
}

func audioExtFromMime(mt string) string {
	switch mt {
	case "audio/mpeg", "audio/mp3":
		return "mp3"
	case "audio/wav", "audio/wave", "audio/x-wav":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/aac":
		return "aac"
	case "audio/flac":
		return "flac"
	case "audio/opus":
		return "opus"
	case "audio/pcm", "audio/L16":
		return "pcm"
	}
	return "mp3"
}
