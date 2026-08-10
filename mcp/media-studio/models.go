package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

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
	ID       string `json:"id"`
	Label    string `json:"label"`
	Provider string `json:"provider,omitempty"`
	// SizeModes tells callers how to express output shape for this model:
	// "pixel" for WxH size, "aspect" for aspect_ratio, and
	// "resolution" for provider tiers such as 1K/2K/4K.
	SizeModes []string `json:"size_modes,omitempty"`
	// Constraints parsed from Venice's model_spec.constraints —
	// surfaced so the panel can render real dropdowns instead of
	// free-form inputs. All optional; empty arrays mean "no
	// preset values, use a text input".
	ModelType            string   `json:"model_type,omitempty"`
	PixelSizes           []string `json:"pixel_sizes,omitempty"`
	AspectRatios         []string `json:"aspect_ratios,omitempty"`
	DefaultAspectRatio   string   `json:"default_aspect_ratio,omitempty"`
	Resolutions          []string `json:"resolutions,omitempty"`
	DefaultResolution    string   `json:"default_resolution,omitempty"`
	Durations            []string `json:"durations,omitempty"`
	SupportsImageToVideo bool     `json:"supports_image_to_video,omitempty"`
	SupportsImageEdit    bool     `json:"supports_image_edit,omitempty"`
	MaxSourceImages      int      `json:"max_source_images,omitempty"`
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

func validateProviderPrompt(_ *sdk.AppCtx, bound *sdk.BoundIntegration, kind, capability string, args map[string]any) error {
	prompt := strArg(args, "prompt", "")
	limit := cachedPromptLimit(bound, kind, capability, strArg(args, "model", ""))
	if limit == 0 && kind == KindVideo && bound.AppSlug == "venice-ai" {
		limit = veniceVideoPromptCharLimit
	}
	if count := utf8.RuneCountInString(prompt); limit > 0 && count > limit {
		return fmt.Errorf("%d characters exceeds the %d-character limit for %s", count, limit, strArg(args, "model", "selected model"))
	}
	return nil
}

func cachedPromptLimit(bound *sdk.BoundIntegration, kind, capability, modelID string) int {
	if bound == nil || strings.TrimSpace(modelID) == "" {
		return 0
	}
	cacheKind := kind
	if capability != "" {
		cacheKind = kind + ":" + capability
	}
	modelCacheMu.RLock()
	defer modelCacheMu.RUnlock()
	for _, candidateKind := range []string{cacheKind, kind} {
		for _, model := range modelCache[modelCacheKey{ConnectionID: bound.ConnectionID, Kind: candidateKind}].Models {
			if model.ID == modelID {
				return model.PromptCharLimit
			}
		}
	}
	return 0
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

func veniceTypeForCapability(kind, capability string) string {
	if kind == KindImage && capability == "image.edit" {
		return "inpaint"
	}
	return kindToVeniceType(kind)
}

// loadModelsFor returns the (live or cached) model list for the
// currently-bound provider of `kind`. nil + nil error when no
// provider is bound.
func loadModelsFor(ctx *sdk.AppCtx, kind string) ([]modelEntry, error) {
	return loadModelsForCapability(ctx, kind, "")
}

func loadModelsForCapability(ctx *sdk.AppCtx, kind, capability string) ([]modelEntry, error) {
	h, ok := handlers[kind]
	if !ok {
		return nil, nil
	}
	bound := ctx.IntegrationFor(h.Role)
	return loadModelsForBoundCapability(ctx, kind, capability, bound)
}

func providerScopedModels(provider string, models []modelEntry, namespace bool) []modelEntry {
	out := make([]modelEntry, 0, len(models))
	for _, m := range models {
		m.Provider = provider
		if namespace {
			m.ID = provider + ":" + m.ID
			if !strings.Contains(m.Label, provider) {
				m.Label = m.Label + " · " + provider
			}
		}
		out = append(out, m)
	}
	return out
}

func normalizeImageModelCapability(capability string) string {
	if strings.TrimSpace(capability) == "" {
		return "image.generate"
	}
	return capability
}

func loadImageModelsForAllProviders(ctx *sdk.AppCtx, capability string) ([]modelEntry, []string, error) {
	capability = normalizeImageModelCapability(capability)
	h, ok := handlers[KindImage]
	if !ok {
		return nil, nil, nil
	}
	bounds := boundIntegrationsFor(ctx, h.Role)
	if len(bounds) == 0 {
		return nil, nil, nil
	}
	namespace := len(bounds) > 1
	out := []modelEntry{}
	providers := []string{}
	for _, bound := range bounds {
		if bound == nil || !imageProviderSupports(bound.AppSlug, capability) {
			continue
		}
		providers = append(providers, bound.AppSlug)
		models, err := loadModelsForBoundCapability(ctx, KindImage, capability, bound)
		if err != nil {
			return out, providers, err
		}
		out = append(out, providerScopedModels(bound.AppSlug, models, namespace)...)
	}
	return out, providers, nil
}

func loadAudioModelsForAllProviders(ctx *sdk.AppCtx, kind, capability string) ([]modelEntry, []string, error) {
	h, ok := handlers[kind]
	if !ok || h.Role != "audio_provider" {
		return nil, nil, nil
	}
	bounds := boundIntegrationsFor(ctx, h.Role)
	supported := make([]*sdk.BoundIntegration, 0, len(bounds))
	for _, bound := range bounds {
		if bound != nil && audioProviderSupports(bound.AppSlug, capability) {
			supported = append(supported, bound)
		}
	}
	namespace := len(supported) > 1
	out := []modelEntry{}
	providers := []string{}
	for _, bound := range supported {
		providers = append(providers, bound.AppSlug)
		models, err := loadModelsForBoundCapability(ctx, kind, capability, bound)
		if err != nil {
			return out, providers, err
		}
		out = append(out, providerScopedModels(bound.AppSlug, models, namespace)...)
	}
	return out, providers, nil
}

func loadModelsForBoundCapability(ctx *sdk.AppCtx, kind, capability string, bound *sdk.BoundIntegration) ([]modelEntry, error) {
	if bound == nil {
		return nil, nil
	}
	return loadModelsForCapabilityBound(ctx, kind, capability, bound)
}

func loadModelsForCapabilityBound(ctx *sdk.AppCtx, kind, capability string, bound *sdk.BoundIntegration) ([]modelEntry, error) {
	if bound == nil {
		return nil, nil
	}
	if bound.AppSlug == "openai-codex" {
		if capability == "image.edit" {
			return []modelEntry{}, nil
		}
		return []modelEntry{{
			ID:                 "gpt-5.5",
			Label:              "GPT-5.5 (Codex image generation)",
			SizeModes:          []string{"pixel"},
			PixelSizes:         []string{"1024x1024", "1024x1536", "1536x1024"},
			DefaultAspectRatio: "1:1",
		}}, nil
	}
	if bound.AppSlug == "fish-audio" {
		if kind == KindAudioTTS {
			return fishAudioDefaultModels(), nil
		}
		return []modelEntry{}, nil
	}
	if bound.AppSlug == "deepgram" {
		if kind == KindAudioTTS {
			return deepgramDefaultModels(), nil
		}
		return []modelEntry{}, nil
	}
	if bound.AppSlug == "cartesia" {
		if kind == KindAudioTTS {
			return cartesiaDefaultModels(), nil
		}
		return []modelEntry{}, nil
	}
	if bound.AppSlug == "minimax-audio" {
		if kind == KindAudioTTS {
			return miniMaxDefaultModels(), nil
		}
		return []modelEntry{}, nil
	}
	cacheKind := kind
	if capability != "" {
		cacheKind = kind + ":" + capability
	}
	key := modelCacheKey{ConnectionID: bound.ConnectionID, Kind: cacheKind}
	modelCacheMu.RLock()
	if v, hit := modelCache[key]; hit && time.Since(v.FetchedAt) < modelCacheTTL {
		modelCacheMu.RUnlock()
		return v.Models, nil
	}
	modelCacheMu.RUnlock()

	args := map[string]any{}
	if bound.AppSlug == "venice-ai" {
		if t := veniceTypeForCapability(kind, capability); t != "" {
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
		veniceType = veniceTypeForCapability(kind, capability)
	}
	models := parseModelList(bound.AppSlug, kind, res.Data, bound.ConnectionID, veniceType)
	models = filterModelsForCapability(models, capability)

	modelCacheMu.Lock()
	modelCache[key] = modelCacheValue{Models: models, FetchedAt: time.Now()}
	modelCacheMu.Unlock()
	return models, nil
}

func modelCatalogForKind(ctx *sdk.AppCtx, kind, capability string) (map[string]any, error) {
	if kind == "" {
		kind = KindImage
	}
	if _, ok := handlers[kind]; !ok {
		return map[string]any{
			"kind":   kind,
			"bound":  false,
			"models": []modelEntry{},
			"error":  "unknown kind",
		}, nil
	}
	if kind == KindImage {
		models, providers, err := loadImageModelsForAllProviders(ctx, capability)
		return map[string]any{
			"kind":      kind,
			"bound":     len(providers) > 0,
			"providers": providers,
			"provider":  strings.Join(providers, ","),
			"models":    models,
		}, err
	}
	if kind == KindAudioTTS || kind == KindAudioSFX {
		capability = handlers[kind].ResolveCapability(map[string]any{})
		models, providers, err := loadAudioModelsForAllProviders(ctx, kind, capability)
		return map[string]any{
			"kind":      kind,
			"bound":     len(providers) > 0,
			"providers": providers,
			"provider":  strings.Join(providers, ","),
			"models":    models,
		}, err
	}
	h := handlers[kind]
	bound := ctx.IntegrationFor(h.Role)
	resp := map[string]any{
		"kind":   kind,
		"bound":  bound != nil,
		"models": []modelEntry{},
	}
	if bound == nil {
		return resp, nil
	}
	resp["provider"] = bound.AppSlug
	models, err := loadModelsForBoundCapability(ctx, kind, capability, bound)
	resp["models"] = models
	return resp, err
}

func (a *App) toolMediaModels(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	kind := strArg(args, "kind", KindImage)
	return modelCatalogForKind(ctx, kind, "")
}

func filterModelsForCapability(models []modelEntry, capability string) []modelEntry {
	if capability != "image.edit" {
		return models
	}
	out := make([]modelEntry, 0, len(models))
	for _, model := range models {
		if model.SupportsImageEdit {
			out = append(out, model)
		}
	}
	return out
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
			out = append(out, buildModelEntryFromVeniceSpec(head.ID, mRaw, veniceType))
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
			entry := modelEntry{ID: m.ID, Label: m.ID}
			if kind == KindImage {
				id := strings.ToLower(m.ID)
				entry.SizeModes = []string{"pixel"}
				entry.PixelSizes = openAIImagePixelSizes(id)
				switch {
				case strings.HasPrefix(id, "gpt-image"):
					entry.SupportsImageEdit = true
					entry.MaxSourceImages = 16
				case id == "dall-e-2":
					entry.SupportsImageEdit = true
					entry.MaxSourceImages = 1
				}
			}
			out = append(out, entry)
		}
		return out
	case "gemini":
		_ = connID
		_ = veniceType
		return parseGeminiModelList(kind, raw)
	case "elevenlabs":
		_ = connID
		_ = veniceType
		return parseElevenLabsModelList(kind, raw)
	}
	return nil
}

func parseGeminiModelList(kind string, raw json.RawMessage) []modelEntry {
	if kind != KindImage {
		return nil
	}
	type geminiModel struct {
		Name                       string   `json:"name"`
		DisplayName                string   `json:"displayName"`
		SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
	}
	var body struct {
		Models []geminiModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return geminiDefaultImageModels()
	}
	out := []modelEntry{}
	for _, m := range body.Models {
		id := strings.TrimPrefix(m.Name, "models/")
		if !geminiImageModelMatches(id, m.SupportedGenerationMethods) {
			continue
		}
		label := m.DisplayName
		if label == "" {
			label = id
		}
		out = append(out, modelEntry{
			ID:                 id,
			Label:              label,
			SizeModes:          []string{"aspect", "resolution"},
			AspectRatios:       []string{"1:1", "9:16", "16:9", "3:4", "4:3"},
			DefaultAspectRatio: "1:1",
			SupportsImageEdit:  true,
			MaxSourceImages:    5,
			DefaultResolution:  "1K",
			Resolutions:        []string{"1K", "2K", "4K"},
			PromptCharLimit:    32000,
		})
	}
	if len(out) == 0 {
		return geminiDefaultImageModels()
	}
	return out
}

func geminiImageModelMatches(id string, methods []string) bool {
	id = strings.ToLower(id)
	if !strings.Contains(id, "image") {
		return false
	}
	if len(methods) == 0 {
		return true
	}
	for _, m := range methods {
		if strings.EqualFold(m, "generateContent") {
			return true
		}
	}
	return false
}

func geminiDefaultImageModels() []modelEntry {
	return []modelEntry{
		{
			ID:                 "gemini-2.5-flash-image",
			Label:              "Gemini 2.5 Flash Image",
			SizeModes:          []string{"aspect"},
			AspectRatios:       []string{"1:1", "9:16", "16:9", "3:4", "4:3"},
			DefaultAspectRatio: "1:1",
			SupportsImageEdit:  true,
			MaxSourceImages:    5,
			PromptCharLimit:    32000,
		},
		{
			ID:                 "gemini-3-pro-image-preview",
			Label:              "Gemini 3 Pro Image Preview",
			SizeModes:          []string{"aspect", "resolution"},
			AspectRatios:       []string{"1:1", "9:16", "16:9", "3:4", "4:3"},
			DefaultAspectRatio: "1:1",
			Resolutions:        []string{"1K", "2K", "4K"},
			DefaultResolution:  "1K",
			SupportsImageEdit:  true,
			MaxSourceImages:    5,
			PromptCharLimit:    32000,
		},
	}
}

func parseElevenLabsModelList(kind string, raw json.RawMessage) []modelEntry {
	type elevenModel struct {
		ID                 string `json:"model_id"`
		Name               string `json:"name"`
		DisplayName        string `json:"display_name"`
		CanDoTextToSpeech  bool   `json:"can_do_text_to_speech"`
		CanDoSoundEffects  bool   `json:"can_do_sound_effects"`
		CanDoMusic         bool   `json:"can_do_music"`
		CanDoVoice         bool   `json:"can_be_used_for_voice_cloning"`
		MaxCharacters      int    `json:"max_characters_request_free_user"`
		MaxCharactersPaid  int    `json:"max_characters_request_subscribed_user"`
		MaxCharactersTotal int    `json:"maximum_text_length_per_request"`
	}
	var list []elevenModel
	if err := json.Unmarshal(raw, &list); err != nil {
		var wrapped struct {
			Data   []elevenModel `json:"data"`
			Models []elevenModel `json:"models"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			return elevenLabsDefaultModels(kind)
		}
		if len(wrapped.Data) > 0 {
			list = wrapped.Data
		} else {
			list = wrapped.Models
		}
	}

	out := make([]modelEntry, 0, len(list))
	for _, m := range list {
		id := m.ID
		if id == "" {
			continue
		}
		if !elevenLabsModelMatches(m, kind) {
			continue
		}
		label := id
		if m.Name != "" {
			label = m.Name
		} else if m.DisplayName != "" {
			label = m.DisplayName
		}
		limit := m.MaxCharactersTotal
		if limit == 0 {
			limit = m.MaxCharactersPaid
		}
		if limit == 0 {
			limit = m.MaxCharacters
		}
		out = append(out, modelEntry{
			ID:              id,
			Label:           label,
			PromptCharLimit: limit,
		})
	}
	if len(out) == 0 {
		return elevenLabsDefaultModels(kind)
	}
	return out
}

func elevenLabsModelMatches(m struct {
	ID                 string `json:"model_id"`
	Name               string `json:"name"`
	DisplayName        string `json:"display_name"`
	CanDoTextToSpeech  bool   `json:"can_do_text_to_speech"`
	CanDoSoundEffects  bool   `json:"can_do_sound_effects"`
	CanDoMusic         bool   `json:"can_do_music"`
	CanDoVoice         bool   `json:"can_be_used_for_voice_cloning"`
	MaxCharacters      int    `json:"max_characters_request_free_user"`
	MaxCharactersPaid  int    `json:"max_characters_request_subscribed_user"`
	MaxCharactersTotal int    `json:"maximum_text_length_per_request"`
}, kind string) bool {
	id := strings.ToLower(m.ID)
	switch kind {
	case KindAudioTTS:
		return m.CanDoTextToSpeech || (strings.HasPrefix(id, "eleven_") &&
			!strings.Contains(id, "sound") && !strings.Contains(id, "music"))
	case KindAudioSFX:
		return m.CanDoSoundEffects || strings.Contains(id, "sound")
	case KindMusic:
		return m.CanDoMusic || strings.Contains(id, "music")
	}
	return false
}

func elevenLabsDefaultModels(kind string) []modelEntry {
	switch kind {
	case KindAudioTTS:
		return []modelEntry{
			{ID: "eleven_multilingual_v2", Label: "eleven_multilingual_v2"},
			{ID: "eleven_flash_v2_5", Label: "eleven_flash_v2_5"},
			{ID: "eleven_turbo_v2_5", Label: "eleven_turbo_v2_5"},
			{ID: "eleven_v3", Label: "eleven_v3"},
		}
	case KindAudioSFX:
		return []modelEntry{{ID: "eleven_text_to_sound_v2", Label: "eleven_text_to_sound_v2"}}
	case KindMusic:
		return []modelEntry{{ID: "music_v1", Label: "music_v1"}}
	}
	return nil
}

func fishAudioDefaultModels() []modelEntry {
	return []modelEntry{
		{ID: "s2.1-pro", Label: "S2.1 Pro"},
		{ID: "s2.1-pro-free", Label: "S2.1 Pro Free"},
		{ID: "s2-pro", Label: "S2 Pro"},
		{ID: "s1", Label: "S1"},
	}
}

func deepgramDefaultModels() []modelEntry {
	ids := []string{
		"aura-2-thalia-en",
		"aura-asteria-en", "aura-luna-en", "aura-stella-en", "aura-athena-en", "aura-hera-en",
		"aura-orion-en", "aura-arcas-en", "aura-perseus-en", "aura-angus-en", "aura-orpheus-en",
		"aura-helios-en", "aura-zeus-en", "aura-sirio-es", "aura-nestor-es",
	}
	out := make([]modelEntry, 0, len(ids))
	for _, id := range ids {
		out = append(out, modelEntry{ID: id, Label: id, PromptCharLimit: 2000})
	}
	return out
}

func cartesiaDefaultModels() []modelEntry {
	return []modelEntry{
		{ID: "sonic-3.5", Label: "Sonic 3.5"},
		{ID: "sonic-3", Label: "Sonic 3"},
		{ID: "sonic-latest", Label: "Sonic Latest"},
	}
}

func miniMaxDefaultModels() []modelEntry {
	return []modelEntry{
		{ID: "speech-2.8-hd", Label: "Speech 2.8 HD", PromptCharLimit: 10000},
		{ID: "speech-2.8-turbo", Label: "Speech 2.8 Turbo", PromptCharLimit: 10000},
		{ID: "speech-2.6-hd", Label: "Speech 2.6 HD", PromptCharLimit: 10000},
		{ID: "speech-2.6-turbo", Label: "Speech 2.6 Turbo", PromptCharLimit: 10000},
		{ID: "speech-02-hd", Label: "Speech 02 HD", PromptCharLimit: 10000},
		{ID: "speech-02-turbo", Label: "Speech 02 Turbo", PromptCharLimit: 10000},
		{ID: "speech-01-hd", Label: "Speech 01 HD", PromptCharLimit: 10000},
		{ID: "speech-01-turbo", Label: "Speech 01 Turbo", PromptCharLimit: 10000},
	}
}

// buildModelEntryFromVeniceSpec parses a Venice model object into the
// uniform modelEntry the panel renders. Venice mixes naming
// conventions (image models use camelCase like aspectRatios /
// defaultAspectRatio; video + inpaint use snake_case like
// aspect_ratios / model_type / durations), so we accept both via
// json struct tags and pick whichever has values.
func buildModelEntryFromVeniceSpec(id string, raw json.RawMessage, veniceType string) modelEntry {
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
				PromptLimitSnake  int      `json:"prompt_character_limit"`
				Steps             struct {
					Default int `json:"default"`
					Max     int `json:"max"`
				} `json:"steps"`
			} `json:"constraints"`
			Pricing struct {
				Generation *struct {
					USD float64 `json:"usd"`
				} `json:"generation,omitempty"`
				Inpaint *struct {
					USD float64 `json:"usd"`
				} `json:"inpaint,omitempty"`
				Resolutions map[string]struct {
					USD float64 `json:"usd"`
				} `json:"resolutions,omitempty"`
			} `json:"pricing"`
		} `json:"model_spec"`
	}
	_ = json.Unmarshal(raw, &spec)

	c := spec.ModelSpec.Constraints
	if c.PromptCharLimit == 0 {
		c.PromptCharLimit = c.PromptLimitSnake
	}
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
	supportsEdit := isVeniceEditModel(id)
	maxSourceImages := veniceMaxSourceImages(id)

	entry := modelEntry{
		ID:                   id,
		Label:                id, // panel re-styles labels with price/model type
		ModelType:            c.ModelType,
		AspectRatios:         aspects,
		DefaultAspectRatio:   c.DefaultAspect,
		Resolutions:          c.Resolutions,
		DefaultResolution:    c.DefaultResolution,
		Durations:            c.Durations,
		SupportsImageToVideo: supportsImg2Vid,
		SupportsImageEdit:    supportsEdit,
		MaxSourceImages:      maxSourceImages,
		AudioConfigurable:    c.AudioConfigurable,
		StepsDefault:         c.Steps.Default,
		StepsMax:             c.Steps.Max,
		PromptCharLimit:      c.PromptCharLimit,
		PriceUSD:             price,
	}
	if veniceType == "image" {
		entry.SizeModes = imageSizeModes(entry)
	}
	return entry
}

func imageSizeModes(entry modelEntry) []string {
	out := []string{}
	if len(entry.PixelSizes) > 0 {
		out = append(out, "pixel")
	}
	if len(entry.AspectRatios) > 0 {
		out = append(out, "aspect")
	}
	if len(entry.Resolutions) > 0 {
		out = append(out, "resolution")
	}
	// Venice pixel-sized image models publish no pixel-size enum; they
	// accept width/height directly. Keep that visible to UIs and agents.
	if len(out) == 0 {
		out = append(out, "pixel")
	}
	return out
}

func openAIImagePixelSizes(id string) []string {
	switch id {
	case "gpt-image-2":
		return []string{"1024x1024", "1024x1536", "1536x1024", "2048x2048", "3840x2160"}
	case "gpt-image-1.5", "gpt-image-1", "gpt-image-1-mini":
		return []string{"1024x1024", "1024x1536", "1536x1024"}
	case "dall-e-3":
		return []string{"1024x1024", "1792x1024", "1024x1792"}
	case "dall-e-2":
		return []string{"256x256", "512x512", "1024x1024"}
	default:
		if strings.HasPrefix(id, "gpt-image") {
			return []string{"1024x1024", "1024x1536", "1536x1024"}
		}
		return nil
	}
}

func isVeniceEditModel(id string) bool {
	return strings.HasSuffix(strings.ToLower(id), "-edit")
}

func veniceMaxSourceImages(id string) int {
	lower := strings.ToLower(id)
	if isVeniceEditModel(id) {
		return 3
	}
	if strings.Contains(lower, "reference-to-video") {
		return veniceReferenceProfile(id).MaxImages
	}
	if strings.Contains(lower, "image-to-video") {
		return 1
	}
	return 0
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
//
//	generate (pixel models):       {"pricing":{"generation":{"usd":0.01}}}
//	generate (resolution tier):    {"pricing":{"resolutions":{"1K":{"usd":0.08}, "2K":{"usd":0.10}}}}
//	edit (Venice's "inpaint"):     {"pricing":{"inpaint":{"usd":0.04}}}
//
// Returns (0, false) when the spec lacks a price for the capability.
func computeVeniceImageCost(specRaw json.RawMessage, capability string, args map[string]any) (float64, bool) {
	var spec struct {
		ModelSpec struct {
			Pricing struct {
				Generation *struct {
					USD float64 `json:"usd"`
				} `json:"generation,omitempty"`
				Inpaint *struct {
					USD float64 `json:"usd"`
				} `json:"inpaint,omitempty"`
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
	ctx, _, err := projectContextFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind == "" {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	capability := r.URL.Query().Get("capability")
	if _, ok := handlers[kind]; !ok {
		http.Error(w, "unknown kind", http.StatusBadRequest)
		return
	}
	if capability != "" && !(kind == KindImage && (capability == "image.generate" || capability == "image.edit")) {
		http.Error(w, "unsupported capability", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("refresh") == "1" {
		for _, bound := range boundIntegrationsFor(ctx, handlers[kind].Role) {
			if bound != nil {
				invalidateModelCacheForConnection(bound.ConnectionID)
			}
		}
	}
	resp, err := modelCatalogForKind(ctx, kind, capability)
	if capability != "" {
		resp["capability"] = capability
	}
	if err != nil {
		resp["error"] = err.Error()
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
