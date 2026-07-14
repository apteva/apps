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
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: social
display_name: Social
version: 0.14.66
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
    - role: media
      kind: app
      compatible_app_names: [media]
      capabilities: [media.search]
      required: false
      label: "Media library (optional)"
    - role: jobs
      kind: app
      compatible_app_names: [jobs]
      capabilities: [jobs.schedule]
      required: false
      label: "Jobs (optional)"
    - role: social_provider
      kind: integration
      compatible_slugs: [zernio]
      required: false
      label: "Social provider (optional)"
      hint: "Import and publish through a unified social API for platforms that are hard to connect directly."
provides:
  http_routes:
    - prefix: /
  workers:
    - { name: scheduled_publisher, schedule: "@every 1m" }
    - { name: inbox_collector, schedule: "@every 5m" }
    - { name: analytics_collector, schedule: "@every 6h" }
  mcp_tools:
    - { name: account_add,                description: "Begin OAuth for a platform." }
    - { name: account_list_pending_pages, description: "List selectable pages/channels for a pending account." }
    - { name: account_finalize,           description: "Commit a pending account into the active list." }
    - { name: account_import_provider,    description: "Import provider-backed accounts such as Zernio into Social." }
    - { name: account_list,               description: "List connected social accounts." }
    - { name: account_check,              description: "Check that connected social accounts still work." }
    - { name: account_disconnect,         description: "Revoke a social account." }
    - { name: post_create,                description: "Create + publish (or schedule) a post across accounts. Scheduled creates are idempotent; retry failed scheduling via post_retry. Pass top-level body or per-target body values." }
    - { name: post_list,                  description: "List recent posts." }
    - { name: post_retry,                 description: "Re-attempt a failed post. Failed scheduled posts retry job creation on the same post." }
    - { name: post_publish_scheduled,     description: "Internal Jobs callback that publishes one scheduled post and reports the final downstream result." }
    - { name: inbox_list,                 description: "List inbox items (comments, DMs, mentions, reviews) from connected accounts." }
    - { name: inbox_get,                  description: "Fetch one inbox item, optionally with the surrounding thread." }
    - { name: inbox_mark_read,            description: "Mark inbox items read (local-only)." }
    - { name: inbox_mark_unread,          description: "Mark inbox items unread (local-only)." }
    - { name: inbox_archive,              description: "Archive inbox items (local-only)." }
    - { name: inbox_reply,                description: "Reply to a comment or DM. For comment items, pass mode=public|private|auto; private is supported where the platform exposes private comment replies." }
    - { name: inbox_private_reply,        description: "Compatibility alias for inbox_reply with mode=private." }
    - { name: inbox_hide,                 description: "Hide a comment on the platform side." }
    - { name: inbox_unhide,               description: "Reverse inbox_hide." }
    - { name: inbox_like,                 description: "Like a comment on the platform side." }
    - { name: inbox_delete,               description: "Delete a comment where the platform permits." }
    - { name: inbox_sync,                 description: "Trigger an out-of-cycle inbox poll for one or more accounts." }
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
	// ThumbnailURLField — optional input field for platforms that can
	// receive a custom thumbnail/cover URL during create (Facebook
	// videos: "thumb", Instagram Reels: "coverUrl"). YouTube is
	// different: it requires a second post-upload call, configured by
	// ThumbnailTool below.
	ThumbnailURLField string
	// ThumbnailFrameField — optional input field for platforms that
	// accept a video timestamp instead of a separate image asset
	// (Instagram: thumbOffset, TikTok: video_cover_timestamp_ms).
	ThumbnailFrameField string
	// ThumbnailTool/BinaryField/IDField — post-publish thumbnail step
	// for platforms such as YouTube where thumbnails.set needs the
	// platform-side video id and binary image bytes.
	ThumbnailTool        string
	ThumbnailBinaryField string
	ThumbnailIDField     string
	// ProfileTool — integration tool that returns the authorising
	// user's own identity (used to seed display_name/avatar for
	// platforms without page-selection). Empty = use a default label.
	ProfileTool        string
	ProfileNameField   string
	ProfileAvatarField string
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
	// Inbox declares which inbound capabilities (comments, DMs,
	// mentions, reviews) this platform supports. Drives the
	// `unsupported` status returned by inbox_* tools and the cadence
	// the poll worker uses per account. Zero-valued struct = no inbox
	// support yet.
	Inbox inboxCaps
}

// inboxCaps captures the per-platform inbound surface. A "Read" flag
// means the poll worker pulls items of that kind for this platform;
// a "Write" flag means inbox_reply / inbox_send can target it.
// PrivateReply covers platforms that can answer a public comment with
// a private message (Meta exposes this for Facebook Pages + IG).
type inboxCaps struct {
	CommentsRead, CommentsWrite                bool
	CommentsHide, CommentsLike, CommentsDelete bool
	DMsRead, DMsWrite                          bool
	MentionsRead                               bool
	ReviewsRead, ReviewsReply                  bool
	PrivateReply                               bool
}

// optionField describes one customizable knob on a platform — its key
// name (matches what publish strategies read), a UI-friendly label,
// the input type the panel should render, and an enum of allowed
// values when applicable.
type optionField struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "text" | "textarea" | "select" | "tags" | "media" | "number" | "boolean"
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
		Strategy:           "twitter",
		PostTool:           "post_tweet",
		BodyField:          "text",
		MediaType:          "any",
		ProfileTool:        "get_me",
		ProfileNameField:   "username",
		ProfileAvatarField: "profile_image_url",
		ProfileToolArgs: map[string]any{
			"user.fields": "id,name,username,profile_image_url,public_metrics,verified,created_at",
		},
		DeleteTool:    "delete_tweet",
		DeleteIDField: "tweet_id",
		Inbox: inboxCaps{
			CommentsRead:   true,
			CommentsWrite:  true,
			CommentsDelete: true,
			DMsRead:        true,
			DMsWrite:       true,
			MentionsRead:   true,
		},
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
		ThumbnailURLField:  "thumb",
		OptionFields: []optionField{
			{Name: "thumbnail_storage_id", Type: "media", Label: "Video thumbnail",
				Help: "Optional image from Storage used as the custom thumbnail for Facebook video posts."},
		},
		// Graph DELETE /{pageId}_{postId} — the platform_post_id we
		// stored from post_to_page is already in that exact form. The
		// page-level access_token is forwarded via PostTokenInputField
		// (same pattern + same field name as the post path).
		DeleteTool:    "facebook_delete_post",
		DeleteIDField: "postId",
		Inbox: inboxCaps{
			CommentsRead:   true,
			CommentsWrite:  true,
			CommentsHide:   true,
			CommentsDelete: true,
			PrivateReply:   true,
			DMsRead:        true,
			DMsWrite:       true,
			MentionsRead:   true,
			ReviewsRead:    true,
		},
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
		ThumbnailURLField:    "coverUrl",
		ThumbnailFrameField:  "thumbOffset",
		OptionFields: []optionField{
			{Name: "thumbnail_storage_id", Type: "media", Label: "Reel cover",
				Help: "Optional image from Storage used as the cover for Instagram video/Reel posts."},
			{Name: "thumbnail_frame_ms", Type: "number", Label: "Cover frame (ms)",
				Help: "Optional video timestamp used as the cover frame when no cover image is selected."},
		},
		// Meta's IG Graph permits hide/delete on comments but NOT like
		// (the comment-like verb was deprecated for IG long before the
		// current API generation). FB pages still support like via
		// /{comment_id}/likes POST, so CommentsLike stays true there.
		Inbox: inboxCaps{
			CommentsRead:   true,
			CommentsWrite:  true,
			CommentsHide:   true,
			CommentsDelete: true,
			DMsRead:        true,
			DMsWrite:       true,
			MentionsRead:   true,
			PrivateReply:   true,
		},
	},
	"tiktok": {
		Platform:        "tiktok",
		IntegrationSlug: "tiktok-api",
		DisplayName:     "TikTok",
		// TikTok's input is nested: {post_info: {title}, source_info:
		// {source: "PULL_FROM_URL", video_url}}. The "tiktok" strategy
		// builds that shape from our flat (body, media_url) inputs.
		Strategy:      "tiktok",
		PostTool:      "post_video",
		BodyField:     "title", // logical, lifted into post_info.title
		MediaRequired: true,
		MediaType:     "any",
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
		ThumbnailFrameField: "video_cover_timestamp_ms",
		OptionFields: []optionField{
			{Name: "thumbnail_frame_ms", Type: "number", Label: "Cover frame (ms)",
				Help: "Optional video timestamp used as TikTok's cover frame."},
			{Name: "auto_add_music", Type: "boolean", Label: "Auto add music",
				Help: "For TikTok photo posts only. TikTok chooses recommended music; the API does not allow selecting a specific song."},
			{Name: "photo_cover_index", Type: "number", Label: "Photo cover index",
				Help: "For TikTok photo posts only. Zero-based index of the image used as the cover."},
			{Name: "title", Type: "text", Label: "Photo title",
				Help: "Optional TikTok photo title. The main post body is sent as the photo description."},
		},
		// TikTok exposes a "publish comment" verb but no read API for
		// comments on others' content — leaving Comments* false until
		// that side lands. Until then inbox_reply against tiktok is
		// effectively a "create a top-level comment" surface; we'll
		// expose it once we wire the write endpoint.
		Inbox: inboxCaps{},
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
		DeleteTool:           "delete_video",
		DeleteIDField:        "id",
		ThumbnailTool:        "set_thumbnail",
		ThumbnailBinaryField: "image",
		ThumbnailIDField:     "videoId",
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
				Help:    "Defaults to public when blank."},
			{Name: "category", Type: "text", Label: "Category ID",
				Help: "YouTube numeric category id (e.g. 22 = People & Blogs, 27 = Education). Optional."},
			{Name: "thumbnail_storage_id", Type: "media", Label: "Thumbnail",
				Help: "Optional image from Storage. YouTube applies it after the video upload returns a video id."},
		},
		Inbox: inboxCaps{
			CommentsRead:   true,
			CommentsWrite:  true,
			CommentsDelete: true,
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

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "scheduled_publisher",
			Schedule: "@every 1m",
			Run:      a.runScheduledPublisher,
		},
		{
			Name:     "inbox_collector",
			Schedule: "@every 5m",
			Run:      a.runInboxCollector,
		},
		{
			Name:     "analytics_collector",
			Schedule: "@every 6h",
			Run:      a.runAnalyticsCollector,
		},
	}
}
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
		{Pattern: "/provider-profiles", Handler: a.handleProviderProfiles},
		{Pattern: "/provider-accounts/import", Handler: a.handleProviderAccountsImport},
		// Import management (HTTP/panel only; intentionally not an MCP tool)
		{Pattern: "/imports/run", Handler: a.handleImportsRun},
		// Inbox management
		{Pattern: "/inbox", Handler: a.handleInboxAPI},
		{Pattern: "/inbox/", Handler: a.handleInboxItem},
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
				"To connect through an optional provider, pass provider='zernio'; Social starts the provider OAuth flow and imports the selected provider-backed account into this project. " +
				"Args: platform, provider? ('zernio' for provider-backed accounts), provider_profile_id? (Zernio profile/workspace id; omitted uses the first Zernio profile), force_new? (default false; set true to force a fresh OAuth dance even when an existing connection is available, e.g. to switch to a different provider-side account).",
			InputSchema: schemaObject(map[string]any{
				"platform":            map[string]any{"type": "string", "enum": socialPlatformKeys()},
				"provider":            map[string]any{"type": "string", "enum": []string{"zernio"}},
				"provider_profile_id": map[string]any{"type": "string"},
				"force_new":           map[string]any{"type": "boolean"},
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
			Name: "account_import_provider",
			Description: "Import accounts from an optional social provider integration into Social. " +
				"Today provider must be zernio. The imported accounts behave like normal Social accounts: use post_create, post_list, account_check, metrics, and imports through Social. " +
				"Args: provider? (default zernio), provider_profile_id?, platforms?[], account_ids?[], profile_id?/profile?, dry_run?.",
			InputSchema: schemaObject(map[string]any{
				"provider":            map[string]any{"type": "string", "enum": []string{"zernio"}},
				"provider_profile_id": map[string]any{"type": "string"},
				"platforms":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"account_ids":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"profile_id":          map[string]any{"type": "integer"},
				"profile":             map[string]any{"type": "string"},
				"dry_run":             map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolAccountImportProvider,
		},
		{
			Name:        "account_list",
			Description: "List connected social accounts in this project. Disconnected history-only rows are hidden unless status='disconnected' is requested. Args: platform? (filter), status? (active|needs_reauth|disconnected).",
			InputSchema: schemaObject(map[string]any{
				"platform": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolAccountList,
		},
		{
			Name: "account_check",
			Description: "Check that a connected social account still works by calling the cheapest authenticated read endpoint for its platform. " +
				"For Facebook/Instagram, also verifies that the selected page/IG account is still accessible and refreshes the stored page token when available. " +
				"Args: social_account_id OR all=true. Optional profile_id/profile filters when all=true.",
			InputSchema: schemaObject(map[string]any{
				"social_account_id": map[string]any{"type": "integer"},
				"all":               map[string]any{"type": "boolean"},
				"profile_id":        map[string]any{"type": "integer"},
				"profile":           map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolAccountCheck,
		},
		{
			Name:        "account_disconnect",
			Description: "Disconnect a social account while preserving historical posts and inbox rows. Pass hard_delete=true and delete_posts=true together to permanently remove the account and its local-only history; upstream posts are never deleted by this tool. Args: id, hard_delete?, delete_posts?.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"hard_delete":  map[string]any{"type": "boolean"},
				"delete_posts": map[string]any{"type": "boolean", "description": "Required confirmation when hard_delete=true."},
			}, []string{"id"}),
			Handler: a.toolAccountDisconnect,
		},
		{
			Name: "post_create",
			Description: "Create a post and publish (or schedule) it to N social accounts. " +
				"Pass EITHER social_account_ids[] (simple multicast — every target uses the post body) " +
				"OR targets[] (when you want per-target overrides). The two are mutually exclusive. " +
				"Each target object: {social_account_id (required), body? (required when top-level body is omitted; otherwise override text for this target), " +
				"plus platform-specific keys for the target's platform}. " +
				"Today: youtube accepts {title, body, visibility (public|unlisted|private), category, tags[], thumbnail_storage_id}. " +
				"Facebook accepts {body, thumbnail_storage_id} for video posts; Instagram accepts {body, thumbnail_storage_id, thumbnail_frame_ms} for Reels; TikTok accepts videos with {body, thumbnail_frame_ms} or photo posts with {body, title, auto_add_music, photo_cover_index}. " +
				"Use plain platform text, not Markdown formatting; most social platforms do not render Markdown. " +
				"Body resolution per target: target.body if set, else post-level body. Top-level body may be omitted only when every target has its own non-empty body. " +
				"Immediate publishes return targets[] with per-platform status, platform_post_id, and platform_url when available. Scheduled publishes return a local scheduled post; call post_list after it runs to get platform_url. " +
				"Scheduled creates are idempotent: if the same profile/account/media/body/options/time already exists, the existing post is returned or its failed scheduling is retried instead of creating a duplicate. " +
				"If scheduling fails, retry that existing post with post_retry; do not create a second post with the same args. " +
				"Args: body? or targets[].body, schedule_at? (RFC3339; omit = post now), media_storage_ids? (file ids), media_project_id? (Storage project scope for those ids).",
			InputSchema: schemaObject(map[string]any{
				"body":               map[string]any{"type": "string", "description": "Post-level default text. Required when using social_account_ids; optional with targets[] if every target has a non-empty body."},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"targets": map[string]any{
					"type":        "array",
					"description": "Per-target overrides. Each entry: {social_account_id (required), body? required when top-level body is omitted, plus platform-specific keys like title/visibility/thumbnail_storage_id for YouTube}. Mutually exclusive with social_account_ids.",
					"items":       map[string]any{"type": "object"},
				},
				"schedule_at":       map[string]any{"type": "string"},
				"media_storage_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"media_project_id":  map[string]any{"type": "string"},
			}, nil),
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
			Description: "Re-attempt a failed post. For failed scheduled posts where no job was created, this retries scheduling the same post. Otherwise it resets failed publish targets and re-publishes. Args: post_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
			}, []string{"post_id"}),
			Handler: a.toolPostRetry,
		},
		{
			Name:        "post_publish_scheduled",
			Description: "Internal Jobs callback. Publishes one scheduled post and returns status=published only after every downstream target succeeds; failures remain retryable for Jobs. Args: post_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
			}, []string{"post_id"}),
			Handler: a.toolPostPublishScheduled,
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
			Description: "Delete a post locally and, where the platform/provider allows it, remove the upstream copy too. For each published target the app calls the native platform delete verb or the bound provider delete verb (for example Zernio). Some providers/platforms report published-post deletion as unsupported; in that case local rows still go and the response includes a per-target `upstream` array (status: deleted | unsupported | skipped | failed) so callers can flag platforms that still hold a copy. Cancels any scheduled job first. Args: post_id, force_local_only? (skip all upstream calls; default false).",
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
				"Wired today: X/Twitter (followers, following, post count), YouTube (subscriberCount, videoCount), TikTok (follower_count, following_count, likes_count, video_count), Facebook and Instagram where their insights APIs expose compatible metrics. " +
				"Args: social_account_id, period? (reserved for time-windowed metrics; ignored today).",
			InputSchema: schemaObject(map[string]any{
				"social_account_id": map[string]any{"type": "integer"},
				"period":            map[string]any{"type": "string", "description": "Optional time window like \"7d\" or \"30d\". Reserved for future use; ignored today."},
			}, []string{"social_account_id"}),
			Handler: a.toolAccountMetrics,
		},
		{
			Name: "post_edit",
			Description: "Edit an already-published post's body and/or per-target metadata. Updates the local post + fans out to each platform's edit verb where supported. " +
				"Editable platforms today: Facebook (message), X/Twitter (body/text via edit_options.previous_post_id when the authenticated account/API plan permits it), YouTube (title, description, tags, privacy, category, thumbnail_storage_id). YouTube thumbnail-only edits are allowed; pass targets:[{social_account_id, thumbnail_storage_id}]. TikTok / Instagram return status=unsupported — those platforms don't permit programmatic edits or the catalog doesn't expose the verb yet. " +
				"Args: post_id, body? (new post-level default body), targets? (per-target overrides keyed by social_account_id, same shape as post_create's targets — body, title, visibility, category, tags, thumbnail_storage_id, etc.). At least one of body/targets must be set.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
				"body":    map[string]any{"type": "string"},
				"targets": map[string]any{
					"type":        "array",
					"description": "Optional per-target overrides. Each entry: {social_account_id (required), body?, plus platform-specific keys (title/visibility/category/tags/thumbnail_storage_id for YouTube; body/message for Facebook).}",
					"items":       map[string]any{"type": "object"},
				},
			}, []string{"post_id"}),
			Handler: a.toolPostEdit,
		},
		// ─── inbox ────────────────────────────────────────────────
		{
			Name:        "inbox_list",
			Description: "List inbox items (comments, DMs, mentions, reviews) pulled from connected social accounts. Items are kind-discriminated; filter by `kinds` (comment|dm|mention|review), `status` (unread|read|replied|hidden|archived; archived hidden by default), `social_account_ids`, and `since` (RFC3339). Returns {items: [...], count}. Newest first by occurred_at. Args: social_account_ids?, kinds?, status?, since?, limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"kinds":              map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"status":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"since":              map[string]any{"type": "string"},
				"limit":              map[string]any{"type": "integer", "default": 50},
			}, nil),
			Handler: a.toolInboxList,
		},
		{
			Name:        "inbox_get",
			Description: "Fetch one inbox item by id. Pass with_thread=true to also return every sibling in the same conversation (walked via parent_external_id). Args: id, with_thread?.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"with_thread": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolInboxGet,
		},
		{
			Name:        "inbox_mark_read",
			Description: "Mark one or more inbox items as read. Local-only — no platform call. Args: id OR ids[].",
			InputSchema: schemaObject(map[string]any{
				"id":  map[string]any{"type": "integer"},
				"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, nil),
			Handler: a.toolInboxMarkRead,
		},
		{
			Name:        "inbox_mark_unread",
			Description: "Mark one or more inbox items as unread. Local-only. Args: id OR ids[].",
			InputSchema: schemaObject(map[string]any{
				"id":  map[string]any{"type": "integer"},
				"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, nil),
			Handler: a.toolInboxMarkUnread,
		},
		{
			Name:        "inbox_archive",
			Description: "Archive one or more inbox items (removes from the default list view; reverse with inbox_mark_unread/read). Local-only. Args: id OR ids[].",
			InputSchema: schemaObject(map[string]any{
				"id":  map[string]any{"type": "integer"},
				"ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, nil),
			Handler: a.toolInboxArchive,
		},
		{
			Name: "inbox_reply",
			Description: "Reply to an inbox item. Routes by kind — a `comment` produces a child comment, a `dm` produces an outbound message in the same thread. Returns {status: ok|unsupported|skipped|failed, reason?, error?, external_id?, permalink?}. " +
				"For comment items, mode controls the route: `auto`/`public` creates a public child comment, `private` sends a private reply where supported (Facebook Pages and Instagram Business). Args: id, body, mode? (`auto` default, `public`, `private`), media_storage_ids?.",
			InputSchema: schemaObject(map[string]any{
				"id":                map[string]any{"type": "integer"},
				"body":              map[string]any{"type": "string"},
				"mode":              map[string]any{"type": "string", "enum": []string{"auto", "public", "private"}, "default": "auto"},
				"media_storage_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"id", "body"}),
			Handler: a.toolInboxReply,
		},
		{
			Name:        "inbox_private_reply",
			Description: "Compatibility alias for inbox_reply with mode=private. Sends a private reply to a comment where supported (Facebook Pages and Instagram Business). Args: id (must be a comment kind), body.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"body": map[string]any{"type": "string"},
			}, []string{"id", "body"}),
			Handler: a.toolInboxPrivateReply,
		},
		{
			Name:        "inbox_hide",
			Description: "Hide a comment on the platform side (Facebook, Instagram). Returns `unsupported` for platforms without a hide verb. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInboxHide,
		},
		{
			Name:        "inbox_unhide",
			Description: "Reverse inbox_hide. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInboxUnhide,
		},
		{
			Name:        "inbox_like",
			Description: "Like a comment on the platform side (Facebook, Instagram). Returns `unsupported` elsewhere. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInboxLike,
		},
		{
			Name:        "inbox_delete",
			Description: "Delete a comment we authored (or, where the platform permits, any comment on our content). Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolInboxDelete,
		},
		{
			Name:        "inbox_sync",
			Description: "Trigger an out-of-cycle poll of the named social accounts (or all active accounts when omitted). The same sync path runs automatically every five minutes. Returns one {status, reason?, error?} per account. Args: social_account_ids?.",
			InputSchema: schemaObject(map[string]any{
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, nil),
			Handler: a.toolInboxSync,
		},
	}
	tools = append(tools, a.profileTools()...)
	return tools
}

func main() { sdk.Run(&App{}) }

func projectScope(ctx *sdk.AppCtx, argSets ...map[string]any) string {
	// The SDK dispatch context is pinned by the authenticated gateway and
	// is authoritative. HTTP panel handlers use the argument fallback below
	// because their Route signature does not receive a per-request AppCtx.
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid
		}
	}
	for _, args := range argSets {
		if pid := strings.TrimSpace(stringArgAny(args, "_project_id", "project_id")); pid != "" {
			return pid
		}
	}
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
}

// ─── account_add ───────────────────────────────────────────────────

func (a *App) toolAccountAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	plat, _ := args["platform"].(string)
	provider := strings.ToLower(strings.TrimSpace(toString(args["provider"])))
	if provider == zernioProviderSlug {
		return a.startZernioAccountConnect(ctx, args)
	}
	def, ok := platforms[plat]
	if !ok {
		return mcpError(fmt.Sprintf("unsupported platform %q — available: %s", plat, strings.Join(platformKeys(), ", "))), nil
	}
	pid := projectScope(ctx, args)
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
				pendingExpiry(time.Now().UTC().Add(10*time.Minute)), profileID,
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
		returnTo = "/api/apps/social/accounts/oauth_done?project_id=" + url.QueryEscape(pid)
	} else if !strings.HasPrefix(returnTo, "/") || strings.HasPrefix(returnTo, "//") {
		return mcpError("return_to must be a same-origin absolute path"), nil
	}

	// Pre-create the pending row so we have a stable id we can hand
	// the agent. It'll be linked to the connection once OAuth completes.
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at, profile_id)
		 VALUES (?, ?, ?, 'pending_oauth', ?, ?)`,
		pid, def.Platform, def.IntegrationSlug, pendingExpiry(now.Add(10*time.Minute)), profileID,
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
	row, err := a.getPendingScoped(ctx, args, int64(pendingID))
	if err != nil {
		ctx.Logger().Warn("list_pending_pages: pending row missing", "pending_id", pendingID, "err", err)
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.expired {
		return mcpError("pending account expired — start account_add again"), nil
	}
	if row.status != "ready" {
		return mcpError("OAuth not yet complete — open the authorize_url first, then re-call this tool"), nil
	}
	if row.providerSlug == zernioProviderSlug {
		return a.listZernioPendingPages(ctx, row)
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
	row, err := a.getPendingScoped(ctx, args, int64(pendingID))
	if err != nil {
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.expired {
		return mcpError("pending account expired — start account_add again"), nil
	}
	if row.status != "ready" {
		return mcpError("pending account is not ready or was already finalized"), nil
	}
	if row.providerSlug == zernioProviderSlug {
		return a.finalizeZernioAccount(ctx, args, row)
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

	// Insert the finalized social_account row in the same project that
	// created the pending OAuth row. This matters for global installs:
	// the OAuth callback itself has no active dashboard project.
	pid := row.projectID
	// Profile assignment: use the value the operator set on the
	// pending row at account_add time, falling back to the project's
	// current default if 0. Resolves the case where the default was
	// promoted between account_add and finalize.
	profileID := row.profileID
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	claim, err := tx.Exec(
		`UPDATE pending_accounts SET status='finalizing'
		  WHERE id=? AND project_id=? AND status='ready'`,
		pendingID, pid,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := claim.RowsAffected(); n != 1 {
		return mcpError("pending account is not ready, expired, or was already finalized"), nil
	}
	var existingID int64
	err = tx.QueryRow(
		`SELECT id FROM social_accounts
		  WHERE project_id=? AND platform=? AND connection_id=?
		    AND COALESCE(external_account_id,'')=?
		  ORDER BY id DESC LIMIT 1`,
		pid, def.Platform, row.connectionID, pageID,
	).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if existingID > 0 {
		if _, err := tx.Exec(
			`UPDATE social_accounts
			    SET display_name=?, avatar_url=?, status='active', page_credentials=?, profile_id=?
			  WHERE id=? AND project_id=?`,
			displayName, nullable(avatar), pageCredsJSON, profileID, existingID, pid,
		); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, pendingID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{
			"social_account_id":   existingID,
			"platform":            def.Platform,
			"display_name":        displayName,
			"avatar_url":          avatar,
			"external_account_id": pageID,
			"reconnected":         true,
		}, nil
	}
	res, err := tx.Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, avatar_url, status, page_credentials, profile_id)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		pid, def.Platform, row.connectionID, nullable(pageID), displayName, nullable(avatar), pageCredsJSON, profileID,
	)
	if err != nil {
		return nil, fmt.Errorf("insert social_account: %w", err)
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE pending_accounts SET status='finalized' WHERE id=?`, pendingID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

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
	pid := projectScope(ctx, args)
	platformFilter, _ := args["platform"].(string)
	statusFilter, _ := args["status"].(string)
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
	}
	q := `SELECT id, platform, connection_id, COALESCE(external_account_id,''), display_name,
	             COALESCE(avatar_url,''), status, created_at, COALESCE(profile_id,0),
	             COALESCE(last_check_at,''), COALESCE(last_check_status,''),
	             COALESCE(last_check_error,''), COALESCE(last_check_details,''),
	             COALESCE(provider_slug,'native'), COALESCE(provider_account_id,''),
	             COALESCE(provider_profile_id,''), COALESCE(capabilities,'')
	      FROM social_accounts WHERE project_id=?`
	qArgs := []any{pid}
	if platformFilter != "" {
		q += " AND platform=?"
		qArgs = append(qArgs, platformFilter)
	}
	if statusFilter != "" {
		q += " AND status=?"
		qArgs = append(qArgs, statusFilter)
	} else {
		q += " AND status!='disconnected'"
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
			checkAt, checkStatus, checkError, checkDetails        string
			providerSlug, providerAccountID, providerProfileID    string
			capabilitiesRaw                                       string
		)
		if err := rows.Scan(&id, &platform, &connID, &externalID, &name, &avatar, &status, &createdAt, &profID, &checkAt, &checkStatus, &checkError, &checkDetails, &providerSlug, &providerAccountID, &providerProfileID, &capabilitiesRaw); err != nil {
			continue
		}
		var details any
		if checkDetails != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(checkDetails), &parsed) == nil {
				details = parsed
			}
		}
		var capabilities any
		if capabilitiesRaw != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(capabilitiesRaw), &parsed) == nil {
				capabilities = parsed
			}
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
			"last_check_at":       checkAt,
			"last_check_status":   checkStatus,
			"last_check_error":    checkError,
			"last_check_details":  details,
			"provider_slug":       providerSlug,
			"provider_account_id": providerAccountID,
			"provider_profile_id": providerProfileID,
			"capabilities":        capabilities,
		})
	}
	return map[string]any{"accounts": out}, nil
}

// ─── account_check ────────────────────────────────────────────────

type accountCheckResult struct {
	AccountID   int64          `json:"account_id"`
	Platform    string         `json:"platform"`
	DisplayName string         `json:"display_name,omitempty"`
	Status      string         `json:"status"` // ok | failed | unsupported
	CheckedAt   string         `json:"checked_at"`
	Error       string         `json:"error,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}

func (a *App) toolAccountCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScope(ctx, args)
	if boolArg(args, "all", false) {
		profileID := resolveProfileArg(ctx, pid, args)
		if profileID < 0 {
			return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
		}
		q := `SELECT id FROM social_accounts WHERE project_id=? AND status!='disconnected'`
		qArgs := []any{pid}
		if profileID > 0 {
			q += " AND profile_id=?"
			qArgs = append(qArgs, profileID)
		}
		q += " ORDER BY id DESC"
		rows, err := ctx.AppDB().Query(q, qArgs...)
		if err != nil {
			return nil, err
		}
		ids := []int64{}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		results := []accountCheckResult{}
		for _, id := range ids {
			results = append(results, a.checkAccount(ctx, pid, id))
		}
		return map[string]any{"checks": results, "count": len(results)}, nil
	}
	id := int64(intArg(args, "social_account_id", 0))
	if id <= 0 {
		id = int64(intArg(args, "id", 0))
	}
	if id <= 0 {
		return nil, errors.New("social_account_id required unless all=true")
	}
	return a.checkAccount(ctx, pid, id), nil
}

func (a *App) checkAccount(ctx *sdk.AppCtx, pid string, accountID int64) accountCheckResult {
	checkedAt := time.Now().UTC().Format(time.RFC3339)
	var (
		platform, externalID, displayName, providerSlug, providerAccountID string
		connID                                                             int64
	)
	err := ctx.AppDB().QueryRow(
		`SELECT platform, connection_id, COALESCE(external_account_id,''),
		        display_name, COALESCE(provider_slug,'native'), COALESCE(provider_account_id,'')
		 FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &connID, &externalID, &displayName, &providerSlug, &providerAccountID)
	result := accountCheckResult{
		AccountID:   accountID,
		Platform:    platform,
		DisplayName: displayName,
		Status:      "failed",
		CheckedAt:   checkedAt,
		Details:     map[string]any{},
	}
	if err == sql.ErrNoRows {
		result.Error = "account not found"
		return result
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}

	result.Details["connection_id"] = connID
	result.Details["external_account_id"] = externalID
	result.Details["provider"] = providerSlug
	if providerSlug == zernioProviderSlug {
		result = a.checkZernioAccount(ctx, pid, result, connID, providerAccountID)
		a.persistAccountCheck(ctx, pid, result)
		return result
	}
	def, ok := platforms[platform]
	if !ok {
		result.Status = "unsupported"
		result.Error = "unsupported platform"
		a.persistAccountCheck(ctx, pid, result)
		return result
	}

	if def.ListPagesTool != "" {
		result = a.checkPageAccount(ctx, pid, def, result, connID, externalID)
		a.persistAccountCheck(ctx, pid, result)
		return result
	}
	if def.ProfileTool != "" {
		input := map[string]any{}
		for k, v := range def.ProfileToolArgs {
			input[k] = v
		}
		out, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ProfileTool, input)
		if err != nil {
			result.Error = err.Error()
			a.persistAccountCheck(ctx, pid, result)
			return result
		}
		if out == nil || !out.Success {
			result.Error = upstreamError(out).Error()
			a.persistAccountCheck(ctx, pid, result)
			return result
		}
		result.Status = "ok"
		result.Error = ""
		result.Details["tool"] = def.ProfileTool
		if profile := profileFromToolData(out.Data, def); profile != nil {
			name := strings.TrimSpace(profile.Name)
			avatar := strings.TrimSpace(profile.Avatar)
			if avatar != "" {
				avatar = a.cacheAvatar(ctx, avatar)
			}
			updates := []string{}
			vals := []any{}
			if name != "" && name != displayName {
				updates = append(updates, "display_name=?")
				vals = append(vals, name)
				result.DisplayName = name
				result.Details["display_name_refreshed"] = true
			}
			if avatar != "" {
				updates = append(updates, "avatar_url=?")
				vals = append(vals, avatar)
				result.Details["avatar_refreshed"] = true
			}
			if len(updates) > 0 {
				vals = append(vals, result.AccountID, pid)
				_, _ = ctx.AppDB().Exec(
					`UPDATE social_accounts SET `+strings.Join(updates, ", ")+` WHERE id=? AND project_id=?`,
					vals...,
				)
			}
		}
		a.persistAccountCheck(ctx, pid, result)
		return result
	}

	result.Status = "unsupported"
	result.Error = "no health-check endpoint wired for this platform"
	a.persistAccountCheck(ctx, pid, result)
	return result
}

func (a *App) checkPageAccount(ctx *sdk.AppCtx, pid string, def platformDef, result accountCheckResult, connID int64, externalID string) accountCheckResult {
	if externalID == "" {
		result.Error = "account has no external_account_id"
		return result
	}
	pages, err := a.fetchPages(ctx, connID, def)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	for _, p := range pages {
		if p.ID != externalID {
			continue
		}
		result.Status = "ok"
		result.Error = ""
		result.Details["tool"] = def.ListPagesTool
		result.Details["destination_name"] = p.Name
		result.Details["destination_id"] = p.ID
		result.Details["destination_count"] = len(pages)
		if p.AccessToken != "" && def.PageAccessTokenField != "" {
			pageCreds, _ := json.Marshal(map[string]string{
				def.PageAccessTokenField: p.AccessToken,
			})
			_, _ = ctx.AppDB().Exec(
				`UPDATE social_accounts SET page_credentials=? WHERE id=? AND project_id=?`,
				string(pageCreds), result.AccountID, pid,
			)
			result.Details["page_token_refreshed"] = true
		}
		return result
	}
	result.Error = fmt.Sprintf("%s %q is no longer accessible from this connection", def.DisplayName, externalID)
	result.Details["tool"] = def.ListPagesTool
	result.Details["destination_count"] = len(pages)
	return result
}

func (a *App) persistAccountCheck(ctx *sdk.AppCtx, pid string, result accountCheckResult) {
	details := result.Details
	if len(details) == 0 {
		details = nil
	}
	detailsJSON, _ := json.Marshal(details)
	_, _ = ctx.AppDB().Exec(
		`UPDATE social_accounts
		    SET last_check_at=?, last_check_status=?, last_check_error=?, last_check_details=?
		  WHERE id=? AND project_id=?`,
		result.CheckedAt, result.Status, result.Error, string(detailsJSON), result.AccountID, pid,
	)
	ctx.Emit("account.checked", map[string]any{
		"social_account_id": result.AccountID,
		"platform":          result.Platform,
		"status":            result.Status,
	})
}

// ─── account_disconnect ──────────────────────────────────────────

func (a *App) toolAccountDisconnect(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id <= 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope(ctx, args)
	var connID int64
	var providerSlug string
	if err := ctx.AppDB().QueryRow(
		`SELECT connection_id, COALESCE(provider_slug,'native') FROM social_accounts WHERE id=? AND project_id=?`,
		id, pid,
	).Scan(&connID, &providerSlug); err != nil {
		return mcpError("account not found"), nil
	}

	if boolArg(args, "hard_delete", false) {
		if !boolArg(args, "delete_posts", false) {
			return mcpError("hard_delete requires delete_posts=true to confirm local history removal"), nil
		}
		return a.hardDeleteAccount(ctx, pid, id, connID, providerSlug)
	}

	// Keep the destination row so historical post targets and inbox
	// entries continue to render with their original account identity.
	if _, err := ctx.AppDB().Exec(
		`UPDATE social_accounts SET status='disconnected' WHERE id=? AND project_id=?`,
		id, pid,
	); err != nil {
		return nil, err
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='failed', last_error='account disconnected before publish'
		  WHERE social_account_id=? AND status='pending'`,
		id,
	)
	var siblings int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM social_accounts WHERE connection_id=? AND status!='disconnected'`, connID,
	).Scan(&siblings)
	if siblings == 0 && providerSlug != zernioProviderSlug {
		// Last reference — release the underlying OAuth connection.
		if err := ctx.PlatformAPI().DisconnectConnection(connID); err != nil {
			ctx.Logger().Warn("DisconnectConnection failed", "conn", connID, "err", err)
			// non-fatal: the social_accounts row is gone; the orphan
			// connection will be reaped on uninstall via cascade.
		}
	}
	ctx.Emit("account.disconnected", map[string]any{"social_account_id": id})
	return map[string]any{"disconnected": id, "history_preserved": true}, nil
}

func (a *App) hardDeleteAccount(ctx *sdk.AppCtx, pid string, id, connID int64, providerSlug string) (any, error) {
	postIDs := []int64{}
	rows, err := ctx.AppDB().Query(
		`SELECT DISTINCT p.id FROM posts p
		  JOIN post_targets t ON t.post_id=p.id
		 WHERE p.project_id=? AND t.social_account_id=?`,
		pid, id,
	)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var postID int64
		if rows.Scan(&postID) == nil {
			postIDs = append(postIDs, postID)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	inboxRes, err := tx.Exec(`DELETE FROM inbox_items WHERE social_account_id=? AND project_id=?`, id, pid)
	if err != nil {
		return nil, fmt.Errorf("delete inbox items: %w", err)
	}
	cursorRes, err := tx.Exec(`DELETE FROM inbox_cursors WHERE social_account_id=?`, id)
	if err != nil {
		return nil, fmt.Errorf("delete inbox cursors: %w", err)
	}
	metricRes, err := tx.Exec(`DELETE FROM social_metric_points WHERE social_account_id=? AND project_id=?`, id, pid)
	if err != nil {
		return nil, fmt.Errorf("delete metric history: %w", err)
	}
	targetRes, err := tx.Exec(`DELETE FROM post_targets WHERE social_account_id=?`, id)
	if err != nil {
		return nil, fmt.Errorf("delete post targets: %w", err)
	}

	orphanPostIDs := []int64{}
	if len(postIDs) > 0 {
		qArgs := int64SliceToAny(postIDs)
		rows, err := tx.Query(
			`SELECT p.id FROM posts p
			 WHERE p.id IN (`+placeholders(len(postIDs))+`) AND p.project_id=?
			   AND NOT EXISTS (SELECT 1 FROM post_targets t WHERE t.post_id=p.id)`,
			append(qArgs, pid)...,
		)
		if err != nil {
			return nil, fmt.Errorf("find orphan posts: %w", err)
		}
		for rows.Next() {
			var postID int64
			if rows.Scan(&postID) == nil {
				orphanPostIDs = append(orphanPostIDs, postID)
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	jobIDs := []int64{}
	var postInboxDeleted, postsDeleted int64
	if len(orphanPostIDs) > 0 {
		qArgs := int64SliceToAny(orphanPostIDs)
		jobRows, err := tx.Query(
			`SELECT job_id FROM posts WHERE id IN (`+placeholders(len(orphanPostIDs))+`) AND job_id > 0`,
			qArgs...,
		)
		if err != nil {
			return nil, err
		}
		for jobRows.Next() {
			var jobID int64
			if jobRows.Scan(&jobID) == nil {
				jobIDs = append(jobIDs, jobID)
			}
		}
		jobRows.Close()
		res, err := tx.Exec(`DELETE FROM inbox_items WHERE post_id IN (`+placeholders(len(orphanPostIDs))+`)`, qArgs...)
		if err != nil {
			return nil, fmt.Errorf("delete post inbox items: %w", err)
		}
		postInboxDeleted, _ = res.RowsAffected()
		if _, err := tx.Exec(`DELETE FROM social_metric_points WHERE post_id IN (`+placeholders(len(orphanPostIDs))+`)`, qArgs...); err != nil {
			return nil, fmt.Errorf("delete post metric history: %w", err)
		}
		res, err = tx.Exec(
			`DELETE FROM posts WHERE id IN (`+placeholders(len(orphanPostIDs))+`) AND project_id=?`,
			append(qArgs, pid)...,
		)
		if err != nil {
			return nil, fmt.Errorf("delete posts: %w", err)
		}
		postsDeleted, _ = res.RowsAffected()
	}
	acctRes, err := tx.Exec(`DELETE FROM social_accounts WHERE id=? AND project_id=?`, id, pid)
	if err != nil {
		return nil, fmt.Errorf("delete account: %w", err)
	}
	if n, _ := acctRes.RowsAffected(); n != 1 {
		return mcpError("account not found"), nil
	}
	var siblings int
	_ = tx.QueryRow(
		`SELECT COUNT(*) FROM social_accounts WHERE connection_id=? AND status!='disconnected'`,
		connID,
	).Scan(&siblings)
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	for _, jobID := range jobIDs {
		a.cancelJob(ctx, jobID)
	}
	connectionDisconnected := false
	if siblings == 0 && providerSlug != zernioProviderSlug {
		if err := ctx.PlatformAPI().DisconnectConnection(connID); err != nil {
			ctx.Logger().Warn("DisconnectConnection failed", "conn", connID, "err", err)
		} else {
			connectionDisconnected = true
		}
	}
	inboxDeleted, _ := inboxRes.RowsAffected()
	cursorsDeleted, _ := cursorRes.RowsAffected()
	metricsDeleted, _ := metricRes.RowsAffected()
	targetsDeleted, _ := targetRes.RowsAffected()
	ctx.Emit("account.deleted", map[string]any{
		"social_account_id": id,
		"hard_delete":       true,
		"posts_deleted":     postsDeleted,
	})
	return map[string]any{
		"deleted":                  id,
		"hard_delete":              true,
		"posts_deleted":            postsDeleted,
		"post_targets_deleted":     targetsDeleted,
		"inbox_items_deleted":      inboxDeleted + postInboxDeleted,
		"inbox_cursors_deleted":    cursorsDeleted,
		"metric_points_deleted":    metricsDeleted,
		"connection_disconnected":  connectionDisconnected,
		"upstream_posts_untouched": true,
		"multi_account_posts_kept": len(postIDs) - int(postsDeleted),
	}, nil
}

// ─── post_create ──────────────────────────────────────────────────

// validateTargetOptions checks each target's options keys against the
// declared OptionFields for that target's platform and logs a warning
// on unknown keys. It does NOT reject — forward-compat matters more
// than strict validation here. Empty platforms (or platforms with
// only `body` semantics) accept just `body`.
func (a *App) validateTargetOptions(ctx *sdk.AppCtx, pid string, targets []targetSpec) {
	for _, t := range targets {
		if len(t.Options) == 0 {
			continue
		}
		var platform, providerSlug string
		_ = ctx.AppDB().QueryRow(
			`SELECT platform, COALESCE(provider_slug,'native') FROM social_accounts WHERE id=? AND project_id=?`,
			t.SocialAccountID, pid,
		).Scan(&platform, &providerSlug)
		if platform == "" {
			continue // unknown account — finalize will catch it
		}
		if providerSlug != "" && providerSlug != "native" {
			continue // provider adapters accept provider-specific option maps
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

func validatePostTargets(ctx *sdk.AppCtx, pid string, targets []targetSpec) (map[int64]int64, error) {
	if strings.TrimSpace(pid) == "" {
		return nil, errors.New("project_id required")
	}
	profiles := make(map[int64]int64, len(targets))
	seen := make(map[int64]struct{}, len(targets))
	for _, target := range targets {
		if _, exists := seen[target.SocialAccountID]; exists {
			return nil, fmt.Errorf("social_account_id %d is duplicated in this post", target.SocialAccountID)
		}
		seen[target.SocialAccountID] = struct{}{}
		var profileID int64
		var status string
		err := ctx.AppDB().QueryRow(
			`SELECT COALESCE(profile_id,0), status
			   FROM social_accounts
			  WHERE id=? AND project_id=?`,
			target.SocialAccountID, pid,
		).Scan(&profileID, &status)
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("social account %d not found in this project", target.SocialAccountID)
		}
		if err != nil {
			return nil, err
		}
		if status != "active" {
			return nil, fmt.Errorf("social account %d is %s; reconnect it before posting", target.SocialAccountID, status)
		}
		profiles[target.SocialAccountID] = profileID
	}
	return profiles, nil
}

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body, _ := args["body"].(string)

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
	if strings.TrimSpace(body) == "" {
		if len(rawTargets) == 0 {
			return nil, errors.New("body required")
		}
		for i, target := range targets {
			targetBody, _ := target.Options["body"].(string)
			if strings.TrimSpace(targetBody) == "" {
				return nil, fmt.Errorf("body required: pass top-level body or targets[%d].body", i)
			}
			if body == "" {
				body = targetBody
			}
		}
	}
	// Validate per-target options against each target's platform's
	// declared OptionFields. Unknown keys log a warning but don't fail
	// the call (forward-compat: an agent passing a field that lands
	// in a future version still works).
	pid := projectScope(ctx, args)
	targetProfiles, err := validatePostTargets(ctx, pid, targets)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	a.validateTargetOptions(ctx, pid, targets)
	// Flat list of just the account ids — used by profile-spanning
	// resolution below, same shape the prior code path relied on.
	acctIDs := make([]int64, len(targets))
	for i, t := range targets {
		acctIDs[i] = t.SocialAccountID
	}
	scheduleAt, _ := args["schedule_at"].(string)
	if strings.TrimSpace(scheduleAt) != "" {
		rfc, err := normaliseScheduleAt(scheduleAt)
		if err != nil {
			return mcpError(fmt.Sprintf("invalid schedule_at %q: %v", scheduleAt, err)), nil
		}
		scheduleAt = rfc
	}
	mediaIDsRaw, _ := args["media_storage_ids"].([]any)
	mediaIDs := []int64{}
	for _, v := range mediaIDsRaw {
		if id := toInt64Loose(v); id > 0 {
			mediaIDs = append(mediaIDs, id)
		}
	}
	mediaJSON, _ := json.Marshal(mediaIDs)
	mediaProjectID := strings.TrimSpace(stringArgAny(args, "media_project_id", "storage_project_id", "_project_id", "project_id"))

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
			spanned[targetProfiles[aid]] = true
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

	if scheduleAt != "" {
		if existing, err := a.findDuplicateScheduledPost(ctx, pid, profileID, body, string(mediaJSON), mediaProjectID, scheduleAt, targets); err != nil {
			return nil, err
		} else if existing != nil {
			if existing.Status == "failed" && existing.JobID == 0 {
				return a.retryFailedSchedule(ctx, pid, existing.ID, scheduleAt)
			}
			return map[string]any{
				"post_id":      existing.ID,
				"status":       existing.Status,
				"targets":      existing.Targets,
				"duplicate_of": existing.ID,
				"deduped":      true,
			}, nil
		}
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO posts (project_id, body, media_storage_ids, media_project_id, schedule_at, status, profile_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, body, string(mediaJSON), mediaProjectID, nullable(scheduleAt), status, profileID,
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
		_, err := tx.Exec(
			`INSERT INTO post_targets (post_id, social_account_id, options) VALUES (?, ?, ?)`,
			postID, t.SocialAccountID, optsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("create post target for account %d: %w", t.SocialAccountID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	// Two execution paths:
	//   - schedule_at empty → publish inline now.
	//   - schedule_at set → schedule a job via the jobs app (when bound)
	//     so the publish is durable. If jobs isn't bound, fall back to
	//     the app's minute worker publishes it at the requested time.
	if scheduleAt == "" {
		a.publishPostTargets(ctx, postID)
	} else if ctx.IntegrationFor("jobs") == nil {
		ctx.Logger().Info("post scheduled for worker fallback", "post", postID, "run_at", scheduleAt)
	} else {
		jobID, err := a.scheduleJob(ctx, postID, scheduleAt)
		if err != nil {
			ctx.Logger().Warn("schedule via jobs failed; using worker fallback", "post", postID, "err", err)
			_, _ = ctx.AppDB().Exec(`UPDATE posts SET job_id=0, status='scheduled' WHERE id=?`, postID)
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
	out := map[string]any{
		"post_id":      postID,
		"status":       status,
		"target_count": len(acctIDs),
	}
	if scheduleAt == "" {
		if finalStatus := a.postStatus(ctx, postID); finalStatus != "" {
			out["status"] = finalStatus
		}
		out["targets"] = a.loadTargets(ctx, postID)
	} else {
		out["targets"] = len(acctIDs)
		var jobID int64
		_ = ctx.AppDB().QueryRow(`SELECT COALESCE(job_id,0) FROM posts WHERE id=?`, postID).Scan(&jobID)
		if jobID == 0 {
			out["worker_fallback"] = true
		}
	}
	return out, nil
}

func (a *App) postStatus(ctx *sdk.AppCtx, postID int64) string {
	var status string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, postID).Scan(&status)
	return status
}

type duplicatePost struct {
	ID      int64
	Status  string
	JobID   int64
	Targets int
}

func (a *App) findDuplicateScheduledPost(ctx *sdk.AppCtx, pid string, profileID int64, body, mediaJSON, mediaProjectID, scheduleAt string, targets []targetSpec) (*duplicatePost, error) {
	wantSig := canonicalTargetSignature(targets)
	rows, err := ctx.AppDB().Query(
		`SELECT p.id, p.status, COALESCE(p.job_id,0), COUNT(t.id)
		   FROM posts p
		   JOIN post_targets t ON t.post_id=p.id
		  WHERE p.project_id=?
		    AND COALESCE(p.profile_id,0)=?
		    AND p.body=?
		    AND COALESCE(p.media_storage_ids,'[]')=?
		    AND COALESCE(p.media_project_id,'')=?
		    AND COALESCE(p.schedule_at,'')=?
		    AND p.status IN ('scheduled','publishing','published','partial','failed')
		  GROUP BY p.id, p.status, p.job_id
		  ORDER BY p.id ASC`,
		pid, profileID, body, mediaJSON, mediaProjectID, scheduleAt,
	)
	if err != nil {
		return nil, err
	}
	candidates := []duplicatePost{}
	for rows.Next() {
		var dup duplicatePost
		if err := rows.Scan(&dup.ID, &dup.Status, &dup.JobID, &dup.Targets); err != nil {
			continue
		}
		candidates = append(candidates, dup)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, dup := range candidates {
		gotSig, err := a.targetSignatureForPost(ctx, dup.ID)
		if err != nil {
			return nil, err
		}
		if gotSig == wantSig {
			return &dup, nil
		}
	}
	return nil, nil
}

func (a *App) targetSignatureForPost(ctx *sdk.AppCtx, postID int64) (string, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT social_account_id, COALESCE(options,'')
		   FROM post_targets
		  WHERE post_id=?
		  ORDER BY social_account_id ASC, id ASC`,
		postID,
	)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	type sigTarget struct {
		ID      int64  `json:"id"`
		Options string `json:"options"`
	}
	out := []sigTarget{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			continue
		}
		out = append(out, sigTarget{ID: id, Options: canonicalOptionsJSONRaw(raw)})
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func canonicalTargetSignature(targets []targetSpec) string {
	type sigTarget struct {
		ID      int64  `json:"id"`
		Options string `json:"options"`
	}
	out := make([]sigTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, sigTarget{
			ID:      target.SocialAccountID,
			Options: canonicalOptionsJSON(target.Options),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID == out[j].ID {
			return out[i].Options < out[j].Options
		}
		return out[i].ID < out[j].ID
	})
	b, _ := json.Marshal(out)
	return string(b)
}

func canonicalOptionsJSON(opts map[string]any) string {
	if len(opts) == 0 {
		return "{}"
	}
	b, err := json.Marshal(opts)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func canonicalOptionsJSONRaw(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}"
	}
	var opts map[string]any
	if err := json.Unmarshal([]byte(raw), &opts); err != nil {
		return raw
	}
	return canonicalOptionsJSON(opts)
}

func (a *App) retryFailedSchedule(ctx *sdk.AppCtx, pid string, postID int64, scheduleAt string) (any, error) {
	rfc, err := normaliseScheduleAt(scheduleAt)
	if err != nil {
		return mcpError("invalid schedule_at: " + err.Error()), nil
	}
	if ctx.IntegrationFor("jobs") == nil {
		_, err = ctx.AppDB().Exec(
			`UPDATE posts SET status='scheduled', schedule_at=?, job_id=0, published_at=NULL WHERE id=? AND project_id=?`,
			rfc, postID, pid,
		)
		if err != nil {
			return nil, err
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets SET status='pending', last_error=NULL WHERE post_id=? AND status='failed' AND attempts=0`,
			postID,
		)
		return map[string]any{
			"post_id":              postID,
			"status":               "scheduled",
			"job_id":               int64(0),
			"worker_fallback":      true,
			"retried_scheduling":   true,
			"reused_existing_post": true,
		}, nil
	}
	jobID, err := a.scheduleJob(ctx, postID, rfc)
	if err != nil {
		ctx.Logger().Warn("scheduling retry via jobs failed; using worker fallback", "post", postID, "err", err)
		jobID = 0
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE posts SET status='scheduled', schedule_at=?, job_id=?, published_at=NULL WHERE id=? AND project_id=?`,
		rfc, jobID, postID, pid,
	)
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='pending', last_error=NULL
		  WHERE post_id=? AND status='failed' AND attempts=0`,
		postID,
	)
	ctx.Emit("post.rescheduled", map[string]any{
		"post_id": postID,
		"job_id":  jobID,
		"run_at":  rfc,
	})
	out := map[string]any{
		"post_id":              postID,
		"status":               "scheduled",
		"job_id":               jobID,
		"retried_scheduling":   true,
		"reused_existing_post": true,
	}
	if jobID == 0 {
		out["worker_fallback"] = true
	}
	return out, nil
}

// publishJob is the unit of work for one (post, social_account)
// combination. Built once from the post + post_target row and passed
// to the platform-specific publish strategy.
type publishJob struct {
	targetID, connID  int64
	platform, extID   string
	providerSlug      string
	providerAccountID string
	body              string      // already resolved: target.options["body"] || post.body
	media             []mediaItem // resolved (URL + MIME) so strategies can branch image/video
	mediaProjectID    string      // Storage project scope for attached media + thumbnail defaults
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
	// A queued post must not revive an account the user disconnected
	// after scheduling. Mark those destinations terminal before loading
	// publish jobs so Jobs can finish cleanly instead of retrying forever.
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='failed', last_error='social account is not active'
		  WHERE post_id=? AND status='pending'
		    AND EXISTS (
		      SELECT 1 FROM social_accounts a
		       WHERE a.id=post_targets.social_account_id AND a.status!='active'
		    )`,
		postID,
	)
	// Load the post's media ids once — every target gets the same media.
	mediaIDs, mediaProjectID := a.loadPostMedia(ctx, postID)
	media, mediaErr := a.resolveMedia(ctx, mediaIDs, mediaProjectID)
	if mediaErr != nil {
		ctx.Logger().Warn("resolve media urls", "post", postID, "err", mediaErr)
		// Surface this per-target below. If the user attached media,
		// publishing text-only would create a misleading upstream post.
	}

	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.platform, a.connection_id,
		        COALESCE(a.external_account_id,''), p.body,
		        COALESCE(a.page_credentials,''),
		        COALESCE(t.options,''),
		        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,'')
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
		if err := rows.Scan(&j.targetID, &acctID, &j.platform, &j.connID, &j.extID, &postBody, &j.pageCreds, &optsRaw, &j.providerSlug, &j.providerAccountID); err != nil {
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
		j.mediaProjectID = mediaProjectID
		jobs = append(jobs, j)
	}
	rows.Close()

	successes := 0
	failures := 0
	if len(jobs) == 0 {
		a.rollupPostStatus(ctx, postID)
		return
	}
	for _, j := range jobs {
		claimed, err := claimPostTarget(ctx, j.targetID)
		if err != nil {
			ctx.Logger().Warn("claim target", "target", j.targetID, "err", err)
			continue
		}
		if !claimed {
			continue
		}
		if j.providerSlug == zernioProviderSlug {
			if len(mediaIDs) > 0 && (mediaErr != nil || len(j.media) == 0) {
				msg := "attached media could not be resolved"
				if mediaErr != nil {
					msg += ": " + mediaErr.Error()
				}
				a.markTargetFailed(ctx, j.targetID, msg)
				failures++
				continue
			}
			platformPostID, platformURL, err := a.publishZernio(ctx, j)
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
				"provider":         j.providerSlug,
				"platform_post_id": platformPostID,
				"platform_url":     platformURL,
			})
			successes++
			continue
		}
		def, ok := platforms[j.platform]
		if !ok {
			a.markTargetFailed(ctx, j.targetID, "unsupported platform: "+j.platform)
			failures++
			continue
		}
		if len(mediaIDs) > 0 && (mediaErr != nil || len(j.media) == 0) {
			msg := "attached media could not be resolved"
			if mediaErr != nil {
				msg += ": " + mediaErr.Error()
			}
			a.markTargetFailed(ctx, j.targetID, msg)
			failures++
			continue
		}
		if def.MediaRequired && len(j.media) == 0 {
			a.markTargetFailed(ctx, j.targetID,
				fmt.Sprintf("%s requires at least one media file (image or video). Pass media_storage_ids in post_create or attach media in the panel.", def.DisplayName))
			failures++
			continue
		}
		platformPostID, platformURL, err := a.runStrategy(ctx, def, j)
		if err != nil {
			var warning *publishedWarningError
			if errors.As(err, &warning) && platformPostID != "" {
				_, _ = ctx.AppDB().Exec(
					`UPDATE post_targets SET status='published', platform_post_id=?, platform_url=?, published_at=CURRENT_TIMESTAMP, last_error=? WHERE id=?`,
					nullable(platformPostID), nullable(platformURL), warning.Error(), j.targetID,
				)
				ctx.Emit("target.published_warning", map[string]any{
					"target_id": j.targetID,
					"platform":  j.platform,
					"warning":   warning.Error(),
				})
				successes++
				continue
			}
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

	finalStatus := a.rollupPostStatus(ctx, postID)
	ctx.Emit("post.completed", map[string]any{
		"post_id":  postID,
		"status":   finalStatus,
		"success":  successes,
		"failures": failures,
	})
}

type publishedWarningError struct{ warning string }

func (e *publishedWarningError) Error() string { return e.warning }

func claimPostTarget(ctx *sdk.AppCtx, targetID int64) (bool, error) {
	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='publishing', attempts=attempts+1, last_attempt_at=CURRENT_TIMESTAMP
		  WHERE id=? AND status='pending'`,
		targetID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (a *App) rollupPostStatus(ctx *sdk.AppCtx, postID int64) string {
	rows, err := ctx.AppDB().Query(`SELECT status FROM post_targets WHERE post_id=?`, postID)
	if err != nil {
		ctx.Logger().Warn("rollup post status", "post", postID, "err", err)
		return ""
	}
	defer rows.Close()
	total, published, failed, active := 0, 0, 0, 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			continue
		}
		total++
		switch status {
		case "published":
			published++
		case "failed":
			failed++
		case "pending", "publishing":
			active++
		}
	}
	if total == 0 {
		return ""
	}
	if active > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='publishing' WHERE id=? AND status NOT IN ('scheduled')`, postID)
		return "publishing"
	}
	finalStatus := "published"
	if failed > 0 && published == 0 {
		finalStatus = "failed"
	} else if failed > 0 {
		finalStatus = "partial"
	}
	if finalStatus == "published" || finalStatus == "partial" {
		_, _ = ctx.AppDB().Exec(
			`UPDATE posts SET status=?, published_at=COALESCE(published_at, CURRENT_TIMESTAMP) WHERE id=?`,
			finalStatus, postID,
		)
	} else {
		_, _ = ctx.AppDB().Exec(
			`UPDATE posts SET status=?, published_at=NULL WHERE id=?`,
			finalStatus, postID,
		)
	}
	return finalStatus
}

// runStrategy dispatches to the platform's publish flow. Returns the
// platform-side post id + URL on success, or an error to record on the
// target row.
func (a *App) runStrategy(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	switch def.Strategy {
	case "twitter":
		return a.publishTwitter(ctx, def, j)
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
	if isVideo && def.ThumbnailURLField != "" {
		thumb, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID)
		if err != nil {
			return "", "", err
		}
		if thumb != nil {
			input[def.ThumbnailURLField] = thumb.URL
		}
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

// publishTwitter uploads attached media through X's media API first,
// then creates the post with media.media_ids. Text-only posts still use
// the simple post_tweet path. X accepts up to 4 images, or one video/GIF.
func (a *App) publishTwitter(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	bodyField := def.BodyField
	if bodyField == "" {
		bodyField = "text"
	}
	input := map[string]any{bodyField: j.body}
	if len(j.media) > 0 {
		ids, err := a.uploadTwitterMedia(ctx, j.connID, j.media)
		if err != nil {
			return "", "", err
		}
		if len(ids) > 0 {
			input["media"] = map[string]any{"media_ids": ids}
		}
	}
	ctx.Logger().Info("publishTwitter: calling post_tweet",
		"media_count", len(j.media), "has_media_ids", input["media"] != nil)
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
	if err != nil {
		return "", "", fmt.Errorf("post_tweet: %w", err)
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	id, url := extractPostIdentity(def.Platform, out.Data)
	ctx.Logger().Info("publishTwitter: published",
		"platform_post_id", id, "platform_url", url)
	return id, url, nil
}

func (a *App) uploadTwitterMedia(ctx *sdk.AppCtx, connID int64, media []mediaItem) ([]string, error) {
	if len(media) == 0 {
		return nil, nil
	}
	firstCat, err := twitterMediaCategory(media[0])
	if err != nil {
		return nil, err
	}
	items := media
	if firstCat == "tweet_video" || firstCat == "tweet_gif" {
		items = media[:1]
		for _, m := range media[1:] {
			cat, err := twitterMediaCategory(m)
			if err != nil {
				return nil, err
			}
			if cat != "tweet_image" {
				continue
			}
			return nil, errors.New("x does not support mixing video/GIF media with images in the same post")
		}
	} else if len(items) > 4 {
		items = items[:4]
	}

	ids := make([]string, 0, len(items))
	for i, item := range items {
		cat, err := twitterMediaCategory(item)
		if err != nil {
			return nil, err
		}
		if firstCat == "tweet_image" && cat != "tweet_image" {
			return nil, errors.New("x does not support mixing images with video/GIF media in the same post")
		}
		var id string
		if cat == "tweet_image" {
			if item.Bytes > 5*1024*1024 {
				return nil, fmt.Errorf("x image %d is too large: %d bytes (max 5 MB)", i+1, item.Bytes)
			}
			id, err = a.uploadTwitterMediaSimple(ctx, connID, item, cat)
		} else {
			id, err = a.uploadTwitterMediaChunked(ctx, connID, item, cat)
		}
		if err != nil {
			return nil, fmt.Errorf("x media %d upload: %w", i+1, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (a *App) uploadTwitterMediaSimple(ctx *sdk.AppCtx, connID int64, item mediaItem, category string) (string, error) {
	const maxSimpleImage = int64(5 * 1024 * 1024)
	body, err := readMediaURL(item.URL, maxSimpleImage+1)
	if err != nil {
		return "", err
	}
	if int64(len(body)) > maxSimpleImage {
		return "", errors.New("x image is larger than 5 MB")
	}
	input := map[string]any{
		"media":          base64.StdEncoding.EncodeToString(body),
		"media_category": category,
	}
	if item.Mime != "" {
		input["media_type"] = item.Mime
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "upload_media", input)
	if err != nil {
		return "", fmt.Errorf("upload_media: %w", err)
	}
	if out == nil || !out.Success {
		return "", upstreamError(out)
	}
	upload := extractTwitterMediaUpload(out.Data)
	if upload.ID == "" {
		return "", fmt.Errorf("upload_media returned no media id: %s", string(out.Data))
	}
	if err := a.waitTwitterMediaReady(ctx, connID, upload); err != nil {
		return "", err
	}
	return upload.ID, nil
}

func (a *App) uploadTwitterMediaChunked(ctx *sdk.AppCtx, connID int64, item mediaItem, category string) (string, error) {
	if item.Bytes <= 0 {
		return "", errors.New("chunked X media upload needs size_bytes from storage")
	}
	mime := item.Mime
	if mime == "" {
		if category == "tweet_video" {
			mime = "video/mp4"
		} else if category == "tweet_gif" {
			mime = "image/gif"
		} else {
			mime = "image/jpeg"
		}
	}
	initInput := map[string]any{
		"total_bytes":    item.Bytes,
		"media_type":     mime,
		"media_category": category,
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "upload_init", initInput)
	if err != nil {
		return "", fmt.Errorf("upload_init: %w", err)
	}
	if out == nil || !out.Success {
		return "", upstreamError(out)
	}
	upload := extractTwitterMediaUpload(out.Data)
	if upload.ID == "" {
		return "", fmt.Errorf("upload_init returned no media id: %s", string(out.Data))
	}

	getReq, err := http.NewRequest(http.MethodGet, item.URL, nil)
	if err != nil {
		return "", fmt.Errorf("build storage GET: %w", err)
	}
	getResp, err := http.DefaultClient.Do(getReq)
	if err != nil {
		return "", fmt.Errorf("fetch media bytes from storage: %w", err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch media: storage returned %d", getResp.StatusCode)
	}

	const chunkSize = int64(4 * 1024 * 1024)
	totalChunks := int((item.Bytes + chunkSize - 1) / chunkSize)
	if totalChunks > 1000 {
		return "", fmt.Errorf("x media too large for chunked upload: %d chunks", totalChunks)
	}
	for i := 0; i < totalChunks; i++ {
		want := chunkSize
		if remaining := item.Bytes - int64(i)*chunkSize; remaining < want {
			want = remaining
		}
		buf := make([]byte, int(want))
		n, err := io.ReadFull(getResp.Body, buf)
		if err != nil {
			return "", fmt.Errorf("read chunk %d/%d from storage: %w", i+1, totalChunks, err)
		}
		if int64(n) != want {
			return "", fmt.Errorf("read chunk %d/%d from storage: got %d bytes, want %d", i+1, totalChunks, n, want)
		}
		appendInput := map[string]any{
			"media_id":      upload.ID,
			"segment_index": i,
			"media":         base64.StdEncoding.EncodeToString(buf),
		}
		ctx.Logger().Info("publishTwitter: append media chunk",
			"chunk", fmt.Sprintf("%d/%d", i+1, totalChunks), "bytes", want)
		appendOut, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "upload_append", appendInput)
		if err != nil {
			return "", fmt.Errorf("upload_append chunk %d/%d: %w", i+1, totalChunks, err)
		}
		if appendOut == nil || !appendOut.Success {
			return "", upstreamError(appendOut)
		}
	}

	finalOut, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "upload_finalize", map[string]any{
		"media_id": upload.ID,
	})
	if err != nil {
		return "", fmt.Errorf("upload_finalize: %w", err)
	}
	if finalOut == nil || !finalOut.Success {
		return "", upstreamError(finalOut)
	}
	finalUpload := extractTwitterMediaUpload(finalOut.Data)
	if finalUpload.ID == "" {
		finalUpload.ID = upload.ID
	}
	if err := a.waitTwitterMediaReady(ctx, connID, finalUpload); err != nil {
		return "", err
	}
	return finalUpload.ID, nil
}

func (a *App) waitTwitterMediaReady(ctx *sdk.AppCtx, connID int64, upload twitterMediaUpload) error {
	if upload.ID == "" {
		return errors.New("missing X media id")
	}
	originalID := upload.ID
	state := upload.ProcessingInfo.State
	if state == "" || state == "succeeded" {
		return nil
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if state == "failed" {
			return fmt.Errorf("x media processing failed: %s", upload.ProcessingInfo.ErrorMessage)
		}
		if state != "pending" && state != "in_progress" {
			return fmt.Errorf("x media processing returned unknown state %q", state)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("x media processing still %q after 5m", state)
		}
		wait := upload.ProcessingInfo.CheckAfterSecs
		if wait <= 0 {
			wait = 2
		}
		if wait > 15 {
			wait = 15
		}
		time.Sleep(time.Duration(wait) * time.Second)
		out, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_media_upload_status", map[string]any{
			"media_id": upload.ID,
		})
		if err != nil {
			return fmt.Errorf("get_media_upload_status: %w", err)
		}
		if out == nil || !out.Success {
			return upstreamError(out)
		}
		upload = extractTwitterMediaUpload(out.Data)
		if upload.ID == "" {
			upload.ID = originalID
		}
		state = upload.ProcessingInfo.State
		if state == "" || state == "succeeded" {
			return nil
		}
	}
}

type twitterMediaUpload struct {
	ID             string
	ProcessingInfo struct {
		State          string
		CheckAfterSecs int
		ErrorMessage   string
	}
}

func extractTwitterMediaUpload(raw json.RawMessage) twitterMediaUpload {
	var resp struct {
		ID      string `json:"id"`
		MediaID string `json:"media_id"`
		Data    struct {
			ID             string `json:"id"`
			MediaID        string `json:"media_id"`
			ProcessingInfo struct {
				State          string         `json:"state"`
				CheckAfterSecs int            `json:"check_after_secs"`
				Error          map[string]any `json:"error"`
			} `json:"processing_info"`
		} `json:"data"`
		ProcessingInfo struct {
			State          string         `json:"state"`
			CheckAfterSecs int            `json:"check_after_secs"`
			Error          map[string]any `json:"error"`
		} `json:"processing_info"`
	}
	_ = json.Unmarshal(raw, &resp)
	var out twitterMediaUpload
	out.ID = resp.Data.ID
	if out.ID == "" {
		out.ID = resp.Data.MediaID
	}
	if out.ID == "" {
		out.ID = resp.ID
	}
	if out.ID == "" {
		out.ID = resp.MediaID
	}
	pi := resp.Data.ProcessingInfo
	if pi.State == "" {
		pi = resp.ProcessingInfo
	}
	out.ProcessingInfo.State = pi.State
	out.ProcessingInfo.CheckAfterSecs = pi.CheckAfterSecs
	if pi.Error != nil {
		if msg := toString(pi.Error["message"]); msg != "" {
			out.ProcessingInfo.ErrorMessage = msg
		} else if msg := toString(pi.Error["name"]); msg != "" {
			out.ProcessingInfo.ErrorMessage = msg
		} else {
			b, _ := json.Marshal(pi.Error)
			out.ProcessingInfo.ErrorMessage = string(b)
		}
	}
	return out
}

func twitterMediaCategory(item mediaItem) (string, error) {
	mime := strings.ToLower(item.Mime)
	switch {
	case strings.HasPrefix(mime, "video/"):
		return "tweet_video", nil
	case mime == "image/gif":
		return "tweet_gif", nil
	case strings.HasPrefix(mime, "image/"):
		return "tweet_image", nil
	default:
		if mime == "" {
			return "", errors.New("x media upload needs content_type from storage")
		}
		return "", fmt.Errorf("x does not support media type %q", item.Mime)
	}
}

func readMediaURL(url string, limit int64) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build storage GET: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch media bytes from storage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch media: storage returned %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
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
		if thumb, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID); err != nil {
			return "", "", err
		} else if thumb != nil && def.ThumbnailURLField != "" {
			containerInput[def.ThumbnailURLField] = thumb.URL
		} else if ms, ok := numericOption(j.options, "thumbnail_frame_ms"); ok && def.ThumbnailFrameField != "" {
			containerInput[def.ThumbnailFrameField] = ms
		}
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
	return id, a.instagramPermalink(ctx, j, id, pageToken), nil
}

func (a *App) instagramPermalink(ctx *sdk.AppCtx, j publishJob, mediaID, pageToken string) string {
	if mediaID == "" {
		return ""
	}
	input := map[string]any{
		"instagramAccountId": j.extID,
		"fields":             "id,permalink",
		"limit":              25,
	}
	if pageToken != "" {
		input["access_token"] = pageToken
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "get_account_media", input)
	if err != nil || res == nil || !res.Success {
		return ""
	}
	var payload struct {
		Data []struct {
			ID        string `json:"id"`
			Permalink string `json:"permalink"`
		} `json:"data"`
	}
	if json.Unmarshal(res.Data, &payload) != nil {
		return ""
	}
	for _, item := range payload.Data {
		if item.ID == mediaID {
			return item.Permalink
		}
	}
	return ""
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
	thumbnail, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID)
	if err != nil {
		return "", "", err
	}
	if thumbnail != nil {
		if err := validateYouTubeThumbnail(*thumbnail); err != nil {
			return "", "", err
		}
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
	//                 Defaults to public so a successful Social publish
	//                 is visible without a second manual YouTube step.
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
		visibility = "public"
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
		"status": map[string]any{
			"privacyStatus":           visibility,
			"selfDeclaredMadeForKids": false,
		},
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
	if thumbnail != nil {
		if err := a.setBinaryThumbnail(ctx, def, j.connID, resource.ID, *thumbnail); err != nil {
			// The video is already uploaded at this point. Do not return
			// an error that would make post_retry upload a duplicate video;
			// surface this in logs so the user can edit the post and retry
			// only the thumbnail later.
			ctx.Logger().Warn("publishYoutube: thumbnail set failed",
				"video_id", resource.ID, "thumbnail_id", thumbnail.ID, "err", err)
			return resource.ID, url, &publishedWarningError{warning: "video published, but custom thumbnail failed: " + err.Error()}
		}
	}
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

// publishTikTok drives TikTok's native publish flows.
//
// Videos use FILE_UPLOAD because TikTok gives us a temporary upload URL
// and does not require the Apteva/storage domain to be verified. Photo
// posts are different in TikTok's API: /post/publish/content/init/ only
// accepts PULL_FROM_URL, so the Storage URLs must be public and the URL
// prefix/domain must be verified in the TikTok developer app.
func (a *App) publishTikTok(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) == 0 {
		return "", "", errors.New("tiktok requires media")
	}
	first := j.media[0]
	if first.IsImage() {
		return a.publishTikTokPhotos(ctx, j)
	}
	if first.IsVideo() {
		return a.publishTikTokVideo(ctx, def, j)
	}
	return "", "", fmt.Errorf("tiktok only accepts image or video files, got %q", first.Mime)
}

func (a *App) publishTikTokPhotos(ctx *sdk.AppCtx, j publishJob) (string, string, error) {
	if len(j.media) > 35 {
		return "", "", fmt.Errorf("tiktok photo posts accept at most 35 images, got %d", len(j.media))
	}
	images := make([]string, 0, len(j.media))
	for i, item := range j.media {
		if !item.IsImage() {
			return "", "", fmt.Errorf("tiktok photo posts cannot mix images with %q media at index %d", item.Mime, i)
		}
		if item.URL == "" {
			return "", "", fmt.Errorf("tiktok photo %d has no public URL", i)
		}
		images = append(images, item.URL)
	}
	coverIndex := 0
	if n, ok := numericOption(j.options, "photo_cover_index"); ok {
		coverIndex = int(n)
	}
	if coverIndex < 0 || coverIndex >= len(images) {
		return "", "", fmt.Errorf("tiktok photo_cover_index %d out of range for %d images", coverIndex, len(images))
	}
	postInfo := map[string]any{
		"description":          j.body,
		"privacy_level":        "PUBLIC_TO_EVERYONE",
		"brand_content_toggle": false,
		"brand_organic_toggle": false,
	}
	if title := strings.TrimSpace(strOption(j.options, "title")); title != "" {
		postInfo["title"] = title
	}
	if autoMusic, ok := boolOption(j.options, "auto_add_music"); ok {
		postInfo["auto_add_music"] = autoMusic
	}
	input := map[string]any{
		"post_info": postInfo,
		"source_info": map[string]any{
			"source":            "PULL_FROM_URL",
			"photo_images":      images,
			"photo_cover_index": coverIndex,
		},
		"post_mode":  "DIRECT_POST",
		"media_type": "PHOTO",
	}
	ctx.Logger().Info("publishTikTok: init photo post", "image_count", len(images), "cover_index", coverIndex)
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, "post_photo", input)
	if err != nil {
		return "", "", fmt.Errorf("post_photo: %w", err)
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	pubID := extractTikTokPublishID(out.Data)
	if pubID == "" {
		return "", "", fmt.Errorf("post_photo: missing publish_id in response: %s", string(out.Data))
	}
	return a.waitTikTokPublish(ctx, j.connID, pubID)
}

// publishTikTokVideo drives TikTok's video publish flow.
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
func (a *App) publishTikTokVideo(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
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
	postInfo := map[string]any{
		"title":         j.body,
		"privacy_level": "PUBLIC_TO_EVERYONE", // sensible default; future: per-target override
	}
	if ms, ok := numericOption(j.options, "thumbnail_frame_ms"); ok && def.ThumbnailFrameField != "" {
		postInfo[def.ThumbnailFrameField] = ms
	}
	initInput := map[string]any{
		"post_info": postInfo,
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
	return a.waitTikTokPublish(ctx, j.connID, publishID)
}

func (a *App) waitTikTokPublish(ctx *sdk.AppCtx, connID int64, publishID string) (string, string, error) {
	if publishID == "" {
		return "", "", errors.New("TikTok returned no publish_id")
	}
	deadline := time.Now().Add(5 * time.Minute)
	for {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_publish_status", map[string]any{"publish_id": publishID})
		if err != nil || res == nil || !res.Success {
			message := "TikTok accepted the upload, but publish status could not be verified"
			if err != nil {
				message += ": " + err.Error()
			} else if res != nil {
				message += ": " + upstreamError(res).Error()
			}
			return publishID, "", &publishedWarningError{warning: message}
		}
		var payload struct {
			Data struct {
				Status  string   `json:"status"`
				Fail    string   `json:"fail_reason"`
				PostIDs []string `json:"publicaly_available_post_id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &payload); err != nil {
			return publishID, "", &publishedWarningError{warning: "TikTok accepted the upload, but returned an unreadable publish status"}
		}
		switch payload.Data.Status {
		case "PUBLISH_COMPLETE":
			if len(payload.Data.PostIDs) > 0 && payload.Data.PostIDs[0] != "" {
				return payload.Data.PostIDs[0], "", nil
			}
			return publishID, "", nil
		case "FAILED":
			return "", "", fmt.Errorf("TikTok publish failed: %s", payload.Data.Fail)
		case "PROCESSING_UPLOAD", "PROCESSING_DOWNLOAD":
			// Keep polling below.
		default:
			return publishID, "", &publishedWarningError{warning: "TikTok accepted the upload, but returned unexpected publish status " + strconv.Quote(payload.Data.Status)}
		}
		if time.Now().After(deadline) {
			return publishID, "", &publishedWarningError{warning: "TikTok accepted the upload, but it was still processing after 5 minutes"}
		}
		time.Sleep(5 * time.Second)
	}
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

// loadPostMedia reads the post's media_storage_ids JSON column and
// the Storage project scope that owns those file ids.
func (a *App) loadPostMedia(ctx *sdk.AppCtx, postID int64) ([]int64, string) {
	var raw, mediaProjectID string
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(media_storage_ids,'[]'), COALESCE(media_project_id,'') FROM posts WHERE id=?`,
		postID,
	).Scan(&raw, &mediaProjectID)
	var out []int64
	_ = json.Unmarshal([]byte(raw), &out)
	return out, strings.TrimSpace(mediaProjectID)
}

// mediaItem is a resolved media file — public URL + MIME + byte size
// so callers can branch image vs video without a second round-trip,
// and chunked-upload paths (TikTok FILE_UPLOAD) can pre-compute chunk
// counts without a second files_get. Bytes is 0 when storage didn't
// return size_bytes (older storage versions); strategies that need
// it should error out clearly rather than guess.
type mediaItem struct {
	ID    int64
	URL   string
	Mime  string
	Bytes int64
}

// IsVideo reports whether this is a video MIME type.
func (m mediaItem) IsVideo() bool { return strings.HasPrefix(m.Mime, "video/") }

// IsImage reports whether this is an image MIME type.
func (m mediaItem) IsImage() bool { return strings.HasPrefix(m.Mime, "image/") }

// resolveMedia turns storage file ids into absolute, publicly fetchable
// URLs paired with the file's content_type. Calls storage.files_get
// for the metadata and storage.files_get_url for the signed URL.
// The URL must be reachable from the social platform's servers — for
// local dev, point APTEVA_PUBLIC_URL at an ngrok tunnel.
func (a *App) resolveMedia(ctx *sdk.AppCtx, ids []int64, projectID string) ([]mediaItem, error) {
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
		storageArgs := map[string]any{"id": id}
		if projectID != "" {
			storageArgs["_project_id"] = projectID
		}
		// Metadata first — content_type drives the publish strategy
		// (image → /feed; video → /videos for FB; REELS for IG).
		var meta struct {
			File struct {
				ContentType string `json:"content_type"`
				SizeBytes   int64  `json:"size_bytes"`
			} `json:"file"`
			ContentType string `json:"content_type"`
			SizeBytes   int64  `json:"size_bytes"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", storageArgs, &meta); err != nil {
			return nil, fmt.Errorf("storage files_get(%d): %w", id, err)
		}
		mime := meta.ContentType
		if mime == "" {
			mime = meta.File.ContentType
		}
		size := meta.SizeBytes
		if size == 0 {
			size = meta.File.SizeBytes
		}

		// Signed URL — separate call because files_get_url is the
		// canonical way to mint a TTL'd link.
		var signed struct {
			URL string `json:"url"`
		}
		urlArgs := map[string]any{
			"id":          id,
			"ttl_seconds": 3600,
		}
		if projectID != "" {
			urlArgs["_project_id"] = projectID
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", urlArgs, &signed); err != nil {
			return nil, fmt.Errorf("storage files_get_url(%d): %w", id, err)
		}
		rel := signed.URL
		if rel == "" {
			return nil, fmt.Errorf("storage files_get_url(%d) returned no url", id)
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
		out = append(out, mediaItem{ID: id, URL: fullURL, Mime: mime, Bytes: size})
	}
	return out, nil
}

const youtubeThumbnailMaxBytes int64 = 2 * 1024 * 1024

func thumbnailStorageID(opts map[string]any) int64 {
	if opts == nil {
		return 0
	}
	return toInt64Loose(opts["thumbnail_storage_id"])
}

func numericOption(opts map[string]any, key string) (int64, bool) {
	if opts == nil {
		return 0, false
	}
	n := toInt64Loose(opts[key])
	return n, n > 0
}

func (a *App) resolveThumbnailOption(ctx *sdk.AppCtx, opts map[string]any, defaultProjectID string) (*mediaItem, error) {
	id := thumbnailStorageID(opts)
	if id <= 0 {
		return nil, nil
	}
	projectID := strOption(opts, "thumbnail_project_id")
	if projectID == "" {
		projectID = strings.TrimSpace(defaultProjectID)
	}
	if projectID == "" {
		projectID = strings.TrimSpace(ctx.CurrentProject())
	}
	items, err := a.resolveMedia(ctx, []int64{id}, projectID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("thumbnail_storage_id %d resolved no file", id)
	}
	if !items[0].IsImage() {
		return nil, fmt.Errorf("thumbnail_storage_id %d is %q, expected an image", id, items[0].Mime)
	}
	return &items[0], nil
}

func validateYouTubeThumbnail(m mediaItem) error {
	if m.Bytes > youtubeThumbnailMaxBytes {
		return fmt.Errorf("youtube thumbnail too large: %d bytes (max 2 MB)", m.Bytes)
	}
	switch m.Mime {
	case "", "image/jpeg", "image/jpg", "image/png", "application/octet-stream":
		return nil
	default:
		return fmt.Errorf("youtube thumbnail must be JPEG or PNG, got %q", m.Mime)
	}
}

func fetchBinaryEnvelope(url, mime string, limit int64) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch thumbnail: storage returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("thumbnail exceeds %d bytes", limit)
	}
	if mime == "" {
		mime = "image/jpeg"
	}
	return map[string]any{
		"_binary":  true,
		"base64":   base64.StdEncoding.EncodeToString(body),
		"mimeType": mime,
	}, nil
}

func (a *App) setBinaryThumbnail(ctx *sdk.AppCtx, def platformDef, connID int64, platformPostID string, thumb mediaItem) error {
	if def.ThumbnailTool == "" {
		return errors.New("platform has no thumbnail tool")
	}
	if platformPostID == "" {
		return errors.New("platform post id required to set thumbnail")
	}
	if err := validateYouTubeThumbnail(thumb); err != nil {
		return err
	}
	binaryField := def.ThumbnailBinaryField
	if binaryField == "" {
		binaryField = "image"
	}
	idField := def.ThumbnailIDField
	if idField == "" {
		idField = "videoId"
	}
	envelope, err := fetchBinaryEnvelope(thumb.URL, thumb.Mime, youtubeThumbnailMaxBytes)
	if err != nil {
		return err
	}
	input := map[string]any{
		idField:     platformPostID,
		binaryField: envelope,
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ThumbnailTool, input)
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		return upstreamError(res)
	}
	return nil
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

// scheduleJob hands off to the jobs app through its brokered app_tool
// target. Returns the job id so the caller can
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
	var jr struct {
		Job struct {
			ID int64 `json:"id"`
		} `json:"job"`
	}
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_schedule", map[string]any{
		"name": fmt.Sprintf("social.publish_post.%d", postID),
		"schedule": map[string]any{
			"kind":   "once",
			"run_at": rfc3339,
		},
		"target": map[string]any{
			"kind":  "app_tool",
			"app":   "social",
			"tool":  "post_publish_scheduled",
			"input": map[string]any{"post_id": postID},
		},
		"idempotency_key": fmt.Sprintf("social.post.%d", postID),
		"max_retries":     3,
		"backoff_seconds": 60,
		"owner_app":       "social",
	}, &jr); err != nil {
		return 0, fmt.Errorf("jobs_schedule: %w", err)
	}
	jobID := jr.Job.ID
	ctx.Logger().Info("scheduleJob: created", "post_id", postID, "job_id", jobID, "run_at", rfc3339)
	return jobID, nil
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
	if err := ctx.PlatformAPI().CallAppResult("jobs", "jobs_cancel", map[string]any{
		"id": jobID,
	}, nil); err != nil {
		ctx.Logger().Warn("cancelJob failed", "job_id", jobID, "err", err)
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
	pid := projectScope(ctx, args)
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	statusFilter, _ := args["status"].(string)
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
	}
	q := `SELECT id, body, COALESCE(media_storage_ids,'[]'), COALESCE(external_media_urls,'[]'), COALESCE(schedule_at,''),
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
	type postRow struct {
		id       int64
		profID   int64
		body     string
		mediaIDs []int64
		extMedia []string
		schedAt  string
		status   string
		created  string
		pubAt    string
	}
	postRows := []postRow{}
	for rows.Next() {
		var (
			id, profID                                                       int64
			body, mediaJSON, extMediaJSON, schedAt, status, createdAt, pubAt string
		)
		if err := rows.Scan(&id, &body, &mediaJSON, &extMediaJSON, &schedAt, &status, &createdAt, &pubAt, &profID); err != nil {
			continue
		}
		var mediaIDs []int64
		_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
		var extMedia []string
		_ = json.Unmarshal([]byte(extMediaJSON), &extMedia)
		postRows = append(postRows, postRow{
			id:       id,
			profID:   profID,
			body:     body,
			mediaIDs: mediaIDs,
			extMedia: extMedia,
			schedAt:  schedAt,
			status:   status,
			created:  createdAt,
			pubAt:    pubAt,
		})
	}
	rows.Close()
	out := []map[string]any{}
	for _, p := range postRows {
		targets := a.loadTargets(ctx, p.id)
		out = append(out, map[string]any{
			"id":                  p.id,
			"body":                p.body,
			"media_storage_ids":   p.mediaIDs,
			"external_media_urls": p.extMedia,
			"profile_id":          p.profID,
			"schedule_at":         p.schedAt,
			"status":              p.status,
			"created_at":          p.created,
			"published_at":        p.pubAt,
			"targets":             targets,
		})
	}
	return map[string]any{"posts": out}, nil
}

func (a *App) loadTargets(ctx *sdk.AppCtx, postID int64) []map[string]any {
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.platform, a.display_name, COALESCE(a.avatar_url,''),
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
			tid, acctID                                                int64
			platform, name, avatar, status, ppid, purl, lastErr, pubAt string
			attempts                                                   int
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
	pid := projectScope(ctx, args)
	var postStatus, scheduleAt string
	var jobID int64
	err := ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(schedule_at,''), COALESCE(job_id,0)
		   FROM posts
		  WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&postStatus, &scheduleAt, &jobID)
	if err == sql.ErrNoRows {
		return mcpError("post not found"), nil
	}
	if err != nil {
		return nil, err
	}
	if postStatus == "failed" && scheduleAt != "" && jobID == 0 {
		return a.retryFailedSchedule(ctx, pid, postID, scheduleAt)
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='pending', last_error=NULL
		  WHERE post_id=? AND (
		        status='failed' OR
		        (status='publishing' AND last_attempt_at < datetime('now','-1 hour'))
		      )
		    AND EXISTS (SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`,
		postID, pid,
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

func (a *App) runScheduledPublisher(runCtx context.Context, ctx *sdk.AppCtx) error {
	pid := projectScope(ctx)
	if pid == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id FROM posts
		  WHERE project_id=? AND status='scheduled' AND job_id=0
		    AND schedule_at IS NOT NULL AND datetime(schedule_at) <= CURRENT_TIMESTAMP
		  ORDER BY schedule_at, id LIMIT 25`,
		pid,
	)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		res, err := ctx.AppDB().Exec(
			`UPDATE posts SET status='publishing' WHERE id=? AND project_id=? AND status='scheduled' AND job_id=0`,
			id, pid,
		)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			continue
		}
		a.publishPostTargets(ctx, id)
	}
	return nil
}

func (a *App) runInboxCollector(runCtx context.Context, ctx *sdk.AppCtx) error {
	if runCtx.Err() != nil || projectScope(ctx) == "" {
		return runCtx.Err()
	}
	result, err := a.toolInboxSync(ctx, map[string]any{})
	if err != nil {
		return err
	}
	ctx.Logger().Info("scheduled inbox sync complete", "result", result)
	return nil
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
	pid := projectScope(ctx, args)
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
	rfc, err := normaliseScheduleAt(scheduleAt)
	if err != nil {
		return mcpError("invalid schedule_at: " + err.Error()), nil
	}
	if ctx.IntegrationFor("jobs") == nil {
		_, err = ctx.AppDB().Exec(
			`UPDATE posts SET schedule_at=?, job_id=0 WHERE id=? AND project_id=?`,
			rfc, postID, pid,
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"post_id":         postID,
			"schedule_at":     rfc,
			"job_id":          int64(0),
			"worker_fallback": true,
		}, nil
	}
	// Cancel the old job FIRST. If the new schedule fails we end up
	// with a post in 'scheduled' but no job — caught by the rollback
	// below: the post status is flipped to 'failed' so the operator
	// notices.
	a.cancelJob(ctx, jobID)

	newJobID, err := a.scheduleJob(ctx, postID, rfc)
	if err != nil {
		ctx.Logger().Warn("reschedule via jobs failed; using worker fallback", "post", postID, "err", err)
		newJobID = 0
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE posts SET schedule_at=?, job_id=? WHERE id=?`,
		rfc, newJobID, postID,
	)
	ctx.Emit("post.rescheduled", map[string]any{
		"post_id": postID,
		"job_id":  newJobID,
		"run_at":  rfc,
	})
	out := map[string]any{
		"post_id":     postID,
		"schedule_at": rfc,
		"job_id":      newJobID,
	}
	if newJobID == 0 {
		out["worker_fallback"] = true
	}
	return out, nil
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

// metricsTarget is the resolved per-target context the dispatcher
// passes to each platform's fetcher. PageCreds is the JSON blob from
// social_accounts.page_credentials (Facebook page access_token, IG's
// linked-page token) — needed by FB/IG metrics calls because those
// endpoints reject user-level tokens.
type metricsTarget struct {
	TargetID, SocialAccountID, ConnID int64
	Platform, ExtPostID, ExtURL       string
	PageCreds                         string
}

// getPostMetrics dispatches to the per-platform fetcher for one
// target. Returns a complete outcome (never nil) so the caller can
// always include it in the response array.
func (a *App) getPostMetrics(ctx *sdk.AppCtx, target metricsTarget) targetMetricsOutcome {
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
	case "facebook":
		return a.getFacebookPostMetrics(ctx, out, target.ConnID, target.PageCreds)
	case "instagram":
		return a.getInstagramPostMetrics(ctx, out, target.ConnID, target.PageCreds)
	default:
		// LinkedIn / Reddit / Pinterest / Threads — analytics tools
		// either aren't in the catalog yet or have slug-style paths
		// that don't resolve. Surface as unsupported.
		out.Status = "unsupported"
		out.Reason = "no analytics tool wired for this platform yet"
		return out
	}
}

// extractPageToken pulls the page-level access_token out of a
// social_accounts.page_credentials JSON blob. Returns "" when the
// blob is missing, malformed, or doesn't carry an access_token —
// callers can decide whether that's fatal.
func extractPageToken(pageCreds string) string {
	if pageCreds == "" {
		return ""
	}
	var creds map[string]string
	if err := json.Unmarshal([]byte(pageCreds), &creds); err != nil {
		return ""
	}
	return creds["access_token"]
}

// getTwitterPostMetrics calls get_tweet_analytics for a single tweet
// and maps Twitter's public_metrics to our normalized shape. Twitter's
// response wraps under {data: {public_metrics: {...}}}; some shapes
// also nest under {data: [{...}]} when called for multiple tweets,
// which we don't use here.
func (a *App) getTwitterPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	if rich := a.getTwitterPostAnalytics(ctx, out, connID); rich.Status == "ok" && rich.Metrics != nil && hasAnyMetrics(rich.Metrics) {
		return rich
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_tweet_analytics", map[string]any{
		"tweet_id":     out.PlatformPostID,
		"tweet.fields": "public_metrics,non_public_metrics,organic_metrics",
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
		Raw:      sanitizeRawJSON(res.Data),
	}
	return out
}

func (a *App) getTwitterPostAnalytics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_post_analytics", map[string]any{
		"ids":              out.PlatformPostID,
		"granularity":      "total",
		"analytics.fields": "impressions,engagements,likes,replies,retweets,quote_tweets,bookmarks,media_views,url_clicks,user_profile_clicks",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	values := extractNamedNumbers(res.Data)
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    firstMetricValue(values, "impressions", "media_views", "views"),
		Likes:    firstMetricValue(values, "likes", "like_count"),
		Comments: firstMetricValue(values, "replies", "reply_count", "comments"),
		Shares: firstMetricValue(values, "retweets", "retweet_count") +
			firstMetricValue(values, "quote_tweets", "quote_count"),
		Raw: sanitizeRawJSON(res.Data),
	}
	return out
}

func hasAnyMetrics(m *normalizedMetrics) bool {
	return m != nil && (m.Views > 0 || m.Likes > 0 || m.Comments > 0 || m.Shares > 0)
}

func extractNamedNumbers(raw json.RawMessage) map[string]int64 {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil
	}
	out := map[string]int64{}
	walkNamedNumbers(v, out)
	return out
}

func walkNamedNumbers(v any, out map[string]int64) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch n := val.(type) {
			case float64:
				if n >= 0 {
					out[k] += int64(n)
				}
			case string:
				if parsed := parseInt64(n); parsed > 0 {
					out[k] += parsed
				}
			default:
				walkNamedNumbers(val, out)
			}
		}
	case []any:
		for _, item := range x {
			walkNamedNumbers(item, out)
		}
	}
}

func firstMetricValue(values map[string]int64, keys ...string) int64 {
	for _, key := range keys {
		if v := values[key]; v > 0 {
			return v
		}
	}
	return 0
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
		Raw:      sanitizeRawJSON(res.Data),
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
		Raw:      sanitizeRawJSON(res.Data),
	}
	return out
}

// getFacebookPostMetrics calls facebook_get_post with engagement-summary
// fields. Graph response carries each count under .summary.total_count
// (likes, comments, reactions) or just .count (shares). Reactions and
// likes are technically separate signals on Graph — we map likes →
// likes (the raw "like" reactions), and stash the broader reactions
// total in raw for callers that want it. Views are not exposed on
// organic FB posts via this endpoint; needs /insights for that.
func (a *App) getFacebookPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64, pageCreds string) targetMetricsOutcome {
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "facebook page access_token missing — reconnect the account"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "facebook_get_post", map[string]any{
		"postId":       out.PlatformPostID,
		"fields":       "likes.summary(true),comments.summary(true),shares,reactions.summary(true)",
		"access_token": token,
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
		Likes struct {
			Summary struct {
				TotalCount int64 `json:"total_count"`
			} `json:"summary"`
		} `json:"likes"`
		Comments struct {
			Summary struct {
				TotalCount int64 `json:"total_count"`
			} `json:"summary"`
		} `json:"comments"`
		Shares struct {
			Count int64 `json:"count"`
		} `json:"shares"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    0, // not exposed on this endpoint; would need /insights for impressions/reach
		Likes:    resp.Likes.Summary.TotalCount,
		Comments: resp.Comments.Summary.TotalCount,
		Shares:   resp.Shares.Count,
		Raw:      sanitizeRawJSON(res.Data),
	}
	return out
}

// getInstagramPostMetrics calls get_media_insights for an IG Business
// media id. Response shape: {data: [{name, period, values: [{value: N}]}]}
// — one entry per requested metric, value lives under values[0].value.
// Maps reach → views (closest match), plus likes / comments / saves /
// shares to their normalized slots; saves goes to raw because the
// normalized shape doesn't have a saves field.
func (a *App) getInstagramPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64, pageCreds string) targetMetricsOutcome {
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "instagram page access_token missing — reconnect the account"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_media_insights", map[string]any{
		"mediaId":      out.PlatformPostID,
		"metric":       "reach,likes,comments,saves,shares",
		"access_token": token,
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
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value int64 `json:"value"`
			} `json:"values"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	byName := map[string]int64{}
	for _, m := range resp.Data {
		if len(m.Values) > 0 {
			byName[m.Name] = m.Values[0].Value
		}
	}
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    byName["reach"],
		Likes:    byName["likes"],
		Comments: byName["comments"],
		Shares:   byName["shares"],
		Raw:      sanitizeRawJSON(res.Data), // includes saves under data[].name="saves"
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
	ProfileID       int64           `json:"profile_id,omitempty"`
	Platform        string          `json:"platform"`
	DisplayName     string          `json:"display_name"`
	Status          string          `json:"status"` // ok | unsupported | failed
	Reason          string          `json:"reason,omitempty"`
	Error           string          `json:"error,omitempty"`
	Followers       int64           `json:"followers,omitempty"`
	Following       int64           `json:"following,omitempty"`
	TotalLikes      int64           `json:"total_likes,omitempty"`
	TotalVideos     int64           `json:"total_videos,omitempty"`
	Posts           int64           `json:"posts,omitempty"`
	Reach           int64           `json:"reach,omitempty"`
	Impressions     int64           `json:"impressions,omitempty"`
	Engagements     int64           `json:"engagements,omitempty"`
	Views           int64           `json:"views,omitempty"`
	Insights        insightSeries   `json:"insights,omitempty"`
	HistorySource   string          `json:"history_source,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}

type insightPoint struct {
	Time  string `json:"time,omitempty"`
	Value int64  `json:"value"`
}

type insightSeries map[string][]insightPoint

func (a *App) getAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64, period string) accountMetricsResult {
	var platform, displayName, extID, pageCreds, providerSlug, providerAccountID string
	var connID, profileID int64
	err := ctx.AppDB().QueryRow(
		`SELECT platform, COALESCE(display_name,''), connection_id,
		        COALESCE(external_account_id,''), COALESCE(page_credentials,''), COALESCE(profile_id,0),
		        COALESCE(provider_slug,'native'), COALESCE(provider_account_id,'')
		 FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &displayName, &connID, &extID, &pageCreds, &profileID, &providerSlug, &providerAccountID)
	if err != nil {
		return accountMetricsResult{
			SocialAccountID: accountID,
			Status:          "failed",
			Error:           "account not found",
		}
	}
	out := accountMetricsResult{
		SocialAccountID: accountID,
		ProfileID:       profileID,
		Platform:        platform,
		DisplayName:     displayName,
	}
	if providerSlug == zernioProviderSlug {
		return a.getZernioAccountMetrics(ctx, out, connID, providerAccountID)
	}
	switch platform {
	case "twitter":
		return a.getTwitterAccountMetrics(ctx, out, connID)
	case "youtube":
		return a.getYoutubeChannelMetrics(ctx, out, connID)
	case "tiktok":
		return a.getTikTokAccountMetrics(ctx, out, connID)
	case "facebook":
		return a.getFacebookAccountMetrics(ctx, out, connID, extID, pageCreds, period)
	case "instagram":
		return a.getInstagramAccountMetrics(ctx, out, connID, extID, pageCreds, period)
	default:
		out.Status = "unsupported"
		out.Reason = "account-level metrics not wired for this platform yet"
		return out
	}
}

func (a *App) getTwitterAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64) accountMetricsResult {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_me", map[string]any{
		"user.fields": "id,name,username,profile_image_url,public_metrics,verified,created_at",
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
			ID            string `json:"id"`
			Name          string `json:"name"`
			Username      string `json:"username"`
			PublicMetrics struct {
				FollowersCount int64 `json:"followers_count"`
				FollowingCount int64 `json:"following_count"`
				TweetCount     int64 `json:"tweet_count"`
				ListedCount    int64 `json:"listed_count"`
			} `json:"public_metrics"`
		} `json:"data"`
	}
	_ = json.Unmarshal(res.Data, &resp)
	pm := resp.Data.PublicMetrics
	out.Status = "ok"
	if out.DisplayName == "" {
		if resp.Data.Username != "" {
			out.DisplayName = "@" + resp.Data.Username
		} else {
			out.DisplayName = resp.Data.Name
		}
	}
	out.Followers = pm.FollowersCount
	out.Following = pm.FollowingCount
	out.Posts = pm.TweetCount
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

func twitterAuthenticatedUser(ctx *sdk.AppCtx, connID int64) (id, username string, err error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_me", map[string]any{
		"user.fields": "id,name,username,profile_image_url,public_metrics,verified,created_at",
	})
	if err != nil {
		return "", "", err
	}
	if res == nil || !res.Success {
		return "", "", upstreamError(res)
	}
	var resp struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		return "", "", err
	}
	return resp.Data.ID, resp.Data.Username, nil
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
	out.Views = parseInt64(s.ViewCount)
	a.addYoutubeAnalytics(ctx, &out, connID)
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

func (a *App) addYoutubeAnalytics(ctx *sdk.AppCtx, out *accountMetricsResult, connID int64) {
	since, until := metricsDateWindow(90)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "query_analytics_report", map[string]any{
		"ids":        "channel==MINE",
		"startDate":  since,
		"endDate":    until,
		"metrics":    "views,estimatedMinutesWatched,averageViewDuration,averageViewPercentage,subscribersGained,subscribersLost,likes,comments,shares",
		"dimensions": "day",
		"sort":       "day",
	})
	if err != nil {
		out.Reason = "youtube analytics unavailable: " + err.Error()
		return
	}
	if res == nil || !res.Success {
		out.Reason = "youtube analytics unavailable: " + upstreamError(res).Error()
		return
	}
	if series := parseYoutubeAnalyticsSeries(res.Data); len(series) > 0 {
		out.Insights = series
	}
}

func parseYoutubeAnalyticsSeries(raw json.RawMessage) insightSeries {
	var resp struct {
		ColumnHeaders []struct {
			Name string `json:"name"`
		} `json:"columnHeaders"`
		Rows [][]any `json:"rows"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil || len(resp.ColumnHeaders) < 2 {
		return nil
	}
	out := insightSeries{}
	for _, row := range resp.Rows {
		if len(row) == 0 {
			continue
		}
		day := fmt.Sprintf("%v", row[0])
		for i := 1; i < len(row) && i < len(resp.ColumnHeaders); i++ {
			name := resp.ColumnHeaders[i].Name
			if name == "" {
				continue
			}
			out[name] = append(out[name], insightPoint{
				Time:  day,
				Value: insightValueToInt64(row[i]),
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (a *App) getFacebookAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, pageID, pageCreds, period string) accountMetricsResult {
	if pageID == "" {
		out.Status = "failed"
		out.Error = "facebook account has no page id stored"
		return out
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "facebook page access_token missing — reconnect the account"
		return out
	}
	if period == "" {
		period = "day"
	}
	since, until := metricsDateWindow(90)
	series := insightSeries{}
	var raw json.RawMessage
	var skipped []string
	for _, metric := range []string{"page_impressions", "page_impressions_unique", "page_post_engagements"} {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_page_insights", map[string]any{
			"pageId":       pageID,
			"metric":       metric,
			"period":       period,
			"since":        since,
			"until":        until,
			"access_token": token,
		})
		if err != nil {
			skipped = append(skipped, metric+": "+err.Error())
			continue
		}
		if res == nil || !res.Success {
			skipped = append(skipped, metric+": "+upstreamError(res).Error())
			continue
		}
		mergeInsightSeries(series, parseInsightSeries(res.Data))
		raw = res.Data
	}
	if len(series) == 0 {
		out.Status = "unsupported"
		out.Reason = "facebook page insights unavailable for this page/API version"
		if len(skipped) > 0 {
			out.Raw, _ = json.Marshal(map[string]any{"skipped": skipped})
		}
		return out
	}
	out.Status = "ok"
	if len(skipped) > 0 {
		out.Reason = "some Facebook metrics were unavailable for this page/API version"
	}
	out.Followers = a.getFacebookPageFollowers(ctx, connID, pageID)
	out.Impressions = latestInsight(series, "page_impressions")
	out.Reach = latestInsight(series, "page_impressions_unique")
	out.Engagements = latestInsight(series, "page_post_engagements")
	out.Insights = series
	out.Raw = sanitizeRawJSON(raw)
	return out
}

func (a *App) getInstagramAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, instagramAccountID, pageCreds, period string) accountMetricsResult {
	if instagramAccountID == "" {
		out.Status = "failed"
		out.Error = "instagram account id missing"
		return out
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "instagram page access_token missing — reconnect the account"
		return out
	}
	if period == "" {
		period = "day"
	}
	since, until := metricsDateWindow(30)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_account_insights", map[string]any{
		"instagramAccountId": instagramAccountID,
		"metric":             "reach",
		"period":             period,
		"metric_type":        "time_series",
		"since":              since,
		"until":              until,
		"access_token":       token,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	series := parseInsightSeries(res.Data)
	out.Status = "ok"
	followers, following, mediaCount := a.getInstagramAccountTotals(ctx, connID, instagramAccountID)
	out.Followers = followers
	out.Following = following
	out.TotalVideos = mediaCount
	out.Reach = latestInsight(series, "reach")
	out.Insights = series
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

func (a *App) getFacebookPageFollowers(ctx *sdk.AppCtx, connID int64, pageID string) int64 {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_pages", map[string]any{
		"fields": "id,name,fan_count,followers_count",
		"limit":  100,
	})
	if err != nil || res == nil || !res.Success {
		return 0
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(res.Data, &resp) != nil {
		return 0
	}
	for _, page := range resp.Data {
		if toString(page["id"]) != pageID {
			continue
		}
		if n := insightValueToInt64(page["followers_count"]); n > 0 {
			return n
		}
		return insightValueToInt64(page["fan_count"])
	}
	return 0
}

func (a *App) getInstagramAccountTotals(ctx *sdk.AppCtx, connID int64, instagramAccountID string) (int64, int64, int64) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_pages", map[string]any{
		"fields": "id,name,instagram_business_account{id,username,followers_count,follows_count,media_count}",
		"limit":  100,
	})
	if err != nil || res == nil || !res.Success {
		return 0, 0, 0
	}
	var resp struct {
		Data []map[string]any `json:"data"`
	}
	if json.Unmarshal(res.Data, &resp) != nil {
		return 0, 0, 0
	}
	for _, page := range resp.Data {
		ig, _ := page["instagram_business_account"].(map[string]any)
		if toString(ig["id"]) != instagramAccountID {
			continue
		}
		return insightValueToInt64(ig["followers_count"]),
			insightValueToInt64(ig["follows_count"]),
			insightValueToInt64(ig["media_count"])
	}
	return 0, 0, 0
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
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

func metricsDateWindow(days int) (string, string) {
	if days <= 0 {
		days = 90
	}
	until := time.Now().UTC()
	since := until.AddDate(0, 0, -days)
	return since.Format("2006-01-02"), until.Format("2006-01-02")
}

func parseInsightSeries(raw json.RawMessage) insightSeries {
	var resp struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value   any    `json:"value"`
				EndTime string `json:"end_time"`
			} `json:"values"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	out := insightSeries{}
	for _, item := range resp.Data {
		if item.Name == "" {
			continue
		}
		for _, v := range item.Values {
			out[item.Name] = append(out[item.Name], insightPoint{
				Time:  v.EndTime,
				Value: insightValueToInt64(v.Value),
			})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func insightValueToInt64(v any) int64 {
	switch x := v.(type) {
	case float64:
		if x < 0 {
			return 0
		}
		return int64(x)
	case string:
		return parseInt64(x)
	default:
		return 0
	}
}

func latestInsight(series insightSeries, name string) int64 {
	points := series[name]
	if len(points) == 0 {
		return 0
	}
	return points[len(points)-1].Value
}

func mergeInsightSeries(dst, src insightSeries) {
	for name, points := range src {
		if len(points) > 0 {
			dst[name] = append(dst[name], points...)
		}
	}
}

const analyticsSnapshotMinInterval = 4 * time.Hour

func (a *App) runAnalyticsCollector(ctx context.Context, app *sdk.AppCtx) error {
	pid := projectScope(app)
	if pid == "" {
		app.Logger().Info("analytics_collector: no project scope; skipping")
		return nil
	}
	rows, err := app.AppDB().Query(
		`SELECT id FROM social_accounts
		  WHERE project_id=? AND status='active'
		  ORDER BY id`,
		pid,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		due, err := accountAnalyticsDue(app, pid, id, analyticsSnapshotMinInterval)
		if err != nil {
			app.Logger().Warn("analytics_collector: due check failed", "project", pid, "account", id, "err", err)
			continue
		}
		if !due {
			continue
		}
		res := a.collectAndStoreAccountMetrics(app, pid, id, "day")
		if res.Status == "failed" {
			app.Logger().Warn("analytics_collector: account metrics failed",
				"project", pid, "account", id, "platform", res.Platform, "err", res.Error)
		}
	}
	return nil
}

func accountAnalyticsDue(ctx *sdk.AppCtx, pid string, accountID int64, minInterval time.Duration) (bool, error) {
	var latest string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(MAX(point_time),'')
		   FROM social_metric_points
		  WHERE project_id=? AND social_account_id=? AND scope='account' AND period='snapshot'`,
		pid, accountID,
	).Scan(&latest)
	if err != nil {
		return true, err
	}
	if strings.TrimSpace(latest) == "" {
		return true, nil
	}
	t, ok := parseMetricPointTime(latest)
	if !ok {
		return true, nil
	}
	return time.Since(t) >= minInterval, nil
}

func (a *App) collectAndStoreAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64, period string) accountMetricsResult {
	res := a.getAccountMetrics(ctx, pid, accountID, period)
	res.Raw = sanitizeRawJSON(res.Raw)
	if err := a.persistAccountMetrics(ctx, pid, res); err != nil {
		ctx.Logger().Warn("account_metrics: persist failed",
			"project", pid, "account", accountID, "platform", res.Platform, "err", err)
	}
	if history := loadAccountMetricHistory(ctx, pid, accountID, 730); len(history) > 0 {
		res.Insights = history
		res.HistorySource = "social_metric_points"
	}
	return res
}

func (a *App) storedAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64) accountMetricsResult {
	var platform, displayName string
	var profileID int64
	err := ctx.AppDB().QueryRow(
		`SELECT platform, COALESCE(display_name,''), COALESCE(profile_id,0)
		   FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &displayName, &profileID)
	if err != nil {
		return accountMetricsResult{
			SocialAccountID: accountID,
			Status:          "failed",
			Error:           "account not found",
		}
	}
	out := accountMetricsResult{
		SocialAccountID: accountID,
		ProfileID:       profileID,
		Platform:        platform,
		DisplayName:     displayName,
	}
	history := loadAccountMetricHistory(ctx, pid, accountID, 730)
	if len(history) == 0 {
		out.Status = "unsupported"
		out.Reason = "No stored analytics yet. Click Refresh metrics to collect fresh metrics."
		return out
	}
	out.Status = "ok"
	out.HistorySource = "social_metric_points"
	out.Insights = history
	applyLatestAccountHistory(&out, history)
	return out
}

func applyLatestAccountHistory(out *accountMetricsResult, history insightSeries) {
	latest := func(name string) int64 {
		points := history[name]
		if len(points) == 0 {
			return 0
		}
		return points[len(points)-1].Value
	}
	out.Followers = latest("followers")
	out.Following = latest("following")
	out.TotalLikes = latest("total_likes")
	out.TotalVideos = latest("total_videos")
	out.Posts = latest("posts")
	if v := latest("views_total"); v > 0 {
		out.Views = v
	} else {
		out.Views = latest("views")
	}
	if v := latest("reach_total"); v > 0 {
		out.Reach = v
	} else {
		out.Reach = latest("reach")
	}
	if v := latest("impressions_total"); v > 0 {
		out.Impressions = v
	} else {
		out.Impressions = latest("page_impressions")
	}
	if v := latest("engagements_total"); v > 0 {
		out.Engagements = v
	} else {
		out.Engagements = latest("page_post_engagements")
	}
}

func (a *App) persistAccountMetrics(ctx *sdk.AppCtx, pid string, res accountMetricsResult) error {
	if res.SocialAccountID <= 0 || pid == "" || res.Platform == "" || res.Status == "failed" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	profileID := res.ProfileID
	if profileID == 0 {
		_ = ctx.AppDB().QueryRow(
			`SELECT COALESCE(profile_id,0) FROM social_accounts WHERE id=? AND project_id=?`,
			res.SocialAccountID, pid,
		).Scan(&profileID)
	}
	source := accountMetricSource(res.Platform, "snapshot")
	totals := []struct {
		name  string
		value int64
	}{
		{"followers", res.Followers},
		{"following", res.Following},
		{"total_likes", res.TotalLikes},
		{"total_videos", res.TotalVideos},
		{"posts", res.Posts},
		{"reach", res.Reach},
		{"impressions", res.Impressions},
		{"engagements", res.Engagements},
		{"views", res.Views},
	}
	for _, item := range totals {
		if item.value <= 0 {
			continue
		}
		if err := insertSocialMetricPoint(ctx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, "account", item.name, "snapshot", now, item.value, source, "ok", ""); err != nil {
			return err
		}
	}
	seriesSource := accountMetricSource(res.Platform, "series")
	for metric, points := range res.Insights {
		for _, point := range points {
			pointTime := normaliseMetricPointTime(point.Time, now)
			if err := insertSocialMetricPoint(ctx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, "account", metric, "day", pointTime, point.Value, seriesSource, "ok", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func persistPostMetricOutcome(ctx *sdk.AppCtx, pid string, profileID, postID int64, outcome targetMetricsOutcome) error {
	if outcome.Status != "ok" || outcome.Metrics == nil || pid == "" || outcome.SocialAccountID <= 0 {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	points := []struct {
		name  string
		value int64
	}{
		{"views", outcome.Metrics.Views},
		{"likes", outcome.Metrics.Likes},
		{"comments", outcome.Metrics.Comments},
		{"shares", outcome.Metrics.Shares},
	}
	source := outcome.Platform + "_post_snapshot"
	for _, point := range points {
		if point.value <= 0 {
			continue
		}
		if err := insertSocialMetricPoint(ctx, pid, profileID, outcome.SocialAccountID, postID, outcome.TargetID, outcome.Platform, "post", point.name, "snapshot", now, point.value, source, "ok", ""); err != nil {
			return err
		}
	}
	return nil
}

func insertSocialMetricPoint(ctx *sdk.AppCtx, pid string, profileID, accountID, postID, targetID int64, platform, scope, metric, period, pointTime string, value int64, source, status, note string) error {
	if pid == "" || accountID <= 0 || platform == "" || scope == "" || metric == "" || pointTime == "" {
		return nil
	}
	if period == "" {
		period = "snapshot"
	}
	if source == "" {
		source = "social"
	}
	if status == "" {
		status = "ok"
	}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO social_metric_points (
		    project_id, profile_id, social_account_id, post_id, post_target_id,
		    platform, scope, metric, period, point_time, value, source, status, note
		  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		  ON CONFLICT(project_id, scope, social_account_id, post_target_id, metric, period, point_time, source)
		  DO UPDATE SET
		    value=excluded.value,
		    status=excluded.status,
		    note=excluded.note,
		    created_at=CURRENT_TIMESTAMP`,
		pid, profileID, accountID, postID, targetID, platform, scope, metric, period, pointTime, value, source, status, note,
	)
	return err
}

func loadAccountMetricHistory(ctx *sdk.AppCtx, pid string, accountID int64, days int) insightSeries {
	if pid == "" || accountID <= 0 {
		return nil
	}
	if days <= 0 {
		days = 730
	}
	since := time.Now().UTC().AddDate(0, 0, -days).Format(time.RFC3339)
	rows, err := ctx.AppDB().Query(
		`SELECT metric, period, point_time, value
		   FROM social_metric_points
		  WHERE project_id=? AND social_account_id=? AND scope='account'
		    AND status='ok' AND point_time >= ?
		  ORDER BY point_time ASC, id ASC`,
		pid, accountID, since,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := insightSeries{}
	for rows.Next() {
		var metric, period, pointTime string
		var value int64
		if err := rows.Scan(&metric, &period, &pointTime, &value); err != nil {
			continue
		}
		label := metric
		if period == "snapshot" && (metric == "views" || metric == "reach" || metric == "impressions" || metric == "engagements") {
			label = metric + "_total"
		}
		out[label] = append(out[label], insightPoint{Time: pointTime, Value: value})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func accountMetricSource(platform, kind string) string {
	switch platform {
	case "youtube":
		if kind == "series" {
			return "youtube_analytics"
		}
		return "youtube_snapshot"
	case "facebook":
		if kind == "series" {
			return "facebook_insights"
		}
		return "facebook_snapshot"
	case "instagram":
		if kind == "series" {
			return "instagram_insights"
		}
		return "instagram_snapshot"
	case "tiktok":
		return "tiktok_totals"
	default:
		return platform + "_" + kind
	}
}

func normaliseMetricPointTime(value, fallback string) string {
	if t, ok := parseMetricPointTime(value); ok {
		return t.UTC().Format(time.RFC3339)
	}
	if t, ok := parseMetricPointTime(fallback); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return time.Now().UTC().Format(time.RFC3339)
}

func parseMetricPointTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	layouts := []string{
		time.RFC3339,
		"2006-01-02",
		"2006-01-02T15:04:05-0700",
		"2006-01-02T15:04:05Z0700",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, value); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func sanitizeRawJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return raw
	}
	clean := scrubSensitiveValue(v)
	b, err := json.Marshal(clean)
	if err != nil {
		return nil
	}
	return b
}

func scrubSensitiveValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, val := range x {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "access_token") || strings.Contains(lk, "authorization") ||
				strings.Contains(lk, "refresh_token") || strings.Contains(lk, "client_secret") ||
				strings.Contains(lk, "password") || lk == "token" || strings.HasSuffix(lk, "_token") {
				out[k] = "[redacted]"
				continue
			}
			out[k] = scrubSensitiveValue(val)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, val := range x {
			out[i] = scrubSensitiveValue(val)
		}
		return out
	case string:
		return scrubSensitiveURL(x)
	default:
		return v
	}
}

func scrubSensitiveURL(s string) string {
	u, err := url.Parse(s)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return s
	}
	q := u.Query()
	changed := false
	for _, key := range []string{"access_token", "refresh_token", "token", "client_secret"} {
		if q.Has(key) {
			q.Set(key, "[redacted]")
			changed = true
		}
	}
	if !changed {
		return s
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// toolPostMetrics is the post_metrics MCP entrypoint. Walks the
// post's targets, dispatches each to its platform's fetcher, returns
// the per-target outcomes.
func (a *App) toolPostMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	pid := projectScope(ctx, args)
	// Existence + ownership check — also surfaces post status / body
	// in the response so the agent gets context without a second call.
	var body, status string
	var profileID int64
	err := ctx.AppDB().QueryRow(
		`SELECT body, status, COALESCE(profile_id,0) FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&body, &status, &profileID)
	if err != nil {
		return mcpError("post not found"), nil
	}
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, COALESCE(t.platform_post_id,''),
		        COALESCE(t.platform_url,''), a.platform, a.connection_id,
		        COALESCE(a.page_credentials,'')
		 FROM post_targets t
		 LEFT JOIN social_accounts a ON a.id=t.social_account_id
		 WHERE t.post_id=?`,
		postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var trs []metricsTarget
	for rows.Next() {
		var r metricsTarget
		var connID sql.NullInt64
		var platform sql.NullString
		if err := rows.Scan(&r.TargetID, &r.SocialAccountID, &r.ExtPostID, &r.ExtURL, &platform, &connID, &r.PageCreds); err == nil {
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
		outcome := a.getPostMetrics(ctx, r)
		if err := persistPostMetricOutcome(ctx, pid, profileID, postID, outcome); err != nil {
			ctx.Logger().Warn("post_metrics: persist failed",
				"project", pid, "post", postID, "target", outcome.TargetID, "err", err)
		}
		outcomes = append(outcomes, outcome)
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
	res := a.collectAndStoreAccountMetrics(ctx, projectScope(ctx, args), id, period)
	return res, nil
}

// ─── import ───────────────────────────────────────────────────────
//
// Internal-only — NOT exposed as an MCP tool. Agents shouldn't be
// triggering bulk reads of upstream content; this is a panel-only
// affordance the user clicks deliberately.
//
// importAccountPosts pulls recent posts from one connected account
// into our local posts/post_targets tables so they show up in the
// Posts tab and the Metrics tab can query them like any locally-
// authored post.
//
// Dedup is handled by a unique partial index on
// post_targets(social_account_id, platform_post_id) — INSERT OR IGNORE
// silently skips already-imported posts. Re-running import is safe.

type importResult struct {
	AccountID       int64  `json:"account_id"`
	Platform        string `json:"platform"`
	Imported        int    `json:"imported"`
	SkippedExisting int    `json:"skipped_existing"`
	Status          string `json:"status"` // ok | unsupported | failed
	Reason          string `json:"reason,omitempty"`
	Error           string `json:"error,omitempty"`
}

type importRunAccount struct {
	AccountID       int64  `json:"account_id"`
	DisplayName     string `json:"display_name"`
	Platform        string `json:"platform"`
	Provider        string `json:"provider,omitempty"`
	Status          string `json:"status"` // ready | ok | unsupported | failed
	Imported        int    `json:"imported,omitempty"`
	SkippedExisting int    `json:"skipped_existing,omitempty"`
	Reason          string `json:"reason,omitempty"`
	Error           string `json:"error,omitempty"`
}

type importRunResponse struct {
	Status          string             `json:"status"`
	DryRun          bool               `json:"dry_run"`
	LimitPerAccount int                `json:"limit_per_account"`
	Accounts        []importRunAccount `json:"accounts"`
	Imported        int                `json:"imported"`
	SkippedExisting int                `json:"skipped_existing"`
	Unsupported     int                `json:"unsupported"`
	Failed          int                `json:"failed"`
}

type importCandidate struct {
	ID          int64
	ProfileID   int64
	Platform    string
	DisplayName string
	Provider    string
}

func importSupportedPlatform(platform string) bool {
	switch platform {
	case "facebook", "instagram", "tiktok", "twitter", "youtube":
		return true
	default:
		return false
	}
}

func (a *App) importCandidates(ctx *sdk.AppCtx, pid string, args map[string]any) ([]importCandidate, error) {
	platformSet := stringSet(stringSliceArg(args, "platforms"))
	accountSet := int64Set(int64SliceArg(args, "social_account_ids", "account_ids"))
	profileID, hasProfile := optionalInt64Arg(args, "profile_id")
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(profile_id,0), platform, display_name, COALESCE(provider_slug,'native')
		   FROM social_accounts
		  WHERE project_id=? AND status='active'
		  ORDER BY profile_id, platform, display_name`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []importCandidate{}
	for rows.Next() {
		var c importCandidate
		if err := rows.Scan(&c.ID, &c.ProfileID, &c.Platform, &c.DisplayName, &c.Provider); err != nil {
			return nil, err
		}
		if hasProfile && c.ProfileID != profileID {
			continue
		}
		if len(platformSet) > 0 && !platformSet[c.Platform] {
			continue
		}
		if len(accountSet) > 0 && !accountSet[c.ID] {
			continue
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) runImport(ctx *sdk.AppCtx, pid string, args map[string]any) (importRunResponse, error) {
	limit := intArg(args, "limit_per_account", intArg(args, "limit", 100))
	if limit <= 0 {
		limit = 100
	}
	if limit > 200 {
		limit = 200
	}
	dryRun := boolArg(args, "dry_run", false)
	candidates, err := a.importCandidates(ctx, pid, args)
	if err != nil {
		return importRunResponse{}, err
	}
	resp := importRunResponse{
		Status:          "ok",
		DryRun:          dryRun,
		LimitPerAccount: limit,
		Accounts:        make([]importRunAccount, 0, len(candidates)),
	}
	for _, c := range candidates {
		row := importRunAccount{
			AccountID:   c.ID,
			DisplayName: c.DisplayName,
			Platform:    c.Platform,
			Provider:    c.Provider,
		}
		if c.Provider != zernioProviderSlug && !importSupportedPlatform(c.Platform) {
			row.Status = "unsupported"
			row.Reason = "import for this platform isn't wired yet"
			resp.Unsupported++
			resp.Accounts = append(resp.Accounts, row)
			continue
		}
		if dryRun {
			row.Status = "ready"
			resp.Accounts = append(resp.Accounts, row)
			continue
		}
		result := a.importAccountPosts(ctx, pid, c.ID, limit)
		row.Status = result.Status
		row.Imported = result.Imported
		row.SkippedExisting = result.SkippedExisting
		row.Reason = result.Reason
		row.Error = result.Error
		resp.Imported += result.Imported
		resp.SkippedExisting += result.SkippedExisting
		if result.Status == "unsupported" {
			resp.Unsupported++
		}
		if result.Status == "failed" {
			resp.Failed++
		}
		resp.Accounts = append(resp.Accounts, row)
	}
	return resp, nil
}

func (a *App) handleImportsRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{}
	if r.Body != nil {
		defer r.Body.Close()
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		for k, v := range body {
			args[k] = v
		}
	}
	copyProjectArgs(args, projectArgsFromRequest(r))
	pid := projectScope(globalCtx, args)
	out, err := a.runImport(globalCtx, pid, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) importAccountPosts(ctx *sdk.AppCtx, pid string, accountID int64, limit int) importResult {
	if limit <= 0 || limit > 200 {
		limit = 25
	}
	var platform, extID, pageCreds, providerSlug, providerAccountID string
	var connID int64
	var profileID int64
	err := ctx.AppDB().QueryRow(
		`SELECT platform, COALESCE(external_account_id,''), connection_id,
		        COALESCE(page_credentials,''), COALESCE(profile_id,0),
		        COALESCE(provider_slug,'native'), COALESCE(provider_account_id,'')
		 FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &extID, &connID, &pageCreds, &profileID, &providerSlug, &providerAccountID)
	if err != nil {
		return importResult{AccountID: accountID, Status: "failed", Error: "account not found"}
	}
	out := importResult{AccountID: accountID, Platform: platform}
	if providerSlug == zernioProviderSlug {
		return a.importZernioPosts(ctx, pid, out, accountID, connID, providerAccountID, profileID, limit)
	}
	switch platform {
	case "facebook":
		return a.importFacebookPosts(ctx, pid, out, accountID, connID, extID, pageCreds, profileID, limit)
	case "instagram":
		return a.importInstagramPosts(ctx, pid, out, accountID, connID, extID, pageCreds, profileID, limit)
	case "tiktok":
		return a.importTikTokPosts(ctx, pid, out, accountID, connID, profileID, limit)
	case "twitter":
		return a.importTwitterPosts(ctx, pid, out, accountID, connID, profileID, limit)
	case "youtube":
		return a.importYoutubePosts(ctx, pid, out, accountID, connID, profileID, limit)
	default:
		out.Status = "unsupported"
		out.Reason = "import for this platform isn't wired yet"
		return out
	}
}

func (a *App) importFacebookPosts(
	ctx *sdk.AppCtx, pid string, out importResult,
	accountID, connID int64, pageID, pageCreds string,
	profileID int64, limit int,
) importResult {
	if pageID == "" {
		out.Status = "failed"
		out.Error = "facebook account has no page id stored"
		return out
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "facebook page access_token missing — reconnect the account"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_page_posts", map[string]any{
		"pageId":       pageID,
		"limit":        limit,
		"access_token": token,
		"fields":       "id,message,created_time,full_picture,permalink_url",
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
		Data []struct {
			ID           string `json:"id"`
			Message      string `json:"message"`
			CreatedTime  string `json:"created_time"`
			FullPicture  string `json:"full_picture"`
			PermalinkURL string `json:"permalink_url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		out.Status, out.Error = "failed", "decode page posts: "+err.Error()
		return out
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	for _, p := range resp.Data {
		if p.ID == "" {
			continue
		}
		// Pre-check dedupe so we know whether we created a new local
		// post or skipped an already-imported one. Without this we'd
		// have to reverse-engineer the result from rows-affected on
		// the unique-constraint INSERT — not portable across drivers.
		var existing int64
		_ = ctx.AppDB().QueryRow(
			`SELECT id FROM post_targets WHERE social_account_id=? AND platform_post_id=?`,
			accountID, p.ID,
		).Scan(&existing)
		if existing > 0 {
			out.SkippedExisting++
			continue
		}
		// Encode the picture as a single-element external media array
		// (or NULL when no image). Read-only references — we never
		// download these into our storage app.
		var extMediaJSON sql.NullString
		if p.FullPicture != "" {
			b, _ := json.Marshal([]string{p.FullPicture})
			extMediaJSON = sql.NullString{String: string(b), Valid: true}
		}
		// Local post row — imported, body comes from the FB message.
		postRes, err := ctx.AppDB().Exec(
			`INSERT INTO posts (project_id, body, media_storage_ids, status, profile_id,
			                    imported_at, external_media_urls, published_at)
			 VALUES (?, ?, '[]', 'published', ?, CURRENT_TIMESTAMP, ?, ?)`,
			pid, p.Message, profileID, extMediaJSON, nullable(p.CreatedTime),
		)
		if err != nil {
			ctx.Logger().Warn("import: insert post failed", "fb_id", p.ID, "err", err)
			continue
		}
		postID, _ := postRes.LastInsertId()
		// Target row — already-published, with the upstream id + URL.
		_, err = ctx.AppDB().Exec(
			`INSERT INTO post_targets (post_id, social_account_id, status,
			                           platform_post_id, platform_url, published_at)
			 VALUES (?, ?, 'published', ?, ?, ?)`,
			postID, accountID, p.ID, nullable(p.PermalinkURL), nullable(p.CreatedTime),
		)
		if err != nil {
			ctx.Logger().Warn("import: insert target failed", "fb_id", p.ID, "err", err)
			// Roll back the orphan post row so re-running import doesn't
			// leave dangling locally-only entries.
			_, _ = ctx.AppDB().Exec(`DELETE FROM posts WHERE id=?`, postID)
			continue
		}
		out.Imported++
	}
	out.Status = "ok"
	return out
}

func (a *App) importInstagramPosts(
	ctx *sdk.AppCtx, pid string, out importResult,
	accountID, connID int64, instagramAccountID, pageCreds string,
	profileID int64, limit int,
) importResult {
	if instagramAccountID == "" {
		out.Status = "failed"
		out.Error = "instagram account id missing"
		return out
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "instagram page access_token missing — reconnect the account"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_account_media", map[string]any{
		"instagramAccountId": instagramAccountID,
		"limit":              limit,
		"fields":             "id,media_type,media_url,thumbnail_url,permalink,caption,timestamp,like_count,comments_count",
		"access_token":       token,
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
		Data []struct {
			ID           string `json:"id"`
			Caption      string `json:"caption"`
			MediaURL     string `json:"media_url"`
			ThumbnailURL string `json:"thumbnail_url"`
			Permalink    string `json:"permalink"`
			Timestamp    string `json:"timestamp"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		out.Status, out.Error = "failed", "decode instagram media: "+err.Error()
		return out
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	for _, p := range resp.Data {
		if p.ID == "" {
			continue
		}
		mediaURL := p.MediaURL
		if mediaURL == "" {
			mediaURL = p.ThumbnailURL
		}
		imported, err := a.insertImportedPost(ctx, pid, accountID, profileID, p.Caption, p.ID, p.Permalink, p.Timestamp, []string{mediaURL})
		if err != nil {
			ctx.Logger().Warn("import: insert instagram post failed", "media_id", p.ID, "err", err)
			continue
		}
		if imported {
			out.Imported++
		} else {
			out.SkippedExisting++
		}
	}
	out.Status = "ok"
	return out
}

func (a *App) importTikTokPosts(
	ctx *sdk.AppCtx, pid string, out importResult,
	accountID, connID int64,
	profileID int64, limit int,
) importResult {
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	var cursor int64
	seen := 0
	for seen < limit {
		maxCount := limit - seen
		if maxCount > 20 {
			maxCount = 20
		}
		args := map[string]any{
			"max_count": maxCount,
			"fields":    "id,title,video_description,create_time,cover_image_url,share_url,like_count,comment_count,share_count,view_count",
		}
		if cursor > 0 {
			args["cursor"] = cursor
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_videos", args)
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
					ID               string `json:"id"`
					Title            string `json:"title"`
					VideoDescription string `json:"video_description"`
					CoverImageURL    string `json:"cover_image_url"`
					ShareURL         string `json:"share_url"`
					CreateTime       int64  `json:"create_time"`
				} `json:"videos"`
				Cursor  int64 `json:"cursor"`
				HasMore bool  `json:"has_more"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			out.Status, out.Error = "failed", "decode tiktok videos: "+err.Error()
			return out
		}
		if len(resp.Data.Videos) == 0 {
			break
		}
		for _, v := range resp.Data.Videos {
			if v.ID == "" {
				continue
			}
			seen++
			body := v.Title
			if body == "" {
				body = v.VideoDescription
			}
			publishedAt := ""
			if v.CreateTime > 0 {
				publishedAt = time.Unix(v.CreateTime, 0).UTC().Format(time.RFC3339)
			}
			imported, err := a.insertImportedPost(ctx, pid, accountID, profileID, body, v.ID, v.ShareURL, publishedAt, []string{v.CoverImageURL})
			if err != nil {
				ctx.Logger().Warn("import: insert tiktok post failed", "video_id", v.ID, "err", err)
				continue
			}
			if imported {
				out.Imported++
			} else {
				out.SkippedExisting++
			}
		}
		if !resp.Data.HasMore || resp.Data.Cursor == 0 || resp.Data.Cursor == cursor {
			break
		}
		cursor = resp.Data.Cursor
	}
	out.Status = "ok"
	return out
}

func (a *App) importTwitterPosts(
	ctx *sdk.AppCtx, pid string, out importResult,
	accountID, connID int64,
	profileID int64, limit int,
) importResult {
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	userID, _, err := twitterAuthenticatedUser(ctx, connID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if userID == "" {
		out.Status = "failed"
		out.Error = "x authenticated user id missing"
		return out
	}
	seen := 0
	pageToken := ""
	for seen < limit {
		maxResults := limit - seen
		if maxResults > 100 {
			maxResults = 100
		}
		if maxResults < 5 {
			maxResults = 5
		}
		args := map[string]any{
			"user_id":      userID,
			"max_results":  maxResults,
			"tweet.fields": "id,text,created_at,public_metrics,entities,referenced_tweets",
			"exclude":      "retweets",
		}
		if pageToken != "" {
			args["pagination_token"] = pageToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_user_tweets", args)
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
		if res == nil || !res.Success {
			out.Status, out.Error = "failed", upstreamError(res).Error()
			return out
		}
		var resp struct {
			Data []struct {
				ID        string `json:"id"`
				Text      string `json:"text"`
				CreatedAt string `json:"created_at"`
				Entities  struct {
					URLs []struct {
						ExpandedURL string `json:"expanded_url"`
						URL         string `json:"url"`
					} `json:"urls"`
				} `json:"entities"`
			} `json:"data"`
			Meta struct {
				NextToken string `json:"next_token"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(res.Data, &resp); err != nil {
			out.Status, out.Error = "failed", "decode x posts: "+err.Error()
			return out
		}
		if len(resp.Data) == 0 {
			break
		}
		for _, p := range resp.Data {
			if p.ID == "" {
				continue
			}
			seen++
			imported, err := a.insertImportedPost(ctx, pid, accountID, profileID, p.Text, p.ID, "https://twitter.com/i/web/status/"+p.ID, p.CreatedAt, twitterEntityMediaURLs(p.Entities.URLs))
			if err != nil {
				ctx.Logger().Warn("import: insert x post failed", "tweet_id", p.ID, "err", err)
				continue
			}
			if imported {
				out.Imported++
			} else {
				out.SkippedExisting++
			}
		}
		if resp.Meta.NextToken == "" || resp.Meta.NextToken == pageToken {
			break
		}
		pageToken = resp.Meta.NextToken
	}
	out.Status = "ok"
	return out
}

func twitterEntityMediaURLs(urls []struct {
	ExpandedURL string `json:"expanded_url"`
	URL         string `json:"url"`
}) []string {
	out := []string{}
	for _, u := range urls {
		if u.ExpandedURL != "" {
			out = append(out, u.ExpandedURL)
		} else if u.URL != "" {
			out = append(out, u.URL)
		}
	}
	return out
}

func (a *App) importYoutubePosts(
	ctx *sdk.AppCtx, pid string, out importResult,
	accountID, connID int64,
	profileID int64, limit int,
) importResult {
	chRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_my_channel", map[string]any{
		"part": "contentDetails",
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if chRes == nil || !chRes.Success {
		out.Status, out.Error = "failed", upstreamError(chRes).Error()
		return out
	}
	var ch struct {
		Items []struct {
			ContentDetails struct {
				RelatedPlaylists struct {
					Uploads string `json:"uploads"`
				} `json:"relatedPlaylists"`
			} `json:"contentDetails"`
		} `json:"items"`
	}
	_ = json.Unmarshal(chRes.Data, &ch)
	if len(ch.Items) == 0 || ch.Items[0].ContentDetails.RelatedPlaylists.Uploads == "" {
		out.Status = "failed"
		out.Error = "youtube uploads playlist not found"
		return out
	}
	if profileID == 0 {
		profileID = projectDefaultProfileID(ctx, pid)
	}
	pageToken := ""
	seen := 0
	for seen < limit {
		maxResults := limit - seen
		if maxResults > 50 {
			maxResults = 50
		}
		args := map[string]any{
			"playlistId": ch.Items[0].ContentDetails.RelatedPlaylists.Uploads,
			"maxResults": maxResults,
			"part":       "snippet,contentDetails,status",
		}
		if pageToken != "" {
			args["pageToken"] = pageToken
		}
		plRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_playlist_items", args)
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
		if plRes == nil || !plRes.Success {
			out.Status, out.Error = "failed", upstreamError(plRes).Error()
			return out
		}
		var pl struct {
			NextPageToken string `json:"nextPageToken"`
			Items         []struct {
				Snippet struct {
					Title       string `json:"title"`
					Description string `json:"description"`
					PublishedAt string `json:"publishedAt"`
					ResourceID  struct {
						VideoID string `json:"videoId"`
					} `json:"resourceId"`
					Thumbnails map[string]struct {
						URL string `json:"url"`
					} `json:"thumbnails"`
				} `json:"snippet"`
				ContentDetails struct {
					VideoID          string `json:"videoId"`
					VideoPublishedAt string `json:"videoPublishedAt"`
				} `json:"contentDetails"`
			} `json:"items"`
		}
		if err := json.Unmarshal(plRes.Data, &pl); err != nil {
			out.Status, out.Error = "failed", "decode youtube playlist: "+err.Error()
			return out
		}
		if len(pl.Items) == 0 {
			break
		}
		for _, item := range pl.Items {
			videoID := item.ContentDetails.VideoID
			if videoID == "" {
				videoID = item.Snippet.ResourceID.VideoID
			}
			if videoID == "" {
				continue
			}
			seen++
			publishedAt := item.ContentDetails.VideoPublishedAt
			if publishedAt == "" {
				publishedAt = item.Snippet.PublishedAt
			}
			thumb := bestYoutubeThumb(item.Snippet.Thumbnails)
			imported, err := a.insertImportedPost(ctx, pid, accountID, profileID, item.Snippet.Title, videoID, "https://www.youtube.com/watch?v="+videoID, publishedAt, []string{thumb})
			if err != nil {
				ctx.Logger().Warn("import: insert youtube post failed", "video_id", videoID, "err", err)
				continue
			}
			if imported {
				out.Imported++
			} else {
				out.SkippedExisting++
			}
		}
		if pl.NextPageToken == "" || pl.NextPageToken == pageToken {
			break
		}
		pageToken = pl.NextPageToken
	}
	out.Status = "ok"
	return out
}

func (a *App) insertImportedPost(
	ctx *sdk.AppCtx,
	pid string,
	accountID, profileID int64,
	body, platformPostID, platformURL, publishedAt string,
	mediaURLs []string,
) (bool, error) {
	var existing int64
	_ = ctx.AppDB().QueryRow(
		`SELECT id FROM post_targets WHERE social_account_id=? AND platform_post_id=?`,
		accountID, platformPostID,
	).Scan(&existing)
	if existing > 0 {
		return false, nil
	}
	filteredMedia := make([]string, 0, len(mediaURLs))
	for _, u := range mediaURLs {
		if u != "" {
			filteredMedia = append(filteredMedia, u)
		}
	}
	var extMediaJSON sql.NullString
	if len(filteredMedia) > 0 {
		b, _ := json.Marshal(filteredMedia)
		extMediaJSON = sql.NullString{String: string(b), Valid: true}
	}
	postRes, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, media_storage_ids, status, profile_id,
		                    imported_at, external_media_urls, published_at)
		 VALUES (?, ?, '[]', 'published', ?, CURRENT_TIMESTAMP, ?, ?)`,
		pid, body, profileID, extMediaJSON, nullable(publishedAt),
	)
	if err != nil {
		return false, err
	}
	postID, _ := postRes.LastInsertId()
	_, err = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status,
		                           platform_post_id, platform_url, published_at)
		 VALUES (?, ?, 'published', ?, ?, ?)`,
		postID, accountID, platformPostID, nullable(platformURL), nullable(publishedAt),
	)
	if err != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM posts WHERE id=?`, postID)
		return false, err
	}
	return true, nil
}

func bestYoutubeThumb(thumbnails map[string]struct {
	URL string `json:"url"`
}) string {
	for _, key := range []string{"maxres", "standard", "high", "medium", "default"} {
		if t, ok := thumbnails[key]; ok && t.URL != "" {
			return t.URL
		}
	}
	for _, t := range thumbnails {
		if t.URL != "" {
			return t.URL
		}
	}
	return ""
}

// ─── post_edit ────────────────────────────────────────────────────
//
// post_edit lets agents (and the panel) update an already-published
// post's text and per-target metadata. Same fan-out shape as
// post_delete + post_metrics — each target reports ok / unsupported /
// skipped / failed independently, with reason / error strings for
// non-ok outcomes.
//
// Editable platforms today: Facebook (message), X/Twitter (text via
// edit_options.previous_post_id when upstream permits it), YouTube
// (title / description / tags / privacy / category / thumbnail).
// Other platforms either don't permit programmatic edits (TikTok) or
// don't have the verb wired in our catalog (Instagram caption-only
// edits).
//
// Local body update: we always update posts.body when the call
// includes a top-level body. The local row is the source of truth for
// what the user *meant* the post to say; divergence with what's
// actually live on unsupported platforms is documented in the
// per-target outcomes.

type targetEditOutcome struct {
	TargetID        int64  `json:"target_id"`
	SocialAccountID int64  `json:"social_account_id"`
	Platform        string `json:"platform"`
	PlatformPostID  string `json:"platform_post_id,omitempty"`
	Status          string `json:"status"` // ok | unsupported | skipped | failed
	Reason          string `json:"reason,omitempty"`
	Error           string `json:"error,omitempty"`
}

func (a *App) toolPostEdit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	newBody, hasBody := args["body"].(string)
	rawTargets, _ := args["targets"].([]any)
	if !hasBody && len(rawTargets) == 0 {
		return mcpError("nothing to edit — pass body and/or targets"), nil
	}
	pid := projectScope(ctx, args)
	var currentBody, status string
	err := ctx.AppDB().QueryRow(
		`SELECT body, status FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&currentBody, &status)
	if err != nil {
		return mcpError("post not found"), nil
	}

	// Parse target-specific overrides into a map keyed by
	// social_account_id so the fan-out loop can look them up cheaply.
	overrides := map[int64]map[string]any{}
	for i, t := range rawTargets {
		m, ok := t.(map[string]any)
		if !ok {
			return mcpError(fmt.Sprintf("targets[%d] must be an object", i)), nil
		}
		id := toInt64Loose(m["social_account_id"])
		if id <= 0 {
			return mcpError(fmt.Sprintf("targets[%d].social_account_id required", i)), nil
		}
		if _, duplicate := overrides[id]; duplicate {
			return mcpError(fmt.Sprintf("targets[%d] duplicates social_account_id %d", i, id)), nil
		}
		opts := make(map[string]any, len(m))
		for k, v := range m {
			if k == "social_account_id" {
				continue
			}
			opts[k] = v
		}
		overrides[id] = opts
	}

	resolvedPostBody := currentBody

	// Fan out across the post's targets.
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, COALESCE(t.platform_post_id,''),
		        a.platform, a.connection_id, COALESCE(a.page_credentials,''),
		        COALESCE(t.options,''), COALESCE(p.media_project_id,''),
		        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,''),
		        COALESCE(t.provider_post_id,'')
		 FROM post_targets t
		 LEFT JOIN social_accounts a ON a.id=t.social_account_id
		 LEFT JOIN posts p ON p.id=t.post_id
		 WHERE t.post_id=?`,
		postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type editTarget struct {
		TargetID, SocialAccountID, ConnID int64
		Platform, ExtPostID, PageCreds    string
		ProviderSlug, ProviderAccountID   string
		ProviderPostID                    string
		MediaProjectID                    string
		ExistingOptions                   map[string]any
	}
	var trs []editTarget
	for rows.Next() {
		var r editTarget
		var optsRaw, mediaProjectID string
		var connID sql.NullInt64
		var platform sql.NullString
		if err := rows.Scan(&r.TargetID, &r.SocialAccountID, &r.ExtPostID, &platform, &connID, &r.PageCreds, &optsRaw, &mediaProjectID, &r.ProviderSlug, &r.ProviderAccountID, &r.ProviderPostID); err == nil {
			if platform.Valid {
				r.Platform = platform.String
			}
			if connID.Valid {
				r.ConnID = connID.Int64
			}
			if optsRaw != "" {
				_ = json.Unmarshal([]byte(optsRaw), &r.ExistingOptions)
			}
			r.MediaProjectID = strings.TrimSpace(mediaProjectID)
			trs = append(trs, r)
		}
	}
	postAccounts := map[int64]bool{}
	for _, r := range trs {
		postAccounts[r.SocialAccountID] = true
	}
	for accountID := range overrides {
		if !postAccounts[accountID] {
			return mcpError(fmt.Sprintf("social account %d is not a target of post %d", accountID, postID)), nil
		}
	}

	// Validate the requested target set before mutating the local post.
	// This keeps a typo in targets[] from partially changing posts.body.
	if hasBody {
		resolvedPostBody = newBody
		if _, err := ctx.AppDB().Exec(
			`UPDATE posts SET body=? WHERE id=? AND project_id=?`,
			resolvedPostBody, postID, pid,
		); err != nil {
			return nil, fmt.Errorf("update post body: %w", err)
		}
	}

	outcomes := make([]targetEditOutcome, 0, len(trs))
	for _, r := range trs {
		requested, targeted := overrides[r.SocialAccountID]
		if !hasBody && !targeted {
			continue
		}
		if requested == nil {
			requested = map[string]any{}
		}
		if hasBody {
			requested["body"] = resolvedPostBody
		}
		out := targetEditOutcome{
			TargetID:        r.TargetID,
			SocialAccountID: r.SocialAccountID,
			Platform:        r.Platform,
			PlatformPostID:  r.ExtPostID,
		}
		if r.Platform == "" || r.ConnID == 0 {
			out.Status = "skipped"
			out.Reason = "social account row gone — was the account disconnected?"
			outcomes = append(outcomes, out)
			continue
		}
		if r.ExtPostID == "" {
			out.Status = "skipped"
			out.Reason = "target was never published"
			outcomes = append(outcomes, out)
			continue
		}
		// Merge: existing per-target options ← post-level body fallback ←
		// caller's per-target overrides (most specific wins). The merged
		// "effective options" are what each platform's editor should send.
		eff := map[string]any{}
		for k, v := range r.ExistingOptions {
			eff[k] = v
		}
		// Body resolution: this target's override → existing target body
		// → resolvedPostBody (post-level after body update).
		if targeted {
			for k, v := range requested {
				eff[k] = v
			}
		}
		// If neither the per-target override nor the existing target
		// options had a body, supply the post-level body so platforms
		// that edit body get the latest text.
		if _, has := eff["body"].(string); !has {
			eff["body"] = resolvedPostBody
		}

		// Persist the merged options back so subsequent reads (metrics
		// tab, list views, future re-edits) reflect what was sent.
		if optsJSON, err := json.Marshal(eff); err == nil {
			_, _ = ctx.AppDB().Exec(
				`UPDATE post_targets SET options=? WHERE id=?`,
				string(optsJSON), r.TargetID,
			)
		}

		if r.ProviderSlug == zernioProviderSlug {
			out = a.editZernioPost(ctx, out, r.ConnID, r.ProviderPostID, eff)
			outcomes = append(outcomes, out)
			continue
		}
		switch r.Platform {
		case "facebook":
			out = a.editFacebookPost(ctx, out, r.ConnID, r.PageCreds, eff)
		case "twitter":
			out = a.editTwitterPost(ctx, out, r.ConnID, eff)
		case "youtube":
			out = a.editYoutubePost(ctx, out, r.ConnID, eff, requested, r.MediaProjectID)
		case "tiktok", "instagram":
			out.Status = "unsupported"
			out.Reason = platformEditReason(r.Platform)
		default:
			out.Status = "unsupported"
			out.Reason = "no edit verb wired for this platform yet"
		}
		outcomes = append(outcomes, out)
	}

	return map[string]any{
		"post_id":      postID,
		"updated_body": resolvedPostBody,
		"prior_status": status,
		"targets":      outcomes,
	}, nil
}

// platformEditReason explains *why* a given platform's edit returns
// unsupported — important context for agents and the UI so the user
// knows whether this is "fix our catalog" or "the platform itself
// doesn't allow it".
func platformEditReason(platform string) string {
	switch platform {
	case "twitter":
		return "X edit is exposed through post_tweet edit_options when the authenticated account/API plan permits it"
	case "tiktok":
		return "TikTok doesn't permit programmatic edits to published videos"
	case "instagram":
		return "Instagram caption edits aren't in the catalog yet (POST /{ig-media-id}?caption=… would work upstream)"
	default:
		return "no edit verb wired for this platform yet"
	}
}

func (a *App) editTwitterPost(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, eff map[string]any) targetEditOutcome {
	body, _ := eff["body"].(string)
	if strings.TrimSpace(body) == "" {
		out.Status = "skipped"
		out.Reason = "x edit needs non-empty text"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "post_tweet", map[string]any{
		"text": body,
		"edit_options": map[string]any{
			"previous_post_id": out.PlatformPostID,
		},
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	if id, url := extractPostIdentity("twitter", res.Data); id != "" && id != out.PlatformPostID {
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets SET platform_post_id=?, platform_url=? WHERE id=?`,
			id, nullable(url), out.TargetID,
		)
		out.PlatformPostID = id
	}
	out.Status = "ok"
	return out
}

func (a *App) editFacebookPost(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, pageCreds string, eff map[string]any) targetEditOutcome {
	body, _ := eff["body"].(string)
	if strings.TrimSpace(body) == "" {
		out.Status = "skipped"
		out.Reason = "facebook edit needs a body — none provided"
		return out
	}
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "facebook page access_token missing — reconnect the account"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "facebook_update_post", map[string]any{
		"postId":       out.PlatformPostID,
		"message":      body,
		"access_token": token,
	})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if res == nil || !res.Success {
		out.Status, out.Error = "failed", upstreamError(res).Error()
		return out
	}
	out.Status = "ok"
	return out
}

// editYoutubePost calls update_video. YouTube wants the *full* snippet
// you want it to keep — it replaces fields you don't include with
// defaults, so we pass through what we have. Body becomes description;
// title comes from eff.title (or first line of body if missing — since
// title is required by the upstream resource).
func (a *App) editYoutubePost(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, eff, requested map[string]any, mediaProjectID string) targetEditOutcome {
	def := platforms["youtube"]
	thumbnail, err := a.resolveThumbnailOption(ctx, eff, mediaProjectID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	metadataRequested := false
	for _, key := range []string{"title", "body", "category", "visibility", "tags"} {
		if _, ok := requested[key]; ok {
			metadataRequested = true
			break
		}
	}
	if !metadataRequested && thumbnail == nil {
		out.Status = "skipped"
		out.Reason = "youtube edit needs metadata or thumbnail_storage_id"
		return out
	}
	if metadataRequested {
		input, err := a.youtubeMergedUpdateInput(ctx, connID, out.PlatformPostID, eff, requested)
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_video", input)
		if err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
		if res == nil || !res.Success {
			out.Status, out.Error = "failed", upstreamError(res).Error()
			return out
		}
	}
	if thumbnail != nil {
		if err := a.setBinaryThumbnail(ctx, def, connID, out.PlatformPostID, *thumbnail); err != nil {
			out.Status, out.Error = "failed", err.Error()
			return out
		}
	}
	out.Status = "ok"
	return out
}

func (a *App) youtubeMergedUpdateInput(ctx *sdk.AppCtx, connID int64, videoID string, eff, requested map[string]any) (map[string]any, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_video", map[string]any{
		"id":   videoID,
		"part": "snippet,status",
	})
	if err != nil {
		return nil, fmt.Errorf("get current YouTube metadata: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("get current YouTube metadata: %w", upstreamError(res))
	}
	var current struct {
		Items []struct {
			Snippet map[string]any `json:"snippet"`
			Status  map[string]any `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(res.Data, &current); err != nil || len(current.Items) == 0 {
		if err == nil {
			err = errors.New("video not found")
		}
		return nil, fmt.Errorf("decode current YouTube metadata: %w", err)
	}
	snippet := map[string]any{}
	for _, key := range []string{"title", "description", "tags", "categoryId", "defaultLanguage", "defaultAudioLanguage"} {
		if v, ok := current.Items[0].Snippet[key]; ok {
			snippet[key] = v
		}
	}
	if _, ok := requested["title"]; ok {
		snippet["title"] = strOption(eff, "title")
	}
	if _, ok := requested["body"]; ok {
		snippet["description"] = toString(eff["body"])
	}
	if _, ok := requested["category"]; ok {
		snippet["categoryId"] = strOption(eff, "category")
	}
	if _, ok := requested["tags"]; ok {
		tags := []string{}
		for _, value := range anySliceOption(eff, "tags") {
			if tag, ok := value.(string); ok && strings.TrimSpace(tag) != "" {
				tags = append(tags, tag)
			}
		}
		snippet["tags"] = tags
	}
	if strings.TrimSpace(toString(snippet["title"])) == "" {
		return nil, errors.New("YouTube metadata update requires a non-empty title")
	}
	input := map[string]any{"id": videoID, "snippet": snippet}
	if _, ok := requested["visibility"]; ok {
		status := map[string]any{}
		for _, key := range []string{"privacyStatus", "publishAt", "license", "embeddable", "publicStatsViewable", "selfDeclaredMadeForKids"} {
			if v, exists := current.Items[0].Status[key]; exists {
				status[key] = v
			}
		}
		status["privacyStatus"] = strOption(eff, "visibility")
		input["status"] = status
	}
	return input, nil
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
	pid := projectScope(ctx, args)
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
//     TikTok), or the catalog doesn't expose a verb yet
//     (LinkedIn, Reddit, Threads). Local row will still be
//     removed; the upstream copy stays
//   - "skipped"     — target was never published (no platform_post_id) or
//     its social_account row is gone (account disconnected
//     after posting), so we have nothing to delete upstream
//   - "failed"      — integration call returned an error; user can verify
//     manually with platform_post_id
func (a *App) deletePostUpstream(ctx *sdk.AppCtx, postID int64) []targetDeleteOutcome {
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.status, COALESCE(t.platform_post_id,''),
		        a.platform, a.connection_id, COALESCE(a.page_credentials,''),
		        COALESCE(a.provider_slug,'native'), COALESCE(t.provider_post_id,'')
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
		targetID       int64
		status         string
		extPostID      string
		platform       sql.NullString
		connID         sql.NullInt64
		pageCreds      sql.NullString
		provider       string
		providerPostID string
	}
	var rs []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.targetID, &r.status, &r.extPostID, &r.platform, &r.connID, &r.pageCreds, &r.provider, &r.providerPostID); err == nil {
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
		if r.provider == zernioProviderSlug {
			providerPostID := r.providerPostID
			if providerPostID == "" {
				providerPostID = r.extPostID
			}
			out = a.deleteZernioPost(ctx, out, r.connID.Int64, providerPostID)
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

func scopedQueryArgs(r *http.Request, keys ...string) map[string]any {
	args := projectArgsFromRequest(r)
	q := r.URL.Query()
	for _, key := range keys {
		if v := strings.TrimSpace(q.Get(key)); v != "" {
			args[key] = v
		}
	}
	return args
}

func splitQueryList(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func splitQueryIDs(raw string) []int64 {
	parts := splitQueryList(raw)
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		if id := toInt64Loose(p); id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (a *App) handleAccountsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	out, err := a.toolAccountList(globalCtx, scopedQueryArgs(r, "profile_id", "profile", "platform", "status"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleInboxAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	args := projectArgsFromRequest(r)
	if ids := splitQueryIDs(q.Get("social_account_ids")); len(ids) > 0 {
		args["social_account_ids"] = ids
	}
	if kinds := splitQueryList(q.Get("kinds")); len(kinds) > 0 {
		args["kinds"] = kinds
	}
	if statuses := splitQueryList(q.Get("status")); len(statuses) > 0 {
		args["status"] = statuses
	}
	if since := strings.TrimSpace(q.Get("since")); since != "" {
		args["since"] = since
	}
	if limit := strings.TrimSpace(q.Get("limit")); limit != "" {
		args["limit"] = limit
	}
	out, err := a.toolInboxList(globalCtx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleInboxItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inbox/"), "/")
	if rest == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if rest == "sync" {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			SocialAccountIDs []int64 `json:"social_account_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		args := projectArgsFromRequest(r)
		if len(body.SocialAccountIDs) > 0 {
			args["social_account_ids"] = body.SocialAccountIDs
		}
		out, err := a.toolInboxSync(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}

	parts := strings.Split(rest, "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", http.StatusMethodNotAllowed)
			return
		}
		args := projectArgsFromRequest(r)
		args["id"] = id
		args["with_thread"] = r.URL.Query().Get("with_thread") != "false"
		out, err := a.toolInboxGet(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	args := projectArgsFromRequest(r)
	args["id"] = id
	switch parts[1] {
	case "read":
		out, err := a.toolInboxMarkRead(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "unread":
		out, err := a.toolInboxMarkUnread(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "archive":
		out, err := a.toolInboxArchive(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "reply", "private_reply":
		var body struct {
			Body string `json:"body"`
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		args["body"] = body.Body
		if parts[1] == "private_reply" {
			args["mode"] = inboxReplyModePrivate
		} else if strings.TrimSpace(body.Mode) != "" {
			args["mode"] = body.Mode
		}
		var out any
		if parts[1] == "private_reply" {
			out, err = a.toolInboxPrivateReply(globalCtx, args)
		} else {
			out, err = a.toolInboxReply(globalCtx, args)
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "hide":
		out, err := a.toolInboxHide(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "unhide":
		out, err := a.toolInboxUnhide(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case "delete":
		out, err := a.toolInboxDelete(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleAccountsStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Platform          string `json:"platform"`
		Provider          string `json:"provider"`
		ProviderProfileID string `json:"provider_profile_id"`
		ProfileID         int64  `json:"profile_id"`
		ReturnTo          string `json:"return_to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	args := projectArgsFromRequest(r)
	args["platform"] = body.Platform
	args["provider"] = body.Provider
	args["provider_profile_id"] = body.ProviderProfileID
	if body.ProfileID > 0 {
		args["profile_id"] = body.ProfileID
	}
	args["return_to"] = body.ReturnTo
	out, err := a.toolAccountAdd(globalCtx, args)
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
	ready := false
	row, rowErr := a.getPending(pendingID)
	if rowErr == nil && !row.expired && row.status == "pending_oauth" {
		requestProject := strings.TrimSpace(r.URL.Query().Get("project_id"))
		if requestProject == "" || requestProject == row.projectID {
			if row.providerSlug == zernioProviderSlug {
				if doneConnID, ok := a.completeZernioOAuth(globalCtx, r, row); ok {
					connID = doneConnID
					ready = true
				}
			} else if connID > 0 && status == "ok" && a.pendingConnectionAllowed(globalCtx, row, connID) {
				res, err := globalCtx.AppDB().Exec(
					`UPDATE pending_accounts SET connection_id=?, status='ready'
					  WHERE id=? AND project_id=? AND status='pending_oauth'`,
					connID, pendingID, row.projectID,
				)
				if err == nil {
					n, _ := res.RowsAffected()
					ready = n == 1
				}
			}
		}
	}
	if ready {
		globalCtx.Emit("account.oauth_ready", map[string]any{
			"pending_account_id": pendingID,
			"connection_id":      connID,
		})
	}
	// Render a minimal page that posts a message to the panel and
	// closes itself. Works whether the OAuth happened in a popup
	// (postMessage to opener) or a top-level redirect (just navigate
	// the user back to the panel).
	eventType := "social.oauth_error"
	heading := "Authorization failed"
	detail := "The connection did not match this request or the request expired. Close this window and try again."
	if ready {
		eventType = "social.oauth_ready"
		heading = "Authorization complete"
		detail = "You can close this window."
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;background:#111;color:#eee;display:grid;place-items:center;height:100vh;margin:0">
<div style="text-align:center"><div style="font-size:20px">%s</div>
<div style="opacity:.7;margin-top:8px">%s</div></div>
<script>
try { if (window.opener) { window.opener.postMessage({type:%q,pending_account_id:%d,connection_id:%d}, window.location.origin); window.close(); } } catch(e){}
setTimeout(function(){ window.location.href = "/" }, 1500);
</script></body></html>`, heading, detail, eventType, pendingID, connID)
}

func (a *App) pendingConnectionAllowed(ctx *sdk.AppCtx, row *pendingRow, connID int64) bool {
	if row == nil || connID <= 0 {
		return false
	}
	if row.providerSlug == zernioProviderSlug {
		return row.connectionID == connID
	}
	conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		ProjectID: row.projectID,
		AppSlug:   row.integrationSlug,
	})
	if err != nil {
		ctx.Logger().Warn("oauth callback connection lookup failed", "pending_id", row.id, "err", err)
		return false
	}
	for _, conn := range conns {
		if conn.ID == connID && conn.Status == "active" {
			return true
		}
	}
	ctx.Logger().Warn("oauth callback rejected unrelated connection", "pending_id", row.id, "connection_id", connID)
	return false
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
	args := projectArgsFromRequest(r)
	args["pending_account_id"] = body.PendingAccountID
	args["page_id"] = body.PageID
	args["name"] = body.Name
	out, err := a.toolAccountFinalize(globalCtx, args)
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
		args := projectArgsFromRequest(r)
		args["pending_account_id"] = id
		out, err := a.toolAccountListPendingPages(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "metrics" && r.Method == http.MethodGet {
		args := scopedQueryArgs(r, "period", "refresh")
		refresh := strings.TrimSpace(strings.ToLower(toString(args["refresh"])))
		if refresh == "0" || refresh == "false" || refresh == "stored" {
			out := a.storedAccountMetrics(globalCtx, projectScope(globalCtx, args), id)
			writeJSON(w, out)
			return
		}
		args["social_account_id"] = id
		out, err := a.toolAccountMetrics(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "import" && r.Method == http.MethodPost {
		// Internal-only — panel route. Not registered as an MCP tool.
		// Optional ?limit=N (default 25, max 200).
		limit := 25
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
				limit = n
			}
		}
		out := a.importAccountPosts(globalCtx, projectScope(globalCtx, projectArgsFromRequest(r)), id, limit)
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "check" && r.Method == http.MethodPost {
		args := projectArgsFromRequest(r)
		args["social_account_id"] = id
		out, err := a.toolAccountCheck(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if r.Method == http.MethodDelete {
		args := scopedQueryArgs(r, "hard_delete", "delete_posts")
		args["id"] = id
		out, err := a.toolAccountDisconnect(globalCtx, args)
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
		out, err := a.toolPostList(globalCtx, scopedQueryArgs(r, "profile_id", "profile", "status", "limit"))
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
		copyProjectArgs(raw, projectArgsFromRequest(r))
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
			args := projectArgsFromRequest(r)
			args["post_id"] = id
			out, err := a.toolPostDelete(globalCtx, args)
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
		args := projectArgsFromRequest(r)
		args["post_id"] = id
		out, err := a.toolPostRetry(globalCtx, args)
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
		args := projectArgsFromRequest(r)
		args["post_id"] = id
		args["schedule_at"] = body.ScheduleAt
		out, err := a.toolPostReschedule(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "metrics" && r.Method == http.MethodGet {
		args := projectArgsFromRequest(r)
		args["post_id"] = id
		out, err := a.toolPostMetrics(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "edit" && r.Method == http.MethodPost {
		// Decode into map[string]any (same pattern as POST /posts) so
		// targets[]/body/etc. flow through without a strict struct
		// dropping unknown fields.
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		raw["post_id"] = id
		copyProjectArgs(raw, projectArgsFromRequest(r))
		out, err := a.toolPostEdit(globalCtx, raw)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	http.Error(w, "not found", http.StatusNotFound)
}

func (a *App) toolPostPublishScheduled(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return map[string]any{"status": "failed", "error": "post_id required"}, nil
	}
	pid := projectScope(ctx, args)
	if pid == "" {
		return map[string]any{"status": "failed", "error": "project required"}, nil
	}
	return a.publishScheduledPost(ctx, pid, postID), nil
}

// publishScheduledPost is shared by the brokered app_tool callback and the
// legacy HTTP route. It is idempotent: published targets are never claimed
// again, and a post is acknowledged only when every target was published.
func (a *App) publishScheduledPost(ctx *sdk.AppCtx, pid string, postID int64) map[string]any {
	var postStatus string
	if err := ctx.AppDB().QueryRow(
		`SELECT status FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&postStatus); err != nil {
		if err == sql.ErrNoRows {
			return map[string]any{"status": "failed", "error": "post not found"}
		}
		return map[string]any{"status": "error", "error": "load post: " + err.Error()}
	}
	if postStatus == "published" {
		return map[string]any{"status": "published", "post_id": postID, "idempotent": true}
	}
	if postStatus != "scheduled" && postStatus != "publishing" && postStatus != "failed" {
		return map[string]any{"status": "failed", "error": "post is not publishable from its current status: " + postStatus}
	}

	_, _ = ctx.AppDB().Exec(
		`UPDATE posts SET status='publishing' WHERE id=? AND project_id=? AND status IN ('scheduled','failed')`,
		postID, pid,
	)
	a.publishPostTargets(ctx, postID)

	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='pending'
		  WHERE post_id=? AND status='failed' AND attempts < 4
		    AND EXISTS (SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`,
		postID, pid,
	)
	if err != nil {
		return map[string]any{"status": "error", "error": "prepare retry: " + err.Error()}
	}
	if n, _ := res.RowsAffected(); n > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='publishing' WHERE id=? AND project_id=?`, postID, pid)
		return map[string]any{"status": "error", "error": "publish failed; retry queued", "post_id": postID}
	}

	var active, failed, published int
	var lastError string
	if err := ctx.AppDB().QueryRow(
		`SELECT
		    COALESCE(SUM(CASE WHEN status IN ('pending','publishing') THEN 1 ELSE 0 END),0),
		    COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		    COALESCE(SUM(CASE WHEN status='published' THEN 1 ELSE 0 END),0),
		    COALESCE(MAX(CASE WHEN status='failed' THEN last_error END),'')
		 FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&active, &failed, &published, &lastError); err != nil {
		return map[string]any{"status": "error", "error": "check publish state: " + err.Error()}
	}
	if active > 0 {
		return map[string]any{"status": "error", "error": "publish still in progress", "post_id": postID}
	}
	if failed > 0 {
		if lastError == "" {
			lastError = "one or more social targets failed"
		}
		return map[string]any{"status": "failed", "error": lastError, "post_id": postID, "failed_targets": failed}
	}
	if published == 0 {
		return map[string]any{"status": "failed", "error": "post has no published targets", "post_id": postID}
	}
	return map[string]any{"status": "published", "post_id": postID, "published_targets": published}
}

// handleJobPublishPost preserves compatibility with legacy Jobs rows. New
// schedules use post_publish_scheduled through the platform app-call broker.
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
	var postProject string
	if err := globalCtx.AppDB().QueryRow(`SELECT project_id FROM posts WHERE id=?`, body.PostID).Scan(&postProject); err != nil {
		http.Error(w, "post not found", http.StatusNotFound)
		return
	}
	if requestProject := strings.TrimSpace(r.URL.Query().Get("project_id")); requestProject != "" && requestProject != postProject {
		http.Error(w, "post not found", http.StatusNotFound)
		return
	}
	result := a.publishScheduledPost(globalCtx, postProject, body.PostID)
	status := toString(result["status"])
	if status != "published" {
		code := http.StatusBadGateway
		if status == "error" && toString(result["error"]) == "publish still in progress" {
			code = http.StatusConflict
		}
		http.Error(w, toString(result["error"]), code)
		return
	}
	writeJSON(w, result)
}

func (a *App) handlePlatforms(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid := projectScope(globalCtx, projectArgsFromRequest(r))
	out := make([]map[string]any, 0, len(platforms))
	zernioAvailable := false
	if conns, err := globalCtx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
		ProjectID: pid,
		AppSlug:   zernioProviderSlug,
	}); err == nil {
		for _, c := range conns {
			if c.Status == "active" {
				zernioAvailable = true
				break
			}
		}
	}
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
			"zernio_available": zernioAvailable,
			"option_fields":    fields,
		})
	}
	for _, p := range zernioProviderPlatforms() {
		if _, native := platforms[p.Platform]; native {
			continue
		}
		out = append(out, map[string]any{
			"platform":         p.Platform,
			"display_name":     p.DisplayName,
			"integration_slug": zernioProviderSlug,
			"requires_picker":  true,
			"available":        false,
			"zernio_available": zernioAvailable,
			"provider_only":    true,
			"option_fields":    []optionField{},
		})
	}
	writeJSON(w, map[string]any{"platforms": out})
}

// ─── helpers ───────────────────────────────────────────────────────

type pendingRow struct {
	id                int64
	projectID         string
	platform          string
	integrationSlug   string
	connectionID      int64
	status            string
	profileID         int64
	providerSlug      string
	providerProfileID string
	providerState     string
	providerData      string
	expired           bool
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var row pendingRow
	var expiresAt string
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, project_id, platform, integration_slug, COALESCE(connection_id,0), status,
		        COALESCE(profile_id,0), COALESCE(provider_slug,''), COALESCE(provider_profile_id,''),
		        COALESCE(provider_state,''), COALESCE(provider_data,''), COALESCE(expires_at,'')
		 FROM pending_accounts WHERE id=?`, id,
	).Scan(&row.id, &row.projectID, &row.platform, &row.integrationSlug, &row.connectionID, &row.status, &row.profileID, &row.providerSlug, &row.providerProfileID, &row.providerState, &row.providerData, &expiresAt)
	if err != nil {
		return nil, err
	}
	row.expired = pendingAccountExpired(expiresAt, time.Now().UTC())
	return &row, nil
}

func pendingExpiry(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func pendingAccountExpired(raw string, now time.Time) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return true
	}
	for _, layout := range []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05",
	} {
		if expiresAt, err := time.Parse(layout, raw); err == nil {
			return !expiresAt.After(now)
		}
	}
	// Invalid expiry data must never make an authorization request live forever.
	return true
}

func (a *App) getPendingScoped(ctx *sdk.AppCtx, args map[string]any, id int64) (*pendingRow, error) {
	row, err := a.getPending(id)
	if err != nil {
		return nil, err
	}
	pid := projectScope(ctx, args)
	if pid == "" || row.projectID != pid {
		return nil, sql.ErrNoRows
	}
	return row, nil
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
	return profileFromToolData(res.Data, def), nil
}

func profileFromToolData(data []byte, def platformDef) *profileEntry {
	var raw map[string]any
	_ = json.Unmarshal(data, &raw)
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
	}
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

func boolOption(opts map[string]any, key string) (bool, bool) {
	if opts == nil {
		return false, false
	}
	switch v := opts[key].(type) {
	case bool:
		return v, true
	case string:
		s := strings.TrimSpace(strings.ToLower(v))
		if s == "" {
			return false, false
		}
		switch s {
		case "true", "1", "yes", "on":
			return true, true
		case "false", "0", "no", "off":
			return false, true
		}
	}
	return false, false
}

func anySliceOption(opts map[string]any, key string) []any {
	if opts == nil {
		return nil
	}
	if out, ok := opts[key].([]any); ok {
		return out
	}
	return nil
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

func optionalInt64Arg(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
		return 0, false
	}
	return toInt64Loose(v), true
}

func int64SliceArg(m map[string]any, keys ...string) []int64 {
	for _, key := range keys {
		raw, ok := m[key]
		if !ok || raw == nil {
			continue
		}
		switch v := raw.(type) {
		case []any:
			out := make([]int64, 0, len(v))
			for _, item := range v {
				if id := toInt64Loose(item); id > 0 {
					out = append(out, id)
				}
			}
			return out
		case []int64:
			return v
		case []int:
			out := make([]int64, 0, len(v))
			for _, item := range v {
				if item > 0 {
					out = append(out, int64(item))
				}
			}
			return out
		case string:
			parts := strings.Split(v, ",")
			out := make([]int64, 0, len(parts))
			for _, part := range parts {
				if id := toInt64Loose(part); id > 0 {
					out = append(out, id)
				}
			}
			return out
		default:
			if id := toInt64Loose(v); id > 0 {
				return []int64{id}
			}
		}
	}
	return nil
}

func stringSliceArg(m map[string]any, key string) []string {
	raw, ok := m[key]
	if !ok || raw == nil {
		return nil
	}
	switch v := raw.(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := strings.TrimSpace(toString(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	case string:
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		if s := strings.TrimSpace(toString(v)); s != "" {
			return []string{s}
		}
	}
	return nil
}

func int64Set(in []int64) map[int64]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[int64]bool, len(in))
	for _, v := range in {
		out[v] = true
	}
	return out
}

func stringSet(in []string) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for _, v := range in {
		if s := strings.TrimSpace(v); s != "" {
			out[s] = true
		}
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

func stringArgAny(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func platformKeys() []string {
	out := make([]string, 0, len(platforms))
	for k := range platforms {
		out = append(out, k)
	}
	return out
}

func socialPlatformKeys() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, k := range platformKeys() {
		seen[k] = true
		out = append(out, k)
	}
	for _, p := range zernioProviderPlatforms() {
		if !seen[p.Platform] {
			seen[p.Platform] = true
			out = append(out, p.Platform)
		}
	}
	return out
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
