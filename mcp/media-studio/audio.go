package main

import "encoding/json"

// Audio (TTS + SFX) is stubbed until at least one provider integration
// lands (ElevenLabs covers both; OpenAI is TTS only). One role
// (audio_provider) with two capabilities (audio.tts, audio.sfx) — the
// dispatcher invokes the matching tool per kind.

func buildAudioTTSArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	return nil, errKindStub
}

func buildAudioSFXArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	return nil, errKindStub
}

// Same normalizer for both — providers tend to return the same envelope
// regardless of which capability was invoked. Split if that turns out
// false for a specific provider.
func normalizeAudioResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	return nil, "", "", errKindStub
}
