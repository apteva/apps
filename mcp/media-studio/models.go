package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Live-loaded model list per (provider, kind). Hits the bound
// integration's list_models tool, parses the provider-specific
// response into a uniform {id, label} list, and caches in-memory.
// Refreshed on cache miss or every modelCacheTTL.
//
// Cache scope: per sidecar process. Cleared on restart. Each install
// has its own sidecar so cross-install pollution isn't a concern.

const modelCacheTTL = 10 * time.Minute

type modelEntry struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type modelCacheKey struct {
	ConnectionID int64
	Kind         string
}

type modelCacheValue struct {
	Models    []modelEntry
	FetchedAt time.Time
}

var (
	modelCacheMu sync.RWMutex
	modelCache   = map[modelCacheKey]modelCacheValue{}
)

// kindToVeniceType maps a media-studio kind to Venice's list_models
// `type` query param. Empty means "no type filter".
func kindToVeniceType(kind string) string {
	switch kind {
	case KindImage:
		return "image"
	case KindVideo:
		return "video"
	case KindAudioTTS, KindAudioSFX:
		return "tts"
	case KindMusic:
		return "music"
	}
	return ""
}

// loadModelsFor returns the (live or cached) model list for the
// currently-bound provider of `kind`. nil + nil error when no
// provider is bound.
func loadModelsFor(ctx *sdk.AppCtx, kind string) ([]modelEntry, error) {
	h, ok := handlers[kind]
	if !ok {
		return nil, nil
	}
	bound := ctx.IntegrationFor(h.Role)
	if bound == nil {
		return nil, nil
	}
	key := modelCacheKey{ConnectionID: bound.ConnectionID, Kind: kind}
	modelCacheMu.RLock()
	if v, hit := modelCache[key]; hit && time.Since(v.FetchedAt) < modelCacheTTL {
		modelCacheMu.RUnlock()
		return v.Models, nil
	}
	modelCacheMu.RUnlock()

	args := map[string]any{}
	if bound.AppSlug == "venice-ai" {
		if t := kindToVeniceType(kind); t != "" {
			args["type"] = t
		}
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_models", args)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		return nil, nil
	}
	models := parseModelList(bound.AppSlug, kind, res.Data)

	modelCacheMu.Lock()
	modelCache[key] = modelCacheValue{Models: models, FetchedAt: time.Now()}
	modelCacheMu.Unlock()
	return models, nil
}

// parseModelList normalizes per-provider response shapes into a
// uniform {id, label} list. Filters to the kind when the provider
// returns mixed types in one payload (OpenAI).
func parseModelList(providerSlug, kind string, raw json.RawMessage) []modelEntry {
	switch providerSlug {
	case "venice-ai":
		var body struct {
			Data []struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil
		}
		out := make([]modelEntry, 0, len(body.Data))
		for _, m := range body.Data {
			if m.ID == "" {
				continue
			}
			out = append(out, modelEntry{ID: m.ID, Label: m.ID})
		}
		return out
	case "openai-api":
		var body struct {
			Data []struct {
				ID      string `json:"id"`
				OwnedBy string `json:"owned_by"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil
		}
		out := make([]modelEntry, 0, len(body.Data))
		for _, m := range body.Data {
			if !openaiModelMatches(m.ID, kind) {
				continue
			}
			out = append(out, modelEntry{ID: m.ID, Label: m.ID})
		}
		return out
	}
	return nil
}

// openaiModelMatches filters OpenAI's flat /models list to the ones
// relevant for a media-studio kind. OpenAI returns every model
// (chat, embeddings, tts, image, whisper, …) in one response — we
// pluck the ones that match the kind's purpose.
func openaiModelMatches(id, kind string) bool {
	id = strings.ToLower(id)
	switch kind {
	case KindImage:
		return strings.HasPrefix(id, "gpt-image") || strings.HasPrefix(id, "dall-e")
	case KindVideo:
		return strings.HasPrefix(id, "sora")
	case KindAudioTTS:
		return strings.HasPrefix(id, "tts-") ||
			strings.HasPrefix(id, "gpt-4o-mini-tts") ||
			strings.HasPrefix(id, "gpt-4o-tts")
	case KindMusic, KindAudioSFX:
		return false // OpenAI doesn't offer these as discrete models
	}
	return false
}

// HTTP /models — read endpoint for the panel.

func (a *App) handleListModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	h, ok := handlers[kind]
	if !ok {
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	bound := globalCtx.IntegrationFor(h.Role)
	resp := map[string]any{
		"kind":   kind,
		"bound":  bound != nil,
		"models": []modelEntry{},
	}
	if bound != nil {
		resp["provider"] = bound.AppSlug
		// ?refresh=1 forces a re-fetch even if the cached entry is fresh.
		if r.URL.Query().Get("refresh") == "1" {
			invalidateModelCacheForConnection(bound.ConnectionID)
		}
		models, err := loadModelsFor(globalCtx, kind)
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["models"] = models
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// invalidateModelCacheForConnection drops any cached entries for a
// given connection. Useful when the operator rotates the binding.
// (Not wired to a hook yet — manual refresh from the panel by
// adding ?refresh=1 covers the common case.)
func invalidateModelCacheForConnection(connID int64) {
	modelCacheMu.Lock()
	defer modelCacheMu.Unlock()
	for k := range modelCache {
		if k.ConnectionID == connID {
			delete(modelCache, k)
		}
	}
}
