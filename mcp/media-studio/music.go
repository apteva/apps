package main

import (
	"encoding/json"
	"fmt"
)

// Music generation is sync for ElevenLabs: POST /music returns audio
// bytes, which the integration executor wraps in a binary envelope.

func buildMusicArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "elevenlabs":
		return buildElevenLabsMusicArgs(args), nil
	}
	return nil, fmt.Errorf("unsupported music provider slug: %q", providerSlug)
}

func buildElevenLabsMusicArgs(args map[string]any) map[string]any {
	out := map[string]any{
		"prompt": strArg(args, "prompt", ""),
	}
	if d := intArg(args, "duration", 0); d > 0 {
		out["music_length_ms"] = d * 1000
	}
	if model := strArg(args, "model", ""); model != "" {
		out["model_id"] = model
	}
	if opts, ok := args["options"].(map[string]any); ok {
		if plan, exists := opts["composition_plan"]; exists {
			delete(out, "prompt")
			out["composition_plan"] = plan
		}
		passThrough := []string{
			"music_length_ms", "model_id", "output_format",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out
}

func normalizeMusicResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "elevenlabs":
		return normalizeBinaryAudioResponse(raw)
	}
	return nil, "", "", fmt.Errorf("unsupported music provider slug: %q", slug)
}
