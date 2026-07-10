package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Audio (TTS + SFX) is synchronous. Provider integrations buffer upstream
// binary responses into {_binary, base64, mimeType}.

func buildAudioTTSArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "elevenlabs":
		return buildElevenLabsTTSArgs(args)
	case "fish-audio":
		return buildFishAudioTTSArgs(args)
	}
	return nil, fmt.Errorf("unsupported audio TTS provider slug: %q", providerSlug)
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
	case "elevenlabs", "fish-audio":
		return normalizeBinaryAudioResponse(raw)
	}
	return nil, "", "", fmt.Errorf("unsupported audio provider slug: %q", slug)
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
