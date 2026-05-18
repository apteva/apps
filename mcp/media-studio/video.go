package main

import "encoding/json"

// Video generation is stubbed until at least one provider integration
// lands (Replicate, Runway, Pika). The dispatcher wires this in via
// handlers[KindVideo]; once buildVideoArgs + normalizeVideoResponse
// know a provider slug, the kind goes live end-to-end without any
// other change.

func buildVideoArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	return nil, errKindStub
}

func normalizeVideoResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	return nil, "", "", errKindStub
}
