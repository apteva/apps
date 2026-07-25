// Media Studio v0.3 — generate images, video, audio, and music via any
// compatible provider.
//
// Architecture:
//   - manifest declares 5 single-binding integration roles:
//     image_provider, video_provider, audio_provider, music_provider, storage
//     each optional; tools enforce "is this role bound?" at call time.
//   - one unified MCP tool (media_generate) discriminates on `kind` and
//     routes to per-kind builders + normalizers (image.go, video.go, …).
//   - bytes are downloaded from the upstream URL while it's still fresh,
//     handed off to storage when bound, or returned inline / via upstream
//     URL otherwise.
//
// History lives in the app's own DB so the panel can render a gallery
// across restarts and sessions, filterable by kind.
package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: media-studio
display_name: Media Studio
version: 0.10.54
description: |
  Generate images, video, audio, music, and avatars via compatible
  providers. Optionally saves outputs to Storage, supports stable
  cache keys for app-to-app generation reuse, and can use OpenAI Codex
  as a subscription-backed image provider. v0.10.54 adds a provider-grouped
  voice picker, automatically routes generation through the selected voice's
  provider, and moves custom voice cloning and design into a responsive
  creation dialog. v0.10.53 publishes the
  complete provider-neutral voice stack with Fish Audio, Cartesia, and
  MiniMax cloning available through the same tools and UI. v0.10.52 adds
  provider-neutral Cartesia and MiniMax TTS, voice catalogs, cloning, and
  MiniMax prompt-based Voice Design. v0.10.51 adds a responsive chat
  generation card with image previews, custom media controls, metadata, and
  live queued-job promotion. v0.10.50 makes JPEG the default
  image output and guarantees final JPEG files stay below 2 MB, preserving
  quality 90 when possible and adapting quality or dimensions only when
  required. v0.10.49 replaces browser-default
  audio and video controls with a reusable Media Studio player, stable video
  stages, generated thumbnail posters, exclusive playback, responsive controls,
  and consistent card metadata. v0.10.48 adds Deepgram Aura as a
  generic TTS provider with provider-specific routing, model choices, output
  formats, and Storage saves. v0.10.47 keeps historical failed
  jobs out of the active gallery feed and makes current UI errors dismissible
  and temporary. v0.10.46 omits aspect_ratio for
  Venice video models whose live constraints do not support it, including WAN
  image-to-video variants. v0.10.45 adds newest-first cursor
  pagination to media_history and the gallery, a history index, functional
  since filtering, and 24-item incremental UI pages for large histories.
  v0.10.44 moves generation into
  a focused responsive dialog with a large prompt editor, grouped settings,
  mobile sheet layout, sticky actions, keyboard focus handling, and automatic
  reference-edit opening. v0.10.43 fixes multi-provider
  image options, stale panel responses, required video references,
  project-scoped previews, upload validation, and responsive/accessibility
  behavior. v0.10.42 includes project_id
  in panel generation and avatar creation URLs so project-scoped installs
  resolve through the app proxy. v0.10.41 rebuilds the panel
  with the production JSX runtime and rejects development-only jsxDEV
  imports in the committed artifact. v0.10.40 adds multiple audio
  providers, Fish Audio TTS and voice catalogs, and provider-neutral
  voice cloning from uploads, Storage references, URLs, and inline audio.
  v0.10.39 filters unsupported
  ElevenLabs request-stitching fields for Eleven v3 and zero-retention
  requests while retaining text continuity where supported, and forwards
  current language-normalization voice options. v0.10.38 enforces image
  output_format on final stored bytes, converting provider mismatches
  before Storage and flattening transparency onto white for JPEG.
  v0.10.37 hardens project
  isolation, async job recovery, Storage fallback, provider prompt
  limits, and oversized downloads. Queued jobs retain their original
  provider connection and safely resume finalization after restarts.
  v0.10.36 sniffs generated
  image bytes before saving so mismatched provider format metadata
  does not create Storage files with the wrong MIME type or extension.
  v0.10.35 restores default
  image model discovery for bound providers when no explicit model
  capability is supplied.
  v0.10.34 adds media_models
  for agent-side model discovery and prepares image generation for
  multiple bound image providers without explicit provider selection
  in tool calls.
  v0.10.30 lets voice
  creation choose the ElevenLabs Voice Design model, defaulting to
  eleven_ttv_v3. v0.10.29 adds UI controls for ElevenLabs Voice Design
  voice creation on the Audio/TTS tab.
  v0.10.28 accepts common ElevenLabs request-id response header
  variants for TTS continuity.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  integrations:
    - role: image_provider
      kind: integration
      mode: multiple
      compatible_slugs: [openai-api, openai-codex, venice-ai, gemini]
      capabilities: [image.generate, image.edit]
      tools:
        image.generate: generate_image
        image.edit: edit_image
      required: false
      label: "Image provider"
    - role: video_provider
      kind: integration
      compatible_slugs: [venice-ai, replicate, runway, pika]
      capabilities: [video.generate]
      tools: { video.generate: queue_video }
      required: false
      label: "Video provider"
    - role: audio_provider
      kind: integration
      mode: multiple
      compatible_slugs: [elevenlabs, fish-audio, deepgram, cartesia, minimax-audio]
      capabilities: [audio.tts, audio.sfx, voice.create]
      tools:
        audio.tts: text_to_speech
        audio.sfx: generate_sfx
        voice.create: design_voice
      required: false
      label: "Audio provider"
    - role: music_provider
      kind: integration
      compatible_slugs: [elevenlabs]
      capabilities: [music.generate]
      tools: { music.generate: generate_music }
      required: false
      label: "Music provider"
    - role: avatar_provider
      kind: integration
      compatible_slugs: [tavus, heygen]
      capabilities: [avatar.generate]
      tools: { avatar.generate: create_video }
      required: false
      label: "Avatar / talking-head provider"
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write, files.delete]
      required: false
      label: "Storage (optional)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: media_models, description: "List available media models for a kind. Args: kind? (default image). Use returned model ids in media_generate; image and audio ids may include a provider prefix when multiple providers are bound." }
    - { name: media_generate, description: "Generate media (image/video/audio/music/avatar). Args: kind, prompt, provider?, model? (use a model id returned by media_models), size?, duration?, voice?, aspect?, avatar?, storage_folder?, n?, options?, cache_key?, cache_policy?. In chat, attach the returned _meta.chat_component through respond(components=[...])." }
    - { name: media_estimate, description: "Estimate generation cost without creating media. Args match media_generate." }
    - { name: media_delete, description: "Delete a media generation and, by default, its linked Storage files. Args: id, delete_storage?." }
    - { name: media_identity_create, description: "Create a reusable provider-side identity such as a voice or avatar. Voice creation is provider-neutral: source_type=prompt designs through ElevenLabs or MiniMax; source_type=audio clones through ElevenLabs, Fish Audio, Cartesia, or MiniMax from source_audio/source_audios. Args also include provider?, name, language?, transcripts?, source_image?, source_video?, labels?, options?." }
    - { name: media_identity_list, description: "List Media Studio tracked reusable identities. Args: kind? (voice|avatar), limit?." }
    - { name: media_identity_get, description: "Fetch one tracked reusable identity by id." }
    - { name: media_voice_create, description: "Alias for media_identity_create with kind=voice. Use source_type=prompt for ElevenLabs or MiniMax Voice Design, or source_type=audio plus source_audio/source_audios for provider-neutral ElevenLabs, Fish Audio, Cartesia, or MiniMax cloning." }
    - { name: media_voice_list, description: "List tracked voice identities and, when bound, provider voice catalog entries." }
    - { name: media_avatar_create, description: "Create/train a reusable avatar from a photo or prompt. Args: name, source_type, source_image?/prompt?, options?." }
    - { name: media_avatar_list, description: "List tracked avatar identities and provider avatar catalog entries." }
    - { name: media_history,  description: "List generations newest-first. Args: kind?, limit?, cursor?, since?. Returns next_cursor and has_more." }
    - { name: media_get,      description: "Fetch one generation by id. Args: id." }
  ui_panels:
    - slot: project.page
      label: Studio
      icon: image
      entry: /ui/MediaPanel.mjs
  ui_components:
    - name: generation-card
      entry: /ui/GenerationCard.mjs
      slots: [chat.message_attachment]
      props_schema:
        type: object
        properties:
          generation_id: { type: integer, minimum: 1 }
          job_id: { type: integer, minimum: 1 }
        anyOf:
          - required: [generation_id]
          - required: [job_id]
      preview_props: { preview: true, generation_id: 1 }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/media-studio
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/media-studio.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("media-studio requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("media-studio mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "video-poll",
			Schedule: "@every 15s",
			Run:      a.videoPollWorker,
		},
		{
			Name:     "avatar-create-poll",
			Schedule: "@every 20s",
			Run:      a.avatarCreatePollWorker,
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (panel data) ──────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/generations", Handler: a.handleListGenerations},
		{Pattern: "/generations/", Handler: a.handleGetGeneration},
		{Pattern: "/generate", Handler: a.handleGenerate},
		{Pattern: "/estimate", Handler: a.handleEstimate},
		{Pattern: "/delete", Handler: a.handleDeleteGeneration},
		{Pattern: "/bindings", Handler: a.handleBindings},
		{Pattern: "/models", Handler: a.handleListModels},
		{Pattern: "/avatars", Handler: a.handleListAvatars},
		{Pattern: "/identities", Handler: a.handleListIdentities},
		{Pattern: "/identity-create", Handler: a.handleIdentityCreate},
		{Pattern: "/avatar-capabilities", Handler: a.handleAvatarCapabilities},
		{Pattern: "/avatar-create", Handler: a.handleAvatarCreate},
		{Pattern: "/avatar-create-jobs", Handler: a.handleListAvatarCreateJobs},
		{Pattern: "/voices", Handler: a.handleListVoices},
		{Pattern: "/video-jobs", Handler: a.handleListVideoJobs},
		{Pattern: "/video-jobs/", Handler: a.handleGetVideoJob},
		{Pattern: "/storage-files", Handler: a.handleStorageFiles},
		{Pattern: "/cache/", Handler: a.handleCacheGet},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "media_models",
			Description: "List available media models for a kind. Args: kind? (image|video|audio_tts|audio_sfx|music; default image). Use returned model ids in media_generate; when multiple image providers are bound, image model ids may be provider-prefixed.",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Media kind to list models for.",
					"enum":        []string{"image", "video", "audio_tts", "audio_sfx", "music"},
					"default":     "image",
				},
			}, nil),
			Handler: a.toolMediaModels,
		},
		{
			Name: "media_generate",
			Description: "Generate media (image / video / audio / music / avatar). " +
				"Args: kind (required: image|video|audio_tts|audio_sfx|music|avatar), prompt (required — " +
				"for avatar this is the spoken script), model?, size? (image), duration? (video/audio/music, seconds), " +
				"voice? (audio_tts / avatar voice override), aspect? (video), avatar? (replica/avatar id, avatar kind), " +
				"source_image? or source_images? (image edit and video references; Venice reference-to-video models support multiple refs), mode? ('generate'|'draft'), draft_id?/generation_id? to generate a saved draft, n?, options? (provider-specific extras; video supports consents.seedance when required). Video + avatar are async (queued; delivered via the " +
				"media.generated event). Returns MCP content blocks: image (thumbnail base64 for image kind only " +
				"when no storage), text (summary), resource (fetchable URL per storage_id). For chat responses, " +
				"pass the returned _meta.chat_component object unchanged in respond(components=[...]) so the generated " +
				"media appears as a live attachment.",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Discriminates which provider to invoke.",
					"enum":        []string{"image", "video", "audio_tts", "audio_sfx", "music", "avatar"},
				},
				"provider": map[string]any{"type": "string", "description": "Optional provider slug when multiple compatible providers are bound, for example elevenlabs, fish-audio, cartesia, or minimax-audio. Provider-prefixed model and voice ids are also accepted."},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text prompt (or text-to-speak when kind=audio_tts; the spoken script when kind=avatar).",
				},
				"model":    map[string]any{"type": "string", "description": "Provider model id; per-kind defaults apply if omitted."},
				"size":     map[string]any{"type": "string", "description": "Image size (image only). e.g. 1024x1024."},
				"duration": map[string]any{"type": "integer", "description": "Length in seconds (video/audio/music)."},
				"voice":    map[string]any{"type": "string", "description": "Voice id (audio_tts; avatar voice override on HeyGen)."},
				"aspect":   map[string]any{"type": "string", "description": "Aspect ratio (video only). e.g. 16:9."},
				"avatar":   map[string]any{"type": "string", "description": "Replica/avatar id (avatar kind). From the /avatars list or the provider."},
				"storage_folder": map[string]any{
					"type":        "string",
					"description": "Optional Storage output folder. Defaults to /.generated/<kind>/ when omitted. Examples: /campaigns/launch/ or personas/mira/images.",
				},
				"source_image": map[string]any{
					"type":        "string",
					"description": "Single source image reference for image.edit or image-to-video. Accepts storage:N, URL, or base64. Kept for backward compatibility.",
				},
				"source_images": map[string]any{
					"type":        "array",
					"description": "Multiple source image references for image.edit or video reference models. Accepts storage:N, URL, or base64. Media Studio validates the selected provider/model limit before calling the provider.",
					"items":       map[string]any{"type": "string"},
				},
				"n": map[string]any{"type": "integer", "default": 1, "minimum": 1, "maximum": 10},
				"cache_key": map[string]any{
					"type":        "string",
					"description": "Stable caller-provided key. When present, cache_policy=reuse returns an existing completed generation instead of regenerating.",
				},
				"cache_policy": map[string]any{
					"type":        "string",
					"enum":        []string{"reuse", "refresh"},
					"default":     "reuse",
					"description": "reuse checks completed/pending rows by cache_key; refresh bypasses cache.",
				},
				"mode": map[string]any{
					"type":        "string",
					"enum":        []string{"generate", "draft"},
					"default":     "generate",
					"description": "draft stores the generation request as an idea without calling the provider. generate is the default.",
				},
				"defer": map[string]any{
					"type":        "boolean",
					"description": "Alias for mode=draft.",
				},
				"draft_id": map[string]any{
					"type":        "integer",
					"description": "Generate a previously saved draft generation row.",
				},
				"generation_id": map[string]any{
					"type":        "integer",
					"description": "Alias for draft_id when the referenced generation row is a draft.",
				},
				"options": map[string]any{
					"type":        "object",
					"description": "Per-provider extras. Images default to JPEG below 2 MB. output_format (png|jpeg|webp) can override the format and guarantees final stored bytes match it even when the provider returns something different. Other extras include background, lyrics, style, seed, image_storage_id, background_url, fast, …",
				},
			}, []string{"kind", "prompt"}),
			Handler: a.toolMediaGenerate,
		},
		{
			Name:        "media_estimate",
			Description: "Estimate media generation cost without creating media. Args match media_generate: kind, prompt?, model?, size?, duration?, voice?, aspect?, avatar?, source_image?, source_images?, n?, options?. Returns cost_usd when the bound provider exposes pricing or Media Studio can derive it.",
			InputSchema: schemaObject(map[string]any{
				"kind":         map[string]any{"type": "string", "enum": []string{"image", "video", "audio_tts", "audio_sfx", "music", "avatar"}},
				"provider":     map[string]any{"type": "string"},
				"prompt":       map[string]any{"type": "string"},
				"model":        map[string]any{"type": "string"},
				"size":         map[string]any{"type": "string"},
				"duration":     map[string]any{"type": "integer"},
				"voice":        map[string]any{"type": "string"},
				"aspect":       map[string]any{"type": "string"},
				"avatar":       map[string]any{"type": "string"},
				"source_image": map[string]any{"type": "string"},
				"source_images": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
				"n":       map[string]any{"type": "integer", "default": 1, "minimum": 1, "maximum": 10},
				"options": map[string]any{"type": "object"},
			}, []string{"kind"}),
			Handler: a.toolMediaEstimate,
		},
		{
			Name:        "media_delete",
			Description: "Delete one media generation for this project. By default this also deletes linked Storage files, related async job rows, and local sidecar cache files. Args: id (required), delete_storage? (default true; set false to keep files in Storage).",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{
					"type":        "integer",
					"description": "Generation id to delete.",
				},
				"delete_storage": map[string]any{
					"type":        "boolean",
					"default":     true,
					"description": "Also delete files referenced by storage_ids. Defaults to true.",
				},
			}, []string{"id"}),
			Handler: a.toolMediaDelete,
		},
		{
			Name:        "media_identity_create",
			Description: "Create a reusable provider-side identity such as a voice or avatar. Voice creation is provider-neutral: use source_type=prompt for ElevenLabs or MiniMax Voice Design, or source_type=audio with source_audio/source_audios for ElevenLabs, Fish Audio, Cartesia, or MiniMax cloning. Args include provider?, name, prompt?/voice_description?, model_id?, preview_text?, language?, provider_voice_id?, source_audio?, source_audios?, transcripts?, source_image?, source_video?, labels?, options?.",
			InputSchema: schemaObject(map[string]any{
				"kind":              map[string]any{"type": "string", "enum": []string{"voice", "avatar"}},
				"provider":          map[string]any{"type": "string", "enum": []string{"elevenlabs", "fish-audio", "cartesia", "minimax-audio"}},
				"name":              map[string]any{"type": "string"},
				"source_type":       map[string]any{"type": "string", "enum": []string{"prompt", "audio", "photo", "video"}},
				"prompt":            map[string]any{"type": "string"},
				"voice_description": map[string]any{"type": "string"},
				"preview_text":      map[string]any{"type": "string", "description": "Preview speech for MiniMax Voice Design or cloning; optional for ElevenLabs."},
				"language":          map[string]any{"type": "string", "description": "Voice sample language. Required by Cartesia; defaults to en."},
				"provider_voice_id": map[string]any{"type": "string", "description": "Optional provider-side voice ID. MiniMax otherwise derives a unique ID from name."},
				"model_id": map[string]any{
					"type":        "string",
					"enum":        []string{"eleven_ttv_v3", "eleven_multilingual_ttv_v2"},
					"default":     "eleven_ttv_v3",
					"description": "ElevenLabs Voice Design model for prompt-created voices.",
				},
				"generated_voice_id": map[string]any{
					"type":        "string",
					"description": "Optional ElevenLabs generated_voice_id from a previous design/remix preview; skips preview generation and saves this preview.",
				},
				"preview_index": map[string]any{
					"type":        "integer",
					"default":     0,
					"description": "Which generated preview to save when the provider returns multiple candidates.",
				},
				"source_image": map[string]any{"type": "string"},
				"source_audio": map[string]any{
					"type":        "string",
					"description": "One consented voice sample as storage:N, HTTP(S) URL, data URL, base64, or runtime blob reference.",
				},
				"source_audios": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Multiple consented voice samples in the same formats as source_audio.",
				},
				"source_audio_filename": map[string]any{"type": "string"},
				"transcripts": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Optional transcripts corresponding to Fish Audio samples; Fish runs ASR when omitted.",
				},
				"source_video":  map[string]any{"type": "string"},
				"consent_video": map[string]any{"type": "string"},
				"labels":        map[string]any{"type": "object"},
				"options":       map[string]any{"type": "object"},
			}, []string{"kind", "name", "source_type"}),
			Handler: a.toolMediaIdentityCreate,
		},
		{
			Name:        "media_identity_list",
			Description: "List Media Studio tracked reusable identities. Args: kind? (voice|avatar), limit? (default 100, max 200).",
			InputSchema: schemaObject(map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"voice", "avatar"}},
				"limit": map[string]any{"type": "integer", "default": 100},
			}, nil),
			Handler: a.toolMediaIdentityList,
		},
		{
			Name:        "media_identity_get",
			Description: "Fetch one Media Studio tracked reusable identity by id. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolMediaIdentityGet,
		},
		{
			Name:        "media_voice_create",
			Description: "Create a reusable voice identity through any bound voice provider. Use source_type=prompt for ElevenLabs or MiniMax Voice Design, or source_type=audio plus source_audio/source_audios for generic ElevenLabs, Fish Audio, Cartesia, or MiniMax instant cloning.",
			InputSchema: schemaObject(map[string]any{
				"name":              map[string]any{"type": "string"},
				"provider":          map[string]any{"type": "string", "enum": []string{"elevenlabs", "fish-audio", "cartesia", "minimax-audio"}},
				"source_type":       map[string]any{"type": "string", "enum": []string{"prompt", "audio"}},
				"prompt":            map[string]any{"type": "string"},
				"voice_description": map[string]any{"type": "string"},
				"preview_text":      map[string]any{"type": "string"},
				"language":          map[string]any{"type": "string"},
				"provider_voice_id": map[string]any{"type": "string"},
				"model_id": map[string]any{
					"type":        "string",
					"enum":        []string{"eleven_ttv_v3", "eleven_multilingual_ttv_v2"},
					"default":     "eleven_ttv_v3",
					"description": "ElevenLabs Voice Design model for prompt-created voices.",
				},
				"generated_voice_id":    map[string]any{"type": "string"},
				"preview_index":         map[string]any{"type": "integer", "default": 0},
				"source_audio":          map[string]any{"type": "string"},
				"source_audios":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"source_audio_filename": map[string]any{"type": "string"},
				"transcripts":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"labels":                map[string]any{"type": "object"},
				"options":               map[string]any{"type": "object"},
			}, []string{"name", "source_type"}),
			Handler: a.toolMediaVoiceCreate,
		},
		{
			Name:        "media_voice_list",
			Description: "List tracked voice identities and, when an audio provider is bound, provider voice catalog entries. Args: limit?.",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer", "default": 100},
			}, nil),
			Handler: a.toolMediaVoiceList,
		},
		{
			Name:        "media_avatar_create",
			Description: "Create/train a reusable avatar through the bound avatar provider. Generic args: name (required), source_type (photo|prompt|video), source_image? (storage:N, URL, or base64 for photo), prompt? (for prompt avatars), source_video?/consent_video? (future video/digital-twin flows), options? (provider extras). Returns an async avatar_create job; refresh /avatars when it completes.",
			InputSchema: schemaObject(map[string]any{
				"name": map[string]any{
					"type":        "string",
					"description": "Display name for the avatar/replica.",
				},
				"source_type": map[string]any{
					"type":        "string",
					"enum":        []string{"photo", "prompt", "video"},
					"description": "Avatar creation source type. Provider capabilities decide which values are usable.",
				},
				"source_image": map[string]any{
					"type":        "string",
					"description": "Photo source for source_type=photo. Accepts storage:N, HTTPS URL, data URL, or base64.",
				},
				"source_video": map[string]any{
					"type":        "string",
					"description": "Training video source for future video/digital-twin providers.",
				},
				"consent_video": map[string]any{
					"type":        "string",
					"description": "Consent video source for providers that require it.",
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text prompt for source_type=prompt.",
				},
				"options": map[string]any{
					"type":        "object",
					"description": "Provider-specific extras such as avatar_group_id, reference_images, voice_name, model_name, or auto_fix_training_image.",
				},
			}, []string{"name", "source_type"}),
			Handler: a.toolMediaAvatarCreate,
		},
		{
			Name:        "media_avatar_list",
			Description: "List tracked avatar identities and, when an avatar provider is bound, provider avatar catalog entries. Args: limit?.",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer", "default": 100},
			}, nil),
			Handler: a.toolMediaAvatarList,
		},
		{
			Name:        "media_history",
			Description: "List generations for this project newest-first. Args: kind? (filter), limit? (default 50, max 200), cursor? (next_cursor from the previous page), since? (RFC3339). Returns generations, next_cursor, and has_more.",
			InputSchema: schemaObject(map[string]any{
				"kind":   map[string]any{"type": "string", "enum": []string{"image", "video", "audio_tts", "audio_sfx", "music", "avatar"}},
				"limit":  map[string]any{"type": "integer", "default": 50, "minimum": 1, "maximum": 200},
				"cursor": map[string]any{"type": "string", "description": "Opaque next_cursor returned by the previous media_history page."},
				"since":  map[string]any{"type": "string", "description": "Only include generations created at or after this RFC3339 timestamp."},
			}, nil),
			Handler: a.toolMediaHistory,
		},
		{
			Name:        "media_get",
			Description: "Fetch one generation for this project. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolMediaGet,
		},
	}
}

func main() { sdk.Run(&App{}) }

func (a *App) handleStorageFiles(w http.ResponseWriter, r *http.Request) {
	ctx := withProjectScope(globalCtx, map[string]any{"_project_id": r.URL.Query().Get("project_id")})
	if ctx == nil {
		http.Error(w, "app not mounted", http.StatusInternalServerError)
		return
	}
	folder := r.URL.Query().Get("folder")
	if folder == "" {
		folder = "/"
	}
	args := map[string]any{"folder": folder, "recursive": boolQuery(r, "recursive", false), "_project_id": projectScope(ctx)}
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
	err := ctx.PlatformAPI().CallAppResult("storage", tool, args, &out)
	filterStorageBrowserOutput(out)
	writeJSON(w, out, err)
}

// ─── generic helpers ───────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

func int64Arg(m map[string]any, key string, def int64) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func boolArg(m map[string]any, key string, def bool) bool {
	switch v := m[key].(type) {
	case bool:
		return v
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1", "yes", "y", "on":
			return true
		case "false", "0", "no", "n", "off":
			return false
		}
	}
	return def
}

const maxFetchedMediaBytes int64 = 200 << 20

func fetchBytes(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Apteva media-studio)")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errors.New("upstream non-2xx")
	}
	return readLimitedMedia(resp.Body, resp.ContentLength, maxFetchedMediaBytes)
}

func readLimitedMedia(reader io.Reader, contentLength, maxBytes int64) ([]byte, error) {
	if contentLength > maxBytes {
		return nil, fmt.Errorf("upstream media exceeds %d MiB limit", maxBytes>>20)
	}
	bytes, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(bytes)) > maxBytes {
		return nil, fmt.Errorf("upstream media exceeds %d MiB limit", maxBytes>>20)
	}
	return bytes, nil
}

// makeThumbnail JPEG-compresses image bytes to ~30KB at the given max
// edge. Best-effort; on any decode failure returns nil so the caller
// skips the image content block. Only meaningful for kind=image (and
// for video provider posters, if a provider ever returns one).
func makeThumbnail(src []byte, maxEdge int) []byte {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	scale := 1.0
	if w > maxEdge || h > maxEdge {
		if w >= h {
			scale = float64(maxEdge) / float64(w)
		} else {
			scale = float64(maxEdge) / float64(h)
		}
	}
	tw, th := int(float64(w)*scale), int(float64(h)*scale)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	thumb := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			sx := int(float64(x) / scale)
			sy := int(float64(y) / scale)
			thumb.Set(x, y, img.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
