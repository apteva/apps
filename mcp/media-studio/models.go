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
	// Constraints parsed from Venice's model_spec.constraints —
	// surfaced so the panel can render real dropdowns instead of
	// free-form inputs. All optional; empty arrays mean "no
	// preset values, use a text input".
	ModelType            string   `json:"model_type,omitempty"`
	AspectRatios         []string `json:"aspect_ratios,omitempty"`
	DefaultAspectRatio   string   `json:"default_aspect_ratio,omitempty"`
	Resolutions          []string `json:"resolutions,omitempty"`
	DefaultResolution    string   `json:"default_resolution,omitempty"`
	Durations            []string `json:"durations,omitempty"`
	SupportsImageToVideo bool     `json:"supports_image_to_video,omitempty"`
	AudioConfigurable    bool     `json:"audio_configurable,omitempty"`
	StepsDefault         int      `json:"steps_default,omitempty"`
	StepsMax             int      `json:"steps_max,omitempty"`
	PromptCharLimit      int      `json:"prompt_char_limit,omitempty"`
	// PriceUSD is a representative cost — flat for pixel-models,
	// the cheapest tier for resolution-tier models, the inpaint
	// price for edit models. The panel uses this for the dropdown
	// label so the user sees what each model costs upfront.
	PriceUSD float64 `json:"price_usd,omitempty"`
}

type modelCacheKey struct {
	ConnectionID int64
	Kind         string
}

type modelCacheValue struct {
	Models    []modelEntry
	FetchedAt time.Time
}

// specCacheKey holds the raw Venice model_spec keyed by venice-type
// (image / inpaint / video / …) so cost lookups can find a model
// regardless of which kind-tab the user originally fetched.
type specCacheKey struct {
	ConnectionID int64
	VeniceType   string // "image" | "inpaint" | "video" | "tts" | "music"
	ModelID      string
}

var (
	modelCacheMu sync.RWMutex
	modelCache   = map[modelCacheKey]modelCacheValue{}
	specCache    = map[specCacheKey]json.RawMessage{}
	specCacheAt  = map[specCacheKey]time.Time{}
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
	veniceType := ""
	if bound.AppSlug == "venice-ai" {
		veniceType = kindToVeniceType(kind)
	}
	models := parseModelList(bound.AppSlug, kind, res.Data, bound.ConnectionID, veniceType)

	modelCacheMu.Lock()
	modelCache[key] = modelCacheValue{Models: models, FetchedAt: time.Now()}
	modelCacheMu.Unlock()
	return models, nil
}

// parseModelList normalizes per-provider response shapes into a
// uniform {id, label} list. Filters to the kind when the provider
// returns mixed types in one payload (OpenAI). For Venice it also
// populates the spec cache so cost lookups can find each model's
// model_spec.pricing later without an extra round-trip.
func parseModelList(providerSlug, kind string, raw json.RawMessage, connID int64, veniceType string) []modelEntry {
	switch providerSlug {
	case "venice-ai":
		var body struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil
		}
		out := make([]modelEntry, 0, len(body.Data))
		now := time.Now()
		modelCacheMu.Lock()
		for _, mRaw := range body.Data {
			var head struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			}
			if err := json.Unmarshal(mRaw, &head); err != nil || head.ID == "" {
				continue
			}
			// Stash the full raw blob keyed by (conn, venice-type, model)
			// — pricing lookup reads from here.
			specCache[specCacheKey{ConnectionID: connID, VeniceType: veniceType, ModelID: head.ID}] = mRaw
			specCacheAt[specCacheKey{ConnectionID: connID, VeniceType: veniceType, ModelID: head.ID}] = now
			out = append(out, buildModelEntryFromVeniceSpec(head.ID, mRaw))
		}
		modelCacheMu.Unlock()
		return out
	case "openai-api":
		_ = connID // openai has no published model_spec to cache; cost stays 0
		_ = veniceType
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

// buildModelEntryFromVeniceSpec parses a Venice model object into the
// uniform modelEntry the panel renders. Venice mixes naming
// conventions (image models use camelCase like aspectRatios /
// defaultAspectRatio; video + inpaint use snake_case like
// aspect_ratios / model_type / durations), so we accept both via
// json struct tags and pick whichever has values.
func buildModelEntryFromVeniceSpec(id string, raw json.RawMessage) modelEntry {
	var spec struct {
		ModelSpec struct {
			Constraints struct {
				// snake_case (video / inpaint)
				ModelType         string   `json:"model_type"`
				AspectRatiosSnake []string `json:"aspect_ratios"`
				Resolutions       []string `json:"resolutions"`
				Durations         []string `json:"durations"`
				AudioConfigurable bool     `json:"audio_configurable"`
				// camelCase (image)
				AspectRatiosCamel []string `json:"aspectRatios"`
				DefaultAspect     string   `json:"defaultAspectRatio"`
				DefaultResolution string   `json:"defaultResolution"`
				PromptCharLimit   int      `json:"promptCharacterLimit"`
				Steps             struct {
					Default int `json:"default"`
					Max     int `json:"max"`
				} `json:"steps"`
			} `json:"constraints"`
			Pricing struct {
				Generation  *struct{ USD float64 `json:"usd"` } `json:"generation,omitempty"`
				Inpaint     *struct{ USD float64 `json:"usd"` } `json:"inpaint,omitempty"`
				Resolutions map[string]struct {
					USD float64 `json:"usd"`
				} `json:"resolutions,omitempty"`
			} `json:"pricing"`
		} `json:"model_spec"`
	}
	_ = json.Unmarshal(raw, &spec)

	c := spec.ModelSpec.Constraints
	aspects := c.AspectRatiosCamel
	if len(aspects) == 0 {
		aspects = c.AspectRatiosSnake
	}

	// PriceUSD: flat generation rate, else cheapest resolution tier,
	// else the inpaint flat rate. Zero when none are published.
	var price float64
	switch {
	case spec.ModelSpec.Pricing.Generation != nil:
		price = spec.ModelSpec.Pricing.Generation.USD
	case spec.ModelSpec.Pricing.Inpaint != nil:
		price = spec.ModelSpec.Pricing.Inpaint.USD
	case len(spec.ModelSpec.Pricing.Resolutions) > 0:
		// Prefer the default tier if known, else the cheapest entry.
		if def := c.DefaultResolution; def != "" {
			if r, ok := spec.ModelSpec.Pricing.Resolutions[def]; ok {
				price = r.USD
			}
		}
		if price == 0 {
			min := 0.0
			for _, r := range spec.ModelSpec.Pricing.Resolutions {
				if min == 0 || r.USD < min {
					min = r.USD
				}
			}
			price = min
		}
	}

	supportsImg2Vid := c.ModelType == "image-to-video"

	return modelEntry{
		ID:                   id,
		Label:                id, // panel re-styles labels with price/model type
		ModelType:            c.ModelType,
		AspectRatios:         aspects,
		DefaultAspectRatio:   c.DefaultAspect,
		Resolutions:          c.Resolutions,
		DefaultResolution:    c.DefaultResolution,
		Durations:            c.Durations,
		SupportsImageToVideo: supportsImg2Vid,
		AudioConfigurable:    c.AudioConfigurable,
		StepsDefault:         c.Steps.Default,
		StepsMax:             c.Steps.Max,
		PromptCharLimit:      c.PromptCharLimit,
		PriceUSD:             price,
	}
}

// ensureVeniceSpecLoaded triggers a sync fetch+parse of Venice's
// type=<veniceType> models for the given connection if the spec cache
// doesn't have it yet (or if the entry is older than modelCacheTTL).
// No-op when the platform call fails — we just log and return,
// cost lookup falls back to 0.
func ensureVeniceSpecLoaded(ctx *sdk.AppCtx, connID int64, veniceType string) {
	// Cheap probe — if any entry exists for this (connID, veniceType)
	// and is fresh, we already have specs.
	modelCacheMu.RLock()
	for k, ts := range specCacheAt {
		if k.ConnectionID == connID && k.VeniceType == veniceType && time.Since(ts) < modelCacheTTL {
			modelCacheMu.RUnlock()
			return
		}
	}
	modelCacheMu.RUnlock()

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_models", map[string]any{
		"type": veniceType,
	})
	if err != nil || res == nil || !res.Success {
		return
	}
	parseModelList("venice-ai", "", res.Data, connID, veniceType)
}

// getVeniceModelSpec returns the cached raw model_spec for a given
// (connection, venice-type, model). Returns (nil, false) when missing.
func getVeniceModelSpec(connID int64, veniceType, modelID string) (json.RawMessage, bool) {
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	raw, ok := specCache[specCacheKey{ConnectionID: connID, VeniceType: veniceType, ModelID: modelID}]
	return raw, ok
}

// computeVeniceImageCost reads model_spec.pricing from a cached
// Venice model and returns the cost in USD for one variant of the
// given capability + args. Cost = perVariant × variants.
//
// Pricing shapes seen in the wild:
//   generate (pixel models):       {"pricing":{"generation":{"usd":0.01}}}
//   generate (resolution tier):    {"pricing":{"resolutions":{"1K":{"usd":0.08}, "2K":{"usd":0.10}}}}
//   edit (Venice's "inpaint"):     {"pricing":{"inpaint":{"usd":0.04}}}
//
// Returns (0, false) when the spec lacks a price for the capability.
func computeVeniceImageCost(specRaw json.RawMessage, capability string, args map[string]any) (float64, bool) {
	var spec struct {
		ModelSpec struct {
			Pricing struct {
				Generation  *struct{ USD float64 `json:"usd"` } `json:"generation,omitempty"`
				Inpaint     *struct{ USD float64 `json:"usd"` } `json:"inpaint,omitempty"`
				Resolutions map[string]struct {
					USD float64 `json:"usd"`
				} `json:"resolutions,omitempty"`
			} `json:"pricing"`
			Constraints struct {
				DefaultResolution string `json:"defaultResolution"`
			} `json:"constraints"`
		} `json:"model_spec"`
	}
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		return 0, false
	}
	p := spec.ModelSpec.Pricing
	variants := intArg(args, "n", 1)
	if v := intArg(args, "variants", 0); v > 0 {
		variants = v
	}
	if variants < 1 {
		variants = 1
	}

	var perVariant float64
	var ok bool
	switch capability {
	case "image.edit":
		if p.Inpaint != nil {
			perVariant, ok = p.Inpaint.USD, true
		}
	case "image.generate":
		if p.Generation != nil {
			perVariant, ok = p.Generation.USD, true
			break
		}
		if len(p.Resolutions) > 0 {
			tier := ""
			if opts, _ := args["options"].(map[string]any); opts != nil {
				tier = strArg(opts, "resolution", "")
			}
			if tier == "" {
				tier = spec.ModelSpec.Constraints.DefaultResolution
			}
			if tier == "" {
				tier = "1K"
			}
			if r, found := p.Resolutions[tier]; found {
				perVariant, ok = r.USD, true
			}
		}
	}
	if !ok {
		return 0, false
	}
	return perVariant * float64(variants), true
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
