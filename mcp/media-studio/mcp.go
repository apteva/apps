package main

import "fmt"

type buildResultArgs struct {
	Kind          string
	Prompt        string
	Revised       string
	Model         string
	Provider      string
	ProjectID     string
	StorageIDs    []int64
	UpstreamURLs  []string
	FirstThumbB64 string
	Count         int
	MimeType      string
}

// buildMCPResult shapes the MCP content blocks per kind. Image kind
// (when storage isn't bound) inlines a JPEG thumbnail so the agent can
// see what was generated; everything else carries text + resource
// blocks pointing at the storage URL (or the upstream URL as fallback
// when storage is unbound).
func buildMCPResult(a buildResultArgs) map[string]any {
	storageURLs := make([]string, 0, len(a.StorageIDs))
	for _, id := range a.StorageIDs {
		storageURLs = append(storageURLs, storageContentURL(id, a.ProjectID))
	}
	hasStorage := len(a.StorageIDs) > 0

	content := []map[string]any{}

	// Inline thumbnail only for image kind without storage. With storage
	// bound the URL is the carrier — no need to also ship 30KB of base64
	// through every tool call. Video/audio/music never inline (too big).
	if a.Kind == KindImage && !hasStorage && a.FirstThumbB64 != "" {
		content = append(content, map[string]any{
			"type":     "image",
			"data":     a.FirstThumbB64,
			"mimeType": "image/jpeg",
		})
	}

	summary := fmt.Sprintf("Generated %d %s item(s) via %s (model=%s).\nPrompt: %q",
		a.Count, a.Kind, a.Provider, a.Model, a.Prompt)
	if a.Revised != "" && a.Revised != a.Prompt {
		summary += "\nRevised: " + a.Revised
	}
	if hasStorage {
		summary += "\nSaved to storage:"
		for i, id := range a.StorageIDs {
			summary += fmt.Sprintf("\n  - id=%d url=%s", id, storageURLs[i])
		}
	} else if len(a.UpstreamURLs) > 0 {
		// Without storage, surface the upstream URLs so the caller can
		// at least fetch the bytes before they expire.
		summary += "\nUpstream URLs (may expire):"
		for _, u := range a.UpstreamURLs {
			if u != "" {
				summary += "\n  - " + u
			}
		}
	}
	content = append(content, map[string]any{"type": "text", "text": summary})

	// Resource blocks carry the fetchable URL so MCP-aware UIs can
	// embed the media without the agent having to construct the URL.
	mime := a.MimeType
	if mime == "" {
		mime = defaultMime(a.Kind)
	}
	for i, id := range a.StorageIDs {
		content = append(content, map[string]any{
			"type": "resource",
			"resource": map[string]any{
				"uri":      storageURLs[i],
				"mimeType": mime,
				"name":     fmt.Sprintf("storage:%d", id),
			},
		})
	}

	meta := map[string]any{
		"kind":           a.Kind,
		"prompt":         a.Prompt,
		"revised_prompt": a.Revised,
		"model":          a.Model,
		"provider":       a.Provider,
		"storage_ids":    a.StorageIDs,
		"storage_urls":   storageURLs,
		"upstream_urls":  a.UpstreamURLs,
	}
	return map[string]any{
		"content": content,
		"_meta":   meta,
	}
}

func defaultMime(kind string) string {
	switch kind {
	case KindImage:
		return "image/png"
	case KindVideo:
		return "video/mp4"
	case KindAudioTTS, KindAudioSFX:
		return "audio/mpeg"
	case KindMusic:
		return "audio/mpeg"
	}
	return "application/octet-stream"
}

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": msg},
		},
	}
}
