package main

import (
	"encoding/json"
	"fmt"
)

// Audio (TTS + SFX) is sync for ElevenLabs: the integration executor
// buffers the upstream binary response into {_binary, base64, mimeType}.

func buildAudioTTSArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "elevenlabs":
		return buildElevenLabsTTSArgs(args)
	}
	return nil, fmt.Errorf("unsupported audio TTS provider slug: %q", providerSlug)
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
			"apply_text_normalization", "output_format", "optimize_streaming_latency",
			"enable_logging",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
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
	case "elevenlabs":
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
