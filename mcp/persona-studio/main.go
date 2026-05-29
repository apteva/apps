// Persona Studio owns reusable virtual identities, their references,
// items/products, style profiles, campaign context, generated assets,
// and handoffs to Media Studio and Composer.
package main

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestFS embed.FS

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	raw, err := manifestFS.ReadFile("apteva.yaml")
	if err != nil {
		panic("missing embedded manifest: " + err.Error())
	}
	m, err := sdk.ParseManifest(raw)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("persona-studio requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("persona-studio mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/personas", Handler: a.handlePersonas},
		{Pattern: "/personas/", Handler: a.handlePersonaByID},
		{Pattern: "/style-profiles", Handler: a.handleStyleProfiles},
		{Pattern: "/references", Handler: a.handleReferences},
		{Pattern: "/items", Handler: a.handleItems},
		{Pattern: "/items/", Handler: a.handleItemByID},
		{Pattern: "/campaigns", Handler: a.handleCampaigns},
		{Pattern: "/assets", Handler: a.handleAssets},
		{Pattern: "/generate", Handler: a.handleGenerate},
		{Pattern: "/clip-plan", Handler: a.handleClipPlan},
		{Pattern: "/composition", Handler: a.handleComposition},
		{Pattern: "/storage-files", Handler: a.handleStorageFiles},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "persona_create", Description: "Create a virtual persona. Args: name, handle?, bio?, audience?, personality?, tone?, visual_style?, negative_style?, brand_rules?, default_voice_id?, default_avatar_id?, default_*_provider?.", InputSchema: schemaObject(map[string]any{"name": sString(), "handle": sString(), "bio": sString(), "audience": sString(), "personality": sString(), "tone": sString(), "visual_style": sString(), "negative_style": sString(), "brand_rules": sObject(), "default_voice_id": sString(), "default_avatar_id": sString(), "default_image_provider": sString(), "default_video_provider": sString(), "default_audio_provider": sString(), "default_music_provider": sString(), "default_avatar_provider": sString()}, []string{"name"}), Handler: a.toolPersonaCreate},
		{Name: "persona_update", Description: "Update a persona. Args: id, patch object with persona fields.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "patch": sObject()}, []string{"id", "patch"}), Handler: a.toolPersonaUpdate},
		{Name: "persona_get", Description: "Fetch one persona with styles, references, items, campaigns, and recent assets. Args: id.", InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}), Handler: a.toolPersonaGet},
		{Name: "persona_list", Description: "List personas. Args: include_archived?, limit?.", InputSchema: schemaObject(map[string]any{"include_archived": sBool(), "limit": sInteger()}, nil), Handler: a.toolPersonaList},
		{Name: "persona_style_profile_upsert", Description: "Create or update a style profile. Args: persona_id, id?, name, asset_type, prompt_prefix?, prompt_suffix?, negative_prompt?, provider_settings?, composition_settings?, is_default?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "id": sInteger(), "name": sString(), "asset_type": sString(), "prompt_prefix": sString(), "prompt_suffix": sString(), "negative_prompt": sString(), "provider_settings": sObject(), "composition_settings": sObject(), "is_default": sBool()}, []string{"persona_id", "name", "asset_type"}), Handler: a.toolStyleProfileUpsert},
		{Name: "persona_reference_add", Description: "Link a Storage file as a persona reference. Args: persona_id, storage_file_id, kind?, label?, weight?, notes?, active?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "storage_file_id": sInteger(), "kind": sString(), "label": sString(), "weight": map[string]any{"type": "number"}, "notes": sString(), "active": sBool()}, []string{"persona_id", "storage_file_id"}), Handler: a.toolReferenceAdd},
		{Name: "persona_reference_list", Description: "List references. Args: persona_id, kind?, active?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "kind": sString(), "active": sBool()}, []string{"persona_id"}), Handler: a.toolReferenceList},
		{Name: "persona_item_create", Description: "Create a reusable item/product/prop/location/brand asset. Args: persona_id, name, kind?, description?, usage_rules?, visual_rules?, storage_file_ids?, metadata?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "name": sString(), "kind": sString(), "description": sString(), "usage_rules": sString(), "visual_rules": sString(), "storage_file_ids": sArray("integer"), "metadata": sObject()}, []string{"persona_id", "name"}), Handler: a.toolItemCreate},
		{Name: "persona_item_update", Description: "Update a reusable item. Args: id, patch object.", InputSchema: schemaObject(map[string]any{"id": sInteger(), "patch": sObject()}, []string{"id", "patch"}), Handler: a.toolItemUpdate},
		{Name: "persona_item_list", Description: "List items. Args: persona_id, kind?, active?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "kind": sString(), "active": sBool()}, []string{"persona_id"}), Handler: a.toolItemList},
		{Name: "persona_generate_asset", Description: "Generate one asset via Media Studio. Args: persona_id, asset_type (image|video|audio_tts|audio_sfx|music|avatar), prompt, style_profile_id?, item_ids?, reference_kinds?, campaign_id?, use_cache?, settings? ({model, size, aspect, duration, quality, source_images, voice, avatar, options}). Image generation automatically sends linked persona/item references via source_images up to the selected model's limit.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "asset_type": sString(), "prompt": sString(), "style_profile_id": sInteger(), "item_ids": sArray("integer"), "reference_kinds": sArray("string"), "campaign_id": sInteger(), "use_cache": sBool(), "settings": sObject()}, []string{"persona_id", "asset_type", "prompt"}), Handler: a.toolGenerateAsset},
		{Name: "persona_generate_pack", Description: "Generate a starter pack. Args: persona_id, campaign_id?, prompts? object, use_cache?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "campaign_id": sInteger(), "prompts": sObject(), "use_cache": sBool()}, []string{"persona_id"}), Handler: a.toolGeneratePack},
		{Name: "persona_campaign_create", Description: "Create a campaign. Args: persona_id, name, brief?, platforms?, content_pillars?, status?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "name": sString(), "brief": sString(), "platforms": sArray("string"), "content_pillars": sArray("string"), "status": sString()}, []string{"persona_id", "name"}), Handler: a.toolCampaignCreate},
		{Name: "persona_create_clip_plan", Description: "Create a structured clip plan. Args: persona_id, brief, campaign_id?, asset_ids?, aspect?, duration_ms?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "brief": sString(), "campaign_id": sInteger(), "asset_ids": sArray("integer"), "aspect": sString(), "duration_ms": sInteger()}, []string{"persona_id", "brief"}), Handler: a.toolCreateClipPlan},
		{Name: "persona_create_composition", Description: "Create a Composer composition from asset ids or a clip plan. Args: persona_id, title?, campaign_id?, asset_ids?, plan?, aspect?, duration_ms?.", InputSchema: schemaObject(map[string]any{"persona_id": sInteger(), "title": sString(), "campaign_id": sInteger(), "asset_ids": sArray("integer"), "plan": sObject(), "aspect": sString(), "duration_ms": sInteger()}, []string{"persona_id"}), Handler: a.toolCreateComposition},
	}
}

type Persona struct {
	ID                    int64           `json:"id"`
	ProjectID             string          `json:"project_id"`
	Name                  string          `json:"name"`
	Handle                string          `json:"handle"`
	Bio                   string          `json:"bio"`
	Audience              string          `json:"audience"`
	Personality           string          `json:"personality"`
	Tone                  string          `json:"tone"`
	VisualStyle           string          `json:"visual_style"`
	NegativeStyle         string          `json:"negative_style"`
	BrandRules            json.RawMessage `json:"brand_rules"`
	DefaultVoiceID        string          `json:"default_voice_id"`
	DefaultAvatarID       string          `json:"default_avatar_id"`
	DefaultImageProvider  string          `json:"default_image_provider"`
	DefaultVideoProvider  string          `json:"default_video_provider"`
	DefaultAudioProvider  string          `json:"default_audio_provider"`
	DefaultMusicProvider  string          `json:"default_music_provider"`
	DefaultAvatarProvider string          `json:"default_avatar_provider"`
	ArchivedAt            string          `json:"archived_at,omitempty"`
	CreatedAt             string          `json:"created_at"`
	UpdatedAt             string          `json:"updated_at"`
}

type StyleProfile struct {
	ID                  int64           `json:"id"`
	ProjectID           string          `json:"project_id"`
	PersonaID           int64           `json:"persona_id"`
	Name                string          `json:"name"`
	AssetType           string          `json:"asset_type"`
	PromptPrefix        string          `json:"prompt_prefix"`
	PromptSuffix        string          `json:"prompt_suffix"`
	NegativePrompt      string          `json:"negative_prompt"`
	ProviderSettings    json.RawMessage `json:"provider_settings"`
	CompositionSettings json.RawMessage `json:"composition_settings"`
	IsDefault           bool            `json:"is_default"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

type Reference struct {
	ID            int64   `json:"id"`
	ProjectID     string  `json:"project_id"`
	PersonaID     int64   `json:"persona_id"`
	StorageFileID int64   `json:"storage_file_id"`
	Kind          string  `json:"kind"`
	Label         string  `json:"label"`
	Weight        float64 `json:"weight"`
	Notes         string  `json:"notes"`
	Active        bool    `json:"active"`
	CreatedAt     string  `json:"created_at"`
	UpdatedAt     string  `json:"updated_at"`
}

type Item struct {
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id"`
	PersonaID      int64           `json:"persona_id"`
	Name           string          `json:"name"`
	Kind           string          `json:"kind"`
	Description    string          `json:"description"`
	UsageRules     string          `json:"usage_rules"`
	VisualRules    string          `json:"visual_rules"`
	StorageFileIDs []int64         `json:"storage_file_ids"`
	Active         bool            `json:"active"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

type Asset struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	PersonaID         int64           `json:"persona_id"`
	CampaignID        int64           `json:"campaign_id,omitempty"`
	StorageFileID     int64           `json:"storage_file_id,omitempty"`
	MediaGenerationID int64           `json:"media_generation_id,omitempty"`
	AssetType         string          `json:"asset_type"`
	Status            string          `json:"status"`
	Prompt            string          `json:"prompt"`
	ResolvedPrompt    string          `json:"resolved_prompt"`
	ProviderSlug      string          `json:"provider_slug"`
	ProviderModel     string          `json:"provider_model"`
	Settings          json.RawMessage `json:"settings"`
	ReferenceIDs      []int64         `json:"reference_ids"`
	ItemIDs           []int64         `json:"item_ids"`
	CacheKey          string          `json:"cache_key"`
	Error             string          `json:"error"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
}

// HTTP handlers

func (a *App) handlePersonas(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.listPersonas(ctx, pid, false, intQuery(r, "limit", 100))
		writeOrErr(w, map[string]any{"personas": out}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
		v, err := a.toolPersonaCreate(ctx, args)
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handlePersonaByID(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	id, err := idFromPath(r.URL.Path, "/personas/")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		v, err := a.personaBundle(ctx, pid, id)
		writeOrErr(w, v, err)
	case http.MethodPatch, http.MethodPut:
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		v, err := a.toolPersonaUpdate(ctx, map[string]any{"_project_id": pid, "id": id, "patch": patch})
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleStyleProfiles(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		personaID := int64Query(r, "persona_id", 0)
		if personaID == 0 {
			httpErr(w, 400, "persona_id required")
			return
		}
		out, err := listStyleProfiles(ctx.AppDB(), pid, personaID, "")
		writeOrErr(w, map[string]any{"style_profiles": out}, err)
	case http.MethodPost, http.MethodPut:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
		v, err := a.toolStyleProfileUpsert(ctx, args)
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleReferences(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		personaID := int64Query(r, "persona_id", 0)
		out, err := listReferences(ctx.AppDB(), pid, personaID, r.URL.Query().Get("kind"), true)
		writeOrErr(w, map[string]any{"references": out}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
		v, err := a.toolReferenceAdd(ctx, args)
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleItems(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		personaID := int64Query(r, "persona_id", 0)
		out, err := listItems(ctx.AppDB(), pid, personaID, r.URL.Query().Get("kind"), true)
		writeOrErr(w, map[string]any{"items": out}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
		v, err := a.toolItemCreate(ctx, args)
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleItemByID(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	id, err := idFromPath(r.URL.Path, "/items/")
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var patch map[string]any
	if err := readJSON(r, &patch); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	v, err := a.toolItemUpdate(ctx, map[string]any{"_project_id": pid, "id": id, "patch": patch})
	writeOrErr(w, v, err)
}

func (a *App) handleCampaigns(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := listCampaigns(ctx.AppDB(), pid, int64Query(r, "persona_id", 0))
		writeOrErr(w, map[string]any{"campaigns": out}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, 400, err.Error())
			return
		}
		args["_project_id"] = pid
		v, err := a.toolCampaignCreate(ctx, args)
		writeOrErr(w, v, err)
	default:
		httpErr(w, 405, "method not allowed")
	}
}

func (a *App) handleAssets(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	out, err := listAssets(ctx.AppDB(), pid, int64Query(r, "persona_id", 0), intQuery(r, "limit", 100))
	writeOrErr(w, map[string]any{"assets": out}, err)
}

func (a *App) handleGenerate(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	var args map[string]any
	if err := readJSON(r, &args); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args["_project_id"] = pid
	v, err := a.toolGenerateAsset(ctx, args)
	writeOrErr(w, v, err)
}

func (a *App) handleClipPlan(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	var args map[string]any
	if err := readJSON(r, &args); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args["_project_id"] = pid
	v, err := a.toolCreateClipPlan(ctx, args)
	writeOrErr(w, v, err)
}

func (a *App) handleComposition(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	var args map[string]any
	if err := readJSON(r, &args); err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	args["_project_id"] = pid
	v, err := a.toolCreateComposition(ctx, args)
	writeOrErr(w, v, err)
}

func (a *App) handleStorageFiles(w http.ResponseWriter, r *http.Request) {
	ctx, pid, ok := requestCtx(w, r)
	if !ok {
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "/"
	}
	args := map[string]any{"folder": folder, "recursive": boolQuery(r, "recursive", false), "_project_id": pid}
	if limit := intQuery(r, "limit", 200); limit > 0 {
		if limit > 500 {
			limit = 500
		}
		args["limit"] = limit
	}
	tool := "files_list"
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args["q"] = q
		tool = "files_search"
	}
	var out map[string]any
	err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", tool, args, &out)
	filterStorageBrowserOutput(out)
	writeOrErr(w, out, err)
}

// MCP tool handlers

func (a *App) toolPersonaCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	brandRules := jsonArg(args, "brand_rules", "{}")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO personas (
			project_id, name, handle, bio, audience, personality, tone, visual_style, negative_style,
			brand_rules_json, default_voice_id, default_avatar_id, default_image_provider,
			default_video_provider, default_audio_provider, default_music_provider, default_avatar_provider
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, strArg(args, "handle"), strArg(args, "bio"), strArg(args, "audience"),
		strArg(args, "personality"), strArg(args, "tone"), strArg(args, "visual_style"),
		strArg(args, "negative_style"), brandRules, strArg(args, "default_voice_id"),
		strArg(args, "default_avatar_id"), strArg(args, "default_image_provider"),
		strArg(args, "default_video_provider"), strArg(args, "default_audio_provider"),
		strArg(args, "default_music_provider"), strArg(args, "default_avatar_provider"),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	p, err := getPersona(ctx.AppDB(), pid, id)
	if err == nil {
		_ = ensureDefaultStyles(ctx.AppDB(), pid, id)
	}
	return map[string]any{"persona": p}, err
}

func (a *App) toolPersonaUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	if _, err := getPersona(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	allowed := map[string]string{
		"name": "name", "handle": "handle", "bio": "bio", "audience": "audience", "personality": "personality",
		"tone": "tone", "visual_style": "visual_style", "negative_style": "negative_style",
		"default_voice_id": "default_voice_id", "default_avatar_id": "default_avatar_id",
		"default_image_provider": "default_image_provider", "default_video_provider": "default_video_provider",
		"default_audio_provider": "default_audio_provider", "default_music_provider": "default_music_provider",
		"default_avatar_provider": "default_avatar_provider", "archived_at": "archived_at",
	}
	sets, vals := []string{}, []any{}
	for k, col := range allowed {
		if v, ok := patch[k]; ok {
			sets = append(sets, col+"=?")
			vals = append(vals, cleanString(v))
		}
	}
	if v, ok := patch["brand_rules"]; ok {
		sets = append(sets, "brand_rules_json=?")
		vals = append(vals, mustJSON(v, "{}"))
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
		vals = append(vals, id, pid)
		_, err = ctx.AppDB().Exec(`UPDATE personas SET `+strings.Join(sets, ", ")+` WHERE id=? AND project_id=?`, vals...)
		if err != nil {
			return nil, err
		}
	}
	p, err := getPersona(ctx.AppDB(), pid, id)
	return map[string]any{"persona": p}, err
}

func (a *App) toolPersonaGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	return a.personaBundle(ctx, pid, int64Arg(args, "id"))
}

func (a *App) toolPersonaList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := a.listPersonas(ctx, pid, boolArg(args, "include_archived"), intArg(args, "limit", 100))
	return map[string]any{"personas": out}, err
}

func (a *App) toolStyleProfileUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	name, assetType := strArg(args, "name"), normalizeAssetType(strArg(args, "asset_type"))
	if name == "" || assetType == "" {
		return nil, errors.New("name and asset_type required")
	}
	isDefault := boolArg(args, "is_default")
	if isDefault {
		_, _ = ctx.AppDB().Exec(`UPDATE persona_style_profiles SET is_default=0 WHERE project_id=? AND persona_id=? AND asset_type=?`, pid, personaID, assetType)
	}
	id := int64Arg(args, "id")
	if id > 0 {
		_, err = ctx.AppDB().Exec(
			`UPDATE persona_style_profiles
			 SET name=?, asset_type=?, prompt_prefix=?, prompt_suffix=?, negative_prompt=?,
			     provider_settings_json=?, composition_settings_json=?, is_default=?, updated_at=CURRENT_TIMESTAMP
			 WHERE id=? AND project_id=? AND persona_id=?`,
			name, assetType, strArg(args, "prompt_prefix"), strArg(args, "prompt_suffix"), strArg(args, "negative_prompt"),
			jsonArg(args, "provider_settings", "{}"), jsonArg(args, "composition_settings", "{}"), boolInt(isDefault), id, pid, personaID,
		)
	} else {
		res, e := ctx.AppDB().Exec(
			`INSERT INTO persona_style_profiles
			 (project_id, persona_id, name, asset_type, prompt_prefix, prompt_suffix, negative_prompt, provider_settings_json, composition_settings_json, is_default)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			pid, personaID, name, assetType, strArg(args, "prompt_prefix"), strArg(args, "prompt_suffix"), strArg(args, "negative_prompt"),
			jsonArg(args, "provider_settings", "{}"), jsonArg(args, "composition_settings", "{}"), boolInt(isDefault),
		)
		err = e
		if err == nil {
			id, _ = res.LastInsertId()
		}
	}
	if err != nil {
		return nil, err
	}
	sp, err := getStyleProfile(ctx.AppDB(), pid, personaID, id)
	return map[string]any{"style_profile": sp}, err
}

func (a *App) toolReferenceAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	storageID := int64Arg(args, "storage_file_id")
	if storageID == 0 {
		return nil, errors.New("storage_file_id required")
	}
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "style"
	}
	active := true
	if _, ok := args["active"]; ok {
		active = boolArg(args, "active")
	}
	weight := floatArg(args, "weight", 1)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO persona_references (project_id, persona_id, storage_file_id, kind, label, weight, notes, active)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, personaID, storageID, kind, strArg(args, "label"), weight, strArg(args, "notes"), boolInt(active),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ref, err := getReference(ctx.AppDB(), pid, id)
	return map[string]any{"reference": ref}, err
}

func (a *App) toolReferenceList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := listReferences(ctx.AppDB(), pid, personaID, strArg(args, "kind"), true)
	return map[string]any{"references": out}, err
}

func (a *App) toolItemCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "product"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO persona_items
		 (project_id, persona_id, name, kind, description, usage_rules, visual_rules, storage_file_ids_json, metadata_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, personaID, name, kind, strArg(args, "description"), strArg(args, "usage_rules"), strArg(args, "visual_rules"),
		mustJSON(int64SliceArg(args, "storage_file_ids"), "[]"), jsonArg(args, "metadata", "{}"),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	item, err := getItem(ctx.AppDB(), pid, id)
	return map[string]any{"item": item}, err
}

func (a *App) toolItemUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	allowed := map[string]string{"name": "name", "kind": "kind", "description": "description", "usage_rules": "usage_rules", "visual_rules": "visual_rules"}
	sets, vals := []string{}, []any{}
	for k, col := range allowed {
		if v, ok := patch[k]; ok {
			sets = append(sets, col+"=?")
			vals = append(vals, cleanString(v))
		}
	}
	if v, ok := patch["storage_file_ids"]; ok {
		sets = append(sets, "storage_file_ids_json=?")
		vals = append(vals, mustJSON(v, "[]"))
	}
	if v, ok := patch["metadata"]; ok {
		sets = append(sets, "metadata_json=?")
		vals = append(vals, mustJSON(v, "{}"))
	}
	if _, ok := patch["active"]; ok {
		sets = append(sets, "active=?")
		vals = append(vals, boolInt(boolArg(patch, "active")))
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
		vals = append(vals, id, pid)
		if _, err := ctx.AppDB().Exec(`UPDATE persona_items SET `+strings.Join(sets, ", ")+` WHERE id=? AND project_id=?`, vals...); err != nil {
			return nil, err
		}
	}
	item, err := getItem(ctx.AppDB(), pid, id)
	return map[string]any{"item": item}, err
}

func (a *App) toolItemList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	out, err := listItems(ctx.AppDB(), pid, personaID, strArg(args, "kind"), true)
	return map[string]any{"items": out}, err
}

func (a *App) toolCampaignCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	status := strArg(args, "status")
	if status == "" {
		status = "draft"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO persona_campaigns (project_id, persona_id, name, brief, platforms_json, content_pillars_json, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, personaID, name, strArg(args, "brief"), mustJSON(stringSliceArg(args, "platforms"), "[]"), mustJSON(stringSliceArg(args, "content_pillars"), "[]"), status,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	campaign, err := getCampaign(ctx.AppDB(), pid, id)
	return map[string]any{"campaign": campaign}, err
}

func (a *App) toolGenerateAsset(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	assetType := normalizeAssetType(strArg(args, "asset_type"))
	prompt := strArg(args, "prompt")
	if assetType == "" || prompt == "" {
		return nil, errors.New("asset_type and prompt required")
	}
	persona, err := getPersona(ctx.AppDB(), pid, personaID)
	if err != nil {
		return nil, err
	}
	style, _ := selectStyleProfile(ctx.AppDB(), pid, personaID, int64Arg(args, "style_profile_id"), assetType)
	referenceKinds := stringSliceArg(args, "reference_kinds")
	refs, _ := listReferencesForKinds(ctx.AppDB(), pid, personaID, referenceKinds)
	itemIDs := int64SliceArg(args, "item_ids")
	items, _ := listItemsByIDs(ctx.AppDB(), pid, personaID, itemIDs)
	settings := cloneMap(mapArg(args, "settings"))
	if assetType == "image" {
		sourceImages := defaultImageSourceRefs(refs, items, settings)
		if len(sourceImages) > 0 {
			// Always use Media Studio's provider-neutral multi-reference path,
			// even for one source. This avoids the old single-source UI path and
			// lets Media Studio choose the correct edit tool per provider.
			delete(settings, "source_image")
			settings["source_images"] = sourceImages
			if !isImageEditModel(strArg(settings, "model")) {
				delete(settings, "model")
			}
		}
	}
	resolved := buildResolvedPrompt(persona, style, refs, items, prompt, assetType)
	cacheKey := generationCacheKey(personaID, assetType, resolved, settings, refIDs(refs), itemIDs)

	useCache := true
	if _, ok := args["use_cache"]; ok {
		useCache = boolArg(args, "use_cache")
	}
	if useCache {
		if asset, ok := cachedAsset(ctx.AppDB(), pid, cacheKey); ok {
			return map[string]any{"asset": asset, "cached": true}, nil
		}
	}

	call := map[string]any{
		"kind":      assetType,
		"prompt":    resolved,
		"cache_key": cacheKey,
		"metadata": map[string]any{
			"source_app":    "persona-studio",
			"persona_id":    personaID,
			"campaign_id":   int64Arg(args, "campaign_id"),
			"reference_ids": refIDs(refs),
			"item_ids":      itemIDs,
		},
		"_project_id": pid,
	}
	for k, v := range settings {
		call[k] = v
	}
	if call["model"] == nil && style != nil {
		for k, v := range jsonObject(style.ProviderSettings) {
			if _, exists := call[k]; !exists {
				call[k] = v
			}
		}
	}
	if assetType == "audio_tts" && call["voice"] == nil && persona.DefaultVoiceID != "" {
		call["voice"] = persona.DefaultVoiceID
	}
	if assetType == "avatar" && call["avatar"] == nil && persona.DefaultAvatarID != "" {
		call["avatar"] = persona.DefaultAvatarID
	}

	var mediaOut map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("media-studio", "media_generate", call, &mediaOut); err != nil {
		asset, insertErr := a.insertAsset(ctx, pid, personaID, int64Arg(args, "campaign_id"), assetType, "failed", prompt, resolved, "", "", settings, refIDs(refs), itemIDs, cacheKey, err.Error(), 0, 0)
		if insertErr != nil {
			return nil, err
		}
		return map[string]any{"asset": asset, "error": err.Error()}, nil
	}
	if msg := mcpResultError(mediaOut); msg != "" {
		asset, err := a.insertAsset(ctx, pid, personaID, int64Arg(args, "campaign_id"), assetType, "failed", prompt, resolved, "", "", settings, refIDs(refs), itemIDs, cacheKey, msg, 0, 0)
		return map[string]any{"asset": asset, "error": msg}, err
	}
	mediaMeta := mediaStudioResultMeta(mediaOut)
	status := "ready"
	if s := strFromMap(mediaMeta, "status"); s == "queued" || s == "polling" {
		status = s
	}
	storageID := firstInt(mediaMeta["storage_ids"])
	genID := firstNonZero(int64FromMap(mediaMeta, "generation_id"), int64FromMap(mediaMeta, "id"))
	provider := strFromMap(mediaMeta, "provider")
	model := strFromMap(mediaMeta, "model")
	asset, err := a.insertAsset(ctx, pid, personaID, int64Arg(args, "campaign_id"), assetType, status, prompt, resolved, provider, model, settings, refIDs(refs), itemIDs, cacheKey, "", storageID, genID)
	if err != nil {
		return nil, err
	}
	if storageID > 0 {
		_, _ = ctx.AppDB().Exec(
			`INSERT OR REPLACE INTO persona_generation_cache (cache_key, project_id, persona_id, asset_type, storage_file_id, asset_id, generation_id)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			cacheKey, pid, personaID, assetType, storageID, asset.ID, genID,
		)
	}
	return map[string]any{"asset": asset, "media": mediaOut, "cached": false}, nil
}

func (a *App) toolGeneratePack(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	prompts := mapArg(args, "prompts")
	jobs := []map[string]any{
		{"asset_type": "image", "prompt": "profile portrait for the persona's main social avatar", "settings": map[string]any{"aspect": "1:1"}},
		{"asset_type": "image", "prompt": "vertical lifestyle image suitable for a first social post", "settings": map[string]any{"aspect": "9:16"}},
		{"asset_type": "audio_tts", "prompt": "short friendly introduction in the persona's voice"},
		{"asset_type": "music", "prompt": "short background music bed matching the persona's brand mood"},
	}
	out := []any{}
	for _, job := range jobs {
		if override := cleanString(prompts[job["asset_type"].(string)]); override != "" {
			job["prompt"] = override
		}
		job["persona_id"] = personaID
		job["_project_id"] = pid
		job["campaign_id"] = int64Arg(args, "campaign_id")
		if _, ok := args["use_cache"]; ok {
			job["use_cache"] = boolArg(args, "use_cache")
		}
		res, err := a.toolGenerateAsset(ctx, job)
		if err != nil {
			out = append(out, map[string]any{"error": err.Error(), "asset_type": job["asset_type"]})
		} else {
			out = append(out, res)
		}
	}
	return map[string]any{"results": out}, nil
}

func (a *App) toolCreateClipPlan(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	persona, err := getPersona(ctx.AppDB(), pid, personaID)
	if err != nil {
		return nil, err
	}
	brief := strArg(args, "brief")
	if brief == "" {
		return nil, errors.New("brief required")
	}
	duration := intArg(args, "duration_ms", 20000)
	aspect := strArg(args, "aspect")
	if aspect == "" {
		aspect = "9:16"
	}
	assets, _ := listAssetsByIDs(ctx.AppDB(), pid, int64SliceArg(args, "asset_ids"))
	plan := map[string]any{
		"persona_id":  personaID,
		"persona":     persona.Name,
		"brief":       brief,
		"aspect":      aspect,
		"duration_ms": duration,
		"scenes": []map[string]any{
			{"index": 1, "start_ms": 0, "duration_ms": duration / 3, "purpose": "hook", "direction": "Open with the persona and the strongest visual promise."},
			{"index": 2, "start_ms": duration / 3, "duration_ms": duration / 3, "purpose": "body", "direction": "Show the item, reference, or proof point clearly."},
			{"index": 3, "start_ms": 2 * duration / 3, "duration_ms": duration - 2*(duration/3), "purpose": "close", "direction": "End with a clear call to action or memorable sign-off."},
		},
		"voiceover":    buildVoiceoverDraft(persona, brief, duration),
		"caption_tone": persona.Tone,
		"asset_ids":    int64SliceArg(args, "asset_ids"),
		"assets":       assets,
	}
	return map[string]any{"plan": plan}, nil
}

func (a *App) toolCreateComposition(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, personaID, err := projectPersona(ctx, args)
	if err != nil {
		return nil, err
	}
	title := strArg(args, "title")
	if title == "" {
		title = "Persona clip"
	}
	aspect := strArg(args, "aspect")
	if aspect == "" {
		aspect = "9:16"
	}
	durationMS := intArg(args, "duration_ms", 20000)
	plan := mapArg(args, "plan")
	assetIDs := int64SliceArg(args, "asset_ids")
	assets, _ := listAssetsByIDs(ctx.AppDB(), pid, assetIDs)
	tracks := buildComposerTracks(assets, durationMS)
	call := map[string]any{
		"name":        title,
		"tracks":      tracks,
		"output":      map[string]any{"format": "mp4", "aspect": aspect, "fps": 30},
		"_project_id": pid,
	}
	var compOut map[string]any
	err = ctx.WithProject(pid).PlatformAPI().CallAppResult("composer", "composition_create", call, &compOut)
	composerID := int64FromMap(compOut, "id")
	status := "draft"
	if err != nil {
		status = "failed"
		plan["composer_error"] = err.Error()
	}
	res, insertErr := ctx.AppDB().Exec(
		`INSERT INTO persona_compositions (project_id, persona_id, campaign_id, composer_composition_id, title, aspect, duration_ms, plan_json, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, personaID, nullableInt64(int64Arg(args, "campaign_id")), nullableInt64(composerID), title, aspect, durationMS, mustJSON(plan, "{}"), status,
	)
	if insertErr != nil {
		return nil, insertErr
	}
	id, _ := res.LastInsertId()
	out := map[string]any{"id": id, "persona_id": personaID, "composer_composition_id": composerID, "status": status, "composition": compOut}
	if err != nil {
		out["error"] = err.Error()
	}
	return map[string]any{"composition": out}, nil
}

// Database helpers

func getPersona(db *sql.DB, pid string, id int64) (*Persona, error) {
	var p Persona
	var brand string
	err := db.QueryRow(
		`SELECT id, project_id, name, handle, bio, audience, personality, tone, visual_style, negative_style,
		        brand_rules_json, default_voice_id, default_avatar_id, default_image_provider, default_video_provider,
		        default_audio_provider, default_music_provider, default_avatar_provider, archived_at, created_at, updated_at
		 FROM personas WHERE id=? AND project_id=?`,
		id, pid,
	).Scan(&p.ID, &p.ProjectID, &p.Name, &p.Handle, &p.Bio, &p.Audience, &p.Personality, &p.Tone, &p.VisualStyle,
		&p.NegativeStyle, &brand, &p.DefaultVoiceID, &p.DefaultAvatarID, &p.DefaultImageProvider, &p.DefaultVideoProvider,
		&p.DefaultAudioProvider, &p.DefaultMusicProvider, &p.DefaultAvatarProvider, &p.ArchivedAt, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	p.BrandRules = rawJSON(brand, "{}")
	return &p, nil
}

func (a *App) listPersonas(ctx *sdk.AppCtx, pid string, includeArchived bool, limit int) ([]Persona, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	q := `SELECT id, project_id, name, handle, bio, audience, personality, tone, visual_style, negative_style,
	             brand_rules_json, default_voice_id, default_avatar_id, default_image_provider, default_video_provider,
	             default_audio_provider, default_music_provider, default_avatar_provider, archived_at, created_at, updated_at
	      FROM personas WHERE project_id=?`
	if !includeArchived {
		q += ` AND archived_at=''`
	}
	q += ` ORDER BY updated_at DESC, id DESC LIMIT ?`
	rows, err := ctx.AppDB().Query(q, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Persona{}
	for rows.Next() {
		var p Persona
		var brand string
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Handle, &p.Bio, &p.Audience, &p.Personality, &p.Tone,
			&p.VisualStyle, &p.NegativeStyle, &brand, &p.DefaultVoiceID, &p.DefaultAvatarID, &p.DefaultImageProvider,
			&p.DefaultVideoProvider, &p.DefaultAudioProvider, &p.DefaultMusicProvider, &p.DefaultAvatarProvider,
			&p.ArchivedAt, &p.CreatedAt, &p.UpdatedAt); err == nil {
			p.BrandRules = rawJSON(brand, "{}")
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

func (a *App) personaBundle(ctx *sdk.AppCtx, pid string, id int64) (map[string]any, error) {
	p, err := getPersona(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	styles, _ := listStyleProfiles(ctx.AppDB(), pid, id, "")
	refs, _ := listReferences(ctx.AppDB(), pid, id, "", true)
	items, _ := listItems(ctx.AppDB(), pid, id, "", true)
	campaigns, _ := listCampaigns(ctx.AppDB(), pid, id)
	assets, _ := listAssets(ctx.AppDB(), pid, id, 30)
	return map[string]any{"persona": p, "style_profiles": styles, "references": refs, "items": items, "campaigns": campaigns, "assets": assets}, nil
}

func ensureDefaultStyles(db *sql.DB, pid string, personaID int64) error {
	defaults := []struct {
		Name, AssetType, Prefix string
	}{
		{"Image default", "image", "Create a visually consistent image of the persona."},
		{"Video default", "video", "Create a short video consistent with the persona identity and visual style."},
		{"Voice default", "audio_tts", "Speak in the persona's voice and tone."},
		{"Music default", "music", "Create a music bed that fits the persona's brand mood."},
		{"Avatar default", "avatar", "Create a talking-head avatar clip for this persona."},
	}
	for _, d := range defaults {
		_, _ = db.Exec(
			`INSERT INTO persona_style_profiles (project_id, persona_id, name, asset_type, prompt_prefix, is_default)
			 SELECT ?, ?, ?, ?, ?, 1
			 WHERE NOT EXISTS (
			   SELECT 1 FROM persona_style_profiles WHERE project_id=? AND persona_id=? AND asset_type=? AND is_default=1
			 )`,
			pid, personaID, d.Name, d.AssetType, d.Prefix, pid, personaID, d.AssetType,
		)
	}
	return nil
}

func getStyleProfile(db *sql.DB, pid string, personaID, id int64) (*StyleProfile, error) {
	rows, err := listStyleProfilesWhere(db, `project_id=? AND persona_id=? AND id=?`, pid, personaID, id)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

func selectStyleProfile(db *sql.DB, pid string, personaID, id int64, assetType string) (*StyleProfile, error) {
	if id > 0 {
		return getStyleProfile(db, pid, personaID, id)
	}
	rows, err := listStyleProfilesWhere(db, `project_id=? AND persona_id=? AND asset_type=? ORDER BY is_default DESC, id ASC LIMIT 1`, pid, personaID, assetType)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return &rows[0], nil
}

func listStyleProfiles(db *sql.DB, pid string, personaID int64, assetType string) ([]StyleProfile, error) {
	if assetType != "" {
		return listStyleProfilesWhere(db, `project_id=? AND persona_id=? AND asset_type=? ORDER BY asset_type, is_default DESC, name`, pid, personaID, assetType)
	}
	return listStyleProfilesWhere(db, `project_id=? AND persona_id=? ORDER BY asset_type, is_default DESC, name`, pid, personaID)
}

func listStyleProfilesWhere(db *sql.DB, where string, vals ...any) ([]StyleProfile, error) {
	rows, err := db.Query(
		`SELECT id, project_id, persona_id, name, asset_type, prompt_prefix, prompt_suffix, negative_prompt,
		        provider_settings_json, composition_settings_json, is_default, created_at, updated_at
		 FROM persona_style_profiles WHERE `+where, vals...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StyleProfile{}
	for rows.Next() {
		var sp StyleProfile
		var provider, composition string
		var def int
		if err := rows.Scan(&sp.ID, &sp.ProjectID, &sp.PersonaID, &sp.Name, &sp.AssetType, &sp.PromptPrefix,
			&sp.PromptSuffix, &sp.NegativePrompt, &provider, &composition, &def, &sp.CreatedAt, &sp.UpdatedAt); err == nil {
			sp.ProviderSettings = rawJSON(provider, "{}")
			sp.CompositionSettings = rawJSON(composition, "{}")
			sp.IsDefault = def != 0
			out = append(out, sp)
		}
	}
	return out, rows.Err()
}

func getReference(db *sql.DB, pid string, id int64) (*Reference, error) {
	rows, err := listReferencesWhere(db, `project_id=? AND id=?`, pid, id)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

func listReferences(db *sql.DB, pid string, personaID int64, kind string, activeOnly bool) ([]Reference, error) {
	where, vals := `project_id=? AND persona_id=?`, []any{pid, personaID}
	if kind != "" {
		where += ` AND kind=?`
		vals = append(vals, kind)
	}
	if activeOnly {
		where += ` AND active=1`
	}
	where += ` ORDER BY kind, label, id`
	return listReferencesWhere(db, where, vals...)
}

func listReferencesForKinds(db *sql.DB, pid string, personaID int64, kinds []string) ([]Reference, error) {
	all, err := listReferences(db, pid, personaID, "", true)
	if err != nil || len(kinds) == 0 {
		return all, err
	}
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	out := []Reference{}
	for _, r := range all {
		if want[r.Kind] {
			out = append(out, r)
		}
	}
	return out, nil
}

func listReferencesWhere(db *sql.DB, where string, vals ...any) ([]Reference, error) {
	rows, err := db.Query(
		`SELECT id, project_id, persona_id, storage_file_id, kind, label, weight, notes, active, created_at, updated_at
		 FROM persona_references WHERE `+where, vals...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reference{}
	for rows.Next() {
		var r Reference
		var active int
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.PersonaID, &r.StorageFileID, &r.Kind, &r.Label, &r.Weight, &r.Notes, &active, &r.CreatedAt, &r.UpdatedAt); err == nil {
			r.Active = active != 0
			out = append(out, r)
		}
	}
	return out, rows.Err()
}

func getItem(db *sql.DB, pid string, id int64) (*Item, error) {
	rows, err := listItemsWhere(db, `project_id=? AND id=?`, pid, id)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

func listItems(db *sql.DB, pid string, personaID int64, kind string, activeOnly bool) ([]Item, error) {
	where, vals := `project_id=? AND persona_id=?`, []any{pid, personaID}
	if kind != "" {
		where += ` AND kind=?`
		vals = append(vals, kind)
	}
	if activeOnly {
		where += ` AND active=1`
	}
	where += ` ORDER BY kind, name, id`
	return listItemsWhere(db, where, vals...)
}

func listItemsByIDs(db *sql.DB, pid string, personaID int64, ids []int64) ([]Item, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := listItems(db, pid, personaID, "", true)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []Item{}
	for _, item := range all {
		if want[item.ID] {
			out = append(out, item)
		}
	}
	return out, nil
}

func listItemsWhere(db *sql.DB, where string, vals ...any) ([]Item, error) {
	rows, err := db.Query(
		`SELECT id, project_id, persona_id, name, kind, description, usage_rules, visual_rules,
		        storage_file_ids_json, active, metadata_json, created_at, updated_at
		 FROM persona_items WHERE `+where, vals...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var item Item
		var ids, meta string
		var active int
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.PersonaID, &item.Name, &item.Kind, &item.Description,
			&item.UsageRules, &item.VisualRules, &ids, &active, &meta, &item.CreatedAt, &item.UpdatedAt); err == nil {
			_ = json.Unmarshal([]byte(ids), &item.StorageFileIDs)
			item.Active = active != 0
			item.Metadata = rawJSON(meta, "{}")
			out = append(out, item)
		}
	}
	return out, rows.Err()
}

func getCampaign(db *sql.DB, pid string, id int64) (map[string]any, error) {
	rows, err := listCampaignsWhere(db, `project_id=? AND id=?`, pid, id)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return rows[0], nil
}

func listCampaigns(db *sql.DB, pid string, personaID int64) ([]map[string]any, error) {
	return listCampaignsWhere(db, `project_id=? AND persona_id=? ORDER BY updated_at DESC, id DESC`, pid, personaID)
}

func listCampaignsWhere(db *sql.DB, where string, vals ...any) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT id, project_id, persona_id, name, brief, platforms_json, content_pillars_json, status, created_at, updated_at
		 FROM persona_campaigns WHERE `+where, vals...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, personaID int64
		var projectID, name, brief, platforms, pillars, status, createdAt, updatedAt string
		if err := rows.Scan(&id, &projectID, &personaID, &name, &brief, &platforms, &pillars, &status, &createdAt, &updatedAt); err == nil {
			out = append(out, map[string]any{"id": id, "project_id": projectID, "persona_id": personaID, "name": name, "brief": brief, "platforms": jsonList(platforms), "content_pillars": jsonList(pillars), "status": status, "created_at": createdAt, "updated_at": updatedAt})
		}
	}
	return out, rows.Err()
}

func (a *App) insertAsset(ctx *sdk.AppCtx, pid string, personaID, campaignID int64, assetType, status, prompt, resolved, provider, model string, settings map[string]any, referenceIDs, itemIDs []int64, cacheKey, errMsg string, storageID, generationID int64) (*Asset, error) {
	res, err := ctx.AppDB().Exec(
		`INSERT INTO persona_assets
		 (project_id, persona_id, campaign_id, storage_file_id, media_generation_id, asset_type, status, prompt,
		  resolved_prompt, provider_slug, provider_model, settings_json, reference_ids_json, item_ids_json, cache_key, error)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, personaID, nullableInt64(campaignID), nullableInt64(storageID), nullableInt64(generationID), assetType, status, prompt,
		resolved, provider, model, mustJSON(settings, "{}"), mustJSON(referenceIDs, "[]"), mustJSON(itemIDs, "[]"), cacheKey, errMsg,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getAsset(ctx.AppDB(), pid, id)
}

func getAsset(db *sql.DB, pid string, id int64) (*Asset, error) {
	rows, err := listAssetsWhere(db, `project_id=? AND id=?`, pid, id)
	if err != nil || len(rows) == 0 {
		if err != nil {
			return nil, err
		}
		return nil, sql.ErrNoRows
	}
	return &rows[0], nil
}

func listAssets(db *sql.DB, pid string, personaID int64, limit int) ([]Asset, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	return listAssetsWhere(db, `project_id=? AND persona_id=? ORDER BY id DESC LIMIT ?`, pid, personaID, limit)
}

func listAssetsByIDs(db *sql.DB, pid string, ids []int64) ([]Asset, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	all, err := listAssetsWhere(db, `project_id=? ORDER BY id DESC LIMIT 500`, pid)
	if err != nil {
		return nil, err
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	out := []Asset{}
	for _, asset := range all {
		if want[asset.ID] {
			out = append(out, asset)
		}
	}
	return out, nil
}

func listAssetsWhere(db *sql.DB, where string, vals ...any) ([]Asset, error) {
	rows, err := db.Query(
		`SELECT id, project_id, persona_id, COALESCE(campaign_id,0), COALESCE(storage_file_id,0), COALESCE(media_generation_id,0),
		        asset_type, status, prompt, resolved_prompt, provider_slug, provider_model, settings_json,
		        reference_ids_json, item_ids_json, cache_key, error, created_at, updated_at
		 FROM persona_assets WHERE `+where, vals...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		var settings, refs, items string
		if err := rows.Scan(&a.ID, &a.ProjectID, &a.PersonaID, &a.CampaignID, &a.StorageFileID, &a.MediaGenerationID, &a.AssetType,
			&a.Status, &a.Prompt, &a.ResolvedPrompt, &a.ProviderSlug, &a.ProviderModel, &settings, &refs, &items,
			&a.CacheKey, &a.Error, &a.CreatedAt, &a.UpdatedAt); err == nil {
			a.Settings = rawJSON(settings, "{}")
			_ = json.Unmarshal([]byte(refs), &a.ReferenceIDs)
			_ = json.Unmarshal([]byte(items), &a.ItemIDs)
			out = append(out, a)
		}
	}
	return out, rows.Err()
}

func filterStorageBrowserOutput(out map[string]any) {
	if out == nil {
		return
	}
	files, ok := out["files"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(files))
	for _, file := range files {
		if storageBrowserFileHidden(file) {
			continue
		}
		filtered = append(filtered, file)
	}
	out["files"] = filtered
	out["count"] = len(filtered)
}

func storageBrowserFileHidden(file any) bool {
	m, ok := file.(map[string]any)
	if !ok {
		return false
	}
	folder := cleanString(m["folder"])
	name := cleanString(m["name"])
	return pathHasDotSegment(folder) || strings.HasPrefix(name, ".")
}

func pathHasDotSegment(path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	for _, part := range strings.Split(path, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func cachedAsset(db *sql.DB, pid, cacheKey string) (*Asset, bool) {
	var assetID int64
	err := db.QueryRow(`SELECT asset_id FROM persona_generation_cache WHERE project_id=? AND cache_key=? AND (expires_at='' OR expires_at > CURRENT_TIMESTAMP)`, pid, cacheKey).Scan(&assetID)
	if err != nil {
		return nil, false
	}
	asset, err := getAsset(db, pid, assetID)
	return asset, err == nil
}

func mediaStudioResultMeta(out map[string]any) map[string]any {
	if out == nil {
		return map[string]any{}
	}
	if meta, ok := out["_meta"].(map[string]any); ok {
		return meta
	}
	return out
}

func mcpResultError(out map[string]any) string {
	if out == nil || !boolArg(out, "isError") {
		return ""
	}
	if blocks, ok := out["content"].([]any); ok {
		for _, block := range blocks {
			m, ok := block.(map[string]any)
			if !ok {
				continue
			}
			if text := cleanString(m["text"]); text != "" {
				return text
			}
		}
	}
	return "media-studio returned an error"
}

// Prompt/composition helpers

func buildResolvedPrompt(persona *Persona, style *StyleProfile, refs []Reference, items []Item, prompt, assetType string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Persona: %s\n", persona.Name)
	if persona.Handle != "" {
		fmt.Fprintf(&b, "Handle: %s\n", persona.Handle)
	}
	writeField(&b, "Bio", persona.Bio)
	writeField(&b, "Audience", persona.Audience)
	writeField(&b, "Personality", persona.Personality)
	writeField(&b, "Tone", persona.Tone)
	writeField(&b, "Visual style", persona.VisualStyle)
	writeField(&b, "Asset type", assetType)
	if style != nil {
		writeField(&b, "Style profile", style.Name)
		writeField(&b, "Style instructions", style.PromptPrefix)
	}
	if len(refs) > 0 {
		b.WriteString("Persona references:\n")
		for _, ref := range refs {
			fmt.Fprintf(&b, "- storage:%d kind=%s weight=%.2f", ref.StorageFileID, ref.Kind, ref.Weight)
			if ref.Label != "" {
				fmt.Fprintf(&b, " label=%q", ref.Label)
			}
			if ref.Notes != "" {
				fmt.Fprintf(&b, " notes=%q", ref.Notes)
			}
			b.WriteByte('\n')
		}
	}
	if len(items) > 0 {
		b.WriteString("Items to include or respect:\n")
		for _, item := range items {
			fmt.Fprintf(&b, "- %s (%s)", item.Name, item.Kind)
			if item.Description != "" {
				fmt.Fprintf(&b, ": %s", item.Description)
			}
			if item.VisualRules != "" {
				fmt.Fprintf(&b, " Visual rules: %s", item.VisualRules)
			}
			if item.UsageRules != "" {
				fmt.Fprintf(&b, " Usage rules: %s", item.UsageRules)
			}
			if len(item.StorageFileIDs) > 0 {
				fmt.Fprintf(&b, " References: %v", item.StorageFileIDs)
			}
			b.WriteByte('\n')
		}
	}
	writeField(&b, "User prompt", prompt)
	negative := persona.NegativeStyle
	if style != nil && style.NegativePrompt != "" {
		if negative != "" {
			negative += "; "
		}
		negative += style.NegativePrompt
	}
	writeField(&b, "Avoid", negative)
	if style != nil {
		writeField(&b, "Final style suffix", style.PromptSuffix)
	}
	return strings.TrimSpace(b.String())
}

func buildComposerTracks(assets []Asset, durationMS int) []map[string]any {
	if durationMS <= 0 {
		durationMS = 20000
	}
	if len(assets) == 0 {
		return []map[string]any{{"clips": []map[string]any{}}}
	}
	clipLen := durationMS / len(assets)
	if clipLen <= 0 {
		clipLen = durationMS
	}
	clips := []map[string]any{}
	for i, asset := range assets {
		if asset.StorageFileID == 0 {
			continue
		}
		clips = append(clips, map[string]any{
			"asset":  map[string]any{"type": mediaKind(asset.AssetType), "src": fmt.Sprintf("storage:%d", asset.StorageFileID)},
			"start":  float64(i*clipLen) / 1000,
			"length": float64(clipLen) / 1000,
			"text":   clipText(asset.Prompt),
		})
	}
	return []map[string]any{{"clips": clips}}
}

func buildVoiceoverDraft(p *Persona, brief string, durationMS int) string {
	seconds := durationMS / 1000
	if seconds <= 0 {
		seconds = 20
	}
	return fmt.Sprintf("%s speaks in a %s tone for about %d seconds: %s", p.Name, fallback(p.Tone, "natural"), seconds, brief)
}

func generationCacheKey(personaID int64, assetType, resolved string, settings map[string]any, refIDs, itemIDs []int64) string {
	body := mustJSON(map[string]any{"persona_id": personaID, "asset_type": assetType, "prompt": resolved, "settings": settings, "references": refIDs, "items": itemIDs}, "{}")
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func defaultImageSourceRefs(refs []Reference, items []Item, settings map[string]any) []string {
	limit := imageSourceLimit(settings)
	if limit <= 0 {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(ref string) {
		ref = strings.TrimSpace(ref)
		if ref == "" || seen[ref] || len(out) >= limit {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	for _, ref := range stringSliceArg(settings, "source_images") {
		add(ref)
	}
	add(strArg(settings, "source_image"))
	for _, ref := range refs {
		if ref.StorageFileID > 0 {
			add(fmt.Sprintf("storage:%d", ref.StorageFileID))
		}
	}
	for _, item := range items {
		for _, id := range item.StorageFileIDs {
			if id > 0 {
				add(fmt.Sprintf("storage:%d", id))
			}
		}
	}
	return out
}

func imageSourceLimit(settings map[string]any) int {
	model := strings.ToLower(strArg(settings, "model"))
	if model == "dall-e-2" {
		return 1
	}
	if strings.HasPrefix(model, "gemini-") {
		return 5
	}
	if strings.HasPrefix(model, "gpt-image") && !strings.HasSuffix(model, "-edit") {
		return 16
	}
	if strings.HasSuffix(model, "-edit") || model == "" {
		return 3
	}
	return 3
}

func isImageEditModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return true
	}
	return strings.HasSuffix(model, "-edit") ||
		strings.HasPrefix(model, "gemini-") ||
		strings.Contains(model, "image-edit")
}

func refIDs(refs []Reference) []int64 {
	out := make([]int64, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.ID)
	}
	return out
}

// Request/arg helpers

func requestCtx(w http.ResponseWriter, r *http.Request) (*sdk.AppCtx, string, bool) {
	if globalCtx == nil {
		httpErr(w, 500, "app context unavailable")
		return nil, "", false
	}
	ctx := globalCtx
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		ctx = globalCtx.WithProject(pid)
	}
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return nil, "", false
	}
	return ctx.WithProject(pid), pid, true
}

func projectFromRequest(ctx *sdk.AppCtx, r *http.Request) (string, error) {
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid, nil
	}
	if pid := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID")); pid != "" {
		return pid, nil
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject(), nil
	}
	return "", errors.New("project_id required for global persona-studio installs")
}

func projectFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if pid := strArg(args, "_project_id"); pid != "" {
		return pid, nil
	}
	if pid := strArg(args, "project_id"); pid != "" {
		return pid, nil
	}
	if ctx != nil && ctx.CurrentProject() != "" {
		return ctx.CurrentProject(), nil
	}
	return "", errors.New("_project_id required for global persona-studio installs")
}

func projectPersona(ctx *sdk.AppCtx, args map[string]any) (string, int64, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return "", 0, err
	}
	personaID := int64Arg(args, "persona_id")
	if personaID == 0 {
		return "", 0, errors.New("persona_id required")
	}
	if _, err := getPersona(ctx.AppDB(), pid, personaID); err != nil {
		return "", 0, err
	}
	return pid, personaID, nil
}

func readJSON(r *http.Request, out any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(out)
}

func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	writeJSON(w, v)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func idFromPath(path, prefix string) (int64, error) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return 0, errors.New("id required")
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("valid id required")
	}
	return id, nil
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }
func sBool() map[string]any    { return map[string]any{"type": "boolean"} }
func sObject() map[string]any  { return map[string]any{"type": "object"} }
func sArray(t string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": t}}
}

func strArg(m map[string]any, key string) string {
	return cleanString(m[key])
}

func cleanString(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func boolArg(m map[string]any, key string) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	case int:
		return v != 0
	}
	return false
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	}
	return def
}

func int64Arg(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}

func floatArg(m map[string]any, key string, def float64) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return def
}

func int64SliceArg(m map[string]any, key string) []int64 {
	switch v := m[key].(type) {
	case []int64:
		return v
	case []int:
		out := make([]int64, 0, len(v))
		for _, n := range v {
			out = append(out, int64(n))
		}
		return out
	case []any:
		out := []int64{}
		for _, x := range v {
			if n := int64FromAny(x); n > 0 {
				out = append(out, n)
			}
		}
		return out
	}
	return nil
}

func stringSliceArg(m map[string]any, key string) []string {
	switch v := m[key].(type) {
	case []string:
		return v
	case []any:
		out := []string{}
		for _, x := range v {
			if s := cleanString(x); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func mapArg(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonArg(m map[string]any, key, def string) string {
	if v, ok := m[key]; ok {
		return mustJSON(v, def)
	}
	return def
}

func mustJSON(v any, def string) string {
	if v == nil {
		return def
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "" {
			return def
		}
		if json.Valid([]byte(s)) {
			return s
		}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return def
	}
	return string(b)
}

func rawJSON(s, def string) json.RawMessage {
	if !json.Valid([]byte(s)) {
		s = def
	}
	return json.RawMessage(s)
}

func jsonObject(raw json.RawMessage) map[string]any {
	out := map[string]any{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func jsonList(s string) []any {
	out := []any{}
	_ = json.Unmarshal([]byte(s), &out)
	return out
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func int64FromMap(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	return int64FromAny(m[key])
}

func strFromMap(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	return cleanString(m[key])
}

func firstInt(v any) int64 {
	switch xs := v.(type) {
	case []any:
		if len(xs) > 0 {
			return int64FromAny(xs[0])
		}
	case []int64:
		if len(xs) > 0 {
			return xs[0]
		}
	}
	return int64FromAny(v)
}

func firstNonZero(vals ...int64) int64 {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func intQuery(r *http.Request, key string, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil {
		return def
	}
	return n
}

func int64Query(r *http.Request, key string, def int64) int64 {
	n, err := strconv.ParseInt(r.URL.Query().Get(key), 10, 64)
	if err != nil {
		return def
	}
	return n
}

func boolQuery(r *http.Request, key string, def bool) bool {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	return v == "1" || v == "true" || v == "yes"
}

func normalizeAssetType(v string) string {
	switch strings.TrimSpace(v) {
	case "image", "video", "audio_tts", "audio_sfx", "music", "avatar":
		return v
	case "audio", "voice":
		return "audio_tts"
	}
	return ""
}

func mediaKind(v string) string {
	switch v {
	case "audio_tts", "audio_sfx", "music":
		return "audio"
	case "image":
		return "image"
	default:
		return "video"
	}
}

func writeField(b *strings.Builder, label, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		fmt.Fprintf(b, "%s: %s\n", label, value)
	}
}

func clipText(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 90 {
		return s[:87] + "..."
	}
	return s
}

func fallback(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func main() { sdk.Run(&App{}) }

var _ = time.Now
