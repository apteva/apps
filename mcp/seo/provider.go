package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// providerAdapter is the only provider-specific boundary used by the app.
// Storage, MCP tools, and UI routes work with these normalized operations.
type providerAdapter interface {
	Slug() string
	ConnectionID() int64
	SyncLocations(*sdk.AppCtx) (any, error)
	RefreshDomain(*sdk.AppCtx, *Domain, *SEOLocation) (any, error)
	RefreshKeyword(*sdk.AppCtx, *Keyword, *SEOLocation) (any, error)
	RefreshBacklinks(*sdk.AppCtx, *Domain) (any, error)
	SERPSearch(*sdk.AppCtx, string, string, *SEOLocation, int, string) (*providerSERPResponse, error)
	KeywordIdeas(*sdk.AppCtx, []string, *SEOLocation, int) (*providerKeywordIdeasResponse, error)
}

type providerSERPResponse struct {
	Tool      string
	ResultRaw []byte
	Raw       []byte
}

type providerKeywordIdeasResponse struct {
	Tool  string
	Items []map[string]any
	Raw   []byte
}

type providerRequestError struct {
	Provider     string
	Status       int
	HTTPStatus   int
	ProviderCode int
	RetryAfter   int
	Message      string
}

func (e *providerRequestError) Error() string {
	provider := e.Provider
	if provider == "" {
		provider = "SEO provider"
	}
	if e.ProviderCode != 0 {
		return fmt.Sprintf("%s: task status %d: %s", provider, e.ProviderCode, e.Message)
	}
	status := e.HTTPStatus
	if status == 0 {
		status = e.Status
	}
	if status > 0 {
		return fmt.Sprintf("%s: HTTP %d: %s", provider, status, e.Message)
	}
	return fmt.Sprintf("%s: %s", provider, e.Message)
}

func providerHTTPStatus(err error) int {
	var requestErr *providerRequestError
	if errors.As(err, &requestErr) {
		status := requestErr.HTTPStatus
		if status == 0 {
			status = requestErr.Status
		}
		switch status {
		case http.StatusPaymentRequired:
			return http.StatusPaymentRequired
		case http.StatusTooManyRequests:
			return http.StatusTooManyRequests
		case http.StatusBadRequest:
			return http.StatusBadRequest
		case http.StatusUnauthorized, http.StatusForbidden:
			return http.StatusBadGateway
		}
		if status >= http.StatusInternalServerError && status < 600 {
			return status
		}
	}
	return 0
}

func retryableProviderServerError(err error) bool {
	var requestErr *providerRequestError
	if !errors.As(err, &requestErr) {
		return false
	}
	status := requestErr.HTTPStatus
	if status == 0 {
		status = requestErr.Status
	}
	return status >= http.StatusInternalServerError && status < 600
}

type dataForSEOProvider struct{ connID int64 }

func (p *dataForSEOProvider) Slug() string        { return "dataforseo" }
func (p *dataForSEOProvider) ConnectionID() int64 { return p.connID }
func (p *dataForSEOProvider) SyncLocations(ctx *sdk.AppCtx) (any, error) {
	return syncDataForSEOLocations(ctx, p.connID)
}
func (p *dataForSEOProvider) RefreshDomain(ctx *sdk.AppCtx, d *Domain, loc *SEOLocation) (any, error) {
	return refreshDomainViaDataForSEO(ctx, p.connID, d, loc)
}
func (p *dataForSEOProvider) RefreshKeyword(ctx *sdk.AppCtx, k *Keyword, loc *SEOLocation) (any, error) {
	return refreshKeywordViaDataForSEO(ctx, p.connID, k, loc)
}
func (p *dataForSEOProvider) RefreshBacklinks(ctx *sdk.AppCtx, d *Domain) (any, error) {
	return refreshBacklinksViaDataForSEO(ctx, p.connID, d)
}
func (p *dataForSEOProvider) SERPSearch(ctx *sdk.AppCtx, engine, keyword string, loc *SEOLocation, depth int, device string) (*providerSERPResponse, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("dataforseo SERP search requires a location with location_code")
	}
	if device == "" {
		device = "desktop"
	}
	input := map[string]any{
		"keyword":       keyword,
		"location_code": *loc.LocationCode,
		"language_code": strings.ToLower(loc.LanguageCode),
		"device":        device,
	}
	tool := "serp_organic"
	if engine == "youtube" {
		tool = "youtube_organic_serp"
		input["block_depth"] = clampInt(depth, 1, 200)
	} else {
		input["depth"] = depth
	}
	rowRaw, taskRaw, err := callDfs(ctx, p.connID, tool, input)
	if err != nil {
		return nil, err
	}
	return &providerSERPResponse{Tool: tool, ResultRaw: rowRaw, Raw: taskRaw}, nil
}
func (p *dataForSEOProvider) KeywordIdeas(ctx *sdk.AppCtx, seeds []string, loc *SEOLocation, limit int) (*providerKeywordIdeasResponse, error) {
	if loc == nil || loc.LocationCode == nil {
		return nil, fmt.Errorf("dataforseo keyword ideas require a location with location_code")
	}
	rowRaw, taskRaw, err := callDfs(ctx, p.connID, "keyword_ideas", map[string]any{
		"keywords":             seeds,
		"location_code":        *loc.LocationCode,
		"language_code":        strings.ToLower(loc.LanguageCode),
		"include_seed_keyword": true,
		"limit":                limit,
	})
	if err != nil {
		return nil, err
	}
	items, err := decodeSERPItems(rowRaw)
	if err != nil {
		return nil, err
	}
	normalized := make([]map[string]any, 0, len(items))
	for _, item := range items {
		idea := normalizeProviderKeywordIdea(item, firstSeed(seeds))
		if idea != nil {
			normalized = append(normalized, idea)
		}
	}
	return &providerKeywordIdeasResponse{Tool: "keyword_ideas", Items: normalized, Raw: taskRaw}, nil
}

func firstSeed(seeds []string) string {
	if len(seeds) == 0 {
		return ""
	}
	return seeds[0]
}

func normalizeProviderKeywordIdea(raw map[string]any, source string) map[string]any {
	keyword := normaliseKeyword(firstString(raw, "keyword"))
	volume := numberAny(raw["volume"])
	difficulty := numberAny(raw["difficulty"])
	cpc := numberAny(raw["cpc"])
	intent := firstString(raw, "intent")
	features := raw["serpFeatures"]
	if nested, ok := raw["keyword_data"].(map[string]any); ok {
		if keyword == "" {
			keyword = normaliseKeyword(firstString(nested, "keyword"))
		}
		if info, ok := nested["keyword_info"].(map[string]any); ok {
			if volume == nil {
				volume = numberAny(info["search_volume"])
			}
			if cpc == nil {
				cpc = numberAny(info["cpc"])
			}
		}
		if props, ok := nested["keyword_properties"].(map[string]any); ok && difficulty == nil {
			difficulty = numberAny(props["keyword_difficulty"])
		}
		if intentInfo, ok := nested["search_intent_info"].(map[string]any); ok && intent == "" {
			intent = firstString(intentInfo, "main_intent")
		}
		if features == nil {
			if serpInfo, ok := nested["serp_info"].(map[string]any); ok {
				features = serpInfo["serp_item_types"]
			}
		}
	}
	if keyword == "" {
		return nil
	}
	out := map[string]any{"keyword": keyword, "source_keyword": source}
	if volume != nil {
		out["volume"] = volume
	}
	if difficulty != nil {
		out["difficulty"] = difficulty
	}
	if cpc != nil {
		out["cpc"] = cpc
	}
	if intent != "" {
		out["intent"] = intent
	}
	if features != nil {
		out["serp_features"] = features
	}
	return out
}

func numberAny(value any) any {
	switch value.(type) {
	case float64, float32, int, int32, int64, json.Number:
		return value
	default:
		return nil
	}
}

func providerFromBinding(slug string, connID int64) (providerAdapter, error) {
	switch strings.ToLower(strings.TrimSpace(slug)) {
	case "dataforseo":
		return &dataForSEOProvider{connID: connID}, nil
	case "yepapi":
		return &yepAPIProvider{connID: connID}, nil
	default:
		return nil, fmt.Errorf("SEO provider %q is not supported; bind DataForSEO or YepAPI", slug)
	}
}

func boundProviders(ctx *sdk.AppCtx) ([]providerAdapter, error) {
	bindings := ctx.IntegrationsFor(providerRole)
	if len(bindings) == 0 {
		return nil, errProviderUnbound
	}
	providers := make([]providerAdapter, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || binding.ConnectionID <= 0 {
			continue
		}
		slug := strings.ToLower(strings.TrimSpace(binding.AppSlug))
		if slug == "" {
			conn, err := ctx.PlatformAPI().GetConnection(binding.ConnectionID)
			if err != nil || conn == nil {
				continue
			}
			slug = strings.ToLower(strings.TrimSpace(conn.AppSlug))
		}
		provider, err := providerFromBinding(slug, binding.ConnectionID)
		if err != nil {
			continue
		}
		providers = append(providers, provider)
	}
	if len(providers) == 0 {
		return nil, errProviderUnbound
	}
	return providers, nil
}

func selectProvider(ctx *sdk.AppCtx, requested string) (providerAdapter, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		binding := ctx.IntegrationFor(providerRole)
		if binding == nil {
			return nil, errProviderUnbound
		}
		slug := strings.ToLower(strings.TrimSpace(binding.AppSlug))
		if slug == "" {
			conn, err := ctx.PlatformAPI().GetConnection(binding.ConnectionID)
			if err != nil || conn == nil {
				return nil, fmt.Errorf("resolve default SEO provider: %w", err)
			}
			slug = strings.ToLower(strings.TrimSpace(conn.AppSlug))
		}
		return providerFromBinding(slug, binding.ConnectionID)
	}
	providers, err := boundProviders(ctx)
	if err != nil {
		return nil, err
	}
	for _, provider := range providers {
		if provider.Slug() == requested {
			return provider, nil
		}
	}
	return nil, fmt.Errorf("SEO provider %q is not bound to this install", requested)
}

// cachedProviderFromArgs applies the configured default to provider-sensitive
// reads. Explicit "all" keeps cross-provider inspection available, and an
// install with no current binding may still inspect its existing cache.
func cachedProviderFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	requested := strings.ToLower(strings.TrimSpace(strArg(args, "provider", "")))
	if requested == "all" {
		return "", nil
	}
	if requested != "" {
		switch requested {
		case "dataforseo", "yepapi":
			return requested, nil
		default:
			return "", &requestValidationError{Message: fmt.Sprintf("SEO provider %q is not supported; use dataforseo, yepapi, or all", requested)}
		}
	}
	provider, err := selectProvider(ctx, "")
	if errors.Is(err, errProviderUnbound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return provider.Slug(), nil
}

func providersStatus(ctx *sdk.AppCtx) (map[string]any, error) {
	providers, err := boundProviders(ctx)
	if err != nil {
		if errors.Is(err, errProviderUnbound) {
			return map[string]any{"default": "", "providers": []any{}}, nil
		}
		return nil, err
	}
	defaultSlug := ""
	if binding := ctx.IntegrationFor(providerRole); binding != nil {
		defaultSlug = strings.ToLower(strings.TrimSpace(binding.AppSlug))
		if defaultSlug == "" {
			for _, provider := range providers {
				if provider.ConnectionID() == binding.ConnectionID {
					defaultSlug = provider.Slug()
					break
				}
			}
		}
	}
	slugs := make([]string, 0, len(providers))
	for _, provider := range providers {
		slugs = append(slugs, provider.Slug())
	}
	sort.Strings(slugs)
	if defaultSlug == "" && len(slugs) > 0 {
		defaultSlug = slugs[0]
	}
	return map[string]any{"default": defaultSlug, "providers": slugs}, nil
}

func validateProviderLocation(provider providerAdapter, loc *SEOLocation) error {
	if loc == nil {
		return errors.New("SEO location is required")
	}
	if loc.Provider != provider.Slug() {
		return fmt.Errorf("location %d belongs to provider %s, not %s", loc.ID, loc.Provider, provider.Slug())
	}
	return nil
}

func syncLocations(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	provider, err := selectProvider(ctx, strArg(args, "provider", ""))
	if err != nil {
		return nil, err
	}
	return provider.SyncLocations(ctx)
}
