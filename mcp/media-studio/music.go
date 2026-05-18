package main

import "encoding/json"

// Music generation is stubbed until at least one provider integration
// lands (Suno, Replicate-MusicGen). The dispatcher wires this in via
// handlers[KindMusic]; once buildMusicArgs + normalizeMusicResponse
// know a provider slug, the kind goes live end-to-end.

func buildMusicArgs(args map[string]any, providerSlug string) (map[string]any, error) {
	return nil, errKindStub
}

func normalizeMusicResponse(slug string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	return nil, "", "", errKindStub
}
