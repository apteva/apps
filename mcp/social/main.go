// social v0.1 — multi-platform social-media publishing.
//
// Architecture:
//   - No `requires.integrations` for the platforms themselves. Accounts
//     are added at runtime via PlatformAPI.StartOAuth, which returns an
//     authorize URL the panel/agent hands the user. After the dance,
//     the platform 302s the browser back to /accounts/oauth_done with
//     conn_id=<id>; we look up the matching pending_accounts row and
//     either auto-finalize (Twitter, LinkedIn personal) or show a
//     page-picker (Facebook, Instagram, YouTube).
//   - Operator-bound deps: storage (optional, for media bytes) and
//     jobs (optional, for durable scheduling). Without them, scheduled
//     posts publish synchronously when the local worker tick fires.
//   - One social_accounts row per "destination" (a Twitter handle, a
//     FB Page, an IG business account); rows can share connection_id
//     when one OAuth grant covers many destinations.
//   - Post fanout: post → N post_targets → N independent publish
//     attempts. A TikTok failure doesn't block X.
package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: social
display_name: Social
version: 0.6.1
description: |
  Schedule and publish posts to your social accounts (X, Facebook,
  Instagram, LinkedIn, TikTok, YouTube, Reddit, Pinterest, Threads).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.connections.manage
    - platform.oauth.start
    - platform.apps.call
  integrations:
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write]
      required: false
      label: "Storage (optional)"
    - role: jobs
      kind: app
      compatible_app_names: [jobs]
      capabilities: [jobs.schedule]
      required: false
      label: "Jobs (optional)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: account_add,                description: "Begin OAuth for a platform." }
    - { name: account_list_pending_pages, description: "List selectable pages/channels for a pending account." }
    - { name: account_finalize,           description: "Commit a pending account into the active list." }
    - { name: account_list,               description: "List connected social accounts." }
    - { name: account_disconnect,         description: "Revoke a social account." }
    - { name: post_create,                description: "Create + publish (or schedule) a post across accounts." }
    - { name: post_list,                  description: "List recent posts." }
    - { name: post_retry,                 description: "Re-attempt failed targets on a post." }
  ui_panels:
    - slot: project.page
      label: Social
      icon: megaphone
      entry: /ui/SocialPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/social
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/social.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// platformDef captures everything app-side we need to know about a
// supported social network: the underlying integration slug, the tool
// names we call for "post" and (optionally) "list pages", and the
// per-platform field-name remapping we need because every API names
// the body / destination differently.
type platformDef struct {
	Platform        string // user-facing key: "twitter", "facebook", ...
	IntegrationSlug string // catalog slug: "twitter-api", "facebook-api", ...
	DisplayName     string
	// PostTool — integration tool that publishes a post.
	PostTool string
	// PublishTool — second-step tool for two-step flows (Instagram).
	// Empty for single-step platforms.
	PublishTool string
	// Strategy — how the publish flow runs. "single" (default) calls
	// PostTool with a flat body+media+external_id; "instagram_two_step"
	// runs create_media_container then publish_media_container; "tiktok"
	// uses the nested {post_info, source_info} shape.
	Strategy string
	// BodyField — name the post tool's input schema uses for the post
	// body (Twitter: "text", Facebook: "message", LinkedIn: "commentary"…).
	BodyField string
	// MediaURLField — where to put a media URL in the post-tool input.
	// Empty when the platform's flow doesn't take a URL parameter
	// (TikTok nests it under source_info.video_url; handled by Strategy).
	MediaURLField string
	// ExternalIDField — name the post tool's input schema uses for the
	// destination id when applicable. Empty when the platform has no
	// destination concept (Twitter personal). Examples: "pageId" (FB),
	// "instagramAccountId" (Instagram).
	ExternalIDField string
	// MediaRequired — when true, the platform refuses text-only posts
	// (TikTok, Instagram, YouTube). Targets without media_storage_ids
	// are marked failed up-front with a clear message.
	MediaRequired bool
	// MediaType — "image", "video", or "any". Used by validation +
	// future per-platform media-prep (resize, transcode, etc).
	MediaType string
	// ListPagesTool — integration tool that lists destinations after
	// OAuth completes. Empty when the platform has only one possible
	// destination (Twitter, LinkedIn personal). When set, the panel
	// shows a picker before finalizing the account.
	ListPagesTool string
	// PageIDField / PageNameField / PageAvatarField — JSONPath-like
	// field names in the list_pages response so we can normalise
	// across platforms without hard-coding each shape in the panel.
	PageIDField     string
	PageNameField   string
	PageAvatarField string
	// ListPagesArgs — optional input passed to ListPagesTool. Lets
	// each platform request the upstream-specific fields it needs
	// (e.g. Facebook's /me/accounts only returns id+name unless we
	// ask for picture explicitly via fields=...). Nil → empty map.
	ListPagesArgs map[string]any
	// PageAccessTokenField — JSONPath in the list_pages response that
	// holds a page-level access token. Facebook rejects user-level
	// tokens for /feed writes (error 210), so we capture the per-page
	// token at finalize time and re-send it on every publish via
	// PostTokenInputField. Empty when the platform reuses the user
	// token for writes (Twitter, TikTok).
	PageAccessTokenField string
	// PostTokenInputField — name of the input field on PostTool that
	// carries the page access token. Empty when not needed.
	PostTokenInputField string
	// VideoPostTool / VideoMediaURLField — when the platform splits
	// image and video posting across separate tools (Facebook: text →
	// post_to_page on /feed, photo → post_photo_to_page on /photos,
	// video → post_video on /videos), set these so publishSingle can
	// switch on the media MIME. Empty means "use PostTool for
	// everything" (Twitter: text-only or any-MIME via the same tool).
	PhotoPostTool      string
	PhotoMediaURLField string
	PhotoBodyField     string // overrides BodyField when posting a photo
	VideoPostTool      string
	VideoMediaURLField string
	VideoBodyField     string // overrides BodyField when posting a video
	// ProfileTool — integration tool that returns the authorising
	// user's own identity (used to seed display_name/avatar for
	// platforms without page-selection). Empty = use a default label.
	ProfileTool          string
	ProfileNameField     string
	ProfileAvatarField   string
	// ProfileToolArgs — optional input passed to ProfileTool. YouTube's
	// get_my_channel needs `part=snippet` (it's a `required` field on
	// the integration's input schema; without it the upstream Graph
	// returns 400 and the profile fetch silently fails). Most platforms
	// can leave this nil — Twitter's get_me, TikTok's get_creator_info
	// take no inputs.
	ProfileToolArgs map[string]any
	// DeleteTool — integration tool that removes an already-published
	// post from the upstream platform. Empty when the platform's API
	// doesn't permit it (Instagram media, TikTok videos) or when the
	// catalog hasn't grown the verb yet (LinkedIn, Reddit, Threads).
	// When empty, post_delete still removes the local rows but leaves
	// the upstream copy in place.
	DeleteTool    string
	DeleteIDField string // input field carrying platform_post_id ("tweet_id", "postId", "id"…)
	// OptionFields declares the per-platform override keys that can
	// appear under post.platform_options[platform]. Drives both the
	// /platforms endpoint (so the UI knows what controls to render)
	// and tool-level validation (unknown keys get a warn-but-accept).
	// Empty for platforms with no overrides today (Twitter, FB, IG,
	// LinkedIn, TikTok in v1).
	OptionFields []optionField
}

// optionField describes one customizable knob on a platform — its key
// name (matches what publish strategies read), a UI-friendly label,
// the input type the panel should render, and an enum of allowed
// values when applicable.
type optionField struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "text" | "textarea" | "select" | "tags"
	Options []string `json:"options,omitempty"`
	Help    string   `json:"help,omitempty"`
}

// platforms is the static registry. v0.1 ships with two — Twitter
// (no page selection, simplest) + Facebook (with page selection,
// proves the abstraction). Adding LinkedIn / TikTok / YouTube / Reddit /
// Pinterest / Threads is "add a row here + ensure the integration
// exposes the named tools" — no other code change.
var platforms = map[string]platformDef{
	"twitter": {
		Platform:           "twitter",
		IntegrationSlug:    "twitter-api",
		DisplayName:        "X (Twitter)",
		Strategy:           "single",
		PostTool:           "post_tweet",
		BodyField:          "text",
		MediaType:          "any",
		ProfileTool:        "get_me",
		ProfileNameField:   "username",
		ProfileAvatarField: "profile_image_url",
		DeleteTool:         "delete_tweet",
		DeleteIDField:      "tweet_id",
	},
	"facebook": {
		Platform:        "facebook",
		IntegrationSlug: "facebook-api",
		DisplayName:     "Facebook Page",
		Strategy:        "single",
		PostTool:        "post_to_page",
		BodyField:       "message",
		MediaURLField:   "image", // post_to_page accepts {message, image} for photo posts
		ExternalIDField: "pageId",
		MediaType:       "image",
		ListPagesTool:   "list_pages",
		PageIDField:     "id",
		PageNameField:   "name",
		PageAvatarField: "picture.data.url",
		// /me/accounts defaults to id+name. Ask for the page logo
		// (picture nested under data.url), category for context, and
		// access_token because Facebook rejects user-level tokens for
		// /feed writes (error 210, A page access token is required).
		ListPagesArgs: map[string]any{
			"fields": "id,name,category,picture{url},access_token",
		},
		PageAccessTokenField: "access_token",
		PostTokenInputField:  "access_token",
		// Three Graph endpoints, one per media kind:
		//   - text/link posts → /{pageId}/feed       ('message')
		//   - photo posts     → /{pageId}/photos     ('caption' + 'url')
		//   - video posts     → /{pageId}/videos     ('description' + 'file_url')
		// /feed silently ignores 'image' fields, which is why a
		// photo attached via post_to_page would publish without the
		// image attached — switching to post_photo_to_page fixes that.
		PhotoPostTool:      "post_photo_to_page",
		PhotoMediaURLField: "url",
		PhotoBodyField:     "caption",
		VideoPostTool:      "post_video",
		VideoMediaURLField: "file_url",
		VideoBodyField:     "description",
		// Graph DELETE /{pageId}_{postId} — the platform_post_id we
		// stored from post_to_page is already in that exact form. The
		// page-level access_token is forwarded via PostTokenInputField
		// (same pattern + same field name as the post path).
		DeleteTool:    "facebook_delete_post",
		DeleteIDField: "postId",
	},
	"instagram": {
		Platform: "instagram",
		// IG Business is a Meta product — the underlying API is the
		// Facebook Graph, and IG Business accounts are reached via the
		// FB Pages they're linked to. Reuse the facebook-api integration
		// here: its OAuth scopes already include instagram_basic +
		// instagram_content_publish, and its list_pages tool returns the
		// linked IG account when we ask for the right fields. This means
		// users with an existing FB connection get IG accounts auto-
		// discovered without a second OAuth dance.
		IntegrationSlug: "facebook-api",
		DisplayName:     "Instagram Business",
		// Two-step: create_media_container({imageUrl|videoUrl, caption,
		// instagramAccountId}) then publish_media_container({containerId,
		// instagramAccountId}). Caption is the body; media required.
		Strategy:        "instagram_two_step",
		PostTool:        "create_media_container",
		PublishTool:     "publish_media_container",
		BodyField:       "caption",
		MediaURLField:   "image_url",
		ExternalIDField: "instagramAccountId",
		MediaRequired:   true,
		MediaType:       "any", // images + REELS via same two-step
		ListPagesTool:   "list_pages",
		// /me/accounts on the FB Graph returns Pages; we ask for each
		// page's linked instagram_business_account so the picker can
		// surface IG accounts directly. PageIDField walks into that
		// nested object. Pages without a linked IG account are filtered
		// out by fetchPages (entry.ID == "" → skip).
		PageIDField:     "instagram_business_account.id",
		PageNameField:   "instagram_business_account.username",
		PageAvatarField: "instagram_business_account.profile_picture_url",
		ListPagesArgs: map[string]any{
			"fields": "name,access_token,instagram_business_account{id,username,profile_picture_url}",
		},
		// IG Business writes also need the page-level access_token
		// (the token belongs to the Facebook Page that owns the IG
		// account, not the user-level token).
		PageAccessTokenField: "access_token",
		PostTokenInputField:  "access_token",
	},
	"tiktok": {
		Platform:        "tiktok",
		IntegrationSlug: "tiktok-api",
		DisplayName:     "TikTok",
		// TikTok's input is nested: {post_info: {title}, source_info:
		// {source: "PULL_FROM_URL", video_url}}. The "tiktok" strategy
		// builds that shape from our flat (body, media_url) inputs.
		Strategy:           "tiktok",
		PostTool:           "post_video",
		BodyField:          "title", // logical, lifted into post_info.title
		MediaRequired:      true,
		MediaType: "video",
		// TikTok's catalog has no "get_creator_info" tool (an older name
		// that never existed); the right primitive for our profile-fetch
		// use case is /user/info/ via get_user_info — same scope
		// (user.info.basic) but returns the display_name + avatar_url we
		// want. The response wraps the user fields under data.user, hence
		// the dotted ProfileNameField/ProfileAvatarField paths. fetchProfile
		// already strips one level of `data` envelope; the second hop into
		// `user` is encoded in the path expressions.
		ProfileTool:        "get_user_info",
		ProfileNameField:   "user.display_name",
		ProfileAvatarField: "user.avatar_url",
		// /user/info/ requires a comma-separated fields query param —
		// no default applied by the executor, so we must pass it.
		ProfileToolArgs: map[string]any{
			"fields": "open_id,display_name,avatar_url",
		},
	},
	"youtube": {
		Platform:        "youtube",
		IntegrationSlug: "youtube-api",
		DisplayName:     "YouTube",
		// YouTube's upload_video uses a resumable session — we'd need a
		// multi-call dance the integration doesn't expose as a single
		// tool. v0.1 surfaces YouTube as a connectable account so the
		// channel flow is testable, but post_create returns a clear
		// "video upload coming in v0.2" error for now.
		//
		// No page picker: the youtube-api integration only exposes
		// get_my_channel (singular) — there's no list_channels tool
		// to drive a picker against. One Google OAuth = one channel
		// under the standard scope, so YT behaves like Twitter/TikTok
		// (single-account, always fresh OAuth, no picker step).
		// Resumable upload: publishYoutube calls upload_video_init (POSTs
		// metadata, gets back a session URL via the Location response
		// header) then PUTs the bytes directly to that session URL —
		// the bytes-PUT bypasses the integration system because Google's
		// session URLs are pre-authorized.
		Strategy:           "youtube",
		PostTool:           "upload_video_init",
		MediaRequired:      true,
		MediaType:          "video",
		ProfileTool:        "get_my_channel",
		ProfileNameField:   "snippet.title",
		ProfileAvatarField: "snippet.thumbnails.default.url",
		// `part` is required by YouTube Data API v3; just snippet is
		// enough for the title + thumbnails we surface.
		ProfileToolArgs: map[string]any{"part": "snippet"},
		// Wired in advance of v0.2 upload support. With current upload
		// strategy returning an error, no published platform_post_id
		// is recorded for YouTube targets, so this branch stays dormant
		// until upload lands — no harm in pre-wiring it.
		DeleteTool:    "delete_video",
		DeleteIDField: "id",
		// publishYoutube reads these keys from posts.platform_options.youtube
		// at publish time. `body` overrides the post-level body when
		// populating snippet.description; `title` is required upstream
		// so we fall back to first 80 chars of body when missing.
		OptionFields: []optionField{
			{Name: "title", Type: "text", Label: "Title",
				Help: "Required by YouTube. Falls back to the first 80 chars of body when blank. Max 100 chars."},
			{Name: "body", Type: "textarea", Label: "Description",
				Help: "Shown on the video page. Falls back to the post body when blank."},
			{Name: "visibility", Type: "select", Label: "Visibility",
				Options: []string{"public", "unlisted", "private"},
				Help: "Defaults to private if blank — safer for first-pass uploads."},
			{Name: "category", Type: "text", Label: "Category ID",
				Help: "YouTube numeric category id (e.g. 22 = People & Blogs, 27 = Education). Optional."},
		},
	},
}

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
		return errors.New("social requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("social mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (panel) ───────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Account management
		{Pattern: "/accounts", Handler: a.handleAccountsAPI},
		{Pattern: "/accounts/start", Handler: a.handleAccountsStart},
		{Pattern: "/accounts/oauth_done", Handler: a.handleOAuthDone},
		{Pattern: "/accounts/finalize", Handler: a.handleAccountsFinalize},
		{Pattern: "/accounts/", Handler: a.handleAccountsItem}, // /accounts/:id (DELETE) and /accounts/:id/pages (GET)
		// Post management
		{Pattern: "/posts", Handler: a.handlePostsAPI},
		{Pattern: "/posts/", Handler: a.handlePostsItem}, // /posts/:id and /posts/:id/retry
		// Static info
		{Pattern: "/platforms", Handler: a.handlePlatforms},
		// Profiles (brand/client/site containers — see profiles.go)
		{Pattern: "/profiles", Handler: a.handleProfilesCollection},
		{Pattern: "/profiles/", Handler: a.handleProfilesItem},
		// Avatar cache — content-addressed by sha256 so we can serve
		// without a DB lookup. Lives next to the sidecar's sqlite at
		// data/avatars/. Invisible to users + agents (no list / search
		// route). Cleaned up on account_disconnect.
		{Pattern: "/avatars/", Handler: a.handleAvatar},
		// Jobs callback — the jobs app POSTs here when a scheduled
		// publish fires. Body: {"post_id": N}. Idempotent per post:
		// running it twice on a published post is a no-op (publishPostTargets
		// only acts on status='pending' targets).
		{Pattern: "/jobs/publish_post", Handler: a.handleJobPublishPost},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
		{
			Name: "account_add",
			Description: "Begin connecting a social account. " +
				"For multi-page platforms (Facebook, Instagram, YouTube) with an existing active connection in this project, the call SKIPS OAuth — the existing access token already covers all the user's pages/channels — and returns a pending_account_id directly. The caller goes straight to account_list_pending_pages without opening a browser. " +
				"Otherwise returns authorize_url + pending_account_id and the human must visit the URL. " +
				"The integrations system handles token exchange + refresh; this app never sees the access token. " +
				"Args: platform (twitter|facebook|instagram|linkedin|tiktok|youtube|reddit|pinterest|threads), force_new? (default false; set true to force a fresh OAuth dance even when an existing connection is available, e.g. to switch to a different provider-side account).",
			InputSchema: schemaObject(map[string]any{
				"platform":  map[string]any{"type": "string", "enum": platformKeys()},
				"force_new": map[string]any{"type": "boolean"},
				"return_to": map[string]any{
					"type":        "string",
					"description": "Where to redirect the browser after OAuth. Defaults to the social app's panel.",
				},
			}, []string{"platform"}),
			Handler: a.toolAccountAdd,
		},
		{
			Name:        "account_list_pending_pages",
			Description: "After OAuth completes, list the pages/channels/accounts the user can pick from. Empty result means the platform has no setup step (e.g. Twitter personal) and you can call account_finalize directly. Args: pending_account_id.",
			InputSchema: schemaObject(map[string]any{
				"pending_account_id": map[string]any{"type": "integer"},
			}, []string{"pending_account_id"}),
			Handler: a.toolAccountListPendingPages,
		},
		{
			Name:        "account_finalize",
			Description: "Commit a pending account into the active social_accounts list. For multi-page platforms (Facebook, Instagram, YouTube) supply page_id from account_list_pending_pages; for personal platforms (Twitter, LinkedIn personal) page_id is optional. Args: pending_account_id, page_id?, name?.",
			InputSchema: schemaObject(map[string]any{
				"pending_account_id": map[string]any{"type": "integer"},
				"page_id":            map[string]any{"type": "string"},
				"name":               map[string]any{"type": "string"},
			}, []string{"pending_account_id"}),
			Handler: a.toolAccountFinalize,
		},
		{
			Name:        "account_list",
			Description: "List connected social accounts in this project. Args: platform? (filter), status? (active|needs_reauth).",
			InputSchema: schemaObject(map[string]any{
				"platform": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolAccountList,
		},
		{
			Name:        "account_disconnect",
			Description: "Revoke a social account, deleting both the social_accounts row and the underlying connection. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolAccountDisconnect,
		},
		{
			Name: "post_create",
			Description: "Create a post and publish (or schedule) it to N social accounts. " +
				"Pass EITHER social_account_ids[] (simple multicast — every target uses the post body) " +
				"OR targets[] (when you want per-target overrides). The two are mutually exclusive. " +
				"Each target object: {social_account_id (required), body? (override text for this target), " +
				"plus platform-specific keys for the target's platform}. " +
				"Today: youtube accepts {title, body, visibility (public|unlisted|private), category, tags[]}. " +
				"Other platforms (twitter, facebook, instagram, linkedin, tiktok) accept just {body}. " +
				"Body resolution per target: target.body if set, else post-level body. " +
				"Args: body, schedule_at? (RFC3339; omit = post now), media_storage_ids? (file ids).",
			InputSchema: schemaObject(map[string]any{
				"body":               map[string]any{"type": "string"},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"targets": map[string]any{
					"type":        "array",
					"description": "Per-target overrides. Each entry: {social_account_id (required), body?, plus platform-specific keys like title/visibility for YouTube}. Mutually exclusive with social_account_ids.",
					"items":       map[string]any{"type": "object"},
				},
				"schedule_at":       map[string]any{"type": "string"},
				"media_storage_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"body"}),
			Handler: a.toolPostCreate,
		},
		{
			Name:        "post_list",
			Description: "List recent posts with per-target status. Args: limit? (default 50, max 200), status? (filter).",
			InputSchema: schemaObject(map[string]any{
				"limit":  map[string]any{"type": "integer", "default": 50},
				"status": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolPostList,
		},
		{
			Name:        "post_retry",
			Description: "Re-attempt every failed target on a post (resets attempts to 0 and re-publishes). Args: post_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
			}, []string{"post_id"}),
			Handler: a.toolPostRetry,
		},
		{
			Name:        "post_reschedule",
			Description: "Change a scheduled post's run time. Cancels the existing jobs row and creates a fresh one — only valid while status='scheduled' (already-published or in-flight posts are immutable). Args: post_id, schedule_at (RFC3339 or datetime-local).",
			InputSchema: schemaObject(map[string]any{
				"post_id":     map[string]any{"type": "integer"},
				"schedule_at": map[string]any{"type": "string"},
			}, []string{"post_id", "schedule_at"}),
			Handler: a.toolPostReschedule,
		},
		{
			Name:        "post_delete",
			Description: "Delete a post locally and, where the platform allows it, remove the upstream copy too. For each published target the app calls the platform's delete verb (Twitter, Facebook, YouTube wired today; LinkedIn/Reddit/Threads pending catalog work; Instagram and TikTok don't permit programmatic deletion of posted media). The local rows always go regardless of upstream outcome — the response includes a per-target `upstream` array (status: deleted | unsupported | skipped | failed) so callers can flag platforms that still hold a copy. Cancels any scheduled job first. Args: post_id, force_local_only? (skip all upstream calls; default false).",
			InputSchema: schemaObject(map[string]any{
				"post_id":          map[string]any{"type": "integer"},
				"force_local_only": map[string]any{"type": "boolean", "description": "Skip upstream platform deletion; only remove local rows. Default false."},
			}, []string{"post_id"}),
			Handler: a.toolPostDelete,
		},
		{
			Name: "post_metrics",
			Description: "Fetch fresh per-target performance metrics for a post by fanning out to each platform's analytics tool. Returns a per-target array of {status: ok|unsupported|skipped|failed, metrics?: {views, likes, comments, shares, raw}, reason?, error?}. " +
				"Wired today: Twitter, YouTube, TikTok. Other platforms return status=unsupported until their analytics tools are wired. " +
				"Direct calls — no caching, no DB writes. Be mindful of upstream rate limits when looping over many posts. Args: post_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
			}, []string{"post_id"}),
			Handler: a.toolPostMetrics,
		},
		{
			Name: "account_metrics",
			Description: "Fetch account-level totals (followers, total likes/videos where available) for one connected social account. " +
				"Wired today: YouTube (subscriberCount, videoCount), TikTok (follower_count, following_count, likes_count, video_count). " +
				"Other platforms return status=unsupported. Args: social_account_id, period? (reserved for time-windowed metrics; ignored today).",
			InputSchema: schemaObject(map[string]any{
				"social_account_id": map[string]any{"type": "integer"},
				"period":            map[string]any{"type": "string", "description": "Optional time window like \"7d\" or \"30d\". Reserved for future use; ignored today."},
			}, []string{"social_account_id"}),
			Handler: a.toolAccountMetrics,
		},
	}
	tools = append(tools, a.profileTools()...)
	return tools
}

func main() { sdk.Run(&App{}) }

// ─── account_add ───────────────────────────────────────────────────

func (a *App) toolAccountAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	plat, _ := args["platform"].(string)
	def, ok := platforms[plat]
	if !ok {
		return mcpError(fmt.Sprintf("unsupported platform %q — available: %s", plat, strings.Join(platformKeys(), ", "))), nil
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	forceNew, _ := args["force_new"].(bool)
	// Profile assignment for the new account. resolveProfileArg
	// returns -1 if a non-empty slug doesn't resolve; that's a
	// caller error (typo / wrong project) — surface it loudly
	// instead of silently widening to "unassigned".
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project — call profile_list to see available slugs", args["profile"])), nil
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid) // 0 if no default = leaves account unassigned
	}

	// Reuse path: if there's already an active social_account for this
	// platform in this project, the underlying connection's access
	// token already grants what we need (Facebook covers all the
	// user's Pages, Google all the channels, etc.). Skip the OAuth
	// dance entirely — create a pending_accounts row pre-linked to
	// the existing connection, mark it ready, return without an
	// authorize_url. The panel goes straight to the page picker.
	//
	// Skipped when:
	//   - force_new=true (operator wants to switch to a different
	//     provider-side account)
	//   - the platform has no picker step (Twitter, LinkedIn-personal)
	//     because reusing a connection doesn't add anything there;
	//     a fresh OAuth is the right thing
	if !forceNew && def.ListPagesTool != "" {
		// Two reuse sources, in order:
		//   1. A prior social_account in this project — its connection
		//      was already vetted, just open the picker against it.
		//   2. An operator-installed integration connection for this
		//      platform's app_slug. The access token already grants
		//      list_pages / list_channels / list_accounts, so there's
		//      no point running another OAuth dance — fresh OAuth would
		//      just produce a second connection with the same scope.
		//
		// Source #2 is critical for first-time use: the operator
		// installs Facebook in Settings → Integrations, then opens the
		// Social panel and clicks Add Account. Without #2 we'd force a
		// pointless re-auth before showing pages they could already see.
		var existingConnID int64
		var reuseSrc string
		err := ctx.AppDB().QueryRow(
			`SELECT connection_id FROM social_accounts
			 WHERE project_id=? AND platform=? AND status='active'
			 ORDER BY id DESC LIMIT 1`,
			pid, def.Platform,
		).Scan(&existingConnID)
		if err == nil && existingConnID > 0 {
			reuseSrc = "social_accounts"
		} else {
			conns, lerr := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
				ProjectID: pid,
				AppSlug:   def.IntegrationSlug,
			})
			ctx.Logger().Info("account_add: probing operator connections",
				"platform", plat, "slug", def.IntegrationSlug, "project_id", pid,
				"count", len(conns), "list_err", lerr)
			if lerr == nil {
				for _, c := range conns {
					ctx.Logger().Info("account_add: candidate connection",
						"id", c.ID, "slug", c.AppSlug, "status", c.Status, "project_id", c.ProjectID)
					if c.Status == "active" {
						existingConnID = c.ID
						reuseSrc = "operator"
						break
					}
				}
			}
		}
		ctx.Logger().Info("account_add: reuse decision",
			"platform", plat, "existing_conn_id", existingConnID, "reuse_source", reuseSrc)
		if existingConnID > 0 {
			res, err := ctx.AppDB().Exec(
				`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at, profile_id)
				 VALUES (?, ?, ?, ?, 'ready', ?, ?)`,
				pid, def.Platform, def.IntegrationSlug, existingConnID,
				time.Now().UTC().Add(10*time.Minute), profileID,
			)
			if err != nil {
				return nil, fmt.Errorf("create pending account (reuse path): %w", err)
			}
			pendingID, _ := res.LastInsertId()
			return map[string]any{
				"pending_account_id": pendingID,
				"platform":           def.Platform,
				"reused_connection":  existingConnID,
				"instructions": fmt.Sprintf(
					"Reusing the existing %s connection — no new OAuth needed. Call account_list_pending_pages with pending_account_id=%d to see selectable items.",
					def.DisplayName, pendingID,
				),
			}, nil
		}
		// fall through to fresh OAuth path
	}

	// Build the panel landing URL. Whether the request came from an
	// agent (MCP tool) or from the panel's "Add account" button, the
	// platform redirects there; the panel JS reads ?conn_id and either
	// finalizes immediately (no page-selection) or shows the picker.
	returnTo, _ := args["return_to"].(string)
	if returnTo == "" {
		returnTo = "/api/apps/social/accounts/oauth_done"
	}

	// Pre-create the pending row so we have a stable id we can hand
	// the agent. It'll be linked to the connection once OAuth completes.
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at, profile_id)
		 VALUES (?, ?, ?, 'pending_oauth', ?, ?)`,
		pid, def.Platform, def.IntegrationSlug, now.Add(10*time.Minute), profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("create pending account: %w", err)
	}
	pendingID, _ := res.LastInsertId()

	// Embed the pending id in the return_url so the OAuth callback
	// landing page knows which row to graduate.
	sep := "?"
	if strings.Contains(returnTo, "?") {
		sep = "&"
	}
	returnURL := fmt.Sprintf("%s%spending=%d", returnTo, sep, pendingID)

	out, err := ctx.PlatformAPI().StartOAuth(sdk.OAuthStartRequest{
		IntegrationSlug: def.IntegrationSlug,
		ReturnURL:       returnURL,
		Name:            fmt.Sprintf("social:%s:%d", def.Platform, pendingID),
		ProjectID:       pid,
	})
	if err != nil {
		// Roll the pending row back so we don't leak orphaned rows.
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		return mcpError("OAuth start failed: " + err.Error()), nil
	}

	return map[string]any{
		"pending_account_id": pendingID,
		"platform":           def.Platform,
		"authorize_url":      out.AuthorizeURL,
		"expires_at":         out.ExpiresAt,
		"instructions": fmt.Sprintf(
			"Open this URL in a browser to authorize %s: %s\n\nAfter you click Allow, the page will redirect back automatically. "+
				"Then call account_list_pending_pages with pending_account_id=%d to see selectable pages, or call account_finalize directly if the platform has no setup step.",
			def.DisplayName, out.AuthorizeURL, pendingID,
		),
	}, nil
}

// ─── account_list_pending_pages ────────────────────────────────────

func (a *App) toolAccountListPendingPages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pendingID := intArg(args, "pending_account_id", 0)
	if pendingID <= 0 {
		return nil, errors.New("pending_account_id required")
	}
	row, err := a.getPending(int64(pendingID))
	if err != nil {
		ctx.Logger().Warn("list_pending_pages: pending row missing", "pending_id", pendingID, "err", err)
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.connectionID == 0 {
		ctx.Logger().Warn("list_pending_pages: connection_id=0", "pending_id", pendingID, "platform", row.platform)
		return mcpError("OAuth not yet complete — open the authorize_url first, then re-call this tool"), nil
	}
	def := platforms[row.platform]
	if def.ListPagesTool == "" {
		ctx.Logger().Info("list_pending_pages: no picker required",
			"pending_id", pendingID, "platform", row.platform, "conn_id", row.connectionID)
		return map[string]any{
			"pages":           []any{},
			"requires_picker": false,
			"platform":        row.platform,
			"hint":            fmt.Sprintf("%s has no page-selection step — call account_finalize with this pending_account_id (no page_id needed)", def.DisplayName),
		}, nil
	}
	ctx.Logger().Info("list_pending_pages: calling fetchPages",
		"pending_id", pendingID, "platform", row.platform, "conn_id", row.connectionID, "tool", def.ListPagesTool)
	pages, err := a.fetchPages(ctx, row.connectionID, def)
	if err != nil {
		ctx.Logger().Error("list_pending_pages: fetchPages failed",
			"pending_id", pendingID, "platform", row.platform, "conn_id", row.connectionID, "err", err)
		return mcpError("fetch pages failed: " + err.Error()), nil
	}
	ctx.Logger().Info("list_pending_pages: returning pages",
		"pending_id", pendingID, "platform", row.platform, "page_count", len(pages))
	return map[string]any{
		"pages":           pages,
		"requires_picker": true,
		"platform":        row.platform,
	}, nil
}

// ─── account_finalize ─────────────────────────────────────────────

func (a *App) toolAccountFinalize(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pendingID := intArg(args, "pending_account_id", 0)
	if pendingID <= 0 {
		return nil, errors.New("pending_account_id required")
	}
	row, err := a.getPending(int64(pendingID))
	if err != nil {
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.connectionID == 0 {
		return mcpError("OAuth not yet complete"), nil
	}
	def := platforms[row.platform]
	pageID, _ := args["page_id"].(string)
	displayName, _ := args["name"].(string)
	avatar := ""
	pageCredsJSON := ""

	if def.ListPagesTool != "" {
		// Multi-page platform — page_id is required; resolve display
		// name + avatar from the freshly-fetched page list (a deliberate
		// extra round-trip to avoid trusting the agent's `name` arg).
		if pageID == "" {
			return mcpError("page_id is required for " + def.DisplayName), nil
		}
		pages, err := a.fetchPages(ctx, row.connectionID, def)
		if err != nil {
			return mcpError("fetch pages failed: " + err.Error()), nil
		}
		var found *pageEntry
		for i := range pages {
			if pages[i].ID == pageID {
				found = &pages[i]
				break
			}
		}
		if found == nil {
			return mcpError("page_id not in the user's accessible pages — re-call account_list_pending_pages"), nil
		}
		if displayName == "" {
			displayName = found.Name
		}
		avatar = found.Avatar
		// Capture the page-level access token for write operations.
		// For Facebook this is mandatory — /feed writes with the user
		// token return error 210.
		if found.AccessToken != "" {
			pageCreds, _ := json.Marshal(map[string]string{
				def.PageAccessTokenField: found.AccessToken,
			})
			pageCredsJSON = string(pageCreds)
		}
	} else if def.ProfileTool != "" {
		// Single-account platform — pull profile via the integration so
		// the panel has something nicer than "social:twitter:42".
		profile, _ := a.fetchProfile(ctx, row.connectionID, def)
		if displayName == "" && profile != nil {
			displayName = profile.Name
		}
		if profile != nil {
			avatar = profile.Avatar
		}
	}
	if displayName == "" {
		displayName = def.DisplayName
	}
	// Replace the upstream signed URL with our content-addressed local
	// cache. cacheAvatar falls back to the upstream URL on any error
	// so finalize never breaks here — broken thumbnails are recoverable
	// by reconnecting; a failed finalize isn't.
	avatar = a.cacheAvatar(ctx, avatar)

	// Insert the finalized social_account row.
	pid := os.Getenv("APTEVA_PROJECT_ID")
	// Profile assignment: use the value the operator set on the
	// pending row at account_add time, falling back to the project's
	// current default if 0. Resolves the case where the default was
	// promoted between account_add and finalize.
	profileID := row.profileID
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, avatar_url, status, page_credentials, profile_id)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		pid, def.Platform, row.connectionID, nullable(pageID), displayName, nullable(avatar), pageCredsJSON, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert social_account: %w", err)
	}
	id, _ := res.LastInsertId()
	_, _ = ctx.AppDB().Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, pendingID)

	ctx.Emit("account.added", map[string]any{
		"social_account_id": id,
		"platform":          def.Platform,
		"display_name":      displayName,
	})

	return map[string]any{
		"social_account_id":   id,
		"platform":            def.Platform,
		"display_name":        displayName,
		"avatar_url":          avatar,
		"external_account_id": pageID,
	}, nil
}

// ─── account_list ─────────────────────────────────────────────────

func (a *App) toolAccountList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	platformFilter, _ := args["platform"].(string)
	statusFilter, _ := args["status"].(string)
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
	}
	q := `SELECT id, platform, connection_id, COALESCE(external_account_id,''), display_name,
	             COALESCE(avatar_url,''), status, created_at, COALESCE(profile_id,0)
	      FROM social_accounts WHERE project_id=?`
	qArgs := []any{pid}
	if platformFilter != "" {
		q += " AND platform=?"
		qArgs = append(qArgs, platformFilter)
	}
	if statusFilter != "" {
		q += " AND status=?"
		qArgs = append(qArgs, statusFilter)
	}
	if profileID > 0 {
		q += " AND profile_id=?"
		qArgs = append(qArgs, profileID)
	}
	q += " ORDER BY id DESC"
	rows, err := ctx.AppDB().Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, connID, profID                                    int64
			platform, externalID, name, avatar, status, createdAt string
		)
		if err := rows.Scan(&id, &platform, &connID, &externalID, &name, &avatar, &status, &createdAt, &profID); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                  id,
			"platform":            platform,
			"connection_id":       connID,
			"external_account_id": externalID,
			"display_name":        name,
			"avatar_url":          avatar,
			"status":              status,
			"created_at":          createdAt,
			"profile_id":          profID,
		})
	}
	return map[string]any{"accounts": out}, nil
}

// ─── account_disconnect ──────────────────────────────────────────

func (a *App) toolAccountDisconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return nil, errors.New("id required")
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var connID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT connection_id FROM social_accounts WHERE id=? AND project_id=?`,
		id, pid,
	).Scan(&connID); err != nil {
		return mcpError("account not found"), nil
	}

	// Delete the social_accounts row first so the panel reflects it
	// immediately even if the platform-side disconnect lags. If any
	// other social_accounts rows share this connection_id (multi-page
	// FB grant), keep the connection alive.
	if _, err := ctx.AppDB().Exec(`DELETE FROM social_accounts WHERE id=?`, id); err != nil {
		return nil, err
	}
	var siblings int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM social_accounts WHERE connection_id=?`, connID,
	).Scan(&siblings)
	if siblings == 0 {
		// Last reference — release the underlying OAuth connection.
		if err := ctx.PlatformAPI().DisconnectConnection(connID); err != nil {
			ctx.Logger().Warn("DisconnectConnection failed", "conn", connID, "err", err)
			// non-fatal: the social_accounts row is gone; the orphan
			// connection will be reaped on uninstall via cascade.
		}
	}
	ctx.Emit("account.removed", map[string]any{"social_account_id": id})
	return map[string]any{"deleted": id}, nil
}

// ─── post_create ──────────────────────────────────────────────────

// validateTargetOptions checks each target's options keys against the
// declared OptionFields for that target's platform and logs a warning
// on unknown keys. It does NOT reject — forward-compat matters more
// than strict validation here. Empty platforms (or platforms with
// only `body` semantics) accept just `body`.
func (a *App) validateTargetOptions(ctx *sdk.AppCtx, targets []targetSpec) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	for _, t := range targets {
		if len(t.Options) == 0 {
			continue
		}
		var platform string
		_ = ctx.AppDB().QueryRow(
			`SELECT platform FROM social_accounts WHERE id=? AND project_id=?`,
			t.SocialAccountID, pid,
		).Scan(&platform)
		if platform == "" {
			continue // unknown account — finalize will catch it
		}
		def := platforms[platform]
		// Build the set of accepted keys: every platform implicitly
		// accepts `body` as an override; OptionFields adds the rest.
		ok := map[string]bool{"body": true}
		for _, f := range def.OptionFields {
			ok[f.Name] = true
		}
		for k := range t.Options {
			if !ok[k] {
				ctx.Logger().Warn("post_create: unknown option key",
					"platform", platform, "social_account_id", t.SocialAccountID, "key", k)
			}
		}
	}
}

// targetSpec is the normalised form of one entry in post_create's
// targets[] array (or a synthetic version of social_account_ids[]). The
// raw options map is kept verbatim so the publish-path strategies can
// pick out whatever keys their platform cares about; unknown keys are
// stored as-is for forward compatibility.
type targetSpec struct {
	SocialAccountID int64
	Options         map[string]any // verbatim — body, title, visibility, etc.
}

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body, _ := args["body"].(string)
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("body required")
	}

	// Accept either form: social_account_ids[] (simple multicast) or
	// targets[] (per-target overrides). Mutually exclusive — passing
	// both is ambiguous and we refuse rather than guess which to use.
	rawAccts, hasAccts := args["social_account_ids"].([]any)
	rawTargets, hasTargets := args["targets"].([]any)
	if hasAccts && hasTargets && len(rawAccts) > 0 && len(rawTargets) > 0 {
		return mcpError("pass either social_account_ids[] OR targets[], not both"), nil
	}
	var targets []targetSpec
	switch {
	case len(rawTargets) > 0:
		for i, t := range rawTargets {
			m, ok := t.(map[string]any)
			if !ok {
				return mcpError(fmt.Sprintf("targets[%d] must be an object {social_account_id, …}", i)), nil
			}
			id := toInt64Loose(m["social_account_id"])
			if id <= 0 {
				return mcpError(fmt.Sprintf("targets[%d].social_account_id required", i)), nil
			}
			// Strip the id from the options map so it doesn't get
			// re-serialised inside the per-target options blob.
			opts := make(map[string]any, len(m))
			for k, v := range m {
				if k == "social_account_id" {
					continue
				}
				opts[k] = v
			}
			targets = append(targets, targetSpec{SocialAccountID: id, Options: opts})
		}
	case len(rawAccts) > 0:
		for _, v := range rawAccts {
			if id := toInt64Loose(v); id > 0 {
				targets = append(targets, targetSpec{SocialAccountID: id})
			}
		}
	default:
		return nil, errors.New("social_account_ids or targets required (at least one)")
	}
	if len(targets) == 0 {
		return nil, errors.New("social_account_ids or targets required (at least one)")
	}
	// Validate per-target options against each target's platform's
	// declared OptionFields. Unknown keys log a warning but don't fail
	// the call (forward-compat: an agent passing a field that lands
	// in a future version still works).
	a.validateTargetOptions(ctx, targets)
	// Flat list of just the account ids — used by profile-spanning
	// resolution below, same shape the prior code path relied on.
	acctIDs := make([]int64, len(targets))
	for i, t := range targets {
		acctIDs[i] = t.SocialAccountID
	}
	scheduleAt, _ := args["schedule_at"].(string)
	mediaIDsRaw, _ := args["media_storage_ids"].([]any)
	mediaIDs := []int64{}
	for _, v := range mediaIDsRaw {
		if id := toInt64Loose(v); id > 0 {
			mediaIDs = append(mediaIDs, id)
		}
	}
	mediaJSON, _ := json.Marshal(mediaIDs)

	pid := os.Getenv("APTEVA_PROJECT_ID")
	status := "publishing"
	if scheduleAt != "" {
		status = "scheduled"
	}
	// Resolve the post's profile_id. Order:
	//   1. explicit `profile` / `profile_id` arg
	//   2. unique profile_id shared by all selected accounts
	//   3. project default
	// If accounts span multiple profiles AND no explicit arg, refuse
	// — silently picking one would lose information; the caller
	// should split into per-profile posts or pass profile_id.
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return nil, fmt.Errorf("profile %q not found in this project", args["profile"])
	}
	if profileID == 0 {
		spanned := map[int64]bool{}
		for _, aid := range acctIDs {
			var p int64
			_ = ctx.AppDB().QueryRow(
				`SELECT COALESCE(profile_id,0) FROM social_accounts WHERE id=? AND project_id=?`,
				aid, pid,
			).Scan(&p)
			spanned[p] = true
		}
		switch len(spanned) {
		case 0:
			profileID = projectDefaultProfileID(ctx, pid)
		case 1:
			for k := range spanned {
				profileID = k
			}
			if profileID == 0 {
				profileID = projectDefaultProfileID(ctx, pid)
			}
		default:
			return nil, errors.New("selected accounts span multiple profiles — pass profile_id explicitly or split into per-profile post_create calls")
		}
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, media_storage_ids, schedule_at, status, profile_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, body, string(mediaJSON), nullable(scheduleAt), status, profileID,
	)
	if err != nil {
		return nil, err
	}
	postID, _ := res.LastInsertId()

	// Fan out: one target row per requested social account, carrying
	// any per-target overrides as a JSON blob in post_targets.options.
	for _, t := range targets {
		var optsJSON sql.NullString
		if len(t.Options) > 0 {
			b, _ := json.Marshal(t.Options)
			optsJSON = sql.NullString{String: string(b), Valid: true}
		}
		_, err := ctx.AppDB().Exec(
			`INSERT INTO post_targets (post_id, social_account_id, options) VALUES (?, ?, ?)`,
			postID, t.SocialAccountID, optsJSON,
		)
		if err != nil {
			ctx.Logger().Warn("create post_target failed",
				"post", postID, "account", t.SocialAccountID, "err", err)
		}
	}

	// Two execution paths:
	//   - schedule_at empty → publish inline now.
	//   - schedule_at set → schedule a job via the jobs app (when bound)
	//     so the publish is durable. If jobs isn't bound, fall back to
	//     inline now-publish with a clear message in the response.
	if scheduleAt == "" {
		a.publishPostTargets(ctx, postID)
	} else {
		jobID, err := a.scheduleJob(ctx, postID, scheduleAt)
		if err != nil {
			ctx.Logger().Warn("schedule via jobs failed", "post", postID, "err", err)
			_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='failed' WHERE id=?`, postID)
			return mcpError("scheduling failed (is the jobs app bound?): " + err.Error()), nil
		}
		// Persist the jobs.id so post_reschedule + post_delete can
		// cancel the right job later. Failure here is non-fatal —
		// the post is scheduled even if we can't track the link;
		// worst case the job lapses on time without an explicit
		// cancel call.
		if jobID > 0 {
			_, _ = ctx.AppDB().Exec(
				`UPDATE posts SET job_id=? WHERE id=?`, jobID, postID,
			)
		}
	}

	ctx.Emit("post.created", map[string]any{
		"post_id":  postID,
		"status":   status,
		"accounts": acctIDs,
	})
	return map[string]any{
		"post_id":  postID,
		"status":   status,
		"targets":  len(acctIDs),
	}, nil
}

// publishJob is the unit of work for one (post, social_account)
// combination. Built once from the post + post_target row and passed
// to the platform-specific publish strategy.
type publishJob struct {
	targetID, connID int64
	platform, extID  string
	body             string      // already resolved: target.options["body"] || post.body
	media            []mediaItem // resolved (URL + MIME) so strategies can branch image/video
	// options — verbatim per-target overrides decoded from
	// post_targets.options. Strategies pick out whatever keys their
	// platform cares about (publishYoutube reads title/visibility/…).
	// Body is already merged into `body` above; strategies should
	// NOT re-read options["body"].
	options map[string]any
	// pageCreds — JSON map of per-destination credentials populated at
	// finalize time (e.g. Facebook's page-level access_token). Empty
	// for platforms that reuse the user-level token for writes.
	pageCreds string
}

// publishPostTargets walks every pending target on a post and tries to
// publish it. Each target runs through the platform-specific strategy
// (single, instagram_two_step, tiktok, …). Media URLs are resolved up
// front via storage.files_get_url so each strategy gets a flat list.
func (a *App) publishPostTargets(ctx *sdk.AppCtx, postID int64) {
	// Load the post's media ids once — every target gets the same media.
	mediaIDs := a.loadPostMedia(ctx, postID)
	media, mediaErr := a.resolveMedia(ctx, mediaIDs)
	if mediaErr != nil {
		ctx.Logger().Warn("resolve media urls", "post", postID, "err", mediaErr)
		// Don't abort — text-only platforms can still publish; media-
		// required platforms will fall through to the strategy's own
		// "no media" branch and surface the error per-target.
	}

	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.platform, a.connection_id,
		        COALESCE(a.external_account_id,''), p.body,
		        COALESCE(a.page_credentials,''),
		        COALESCE(t.options,'')
		 FROM post_targets t
		 JOIN social_accounts a ON a.id=t.social_account_id
		 JOIN posts p ON p.id=t.post_id
		 WHERE t.post_id=? AND t.status='pending'`,
		postID,
	)
	if err != nil {
		ctx.Logger().Warn("query targets", "err", err)
		return
	}
	var jobs []publishJob
	for rows.Next() {
		var j publishJob
		var acctID int64
		var optsRaw, postBody string
		if err := rows.Scan(&j.targetID, &acctID, &j.platform, &j.connID, &j.extID, &postBody, &j.pageCreds, &optsRaw); err != nil {
			continue
		}
		// Decode per-target options (may be empty/null).
		if optsRaw != "" {
			_ = json.Unmarshal([]byte(optsRaw), &j.options)
		}
		// Body resolution: target's own body override beats post-level
		// body. The merged value is what strategies see; they don't
		// re-read options["body"].
		j.body = postBody
		if j.options != nil {
			if override, ok := j.options["body"].(string); ok && override != "" {
				j.body = override
			}
		}
		j.media = media
		jobs = append(jobs, j)
	}
	rows.Close()

	successes := 0
	failures := 0
	for _, j := range jobs {
		def, ok := platforms[j.platform]
		if !ok {
			a.markTargetFailed(ctx, j.targetID, "unsupported platform: "+j.platform)
			failures++
			continue
		}
		if def.MediaRequired && len(j.media) == 0 {
			a.markTargetFailed(ctx, j.targetID,
				fmt.Sprintf("%s requires at least one media file (image or video). Pass media_storage_ids in post_create or attach media in the panel.", def.DisplayName))
			failures++
			continue
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets SET status='publishing', attempts=attempts+1, last_attempt_at=CURRENT_TIMESTAMP WHERE id=?`,
			j.targetID,
		)
		platformPostID, platformURL, err := a.runStrategy(ctx, def, j)
		if err != nil {
			a.markTargetFailed(ctx, j.targetID, err.Error())
			failures++
			continue
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets SET status='published', platform_post_id=?, platform_url=?, published_at=CURRENT_TIMESTAMP, last_error=NULL WHERE id=?`,
			nullable(platformPostID), nullable(platformURL), j.targetID,
		)
		ctx.Emit("target.published", map[string]any{
			"target_id":        j.targetID,
			"platform":         j.platform,
			"platform_post_id": platformPostID,
			"platform_url":     platformURL,
		})
		successes++
	}

	// Roll up post status.
	finalStatus := "published"
	if failures > 0 && successes == 0 {
		finalStatus = "failed"
	} else if failures > 0 {
		finalStatus = "partial"
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE posts SET status=?, published_at=CURRENT_TIMESTAMP WHERE id=?`,
		finalStatus, postID,
	)
	ctx.Emit("post.completed", map[string]any{
		"post_id":  postID,
		"status":   finalStatus,
		"success":  successes,
		"failures": failures,
	})
}

// runStrategy dispatches to the platform's publish flow. Returns the
// platform-side post id + URL on success, or an error to record on the
// target row.
func (a *App) runStrategy(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	switch def.Strategy {
	case "instagram_two_step":
		return a.publishInstagram(ctx, def, j)
	case "tiktok":
		return a.publishTikTok(ctx, def, j)
	case "youtube":
		return a.publishYoutube(ctx, def, j)
	default: // "single" or empty
		return a.publishSingle(ctx, def, j)
	}
}

// publishSingle covers Twitter / Facebook / LinkedIn — a single
// integration tool call with a flat input shape. Switches between
// the platform's image PostTool and VideoPostTool based on the first
// media item's MIME (Facebook splits POST /feed for photos and POST
// /videos for video; same token, different endpoint).
func (a *App) publishSingle(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	bodyField := def.BodyField
	if bodyField == "" {
		bodyField = "text"
	}
	// Pick the tool + media field + body field based on whether we
	// have a photo or video. Each branch is a self-contained
	// override; falls back to the default text/link tool when there's
	// no media or the platform doesn't declare a media-specific tool.
	tool := def.PostTool
	mediaField := def.MediaURLField
	hasMedia := len(j.media) > 0
	isVideo := hasMedia && j.media[0].IsVideo()
	isImage := hasMedia && !isVideo
	switch {
	case isVideo && def.VideoPostTool != "":
		tool = def.VideoPostTool
		mediaField = def.VideoMediaURLField
		if def.VideoBodyField != "" {
			bodyField = def.VideoBodyField
		}
	case isImage && def.PhotoPostTool != "":
		tool = def.PhotoPostTool
		mediaField = def.PhotoMediaURLField
		if def.PhotoBodyField != "" {
			bodyField = def.PhotoBodyField
		}
	}
	input := map[string]any{bodyField: j.body}
	if def.ExternalIDField != "" && j.extID != "" {
		input[def.ExternalIDField] = j.extID
	}
	if mediaField != "" && len(j.media) > 0 {
		input[mediaField] = j.media[0].URL
	}
	// Inject per-destination credentials (Facebook page access token,
	// etc.). The integration tool's input_schema declares the field
	// (access_token); the executor merges it into the request.
	if def.PostTokenInputField != "" && j.pageCreds != "" {
		var creds map[string]string
		_ = json.Unmarshal([]byte(j.pageCreds), &creds)
		if tok, ok := creds[def.PageAccessTokenField]; ok && tok != "" {
			input[def.PostTokenInputField] = tok
		}
	}
	ctx.Logger().Info("publishSingle: calling PostTool",
		"platform", def.Platform, "tool", tool, "ext_id", j.extID,
		"is_video", isVideo, "has_page_token", input[def.PostTokenInputField] != nil)
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, tool, input)
	if err != nil {
		ctx.Logger().Error("publishSingle: ExecuteIntegrationTool err",
			"platform", def.Platform, "tool", tool, "err", err)
		return "", "", err
	}
	if out == nil || !out.Success {
		ue := upstreamError(out)
		ctx.Logger().Error("publishSingle: upstream non-2xx",
			"platform", def.Platform, "tool", tool, "err", ue)
		return "", "", ue
	}
	id, url := extractPostIdentity(def.Platform, out.Data)
	ctx.Logger().Info("publishSingle: published",
		"platform", def.Platform, "platform_post_id", id, "platform_url", url)
	return id, url, nil
}

// publishInstagram runs the two-step IG dance: create_media_container
// (with imageUrl OR videoUrl+REELS + caption) → publish_media_container
// (with the containerId returned by step 1). IG Business writes need
// the page-level access_token same as Facebook.
func (a *App) publishInstagram(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) == 0 {
		return "", "", errors.New("instagram requires media")
	}
	// Resolve page access_token once — both steps need it.
	pageToken := ""
	if def.PostTokenInputField != "" && j.pageCreds != "" {
		var creds map[string]string
		_ = json.Unmarshal([]byte(j.pageCreds), &creds)
		pageToken = creds[def.PageAccessTokenField]
	}

	// Step 1: create_media_container. Branch on MIME — IG videos go
	// in as REELS now (the legacy VIDEO type is deprecated). sync=true
	// makes the integration block until processing finishes so step 2
	// doesn't race the upstream pipeline.
	first := j.media[0]
	containerInput := map[string]any{
		"caption":            j.body,
		"instagramAccountId": j.extID,
	}
	if first.IsVideo() {
		containerInput["video_url"] = first.URL
		containerInput["media_type"] = "REELS"
	} else {
		containerInput["image_url"] = first.URL
		containerInput["media_type"] = "IMAGE"
	}
	if pageToken != "" {
		containerInput["access_token"] = pageToken
	}
	ctx.Logger().Info("publishInstagram: create container",
		"is_video", first.IsVideo(), "ig_account", j.extID, "has_token", pageToken != "")
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, containerInput)
	if err != nil {
		return "", "", fmt.Errorf("create_media_container: %w", err)
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	containerID := extractContainerID(out.Data)
	if containerID == "" {
		return "", "", fmt.Errorf("create_media_container returned no containerId: %s", string(out.Data))
	}
	// Reels need processing time before publish — Graph API rejects
	// publish_media_container with error 9007 ("Media is not ready")
	// otherwise. Poll get_container_status until FINISHED or timeout.
	// Images are processed inline so no wait is needed.
	if first.IsVideo() {
		if err := a.waitContainerReady(ctx, j.connID, containerID, pageToken); err != nil {
			return "", "", fmt.Errorf("container not ready: %w", err)
		}
	}
	// Step 2: publish_media_container. Graph API expects
	// creation_id; we send both names so the integration's input
	// schema doesn't have to translate.
	publishInput := map[string]any{
		"containerId":        containerID,
		"creation_id":        containerID,
		"instagramAccountId": j.extID,
	}
	if pageToken != "" {
		publishInput["access_token"] = pageToken
	}
	ctx.Logger().Info("publishInstagram: publish container",
		"container_id", containerID, "ig_account", j.extID)
	out2, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PublishTool, publishInput)
	if err != nil {
		return "", "", fmt.Errorf("publish_media_container: %w", err)
	}
	if out2 == nil || !out2.Success {
		return "", "", upstreamError(out2)
	}
	id, _ := extractPostIdentity("instagram", out2.Data)
	url := ""
	if id != "" {
		url = "https://www.instagram.com/p/" + id // best-effort; real shortcode may differ
	}
	return id, url, nil
}

// publishYoutube drives YouTube's resumable upload protocol.
//
//  1. upload_video_init: POSTs the snippet+status metadata to the
//     upload host. The response carries the session URL in the
//     Location header (surfaced via ExecuteResult.Headers thanks to
//     the server-side header allowlist).
//  2. PUT the video bytes directly to that session URL using stdlib
//     http. This step does NOT go through the integration system —
//     Google's session URLs are pre-authorized, so no Bearer token is
//     needed and we don't have to expose credentials to apps.
//
// On success the PUT response contains the published video resource;
// we extract `id` for the platform_post_id and assemble the canonical
// watch URL. The post body is used as the title; the title field is
// the only required snippet metadata.
func (a *App) publishYoutube(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) == 0 {
		return "", "", errors.New("youtube requires a video file")
	}
	first := j.media[0]
	if !first.IsVideo() {
		return "", "", errors.New("youtube only accepts video files")
	}

	// Step 1: init the upload session.
	//
	// Per-target overrides (via post_targets.options for this row):
	//   title       — snippet.title. If blank, fall back to first
	//                 ~80 chars of body so YouTube's required-title
	//                 constraint is satisfied without surprising the
	//                 user with a "missing title" upstream error.
	//   body        — already merged into j.body upstream, so the
	//                 description below uses j.body directly.
	//   visibility  — status.privacyStatus (public|unlisted|private).
	//                 Defaults to private — safer for first-pass
	//                 uploads, matches what most users want when
	//                 they didn't explicitly set it.
	//   category    — snippet.categoryId (numeric string).
	//   tags        — snippet.tags (array of strings).
	title := strOption(j.options, "title")
	if title == "" {
		title = firstChars(strings.TrimSpace(j.body), 80)
	}
	if title == "" {
		title = "Untitled"
	}
	visibility := strOption(j.options, "visibility")
	if visibility == "" {
		visibility = "private"
	}
	snippet := map[string]any{
		"title":       title,
		"description": j.body,
	}
	if cat := strOption(j.options, "category"); cat != "" {
		snippet["categoryId"] = cat
	}
	if tags, ok := j.options["tags"].([]any); ok && len(tags) > 0 {
		out := make([]string, 0, len(tags))
		for _, t := range tags {
			if s, ok := t.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			snippet["tags"] = out
		}
	}
	initInput := map[string]any{
		"snippet": snippet,
		"status":  map[string]any{"privacyStatus": visibility},
	}
	ctx.Logger().Info("publishYoutube: init upload session",
		"title", title, "visibility", visibility, "media_url", first.URL)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, initInput)
	if err != nil {
		return "", "", fmt.Errorf("upload_video_init: %w", err)
	}
	if res == nil || !res.Success {
		return "", "", upstreamError(res)
	}
	sessionURL := ""
	if res.Headers != nil {
		sessionURL = res.Headers["Location"]
	}
	if sessionURL == "" {
		return "", "", errors.New("upload_video_init: no Location header (apteva-server may be older than the header-forwarding change — bump server)")
	}
	ctx.Logger().Info("publishYoutube: got session url",
		"session_url_len", len(sessionURL))

	// Step 2: stream bytes from storage's signed URL into a PUT to the
	// session URL. Both calls happen on the social sidecar's own HTTP
	// client. The signed URL is short-lived but we use it within the
	// same function so freshness isn't a concern.
	getReq, err := http.NewRequest(http.MethodGet, first.URL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build storage GET: %w", err)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return "", "", fmt.Errorf("fetch media bytes from storage: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetch media: storage returned %d", getResp.StatusCode)
	}

	putReq, err := http.NewRequest(http.MethodPut, sessionURL, getResp.Body)
	if err != nil {
		return "", "", fmt.Errorf("build upload PUT: %w", err)
	}
	mime := first.Mime
	if mime == "" {
		mime = "video/*"
	}
	putReq.Header.Set("Content-Type", mime)
	if cl := getResp.ContentLength; cl > 0 {
		putReq.ContentLength = cl
	}
	// Tighter timeout than http.DefaultClient (no timeout) so a stuck
	// upload doesn't pin a worker forever. 30 minutes covers most
	// reasonable YouTube videos at any practical bitrate.
	putClient := &http.Client{Timeout: 30 * time.Minute}
	putResp, err := putClient.Do(putReq)
	if err != nil {
		return "", "", fmt.Errorf("upload PUT: %w", err)
	}
	defer putResp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(putResp.Body, 1<<20))
	if putResp.StatusCode < 200 || putResp.StatusCode >= 300 {
		return "", "", fmt.Errorf("upload PUT %d: %s", putResp.StatusCode, string(body))
	}
	var resource struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(body, &resource)
	if resource.ID == "" {
		return "", "", fmt.Errorf("upload PUT returned no video id: %s", string(body))
	}
	url := "https://www.youtube.com/watch?v=" + resource.ID
	ctx.Logger().Info("publishYoutube: upload complete",
		"video_id", resource.ID)
	return resource.ID, url, nil
}

// waitContainerReady polls get_container_status on an Instagram media
// container until status_code is FINISHED, then returns nil. Returns an
// error on ERROR / EXPIRED status, on a timeout, or on a transport
// failure. Reels typically finish in 5-30s; we cap the wait at 3
// minutes to avoid blocking a worker forever on a stuck transcode.
func (a *App) waitContainerReady(ctx *sdk.AppCtx, connID int64, containerID, pageToken string) error {
	const (
		maxWait  = 3 * time.Minute
		interval = 5 * time.Second
	)
	deadline := time.Now().Add(maxWait)
	input := map[string]any{
		"containerId": containerID,
		"fields":      "id,status_code,status",
	}
	if pageToken != "" {
		input["access_token"] = pageToken
	}
	for {
		out, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_container_status", input)
		if err != nil {
			return fmt.Errorf("get_container_status: %w", err)
		}
		if out == nil || !out.Success {
			return upstreamError(out)
		}
		var resp struct {
			StatusCode string `json:"status_code"`
			Status     string `json:"status"`
		}
		_ = json.Unmarshal(out.Data, &resp)
		ctx.Logger().Info("publishInstagram: container status",
			"container_id", containerID, "status_code", resp.StatusCode)
		switch resp.StatusCode {
		case "FINISHED":
			return nil
		case "ERROR":
			return fmt.Errorf("container processing failed: %s", resp.Status)
		case "EXPIRED":
			return fmt.Errorf("container expired (>24h old) before publish")
		}
		// IN_PROGRESS or empty — keep polling.
		if time.Now().After(deadline) {
			return fmt.Errorf("container still %q after %s — giving up",
				resp.StatusCode, maxWait)
		}
		time.Sleep(interval)
	}
}

// publishTikTok drives TikTok's video publish flow.
//
// Default path: FILE_UPLOAD — TikTok hands us a temporary upload_url
// and we PUT the video bytes there directly (no domain verification
// needed). Same architectural pattern as publishYoutube's resumable
// upload: init via the integration to mint an upload_url + publish_id,
// then bypass the integration system for the bytes-PUT (the upload
// URL is pre-authorized, no Bearer header needed).
//
// Single-chunk for ≤64 MB, multi-chunk for larger. Per TikTok's docs
// (Media Transfer Guide, "Chunk restrictions"):
//   - chunk_size in [5 MB, 64 MB]
//   - total_chunk_count = floor(video_size / chunk_size); the final
//     chunk absorbs the trailing bytes (up to 128 MB)
//   - chunks must be uploaded sequentially with Content-Range tracking
//
// Why we don't use PULL_FROM_URL by default: it requires the caller's
// domain to be DNS-verified in the TikTok dev portal. FILE_UPLOAD has
// no such requirement and works from a fresh OAuth grant. The
// PULL_FROM_URL path is preserved as publishTikTokPullFromURL below
// for callers that need it (verified-domain installs that want
// TikTok's servers to do the fetch instead of streaming through us).
func (a *App) publishTikTok(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) == 0 {
		return "", "", errors.New("tiktok requires a video")
	}
	first := j.media[0]
	if !first.IsVideo() {
		return "", "", errors.New("tiktok only accepts video files")
	}
	if first.Bytes <= 0 {
		return "", "", errors.New("tiktok FILE_UPLOAD needs the video's byte size — storage didn't return size_bytes")
	}

	// TikTok's per-chunk constraints: each in [5 MB, 64 MB] except
	// the final chunk can absorb up to 128 MB of trailing bytes.
	// Strategy: pick 32 MB chunks (mid-range) when we need to chunk.
	const (
		singleChunkLimit = int64(64 * 1024 * 1024)
		multiChunkSize   = int64(32 * 1024 * 1024)
		hardCeiling      = int64(4 * 1024 * 1024 * 1024) // TikTok's 4GB max
	)
	if first.Bytes > hardCeiling {
		return "", "", fmt.Errorf("tiktok video too large: %d bytes (max 4 GB)", first.Bytes)
	}

	var chunkSize int64
	var totalChunks int
	if first.Bytes <= singleChunkLimit {
		chunkSize = first.Bytes
		totalChunks = 1
	} else {
		chunkSize = multiChunkSize
		// Per TikTok's spec: total_chunk_count = floor(video_size / chunk_size).
		// The final chunk absorbs the trailing bytes, so this is correct
		// (no +1 for remainder).
		totalChunks = int(first.Bytes / chunkSize)
	}

	// Step 1: init upload via the integration to get upload_url +
	// publish_id. The integration handles auth + URL building; we just
	// pass the post_info / source_info shapes TikTok expects.
	initInput := map[string]any{
		"post_info": map[string]any{
			"title":         j.body,
			"privacy_level": "PUBLIC_TO_EVERYONE", // sensible default; future: per-target override
		},
		"source_info": map[string]any{
			"source":            "FILE_UPLOAD",
			"video_size":        first.Bytes,
			"chunk_size":        chunkSize,
			"total_chunk_count": totalChunks,
		},
	}
	ctx.Logger().Info("publishTikTok: init upload",
		"video_size", first.Bytes, "chunk_size", chunkSize, "total_chunks", totalChunks)
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, initInput)
	if err != nil {
		return "", "", fmt.Errorf("post_video init: %w", err)
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	uploadURL, publishID := extractTikTokUploadInit(out.Data)
	if uploadURL == "" || publishID == "" {
		return "", "", fmt.Errorf("post_video init: missing upload_url or publish_id in response: %s", string(out.Data))
	}
	ctx.Logger().Info("publishTikTok: init done",
		"publish_id", publishID, "upload_url_len", len(uploadURL))

	// Step 2: GET the bytes from storage as a single stream. We'll
	// consume chunkBytes per iteration via io.LimitReader, leaving the
	// rest of the stream for the next PUT.
	getReq, err := http.NewRequest(http.MethodGet, first.URL, nil)
	if err != nil {
		return "", "", fmt.Errorf("build storage GET: %w", err)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return "", "", fmt.Errorf("fetch media bytes from storage: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("fetch media: storage returned %d", getResp.StatusCode)
	}

	// Step 3: PUT each chunk. Sequential per TikTok's spec; each chunk
	// returns 206 Partial Content except the last which returns 201
	// Created. Tight per-chunk timeout so a stuck PUT doesn't pin a
	// worker forever — 10 minutes per chunk covers 64 MB on slow
	// uplinks (≈100 KB/s).
	putClient := &http.Client{Timeout: 10 * time.Minute}
	mime := first.Mime
	if mime == "" {
		mime = "video/mp4"
	}
	for i := 0; i < totalChunks; i++ {
		firstByte := int64(i) * chunkSize
		var chunkBytes int64
		if i == totalChunks-1 {
			// Final chunk absorbs trailing bytes — could be larger than
			// chunkSize for multi-chunk uploads, equal to videoSize for
			// single-chunk.
			chunkBytes = first.Bytes - firstByte
		} else {
			chunkBytes = chunkSize
		}
		lastByte := firstByte + chunkBytes - 1

		body := io.LimitReader(getResp.Body, chunkBytes)
		putReq, err := http.NewRequest(http.MethodPut, uploadURL, body)
		if err != nil {
			return "", "", fmt.Errorf("build chunk %d PUT: %w", i+1, err)
		}
		putReq.ContentLength = chunkBytes
		putReq.Header.Set("Content-Type", mime)
		putReq.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", firstByte, lastByte, first.Bytes))

		ctx.Logger().Info("publishTikTok: PUT chunk",
			"chunk", fmt.Sprintf("%d/%d", i+1, totalChunks), "bytes", chunkBytes,
			"range", fmt.Sprintf("%d-%d", firstByte, lastByte))
		putResp, err := putClient.Do(putReq)
		if err != nil {
			return "", "", fmt.Errorf("chunk %d PUT: %w", i+1, err)
		}
		respBody, _ := io.ReadAll(io.LimitReader(putResp.Body, 4<<10))
		putResp.Body.Close()

		// Intermediate chunks should return 206; the final chunk
		// returns 201. Anything else is a fail.
		expected := http.StatusPartialContent
		if i == totalChunks-1 {
			expected = http.StatusCreated
		}
		if putResp.StatusCode != expected {
			return "", "", fmt.Errorf("chunk %d/%d returned %d (expected %d): %s",
				i+1, totalChunks, putResp.StatusCode, expected, string(respBody))
		}
	}

	ctx.Logger().Info("publishTikTok: upload complete", "publish_id", publishID)
	// TikTok continues processing async after the last chunk is in;
	// the published URL isn't known until the worker polls
	// get_publish_status. v0.1 records the publish_id; v0.2 schedules
	// a follow-up status check.
	return publishID, "", nil
}

// publishTikTokPullFromURL is the original PULL_FROM_URL implementation,
// kept around for installs that have verified their domain in the
// TikTok dev portal and prefer letting TikTok's servers do the fetch
// (saves bandwidth on the social sidecar's host vs streaming bytes
// through us). Not currently called — runStrategy dispatches to
// publishTikTok which uses FILE_UPLOAD. Wire this in if you ever add
// a per-target / per-platformDef opt-in flag.
func (a *App) publishTikTokPullFromURL(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) == 0 {
		return "", "", errors.New("tiktok requires a video URL")
	}
	input := map[string]any{
		"post_info": map[string]any{
			"title":         j.body,
			"privacy_level": "PUBLIC_TO_EVERYONE",
		},
		"source_info": map[string]any{
			"source":    "PULL_FROM_URL",
			"video_url": j.media[0].URL,
		},
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
	if err != nil {
		return "", "", err
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	pubID := extractTikTokPublishID(out.Data)
	return pubID, "", nil
}

// extractTikTokUploadInit pulls upload_url + publish_id out of the
// /post/publish/video/init/ response. Shape: {data: {publish_id,
// upload_url}, error: {code, message, log_id}}. Empty strings on
// missing — caller decides what to do.
func extractTikTokUploadInit(raw json.RawMessage) (uploadURL, publishID string) {
	if len(raw) == 0 {
		return "", ""
	}
	var resp struct {
		Data struct {
			UploadURL string `json:"upload_url"`
			PublishID string `json:"publish_id"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &resp) == nil {
		return resp.Data.UploadURL, resp.Data.PublishID
	}
	return "", ""
}

// loadPostMedia reads the post's media_storage_ids JSON column.
func (a *App) loadPostMedia(ctx *sdk.AppCtx, postID int64) []int64 {
	var raw string
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(media_storage_ids,'[]') FROM posts WHERE id=?`,
		postID,
	).Scan(&raw)
	var out []int64
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// mediaItem is a resolved media file — public URL + MIME + byte size
// so callers can branch image vs video without a second round-trip,
// and chunked-upload paths (TikTok FILE_UPLOAD) can pre-compute chunk
// counts without a second files_get. Bytes is 0 when storage didn't
// return size_bytes (older storage versions); strategies that need
// it should error out clearly rather than guess.
type mediaItem struct {
	URL   string
	Mime  string
	Bytes int64
}

// IsVideo reports whether this is a video MIME type.
func (m mediaItem) IsVideo() bool { return strings.HasPrefix(m.Mime, "video/") }

// resolveMedia turns storage file ids into absolute, publicly fetchable
// URLs paired with the file's content_type. Calls storage.files_get
// for the metadata and storage.files_get_url for the signed URL.
// The URL must be reachable from the social platform's servers — for
// local dev, point APTEVA_PUBLIC_URL at an ngrok tunnel.
func (a *App) resolveMedia(ctx *sdk.AppCtx, ids []int64) ([]mediaItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	publicBase := os.Getenv("APTEVA_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://127.0.0.1:5280"
	}
	publicBase = strings.TrimRight(publicBase, "/")

	out := make([]mediaItem, 0, len(ids))
	for _, id := range ids {
		// Metadata first — content_type drives the publish strategy
		// (image → /feed; video → /videos for FB; REELS for IG).
		metaRes, err := ctx.PlatformAPI().CallApp("storage", "files_get", map[string]any{
			"id": id,
		})
		if err != nil {
			return nil, fmt.Errorf("storage files_get(%d): %w", id, err)
		}
		mime := extractStorageContentType(metaRes)
		size := extractStorageSize(metaRes)

		// Signed URL — separate call because files_get_url is the
		// canonical way to mint a TTL'd link.
		urlRes, err := ctx.PlatformAPI().CallApp("storage", "files_get_url", map[string]any{
			"id":          id,
			"ttl_seconds": 3600,
		})
		if err != nil {
			return nil, fmt.Errorf("storage files_get_url(%d): %w", id, err)
		}
		// Storage's MCP response is wrapped in {content:[{type:text, text:json}]}.
		// The inner JSON is {url, expires_at, file_id}.
		rel := extractStorageGetURL(urlRes)
		if rel == "" {
			return nil, fmt.Errorf("storage files_get_url(%d) returned no url: %s", id, string(urlRes))
		}
		var fullURL string
		if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
			fullURL = rel
		} else if strings.HasPrefix(rel, "/api/apps/storage/") {
			fullURL = publicBase + rel
		} else {
			fullURL = publicBase + "/api/apps/storage" + rel
		}
		ctx.Logger().Info("resolveMedia: item",
			"id", id, "mime", mime, "is_video", strings.HasPrefix(mime, "video/"))
		out = append(out, mediaItem{URL: fullURL, Mime: mime, Bytes: size})
	}
	return out, nil
}

// extractStorageSize pulls size_bytes out of storage's files_get
// response. Mirrors extractStorageContentType's shape-handling: direct
// {file: {size_bytes}} / {size_bytes}, JSON-RPC-wrapped, and flat-MCP
// envelopes. Returns 0 when the field isn't present (caller decides
// whether that's fatal — TikTok FILE_UPLOAD needs it; FB / IG / X
// don't read it).
func extractStorageSize(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var direct struct {
		File struct {
			SizeBytes int64 `json:"size_bytes"`
		} `json:"file"`
		SizeBytes int64 `json:"size_bytes"`
	}
	if json.Unmarshal(raw, &direct) == nil {
		if direct.File.SizeBytes > 0 {
			return direct.File.SizeBytes
		}
		if direct.SizeBytes > 0 {
			return direct.SizeBytes
		}
	}
	var wrapped struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				if got := extractStorageSize(json.RawMessage(c.Text)); got > 0 {
					return got
				}
			}
		}
	}
	var flat struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &flat) == nil {
		for _, c := range flat.Content {
			if c.Type == "text" && c.Text != "" {
				if got := extractStorageSize(json.RawMessage(c.Text)); got > 0 {
					return got
				}
			}
		}
	}
	return 0
}

// extractStorageContentType pulls the file's content_type out of
// storage's files_get response. The shape is {file: {...}} or the
// row at the top level depending on storage version — handle both.
func extractStorageContentType(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Direct shapes first: {file: {content_type}} or {content_type}.
	var direct struct {
		File struct {
			ContentType string `json:"content_type"`
		} `json:"file"`
		ContentType string `json:"content_type"`
	}
	if json.Unmarshal(raw, &direct) == nil {
		if direct.File.ContentType != "" {
			return direct.File.ContentType
		}
		if direct.ContentType != "" {
			return direct.ContentType
		}
	}
	// JSON-RPC wrapper from CallApp: {result:{content:[{type,text}]}}.
	// Mirrors extractStorageGetURL — unwrap result.content[].text and
	// recurse into the inner JSON to pick up the file/content_type.
	var wrapped struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				if got := extractStorageContentType(json.RawMessage(c.Text)); got != "" {
					return got
				}
			}
		}
	}
	// MCP-flat shape (no jsonrpc wrapper): {content:[...]}. Some
	// transports unwrap one layer for us.
	var flat struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(raw, &flat) == nil {
		for _, c := range flat.Content {
			if c.Type == "text" && c.Text != "" {
				if got := extractStorageContentType(json.RawMessage(c.Text)); got != "" {
					return got
				}
			}
		}
	}
	return ""
}

// extractStorageGetURL pulls the .url field out of storage's
// files_get_url response. Same wrapping shape as extractStorageID.
func extractStorageGetURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var direct struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.URL != "" {
		return direct.URL
	}
	var wrapped struct {
		Result struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				var inner struct {
					URL string `json:"url"`
				}
				if json.Unmarshal([]byte(c.Text), &inner) == nil && inner.URL != "" {
					return inner.URL
				}
			}
		}
	}
	return ""
}

// extractContainerID pulls the IG containerId from create_media_container.
// IG returns either {id: "<container>"} or {containerId: "..."}.
func extractContainerID(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if id := toString(obj["containerId"]); id != "" {
		return id
	}
	if id := toString(obj["id"]); id != "" {
		return id
	}
	return ""
}

// extractTikTokPublishID pulls publish_id from {data: {publish_id}, …}.
func extractTikTokPublishID(raw json.RawMessage) string {
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return ""
	}
	if data, ok := obj["data"].(map[string]any); ok {
		if id := toString(data["publish_id"]); id != "" {
			return id
		}
	}
	return toString(obj["publish_id"])
}

// upstreamError formats a non-2xx integration response into a single
// error. Truncates long payloads so the error column doesn't blow up.
func upstreamError(out *sdk.ExecuteResult) error {
	if out == nil {
		return errors.New("upstream call returned nil")
	}
	body := string(out.Data)
	if len(body) > 500 {
		body = body[:500] + "…"
	}
	return fmt.Errorf("upstream %d: %s", out.Status, body)
}

// scheduleJob hands off to the jobs app: register an event-style job
// that POSTs back into /api/apps/social/jobs/publish_post when its
// scheduled time arrives. Returns the job id so the caller can
// persist it on the post row — post_reschedule / post_delete need
// it to cancel the right job later.
func (a *App) scheduleJob(ctx *sdk.AppCtx, postID int64, scheduleAt string) (int64, error) {
	jobsBound := ctx.IntegrationFor("jobs")
	if jobsBound == nil {
		return 0, errors.New("jobs app not bound — bind it at install time to enable durable scheduling")
	}
	rfc3339, err := normaliseScheduleAt(scheduleAt)
	if err != nil {
		return 0, fmt.Errorf("invalid schedule_at %q: %w", scheduleAt, err)
	}
	res, err := ctx.PlatformAPI().CallApp("jobs", "jobs_schedule", map[string]any{
		"name": fmt.Sprintf("social.publish_post.%d", postID),
		"schedule": map[string]any{
			"kind":   "once",
			"run_at": rfc3339,
		},
		"target": map[string]any{
			"kind":   "http",
			"app":    "social",
			"path":   "/jobs/publish_post",
			"method": "POST",
			"body":   map[string]any{"post_id": postID},
		},
		"idempotency_key": fmt.Sprintf("social.post.%d", postID),
		"max_retries":     3,
		"backoff_seconds": 60,
		"owner_app":       "social",
	})
	if err != nil {
		return 0, err
	}
	if msg := mcpErrorMessage(res); msg != "" {
		return 0, fmt.Errorf("jobs_schedule: %s", msg)
	}
	jobID := extractJobID(res)
	ctx.Logger().Info("scheduleJob: created", "post_id", postID, "job_id", jobID, "run_at", rfc3339)
	return jobID, nil
}

// extractJobID pulls the new job's id out of a jobs_schedule
// response. Both shapes appear: direct `{job:{id}}` (in-process
// tests) and the JSON-RPC-wrapped `result.content[].text` form
// CallApp returns over HTTP. Returns 0 when the response doesn't
// match — caller stores 0 and post_delete falls back to letting
// the run_at lapse without a cancel.
func extractJobID(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var direct struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.Job.ID != 0 {
		return direct.Job.ID
	}
	var wrapped struct {
		Result struct {
			Content []struct {
				Type, Text string
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				if id := extractJobID(json.RawMessage(c.Text)); id != 0 {
					return id
				}
			}
		}
	}
	return 0
}

// cancelJob asks jobs to cancel a previously-created job. Quiet on
// failure — post_delete proceeds regardless so a stale jobs row
// doesn't block deletion.
func (a *App) cancelJob(ctx *sdk.AppCtx, jobID int64) {
	if jobID <= 0 {
		return
	}
	if ctx.IntegrationFor("jobs") == nil {
		return
	}
	res, err := ctx.PlatformAPI().CallApp("jobs", "jobs_cancel", map[string]any{
		"id": jobID,
	})
	if err != nil {
		ctx.Logger().Warn("cancelJob failed", "job_id", jobID, "err", err)
		return
	}
	if msg := mcpErrorMessage(res); msg != "" {
		ctx.Logger().Warn("cancelJob envelope error", "job_id", jobID, "err", msg)
	}
}

// normaliseScheduleAt accepts the formats the panel + agents send and
// returns a canonical RFC3339 string. Order:
//   - already RFC3339 / RFC3339Nano → pass through
//   - "2006-01-02 15:04:05" → reinterpret as local
//   - "2006-01-02T15:04" (HTML datetime-local) → reinterpret as local,
//     append :00 + offset
//   - "2006-01-02" → midnight local
func normaliseScheduleAt(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty")
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format(time.RFC3339), nil
		}
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04", // datetime-local
		"2006-01-02",
	} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.Format(time.RFC3339), nil
		}
	}
	return "", fmt.Errorf("unrecognised time format")
}

// mcpErrorMessage extracts the inner text from an MCP-shaped error
// response. Returns "" when the response is a normal success — used
// by callers of CallApp to detect tool-level errors that came back
// at HTTP 200. Mirrors the envelope check in fetchPages.
func mcpErrorMessage(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// Direct: {isError:true, content:[{type,text}]}.
	var direct struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type, Text string
		} `json:"content"`
	}
	if json.Unmarshal(raw, &direct) == nil && direct.IsError {
		for _, c := range direct.Content {
			if c.Type == "text" && c.Text != "" {
				return c.Text
			}
		}
		return "tool returned an error envelope"
	}
	// JSON-RPC wrapper: {jsonrpc, id, result:{isError, content}}.
	var wrapped struct {
		Result struct {
			IsError bool `json:"isError"`
			Content []struct {
				Type, Text string
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Result.IsError {
		for _, c := range wrapped.Result.Content {
			if c.Type == "text" && c.Text != "" {
				return c.Text
			}
		}
		return "tool returned an error envelope"
	}
	return ""
}

func (a *App) markTargetFailed(ctx *sdk.AppCtx, targetID int64, msg string) {
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets SET status='failed', last_error=? WHERE id=?`,
		msg, targetID,
	)
	ctx.Emit("target.failed", map[string]any{
		"target_id": targetID,
		"error":     msg,
	})
}

// ─── post_list ────────────────────────────────────────────────────

func (a *App) toolPostList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	statusFilter, _ := args["status"].(string)
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
	}
	q := `SELECT id, body, COALESCE(media_storage_ids,'[]'), COALESCE(schedule_at,''),
	             status, created_at, COALESCE(published_at,''), COALESCE(profile_id,0)
	      FROM posts WHERE project_id=?`
	qArgs := []any{pid}
	if statusFilter != "" {
		q += " AND status=?"
		qArgs = append(qArgs, statusFilter)
	}
	if profileID > 0 {
		q += " AND profile_id=?"
		qArgs = append(qArgs, profileID)
	}
	q += " ORDER BY id DESC LIMIT ?"
	qArgs = append(qArgs, limit)
	rows, err := ctx.AppDB().Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, profID                                           int64
			body, mediaJSON, schedAt, status, createdAt, pubAt   string
		)
		if err := rows.Scan(&id, &body, &mediaJSON, &schedAt, &status, &createdAt, &pubAt, &profID); err != nil {
			continue
		}
		var mediaIDs []int64
		_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
		targets := a.loadTargets(ctx, id)
		out = append(out, map[string]any{
			"id":                id,
			"body":              body,
			"media_storage_ids": mediaIDs,
			"profile_id":        profID,
			"schedule_at":       schedAt,
			"status":            status,
			"created_at":        createdAt,
			"published_at":      pubAt,
			"targets":           targets,
		})
	}
	return map[string]any{"posts": out}, nil
}

func (a *App) loadTargets(ctx *sdk.AppCtx, postID int64) []map[string]any {
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.platform, a.display_name, a.avatar_url,
		        t.status, COALESCE(t.platform_post_id,''), COALESCE(t.platform_url,''),
		        t.attempts, COALESCE(t.last_error,''), COALESCE(t.published_at,'')
		 FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id
		 WHERE t.post_id=? ORDER BY t.id`,
		postID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			tid, acctID                                                 int64
			platform, name, avatar, status, ppid, purl, lastErr, pubAt  string
			attempts                                                    int
		)
		if err := rows.Scan(&tid, &acctID, &platform, &name, &avatar, &status, &ppid, &purl, &attempts, &lastErr, &pubAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id":                tid,
			"social_account_id": acctID,
			"platform":          platform,
			"display_name":      name,
			"avatar_url":        avatar,
			"status":            status,
			"platform_post_id":  ppid,
			"platform_url":      purl,
			"attempts":          attempts,
			"last_error":        lastErr,
			"published_at":      pubAt,
		})
	}
	return out
}

// ─── post_retry ───────────────────────────────────────────────────

func (a *App) toolPostRetry(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return nil, errors.New("post_id required")
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets SET status='pending', last_error=NULL WHERE post_id=? AND status='failed'`,
		postID,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return mcpError("no failed targets to retry on this post"), nil
	}
	a.publishPostTargets(ctx, postID)
	return map[string]any{"retried": n}, nil
}

// ─── post_reschedule ──────────────────────────────────────────────

func (a *App) toolPostReschedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	scheduleAt, _ := args["schedule_at"].(string)
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	if scheduleAt == "" {
		return mcpError("schedule_at required"), nil
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var status string
	var jobID int64
	err := ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(job_id,0) FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&status, &jobID)
	if err != nil {
		return mcpError("post not found"), nil
	}
	if status != "scheduled" {
		return mcpError(fmt.Sprintf(
			"post status=%q can't be rescheduled (only 'scheduled' posts are reschedulable)", status,
		)), nil
	}
	// Cancel the old job FIRST. If the new schedule fails we end up
	// with a post in 'scheduled' but no job — caught by the rollback
	// below: the post status is flipped to 'failed' so the operator
	// notices.
	a.cancelJob(ctx, jobID)

	newJobID, err := a.scheduleJob(ctx, postID, scheduleAt)
	if err != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE posts SET status='failed', job_id=0 WHERE id=?`, postID,
		)
		return mcpError("reschedule failed: " + err.Error()), nil
	}
	rfc, _ := normaliseScheduleAt(scheduleAt)
	_, _ = ctx.AppDB().Exec(
		`UPDATE posts SET schedule_at=?, job_id=? WHERE id=?`,
		rfc, newJobID, postID,
	)
	ctx.Emit("post.rescheduled", map[string]any{
		"post_id":  postID,
		"job_id":   newJobID,
		"run_at":   rfc,
	})
	return map[string]any{
		"post_id":     postID,
		"schedule_at": rfc,
		"job_id":      newJobID,
	}, nil
}

// ─── metrics ──────────────────────────────────────────────────────
//
// post_metrics(post_id) and account_metrics(social_account_id) fan out
// to per-platform analytics tools and return fresh numbers. No DB
// writes, no caching — every call hits the upstream. Suitable for
// agent-driven one-off queries; agents looping through 100 posts will
// burn rate limits (mitigation: add a metrics cache later with a TTL).
//
// Per-target outcome envelope mirrors post_delete's vocabulary:
//   ok          — upstream returned numbers; metrics populated
//   unsupported — platform's analytics tool isn't in catalog yet
//   skipped     — target was never published (no platform_post_id)
//   failed      — integration call errored or returned non-2xx
//
// Hybrid response shape: normalized common fields (views, likes,
// comments, shares) so agents can compare across platforms, plus the
// raw platform JSON for deep dives into platform-specific fields
// (IG saves, TikTok profile_visits, YouTube likeCount-vs-favoriteCount,
// etc.) that don't fit the common shape.

type normalizedMetrics struct {
	Views    int64           `json:"views"`
	Likes    int64           `json:"likes"`
	Comments int64           `json:"comments"`
	Shares   int64           `json:"shares"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

type targetMetricsOutcome struct {
	TargetID        int64              `json:"target_id"`
	SocialAccountID int64              `json:"social_account_id"`
	Platform        string             `json:"platform"`
	PlatformPostID  string             `json:"platform_post_id,omitempty"`
	PlatformURL     string             `json:"platform_url,omitempty"`
	Status          string             `json:"status"` // ok | unsupported | skipped | failed
	Reason          string             `json:"reason,omitempty"`
	Error           string             `json:"error,omitempty"`
	Metrics         *normalizedMetrics `json:"metrics,omitempty"`
}

// getPostMetrics dispatches to the per-platform fetcher for one
// target. Returns a complete outcome (never nil) so the caller can
// always include it in the response array.
func (a *App) getPostMetrics(ctx *sdk.AppCtx, target struct {
	TargetID, SocialAccountID, ConnID int64
	Platform, ExtPostID, ExtURL       string
}) targetMetricsOutcome {
	out := targetMetricsOutcome{
		TargetID:        target.TargetID,
		SocialAccountID: target.SocialAccountID,
		Platform:        target.Platform,
		PlatformPostID:  target.ExtPostID,
		PlatformURL:     target.ExtURL,
	}
	if target.ExtPostID == "" {
		out.Status = "skipped"
		out.Reason = "target was never published — no platform_post_id"
		return out
	}
	switch target.Platform {
	case "twitter":
		return a.getTwitterPostMetrics(ctx, out, target.ConnID)
	case "youtube":
		return a.getYoutubePostMetrics(ctx, out, target.ConnID)
	case "tiktok":
		return a.getTikTokPostMetrics(ctx, out, target.ConnID)
	default:
		// FB / IG / LinkedIn / Reddit / Pinterest / Threads — analytics
		// tools either aren't in the catalog yet or have slug-style paths
		// that don't resolve to real upstream endpoints. Surface as
		// unsupported so the agent + UI know not to expect numbers.
		out.Status = "unsupported"
		out.Reason = "no analytics tool wired for this platform yet"
		return out
	}
}

// getTwitterPostMetrics calls get_tweet_analytics for a single tweet
// and maps Twitter's public_metrics to our normalized shape. Twitter's
// response wraps under {data: {public_metrics: {...}}}; some shapes
// also nest under {data: [{...}]} when called for multiple tweets,
// which we don't use here.
func (a *App) getTwitterPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_tweet_analytics", map[string]any{
		"tweetId": out.PlatformPostID,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	// Pull public_metrics out of either {data: {public_metrics}} or
	// {public_metrics} top-level depending on the integration's response
	// shape.
	var resp struct {
		Data struct {
			PublicMetrics struct {
				ImpressionCount int64 `json:"impression_count"`
				LikeCount       int64 `json:"like_count"`
				ReplyCount      int64 `json:"reply_count"`
				RetweetCount    int64 `json:"retweet_count"`
				QuoteCount      int64 `json:"quote_count"`
				BookmarkCount   int64 `json:"bookmark_count"`
			} `json:"public_metrics"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	pm := resp.Data.PublicMetrics
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    pm.ImpressionCount,
		Likes:    pm.LikeCount,
		Comments: pm.ReplyCount,
		Shares:   pm.RetweetCount + pm.QuoteCount, // group retweets + quotes under shares
		Raw:      res.Data,
	}
	return out
}

// getYoutubePostMetrics calls get_video?part=statistics and maps the
// YouTube Data API v3 statistics block. Response shape:
// {items: [{statistics: {viewCount, likeCount, commentCount, ...}}]}.
// Statistic counts come back as STRINGS in YouTube's API (per spec),
// so we parse them out.
func (a *App) getYoutubePostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_video", map[string]any{
		"id":   out.PlatformPostID,
		"part": "statistics,snippet",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		Items []struct {
			Statistics struct {
				ViewCount    string `json:"viewCount"`
				LikeCount    string `json:"likeCount"`
				CommentCount string `json:"commentCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	if len(resp.Items) == 0 {
		out.Status = "failed"
		out.Error = "video not found or no items in response"
		return out
	}
	stats := resp.Items[0].Statistics
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    parseInt64(stats.ViewCount),
		Likes:    parseInt64(stats.LikeCount),
		Comments: parseInt64(stats.CommentCount),
		Shares:   0, // YouTube doesn't expose share count via Data API
		Raw:      res.Data,
	}
	return out
}

// getTikTokPostMetrics calls query_videos with filters.video_ids + the
// metric fields. Response shape:
// {data: {videos: [{view_count, like_count, comment_count, share_count}]}}.
func (a *App) getTikTokPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "query_videos", map[string]any{
		"filters": map[string]any{"video_ids": []string{out.PlatformPostID}},
		"fields":  "id,title,view_count,like_count,comment_count,share_count",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		Data struct {
			Videos []struct {
				ViewCount    int64 `json:"view_count"`
				LikeCount    int64 `json:"like_count"`
				CommentCount int64 `json:"comment_count"`
				ShareCount   int64 `json:"share_count"`
			} `json:"videos"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	if len(resp.Data.Videos) == 0 {
		out.Status = "failed"
		out.Error = "video not in query result (may not have propagated yet, or wrong id)"
		return out
	}
	v := resp.Data.Videos[0]
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    v.ViewCount,
		Likes:    v.LikeCount,
		Comments: v.CommentCount,
		Shares:   v.ShareCount,
		Raw:      res.Data,
	}
	return out
}

// parseInt64 is a forgiving int64 parser that returns 0 for empty,
// non-numeric, or negative inputs. YouTube's Data API serialises
// numeric stats as strings, hence the need.
func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// ─── account_metrics ──────────────────────────────────────────────

type accountMetricsResult struct {
	SocialAccountID int64           `json:"social_account_id"`
	Platform        string          `json:"platform"`
	DisplayName     string          `json:"display_name"`
	Status          string          `json:"status"` // ok | unsupported | failed
	Reason          string          `json:"reason,omitempty"`
	Error           string          `json:"error,omitempty"`
	Followers       int64           `json:"followers,omitempty"`
	Following       int64           `json:"following,omitempty"`
	TotalLikes      int64           `json:"total_likes,omitempty"`
	TotalVideos     int64           `json:"total_videos,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

func (a *App) getAccountMetrics(ctx *sdk.AppCtx, accountID int64, period string) accountMetricsResult {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var platform, displayName string
	var connID int64
	err := ctx.AppDB().QueryRow(
		`SELECT platform, COALESCE(display_name,''), connection_id
		 FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &displayName, &connID)
	if err != nil {
		return accountMetricsResult{
			SocialAccountID: accountID,
			Status:          "failed",
			Error:           "account not found",
		}
	}
	out := accountMetricsResult{
		SocialAccountID: accountID,
		Platform:        platform,
		DisplayName:     displayName,
	}
	switch platform {
	case "youtube":
		return a.getYoutubeChannelMetrics(ctx, out, connID)
	case "tiktok":
		return a.getTikTokAccountMetrics(ctx, out, connID)
	default:
		// FB pages have follower counts via /me/accounts fields, IG via
		// instagram_business_account.followers_count, X via get_me — but
		// each takes a different shape and the existing platformDef
		// machinery doesn't surface them yet. Mark as unsupported with
		// a clear reason; agents and the UI both render that gracefully.
		out.Status = "unsupported"
		out.Reason = "account-level metrics not wired for this platform yet"
		return out
	}
}

func (a *App) getYoutubeChannelMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64) accountMetricsResult {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_my_channel", map[string]any{
		"part": "statistics,snippet",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		Items []struct {
			Statistics struct {
				ViewCount       string `json:"viewCount"`
				SubscriberCount string `json:"subscriberCount"`
				VideoCount      string `json:"videoCount"`
			} `json:"statistics"`
		} `json:"items"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	if len(resp.Items) == 0 {
		out.Status = "failed"
		out.Error = "channel not found in response"
		return out
	}
	s := resp.Items[0].Statistics
	out.Status = "ok"
	out.Followers = parseInt64(s.SubscriberCount)
	out.TotalVideos = parseInt64(s.VideoCount)
	// Stash totalViewCount in raw — useful for "channel reach" but
	// doesn't fit the per-account shape cleanly.
	out.Raw = res.Data
	return out
}

func (a *App) getTikTokAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64) accountMetricsResult {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_user_info", map[string]any{
		"fields": "open_id,display_name,follower_count,following_count,likes_count,video_count",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	var resp struct {
		Data struct {
			User struct {
				FollowerCount  int64 `json:"follower_count"`
				FollowingCount int64 `json:"following_count"`
				LikesCount     int64 `json:"likes_count"`
				VideoCount     int64 `json:"video_count"`
			} `json:"user"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	u := resp.Data.User
	out.Status = "ok"
	out.Followers = u.FollowerCount
	out.Following = u.FollowingCount
	out.TotalLikes = u.LikesCount
	out.TotalVideos = u.VideoCount
	out.Raw = res.Data
	return out
}

// toolPostMetrics is the post_metrics MCP entrypoint. Walks the
// post's targets, dispatches each to its platform's fetcher, returns
// the per-target outcomes.
func (a *App) toolPostMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	// Existence + ownership check — also surfaces post status / body
	// in the response so the agent gets context without a second call.
	var body, status string
	err := ctx.AppDB().QueryRow(
		`SELECT body, status FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&body, &status)
	if err != nil {
		return mcpError("post not found"), nil
	}
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, COALESCE(t.platform_post_id,''),
		        COALESCE(t.platform_url,''), a.platform, a.connection_id
		 FROM post_targets t
		 LEFT JOIN social_accounts a ON a.id=t.social_account_id
		 WHERE t.post_id=?`,
		postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type targetRow struct {
		TargetID, SocialAccountID, ConnID int64
		Platform, ExtPostID, ExtURL       string
	}
	var trs []targetRow
	for rows.Next() {
		var r targetRow
		var connID sql.NullInt64
		var platform sql.NullString
		if err := rows.Scan(&r.TargetID, &r.SocialAccountID, &r.ExtPostID, &r.ExtURL, &platform, &connID); err == nil {
			if platform.Valid {
				r.Platform = platform.String
			}
			if connID.Valid {
				r.ConnID = connID.Int64
			}
			trs = append(trs, r)
		}
	}
	outcomes := make([]targetMetricsOutcome, 0, len(trs))
	for _, r := range trs {
		if r.Platform == "" || r.ConnID == 0 {
			outcomes = append(outcomes, targetMetricsOutcome{
				TargetID: r.TargetID, SocialAccountID: r.SocialAccountID,
				Platform: r.Platform, PlatformPostID: r.ExtPostID,
				Status: "skipped",
				Reason: "social account row gone — was the account disconnected?",
			})
			continue
		}
		ctx.Logger().Info("post_metrics: fetching",
			"post", postID, "platform", r.Platform, "platform_post_id", r.ExtPostID)
		outcomes = append(outcomes, a.getPostMetrics(ctx, r))
	}
	return map[string]any{
		"post_id": postID,
		"body":    body,
		"status":  status,
		"targets": outcomes,
	}, nil
}

// toolAccountMetrics is the account_metrics MCP entrypoint.
func (a *App) toolAccountMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "social_account_id", 0))
	if id <= 0 {
		return mcpError("social_account_id required"), nil
	}
	period, _ := args["period"].(string) // currently unused; reserved for future
	_ = period
	res := a.getAccountMetrics(ctx, id, period)
	return res, nil
}

// ─── post_delete ───────────────────────────────────────────────────

// targetDeleteOutcome captures one upstream-delete attempt for the
// post.deleted event payload + tool response. Best-effort: a failed
// outcome here does NOT block the local row delete — the user gets a
// clear list of which platforms still hold a copy.
type targetDeleteOutcome struct {
	TargetID       int64  `json:"target_id"`
	Platform       string `json:"platform"`
	PlatformPostID string `json:"platform_post_id"`
	Status         string `json:"status"` // deleted | unsupported | skipped | failed
	Error          string `json:"error,omitempty"`
}

func (a *App) toolPostDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	forceLocal := boolArg(args, "force_local_only", false)
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var status string
	var jobID int64
	err := ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(job_id,0) FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&status, &jobID)
	if err != nil {
		return mcpError("post not found"), nil
	}
	// Cancel the upstream jobs row first (best-effort — if the post
	// already fired, jobs treats the cancel as a no-op).
	if status == "scheduled" && jobID > 0 {
		a.cancelJob(ctx, jobID)
	}
	// Fan out upstream deletes for every published target with a
	// platform_post_id. Best-effort: failures are recorded but the
	// local rows still get removed below.
	var outcomes []targetDeleteOutcome
	if !forceLocal && (status == "published" || status == "partial") {
		outcomes = a.deletePostUpstream(ctx, postID)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM post_targets WHERE post_id=?`, postID); err != nil {
		return nil, fmt.Errorf("delete targets: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM posts WHERE id=? AND project_id=?`, postID, pid,
	); err != nil {
		return nil, fmt.Errorf("delete post: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("post.deleted", map[string]any{
		"post_id":          postID,
		"prior_status":     status,
		"cancelled_job_id": jobID,
		"upstream":         outcomes,
	})
	return map[string]any{
		"deleted":          postID,
		"prior_status":     status,
		"cancelled_job_id": jobID,
		"upstream":         outcomes,
	}, nil
}

// deletePostUpstream walks every published target with a platform_post_id
// and asks the platform's integration to remove the post. Returns one
// outcome per target so callers can surface per-platform results.
//
// Status semantics:
//   - "deleted"     — upstream confirmed the removal
//   - "unsupported" — platform's API doesn't allow deletion (Instagram media,
//                     TikTok), or the catalog doesn't expose a verb yet
//                     (LinkedIn, Reddit, Threads). Local row will still be
//                     removed; the upstream copy stays
//   - "skipped"     — target was never published (no platform_post_id) or
//                     its social_account row is gone (account disconnected
//                     after posting), so we have nothing to delete upstream
//   - "failed"      — integration call returned an error; user can verify
//                     manually with platform_post_id
func (a *App) deletePostUpstream(ctx *sdk.AppCtx, postID int64) []targetDeleteOutcome {
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.status, COALESCE(t.platform_post_id,''),
		        a.platform, a.connection_id, COALESCE(a.page_credentials,'')
		 FROM post_targets t
		 LEFT JOIN social_accounts a ON a.id=t.social_account_id
		 WHERE t.post_id=?`,
		postID,
	)
	if err != nil {
		ctx.Logger().Warn("deletePostUpstream: query targets", "post_id", postID, "err", err)
		return nil
	}
	defer rows.Close()
	type row struct {
		targetID  int64
		status    string
		extPostID string
		platform  sql.NullString
		connID    sql.NullInt64
		pageCreds sql.NullString
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.targetID, &r.status, &r.extPostID, &r.platform, &r.connID, &r.pageCreds); err == nil {
			rs = append(rs, r)
		}
	}
	outcomes := make([]targetDeleteOutcome, 0, len(rs))
	for _, r := range rs {
		out := targetDeleteOutcome{TargetID: r.targetID, PlatformPostID: r.extPostID}
		if r.platform.Valid {
			out.Platform = r.platform.String
		}
		// Skip unpublished targets and orphans (account row gone).
		if r.status != "published" || r.extPostID == "" || !r.platform.Valid || !r.connID.Valid {
			out.Status = "skipped"
			outcomes = append(outcomes, out)
			continue
		}
		def, ok := platforms[r.platform.String]
		if !ok || def.DeleteTool == "" {
			out.Status = "unsupported"
			outcomes = append(outcomes, out)
			continue
		}
		input := map[string]any{def.DeleteIDField: r.extPostID}
		// Reuse the same page-token forwarding as the post path —
		// Facebook's DELETE /{pageId}_{postId} requires the page-level
		// access_token same as /feed writes do.
		if def.PostTokenInputField != "" && r.pageCreds.Valid && r.pageCreds.String != "" {
			var creds map[string]string
			if json.Unmarshal([]byte(r.pageCreds.String), &creds) == nil {
				if tok, ok := creds[def.PageAccessTokenField]; ok && tok != "" {
					input[def.PostTokenInputField] = tok
				}
			}
		}
		ctx.Logger().Info("deletePostUpstream: calling DeleteTool",
			"platform", def.Platform, "tool", def.DeleteTool, "platform_post_id", r.extPostID)
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(r.connID.Int64, def.DeleteTool, input)
		if err != nil {
			out.Status = "failed"
			out.Error = err.Error()
			ctx.Logger().Warn("deletePostUpstream: integration err",
				"platform", def.Platform, "tool", def.DeleteTool, "err", err)
			outcomes = append(outcomes, out)
			continue
		}
		if res == nil || !res.Success {
			ue := upstreamError(res)
			out.Status = "failed"
			out.Error = ue.Error()
			ctx.Logger().Warn("deletePostUpstream: upstream non-2xx",
				"platform", def.Platform, "tool", def.DeleteTool, "err", ue)
			outcomes = append(outcomes, out)
			continue
		}
		out.Status = "deleted"
		ctx.Logger().Info("deletePostUpstream: deleted",
			"platform", def.Platform, "platform_post_id", r.extPostID)
		outcomes = append(outcomes, out)
	}
	return outcomes
}

// ─── HTTP handlers (panel) ────────────────────────────────────────

func (a *App) handleAccountsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolAccountList(globalCtx, map[string]any{})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleAccountsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Platform string `json:"platform"`
		ReturnTo string `json:"return_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := a.toolAccountAdd(globalCtx, map[string]any{
		"platform":  body.Platform,
		"return_to": body.ReturnTo,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

// handleOAuthDone is the URL the platform redirects the browser to
// after the OAuth dance. It looks up the pending row, links the
// connection_id, and flips status to 'ready'. Returns a tiny HTML page
// that postMessages the panel — the panel JS then either auto-finalizes
// or shows the picker.
func (a *App) handleOAuthDone(w http.ResponseWriter, r *http.Request) {
	pendingStr := r.URL.Query().Get("pending")
	connStr := r.URL.Query().Get("conn_id")
	status := r.URL.Query().Get("status")
	pendingID, _ := strconv.ParseInt(pendingStr, 10, 64)
	connID, _ := strconv.ParseInt(connStr, 10, 64)
	if pendingID > 0 && connID > 0 && status == "ok" {
		_, _ = globalCtx.AppDB().Exec(
			`UPDATE pending_accounts SET connection_id=?, status='ready' WHERE id=?`,
			connID, pendingID,
		)
		globalCtx.Emit("account.oauth_ready", map[string]any{
			"pending_account_id": pendingID,
			"connection_id":      connID,
		})
	}
	// Render a minimal page that posts a message to the panel and
	// closes itself. Works whether the OAuth happened in a popup
	// (postMessage to opener) or a top-level redirect (just navigate
	// the user back to the panel).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;background:#111;color:#eee;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><div style="font-size:20px">Authorization complete</div>
<div style="opacity:.7;margin-top:8px">You can close this window.</div></div>
<script>
try { if (window.opener) { window.opener.postMessage({type:"social.oauth_ready",pending_account_id:%d,connection_id:%d}, "*"); window.close(); } } catch(e){}
setTimeout(function(){ window.location.href = "/" }, 1500);
</script></body></html>`, pendingID, connID)
}

func (a *App) handleAccountsFinalize(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PendingAccountID int64  `json:"pending_account_id"`
		PageID           string `json:"page_id"`
		Name             string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	out, err := a.toolAccountFinalize(globalCtx, map[string]any{
		"pending_account_id": body.PendingAccountID,
		"page_id":            body.PageID,
		"name":               body.Name,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleAccountsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/accounts/")
	if rest == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if len(parts) == 2 && parts[1] == "pages" && r.Method == http.MethodGet {
		out, err := a.toolAccountListPendingPages(globalCtx, map[string]any{"pending_account_id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "metrics" && r.Method == http.MethodGet {
		out, err := a.toolAccountMetrics(globalCtx, map[string]any{
			"social_account_id": id,
			"period":            r.URL.Query().Get("period"),
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if r.Method == http.MethodDelete {
		out, err := a.toolAccountDisconnect(globalCtx, map[string]any{"id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
}

func (a *App) handlePostsAPI(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolPostList(globalCtx, map[string]any{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		// Decode into a generic map so we keep targets[] / profile_id /
		// any other field the panel sends without a strict struct
		// schema getting in the way (Go silently drops unknown JSON
		// fields, which previously made `targets` invisible to
		// toolPostCreate and produced a confusing 500).
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		out, err := a.toolPostCreate(globalCtx, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handlePostsItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/posts/")
	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		// /posts/:id — only DELETE for now (no GET on a single
		// post; post_list is granular enough).
		switch r.Method {
		case http.MethodDelete:
			out, err := a.toolPostDelete(globalCtx, map[string]any{"post_id": id})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, out)
			return
		default:
			http.Error(w, "DELETE only at this path", http.StatusMethodNotAllowed)
			return
		}
	}
	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		out, err := a.toolPostRetry(globalCtx, map[string]any{"post_id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "reschedule" && r.Method == http.MethodPost {
		var body struct {
			ScheduleAt string `json:"schedule_at"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolPostReschedule(globalCtx, map[string]any{
			"post_id":     id,
			"schedule_at": body.ScheduleAt,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "metrics" && r.Method == http.MethodGet {
		out, err := a.toolPostMetrics(globalCtx, map[string]any{"post_id": id})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

// handleJobPublishPost is the callback the jobs app fires when a
// scheduled post's run_at arrives. Idempotent: re-running on an
// already-published post is a no-op (publishPostTargets only touches
// status='pending' targets).
func (a *App) handleJobPublishPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		PostID int64 `json:"post_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.PostID <= 0 {
		http.Error(w, "post_id required", http.StatusBadRequest)
		return
	}
	// Move from 'scheduled' → 'publishing' before fanning out.
	_, _ = globalCtx.AppDB().Exec(
		`UPDATE posts SET status='publishing' WHERE id=? AND status='scheduled'`,
		body.PostID,
	)
	a.publishPostTargets(globalCtx, body.PostID)
	writeJSON(w, map[string]any{"published": body.PostID})
}

func (a *App) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")
	out := make([]map[string]any, 0, len(platforms))
	for _, def := range platforms {
		// available — a platform's "Add account" button only makes
		// sense when the operator has seeded an integration connection
		// for it (Settings → Integrations). Without that, OAuth start
		// fails with "missing client_id". Probe per-platform so the UI
		// can gray out buttons we know will fail.
		available := false
		if conns, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
			ProjectID: pid,
			AppSlug:   def.IntegrationSlug,
		}); err == nil && len(conns) > 0 {
			available = true
		}
		// option_fields drives the compose-dialog "Customize" expander —
		// when a user picks an account whose platform has fields, the
		// panel renders inputs for them. Empty array = no per-target
		// customisation possible (Twitter / FB / IG / LinkedIn / TikTok
		// today; just YouTube has knobs in v1).
		fields := def.OptionFields
		if fields == nil {
			fields = []optionField{}
		}
		out = append(out, map[string]any{
			"platform":         def.Platform,
			"display_name":     def.DisplayName,
			"integration_slug": def.IntegrationSlug,
			"requires_picker":  def.ListPagesTool != "",
			"available":        available,
			"option_fields":    fields,
		})
	}
	writeJSON(w, map[string]any{"platforms": out})
}

// ─── helpers ───────────────────────────────────────────────────────

type pendingRow struct {
	id              int64
	platform        string
	integrationSlug string
	connectionID    int64
	status          string
	profileID       int64
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var row pendingRow
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, platform, integration_slug, COALESCE(connection_id,0), status,
		        COALESCE(profile_id,0)
		 FROM pending_accounts WHERE id=?`, id,
	).Scan(&row.id, &row.platform, &row.integrationSlug, &row.connectionID, &row.status, &row.profileID)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

type pageEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar_url"`
	// AccessToken — page-level OAuth token, populated only when the
	// upstream returns it in the list_pages payload (Facebook does
	// under data[].access_token; Twitter/TikTok don't have the
	// concept). Held in memory through finalize, then persisted to
	// social_accounts.page_credentials so publishSingle can pass it
	// when posting. Never returned to the panel — the page picker UI
	// only sees ID/Name/Avatar.
	AccessToken string `json:"-"`
}

// fetchPages calls the integration's list_pages tool and normalises the
// result via the platformDef's PageIDField / PageNameField / PageAvatarField.
// Supports dotted paths in field names ("picture.data.url" → walk objects).
//
// Pagination: Graph-style APIs return {data:[...], paging:{cursors:{after}}}
// and only include 25 items per page by default. We walk paging.cursors.after
// (or paging.next when present) until exhausted, capped at maxPagePages
// iterations so a runaway upstream can't OOM us. Limit per call is set high
// up-front to minimise round-trips.
func (a *App) fetchPages(ctx *sdk.AppCtx, connID int64, def platformDef) ([]pageEntry, error) {
	const maxPagePages = 10 // 10 × 100 = 1000 destinations is a lot for social
	const perPage = 100

	// Start with the platform-supplied args and a reasonably high limit.
	// The integration tool ignores unknown keys for GETs (they pass
	// through as query params), so adding limit is safe even if the
	// tool's input_schema doesn't declare it.
	args := map[string]any{}
	for k, v := range def.ListPagesArgs {
		args[k] = v
	}
	if _, hasLimit := args["limit"]; !hasLimit {
		args["limit"] = perPage
	}

	pages := make([]pageEntry, 0, perPage)
	for iter := 0; iter < maxPagePages; iter++ {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ListPagesTool, args)
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			return nil, fmt.Errorf("upstream non-2xx: %s", body)
		}
		// Graph/Twitter shape: {data:[...], paging:{...}}.
		var envelope struct {
			Data   []map[string]any `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := json.Unmarshal(res.Data, &envelope); err != nil || envelope.Data == nil {
			// Fall back to "raw is the array" — no pagination possible
			// without a paging envelope, so this is necessarily the last
			// (and only) call.
			var raw []map[string]any
			if err2 := json.Unmarshal(res.Data, &raw); err2 != nil {
				return nil, fmt.Errorf("parse list_pages response: %w", err)
			}
			for _, p := range raw {
				entry := pageEntry{
					ID:     toString(walkPath(p, def.PageIDField)),
					Name:   toString(walkPath(p, def.PageNameField)),
					Avatar: toString(walkPath(p, def.PageAvatarField)),
				}
				if def.PageAccessTokenField != "" {
					entry.AccessToken = toString(walkPath(p, def.PageAccessTokenField))
				}
				pages = append(pages, entry)
			}
			return pages, nil
		}
		for _, p := range envelope.Data {
			entry := pageEntry{
				ID:     toString(walkPath(p, def.PageIDField)),
				Name:   toString(walkPath(p, def.PageNameField)),
				Avatar: toString(walkPath(p, def.PageAvatarField)),
			}
			if def.PageAccessTokenField != "" {
				entry.AccessToken = toString(walkPath(p, def.PageAccessTokenField))
			}
			// Skip entries the platform returned but where the
			// destination ID couldn't be resolved (e.g. Instagram
			// /me/accounts returns FB Pages without a linked
			// instagram_business_account — those rows have no IG ID
			// and aren't postable).
			if entry.ID == "" {
				continue
			}
			pages = append(pages, entry)
		}
		// Done when neither paging.cursors.after nor paging.next is set.
		// Some shapes use one or the other — Facebook tends to give both;
		// IG sometimes only `next`. Either signals "more is available".
		if envelope.Paging.Cursors.After == "" && envelope.Paging.Next == "" {
			break
		}
		// Prefer cursor-based continuation (works with our static path);
		// `paging.next` is a full URL we'd have to call directly which
		// the integration tool layer doesn't support.
		if envelope.Paging.Cursors.After == "" {
			ctx.Logger().Warn("fetchPages: paging.next set but no cursor — stopping",
				"platform", def.Platform, "fetched", len(pages))
			break
		}
		args["after"] = envelope.Paging.Cursors.After
	}
	ctx.Logger().Info("fetchPages: done", "platform", def.Platform, "total", len(pages))
	return pages, nil
}

type profileEntry struct {
	Name   string
	Avatar string
}

func (a *App) fetchProfile(ctx *sdk.AppCtx, connID int64, def platformDef) (*profileEntry, error) {
	if def.ProfileTool == "" {
		return nil, nil
	}
	input := map[string]any{}
	for k, v := range def.ProfileToolArgs {
		input[k] = v
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ProfileTool, input)
	if err != nil {
		ctx.Logger().Warn("fetchProfile: integration error",
			"platform", def.Platform, "tool", def.ProfileTool, "err", err)
		return nil, err
	}
	if res == nil || !res.Success {
		ctx.Logger().Warn("fetchProfile: upstream non-2xx",
			"platform", def.Platform, "tool", def.ProfileTool, "err", upstreamError(res))
		return nil, nil
	}
	var raw map[string]any
	_ = json.Unmarshal(res.Data, &raw)
	// Unwrap whichever envelope the integration uses so the platformDef
	// path expressions can stay shallow:
	//   Twitter / TikTok → {data: {...}}
	//   YouTube          → {items: [{...}], kind: "..."}  (channelListResponse)
	if inner, ok := raw["data"].(map[string]any); ok {
		raw = inner
	}
	if items, ok := raw["items"].([]any); ok && len(items) > 0 {
		if first, ok := items[0].(map[string]any); ok {
			raw = first
		}
	}
	return &profileEntry{
		Name:   toString(walkPath(raw, def.ProfileNameField)),
		Avatar: toString(walkPath(raw, def.ProfileAvatarField)),
	}, nil
}

// ─── avatar cache ──────────────────────────────────────────────────
//
// Upstream avatar URLs (Facebook CDN, IG, X, YT) are signed and rotate
// on a schedule we don't control. Storing them straight into
// social_accounts.avatar_url means panel thumbnails break a few hours
// later. Solution: download once at finalize time, write to
// data/avatars/<sha256><ext>, store the local URL. Content-addressed
// so the same upstream image (or two pages sharing one logo) costs
// one file.
//
// Lives entirely in the social app's data dir — never enters the
// storage app, never appears in any tool listing. Cleaned up
// alongside the social_account row in account_disconnect.

// avatarsDir returns the on-disk directory where avatar bytes live,
// derived from DB_PATH (the SDK's per-install data dir). Falls back to
// "./avatars" so unit tests that don't set DB_PATH still work.
func avatarsDir() string {
	if v := os.Getenv("DB_PATH"); v != "" {
		return filepath.Join(filepath.Dir(v), "avatars")
	}
	return "avatars"
}

// extFromContentType picks a sensible extension from the upstream
// Content-Type header. Empty string when we can't recognise it — the
// caller decides whether to keep the file extensionless or skip the
// cache. Restricted to image MIME types we know browsers render.
func extFromContentType(ct string) string {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	switch ct {
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/svg+xml":
		return ".svg"
	}
	return ""
}

// cacheAvatar fetches an upstream avatar URL and writes it under
// data/avatars/<sha256><ext>, returning the panel-ready local URL
// "/api/apps/social/avatars/<filename>". Idempotent: same upstream
// content → same on-disk filename, second call is a near-no-op.
//
// Failures are logged but never bubble up — the caller stays
// resilient and falls back to the upstream URL if cache fails.
func (a *App) cacheAvatar(ctx *sdk.AppCtx, upstreamURL string) string {
	if upstreamURL == "" {
		return ""
	}
	if strings.HasPrefix(upstreamURL, "/api/apps/social/avatars/") {
		// Already cached (e.g. account_disconnect → reconnect on the
		// same connection re-runs finalize with our own URL).
		return upstreamURL
	}
	cli := &http.Client{Timeout: 15 * time.Second}
	resp, err := cli.Get(upstreamURL)
	if err != nil {
		ctx.Logger().Warn("avatar fetch failed", "url", upstreamURL, "err", err)
		return upstreamURL
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		ctx.Logger().Warn("avatar fetch non-2xx", "url", upstreamURL, "status", resp.StatusCode)
		return upstreamURL
	}
	// 2 MB cap — avatars are typically <50KB. A pathological response
	// stops getting copied past the cap; we'll see a truncated file
	// and the browser will drop it cleanly.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		ctx.Logger().Warn("avatar read failed", "url", upstreamURL, "err", err)
		return upstreamURL
	}
	ext := extFromContentType(resp.Header.Get("Content-Type"))
	if ext == "" {
		ctx.Logger().Warn("avatar unknown content-type", "url", upstreamURL, "ct", resp.Header.Get("Content-Type"))
		return upstreamURL
	}
	sum := sha256.Sum256(body)
	name := hex.EncodeToString(sum[:]) + ext
	dir := avatarsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		ctx.Logger().Warn("avatar mkdir failed", "dir", dir, "err", err)
		return upstreamURL
	}
	path := filepath.Join(dir, name)
	// Skip the write if the file already exists with the right size.
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(body)) {
		return "/api/apps/social/avatars/" + name
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		ctx.Logger().Warn("avatar write failed", "path", tmp, "err", err)
		return upstreamURL
	}
	if err := os.Rename(tmp, path); err != nil {
		ctx.Logger().Warn("avatar rename failed", "from", tmp, "to", path, "err", err)
		_ = os.Remove(tmp)
		return upstreamURL
	}
	ctx.Logger().Info("avatar cached", "url", upstreamURL, "name", name, "bytes", len(body))
	return "/api/apps/social/avatars/" + name
}

// handleAvatar serves a previously-cached avatar from disk. The URL
// path under the SDK is /avatars/<filename>; we sanitise to a single
// path component (no subdirs, no traversal) so a malicious request
// can't read arbitrary files. Returns 404 for missing files; the
// browser will fall back to its alt text.
func (a *App) handleAvatar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/avatars/")
	// Path-traversal defence: only one component, only [a-f0-9]+.<ext>.
	if rest == "" || strings.Contains(rest, "/") || strings.Contains(rest, "\\") || strings.Contains(rest, "..") {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	path := filepath.Join(avatarsDir(), rest)
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	// Long cache: filenames are content-addressed, so the bytes for a
	// given URL never change. The dashboard just re-renders the new
	// URL when the avatar refreshes.
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	switch {
	case strings.HasSuffix(rest, ".jpg"):
		w.Header().Set("Content-Type", "image/jpeg")
	case strings.HasSuffix(rest, ".png"):
		w.Header().Set("Content-Type", "image/png")
	case strings.HasSuffix(rest, ".gif"):
		w.Header().Set("Content-Type", "image/gif")
	case strings.HasSuffix(rest, ".webp"):
		w.Header().Set("Content-Type", "image/webp")
	case strings.HasSuffix(rest, ".svg"):
		w.Header().Set("Content-Type", "image/svg+xml")
	}
	http.ServeContent(w, r, rest, st.ModTime(), f)
}

// extractPostIdentity tries to pull a stable id + URL out of the
// upstream post response. Best-effort: returns ("", "") if either
// field can't be located. Different platforms use different shapes;
// full coverage will land as we add platforms.
func extractPostIdentity(platform string, raw json.RawMessage) (string, string) {
	if len(raw) == 0 {
		return "", ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return "", ""
	}
	switch platform {
	case "twitter":
		// {data: {id, text}}
		if data, ok := obj["data"].(map[string]any); ok {
			id := toString(data["id"])
			if id != "" {
				return id, "https://twitter.com/i/web/status/" + id
			}
		}
	case "facebook":
		// {id: "<page_id>_<post_id>"}
		id := toString(obj["id"])
		if id != "" {
			return id, "https://www.facebook.com/" + id
		}
	}
	if id := toString(obj["id"]); id != "" {
		return id, ""
	}
	return "", ""
}

// walkPath supports dotted paths like "picture.data.url" so a single
// platformDef can extract nested fields without per-platform code.
func walkPath(m map[string]any, path string) any {
	if path == "" || m == nil {
		return nil
	}
	parts := strings.Split(path, ".")
	var cur any = m
	for _, p := range parts {
		if cur == nil {
			return nil
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = obj[p]
	}
	return cur
}

func toString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}

// strOption pulls a string-valued key out of a per-target options map.
// Returns "" when the key is missing, nil, or non-string. Used by
// publish strategies to read overrides like title/visibility/category.
func strOption(opts map[string]any, key string) string {
	if opts == nil {
		return ""
	}
	if s, ok := opts[key].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// firstChars returns up to n characters of s — used to derive a
// YouTube title from body when no explicit title was set. Trims
// trailing whitespace on the cut so the result doesn't end mid-word
// when truncation lands on a space.
func firstChars(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimRight(s[:n], " \t")
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func int64SliceToAny(in []int64) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{
			{"type": "text", "text": msg},
		},
	}
}

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

// toInt64Loose accepts any of the JSON shapes a tool argument can
// arrive as (float64 from generic decode, int / int64 from typed
// callers, "12" from agents that JSON-encode numeric ids as strings)
// and returns the int64 value or 0 on no-match. Used by tool
// argument coercion for things like account_ids and media_ids.
func toInt64Loose(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case string:
		s := strings.TrimSpace(n)
		if s == "" {
			return 0
		}
		if x, err := strconv.ParseInt(s, 10, 64); err == nil {
			return x
		}
	}
	return 0
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		// Smaller models often pass numeric ids as JSON strings
		// ({post_id: "12"}) rather than numbers ({post_id: 12}).
		// Accept both; reject non-numeric strings via the default.
		// Mirrors jobs.intArg's behavior.
		if v == "" {
			return def
		}
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func platformKeys() []string {
	out := make([]string, 0, len(platforms))
	for k := range platforms {
		out = append(out, k)
	}
	return out
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
