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
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: social
display_name: Social
version: 0.21.0
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
    - role: editorial
      kind: app
      compatible_app_names: [editorial]
      required: false
      label: "Editorial record repair (optional)"
    - role: content-catalog
      kind: app
      compatible_app_names: [content-catalog]
      required: false
      label: "Content Catalog record repair (optional)"
    - role: social_provider
      kind: integration
      compatible_slugs: [zernio]
      required: false
      label: "Social provider (optional)"
      hint: "Import and publish through a unified social API for platforms that are hard to connect directly."
provides:
  http_routes:
    - prefix: /
    - { method: GET, prefix: /accounts/oauth_done, no_auth: true }
  workers:
    - { name: scheduled_publisher, schedule: "@every 1m" }
    - { name: inbox_collector, schedule: "@every 5m" }
    - { name: analytics_collector, schedule: "@every 1h" }
    - { name: provider_reconciler, schedule: "@every 15m" }
  mcp_tools:
    - { name: account_list_connections, description: "Choose an active direct connection or connect another account." }
    - { name: destination_list, description: "List publishing destinations under an account." }
    - { name: destination_create, description: "Create a publishing destination when supported." }
    - { name: account_set_default_destination, description: "Set the default for new posts; existing targets retain their destination." }
    - { name: account_add,                description: "Begin OAuth for a platform." }
    - { name: account_list_pending_pages, description: "List selectable pages/channels for a pending account." }
    - { name: account_finalize,           description: "Commit a pending account into the active list." }
    - { name: account_import_provider,    description: "Import provider-backed accounts such as Zernio into Social." }
    - { name: account_list,               description: "List connected social accounts." }
    - { name: account_history_sync, description: "Bounded resumable native history sync for one Social account; no publishing or automatic-policy changes." }
    - { name: account_history_status, description: "Read history coverage and freshness for one Social account." }
    - { name: post_inspect, description: "Read native metadata through Social without publishing or implying membership." }
    - { name: account_provider_sync_update, description: "Set provider synchronization to off, managed_only, or explicitly opted-in full_history without changing publishing access." }
    - { name: account_audience_capabilities, description: "Optional account audience capabilities; no provider calls." }
    - { name: account_membership_levels, description: "Optional cached tier discovery; refresh=true queries the existing integration connection." }
    - { name: account_check,              description: "Check that connected social accounts still work." }
    - { name: account_disconnect,         description: "Revoke a social account." }
    - { name: post_create,                description: "Create with required explicit mode=draft|schedule|publish. Drafts are optional; direct scheduling and publishing remain first-class. Missing mode never publishes." }
    - { name: post_draft_create,          description: "Create a media-capable draft without publishing." }
    - { name: post_draft_update,          description: "Update a draft using expected_revision; edits invalidate approval." }
    - { name: post_draft_submit,          description: "Submit an exact draft revision for optional review." }
    - { name: post_draft_approve,         description: "Approve the exact revision in review." }
    - { name: post_draft_reject,          description: "Reject the exact revision in review with a reason." }
    - { name: post_draft_publish,         description: "Publish or schedule a stored draft with explicit mode and expected_revision." }
    - { name: post_list,                  description: "List stored posts with profile filters, pagination, and separate post/target totals." }
    - { name: post_publication_reconcile, description: "Preview or apply account-scoped publication link/time corrections; repair existing linked records without publishing." }
    - { name: post_delivery_inspect, description: "Inspect one target and native operation without resubmitting." }
    - { name: post_delivery_revalidate, description: "Audit source and remote state; prepare an expiring recovery decision." }
    - { name: post_delivery_recover, description: "Explicitly consume one audited recovery decision for one target." }
    - { name: post_retry,                 description: "Retry transient failures only; deterministic duplicates fail once and return the existing post id." }
    - { name: post_publish_scheduled,     description: "Internal Jobs callback that publishes one scheduled post and reports the final downstream result." }
    - { name: inbox_list,                 description: "List inbox items (comments, DMs, mentions, reviews) from connected accounts." }
    - { name: inbox_get,                  description: "Fetch one inbox item, optionally with the surrounding thread." }
    - { name: inbox_mark_read,            description: "Mark inbox items read (local-only)." }
    - { name: inbox_mark_unread,          description: "Mark inbox items unread (local-only)." }
    - { name: inbox_archive,              description: "Archive inbox items (local-only)." }
    - { name: inbox_reply,                description: "Reply to a comment or DM with text and, where supported, image/video/audio/file attachments from Storage or a public HTTPS URL. For comment items, pass mode=public|private|auto; private is supported where the platform exposes private comment replies." }
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
  ui_components:
    - name: publishing-calendar
      label: Social · Publishing calendar
      description: Upcoming scheduled posts, a week calendar, and items needing attention.
      entry: /ui/SocialPublishingCalendarWidget.mjs
      slots: [dashboard.home]
      suggested: true
      visibility: project
      supported_sizes: [half, full]
      default_size: half
      refresh_topics: [post.created, post.rescheduled, post.completed, post.deleted, post.draft_created, post.draft_updated, post.draft_submitted, post.draft_approved, post.draft_rejected, post.publish_requested, target.published, target.pending, target.published_warning, target.awaiting_audience_setup, post.audience_confirmed, target.failed, profile.accounts_moved, account.deleted]
      settings_schema:
        type: object
        properties:
          profile_id: { type: integer, title: Profile ID, description: '0 includes all profiles in this project.', minimum: 0, default: 0 }
          account_ids: { type: string, title: Account IDs, description: 'Optional comma-separated Social account IDs.', default: '' }
          view: { type: string, title: View, enum: [auto, upcoming, calendar], default: auto }
          horizon_days: { type: integer, title: Upcoming days, minimum: 1, maximum: 42, default: 7 }
          max_posts: { type: integer, title: Upcoming post limit, minimum: 1, maximum: 20, default: 5 }
          show_attention: { type: boolean, title: Show items needing attention, default: true }
      preview_props: { preview: true }
    - name: performance
      label: Social · Performance
      description: Cached audience totals, daily performance trends, and account coverage.
      entry: /ui/SocialPerformanceWidget.mjs
      slots: [dashboard.home]
      suggested: true
      visibility: project
      supported_sizes: [half, full]
      default_size: half
      refresh_topics: [metrics.updated, destinations.updated, account.added, account.disconnected, account.deleted, profile.accounts_moved]
      settings_schema:
        type: object
        properties:
          profile_id: { type: integer, title: Profile ID, description: '0 includes all profiles in this project.', minimum: 0, default: 0 }
          account_ids: { type: string, title: Account IDs, description: 'Optional comma-separated Social account IDs.', default: '' }
          days: { type: integer, title: Complete days, enum: [7, 28, 90], default: 28 }
          visibility_metric: { type: string, title: Visibility metric, enum: [views, impressions], default: views }
          show_trends: { type: boolean, title: Show trends, default: true }
      preview_props: { preview: true }
    - name: calendar-card
      entry: /ui/SocialCalendarCard.mjs
      slots: [chat.message_attachment]
      props_schema:
        type: object
        properties:
          profile_id: { type: integer }
          social_account_id: { type: integer }
          view: { type: string, enum: [week, month] }
          status: { type: string, enum: [all, scheduled, published, failed, partial, draft, in_review, approved, rejected, awaiting_audience_setup] }
          anchor_date: { type: string }
      preview_props:
        preview: true
        view: week
    - name: post-card
      entry: /ui/SocialPostCard.mjs
      slots: [chat.message_attachment]
      props_schema:
        type: object
        required: [post_id]
        properties:
          post_id: { type: integer }
      preview_props:
        preview: true
        post_id: 1
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: social/v0.21.0
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
	// ListPagesItemsField/LimitField/CursorArg/CursorResponseField make
	// destination discovery work for non-Graph APIs too. Defaults are
	// data, limit, after, and paging.cursors.after. Pinterest uses
	// items, page_size, bookmark, and bookmark.
	ListPagesItemsField          string
	ListPagesLimitField          string
	ListPagesCursorArg           string
	ListPagesCursorResponseField string
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
	DMAttachmentTypes                          []string
	DMMaxAttachments                           int
	CommentAttachmentTypes                     []string
	CommentMaxAttachments                      int
}

// optionField describes one customizable knob on a platform — its key
// name (matches what publish strategies read), a UI-friendly label,
// the input type the panel should render, and an enum of allowed
// values when applicable.
type optionField struct {
	Name    string   `json:"name"`
	Label   string   `json:"label"`
	Type    string   `json:"type"` // "text" | "textarea" | "select" | "tags" | "media" | "number" | "boolean" | "datetime"
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
		OptionFields: []optionField{
			{Name: "allow_longform", Type: "boolean", Label: "Allow long-form post",
				Help: "Bypass the standard 280-character preflight for X accounts whose API access supports long-form posts."},
		},
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
			DMAttachmentTypes: []string{
				inboxAttachmentImage,
				inboxAttachmentVideo,
				inboxAttachmentAudio,
				inboxAttachmentFile,
			},
			DMMaxAttachments: 1,
			MentionsRead:     true,
			ReviewsRead:      true,
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
			DMAttachmentTypes: []string{
				inboxAttachmentImage,
				inboxAttachmentVideo,
				inboxAttachmentAudio,
				inboxAttachmentFile,
			},
			DMMaxAttachments: 1,
			MentionsRead:     true,
			PrivateReply:     true,
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
			{Name: "privacy_level", Type: "select", Label: "TikTok privacy", Help: "Choose from this creator's current privacy options before publishing."},
			{Name: "disable_comment", Type: "boolean", Label: "Disable comments"},
			{Name: "disable_duet", Type: "boolean", Label: "Disable duet"},
			{Name: "disable_stitch", Type: "boolean", Label: "Disable stitch"},
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
			{Name: "release_at", Type: "datetime", Label: "YouTube release time",
				Help: "Optional. Uploads the video privately and YouTube makes it public at this time (status.publishAt)."},
			{Name: "category", Type: "text", Label: "Category ID",
				Help: "YouTube numeric category id (e.g. 22 = People & Blogs, 27 = Education). Optional."},
			{Name: "contains_synthetic_media", Type: "boolean", Label: "AI-generated or altered content",
				Help: "Discloses that the video contains realistic altered or synthetic content (status.containsSyntheticMedia). Kept on later edits."},
			{Name: "thumbnail_storage_id", Type: "media", Label: "Thumbnail",
				Help: "Optional image from Storage. YouTube applies it after the video upload returns a video id."},
		},
		Inbox: inboxCaps{
			CommentsRead:   true,
			CommentsWrite:  true,
			CommentsDelete: true,
		},
	},
	"pinterest": {
		Platform:                     "pinterest",
		IntegrationSlug:              "pinterest",
		DisplayName:                  "Pinterest",
		Strategy:                     "pinterest",
		PostTool:                     "create_pin",
		BodyField:                    "description",
		ExternalIDField:              "board_id",
		MediaRequired:                true,
		MediaType:                    "any",
		ListPagesTool:                "",
		PageIDField:                  "id",
		PageNameField:                "name",
		PageAvatarField:              "media.image_cover_url",
		ListPagesItemsField:          "items",
		ListPagesLimitField:          "page_size",
		ListPagesCursorArg:           "bookmark",
		ListPagesCursorResponseField: "bookmark",
		ProfileTool:                  "get_user_account",
		ProfileNameField:             "username",
		ProfileAvatarField:           "profile_image",
		DeleteTool:                   "delete_pin",
		DeleteIDField:                "pin_id",
		OptionFields: []optionField{
			{Name: "title", Type: "text", Label: "Pin title",
				Help: "Optional Pinterest title. Maximum 100 characters."},
			{Name: "link", Type: "text", Label: "Destination link",
				Help: "Optional URL opened when someone clicks the Pin."},
			{Name: "alt_text", Type: "textarea", Label: "Alt text",
				Help: "Accessible description of the visual. Maximum 500 characters."},
			{Name: "board_section_id", Type: "text", Label: "Board section ID",
				Help: "Optional section inside the connected board."},
			{Name: "thumbnail_storage_id", Type: "media", Label: "Video cover",
				Help: "Required for video Pins unless an attached image is used as the cover."},
		},
		Inbox: inboxCaps{},
	},
}

var globalCtx *sdk.AppCtx

type App struct{ avatarClient *http.Client }

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
			Schedule: "@every 1h",
			Run:      a.runAnalyticsCollector,
		},
		{
			Name:     "provider_reconciler",
			Schedule: "@every 15m",
			Run:      a.runProviderReconciler,
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (panel) ───────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Account management
		{Pattern: "/accounts", Handler: a.handleAccountsAPI},
		{Pattern: "/destinations/accounts/", Handler: a.handleDestinationAccount},
		{Pattern: "/audiences/accounts/", Handler: a.handleAudienceAccount},
		{Pattern: "/supporters/accounts/", Handler: a.handleSupportersAccount},
		{Pattern: "/audiences/confirm", Handler: a.handleAudienceConfirm},
		{Pattern: "/accounts/start", Handler: a.handleAccountsStart},
		{Pattern: "/accounts/connections", Handler: a.handleAccountConnections},
		{Method: http.MethodGet, Pattern: "/accounts/oauth_done", Handler: a.handleOAuthDone, NoAuth: true},
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
		// Read-only summaries for project Home widgets.
		{Pattern: "/widgets/publishing-calendar", Handler: a.handlePublishingWidget},
		{Pattern: "/widgets/performance", Handler: a.handlePerformanceWidget},
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
				"Use account_list_connections to choose an existing direct connection, or force_new=true for a fresh OAuth login to another account. Multiple available grants require an explicit choice. " +
				"Otherwise returns authorize_url + pending_account_id and the human must visit the URL. " +
				"The integrations system handles token exchange + refresh; this app never sees the access token. " +
				"To connect through an optional provider, pass provider='zernio'; Social starts the provider OAuth flow and imports the selected provider-backed account into this project. " +
				"Args: platform, provider? ('zernio' for provider-backed accounts), provider_profile_id? (Zernio profile/workspace id; omitted uses the first Zernio profile), force_new? (default false; set true to force a fresh OAuth dance even when an existing connection is available, e.g. to switch to a different provider-side account).",
			InputSchema: schemaObject(map[string]any{
				"platform":            map[string]any{"type": "string", "enum": socialPlatformKeys()},
				"provider":            map[string]any{"type": "string", "enum": []string{"zernio"}},
				"provider_profile_id": map[string]any{"type": "string"},
				"force_new":           map[string]any{"type": "boolean"},
				"connection_id":       map[string]any{"type": "integer", "minimum": 1, "description": "Active direct connection from account_list_connections; mutually exclusive with force_new."},
				"profile_id":          map[string]any{"type": "integer"},
				"profile":             map[string]any{"type": "string"},
				"return_to": map[string]any{
					"type":        "string",
					"description": "Optional Social OAuth callback path; must be /api/apps/social/accounts/oauth_done, optionally with the matching project_id.",
				},
			}, []string{"platform"}),
			Handler: a.toolAccountAdd,
		},
		{
			Name:        "account_list_pending_pages",
			Description: "After OAuth completes, list the pages/channels/boards/accounts the user can pick from. Empty result means the platform has no setup step (e.g. Twitter personal) and you can call account_finalize directly. Args: pending_account_id.",
			InputSchema: schemaObject(map[string]any{
				"pending_account_id": map[string]any{"type": "integer"},
			}, []string{"pending_account_id"}),
			Handler: a.toolAccountListPendingPages,
		},
		{
			Name:        "account_finalize",
			Description: "Commit a pending account into the active social_accounts list. For identity-selection platforms (Facebook, Instagram) supply page_id from account_list_pending_pages; for personal platforms page_id is optional. Args: pending_account_id, page_id?, name?.",
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
				"Today provider must be zernio. The imported accounts behave like normal Social accounts: use post_create, post_list, account_check, metrics, and explicit imports through Social. " +
				"Account connection never opts into historical post import; automatic synchronization defaults to managed_only. " +
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
			Description: "List connected social accounts in this project, including provider_import_mode for provider-backed accounts. Disconnected history-only rows are hidden unless status='disconnected' is requested. Args: platform? (filter), status? (active|needs_reauth|disconnected).",
			InputSchema: schemaObject(map[string]any{
				"platform": map[string]any{"type": "string"},
				"status":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolAccountList,
		},
		{
			Name: "account_provider_sync_update",
			Description: "Configure automatic provider synchronization for one provider-backed account without changing its publishing or direct-scheduling access. " +
				"managed_only (default) reads only provider posts already created, mirrored, or explicitly imported through Social; it never imports unrelated account history. " +
				"off disables automatic provider reads. full_history explicitly opts into automatic discovery/import of recent provider history. " +
				"Manual history reads remain explicit and do not change automatic settings; account_history_sync mode=full is bounded and resumable. Args: social_account_id, mode.",
			InputSchema: schemaObject(map[string]any{
				"social_account_id": map[string]any{"type": "integer"},
				"mode":              map[string]any{"type": "string", "enum": []string{providerImportModeOff, providerImportModeManaged, providerImportModeFullHistory}},
			}, []string{"social_account_id", "mode"}),
			Handler: a.toolAccountProviderSyncUpdate,
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
			Description: "Create a post with an explicit, required intent: mode=draft stores it without publishing, mode=schedule requires schedule_at, and mode=publish publishes now. Omitting mode is always rejected and has no side effects. Drafts may omit targets; scheduling and publishing require at least one. " +
				"Pass EITHER social_account_ids[] (simple multicast — every target uses the post body) " +
				"OR targets[] (when you want per-target overrides). The two are mutually exclusive. " +
				"Each target object: {social_account_id (required), body? (required when top-level body is omitted; otherwise override text for this target), " +
				"plus platform-specific keys for the target's platform}. " +
				"Today: youtube accepts {title, body, visibility (public|unlisted|private), category, tags[], thumbnail_storage_id}. Optional target audience={type:members,level_ids?:[]} requests membership staging; omitted audience preserves ordinary publishing. Membership uploads require the updated YouTube catalog/server and completion in Studio. " +
				"Destination-capable accounts accept destination_id per target; omitted uses the saved default, frozen when the post is created. Pinterest boards can be listed/created with destination_list/destination_create on both native and Zernio connections. " +
				"X accepts {body, allow_longform}; normal posts are validated against X's 280-character weighted limit before media is uploaded. " +
				"Facebook accepts {body, thumbnail_storage_id} for video posts; Instagram accepts {body, thumbnail_storage_id, thumbnail_frame_ms} for Reels; TikTok accepts videos with {body, thumbnail_frame_ms} or photo posts with {body, title, auto_add_music, photo_cover_index}. " +
				"Use plain platform text, not Markdown formatting; most social platforms do not render Markdown. " +
				"Body resolution per target: target.body if set, else post-level body. Top-level body may be omitted only when every target has its own non-empty body. " +
				"Immediate publishes return targets[] with per-platform status, platform_post_id, and platform_url when available. Scheduled publishes return a local scheduled post; call post_list after it runs to get platform_url. " +
				"Scheduled creates are idempotent: if the same profile/account/media/body/options/time already exists, the existing post is returned or its failed scheduling is retried instead of creating a duplicate. " +
				"If scheduling fails, retry that existing post with post_retry; do not create a second post with the same args. " +
				"Args: mode (draft|schedule|publish), body? or targets[].body, schedule_at? (RFC3339; required only for schedule), media_storage_ids? (file ids), media_project_id? (Storage project scope for those ids), approval_required?, provider_sync_mode? (local|mirror).",
			InputSchema: schemaObject(map[string]any{
				"mode":               map[string]any{"type": "string", "enum": []string{"draft", "schedule", "publish"}, "description": "Required publishing intent. There is no implicit publish default."},
				"body":               map[string]any{"type": "string", "description": "Post-level default text. Required when using social_account_ids; optional with targets[] if every target has a non-empty body."},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"targets": map[string]any{
					"type":        "array",
					"description": "Per-target overrides. Each entry: {social_account_id (required), body? required when top-level body is omitted, plus platform-specific keys such as allow_longform for X or title/visibility/thumbnail_storage_id for YouTube}. Mutually exclusive with social_account_ids.",
					"items":       map[string]any{"type": "object"},
				},
				"schedule_at":        map[string]any{"type": "string"},
				"media_storage_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"media_project_id":   map[string]any{"type": "string"},
				"approval_required":  map[string]any{"type": "boolean", "description": "If true, the draft's current revision must be approved before it can be scheduled or published."},
				"provider_sync_mode": map[string]any{"type": "string", "enum": []string{"local", "mirror"}, "description": "Draft/provider synchronization policy. Defaults to local."},
			}, []string{"mode"}),
			Handler: a.toolPostCreate,
		},
		{
			Name:        "post_draft_create",
			Description: "Create a local draft without publishing. Convenience alias for post_create mode=draft. Targets and media are optional and may be added later. Args: body?, social_account_ids? or targets?, media_storage_ids?, media_project_id?, approval_required?, provider_sync_mode?.",
			InputSchema: schemaObject(map[string]any{
				"body":               map[string]any{"type": "string"},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"targets":            map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"media_storage_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"media_project_id":   map[string]any{"type": "string"},
				"approval_required":  map[string]any{"type": "boolean"},
				"provider_sync_mode": map[string]any{"type": "string", "enum": []string{"local", "mirror"}},
			}, nil),
			Handler: a.toolPostDraftCreate,
		},
		{
			Name:        "post_draft_update",
			Description: "Update a draft using optimistic revision locking. Body, targets and media attachments can be replaced. Any approved revision returns to draft and requires fresh approval. Args: post_id, expected_revision, body?, social_account_ids? or targets?, media_storage_ids?, media_project_id?.",
			InputSchema: schemaObject(map[string]any{
				"post_id":            map[string]any{"type": "integer"},
				"expected_revision":  map[string]any{"type": "integer"},
				"body":               map[string]any{"type": "string"},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"targets":            map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"media_storage_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"media_project_id":   map[string]any{"type": "string"},
			}, []string{"post_id", "expected_revision"}),
			Handler: a.toolPostDraftUpdate,
		},
		{
			Name:        "post_draft_submit",
			Description: "Submit a draft revision for review. This opts the post into approval; direct scheduling/publishing remains available for drafts that do not require approval. Args: post_id, expected_revision.",
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "expected_revision": map[string]any{"type": "integer"}}, []string{"post_id", "expected_revision"}),
			Handler:     a.toolPostDraftSubmit,
		},
		{
			Name:        "post_draft_approve",
			Description: "Approve the exact revision currently in review. Later edits invalidate this approval. Args: post_id, expected_revision.",
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "expected_revision": map[string]any{"type": "integer"}}, []string{"post_id", "expected_revision"}),
			Handler:     a.toolPostDraftApprove,
		},
		{
			Name:        "post_draft_reject",
			Description: "Reject the exact revision currently in review with a required reason. Args: post_id, expected_revision, reason.",
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "expected_revision": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"}}, []string{"post_id", "expected_revision", "reason"}),
			Handler:     a.toolPostDraftReject,
		},
		{
			Name:        "post_draft_publish",
			Description: "Publish or schedule a stored draft revision. mode=publish publishes now; mode=schedule requires schedule_at. Approval is enforced only when the post opted into it. Args: post_id, expected_revision, mode, schedule_at?.",
			InputSchema: schemaObject(map[string]any{
				"post_id":           map[string]any{"type": "integer"},
				"expected_revision": map[string]any{"type": "integer"},
				"mode":              map[string]any{"type": "string", "enum": []string{"publish", "schedule"}},
				"schedule_at":       map[string]any{"type": "string"},
			}, []string{"post_id", "expected_revision", "mode"}),
			Handler: a.toolPostDraftPublish,
		},
		{
			Name:        "post_list",
			Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false},
			Description: "Read-only: retrieve stored post history without creating, retrying, scheduling or updating posts. List stored posts with per-target status, newest lifecycle date first. Filter by profile_id or profile (slug), status, from/to, social_account_id, query (title/body/original filename) and exact native_post_id before pagination. compact=true omits delivery diagnostics. Includes stored history coverage/freshness per account. Unknown native fields are null. Args: limit? (default 50, max 200), offset? (default 0). Returns posts, total (matching post records), total_targets (matching per-account delivery entries), returned_targets, limit, offset, has_more, next_offset (null at end). Continue with next_offset and the same filters. published_at is authoritative native publication only; publication_facts separates submission, provider acceptance, local delivery recording, native upload and observation; legacy times remain evidence. Counts include all statuses unless filtered, and only stored history; deleted or never-imported platform history is excluded. Keep the dataset unchanged while paging, or restart if it changes.",
			InputSchema: schemaObject(map[string]any{
				"limit":             map[string]any{"type": "integer", "default": 50, "minimum": 1, "maximum": 200},
				"offset":            map[string]any{"type": "integer", "default": 0, "minimum": 0},
				"status":            map[string]any{"type": "string", "description": "Exact post status; omitted includes all statuses."},
				"query":             map[string]any{"type": "string", "description": "Text search across stored title, body and known original filename before pagination."},
				"social_account_id": map[string]any{"type": "integer", "minimum": 1, "description": "Social account ID from account_list; no provider connection IDs."},
				"native_post_id":    map[string]any{"type": "string", "description": "Exact native post ID; combine with social_account_id for unambiguous lookup."},
				"compact":           map[string]any{"type": "boolean", "description": "Return compact post/target summaries with native observations instead of delivery diagnostics."},
				"profile_id":        map[string]any{"type": "integer", "minimum": 0, "description": "Social profile ID from profile_list. Omitted or 0 includes all profiles unless a profile slug is supplied; a positive ID takes precedence over profile."},
				"profile":           map[string]any{"type": "string", "description": "Social profile slug from profile_list."},
				"from":              map[string]any{"type": "string", "description": "Inclusive RFC3339 lifecycle date: published_at for published/partial posts, otherwise schedule_at or created_at."},
				"to":                map[string]any{"type": "string", "description": "Exclusive RFC3339 lifecycle date bound."},
			}, nil),
			Handler: a.toolPostList,
		},
		{
			Name:        "post_delivery_inspect",
			Description: "Inspect one existing destination and its native operation without retrying or changing publication status. Args: post_id, target_id.",
			Annotations: map[string]any{"readOnlyHint": true, "destructiveHint": false},
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "social_account_id": map[string]any{"type": "integer", "minimum": 1}, "target_id": map[string]any{"type": "integer"}, "history_after_id": map[string]any{"type": "integer", "minimum": 0}, "history_limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}, []string{"post_id", "target_id"}),
			Handler:     a.toolPostDeliveryInspect,
		},
		{
			Name:        "post_delivery_revalidate",
			Description: "Audit source, account, options and known remote state for one target. Returns decision, evidence and an expiring recovery_id; never publishes. Args: post_id, target_id, review_expired_upload? (explicit review of application-owned receipts).",
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "social_account_id": map[string]any{"type": "integer", "minimum": 1}, "target_id": map[string]any{"type": "integer"}, "review_expired_upload": map[string]any{"type": "boolean", "description": "Explicitly review expiry evidence for a zero-byte TikTok FILE_UPLOAD. Missing legacy evidence blocks replacement."}}, []string{"post_id", "target_id"}),
			Handler:     a.toolPostDeliveryRevalidate,
		},
		{
			Name:        "post_delivery_recover",
			Description: "Consume one audited recovery decision with confirm=true. Rechecks source and remote state, reconciles completed/pending operations, or retries only this eligible target once per revision. Expired zero-byte uploads require verified receipts and a stopped uploader. Args: post_id, target_id, recovery_id, confirm.",
			InputSchema: schemaObject(map[string]any{"post_id": map[string]any{"type": "integer"}, "social_account_id": map[string]any{"type": "integer", "minimum": 1}, "target_id": map[string]any{"type": "integer"}, "recovery_id": map[string]any{"type": "string"}, "confirm": map[string]any{"type": "boolean"}}, []string{"post_id", "target_id", "recovery_id", "confirm"}),
			Handler:     a.toolPostDeliveryRecover,
		},
		{
			Name:        "post_retry",
			Description: "Re-attempt failed delivery. Pass target_id to retry only that destination; published and other targets are preserved. Known TikTok operations are reconciled before retry. Omit target_id for existing post-wide retry or failed-schedule recovery. Args: post_id, target_id?.",
			InputSchema: schemaObject(map[string]any{
				"post_id":   map[string]any{"type": "integer"},
				"target_id": map[string]any{"type": "integer", "minimum": 1},
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
			Description: "Delete a post locally and, where the platform/provider allows it, remove the upstream copy too. Provider imports receive a local tombstone so automatic or manual history imports cannot recreate them. For each published target the app calls the native platform delete verb or the bound provider delete verb (for example Zernio). Some providers/platforms report published-post deletion as unsupported; in that case local rows still go and the response includes a per-target `upstream` array (status: deleted | unsupported | skipped | failed) so callers can flag platforms that still hold a copy. Cancels any scheduled job first. Args: post_id, force_local_only? (skip all upstream calls; default false).",
			InputSchema: schemaObject(map[string]any{
				"post_id":          map[string]any{"type": "integer"},
				"force_local_only": map[string]any{"type": "boolean", "description": "Skip upstream platform deletion; only remove local rows. Default false."},
			}, []string{"post_id"}),
			Handler: a.toolPostDelete,
		},
		{
			Name: "post_metrics",
			Description: "Fetch per-target performance metrics for a post by fanning out to each platform's analytics tool. Returns a per-target array of {status: ok|unsupported|skipped|failed, metrics?: {views, likes, comments, shares, raw}, reason?, error?}. " +
				"Native accounts use their platform adapter; provider-backed accounts use the bound provider adapter, including LinkedIn through Zernio. " +
				"Results are cached for up to four hours; refresh=true fetches upstream again. Normalized snapshots are stored for history. Be mindful of upstream rate limits when looping over many posts. Args: post_id or post_ids (max 20), range?, start_date?, end_date?, refresh?. Reporting metadata states actual scope, metric availability and lifetime versus date-range semantics.",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"post_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "maxItems": 20},
				"range":    map[string]any{"type": "string"}, "start_date": map[string]any{"type": "string"}, "end_date": map[string]any{"type": "string"}, "refresh": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolPostMetrics,
		},
		{
			Name:        "account_metrics",
			Description: "Fetch normalized totals, history, and provider-supported audience breakdowns. Use social_account_id for the backward-compatible single-account response. Use social_account_ids or profile_id with group_by=['social_account'] to compare channels; filters such as {device:['tv']} rank only matching views. Unsupported dimensions are reported explicitly and never treated as zero. YouTube supports device, OS, country, traffic source, audience type, content type, sharing service, and video; Instagram supports available audience demographics; organic X currently exposes temporal metrics but no device breakdown. Optional scope=destination|provider_account, group_by=[post], sort_by, limit, compare, published_in_range. Pinterest destinations aggregate current board Pins with explicit coverage; provider-account results must not be counted once per board.",
			InputSchema: schemaObject(map[string]any{
				"destination_id":     map[string]any{"type": "string", "description": "Optional publishing destination to report on; native Pinterest board ID."},
				"scope":              map[string]any{"type": "string", "enum": []string{"destination", "provider_account"}},
				"compare":            map[string]any{"type": "boolean", "description": "Compare with the preceding equal-length period where supported."},
				"published_in_range": map[string]any{"type": "boolean", "description": "Restrict destination inventory to posts published in the reporting window."},
				"sort_by":            map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				"max_posts": map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}, "refresh": map[string]any{"type": "boolean"},
				"social_account_id":  map[string]any{"type": "integer"},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}, "description": "Accounts to compare."},
				"profile_id":         map[string]any{"type": "integer", "description": "Compare active accounts in this Social profile."},
				"range":              map[string]any{"type": "string", "description": "Relative window such as 7d, 28d, or 90d (maximum 730d)."},
				"start_date":         map[string]any{"type": "string", "description": "Optional YYYY-MM-DD range start."},
				"end_date":           map[string]any{"type": "string", "description": "Optional YYYY-MM-DD range end."},
				"breakdowns":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Canonical dimensions such as device, os, country, traffic_source, age, gender, audience_type, content_type, video."},
				"filters":            map[string]any{"type": "object", "description": "Dimension filters, for example {device:[\"tv\"]}."},
				"group_by":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Use social_account to compare channels or post for a ranked post report."},
				"metric":             map[string]any{"type": "string", "enum": []string{"views", "followers", "reach", "impressions", "engagements", "saves", "pin_clicks", "outbound_clicks", "video_views", "clicks"}},
				"include_raw":        map[string]any{"type": "boolean", "description": "Include sanitized raw provider responses for explicit debugging. Defaults to false."},
			}, nil),
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
			Description: "Fetch one inbox item by id. Pass with_thread=true to return the latest conversation page. Use thread_next_before as thread_before to load older messages. Args: id, with_thread?, thread_limit?, thread_before?.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"with_thread":   map[string]any{"type": "boolean"},
				"thread_limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 500},
				"thread_before": map[string]any{"type": "integer"},
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
			Description: "Reply to an inbox item. Routes by kind — a `comment` produces a child comment, a `dm` produces an outbound message in the same thread. Returns {status: ok|partial|unsupported|skipped|failed, reason?, error?, external_id?, permalink?, deliveries?}. " +
				"For comment items, mode controls the route: `auto`/`public` creates a public child comment, `private` sends a private reply where supported (Facebook Pages and Instagram Business). " +
				"For DMs, attachments can reference Storage through media_storage_ids or attachments:[{source:'storage',storage_id}], or a public HTTPS URL through attachments:[{source:'url',url,type}]. Platform capabilities decide the allowed types. Args: id, body?, mode?, media_storage_ids?, media_project_id?, attachments?.",
			InputSchema: schemaObject(map[string]any{
				"id":                map[string]any{"type": "integer"},
				"body":              map[string]any{"type": "string"},
				"mode":              map[string]any{"type": "string", "enum": []string{"auto", "public", "private"}, "default": "auto"},
				"media_storage_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"media_project_id":  map[string]any{"type": "string"},
				"attachments": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"source":     map[string]any{"type": "string", "enum": []string{"storage", "url"}},
							"storage_id": map[string]any{"type": "integer"},
							"project_id": map[string]any{"type": "string"},
							"url":        map[string]any{"type": "string"},
							"type":       map[string]any{"type": "string", "enum": []string{"image", "video", "audio", "file"}},
							"mime":       map[string]any{"type": "string"},
							"name":       map[string]any{"type": "string"},
							"size_bytes": map[string]any{"type": "integer"},
						},
					},
				},
			}, []string{"id"}),
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
	tools = append(tools, append(a.historyTools(), publicationReconcileTool(a), accountSupportersTool(a))...)
	tools = append(tools, a.profileTools()...)
	tools = append(tools, a.audienceTools()...)
	tools = append(tools, destinationTools(a)...)
	tools = append(tools, sdk.Tool{Name: "account_list_connections", Description: "List active direct connections for a platform in this project, with linked account and brand/profile labels. Choose one with account_add connection_id or connect another account with force_new=true. No credentials are returned.", InputSchema: schemaObject(map[string]any{"platform": map[string]any{"type": "string", "enum": platformKeys()}}, []string{"platform"}), Handler: a.toolAccountListConnections, Annotations: map[string]any{"readOnlyHint": true}})
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
	ctx = ctx.WithProject(projectScope(ctx, args))
	plat, _ := args["platform"].(string)
	provider := strings.ToLower(strings.TrimSpace(toString(args["provider"])))
	if provider == zernioProviderSlug {
		if _, present := args["connection_id"]; present {
			return mcpError("connection_id is for direct connections; use provider account selection for Zernio"), nil
		}
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

	var connectionID int64
	if raw, present := args["connection_id"]; present {
		var err error
		text := strings.TrimSpace(toString(raw))
		if number, ok := raw.(float64); ok {
			text = strconv.FormatFloat(number, 'f', -1, 64)
		}
		connectionID, err = strconv.ParseInt(text, 10, 64)
		if err != nil || connectionID <= 0 {
			return mcpError("connection_id must be a positive integer"), nil
		}
	}
	if forceNew && connectionID > 0 {
		return mcpError("Choose connection_id or force_new, not both"), nil
	}
	if connectionID > 0 || (!forceNew && (def.ListPagesTool != "" || def.Platform == "pinterest")) {
		choices, err := accountConnectionChoices(ctx, pid, def)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		if connectionID > 0 {
			for _, choice := range choices {
				if choice.ID == connectionID {
					return pendingForExistingConnection(ctx, pid, def, connectionID, profileID)
				}
			}
			return mcpError("connection not found, inactive, or unavailable for this platform/project; reload available connections"), nil
		}
		if len(choices) > 1 {
			return map[string]any{"status": "select_connection", "platform": def.Platform, "connections": choices, "instructions": "Choose connection_id from this list or use force_new=true to connect another account."}, nil
		}
		if len(choices) == 1 {
			return pendingForExistingConnection(ctx, pid, def, choices[0].ID, profileID)
		}
	}

	// Both MCP and panel starts return to the same token-validated handoff.
	returnTo, err := socialOAuthReturnURL(pid, toString(args["return_to"]))
	if err != nil {
		return mcpError(err.Error()), nil
	}
	callbackToken, callbackHash, err := newOAuthCallbackToken()
	if err != nil {
		return nil, fmt.Errorf("create OAuth callback token: %w", err)
	}

	// Pre-create the pending row so we have a stable id we can hand
	// the agent. It'll be linked to the connection once OAuth completes.
	now := time.Now().UTC()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at, profile_id, callback_token_hash)
		 VALUES (?, ?, ?, 'pending_oauth', ?, ?, ?)`,
		pid, def.Platform, def.IntegrationSlug, pendingExpiry(now.Add(10*time.Minute)), profileID, callbackHash,
	)
	if err != nil {
		return nil, fmt.Errorf("create pending account: %w", err)
	}
	pendingID, _ := res.LastInsertId()

	// The platform preserves these parameters when it redirects the browser
	// back with conn_id and status. Only the token hash is stored locally.
	returnURL := fmt.Sprintf("%s&pending=%d&callback_token=%s", returnTo, pendingID, url.QueryEscape(callbackToken))

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
	if out == nil || out.ConnectionID <= 0 {
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		return mcpError("OAuth start did not return a connection ID"), nil
	}
	linked, err := ctx.AppDB().Exec(
		`UPDATE pending_accounts SET connection_id=? WHERE id=? AND project_id=? AND status='pending_oauth'`,
		out.ConnectionID, pendingID, pid,
	)
	if err != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM pending_accounts WHERE id=?`, pendingID)
		_ = ctx.PlatformAPI().DisconnectConnection(out.ConnectionID)
		return nil, fmt.Errorf("save pending OAuth connection: %w", err)
	}
	if n, _ := linked.RowsAffected(); n != 1 {
		_ = ctx.PlatformAPI().DisconnectConnection(out.ConnectionID)
		return mcpError("pending OAuth request disappeared before the connection was linked"), nil
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	defaultDestination, defaultDestinationName := "", ""
	if def.Platform == "pinterest" && pageID != "" {
		selected, e := findDestination(ctx, destinationAccount{Platform: row.platform, Provider: "native", ConnID: row.connectionID}, pageID)
		if e != nil {
			return mcpError(e.Error()), nil
		}
		defaultDestination = selected.ID
		defaultDestinationName = selected.Name
		pageID = ""
	}

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
	// cache. cacheAvatar omits the avatar on any error
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
		var existingProfile int64
		if err := tx.QueryRow(`SELECT COALESCE(profile_id,0) FROM social_accounts WHERE id=? AND project_id=?`, existingID, pid).Scan(&existingProfile); err != nil {
			return nil, err
		}
		if existingProfile > 0 && profileID > 0 && existingProfile != profileID {
			return mcpError("This account is already linked to another brand/profile. Switch to that profile, use the account Move action, or connect another account."), nil
		}
		if existingProfile > 0 {
			profileID = existingProfile
		}
		if defaultDestination != "" {
			if _, err := tx.Exec(`UPDATE social_accounts SET default_destination_id=?,default_destination_name=? WHERE id=? AND project_id=?`, defaultDestination, defaultDestinationName, existingID, pid); err != nil {
				return nil, err
			}
		}
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
		// A reconnect can add scopes that were unavailable on the old
		// token. Clear capability backoffs so the next worker tick tests
		// the new credentials immediately.
		if _, err := tx.Exec(`DELETE FROM inbox_cursors WHERE social_account_id=?`, existingID); err != nil {
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
	if defaultDestination != "" {
		if _, err := tx.Exec(`UPDATE social_accounts SET default_destination_id=?,default_destination_name=? WHERE id=? AND project_id=?`, defaultDestination, defaultDestinationName, id, pid); err != nil {
			return nil, err
		}
	}
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	             COALESCE(provider_profile_id,''), COALESCE(capabilities,''),
	             COALESCE(provider_import_mode,'managed_only'),default_destination_id,default_destination_name,account_kind
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
			id, connID, profID                                      int64
			platform, externalID, name, avatar, status, createdAt   string
			checkAt, checkStatus, checkError, checkDetails          string
			providerSlug, providerAccountID, providerProfileID      string
			capabilitiesRaw, providerImportMode                     string
			defaultDestination, defaultDestinationName, accountKind string
		)
		if err := rows.Scan(&id, &platform, &connID, &externalID, &name, &avatar, &status, &createdAt, &profID, &checkAt, &checkStatus, &checkError, &checkDetails, &providerSlug, &providerAccountID, &providerProfileID, &capabilitiesRaw, &providerImportMode, &defaultDestination, &defaultDestinationName, &accountKind); err != nil {
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
			"id":                       id,
			"platform":                 platform,
			"connection_id":            connID,
			"external_account_id":      externalID,
			"display_name":             name,
			"avatar_url":               avatar,
			"status":                   status,
			"created_at":               createdAt,
			"profile_id":               profID,
			"last_check_at":            checkAt,
			"last_check_status":        checkStatus,
			"last_check_error":         checkError,
			"last_check_details":       details,
			"provider_slug":            providerSlug,
			"audience_capabilities":    adapterForAudience(platform, providerSlug).Capabilities,
			"destination_capabilities": adapterForDestinations(platform, providerSlug),
			"default_destination_id":   defaultDestination, "default_destination_name": defaultDestinationName, "account_kind": accountKind,
			"provider_account_id":  providerAccountID,
			"provider_profile_id":  providerProfileID,
			"provider_import_mode": providerImportMode,
			"history_capabilities": historyCapabilities(platform, providerSlug, externalID),
			"capabilities":         capabilities,
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	if _, err := tx.Exec(`DELETE FROM social_membership_cache WHERE social_account_id=?`, id); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM target_delivery_attempts WHERE target_id IN (SELECT id FROM post_targets WHERE social_account_id=?)`, id); err != nil {
		return nil, err
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
	if _, err = tx.Exec(`DELETE FROM account_metric_cache WHERE social_account_id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
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
		ok := map[string]bool{"body": true, "audience": true, "destination_id": true, "board_id": true, "boardId": true}
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
		if err := validateAudienceTarget(ctx, pid, target); err != nil {
			return nil, fmt.Errorf("account %d: %w", target.SocialAccountID, err)
		}
		profiles[target.SocialAccountID] = profileID
	}
	return profiles, nil
}

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	mode, err := explicitPostMode(args)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	body, _ := args["body"].(string)
	targets, _, err := parsePostTargets(args, mode == postModeDraft)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if mode != postModeDraft {
		if err := validatePostBodyForDelivery(body, targets); err != nil {
			return nil, err
		}
		if strings.TrimSpace(body) == "" && len(targets) > 0 {
			body, _ = targets[0].Options["body"].(string)
		}
	}
	// Validate per-target options against each target's platform's
	// declared OptionFields. Unknown keys log a warning but don't fail
	// the call (forward-compat: an agent passing a field that lands
	// in a future version still works).
	pid := projectScope(ctx, args)
	targetProfiles := map[int64]int64{}
	if len(targets) > 0 {
		targetProfiles, err = validatePostTargets(ctx, pid, targets)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		if err := freezeTargetDestinations(ctx, pid, targets, mode != postModeDraft); err != nil {
			return mcpError(err.Error()), nil
		}
		a.validateTargetOptions(ctx, pid, targets)
	}
	// Flat list of just the account ids — used by profile-spanning
	// resolution below, same shape the prior code path relied on.
	acctIDs := make([]int64, len(targets))
	for i, t := range targets {
		acctIDs[i] = t.SocialAccountID
	}
	scheduleAt := strings.TrimSpace(toString(args["schedule_at"]))
	if mode == postModeSchedule {
		if scheduleAt == "" {
			return mcpError("schedule_at required when mode=schedule"), nil
		}
		rfc, err := normaliseScheduleAt(scheduleAt)
		if err != nil {
			return mcpError(fmt.Sprintf("invalid schedule_at %q: %v", scheduleAt, err)), nil
		}
		scheduleAt = rfc
	} else if scheduleAt != "" {
		return mcpError("schedule_at is only valid when mode=schedule"), nil
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
	switch mode {
	case postModeDraft:
		status = "draft"
	case postModeSchedule:
		status = "scheduled"
	}
	providerSyncMode := strings.ToLower(strings.TrimSpace(toString(args["provider_sync_mode"])))
	if providerSyncMode == "" {
		providerSyncMode = "local"
	}
	if providerSyncMode != "local" && providerSyncMode != "mirror" {
		return mcpError("provider_sync_mode must be local or mirror"), nil
	}
	approvalRequired := boolArg(args, "approval_required", false)
	if approvalRequired && mode != postModeDraft {
		return mcpError("approval_required needs mode=draft; submit and approve the draft before delivery"), nil
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

	if mode == postModeSchedule {
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
		`INSERT INTO posts
		 (project_id, body, media_storage_ids, media_project_id, schedule_at, status, profile_id,
		  revision, approval_status, approval_required, requested_mode, provider_sync_mode, source, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 1, 'not_requested', ?, ?, ?, 'local', CURRENT_TIMESTAMP)`,
		pid, body, string(mediaJSON), mediaProjectID, nullable(scheduleAt), status, profileID,
		approvalRequired, mode, providerSyncMode,
	)
	if err != nil {
		return nil, err
	}
	postID, _ := res.LastInsertId()

	// Fan out: one target row per requested social account, carrying
	// any per-target overrides as a JSON blob in post_targets.options.
	targetStatus := "pending"
	if mode == postModeDraft {
		targetStatus = "draft"
	}
	for _, t := range targets {
		var optsJSON sql.NullString
		if len(t.Options) > 0 {
			b, _ := json.Marshal(t.Options)
			optsJSON = sql.NullString{String: string(b), Valid: true}
		}
		_, err := tx.Exec(
			`INSERT INTO post_targets (post_id, social_account_id, status, options) VALUES (?, ?, ?, ?)`,
			postID, t.SocialAccountID, targetStatus, optsJSON,
		)
		if err != nil {
			return nil, fmt.Errorf("create post target for account %d: %w", t.SocialAccountID, err)
		}
	}
	if err := recordPostRevisionTx(tx, postID, 1, workflowActor(args)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if mode == postModeDraft {
		ctx.Emit("post.draft_created", map[string]any{"post_id": postID, "revision": 1, "accounts": acctIDs})
		providerSync := a.syncProviderWorkflow(ctx, postID, postModeDraft, "")
		post, err := a.loadPostByID(ctx, pid, postID)
		if post != nil && len(providerSync) > 0 {
			post["provider_sync"] = providerSync
		}
		return post, err
	}

	// Two execution paths:
	//   - schedule_at empty → publish inline now.
	//   - schedule_at set → schedule a job via the jobs app (when bound)
	//     so the publish is durable. If jobs isn't bound, fall back to
	//     the app's minute worker publishes it at the requested time.
	if mode == postModePublish {
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
		"mode":         mode,
		"revision":     1,
		"target_count": len(acctIDs),
	}
	if mode == postModePublish {
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
		    AND p.status IN ('scheduled','publishing','published','partial','failed','awaiting_audience_setup')
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
	deliveryAttempt   int
	platform, extID   string
	providerSlug      string
	providerAccountID string
	providerPostID    string
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
	pageCreds  string
	tiktokInfo map[string]any
}

// publishPostTargets walks every pending target on a post and tries to
// publish it. Each target runs through the platform-specific strategy
// (single, instagram_two_step, tiktok, …). Media URLs are resolved up
// front via storage.files_get_url so each strategy gets a flat list.
func (a *App) publishPostTargets(ctx *sdk.AppCtx, postID int64) {
	a.publishSelectedPostTargets(ctx, postID, 0)
}
func (a *App) publishSelectedPostTargets(ctx *sdk.AppCtx, postID, selectedTargetID int64) {
	// A queued post must not revive an account the user disconnected
	// after scheduling. Mark those destinations terminal before loading
	// publish jobs so Jobs can finish cleanly instead of retrying forever.
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='failed', last_error='social account is not active',
		        failure_code='account_inactive', retryable=0
		  WHERE post_id=? AND status='pending' AND (?=0 OR id=?)
		    AND EXISTS (
		      SELECT 1 FROM social_accounts a
		       WHERE a.id=post_targets.social_account_id AND a.status!='active'
		    )`,
		postID, selectedTargetID, selectedTargetID,
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
		        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,''),
		        COALESCE(t.provider_post_id,'')
		 FROM post_targets t
		 JOIN social_accounts a ON a.id=t.social_account_id
		 JOIN posts p ON p.id=t.post_id
		 WHERE t.post_id=? AND t.status='pending' AND (?=0 OR t.id=?)`,
		postID, selectedTargetID, selectedTargetID,
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
		if err := rows.Scan(&j.targetID, &acctID, &j.platform, &j.connID, &j.extID, &postBody, &j.pageCreds, &optsRaw, &j.providerSlug, &j.providerAccountID, &j.providerPostID); err != nil {
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
		if err := ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, j.targetID).Scan(&j.deliveryAttempt); err != nil {
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
			platformURL = safePublicationURL(j.platform, platformURL)
			var pending *providerPendingError
			if errors.As(err, &pending) {
				continue
			}
			if err != nil {
				a.markTargetError(ctx, j.targetID, err)
				failures++
				continue
			}
			_, _ = ctx.AppDB().Exec(
				`UPDATE post_targets SET status='published', platform_post_id=?, platform_url=?, delivery_recorded_at=CURRENT_TIMESTAMP, last_error=NULL WHERE id=?`,
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
		if j.platform == "tiktok" {
			var current int
			if checkErr := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE id=? AND attempts=? AND status='publishing'`, j.targetID, j.deliveryAttempt).Scan(&current); checkErr != nil || current != 1 {
				continue
			}
		}
		if err != nil {
			var awaiting *awaitingAudienceError
			if errors.As(err, &awaiting) && platformPostID != "" {
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET status='awaiting_audience_setup',audience_status='awaiting_setup',platform_post_id=?,platform_url=?,published_at=NULL,last_error=?,retryable=0 WHERE id=? AND attempts=? AND status='publishing'`, platformPostID, platformURL, awaiting.Error(), j.targetID, j.deliveryAttempt)
				ctx.Emit("target.awaiting_audience_setup", map[string]any{"target_id": j.targetID, "platform": j.platform, "platform_post_id": platformPostID})
				continue
			}
			var warning *publishedWarningError
			if errors.As(err, &warning) && (platformPostID != "" || warning.operationID != "") {
				if warning.pending {
					_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id=?,last_error=?,retryable=0,identity_resolve_after=datetime('now','+5 minutes') WHERE id=? AND attempts=? AND status='publishing'`, warning.operationID, warning.Error(), j.targetID, j.deliveryAttempt)
					ctx.Emit("target.pending", map[string]any{"target_id": j.targetID, "platform": j.platform, "reason": warning.Error()})
					continue
				}

				_, _ = ctx.AppDB().Exec(
					`UPDATE post_targets
					    SET status='published', platform_post_id=?, platform_url=?,
					        publish_operation_id=?, identity_resolve_attempts=0,
					        identity_resolve_after=CASE WHEN ?!='' THEN datetime('now','+5 minutes') ELSE NULL END,
					        delivery_recorded_at=CURRENT_TIMESTAMP, last_error=?
					  WHERE id=? AND attempts=? AND status='publishing'`,
					nullable(platformPostID), nullable(platformURL), warning.operationID,
					warning.operationID, warning.Error(), j.targetID, j.deliveryAttempt,
				)
				ctx.Emit("target.published_warning", map[string]any{
					"target_id": j.targetID,
					"platform":  j.platform,
					"warning":   warning.Error(),
				})
				successes++
				continue
			}
			a.markTargetError(ctx, j.targetID, err, j.deliveryAttempt)
			failures++
			continue
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE post_targets
			    SET status='published', platform_post_id=?, platform_url=?,
			        publish_operation_id='', identity_resolve_after=NULL,
			        delivery_recorded_at=CURRENT_TIMESTAMP, last_error=NULL
			  WHERE id=? AND attempts=? AND status='publishing'`,
			nullable(platformPostID), nullable(platformURL), j.targetID, j.deliveryAttempt,
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

type publishedWarningError struct {
	pending     bool
	warning     string
	operationID string
}

func (e *publishedWarningError) Error() string { return e.warning }

func claimPostTarget(ctx *sdk.AppCtx, targetID int64) (bool, error) {
	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='publishing',submitted_at=NULL,provider_accepted_at=NULL,delivery_recorded_at=CURRENT_TIMESTAMP, delivery_revision=(SELECT revision FROM posts WHERE id=post_id), attempts=attempts+1, last_attempt_at=CURRENT_TIMESTAMP
		  WHERE id=? AND status='pending' AND EXISTS (SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.status IN ('publishing','scheduled','failed','partial','awaiting_audience_setup') AND (p.approval_required=0 OR p.approved_revision=p.revision))`,
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
	total, published, failed, active, scheduled, drafts, unknown, awaiting := 0, 0, 0, 0, 0, 0, 0, 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			continue
		}
		total++
		switch status {
		case "published":
			published++
		case "awaiting_audience_setup":
			awaiting++
		case "failed":
			failed++
		case "pending", "publishing":
			active++
		case "scheduled":
			scheduled++
		case "draft", "in_review", "approved", "rejected":
			drafts++
		default:
			unknown++
		}
	}
	if total == 0 {
		return ""
	}
	if active > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='publishing' WHERE id=? AND status NOT IN ('scheduled')`, postID)
		return "publishing"
	}
	// A provider-imported scheduled/draft target is not a successful
	// publication. Preserve its lifecycle and never manufacture a parent
	// publish timestamp merely because no target is pending.
	if scheduled > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='scheduled', published_at=NULL WHERE id=?`, postID)
		return "scheduled"
	}
	if drafts > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='draft', published_at=NULL WHERE id=?`, postID)
		return "draft"
	}
	if awaiting > 0 {
		_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='awaiting_audience_setup',published_at=NULL WHERE id=?`, postID)
		return "awaiting_audience_setup"
	}
	if unknown > 0 || published+failed != total {
		ctx.Logger().Warn("rollup post status: non-terminal target status", "post", postID, "unknown", unknown)
		return ""
	}
	finalStatus := "published"
	if failed > 0 && published == 0 {
		finalStatus = "failed"
	} else if failed > 0 {
		finalStatus = "partial"
	}
	if finalStatus == "published" || finalStatus == "partial" {
		_, _ = ctx.AppDB().Exec(
			`UPDATE posts SET status=?, delivery_recorded_at=COALESCE(delivery_recorded_at,CURRENT_TIMESTAMP),published_at=(SELECT CASE WHEN COUNT(*)=COUNT(t.published_at) THEN MIN(t.published_at) ELSE NULL END FROM post_targets t WHERE t.post_id=posts.id AND t.status='published') WHERE id=?`,
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
	id, link, err := a.runStrategyRaw(ctx, def, j)
	return id, safePublicationURL(j.platform, link), err
}
func (a *App) runStrategyRaw(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	unlock := lockSocialResource(ctx, j.connID, "delivery-connection")
	defer unlock()
	audience, err := parseAudience(j.options)
	if err != nil {
		return "", "", err
	}
	if audience.Type == "members" {
		adapter := adapterForAudience(j.platform, j.providerSlug)
		if adapter.Publish == nil {
			return "", "", errors.New("membership publishing is unsupported by this destination")
		}
		return adapter.Publish(a, ctx, def, j, audience)
	}
	switch def.Strategy {
	case "twitter":
		return a.publishTwitter(ctx, def, j)
	case "instagram_two_step":
		return a.publishInstagram(ctx, def, j)
	case "tiktok":
		return a.publishTikTok(ctx, def, j)
	case "youtube":
		return a.publishYoutube(ctx, def, j)
	case "pinterest":
		return a.publishPinterest(ctx, def, j)
	default: // "single" or empty
		return a.publishSingle(ctx, def, j)
	}
}

const pinterestVideoMaxBytes int64 = 2 * 1024 * 1024 * 1024

// publishPinterest maps Social's generic post and media model onto
// Pinterest's nested media_source contract. A connected Pinterest account
// publishes to the frozen target destination, with legacy board presets supported.
func (a *App) publishPinterest(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	boardID, err := resolvePinterestBoard(j)
	if err != nil {
		return "", "", err
	}
	if len(j.media) == 0 {
		return "", "", errors.New("pinterest requires at least one image or video")
	}
	if utf8.RuneCountInString(j.body) > 800 {
		return "", "", errors.New("pinterest description exceeds 800 characters")
	}
	title := strOption(j.options, "title")
	if utf8.RuneCountInString(title) > 100 {
		return "", "", errors.New("pinterest title exceeds 100 characters")
	}
	altText := strOption(j.options, "alt_text")
	if utf8.RuneCountInString(altText) > 500 {
		return "", "", errors.New("pinterest alt text exceeds 500 characters")
	}
	link := strOption(j.options, "link")
	if link != "" {
		parsed, err := url.Parse(link)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", "", errors.New("pinterest destination link must be an absolute HTTP(S) URL")
		}
	}

	images := make([]mediaItem, 0, len(j.media))
	videos := make([]mediaItem, 0, 1)
	for _, item := range j.media {
		switch {
		case item.IsImage():
			images = append(images, item)
		case item.IsVideo():
			videos = append(videos, item)
		default:
			return "", "", fmt.Errorf("pinterest does not support media type %q", item.Mime)
		}
	}
	if len(videos) > 1 || (len(videos) == 1 && len(images) > 1) {
		return "", "", errors.New("pinterest supports one video with at most one attached cover image")
	}
	if len(videos) == 0 && len(images) > 5 {
		return "", "", errors.New("pinterest supports at most 5 images per Pin")
	}

	var mediaSource map[string]any
	if len(videos) == 1 {
		coverURL := ""
		thumb, err := a.resolveThumbnailOption(ctx, j.options, j.mediaProjectID)
		if err != nil {
			return "", "", err
		}
		if thumb != nil {
			coverURL = thumb.URL
		} else if len(images) == 1 {
			coverURL = images[0].URL
		}
		if coverURL == "" {
			return "", "", errors.New("pinterest video Pins require thumbnail_storage_id or one attached image as the cover")
		}
		mediaID, err := a.uploadPinterestVideo(ctx, j.connID, videos[0])
		if err != nil {
			return "", "", err
		}
		mediaSource = map[string]any{
			"source_type":     "video_id",
			"media_id":        mediaID,
			"cover_image_url": coverURL,
		}
	} else if len(images) == 1 {
		mediaSource = map[string]any{"source_type": "image_url", "url": images[0].URL}
	} else {
		items := make([]map[string]any, 0, len(images))
		for _, image := range images {
			items = append(items, map[string]any{"url": image.URL})
		}
		mediaSource = map[string]any{"source_type": "multiple_image_urls", "items": items}
	}

	input := map[string]any{
		def.ExternalIDField: boardID,
		def.BodyField:       j.body,
		"media_source":      mediaSource,
	}
	for key, value := range map[string]string{
		"title": title, "link": link, "alt_text": altText,
		"board_section_id": strOption(j.options, "board_section_id"),
	} {
		if value != "" {
			input[key] = value
		}
	}
	res, err := executePublicationWrite(ctx, j, def.PostTool, input)
	if err != nil {
		return "", "", fmt.Errorf("create_pin: %w", err)
	}
	if res == nil || !res.Success {
		return "", "", upstreamError(res)
	}
	id, platformURL := extractPostIdentity("pinterest", res.Data)
	return id, platformURL, nil
}

type pinterestMediaRegistration struct {
	MediaID          string         `json:"media_id"`
	UploadURL        string         `json:"upload_url"`
	UploadParameters map[string]any `json:"upload_parameters"`
}

func (a *App) uploadPinterestVideo(ctx *sdk.AppCtx, connID int64, item mediaItem) (string, error) {
	if item.Bytes > pinterestVideoMaxBytes {
		return "", fmt.Errorf("pinterest video is too large: %d bytes (max 2 GB)", item.Bytes)
	}
	registered, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "register_media_upload", map[string]any{"media_type": "video"})
	if err != nil {
		return "", fmt.Errorf("register pinterest media: %w", err)
	}
	if registered == nil || !registered.Success {
		return "", fmt.Errorf("register pinterest media: %w", upstreamError(registered))
	}
	var upload pinterestMediaRegistration
	if err := json.Unmarshal(registered.Data, &upload); err != nil {
		return "", fmt.Errorf("decode pinterest media registration: %w", err)
	}
	if upload.MediaID == "" || upload.UploadURL == "" {
		return "", errors.New("pinterest media registration returned no media_id or upload_url")
	}
	if err := postPinterestMultipartUpload(upload.UploadURL, upload.UploadParameters, item); err != nil {
		return "", err
	}
	for attempt := 0; attempt < 30; attempt++ {
		statusRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_media_upload", map[string]any{"media_id": upload.MediaID})
		if err != nil {
			return "", fmt.Errorf("check pinterest media: %w", err)
		}
		if statusRes == nil || !statusRes.Success {
			return "", fmt.Errorf("check pinterest media: %w", upstreamError(statusRes))
		}
		var status struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(statusRes.Data, &status)
		switch strings.ToLower(status.Status) {
		case "succeeded":
			return upload.MediaID, nil
		case "failed":
			return "", errors.New("pinterest rejected the uploaded video while processing it")
		}
		time.Sleep(2 * time.Second)
	}
	return "", errors.New("pinterest video processing did not finish within 60 seconds")
}

func pinterestUploadURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && strings.EqualFold(u.Hostname(), "pinterest-media-upload.s3-accelerate.amazonaws.com")
}

// postPinterestMultipartUpload writes the form to a temporary file so the S3
// upload has an exact Content-Length without holding a potentially large video
// in memory. Pinterest's pre-authorized upload never receives OAuth headers.
func postPinterestMultipartUpload(uploadURL string, fields map[string]any, item mediaItem) error {
	if !pinterestUploadURLAllowed(uploadURL) {
		return errors.New("pinterest returned an untrusted media upload URL")
	}
	tmp, err := os.CreateTemp("", "social-pinterest-upload-*")
	if err != nil {
		return fmt.Errorf("create pinterest upload buffer: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	defer tmp.Close()

	mw := multipart.NewWriter(tmp)
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if err := mw.WriteField(key, toString(fields[key])); err != nil {
			return fmt.Errorf("write pinterest upload field: %w", err)
		}
	}
	filename := item.Name
	if filename == "" {
		filename = "video"
	}
	part, err := mw.CreateFormFile("file", filepath.Base(filename))
	if err != nil {
		return fmt.Errorf("create pinterest video form part: %w", err)
	}
	mediaReq, err := http.NewRequest(http.MethodGet, item.URL, nil)
	if err != nil {
		return fmt.Errorf("build pinterest media GET: %w", err)
	}
	mediaRes, err := mediaHTTPClient.Do(mediaReq)
	if err != nil {
		return fmt.Errorf("fetch pinterest video from storage: %w", err)
	}
	defer mediaRes.Body.Close()
	if mediaRes.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch pinterest video: storage returned %d", mediaRes.StatusCode)
	}
	written, err := io.Copy(part, io.LimitReader(mediaRes.Body, pinterestVideoMaxBytes+1))
	if err != nil {
		return fmt.Errorf("buffer pinterest video: %w", err)
	}
	if written > pinterestVideoMaxBytes {
		return errors.New("pinterest video exceeds 2 GB")
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("finish pinterest upload form: %w", err)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind pinterest upload form: %w", err)
	}
	stat, err := tmp.Stat()
	if err != nil {
		return fmt.Errorf("size pinterest upload form: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, uploadURL, tmp)
	if err != nil {
		return fmt.Errorf("build pinterest upload POST: %w", err)
	}
	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", mw.FormDataContentType())
	res, err := mediaHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("upload pinterest video: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return fmt.Errorf("upload pinterest video: S3 returned %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// publishSingle covers Twitter / Facebook / LinkedIn — a single
// integration tool call with a flat input shape. Switches between
// the platform's image PostTool and VideoPostTool based on the first
// media item's MIME (Facebook splits POST /feed for photos and POST
// /videos for video; same token, different endpoint).
func (a *App) publishSingle(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) > 1 {
		return "", "", errors.New("this native destination accepts one media item per post; split the post or use a provider supporting albums")
	}
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
	if isVideo && mediaField != "" {
		prepared, err := a.prepareVideoSource(ctx, j, j.media[0], "PULL_FROM_URL")
		if err != nil {
			return "", "", err
		}
		defer prepared.Close()
		j.media = append([]mediaItem(nil), j.media...)
		j.media[0].URL = prepared.URL
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
	if isVideo {
		d := loadDeliveryDiagnostics(ctx, j.targetID)
		d.OperationState = "unknown"
		if err := validatePreparedDestination(ctx, j); err != nil {
			return "", "", err
		}
		if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "provider_fetch_requested"}); err != nil {
			return "", "", errors.New("could not persist upload initialization guard")
		}
	}
	out, err := executePublicationWrite(ctx, j, tool, input)
	if err != nil {
		ctx.Logger().Error("publishSingle: ExecuteIntegrationTool err",
			"platform", def.Platform, "tool", tool, "err", redactDeliverySecrets(err.Error()))
		return "", "", err
	}
	if out == nil || !out.Success {
		ue := upstreamError(out)
		ctx.Logger().Error("publishSingle: upstream non-2xx",
			"platform", def.Platform, "tool", tool, "err", redactDeliverySecrets(ue.Error()))
		if isVideo {
			d := loadDeliveryDiagnostics(ctx, j.targetID)
			if out != nil && out.Status >= 400 && out.Status < 500 {
				d.OperationState = "confirmed_failure"
			}
			captureProviderDiagnostics(&d, out)
			d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", HTTPStatus: executeStatus(out), Result: "provider_rejected"})
		}
		return "", "", ue
	}
	id, url := extractPostIdentity(def.Platform, out.Data)
	if isVideo && id == "" {
		return "", "", errors.New("provider response has no video identity; verify the remote outcome before retrying")
	}
	if isVideo {
		d := loadDeliveryDiagnostics(ctx, j.targetID)
		d.OperationID = id
		if id != "" {
			d.OperationState = "published"
		}
		d.event(ctx, j.targetID, deliveryEvent{Stage: "processing", Result: "provider_response_received"})
	}
	ctx.Logger().Info("publishSingle: published",
		"platform", def.Platform, "platform_post_id", id, "platform_url", url)
	return id, url, nil
}

// publishTwitter uploads attached media through X's media API first,
// then creates the post with media.media_ids. Text-only posts still use
// the simple post_tweet path. X accepts up to 4 images, or one video/GIF.
func (a *App) publishTwitter(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	allowLongform, _ := boolOption(j.options, "allow_longform")
	if weighted := twitterWeightedTextLength(j.body); weighted > 280 && !allowLongform {
		return "", "", fmt.Errorf(
			"X post is %d weighted characters; the standard limit is 280. Shorten it or set targets[].allow_longform=true for an account with long-form API access",
			weighted,
		)
	}
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
	out, err := executePublicationWrite(ctx, j, def.PostTool, input)
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

var twitterURLPattern = regexp.MustCompile(`https?://[^\s]+`)

func twitterWeightedTextLength(text string) int {
	weighted := 0
	offset := 0
	for _, match := range twitterURLPattern.FindAllStringIndex(text, -1) {
		weighted += twitterWeightedRunes(text[offset:match[0]])
		weighted += 23
		offset = match[1]
	}
	return weighted + twitterWeightedRunes(text[offset:])
}

func twitterWeightedRunes(text string) int {
	weighted := 0
	for _, r := range text {
		switch {
		case r <= 0x10FF,
			r >= 0x2000 && r <= 0x200D,
			r >= 0x2010 && r <= 0x201F,
			r >= 0x2032 && r <= 0x2037:
			weighted++
		default:
			weighted += 2
		}
	}
	return weighted
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
	getResp, err := mediaHTTPClient.Do(getReq)
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
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch media bytes from storage: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch media: storage returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err == nil && int64(len(body)) > limit {
		return nil, fmt.Errorf("media exceeds %d bytes", limit)
	}
	return body, err
}

// publishInstagram runs the two-step IG dance: create_media_container
// (with imageUrl OR videoUrl+REELS + caption) → publish_media_container
// (with the containerId returned by step 1). IG Business writes need
// the page-level access_token same as Facebook.
func (a *App) publishInstagram(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) > 1 {
		return "", "", errors.New("this native destination accepts one media item per post; split the post or use a provider supporting albums")
	}
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
	out, err := executePublicationWrite(ctx, j, def.PostTool, containerInput)
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
	out2, err := executePublicationWrite(ctx, j, def.PublishTool, publishInput)
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
	if len(j.media) > 1 {
		return "", "", errors.New("this native destination accepts one media item per post; split the post or use a provider supporting albums")
	}
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
	//   release_at  — status.publishAt; uploads privately and YouTube
	//                 releases it at that time. Overrides visibility.
	//   contains_synthetic_media — status.containsSyntheticMedia; sent
	//                 only when the creator set it, so unset stays unset.
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
	releaseAt, err := youtubeReleaseAt(j.options)
	if err != nil {
		return "", "", err
	}
	if releaseAt != "" {
		if at, _ := time.Parse(time.RFC3339, releaseAt); !at.After(time.Now()) {
			return "", "", fmt.Errorf("YouTube release time %s has already passed; set a later release_at", releaseAt)
		}
		visibility = "private" // YouTube only schedules private videos.
	}
	snippet := map[string]any{
		"title":       title,
		"description": j.body,
	}
	category := strOption(j.options, "category")
	if category == "" {
		category = "22"
	} // People & Blogs: YouTube insert requires categoryId.
	snippet["categoryId"] = category
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
	status := map[string]any{
		"privacyStatus":           visibility,
		"selfDeclaredMadeForKids": false,
	}
	if releaseAt != "" {
		status["publishAt"] = releaseAt
	}
	if synthetic, ok := boolOption(j.options, "contains_synthetic_media"); ok {
		status["containsSyntheticMedia"] = synthetic
	}
	initInput := map[string]any{
		"snippet": snippet,
		"status":  status,
	}
	prepared, err := a.prepareVideoSource(ctx, j, first, "FILE_UPLOAD")
	if err != nil {
		return "", "", err
	}
	defer prepared.Close()
	ctx.Logger().Info("publishYoutube: prepared source", "file_id", first.ID, "correlation_id", prepared.Diagnostics.CorrelationID, "bytes", prepared.Size)
	initInput["upload_content_type"] = prepared.MIME
	initInput["upload_content_length"] = prepared.Size
	videoID, err := a.uploadPreparedYouTube(ctx, def, j, prepared, initInput)
	if err != nil {
		return "", "", err
	}
	resource := struct{ ID string }{ID: videoID}
	// Canonical watch URL derived from the video ID YouTube returned for this
	// channel's upload. It does not prove public or Members access.
	url := youtubeWatchURL(videoID)
	if thumbnail != nil {
		if err := a.setBinaryThumbnail(ctx, def, j.connID, resource.ID, *thumbnail); err != nil {
			// The video is already uploaded at this point. Do not return
			// an error that would make post_retry upload a duplicate video;
			// surface this in logs so the user can edit the post and retry
			// only the thumbnail later.
			ctx.Logger().Warn("publishYoutube: thumbnail set failed",
				"video_id", resource.ID, "thumbnail_id", thumbnail.ID, "err", redactDeliverySecrets(err.Error()))
			return resource.ID, url, &publishedWarningError{warning: "video published, but custom thumbnail failed: " + redactDeliverySecrets(err.Error())}
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
	info, err := tikTokPostInfo(ctx, j)
	if err != nil {
		return "", "", err
	}
	j.tiktokInfo = info
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
	if status, err := sdk.GetIntegrationURLProperty(ctx.PlatformAPI(), j.connID, "content_delivery"); err == nil && !status.Ready {
		return "", "", errors.New("TikTok media delivery is not ready; upload TikTok's verification file and confirm the URL prefix in Integrations")
	}
	if len(j.media) > 35 {
		return "", "", fmt.Errorf("tiktok photo posts accept at most 35 images, got %d", len(j.media))
	}
	for i, item := range j.media {
		if !item.IsImage() {
			return "", "", fmt.Errorf("tiktok photo posts cannot mix images with %q media at index %d", item.Mime, i)
		}
	}
	if err := persistImageDeliveryDiagnostics(ctx, j.targetID, []imageDeliveryDiagnostics{}); err != nil {
		return "", "", err
	}
	images := make([]string, 0, len(j.media))
	diagnostics := make([]imageDeliveryDiagnostics, 0, len(j.media))
	for i, item := range j.media {
		prepared, diagnostic, err := a.prepareImageDelivery(ctx, item, j.mediaProjectID, tikTokPhotoPolicy)
		if err == nil {
			var verified imageDeliveryDiagnostics
			var proxyURL string
			proxyURL, verified, err = a.resolveImageDeliveryURL(ctx, prepared, j.mediaProjectID, tikTokPhotoPolicy, diagnostic.DeliverySHA)
			diagnostic.HTTPStatus = verified.HTTPStatus
			diagnostic.DeliveryPath = verified.DeliveryPath
			diagnostic.Validation = verified.Validation
			if err == nil {
				images = append(images, proxyURL)
			}
		} else {
			diagnostic.Validation = "failed"
		}
		diagnostics = append(diagnostics, diagnostic)
		if persistErr := persistImageDeliveryDiagnostics(ctx, j.targetID, diagnostics); persistErr != nil {
			return "", "", persistErr
		}
		if err != nil {
			return "", "", fmt.Errorf("tiktok photo %d: %w", i, err)
		}
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
		"privacy_level":        j.tiktokInfo["privacy_level"],
		"brand_content_toggle": false,
		"brand_organic_toggle": false,
	}
	for k, v := range j.tiktokInfo {
		postInfo[k] = v
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
	out, err := executePublicationWrite(ctx, j, "post_photo", input)
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
	if err := persistPublishOperation(ctx, j.targetID, pubID); err != nil {
		return "", "", &publishedWarningError{warning: err.Error(), operationID: pubID, pending: true}
	}
	return a.waitTikTokPublish(ctx, j.connID, pubID)
}

const tiktokPhotoURLTTLSeconds = 2 * 60 * 60

// Retained as a small provider adapter; normalization lives in the reusable pipeline.
func (a *App) prepareTikTokPhoto(ctx *sdk.AppCtx, item mediaItem, projectID string) (mediaItem, error) {
	prepared, _, err := a.prepareImageDelivery(ctx, item, projectID, tikTokPhotoPolicy)
	return prepared, err
}
func (a *App) resolveTikTokPhotoURL(ctx *sdk.AppCtx, item mediaItem, projectID string) (string, error) {
	url, _, err := a.resolveImageDeliveryURL(ctx, item, projectID, tikTokPhotoPolicy, "")
	return url, err
}
func preflightTikTokPhotoURL(mediaURL string) error {
	_, err := preflightImageDeliveryURL(mediaURL, tikTokPhotoPolicy, "")
	return err
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
// no such requirement and works from a fresh OAuth grant.
func (a *App) publishTikTokVideo(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.media) > 1 {
		return "", "", errors.New("this native destination accepts one media item per post; split the post or use a provider supporting albums")
	}
	if len(j.media) == 0 {
		return "", "", errors.New("tiktok requires a video")
	}
	first := j.media[0]
	if !first.IsVideo() {
		return "", "", errors.New("tiktok only accepts video files")
	}
	// Validate and upload the exact bytes locally. The server rewrites pull URLs
	// after Social submits them, so that route cannot be preflighted here.
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

	prepared, err := a.prepareVideoSource(ctx, j, first, "FILE_UPLOAD")
	if err != nil {
		return "", "", err
	}
	defer prepared.Close()
	if prepared.Size != first.Bytes {
		return "", "", &sourceDeliveryError{"video size differs from upload metadata", false}
	}
	d := prepared.Diagnostics
	d.ProgressVersion = 1
	d.AttemptedBytes = bytePointer(0)
	d.AcknowledgedBytes = bytePointer(0)
	d.TransferredBytes = 0
	d.Initialization = &deliveryOutcome{StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Result: "request_started"}
	d.FirstUploadError = nil
	d.LatestUploadError = nil
	d.LastUpload = nil
	d.UploadAcknowledgementResolution = nil
	d.ProviderObservation = nil
	d.LastProgressAt = ""
	d.LocalDiagnostic = ""
	d.AutomaticPollAttempts = 0
	d.AutomaticPollingStopped = false
	d.NextPollAt = ""
	d.OperationState = "unknown"
	if err := validatePreparedDestination(ctx, j); err != nil {
		return "", "", err
	}
	if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "request_started"}); err != nil {
		return "", "", errors.New("could not persist upload initialization guard")
	}
	// Step 1: init upload via the integration to get upload_url +
	// publish_id. The integration handles auth + URL building; we just
	// pass the post_info / source_info shapes TikTok expects.
	postInfo := map[string]any{
		"title":         j.body,
		"privacy_level": j.tiktokInfo["privacy_level"],
	}
	for k, v := range j.tiktokInfo {
		postInfo[k] = v
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
	out, err := executePublicationWrite(ctx, j, def.PostTool, initInput)
	d.Initialization.At = time.Now().UTC().Format(time.RFC3339Nano)
	d.Initialization.HTTPStatus = executeStatus(out)
	captureProviderDiagnostics(&d, out)
	d.Initialization.RequestID = d.ProviderRequestID
	if out != nil {
		d.Initialization.ResponseExcerpt = deliveryExcerpt(out.Data)
	}

	if err != nil {
		d.Initialization.Result = "outcome_unknown"
		d.Initialization.ResponseExcerpt = deliveryExcerpt([]byte(err.Error()))
		_ = d.save(ctx, j.targetID)
		return "", "", fmt.Errorf("TikTok init outcome unknown; verify the remote outcome: %s", redactDeliverySecrets(err.Error()))
	}
	if out == nil || !out.Success {
		if out != nil && out.Status >= 400 && out.Status < 500 {
			d.OperationState = "not_started"
		}
		d.Initialization.Result = "provider_rejected"
		captureProviderDiagnostics(&d, out)
		d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", HTTPStatus: executeStatus(out), Result: "provider_rejected"})
		return "", "", upstreamError(out)
	}
	uploadURL, publishID := extractTikTokUploadInit(out.Data)
	if uploadURL == "" || publishID == "" {
		d.Initialization.Result = "invalid_response"
		_ = d.save(ctx, j.targetID)
		return "", "", errors.New("post_video init: missing upload_url or publish_id; verify the remote outcome before retrying")
	}
	if err := persistTikTokUploadOperation(ctx, j.targetID, &d, publishID); err != nil {
		return "", "", &publishedWarningError{warning: err.Error(), operationID: publishID, pending: true}
	}
	d.OperationID = publishID
	d.boundOperation = publishID
	d.Initialization.Result = "operation_persisted"
	d.UploadURLSHA256 = uploadURLHash(uploadURL)
	d.OperationState = "pending"
	captureProviderDiagnostics(&d, out)
	if err := d.event(ctx, j.targetID, deliveryEvent{Stage: "initialization", Result: "operation_persisted"}); err != nil {
		return "", "", &publishedWarningError{pending: true, operationID: publishID, warning: "could not persist upload receipt; upload not started"}
	}
	ctx.Logger().Info("publishTikTok: init done",
		"publish_id", publishID, "upload_url_len", len(uploadURL))

	if err := guardedTikTokUpload(ctx, j.targetID, prepared, &d, uploadURL, chunkSize, totalChunks); err != nil {
		return "", "", &publishedWarningError{pending: true, operationID: publishID, warning: err.Error() + "; inspect existing operation before recovery"}
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
	delay := 2 * time.Second
	for {
		status, failReason, postIDs, err := a.getTikTokPublishStatus(ctx, connID, publishID)
		if err != nil {
			message := "TikTok accepted the upload, but publish status could not be verified"
			message += ": " + err.Error()
			return "", "", &publishedWarningError{warning: message, operationID: publishID, pending: true}
		}
		switch status {
		case "PUBLISH_COMPLETE":
			if len(postIDs) > 0 && postIDs[0] != "" {
				return postIDs[0], "", nil
			}
			return "", "", &publishedWarningError{
				warning:     "TikTok published the upload, but its public post ID is not available yet",
				operationID: publishID,
			}
		case "FAILED":
			return "", "", &upstreamCallError{Status: 422, Body: "TikTok publish failed: " + safeRequestID(failReason)}
		case "PROCESSING_UPLOAD", "PROCESSING_DOWNLOAD":
			// Keep polling below.
		default:
			return "", "", &publishedWarningError{
				pending:     true,
				warning:     "TikTok accepted the upload, but returned unexpected publish status " + strconv.Quote(status),
				operationID: publishID,
			}
		}
		if time.Now().After(deadline) {
			return "", "", &publishedWarningError{
				pending:     true,
				warning:     "TikTok accepted the upload, but it was still processing after 5 minutes",
				operationID: publishID,
			}
		}
		pause := time.Until(deadline)
		if pause > delay {
			pause = delay
		}
		if pause > 0 {
			time.Sleep(pause)
		}
		if delay < 30*time.Second {
			delay *= 2
		}
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
	}
}

type exactJSONID string

func (id *exactJSONID) UnmarshalJSON(raw []byte) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		*id = ""
		return nil
	}
	if raw[0] == '"' {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
		*id = exactJSONID(value)
		return nil
	}
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return fmt.Errorf("invalid numeric id %q", raw)
		}
	}
	*id = exactJSONID(string(raw))
	return nil
}

func (a *App) getTikTokPublishStatus(ctx *sdk.AppCtx, connID int64, publishID string) (string, string, []string, error) {
	bindings := tikTokStatusBindings(ctx, connID, publishID)
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_publish_status", map[string]any{"publish_id": publishID})
	var payload struct {
		Data struct {
			Status           string        `json:"status"`
			Fail             string        `json:"fail_reason"`
			PostIDs          []exactJSONID `json:"publicaly_available_post_id"`
			AlternatePostIDs []exactJSONID `json:"publicly_available_post_id"`
			PostID           exactJSONID   `json:"post_id"`
			UploadedBytes    *int64        `json:"uploaded_bytes"`
			DownloadedBytes  *int64        `json:"downloaded_bytes"`
		} `json:"data"`
		Error struct {
			Code  string `json:"code"`
			LogID string `json:"log_id"`
		} `json:"error"`
	}
	var parseErr error
	if res != nil {
		parseErr = json.Unmarshal(res.Data, &payload)
	}
	payload.Data.PostIDs = append(payload.Data.PostIDs, payload.Data.AlternatePostIDs...)
	if payload.Data.PostID != "" {
		payload.Data.PostIDs = append(payload.Data.PostIDs, payload.Data.PostID)
	}
	observation := tikTokProgressObservation{At: time.Now().UTC().Format(time.RFC3339Nano), Status: safeRequestID(payload.Data.Status), HTTPStatus: executeStatus(res), ErrorCode: safeRequestID(payload.Error.Code), RequestID: safeRequestID(payload.Error.LogID), UploadedBytes: payload.Data.UploadedBytes, DownloadedBytes: payload.Data.DownloadedBytes}
	if (payload.Data.UploadedBytes != nil && *payload.Data.UploadedBytes < 0) || (payload.Data.DownloadedBytes != nil && *payload.Data.DownloadedBytes < 0) {
		parseErr = errors.New("negative TikTok progress")
		observation.UploadedBytes = nil
		observation.DownloadedBytes = nil
	}
	observation.QuerySucceeded = parseErr == nil && err == nil && res != nil && res.Success && res.Status >= 200 && res.Status < 300 && (payload.Error.Code == "" || payload.Error.Code == "ok") && payload.Data.Status != ""
	if parseErr != nil {
		a.recordTikTokStatus(ctx, bindings, publishID, "", "", "", "", res, parseErr, observation)
		return "", "", nil, errors.New("TikTok returned malformed operation status; outcome is unknown")
	}
	if payload.Data.Status != "PUBLISH_COMPLETE" && len(payload.Data.PostIDs) > 0 {
		observation.QuerySucceeded = false
		conflict := errors.New("TikTok returned a public identity with a non-complete status; outcome is unknown")
		a.recordTikTokStatus(ctx, bindings, publishID, payload.Data.Status, payload.Data.Fail, payload.Error.Code, payload.Error.LogID, res, conflict, observation)
		return "", "", nil, conflict
	}
	a.recordTikTokStatus(ctx, bindings, publishID, payload.Data.Status, payload.Data.Fail, payload.Error.Code, payload.Error.LogID, res, err, observation)
	if err != nil {
		return "", "", nil, errors.New(redactDeliverySecrets(err.Error()))
	}
	if res == nil || !res.Success {
		return "", "", nil, upstreamError(res)
	}
	if payload.Error.Code != "" && payload.Error.Code != "ok" {
		return "", "", nil, fmt.Errorf("TikTok status unavailable: %s (log %s)", safeRequestID(payload.Error.Code), safeRequestID(payload.Error.LogID))
	}
	if payload.Data.Status == "" {
		return "", "", nil, errors.New("TikTok status response contains no operation state")
	}
	postIDs := make([]string, 0, len(payload.Data.PostIDs))
	for _, id := range payload.Data.PostIDs {
		if id != "" {
			postIDs = append(postIDs, string(id))
		}
	}
	return payload.Data.Status, payload.Data.Fail, postIDs, nil
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
	SHA256           string
	StorageInstallID int64
	MediaProjectID   string
	Duration         float64
	ID               int64
	URL              string
	Mime             string
	Name             string
	Bytes            int64
}

// IsVideo reports whether this is a video MIME type.
func (m mediaItem) IsVideo() bool { return strings.HasPrefix(m.Mime, "video/") }

// IsImage reports whether this is an image MIME type.
func (m mediaItem) IsImage() bool { return strings.HasPrefix(m.Mime, "image/") }

func absoluteStorageURL(rawURL string) string {
	rawURL = strings.TrimSpace(rawURL)
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	publicBase := os.Getenv("APTEVA_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://127.0.0.1:5280"
	}
	publicBase = strings.TrimRight(publicBase, "/")
	if strings.HasPrefix(rawURL, "/api/apps/storage/") {
		return publicBase + rawURL
	}
	return publicBase + "/api/apps/storage" + rawURL
}

// resolveMedia turns storage file ids into absolute, publicly fetchable
// URLs paired with the file's content_type. Calls storage.files_get
// for the metadata and storage.files_get_url for the signed URL.
// The URL must be reachable from the social platform's servers — for
// local dev, point APTEVA_PUBLIC_URL at an ngrok tunnel.
func (a *App) resolveMedia(ctx *sdk.AppCtx, ids []int64, projectID string) ([]mediaItem, error) {
	if len(ids) == 0 {
		return nil, nil
	}
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
				SHA256      string  `json:"sha256"`
				ContentType string  `json:"content_type"`
				Name        string  `json:"name"`
				SizeBytes   int64   `json:"size_bytes"`
				Duration    float64 `json:"duration_seconds"`
			} `json:"file"`
			SHA256      string  `json:"sha256"`
			ContentType string  `json:"content_type"`
			Name        string  `json:"name"`
			SizeBytes   int64   `json:"size_bytes"`
			Duration    float64 `json:"duration_seconds"`
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
		name := meta.Name
		if name == "" {
			name = meta.File.Name
		}

		// Signed URL — separate call because files_get_url is the
		// canonical way to mint a TTL'd link.
		var signed struct {
			URL string `json:"url"`
		}
		urlArgs := map[string]any{
			"id":          id,
			"ttl_seconds": 3600,
			"delivery":    "apteva",
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
		fullURL := absoluteStorageURL(rel)
		ctx.Logger().Info("resolveMedia: item",
			"id", id, "mime", mime, "is_video", strings.HasPrefix(mime, "video/"))
		checksum := meta.SHA256
		if checksum == "" {
			checksum = meta.File.SHA256
		}
		storageID := int64(0)
		if bound := ctx.IntegrationFor("storage"); bound != nil {
			storageID = bound.InstallID
		}
		out = append(out, mediaItem{ID: id, URL: fullURL, Mime: mime, Name: name, Bytes: size, SHA256: checksum, StorageInstallID: storageID, MediaProjectID: projectID})
	}
	return out, nil
}

const (
	youtubeThumbnailMaxBytes       int64 = 2 * 1024 * 1024
	youtubeThumbnailSourceMaxBytes int64 = 10 * 1024 * 1024
	youtubeThumbnailMaxPixels            = 40_000_000
)

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

func validateYouTubeThumbnailMime(mime string) error {
	switch mime {
	case "", "image/jpeg", "image/jpg", "image/png", "application/octet-stream":
		return nil
	default:
		return fmt.Errorf("youtube thumbnail must be JPEG or PNG, got %q", mime)
	}
}

func prepareYouTubeThumbnail(body []byte, mime string) ([]byte, string, error) {
	if err := validateYouTubeThumbnailMime(mime); err != nil {
		return nil, "", err
	}
	if int64(len(body)) <= youtubeThumbnailMaxBytes {
		if mime == "" {
			mime = "image/jpeg"
		}
		return body, mime, nil
	}

	config, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("decode oversized youtube thumbnail: %w", err)
	}
	if config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > youtubeThumbnailMaxPixels {
		return nil, "", fmt.Errorf("youtube thumbnail dimensions are too large: %dx%d", config.Width, config.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("decode oversized youtube thumbnail: %w", err)
	}
	var optimized bytes.Buffer
	if err := jpeg.Encode(&optimized, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, "", fmt.Errorf("optimize youtube thumbnail: %w", err)
	}
	if int64(optimized.Len()) > youtubeThumbnailMaxBytes {
		return nil, "", fmt.Errorf("youtube thumbnail remains too large after optimization: %d bytes (max 2 MB)", optimized.Len())
	}
	return optimized.Bytes(), "image/jpeg", nil
}

func fetchYouTubeThumbnailEnvelope(url, mime string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch thumbnail: storage returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, youtubeThumbnailSourceMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > youtubeThumbnailSourceMaxBytes {
		return nil, fmt.Errorf("youtube thumbnail source exceeds %d bytes", youtubeThumbnailSourceMaxBytes)
	}
	body, mime, err = prepareYouTubeThumbnail(body, mime)
	if err != nil {
		return nil, err
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
	if err := validateYouTubeThumbnailMime(thumb.Mime); err != nil {
		return err
	}
	if thumb.Bytes > youtubeThumbnailSourceMaxBytes {
		return fmt.Errorf("youtube thumbnail source exceeds %d bytes", youtubeThumbnailSourceMaxBytes)
	}
	binaryField := def.ThumbnailBinaryField
	if binaryField == "" {
		binaryField = "image"
	}
	idField := def.ThumbnailIDField
	if idField == "" {
		idField = "videoId"
	}
	envelope, err := fetchYouTubeThumbnailEnvelope(thumb.URL, thumb.Mime)
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
	return &upstreamCallError{
		Status:         out.Status,
		Body:           string(out.Data),
		ExistingPostID: extractExistingPostID(out.Data),
	}
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
	var projectID string
	var generation int64
	if err := ctx.AppDB().QueryRow(`SELECT project_id, schedule_generation FROM posts WHERE id=?`, postID).Scan(&projectID, &generation); err != nil {
		return 0, err
	}
	ctx = ctx.WithProject(projectID)
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
			"input": map[string]any{"post_id": postID, "schedule_generation": generation},
		},
		"idempotency_key": fmt.Sprintf("social.post.%d.%d", postID, generation),
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
	a.markTargetFailure(ctx, targetID, msg, publishFailure{Code: "validation_error", Retryable: false})
}

func (a *App) markTargetError(ctx *sdk.AppCtx, targetID int64, err error, expectedAttempt ...int) {
	failure := classifyPublishFailure(err)
	d := loadDeliveryDiagnostics(ctx, targetID)
	if len(expectedAttempt) > 0 && d.boundAttempt != expectedAttempt[0] {
		return
	}
	if d.OperationState == "unknown" || d.OperationState == "pending" && failure.Code != "upload_incomplete" {
		failure.Code, failure.Retryable = "outcome_unknown", false
	}
	if d.CorrelationID != "" || d.OperationID != "" {
		if failure.Code == "outcome_unknown" {
			d.RetryEligibility = "blocked_unknown_outcome"
		} else if failure.Code == "upload_incomplete" {
			d.RetryEligibility = "resume_same_session"
		} else if failure.Retryable {
			d.RetryEligibility = "transient_retry"
		} else {
			d.RetryEligibility = "requires_revalidation"
		}
		_ = d.save(ctx, targetID)
	}
	a.markTargetFailure(ctx, targetID, redactDeliverySecrets(err.Error()), failure, expectedAttempt...)
}

func (a *App) markTargetFailure(ctx *sdk.AppCtx, targetID int64, msg string, failure publishFailure, expectedAttempt ...int) {
	attempt := -1
	if len(expectedAttempt) > 0 {
		attempt = expectedAttempt[0]
	}
	msg = redactDeliverySecrets(msg)
	_, _ = ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='failed',delivery_recorded_at=CURRENT_TIMESTAMP, last_error=?, failure_code=?, retryable=?,
		        upstream_status=?, existing_post_id=?
		  WHERE id=? AND (?<0 OR attempts=?)`,
		msg, failure.Code, failure.Retryable, failure.UpstreamStatus, failure.ExistingPostID, targetID, attempt, attempt,
	)
	ctx.Emit("target.failed", map[string]any{
		"target_id":        targetID,
		"error":            msg,
		"failure_code":     failure.Code,
		"retryable":        failure.Retryable,
		"existing_post_id": failure.ExistingPostID,
	})
}

// ─── post_list ────────────────────────────────────────────────────

func (a *App) toolPostList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	return a.listPosts(ctx, args, 200)
}

// listPosts backs both the MCP tool and the panel HTTP route. The MCP surface
// keeps its established 200-row ceiling; the panel can request a bounded
// calendar window with a larger cap. Both surfaces return pagination metadata.
func (a *App) listPosts(ctx *sdk.AppCtx, args map[string]any, maxLimit int) (any, error) {
	pid := projectScope(ctx, args)
	offset, offsetErr := postListOffset(args)
	if offsetErr != nil {
		return mcpError(offsetErr.Error()), nil
	}
	limit := intArg(args, "limit", 50)
	if limit < 1 {
		limit = 1
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	statusFilter, _ := args["status"].(string)
	from, fromErr := postListTimeBound(args, "from")
	if fromErr != nil {
		return mcpError(fromErr.Error()), nil
	}
	to, toErr := postListTimeBound(args, "to")
	if toErr != nil {
		return mcpError(toErr.Error()), nil
	}
	if from != nil && to != nil && !from.Before(*to) {
		return mcpError("from must be before to"), nil
	}
	profileID := resolveProfileArg(ctx, pid, args)
	if profileID < 0 {
		return mcpError(fmt.Sprintf("profile %q not found in this project", args["profile"])), nil
	}
	fields := `SELECT id, body, COALESCE(media_storage_ids,'[]'), COALESCE(external_media_urls,'[]'), COALESCE(schedule_at,''),
	             status, created_at, COALESCE(published_at,''), COALESCE(delivery_recorded_at,''), COALESCE(profile_id,0),
	             revision, approval_status, approved_revision, approval_required,
	             COALESCE(rejection_reason,''), requested_mode, provider_sync_mode, source, COALESCE(updated_at,created_at)`
	where := ` FROM posts WHERE project_id=?`
	accountID := int64(intArg(args, "social_account_id", 0))
	if _, present := args["social_account_id"]; present && accountID <= 0 {
		return mcpError("social_account_id must be positive"), nil
	}
	if accountID > 0 {
		var found int
		if e := ctx.AppDB().QueryRow(`SELECT count(*) FROM social_accounts WHERE id=? AND project_id=?`, accountID, pid).Scan(&found); e != nil {
			return nil, e
		}
		if found != 1 {
			return mcpError("Social account not found in this project"), nil
		}
	}
	qArgs := []any{pid}
	effectiveTime := `CASE
		WHEN status IN ('published','partial') AND COALESCE(published_at,'') != '' THEN published_at
		WHEN COALESCE(schedule_at,'') != '' THEN schedule_at
		ELSE created_at
	END`
	if statusFilter != "" {
		where += " AND status=?"
		qArgs = append(qArgs, statusFilter)
	}
	if profileID > 0 {
		where += " AND profile_id=?"
		qArgs = append(qArgs, profileID)
	}
	if from != nil {
		where += " AND datetime(" + effectiveTime + ") >= datetime(?)"
		qArgs = append(qArgs, from.UTC().Format(time.RFC3339))
	}
	if to != nil {
		where += " AND datetime(" + effectiveTime + ") < datetime(?)"
		qArgs = append(qArgs, to.UTC().Format(time.RFC3339))
	}
	if accountID > 0 {
		where += ` AND EXISTS(SELECT 1 FROM post_targets st WHERE st.post_id=posts.id AND st.social_account_id=?)`
		qArgs = append(qArgs, accountID)
	}
	nativeID := strings.TrimSpace(toString(args["native_post_id"]))
	if len(nativeID) > 512 {
		return mcpError("native_post_id too long"), nil
	}
	if nativeID != "" {
		where += ` AND EXISTS(SELECT 1 FROM post_targets st WHERE st.post_id=posts.id AND st.platform_post_id=?`
		qArgs = append(qArgs, nativeID)
		if accountID > 0 {
			where += ` AND st.social_account_id=?`
			qArgs = append(qArgs, accountID)
		}
		where += `)`
	}
	query := strings.TrimSpace(toString(args["query"]))
	if len(query) > 500 {
		return mcpError("query too long (max 500 characters)"), nil
	}
	if query != "" {
		where += ` AND (instr(lower(body),lower(?))>0 OR EXISTS(SELECT 1 FROM post_targets st LEFT JOIN social_native_posts hn ON hn.target_id=st.id AND hn.project_id=posts.project_id WHERE st.post_id=posts.id`
		qArgs = append(qArgs, query)
		if accountID > 0 {
			where += ` AND st.social_account_id=?`
			qArgs = append(qArgs, accountID)
		}
		where += ` AND (instr(lower(COALESCE(json_extract(CASE WHEN json_valid(st.options) THEN st.options ELSE '{}' END,'$.title'),'')),lower(?))>0 OR instr(lower(COALESCE(json_extract(hn.metadata,'$.title'),'')),lower(?))>0 OR instr(lower(COALESCE(json_extract(hn.metadata,'$.body'),'')),lower(?))>0 OR instr(lower(COALESCE(json_extract(hn.last_known_metadata,'$.original_filename'),'')),lower(?))>0)))`
		qArgs = append(qArgs, query, query, query, query)
	}
	// One read snapshot keeps totals, the page, and its targets consistent
	// even when background publishing updates the database during this call.
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var total, totalTargets int
	countSQL := postCountsSQL
	if accountID > 0 {
		countSQL = fmt.Sprintf(`SELECT COUNT(*),COALESCE(SUM((SELECT COUNT(*) FROM post_targets tc WHERE tc.post_id=posts.id AND tc.social_account_id=%d)),0)`, accountID)
	}
	if err := tx.QueryRow(countSQL+where, qArgs...).Scan(&total, &totalTargets); err != nil {
		return nil, fmt.Errorf("count posts: %w", err)
	}
	q := fields + where + " ORDER BY datetime(" + effectiveTime + ") DESC, id DESC LIMIT ? OFFSET ?"
	qArgs = append(qArgs, limit, offset)
	rows, err := tx.Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type postRow struct {
		id                                                                                  int64
		profID                                                                              int64
		body                                                                                string
		mediaIDs                                                                            []int64
		extMedia                                                                            []string
		schedAt                                                                             string
		status                                                                              string
		created                                                                             string
		pubAt, recordedAt                                                                   string
		revision, approvedRevision                                                          int64
		approvalRequired                                                                    bool
		approvalStatus, rejectionReason, requestedMode, providerSyncMode, source, updatedAt string
	}
	postRows := []postRow{}
	for rows.Next() {
		var (
			id, profID, revision, approvedRevision, approvalRequired                            int64
			body, mediaJSON, extMediaJSON, schedAt, status, createdAt, pubAt, recordedAt        string
			approvalStatus, rejectionReason, requestedMode, providerSyncMode, source, updatedAt string
		)
		if err := rows.Scan(&id, &body, &mediaJSON, &extMediaJSON, &schedAt, &status, &createdAt, &pubAt, &recordedAt, &profID,
			&revision, &approvalStatus, &approvedRevision, &approvalRequired, &rejectionReason,
			&requestedMode, &providerSyncMode, &source, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		var mediaIDs []int64
		_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
		var extMedia []string
		_ = json.Unmarshal([]byte(extMediaJSON), &extMedia)
		postRows = append(postRows, postRow{
			id:         id,
			profID:     profID,
			body:       body,
			mediaIDs:   mediaIDs,
			extMedia:   extMedia,
			schedAt:    schedAt,
			status:     status,
			created:    createdAt,
			pubAt:      pubAt,
			recordedAt: recordedAt,
			revision:   revision, approvedRevision: approvedRevision, approvalRequired: approvalRequired != 0,
			approvalStatus: approvalStatus, rejectionReason: rejectionReason, requestedMode: requestedMode,
			providerSyncMode: providerSyncMode, source: source, updatedAt: updatedAt,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	returnedTargets := 0
	for _, p := range postRows {
		targets, err := loadTargetsFrom(tx, p.id)
		if err != nil {
			return nil, fmt.Errorf("load post targets: %w", err)
		}
		if accountID > 0 {
			filtered := []map[string]any{}
			for _, target := range targets {
				if target["social_account_id"] == accountID {
					filtered = append(filtered, target)
				}
			}
			targets = filtered
		}
		if boolArg(args, "compact", false) {
			for i, target := range targets {
				keep := map[string]any{}
				for _, key := range []string{"id", "social_account_id", "platform", "status", "platform_post_id", "platform_url", "published_at", "publication_facts", "native_metadata", "last_known_native_metadata", "native_observed_at"} {
					if value, ok := target[key]; ok {
						keep[key] = value
					}
				}
				for _, key := range []string{"native_metadata", "last_known_native_metadata"} {
					if m, ok := keep[key].(map[string]any); ok {
						delete(m, "body")
						if title, ok := m["title"].(string); ok {
							m["title"] = historySnippet(title, 200)
						}
					}
				}
				targets[i] = keep
			}
		}
		returnedTargets += len(targets)
		if boolArg(args, "compact", false) {
			p.body = historySnippet(p.body, 300)
		}
		out = append(out, map[string]any{
			"id":                   p.id,
			"body":                 p.body,
			"media_storage_ids":    p.mediaIDs,
			"external_media_urls":  p.extMedia,
			"profile_id":           p.profID,
			"schedule_at":          p.schedAt,
			"status":               p.status,
			"created_at":           p.created,
			"published_at":         p.pubAt,
			"delivery_recorded_at": p.recordedAt,
			"updated_at":           p.updatedAt,
			"revision":             p.revision,
			"approval_status":      p.approvalStatus,
			"approved_revision":    p.approvedRevision,
			"approval_required":    p.approvalRequired,
			"rejection_reason":     p.rejectionReason,
			"requested_mode":       p.requestedMode,
			"provider_sync_mode":   p.providerSyncMode,
			"source":               p.source,
			"targets":              targets,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	var nextOffset any
	hasMore := offset < total && len(out) < total-offset
	if hasMore {
		nextOffset = offset + len(out)
	}
	return map[string]any{
		"posts": out, "total": total, "total_targets": totalTargets,
		"returned_targets": returnedTargets, "limit": limit, "offset": offset,
		"has_more": hasMore, "next_offset": nextOffset,
		"history": postListHistoryStates(ctx, accountID, profileID),
	}, nil
}

func (a *App) loadPostByID(ctx *sdk.AppCtx, projectID string, postID int64) (map[string]any, error) {
	var (
		id, profileID, revision, approvedRevision, approvalRequired                           int64
		body, mediaJSON, externalJSON, scheduleAt, status, createdAt, publishedAt, recordedAt string
		approvalStatus, rejectionReason, requestedMode, providerSyncMode, source, updatedAt   string
	)
	err := ctx.AppDB().QueryRow(
		`SELECT id, body, COALESCE(media_storage_ids,'[]'), COALESCE(external_media_urls,'[]'), COALESCE(schedule_at,''),
		        status, created_at, COALESCE(published_at,''), COALESCE(delivery_recorded_at,''), COALESCE(profile_id,0),
		        revision, approval_status, approved_revision, approval_required,
		        COALESCE(rejection_reason,''), requested_mode, provider_sync_mode, source, COALESCE(updated_at,created_at)
		   FROM posts WHERE id=? AND project_id=?`,
		postID, projectID,
	).Scan(&id, &body, &mediaJSON, &externalJSON, &scheduleAt, &status, &createdAt, &publishedAt, &recordedAt, &profileID,
		&revision, &approvalStatus, &approvedRevision, &approvalRequired, &rejectionReason,
		&requestedMode, &providerSyncMode, &source, &updatedAt)
	if err != nil {
		return nil, err
	}
	var mediaIDs []int64
	_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
	var externalURLs []string
	_ = json.Unmarshal([]byte(externalJSON), &externalURLs)
	return map[string]any{
		"id":                   id,
		"body":                 body,
		"media_storage_ids":    mediaIDs,
		"external_media_urls":  externalURLs,
		"profile_id":           profileID,
		"schedule_at":          scheduleAt,
		"status":               status,
		"created_at":           createdAt,
		"published_at":         publishedAt,
		"delivery_recorded_at": recordedAt,
		"updated_at":           updatedAt,
		"revision":             revision,
		"approval_status":      approvalStatus,
		"approved_revision":    approvedRevision,
		"approval_required":    approvalRequired != 0,
		"rejection_reason":     rejectionReason,
		"requested_mode":       requestedMode,
		"provider_sync_mode":   providerSyncMode,
		"source":               source,
		"targets":              a.loadTargets(ctx, id),
		"reviews":              a.loadPostReviews(ctx, id),
		"revisions":            a.loadPostRevisions(ctx, id),
	}, nil
}

func (a *App) loadPostRevisions(ctx *sdk.AppCtx, postID int64) []map[string]any {
	rows, err := ctx.AppDB().Query(
		`SELECT revision, actor, snapshot, created_at
		   FROM post_revisions WHERE post_id=? ORDER BY revision DESC`, postID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var revision int64
		var actor, snapshotRaw, createdAt string
		if rows.Scan(&revision, &actor, &snapshotRaw, &createdAt) != nil {
			continue
		}
		var snapshot map[string]any
		_ = json.Unmarshal([]byte(snapshotRaw), &snapshot)
		out = append(out, map[string]any{
			"revision": revision, "actor": actor, "snapshot": snapshot, "created_at": createdAt,
		})
	}
	return out
}

func (a *App) loadPostReviews(ctx *sdk.AppCtx, postID int64) []map[string]any {
	rows, err := ctx.AppDB().Query(
		`SELECT revision, action, actor, COALESCE(reason,''), created_at
		   FROM post_reviews WHERE post_id=? ORDER BY id`, postID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var revision int64
		var action, actor, reason, createdAt string
		if rows.Scan(&revision, &action, &actor, &reason, &createdAt) == nil {
			out = append(out, map[string]any{"revision": revision, "action": action, "actor": actor, "reason": reason, "created_at": createdAt})
		}
	}
	return out
}

func postListTimeBound(args map[string]any, name string) (*time.Time, error) {
	raw, _ := args[name].(string)
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid %s: expected RFC3339 timestamp", name)
	}
	return &parsed, nil
}

func (a *App) loadTargets(ctx *sdk.AppCtx, postID int64) []map[string]any {
	out, _ := loadTargetsFrom(ctx.AppDB(), postID)
	return out
}

func loadTargetsFrom(db interface {
	Query(string, ...any) (*sql.Rows, error)
}, postID int64) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT t.id, t.social_account_id, a.platform, a.display_name, COALESCE(a.avatar_url,''),
		        t.status, COALESCE(t.platform_post_id,''), COALESCE(t.platform_url,''),
		        t.attempts, COALESCE(t.last_error,''), COALESCE(t.published_at,''), COALESCE(t.options,''),
		        COALESCE(t.failure_code,''), t.retryable, t.upstream_status, COALESCE(t.existing_post_id,''),
		        COALESCE(t.provider_sync_status,''), COALESCE(t.provider_updated_at,''),
		 t.audience_status,COALESCE(t.audience_confirmed_at,''),COALESCE(t.audience_confirmation_actor,''),COALESCE(a.provider_slug,'native'), t.media_delivery_diagnostics, t.delivery_diagnostics, COALESCE(t.publish_operation_id,''),t.submitted_at,t.provider_accepted_at,t.provider_reported_published_at,t.delivery_recorded_at,t.native_uploaded_at,t.publication_observed_at,t.publication_time_source,t.legacy_published_at,t.publication_revision
		 FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id
		 WHERE t.post_id=? ORDER BY t.id`,
		postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var facts publicationFacts
		var (
			tid, acctID                                                                     int64
			platform, name, avatar, status, ppid, purl, lastErr, pubAt, optionsRaw          string
			failureCode, existingPostID, providerSyncStatus, providerUpdatedAt              string
			audienceStatus, audienceConfirmedAt, audienceActor, targetProvider, deliveryRaw string
			videoRaw, operationID                                                           string
			attempts                                                                        int
			retryable                                                                       int64
			upstreamStatus                                                                  int64
		)
		if err := rows.Scan(&tid, &acctID, &platform, &name, &avatar, &status, &ppid, &purl, &attempts, &lastErr, &pubAt,
			&optionsRaw, &failureCode, &retryable, &upstreamStatus, &existingPostID, &providerSyncStatus, &providerUpdatedAt, &audienceStatus, &audienceConfirmedAt, &audienceActor, &targetProvider, &deliveryRaw, &videoRaw, &operationID, &facts.SubmittedAt, &facts.ProviderAcceptedAt, &facts.ProviderPublishedAt, &facts.DeliveryRecordedAt, &facts.UploadedAt, &facts.ObservedAt, &facts.TimeSource, &facts.LegacyPublishedAt, &facts.Revision); err != nil {
			return nil, err
		}
		options := map[string]any{}
		if optionsRaw != "" {
			_ = json.Unmarshal([]byte(optionsRaw), &options)
		}
		var mediaDiagnostics []imageDeliveryDiagnostics
		_ = json.Unmarshal([]byte(deliveryRaw), &mediaDiagnostics)
		var videoDiagnostics map[string]any
		_ = json.Unmarshal([]byte(redactDeliverySecrets(videoRaw)), &videoDiagnostics)
		if platform == "tiktok" {
			decoded := decodeDeliveryDiagnostics(videoRaw, platform, lastErr)
			encoded, _ := json.Marshal(decoded)
			_ = json.Unmarshal(encoded, &videoDiagnostics)
		}
		if operationID == "" {
			operationID, _ = videoDiagnostics["operation_id"].(string)
		}
		facts.URL = optionalText(safePublicationURL(platform, purl))
		facts.PublishedAt = optionalText(pubAt)
		out = append(out, map[string]any{
			"publication_facts":           facts,
			"publish_operation_id":        safeRequestID(operationID),
			"delivery_diagnostics":        videoDiagnostics,
			"media_delivery_diagnostics":  mediaDiagnostics,
			"id":                          tid,
			"social_account_id":           acctID,
			"platform":                    platform,
			"provider":                    targetProvider,
			"display_name":                name,
			"avatar_url":                  avatar,
			"status":                      status,
			"platform_post_id":            ppid,
			"platform_url":                safePublicationURL(platform, purl),
			"attempts":                    attempts,
			"last_error":                  redactDeliverySecrets(lastErr),
			"published_at":                pubAt,
			"options":                     options,
			"failure_code":                failureCode,
			"retryable":                   retryable != 0,
			"upstream_status":             upstreamStatus,
			"existing_post_id":            existingPostID,
			"provider_sync_status":        providerSyncStatus,
			"provider_updated_at":         providerUpdatedAt,
			"audience_capabilities":       adapterForAudience(platform, targetProvider).Capabilities,
			"destination_capabilities":    adapterForDestinations(platform, targetProvider),
			"audience_status":             audienceStatus,
			"audience_confirmed_at":       audienceConfirmedAt,
			"audience_confirmation_actor": audienceActor,
			"audience_setup_url":          audienceSetupURL(platform, targetProvider, ppid),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	metadata, e := db.Query(`SELECT hn.target_id,hn.metadata,hn.last_known_metadata,hn.observed_at FROM social_native_posts hn JOIN post_targets ht ON ht.id=hn.target_id JOIN posts hp ON hp.id=ht.post_id WHERE ht.post_id=? AND hn.project_id=hp.project_id AND hn.social_account_id=ht.social_account_id`, postID)
	if e != nil {
		return nil, e
	}
	defer metadata.Close()
	for metadata.Next() {
		var id int64
		var current, known, at string
		if e = metadata.Scan(&id, &current, &known, &at); e != nil {
			return nil, e
		}
		var c, k any
		_ = json.Unmarshal([]byte(current), &c)
		_ = json.Unmarshal([]byte(known), &k)
		for _, target := range out {
			if target["id"] == id {
				target["native_metadata"] = normalizeNativeMetadataMap(toString(target["platform"]), toString(target["provider"]), c)
				target["last_known_native_metadata"] = normalizeNativeMetadataMap(toString(target["platform"]), toString(target["provider"]), k)
				target["native_observed_at"] = at
			}
		}
	}
	if err := metadata.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, target := range out {
		if target["platform"] == "youtube" && target["provider"] == "native" && target["status"] == "published" {
			options, _ := target["options"].(map[string]any)
			target["native_release"] = youtubeReleaseState(options, target["native_metadata"], toString(target["native_observed_at"]), now)
		}
	}
	return out, nil
}

// ─── post_retry ───────────────────────────────────────────────────

func (a *App) toolPostRetry(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return nil, errors.New("post_id required")
	}
	targetID := int64(intArg(args, "target_id", 0))
	if _, supplied := args["target_id"]; supplied && targetID <= 0 {
		return mcpError("target_id must be positive"), nil
	}
	unlock := lockSocialPost(ctx, postID)
	defer unlock()
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
	if targetID > 0 {
		if out, done, err := a.reconcileSelectedRetry(ctx, postID, targetID); done {
			return out, err
		}
	}
	var attempted int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=? AND attempts>0`, postID).Scan(&attempted)
	if targetID == 0 && postStatus == "failed" && scheduleAt != "" && jobID == 0 && attempted == 0 {
		return a.retryFailedSchedule(ctx, pid, postID, scheduleAt)
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET status='pending', last_error=NULL, failure_code='',
		        upstream_status=0, existing_post_id=''
		  WHERE post_id=? AND (?=0 OR id=?) AND (
		        (status='failed' AND retryable=1)
		      )
		    AND EXISTS (SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`,
		postID, targetID, targetID, pid,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		var code, existingID, lastError string
		var terminal int
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*), COALESCE(MAX(failure_code),''), COALESCE(MAX(existing_post_id),''), COALESCE(MAX(last_error),'')
			   FROM post_targets WHERE post_id=? AND (?=0 OR id=?) AND status='failed' AND retryable=0`, postID, targetID, targetID,
		).Scan(&terminal, &code, &existingID, &lastError)
		if terminal > 0 {
			return map[string]any{
				"status":           "failed",
				"retryable":        false,
				"failure_code":     code,
				"existing_post_id": existingID,
				"error":            redactDeliverySecrets(lastError),
				"message":          "content or configuration must change before retrying",
			}, nil
		}
		return mcpError("no failed targets to retry on this post"), nil
	}
	a.publishSelectedPostTargets(ctx, postID, targetID)
	return map[string]any{"retried": n, "target_id": targetID}, nil
}

func (a *App) runScheduledPublisher(runCtx context.Context, ctx *sdk.AppCtx) error {
	pid := projectScope(ctx)
	if pid == "" {
		return nil
	}
	a.recoverDeliveries(ctx)
	if err := a.resolvePendingTikTokPostIDs(runCtx, ctx, pid); err != nil {
		ctx.Logger().Warn("resolve pending TikTok post IDs", "project", pid, "err", err)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id FROM posts
		  WHERE project_id=? AND status='scheduled' AND job_id=0
		    AND source!='provider'
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
	publishDueBatch(runCtx, ids, func(id int64) { a.publishDuePost(ctx, id) })
	return nil
}

func (a *App) resolvePendingTikTokPostIDs(runCtx context.Context, ctx *sdk.AppCtx, pid string) error {
	if strings.TrimSpace(pid) == "" {
		return nil
	}
	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.connection_id,
		        COALESCE(t.platform_post_id,''), COALESCE(t.publish_operation_id,''),t.attempts
		   FROM post_targets t
		   JOIN posts p ON p.id=t.post_id
		   JOIN social_accounts a ON a.id=t.social_account_id
		  WHERE p.project_id=? AND a.project_id=?
		    AND a.platform='tiktok' AND a.status='active' AND t.status='published'
		    AND (
		      t.publish_operation_id!=''
		      OR t.platform_post_id LIKE 'v_pub_%'
		      OR t.platform_post_id LIKE 'p_pub_%'
		    )
		    AND (t.identity_resolve_after IS NULL OR t.identity_resolve_after<=CURRENT_TIMESTAMP)
		  ORDER BY COALESCE(t.identity_resolve_after,t.published_at), t.id
		  LIMIT 20`,
		pid, pid,
	)
	if err != nil {
		return err
	}
	var targets []metricsTarget
	for rows.Next() {
		var target metricsTarget
		if err := rows.Scan(
			&target.TargetID, &target.SocialAccountID, &target.ConnID,
			&target.ExtPostID, &target.PublishOperationID, &target.DeliveryAttempt,
		); err == nil {
			target.Platform = "tiktok"
			targets = append(targets, target)
		}
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, target := range targets {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if _, err := a.resolveTikTokPublicPostID(ctx, target); err != nil {
			_, _ = ctx.AppDB().Exec(
				`UPDATE post_targets
				    SET identity_resolve_attempts=identity_resolve_attempts+1,
				        identity_resolve_after=CASE
				          WHEN identity_resolve_attempts<2 THEN datetime('now','+5 minutes')
				          WHEN identity_resolve_attempts<5 THEN datetime('now','+30 minutes')
				          WHEN identity_resolve_attempts<12 THEN datetime('now','+6 hours')
				          ELSE datetime('now','+7 days')
				        END,
				        last_error=?
				  WHERE id=? AND COALESCE(publish_operation_id,'')=? AND COALESCE(platform_post_id,'')=? AND attempts=?`,
				err.Error(), target.TargetID, target.PublishOperationID, target.ExtPostID, target.DeliveryAttempt,
			)
			continue
		}
		ctx.Emit("target.identity_resolved", map[string]any{
			"target_id": target.TargetID,
			"platform":  "tiktok",
		})
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
	ctx = ctx.WithProject(projectScope(ctx, args))
	postID := int64(intArg(args, "post_id", 0))
	scheduleAt, _ := args["schedule_at"].(string)
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	if scheduleAt == "" {
		return mcpError("schedule_at required"), nil
	}
	unlock := lockSocialPost(ctx, postID)
	defer unlock()
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
	if _, err := ctx.AppDB().Exec(`UPDATE posts SET schedule_at=?,schedule_generation=schedule_generation+1,job_id=0 WHERE id=? AND project_id=? AND status='scheduled'`, rfc, postID, pid); err != nil {
		return nil, err
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
// post_metrics(post_id) and account_metrics(...) fan out to provider
// analytics tools, normalize their responses, and persist metric points.
// Account analytics support optional canonical breakdowns and filters;
// provider-specific omissions are surfaced as unsupported capabilities.
// Fresh requests still hit upstream APIs, so callers should avoid tight loops.
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
	Impressions    int64           `json:"impressions"`
	VideoViews     int64           `json:"video_views"`
	PinClicks      int64           `json:"pin_clicks"`
	OutboundClicks int64           `json:"outbound_clicks"`
	Engagements    int64           `json:"engagements"`
	Views          int64           `json:"views"`
	Reach          int64           `json:"reach"`
	Likes          int64           `json:"likes"`
	Comments       int64           `json:"comments"`
	Shares         int64           `json:"shares"`
	Saves          int64           `json:"saves"`
	Clicks         int64           `json:"clicks"`
	Available      []string        `json:"available"`
	Raw            json.RawMessage `json:"raw,omitempty"`
}

type targetMetricsOutcome struct {
	Report          *analyticsReport   `json:"report,omitempty"`
	Insights        insightSeries      `json:"insights,omitempty"`
	UpdatedAt       string             `json:"updated_at,omitempty"`
	TargetID        int64              `json:"target_id"`
	SocialAccountID int64              `json:"social_account_id"`
	Platform        string             `json:"platform"`
	PlatformPostID  string             `json:"platform_post_id,omitempty"`
	PlatformURL     string             `json:"platform_url,omitempty"`
	Status          string             `json:"status"` // ok | unsupported | skipped | failed
	Reason          string             `json:"reason,omitempty"`
	Error           string             `json:"error,omitempty"`
	Warnings        []string           `json:"warnings,omitempty"`
	Metrics         *normalizedMetrics `json:"metrics,omitempty"`
}

// metricsTarget is the resolved per-target context the dispatcher
// passes to each platform's fetcher. PageCreds is the JSON blob from
// social_accounts.page_credentials (Facebook page access_token, IG's
// linked-page token) — needed by FB/IG metrics calls because those
// endpoints reject user-level tokens.
type metricsTarget struct {
	TargetID, SocialAccountID, ConnID int64
	DeliveryAttempt                   int
	Platform, ExtPostID, ExtURL       string
	ExternalAccountID                 string
	PageCreds                         string
	ProviderSlug                      string
	ProviderAccountID                 string
	ProviderPostID                    string
	PublishOperationID                string
}

// getPostMetrics dispatches to the per-platform fetcher for one
// target. Returns a complete outcome (never nil) so the caller can
// always include it in the response array.
func (a *App) getPostMetrics(ctx *sdk.AppCtx, target metricsTarget) targetMetricsOutcome {
	return a.getPostMetricsQuery(ctx, target, analyticsQuery{RangeDays: 30})
}

func (a *App) fetchPostMetrics(ctx *sdk.AppCtx, target metricsTarget) targetMetricsOutcome {
	out := targetMetricsOutcome{
		TargetID:        target.TargetID,
		SocialAccountID: target.SocialAccountID,
		Platform:        target.Platform,
		PlatformPostID:  target.ExtPostID,
		PlatformURL:     safePublicationURL(target.Platform, target.ExtURL),
	}
	if target.ExtPostID == "" && target.ProviderPostID == "" && target.PublishOperationID == "" {
		out.Status = "skipped"
		out.Reason = "target was never published — no platform or provider post id"
		return out
	}
	if target.ProviderSlug != "" && target.ProviderSlug != "native" {
		return a.getProviderPostMetrics(ctx, out, target)
	}
	switch target.Platform {
	case "twitter":
		return a.getTwitterPostMetrics(ctx, out, target.ConnID)
	case "youtube":
		return a.getYoutubePostMetrics(ctx, out, target.ConnID)
	case "tiktok":
		return a.getTikTokPostMetrics(ctx, out, target)
	case "facebook":
		return a.getFacebookPostMetrics(ctx, out, target.ConnID, target.ExternalAccountID, target.PageCreds)
	case "instagram":
		return a.getInstagramPostMetrics(ctx, out, target.ConnID, target.PageCreds)
	case "pinterest":
		return a.getPinterestPostMetrics(ctx, out, target.ConnID)
	default:
		// LinkedIn / Reddit / Pinterest / Threads — analytics tools
		// either aren't in the catalog yet or have slug-style paths
		// that don't resolve. Surface as unsupported.
		out.Status = "unsupported"
		out.Reason = "no analytics tool wired for this platform yet"
		return out
	}
}

func (a *App) getProviderPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, target metricsTarget) targetMetricsOutcome {
	switch target.ProviderSlug {
	case zernioProviderSlug:
		return a.getZernioPostMetrics(ctx, out, target)
	default:
		out.Status = "unsupported"
		out.Reason = "analytics not wired for provider " + target.ProviderSlug
		return out
	}
}

func (a *App) getPinterestPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	return a.fetchPinterestPostReport(ctx, out, connID, analyticsQuery{RangeDays: 30})
}

// pinterestSummaryMetrics accepts both PinAnalyticsResponse and
// AnalyticsResponse. Each is keyed by a split such as "all" and carries a
// summary_metrics object; when Pinterest returns several splits we sum them.
func pinterestSummaryMetrics(raw json.RawMessage) map[string]int64 {
	var payload map[string]struct {
		Summary map[string]*float64 `json:"summary_metrics"`
	}
	_ = json.Unmarshal(raw, &payload)
	out := map[string]int64{}
	if all, ok := payload["all"]; ok {
		for key, value := range all.Summary {
			if value != nil {
				out[key] = int64(*value)
			}
		}
		return out
	}
	for _, group := range payload {
		for key, value := range group.Summary {
			if value != nil {
				out[key] += int64(*value)
			}
		}
	}
	return out
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

type twitterMetricCounts struct {
	ImpressionCount int64 `json:"impression_count"`
	LikeCount       int64 `json:"like_count"`
	ReplyCount      int64 `json:"reply_count"`
	RetweetCount    int64 `json:"retweet_count"`
	QuoteCount      int64 `json:"quote_count"`
	BookmarkCount   int64 `json:"bookmark_count"`
}

// getTwitterPostMetrics uses the standard tweet lookup first. It is
// available to normal OAuth clients and already includes owner-only
// metrics when the caller owns the post. The separate analytics endpoint
// is an enrollment-gated fallback, not a prerequisite for basic stats.
func (a *App) getTwitterPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64) targetMetricsOutcome {
	var res *sdk.ExecuteResult
	for _, fields := range []string{
		"public_metrics,non_public_metrics,organic_metrics",
		"public_metrics",
	} {
		current, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_tweet_analytics", map[string]any{
			"tweet_id":     out.PlatformPostID,
			"tweet.fields": fields,
		})
		if err == nil && current != nil && current.Success {
			res = current
			break
		}
	}
	if res == nil {
		return a.getTwitterPostAnalytics(ctx, out, connID)
	}
	var resp struct {
		Data struct {
			PublicMetrics    twitterMetricCounts `json:"public_metrics"`
			NonPublicMetrics twitterMetricCounts `json:"non_public_metrics"`
			OrganicMetrics   twitterMetricCounts `json:"organic_metrics"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &resp); err != nil {
		out.Status, out.Error = "failed", "decode X post metrics: "+err.Error()
		return out
	}
	pm := mergeTwitterMetricCounts(
		resp.Data.PublicMetrics,
		resp.Data.NonPublicMetrics,
		resp.Data.OrganicMetrics,
	)
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:     pm.ImpressionCount,
		Likes:     pm.LikeCount,
		Comments:  pm.ReplyCount,
		Shares:    pm.RetweetCount + pm.QuoteCount, // group retweets + quotes under shares
		Saves:     pm.BookmarkCount,
		Available: []string{"views", "likes", "comments", "shares", "saves"},
		Raw:       sanitizeRawJSON(res.Data),
	}
	return out
}

func mergeTwitterMetricCounts(groups ...twitterMetricCounts) twitterMetricCounts {
	var merged twitterMetricCounts
	for _, group := range groups {
		merged.ImpressionCount = maxInt64(merged.ImpressionCount, group.ImpressionCount)
		merged.LikeCount = maxInt64(merged.LikeCount, group.LikeCount)
		merged.ReplyCount = maxInt64(merged.ReplyCount, group.ReplyCount)
		merged.RetweetCount = maxInt64(merged.RetweetCount, group.RetweetCount)
		merged.QuoteCount = maxInt64(merged.QuoteCount, group.QuoteCount)
		merged.BookmarkCount = maxInt64(merged.BookmarkCount, group.BookmarkCount)
	}
	return merged
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
	available := []string{}
	if namedMetricPresent(values, "impressions", "media_views", "views") {
		available = append(available, "views")
	}
	if namedMetricPresent(values, "likes", "like_count") {
		available = append(available, "likes")
	}
	if namedMetricPresent(values, "replies", "reply_count", "comments") {
		available = append(available, "comments")
	}
	if namedMetricPresent(values, "retweets", "retweet_count", "quote_tweets", "quote_count") {
		available = append(available, "shares")
	}
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:    firstMetricValue(values, "impressions", "media_views", "views"),
		Likes:    firstMetricValue(values, "likes", "like_count"),
		Comments: firstMetricValue(values, "replies", "reply_count", "comments"),
		Shares: firstMetricValue(values, "retweets", "retweet_count") +
			firstMetricValue(values, "quote_tweets", "quote_count"),
		Available: available,
		Raw:       sanitizeRawJSON(res.Data),
	}
	return out
}

func hasAnyMetrics(m *normalizedMetrics) bool {
	return m != nil && (len(m.Available) > 0 || m.Views > 0 || m.Reach > 0 || m.Likes > 0 || m.Comments > 0 || m.Shares > 0 || m.Saves > 0 || m.Clicks > 0)
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

func namedMetricPresent(values map[string]int64, keys ...string) bool {
	for _, key := range keys {
		if _, ok := values[key]; ok {
			return true
		}
	}
	return false
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
		Views:     parseInt64(stats.ViewCount),
		Likes:     parseInt64(stats.LikeCount),
		Comments:  parseInt64(stats.CommentCount),
		Shares:    0, // YouTube doesn't expose share count via Data API
		Available: []string{"views", "likes", "comments"},
		Raw:       sanitizeRawJSON(res.Data),
	}
	return out
}

// getTikTokPostMetrics calls query_videos with filters.video_ids + the
// metric fields. Response shape:
// {data: {videos: [{view_count, like_count, comment_count, share_count}]}}.
func (a *App) getTikTokPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, target metricsTarget) targetMetricsOutcome {
	publicID := strings.TrimSpace(target.ExtPostID)
	if isTikTokPublishOperationID(publicID) || publicID == "" {
		var err error
		publicID, err = a.resolveTikTokPublicPostID(ctx, target)
		if err != nil {
			out.Status = "skipped"
			out.Reason = err.Error()
			return out
		}
		out.PlatformPostID = publicID
	}
	if isTikTokPublishOperationID(publicID) {
		out.Status = "skipped"
		out.Reason = "TikTok public post ID is still pending"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(target.ConnID, "query_videos", map[string]any{
		"filters": map[string]any{"video_ids": []string{publicID}},
		"fields":  "id,title,share_url,view_count,like_count,comment_count,share_count",
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
				ID           exactJSONID `json:"id"`
				ShareURL     string      `json:"share_url"`
				ViewCount    int64       `json:"view_count"`
				LikeCount    int64       `json:"like_count"`
				CommentCount int64       `json:"comment_count"`
				ShareCount   int64       `json:"share_count"`
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
	v.ShareURL = safePublicationURL("tiktok", v.ShareURL)
	if v.ShareURL != "" {
		out.PlatformURL = v.ShareURL
		_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET platform_url=? WHERE id=? AND social_account_id=? AND platform_post_id=?`, v.ShareURL, target.TargetID, target.SocialAccountID, publicID)
	}
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:     v.ViewCount,
		Likes:     v.LikeCount,
		Comments:  v.CommentCount,
		Shares:    v.ShareCount,
		Available: []string{"views", "likes", "comments", "shares"},
		Raw:       sanitizeRawJSON(res.Data),
	}
	return out
}

func isTikTokPublishOperationID(id string) bool {
	return strings.HasPrefix(id, "v_pub_") || strings.HasPrefix(id, "p_pub_")
}

func (a *App) resolveTikTokPublicPostID(ctx *sdk.AppCtx, target metricsTarget) (string, error) {
	operationID := strings.TrimSpace(target.PublishOperationID)
	if operationID == "" && isTikTokPublishOperationID(target.ExtPostID) {
		operationID = target.ExtPostID
	}
	if operationID == "" {
		return "", errors.New("TikTok target has no public post ID")
	}
	var expectedOperation, expectedPublic string
	var attempt int
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(publish_operation_id,''),attempts,COALESCE(platform_post_id,'') FROM post_targets WHERE id=? AND EXISTS(SELECT 1 FROM posts p WHERE p.id=post_targets.post_id AND p.project_id=?)`, target.TargetID, projectScope(ctx)).Scan(&expectedOperation, &attempt, &expectedPublic); err != nil {
		return "", err
	}
	if target.DeliveryAttempt > 0 && target.DeliveryAttempt != attempt {
		return "", errors.New("stale queued TikTok attempt")
	}
	if expectedPublic != target.ExtPostID || expectedOperation != operationID && !(expectedOperation == "" && target.ExtPostID == operationID) {
		return "", errors.New("stale TikTok identity operation")
	}
	status, failReason, postIDs, err := a.getTikTokPublishStatus(ctx, target.ConnID, operationID)
	if err != nil {
		return "", fmt.Errorf("TikTok public post ID could not be resolved: %w", err)
	}
	if status == "FAILED" {
		return "", fmt.Errorf("TikTok publish failed while resolving public post ID: %s", failReason)
	}
	if len(postIDs) == 0 || postIDs[0] == "" {
		return "", fmt.Errorf("TikTok public post ID is not available yet (status %s)", status)
	}
	publicID := postIDs[0]
	updated, err := ctx.AppDB().Exec(
		`UPDATE post_targets
		    SET platform_post_id=?, publish_operation_id='',
		        identity_resolve_attempts=0, identity_resolve_after=NULL,
		        last_error=NULL
		  WHERE id=? AND attempts=? AND COALESCE(publish_operation_id,'')=? AND COALESCE(platform_post_id,'')=?`,
		publicID, target.TargetID, attempt, expectedOperation, expectedPublic,
	)
	if err != nil {
		return "", err
	}
	if n, _ := updated.RowsAffected(); n != 1 {
		return "", errors.New("stale TikTok identity result ignored")
	}
	return publicID, nil
}

// getFacebookPostMetrics calls facebook_get_post with engagement-summary
// fields. Graph response carries each count under .summary.total_count
// (likes, comments, reactions) or just .count (shares). Reactions and
// likes are technically separate signals on Graph — we map likes →
// likes (the raw "like" reactions), and stash the broader reactions
// total in raw for callers that want it. Views are not exposed on
// organic FB posts via this endpoint; needs /insights for that.
func (a *App) getFacebookPostMetrics(ctx *sdk.AppCtx, out targetMetricsOutcome, connID int64, pageID, pageCreds string) targetMetricsOutcome {
	token := extractPageToken(pageCreds)
	if token == "" {
		out.Status = "failed"
		out.Error = "facebook page access_token missing — reconnect the account"
		return out
	}
	type engagementResponse struct {
		ID        string `json:"id"`
		PostID    string `json:"post_id"`
		Permalink string `json:"permalink_url"`
		Views     int64  `json:"views"`
		Likes     struct {
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
	call := func(tool string, input map[string]any) (engagementResponse, json.RawMessage, error) {
		var parsed engagementResponse
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
		if err != nil {
			return parsed, nil, err
		}
		if res == nil || !res.Success {
			return parsed, nil, upstreamError(res)
		}
		if err := json.Unmarshal(res.Data, &parsed); err != nil {
			return parsed, nil, err
		}
		return parsed, sanitizeRawJSON(res.Data), nil
	}

	input := map[string]any{"access_token": token}
	var base engagementResponse
	var baseRaw json.RawMessage
	var err error
	feedID := out.PlatformPostID
	available := []string{"likes", "comments"}
	if !strings.Contains(out.PlatformPostID, "_") {
		input["videoId"] = out.PlatformPostID
		input["fields"] = "id,post_id,permalink_url,views,likes.limit(0).summary(true),comments.limit(0).summary(true)"
		base, baseRaw, err = call("facebook_get_video", input)
		available = append(available, "views")
		if base.PostID != "" && pageID != "" {
			feedID = pageID + "_" + base.PostID
		}
	} else {
		input["postId"] = out.PlatformPostID
		input["fields"] = "id,permalink_url,likes.limit(0).summary(true),comments.limit(0).summary(true)"
		base, baseRaw, err = call("facebook_get_post", input)
	}
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	base.Permalink = safePublicationURL("facebook", base.Permalink)
	if base.Permalink != "" {
		out.PlatformURL = base.Permalink
		_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET platform_url=? WHERE id=? AND social_account_id=? AND platform_post_id=?`, base.Permalink, out.TargetID, out.SocialAccountID, out.PlatformPostID)
	}
	rawParts := map[string]json.RawMessage{"base": baseRaw}
	shares := int64(0)
	if strings.Contains(feedID, "_") {
		shareInput := map[string]any{
			"postId":       feedID,
			"fields":       "shares",
			"access_token": token,
		}
		shareResp, shareRaw, shareErr := call("facebook_get_post", shareInput)
		if shareErr != nil {
			out.Warnings = append(out.Warnings, "Facebook shares unavailable: "+shareErr.Error())
		} else {
			shares = shareResp.Shares.Count
			available = append(available, "shares")
			rawParts["shares"] = shareRaw
		}
	}
	combinedRaw, _ := json.Marshal(rawParts)
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:     base.Views,
		Likes:     base.Likes.Summary.TotalCount,
		Comments:  base.Comments.Summary.TotalCount,
		Shares:    shares,
		Available: available,
		Raw:       sanitizeRawJSON(combinedRaw),
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
	values := map[string]int64{}
	available := []string{}
	rawParts := map[string]json.RawMessage{}

	mediaRes, mediaErr := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_media", map[string]any{
		"mediaId":      out.PlatformPostID,
		"fields":       "id,media_type,media_product_type,permalink,like_count,comments_count",
		"access_token": token,
	})
	if mediaErr == nil && mediaRes != nil && mediaRes.Success {
		var media struct {
			Permalink    string `json:"permalink"`
			LikeCount    int64  `json:"like_count"`
			CommentCount int64  `json:"comments_count"`
		}
		if json.Unmarshal(mediaRes.Data, &media) == nil {
			values["likes"] = media.LikeCount
			values["comments"] = media.CommentCount
			available = append(available, "likes", "comments")
			media.Permalink = safePublicationURL("instagram", media.Permalink)
			if media.Permalink != "" {
				out.PlatformURL = media.Permalink
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET platform_url=? WHERE id=? AND social_account_id=? AND platform_post_id=?`, media.Permalink, out.TargetID, out.SocialAccountID, out.PlatformPostID)
			}
		}
		rawParts["media"] = sanitizeRawJSON(mediaRes.Data)
	} else {
		if mediaErr != nil {
			out.Warnings = append(out.Warnings, "Instagram media details unavailable: "+mediaErr.Error())
		} else {
			out.Warnings = append(out.Warnings, "Instagram media details unavailable: "+upstreamError(mediaRes).Error())
		}
	}

	metricNames := []string{"views", "reach", "saved", "shares"}
	if len(available) == 0 {
		metricNames = append(metricNames, "likes", "comments")
	}
	for _, metric := range metricNames {
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_media_insights", map[string]any{
			"mediaId":      out.PlatformPostID,
			"metric":       metric,
			"access_token": token,
		})
		if err != nil || res == nil || !res.Success {
			reason := err
			if reason == nil {
				reason = upstreamError(res)
			}
			out.Warnings = append(out.Warnings, "Instagram "+metric+" unavailable: "+reason.Error())
			continue
		}
		value, ok := parseInstagramInsightValue(res.Data, metric)
		if !ok {
			out.Warnings = append(out.Warnings, "Instagram "+metric+" returned no value")
			continue
		}
		values[metric] = value
		normalizedName := metric
		if metric == "saved" {
			normalizedName = "saves"
		}
		available = append(available, normalizedName)
		rawParts[metric] = sanitizeRawJSON(res.Data)
	}
	if len(available) == 0 {
		out.Status = "failed"
		out.Error = "Instagram returned no supported metrics"
		return out
	}
	combinedRaw, _ := json.Marshal(rawParts)
	out.Status = "ok"
	out.Metrics = &normalizedMetrics{
		Views:     values["views"],
		Reach:     values["reach"],
		Likes:     values["likes"],
		Comments:  values["comments"],
		Shares:    values["shares"],
		Saves:     values["saved"],
		Available: available,
		Raw:       sanitizeRawJSON(combinedRaw),
	}
	return out
}

func parseInstagramInsightValue(raw json.RawMessage, metric string) (int64, bool) {
	var resp struct {
		Data []struct {
			Name   string `json:"name"`
			Values []struct {
				Value any `json:"value"`
			} `json:"values"`
			TotalValue struct {
				Value any `json:"value"`
			} `json:"total_value"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return 0, false
	}
	for _, item := range resp.Data {
		if item.Name != "" && item.Name != metric {
			continue
		}
		if len(item.Values) > 0 {
			return insightValueToInt64(item.Values[len(item.Values)-1].Value), true
		}
		if item.TotalValue.Value != nil {
			return insightValueToInt64(item.TotalValue.Value), true
		}
	}
	return 0, false
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
	PeriodMetrics   map[string]int64           `json:"period_metrics,omitempty"`
	PeriodWindow    *analyticsWindow           `json:"period_window,omitempty"`
	Report          *analyticsReport           `json:"report,omitempty"`
	PostRows        []analyticsPostRow         `json:"post_rows,omitempty"`
	Comparison      *analyticsPeriodComparison `json:"period_comparison,omitempty"`
	VideoViews      int64                      `json:"video_views"`
	PinClicks       int64                      `json:"pin_clicks"`
	OutboundClicks  int64                      `json:"outbound_clicks"`
	Available       []string                   `json:"available"`
	SocialAccountID int64                      `json:"social_account_id"`
	ProfileID       int64                      `json:"profile_id,omitempty"`
	Platform        string                     `json:"platform"`
	DisplayName     string                     `json:"display_name"`
	Status          string                     `json:"status"` // ok | unsupported | failed
	Reason          string                     `json:"reason,omitempty"`
	Error           string                     `json:"error,omitempty"`
	Followers       int64                      `json:"followers,omitempty"`
	Supporters      int64                      `json:"supporters,omitempty"`
	SupportersState string                     `json:"supporters_state,omitempty"`
	supporters      *supporterSnapshot
	Following       int64                 `json:"following,omitempty"`
	TotalLikes      int64                 `json:"total_likes,omitempty"`
	TotalVideos     int64                 `json:"total_videos,omitempty"`
	Posts           int64                 `json:"posts,omitempty"`
	Reach           int64                 `json:"reach,omitempty"`
	Impressions     int64                 `json:"impressions,omitempty"`
	Engagements     int64                 `json:"engagements,omitempty"`
	Views           int64                 `json:"views,omitempty"`
	Likes           int64                 `json:"likes,omitempty"`
	Comments        int64                 `json:"comments,omitempty"`
	Shares          int64                 `json:"shares,omitempty"`
	Saves           int64                 `json:"saves,omitempty"`
	Sends           int64                 `json:"sends,omitempty"`
	Clicks          int64                 `json:"clicks,omitempty"`
	EngagementRate  float64               `json:"engagement_rate,omitempty"`
	Insights        insightSeries         `json:"insights,omitempty"`
	Breakdowns      []analyticsBreakdown  `json:"breakdowns,omitempty"`
	Capabilities    analyticsCapabilities `json:"capabilities"`
	HistorySource   string                `json:"history_source,omitempty"`
	UpdatedAt       string                `json:"updated_at,omitempty"`
	Raw             json.RawMessage       `json:"raw,omitempty"`
}

type insightPoint struct {
	Estimated  bool   `json:"estimated,omitempty"`
	DataStatus string `json:"data_status,omitempty"`
	Included   int    `json:"included,omitempty"`
	Expected   int    `json:"expected,omitempty"`
	Partial    bool   `json:"partial,omitempty"`
	Time       string `json:"time,omitempty"`
	Value      int64  `json:"value"`
}

type insightSeries map[string][]insightPoint

func (a *App) getAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64, period string, query analyticsQuery) accountMetricsResult {
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
	var result accountMetricsResult
	if query.PublishedInRange && (query.Scope != "destination" || !analyticsCapabilitiesFor(platform, providerSlug).PublicationFilter) {
		out.Status = "unsupported"
		out.Reason = "Publication-date inventory filtering is unsupported by this adapter."
		out.Capabilities = analyticsCapabilitiesFor(platform, providerSlug)
		decorateAccountReport(&out, connID, providerSlug, providerAccountID, extID, query)
		return out
	}
	for dimension := range query.Filters {
		if !containsFold(analyticsCapabilitiesFor(platform, providerSlug).Available, dimension) {
			out.Status = "unsupported"
			out.Reason = "This adapter does not support the requested analytics filter: " + dimension
			out.Capabilities = analyticsCapabilitiesFor(platform, providerSlug)
			decorateAccountReport(&out, connID, providerSlug, providerAccountID, extID, query)
			return out
		}
	}
	if providerSlug != "" && providerSlug != "native" {
		if platform == "pinterest" && query.Scope == "destination" {
			out.Status = "unsupported"
			out.Reason = "Zernio exposes account analytics here; board analytics require provider support. Use scope=provider_account."
			out.Capabilities = analyticsCapabilitiesFor(platform, providerSlug)
			decorateAccountReport(&out, connID, providerSlug, providerAccountID, extID, query)
			return out
		}
		result = a.getProviderAccountMetrics(ctx, out, connID, providerSlug, providerAccountID, platform, query)
		applySupporterMetrics(ctx, pid, &result, providerSlug)
		a.addAccountBreakdowns(ctx, &result, connID, providerSlug, providerAccountID, extID, pageCreds, query)
		decorateAccountReport(&result, connID, providerSlug, providerAccountID, extID, query)
		a.addPeriodComparison(ctx, pid, &result, period, query)
		if containsFold(query.GroupBy, "post") && result.Status == "ok" {
			a.addKnownPostReport(ctx, pid, &result, query)
		}
		return result
	}
	switch platform {
	case "twitter":
		result = a.getTwitterAccountMetrics(ctx, out, connID)
	case "youtube":
		result = a.getYoutubeChannelMetrics(ctx, out, connID, query)
	case "tiktok":
		result = a.getTikTokAccountMetrics(ctx, out, connID)
	case "facebook":
		result = a.getFacebookAccountMetrics(ctx, out, connID, extID, pageCreds, period, query)
	case "instagram":
		result = a.getInstagramAccountMetrics(ctx, out, connID, extID, pageCreds, period, query)
	case "pinterest":
		if query.Scope == "destination" {
			boardID := query.DestinationID
			if boardID == "" {
				if b, e := loadDestinationAccount(ctx, pid, accountID); e == nil {
					boardID = b.DefaultID
				}
			}
			if boardID == "" {
				out.Status = "unsupported"
				out.Reason = "Select destination_id or set a default board; account statistics use scope=provider_account"
				result = out
			} else {
				result = a.getPinterestBoardMetrics(ctx, out, connID, boardID, query)
			}
		} else {
			result = a.getPinterestAccountMetrics(ctx, out, connID, query)
		}
	default:
		out.Status = "unsupported"
		out.Reason = "account-level metrics not wired for this platform yet"
		result = out
	}
	applySupporterMetrics(ctx, pid, &result, providerSlug)
	a.addAccountBreakdowns(ctx, &result, connID, providerSlug, providerAccountID, extID, pageCreds, query)
	decorateAccountReport(&result, connID, providerSlug, providerAccountID, extID, query)
	a.addPeriodComparison(ctx, pid, &result, period, query)
	if containsFold(query.GroupBy, "post") && platform != "pinterest" && result.PostRows == nil && result.Status == "ok" {
		a.addKnownPostReport(ctx, pid, &result, query)
	}
	return result
}

func (a *App) getProviderAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, providerSlug, providerAccountID, platform string, query analyticsQuery) accountMetricsResult {
	switch providerSlug {
	case zernioProviderSlug:
		return a.getZernioAccountMetrics(ctx, out, connID, providerAccountID, platform, query)
	default:
		out.Status = "unsupported"
		out.Reason = "analytics not wired for provider " + providerSlug
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

func (a *App) getPinterestAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, query analyticsQuery) accountMetricsResult {
	if err := pinterestReportDates(query); err != nil {
		out.Status = "unsupported"
		out.Reason = err.Error()
		return out
	}
	profileRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_user_account", map[string]any{})
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	if profileRes == nil || !profileRes.Success {
		out.Status, out.Error = "failed", upstreamError(profileRes).Error()
		return out
	}
	var profile struct {
		Username       string `json:"username"`
		FollowerCount  int64  `json:"follower_count"`
		FollowingCount int64  `json:"following_count"`
		PinCount       int64  `json:"pin_count"`
	}
	_ = json.Unmarshal(profileRes.Data, &profile)
	out.Status = "ok"
	if out.DisplayName == "" {
		out.DisplayName = profile.Username
	}
	out.Followers = profile.FollowerCount
	out.Following = profile.FollowingCount
	out.Posts = profile.PinCount
	out.Report = newAnalyticsReport("provider_account", profile.Username, "provider", query, "snapshot")
	out.Report.Identity = "pinterest:account:" + profile.Username
	if profile.Username == "" {
		out.Report.Identity = fmt.Sprintf("pinterest:connection:%d", connID)
	}
	var profileValues map[string]json.RawMessage
	_ = json.Unmarshal(profileRes.Data, &profileValues)
	for _, field := range []struct{ raw, normalized string }{{"follower_count", "followers"}, {"following_count", "following"}, {"pin_count", "posts"}} {
		if _, ok := profileValues[field.raw]; ok {
			out.Available = append(out.Available, field.normalized)
		}
	}

	startDate, endDate := query.dates()
	analyticsRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_user_analytics", map[string]any{
		"start_date":   startDate,
		"end_date":     endDate,
		"metric_types": []string{"IMPRESSION", "ENGAGEMENT", "PIN_CLICK", "OUTBOUND_CLICK", "SAVE"},
	})
	if err != nil || analyticsRes == nil || !analyticsRes.Success {
		if err == nil {
			err = upstreamError(analyticsRes)
		}
		out.Reason = "pinterest analytics unavailable: " + err.Error()
		out.Raw = sanitizeRawJSON(profileRes.Data)
		return out
	}
	summary := pinterestSummaryMetrics(analyticsRes.Data)
	out.Impressions = summary["IMPRESSION"]
	out.Engagements = summary["ENGAGEMENT"]
	out.Clicks = summary["OUTBOUND_CLICK"]
	out.PinClicks = summary["PIN_CLICK"]
	out.OutboundClicks = summary["OUTBOUND_CLICK"]
	out.Available = append(out.Available, pinterestAvailable(summary)...)
	out.Report.Window = newAnalyticsReport("provider_account", profile.Username, "provider", query, "range").Window
	setReportMetricInfo(out.Report, out.Available)
	out.Saves = summary["SAVE"]
	out.Insights = pinterestDailyInsights(analyticsRes.Data, "IMPRESSION", "ENGAGEMENT", "PIN_CLICK", "OUTBOUND_CLICK", "SAVE")
	finalizePinterestReport(out.Report, out.Insights, query, []string{"impressions", "engagements", "pin_clicks", "outbound_clicks", "saves"})
	out.Raw = sanitizeRawJSON(analyticsRes.Data)
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

func (a *App) getYoutubeChannelMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, query analyticsQuery) accountMetricsResult {
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
	a.addYoutubeAnalytics(ctx, &out, connID, query)
	out.Raw = sanitizeRawJSON(res.Data)
	return out
}

func (a *App) addYoutubeAnalytics(ctx *sdk.AppCtx, out *accountMetricsResult, connID int64, query analyticsQuery) {
	since, until := query.dates()
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

func (a *App) getFacebookAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, pageID, pageCreds, period string, query analyticsQuery) accountMetricsResult {
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
	since, until := query.dates()
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

func (a *App) getInstagramAccountMetrics(ctx *sdk.AppCtx, out accountMetricsResult, connID int64, instagramAccountID, pageCreds, period string, query analyticsQuery) accountMetricsResult {
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
	since, until := query.dates()
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

const (
	analyticsSnapshotMinInterval  = 4 * time.Hour
	postAnalyticsBatchSize        = 50
	postAnalyticsCandidateScanMax = 200
)

const (
	defaultAccountMetricsHistoryDays = 30
	maxAccountMetricsHistoryDays     = 730
)

func (a *App) runAnalyticsCollector(ctx context.Context, app *sdk.AppCtx) error {
	projects, err := analyticsProjectScopes(app)
	if err != nil {
		return err
	}
	for _, pid := range projects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := a.collectProjectAccountMetrics(ctx, app, pid); err != nil {
			app.Logger().Warn("analytics_collector: project failed", "project", pid, "err", err)
		}
		if err := a.collectProjectPostMetrics(ctx, app, pid); err != nil {
			app.Logger().Warn("analytics_collector: post metrics failed", "project", pid, "err", err)
		}
	}
	return nil
}

func analyticsProjectScopes(ctx *sdk.AppCtx) ([]string, error) {
	if pid := projectScope(ctx); pid != "" {
		return []string{pid}, nil
	}
	rows, err := ctx.AppDB().Query(
		`SELECT DISTINCT project_id FROM social_accounts
		  WHERE project_id<>'' AND status='active'
		  ORDER BY project_id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var projects []string
	for rows.Next() {
		var pid string
		if err := rows.Scan(&pid); err == nil && strings.TrimSpace(pid) != "" {
			projects = append(projects, pid)
		}
	}
	return projects, rows.Err()
}

func (a *App) collectProjectAccountMetrics(runCtx context.Context, app *sdk.AppCtx, pid string) error {
	rows, err := app.AppDB().Query(
		`SELECT id FROM social_accounts
		  WHERE project_id=? AND status='active'
		  ORDER BY id`,
		pid,
	)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		a.refreshSupportersIfDue(app, pid, id)
		due, err := accountAnalyticsDue(app, pid, id, analyticsSnapshotMinInterval)
		if err != nil {
			app.Logger().Warn("analytics_collector: due check failed", "project", pid, "account", id, "err", err)
			continue
		}
		if !due {
			continue
		}
		query := analyticsQuery{RangeDays: 28, Filters: map[string][]string{}}
		var platform string
		_ = app.AppDB().QueryRow(`SELECT platform FROM social_accounts WHERE id=? AND project_id=?`, id, pid).Scan(&platform)
		if platform == "pinterest" {
			query.Scope = "provider_account"
			if b, err := loadDestinationAccount(app, pid, id); err == nil && b.Kind == "destination_preset" {
				query.Scope = "destination"
				query.DestinationID = b.DefaultID
			}
			query.Compare = true
			query.GroupBy = []string{"post"}
		}
		if due, err := accountBreakdownsDue(app, pid, id, 24*time.Hour); err == nil && due {
			if platform != "pinterest" {
				query.Breakdowns = []string{"device", "country", "traffic_source", "age", "gender"}
			}
		}
		res := a.collectAndStoreAccountMetrics(app, pid, id, "day", query)
		if res.Status == "failed" {
			app.Logger().Warn("analytics_collector: account metrics failed",
				"project", pid, "account", id, "platform", res.Platform, "err", res.Error)
		}
	}
	return nil
}

type postAnalyticsCandidate struct {
	PostID     int64
	ProfileID  int64
	Published  string
	LastRun    string
	LastStatus string
	Target     metricsTarget
}

func (a *App) collectProjectPostMetrics(runCtx context.Context, app *sdk.AppCtx, pid string) error {
	rows, err := app.AppDB().Query(
		`SELECT p.id, COALESCE(p.profile_id,0),
		        COALESCE(t.published_at, p.published_at, p.created_at, ''),
		        t.id, t.social_account_id, COALESCE(t.platform_post_id,''),
		        COALESCE(t.platform_url,''), a.platform, a.connection_id,
		        COALESCE(a.external_account_id,''), COALESCE(a.page_credentials,''),
		        COALESCE(a.provider_slug,'native'), COALESCE(a.provider_account_id,''),
		        COALESCE(t.provider_post_id,''), COALESCE(t.publish_operation_id,''),
		        COALESCE((
		          SELECT point_time FROM social_metric_points mp
		           WHERE mp.project_id=p.project_id AND mp.scope='post'
		             AND mp.post_target_id=t.id AND mp.metric='_refresh'
		           ORDER BY mp.point_time DESC, mp.id DESC LIMIT 1
		        ), ''),
		        COALESCE((
		          SELECT status FROM social_metric_points mp
		           WHERE mp.project_id=p.project_id AND mp.scope='post'
		             AND mp.post_target_id=t.id AND mp.metric='_refresh'
		           ORDER BY mp.point_time DESC, mp.id DESC LIMIT 1
		        ), '')
		   FROM post_targets t
		   JOIN posts p ON p.id=t.post_id
		   JOIN social_accounts a ON a.id=t.social_account_id
		  WHERE p.project_id=? AND t.status='published' AND a.status='active'
		    AND (
		      COALESCE(t.platform_post_id,'')<>''
		      OR COALESCE(t.provider_post_id,'')<>''
		      OR COALESCE(t.publish_operation_id,'')<>''
		    )
		  ORDER BY
		    CASE WHEN EXISTS(
		      SELECT 1 FROM social_metric_points mp
		       WHERE mp.project_id=p.project_id AND mp.scope='post'
		         AND mp.post_target_id=t.id AND mp.metric='_refresh'
		    ) THEN 1 ELSE 0 END,
		    COALESCE((
		      SELECT MAX(point_time) FROM social_metric_points mp
		       WHERE mp.project_id=p.project_id AND mp.scope='post'
		         AND mp.post_target_id=t.id AND mp.metric='_refresh'
		    ), ''),
		    t.id
		  LIMIT ?`,
		pid, postAnalyticsCandidateScanMax,
	)
	if err != nil {
		return err
	}
	candidates := make([]postAnalyticsCandidate, 0, postAnalyticsCandidateScanMax)
	for rows.Next() {
		var candidate postAnalyticsCandidate
		if err := rows.Scan(
			&candidate.PostID, &candidate.ProfileID, &candidate.Published,
			&candidate.Target.TargetID, &candidate.Target.SocialAccountID,
			&candidate.Target.ExtPostID, &candidate.Target.ExtURL,
			&candidate.Target.Platform, &candidate.Target.ConnID,
			&candidate.Target.ExternalAccountID, &candidate.Target.PageCreds,
			&candidate.Target.ProviderSlug, &candidate.Target.ProviderAccountID,
			&candidate.Target.ProviderPostID, &candidate.Target.PublishOperationID,
			&candidate.LastRun, &candidate.LastStatus,
		); err == nil {
			candidates = append(candidates, candidate)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}

	now := time.Now().UTC()
	refreshed := 0
	for _, candidate := range candidates {
		if runCtx.Err() != nil {
			return runCtx.Err()
		}
		if refreshed >= postAnalyticsBatchSize {
			break
		}
		if !postAnalyticsDue(candidate.Published, candidate.LastRun, candidate.LastStatus, now) {
			continue
		}
		outcome := a.getPostMetrics(app, candidate.Target)
		if err := persistPostMetricOutcome(app, pid, candidate.ProfileID, candidate.PostID, outcome); err != nil {
			app.Logger().Warn("analytics_collector: post persist failed",
				"project", pid, "post", candidate.PostID, "target", candidate.Target.TargetID, "err", err)
			continue
		}
		refreshed++
		if outcome.Status == "failed" {
			app.Logger().Warn("analytics_collector: post metrics provider failed",
				"project", pid, "post", candidate.PostID, "target", candidate.Target.TargetID,
				"platform", candidate.Target.Platform, "err", outcome.Error)
		}
		if outcome.Status == "ok" {
			app.EmitWithProject("metrics.updated", pid, map[string]any{
				"post_id":              candidate.PostID,
				"post_target_id":       candidate.Target.TargetID,
				"social_account_id":    candidate.Target.SocialAccountID,
				"platform":             candidate.Target.Platform,
				"status":               outcome.Status,
				"automatic_collection": true,
			})
		}
	}
	return nil
}

func postAnalyticsDue(publishedAt, lastRun, lastStatus string, now time.Time) bool {
	if strings.TrimSpace(lastRun) == "" {
		return true
	}
	last, ok := parseMetricPointTime(lastRun)
	if !ok {
		return true
	}
	published, ok := parseMetricPointTime(publishedAt)
	if !ok {
		published = now
	}
	age := now.Sub(published)
	interval := time.Hour
	switch {
	case age > 90*24*time.Hour:
		interval = 7 * 24 * time.Hour
	case age > 30*24*time.Hour:
		interval = 24 * time.Hour
	case age > 7*24*time.Hour:
		interval = 6 * time.Hour
	}
	switch lastStatus {
	case "unsupported", "skipped":
		interval = maxDuration(interval, 7*24*time.Hour)
	case "failed":
		interval = maxDuration(interval, 6*time.Hour)
	}
	return !last.After(now) && now.Sub(last) >= interval
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func accountAnalyticsDue(ctx *sdk.AppCtx, pid string, accountID int64, minInterval time.Duration) (bool, error) {
	latest, err := latestAccountMetricsRefresh(ctx, pid, accountID)
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

func accountBreakdownsDue(ctx *sdk.AppCtx, pid string, accountID int64, minInterval time.Duration) (bool, error) {
	var latest string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(MAX(point_time),'')
		   FROM social_metric_points
		  WHERE project_id=? AND social_account_id=? AND scope='account' AND dimensions_key<>'' AND metric<>'supporters'`,
		pid, accountID,
	).Scan(&latest)
	if err != nil || strings.TrimSpace(latest) == "" {
		return true, err
	}
	t, ok := parseMetricPointTime(latest)
	return !ok || time.Since(t) >= minInterval, nil
}

func (a *App) collectAndStoreAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64, period string, query analyticsQuery) accountMetricsResult {
	if query.RangeDays <= 0 {
		query.RangeDays = 28
	}
	if query.Filters == nil {
		query.Filters = map[string][]string{}
	}
	if !query.ForceRefresh {
		if cached := a.storedAccountMetrics(ctx, pid, accountID, query); cached.Report != nil && cached.UpdatedAt != "" {
			if stamp, err := time.Parse(time.RFC3339, cached.UpdatedAt); err == nil && time.Since(stamp) < analyticsReportTTL(cached.Status) {
				return cached
			}
		}
	}
	res := a.getAccountMetrics(ctx, pid, accountID, period, query)
	res.Raw = sanitizeRawJSON(res.Raw)
	// Cache provider failures briefly so a background tick or another open panel
	// cannot immediately repeat a failed/rate-limited inventory request.
	if res.Status == "failed" && res.Report != nil {
		res.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		raw, err := json.Marshal(res)
		if err == nil {
			_, err = ctx.AppDB().Exec(`INSERT INTO account_metric_cache(project_id,social_account_id,query_key,result,refreshed_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id,social_account_id,query_key) DO UPDATE SET result=excluded.result,refreshed_at=excluded.refreshed_at`, pid, accountID, query.cacheKey(), string(raw), res.UpdatedAt)
		}
		if err != nil {
			ctx.Logger().Warn("account analytics failure cache write failed", "err", err)
		}
		return res
	}
	if len(res.Insights) > 0 {
		res.HistorySource = "social_metric_points"
	}
	persisted := true
	if err := a.persistAccountMetrics(ctx, pid, res, query); err != nil {
		persisted = false
		ctx.Logger().Warn("account_metrics: persist failed",
			"project", pid, "account", accountID, "platform", res.Platform, "err", err)
	}
	if persisted && res.Status == "ok" {
		res.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	}

	if res.Status != "failed" && persisted {
		ctx.EmitWithProject("metrics.updated", pid, map[string]any{
			"social_account_id": accountID,
			"status":            res.Status,
			"updated_at":        res.UpdatedAt,
		})
	}
	return res
}

func (a *App) storedAccountMetrics(ctx *sdk.AppCtx, pid string, accountID int64, query analyticsQuery) accountMetricsResult {
	var platform, displayName, providerSlug, providerAccountID, externalID string
	var profileID, connID int64
	err := ctx.AppDB().QueryRow(
		`SELECT platform, COALESCE(display_name,''), COALESCE(profile_id,0), COALESCE(provider_slug,'native'), connection_id, COALESCE(provider_account_id,''),COALESCE(external_account_id,'')
		   FROM social_accounts WHERE id=? AND project_id=?`,
		accountID, pid,
	).Scan(&platform, &displayName, &profileID, &providerSlug, &connID, &providerAccountID, &externalID)
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
		Capabilities:    analyticsCapabilitiesFor(platform, providerSlug),
	}
	var raw, refreshed string
	if err := ctx.AppDB().QueryRow(`SELECT result,refreshed_at FROM account_metric_cache WHERE project_id=? AND social_account_id=? AND query_key=?`, pid, accountID, query.cacheKey()).Scan(&raw, &refreshed); err == nil {
		var cached accountMetricsResult
		if json.Unmarshal([]byte(raw), &cached) == nil && cached.Report != nil && cached.Report.BindingKey == accountReportBinding(connID, providerSlug, providerAccountID, externalID)+":"+query.DestinationID {
			cached.ProfileID = profileID
			cached.DisplayName = displayName
			cached.UpdatedAt = refreshed
			cached.HistorySource = "social_metric_points"

			return cached
		}
	}
	out.Status = "unsupported"
	out.Reason = "No cached analytics for this exact date range and filters. Refresh to load them."
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
	out.Supporters = latest("supporters")
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
	out.Likes = latest("likes_total")
	out.Comments = latest("comments_total")
	out.Shares = latest("shares_total")
	out.Saves = latest("saves_total")
	out.Sends = latest("sends_total")
	out.Clicks = latest("clicks_total")
}

func (a *App) persistAccountMetrics(ctx *sdk.AppCtx, pid string, res accountMetricsResult, query analyticsQuery) error {
	if res.SocialAccountID <= 0 || pid == "" || res.Platform == "" || res.Status == "failed" {
		return nil
	}
	pointScope := "account"
	if res.Platform == "pinterest" && res.Report != nil && res.Report.Scope == "provider_account" {
		pointScope = "provider_account"
	} else if res.Platform == "pinterest" && query.Scope == "destination" {
		if query.DestinationID == "" {
			if b, err := loadDestinationAccount(ctx, pid, res.SocialAccountID); err == nil {
				query.DestinationID = b.DefaultID
			}
		}
		if query.DestinationID == "" {
			return nil
		}
		pointScope = "destination:" + query.DestinationID
	}
	now := time.Now().UTC().Format(time.RFC3339)
	profileID := res.ProfileID
	if profileID == 0 {
		_ = ctx.AppDB().QueryRow(
			`SELECT COALESCE(profile_id,0) FROM social_accounts WHERE id=? AND project_id=?`,
			res.SocialAccountID, pid,
		).Scan(&profileID)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	source := accountMetricSource(res.Platform, "snapshot")
	if err := insertSocialMetricPointDB(tx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, pointScope, "_refresh", "heartbeat", now, 1, source, "ok", ""); err != nil {
		return err
	}
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
		{"likes", res.Likes},
		{"comments", res.Comments},
		{"shares", res.Shares},
		{"saves", res.Saves},
		{"sends", res.Sends},
		{"clicks", res.Clicks},
		{"pin_clicks", res.PinClicks}, {"outbound_clicks", res.OutboundClicks}, {"video_views", res.VideoViews},
	}
	for _, item := range totals {
		if item.value < 0 || (res.Report != nil && !containsFold(res.Available, item.name)) || (item.value == 0 && !accountMetricKnown(res, item.name)) {
			continue
		}
		if err := insertSocialMetricPointDB(tx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, pointScope, item.name, query.snapshotPeriod(item.name), now, item.value, source, "ok", ""); err != nil {
			return err
		}
	}
	seriesSource := accountMetricSource(res.Platform, "series")
	for metric, points := range res.Insights {
		if len(query.Filters) > 0 || query.PublishedInRange || (res.Report != nil && res.Report.Coverage != nil && !res.Report.Coverage.Complete) {
			continue
		}
		for _, point := range points {
			if point.Partial || point.DataStatus == "pending" || point.DataStatus == "unavailable" {
				continue
			}
			pointTime := normaliseMetricPointTime(point.Time, now)
			if err := insertSocialMetricPointDB(tx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, pointScope, metric, "day", pointTime, point.Value, seriesSource, "ok", ""); err != nil {
				return err
			}
		}
	}
	for _, breakdown := range res.Breakdowns {
		// supporter_level rows are already stored by the supporter snapshot.
		if breakdown.Status != "ok" || breakdown.Dimension == "supporter_level" {
			continue
		}
		for _, row := range breakdown.Rows {
			for metric, value := range row.Metrics {
				source := accountMetricSource(res.Platform, "breakdown") + ":" + breakdown.Dimension
				if err := insertSocialMetricPointDimensionsDB(tx, pid, profileID, res.SocialAccountID, 0, 0, res.Platform, pointScope, metric, query.cacheKey(), now, value, source, "ok", "", row.Dimensions); err != nil {
					return err
				}
			}
		}
	}
	res.UpdatedAt = now
	res.ProfileID = profileID
	res.Raw = sanitizeRawJSON(res.Raw)
	raw, err := json.Marshal(res)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO account_metric_cache(project_id,social_account_id,query_key,result,refreshed_at) VALUES (?,?,?,?,?) ON CONFLICT(project_id,social_account_id,query_key) DO UPDATE SET result=excluded.result,refreshed_at=excluded.refreshed_at`, pid, res.SocialAccountID, query.cacheKey(), string(raw), now); err != nil {
		return err
	}
	// Query results are rebuildable cache data; preserve metric history.
	if _, err = tx.Exec(`DELETE FROM account_metric_cache WHERE project_id=? AND social_account_id=? AND datetime(refreshed_at)<datetime('now','-90 days')`, pid, res.SocialAccountID); err != nil {
		return err
	}
	return tx.Commit()
}

func persistPostMetricOutcome(ctx *sdk.AppCtx, pid string, profileID, postID int64, outcome targetMetricsOutcome) error {
	if pid == "" || outcome.SocialAccountID <= 0 || outcome.TargetID <= 0 || outcome.Platform == "" {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	status := outcome.Status
	if status == "" {
		status = "failed"
	}
	note := strings.TrimSpace(strings.Join(append([]string{outcome.Error, outcome.Reason}, outcome.Warnings...), "; "))
	noteRunes := []rune(note)
	if len(noteRunes) > 500 {
		note = string(noteRunes[:500])
	}
	if err := insertSocialMetricPoint(
		ctx, pid, profileID, outcome.SocialAccountID, postID, outcome.TargetID,
		outcome.Platform, "post", "_refresh", "heartbeat", now, 1,
		outcome.Platform+"_post_refresh", status, note,
	); err != nil {
		return err
	}
	if status != "ok" || outcome.Metrics == nil {
		return nil
	}
	points := []struct {
		name  string
		value int64
	}{
		{"views", outcome.Metrics.Views},
		{"reach", outcome.Metrics.Reach},
		{"likes", outcome.Metrics.Likes},
		{"comments", outcome.Metrics.Comments},
		{"shares", outcome.Metrics.Shares},
		{"saves", outcome.Metrics.Saves},
		{"clicks", outcome.Metrics.Clicks},
		{"impressions", outcome.Metrics.Impressions}, {"video_views", outcome.Metrics.VideoViews}, {"pin_clicks", outcome.Metrics.PinClicks}, {"outbound_clicks", outcome.Metrics.OutboundClicks}, {"engagements", outcome.Metrics.Engagements},
	}
	source := outcome.Platform + "_post_snapshot"
	for _, point := range points {
		if !metricAvailable(outcome.Metrics, point.name) {
			continue
		}
		if err := insertSocialMetricPoint(ctx, pid, profileID, outcome.SocialAccountID, postID, outcome.TargetID, outcome.Platform, "post", point.name, postMetricPeriod(outcome), now, point.value, source, "ok", ""); err != nil {
			return err
		}
	}
	return nil
}

func metricAvailable(metrics *normalizedMetrics, name string) bool {
	if metrics == nil {
		return false
	}
	for _, available := range metrics.Available {
		if available == name {
			return true
		}
	}
	return false
}

func insertSocialMetricPoint(ctx *sdk.AppCtx, pid string, profileID, accountID, postID, targetID int64, platform, scope, metric, period, pointTime string, value int64, source, status, note string) error {
	return insertSocialMetricPointDimensions(ctx, pid, profileID, accountID, postID, targetID, platform, scope, metric, period, pointTime, value, source, status, note, nil)
}

func insertSocialMetricPointDimensions(ctx *sdk.AppCtx, pid string, profileID, accountID, postID, targetID int64, platform, scope, metric, period, pointTime string, value int64, source, status, note string, dimensions map[string]string) error {
	return insertSocialMetricPointDimensionsDB(ctx.AppDB(), pid, profileID, accountID, postID, targetID, platform, scope, metric, period, pointTime, value, source, status, note, dimensions)
}

func insertSocialMetricPointDimensionsDB(db metricDB, pid string, profileID, accountID, postID, targetID int64, platform, scope, metric, period, pointTime string, value int64, source, status, note string, dimensions map[string]string) error {
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
	dimensionsJSON, dimensionsKey := canonicalDimensionsJSON(dimensions)
	_, err := db.Exec(
		`INSERT INTO social_metric_points (
		    project_id, profile_id, social_account_id, post_id, post_target_id,
		    platform, scope, metric, period, point_time, value, source, status, note,
		    dimensions_json, dimensions_key
		  ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		  ON CONFLICT(project_id, scope, social_account_id, post_target_id, metric, period, point_time, source, dimensions_key)
		  DO UPDATE SET
		    value=excluded.value,
		    status=excluded.status,
		    note=excluded.note,
		    created_at=CURRENT_TIMESTAMP`,
		pid, profileID, accountID, postID, targetID, platform, scope, metric, period, pointTime, value, source, status, note, dimensionsJSON, dimensionsKey,
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
		  WHERE project_id=? AND social_account_id=? AND scope='account' AND dimensions_key=''
		    AND status='ok' AND metric<>'_refresh' AND period IN ('snapshot','day') AND point_time >= ?
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
		if period == "snapshot" && (metric == "views" || metric == "reach" || metric == "impressions" || metric == "engagements" ||
			metric == "likes" || metric == "comments" || metric == "shares" || metric == "saves" || metric == "sends" || metric == "clicks") {
			label = metric + "_total"
		}
		out[label] = append(out[label], insightPoint{Time: pointTime, Value: value})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func latestAccountMetricsRefresh(ctx *sdk.AppCtx, pid string, accountID int64) (string, error) {
	var latest string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(MAX(point_time),'')
		   FROM social_metric_points
		  WHERE project_id=? AND social_account_id=? AND (scope='account' OR scope='provider_account' OR scope LIKE 'destination:%')
		    AND (metric='_refresh' OR period='snapshot')`,
		pid, accountID,
	).Scan(&latest)
	return latest, err
}

func loadLatestAccountBreakdowns(ctx *sdk.AppCtx, pid string, accountID int64) []analyticsBreakdown {
	var period string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(period,'') FROM social_metric_points
		  WHERE project_id=? AND social_account_id=? AND scope='account' AND dimensions_key<>'' AND metric<>'supporters'
		  ORDER BY point_time DESC, id DESC LIMIT 1`,
		pid, accountID,
	).Scan(&period)
	if err != nil || period == "" {
		return nil
	}
	return loadAccountBreakdowns(ctx, pid, accountID, period)
}

func loadAccountBreakdowns(ctx *sdk.AppCtx, pid string, accountID int64, period string) []analyticsBreakdown {
	if pid == "" || accountID <= 0 || period == "" {
		return nil
	}

	rows, err := ctx.AppDB().Query(`WITH latest AS (
 SELECT source,MAX(point_time) AS point_time FROM social_metric_points
 WHERE project_id=? AND social_account_id=? AND scope='account' AND period=? AND status='ok' GROUP BY source
 ) SELECT p.metric,p.value,p.dimensions_json,p.source FROM latest
 JOIN social_metric_points p ON p.source=latest.source AND p.point_time=latest.point_time
 WHERE p.project_id=? AND p.social_account_id=? AND p.scope='account' AND p.period=? AND p.status='ok' AND p.dimensions_key<>''
 ORDER BY p.dimensions_key,p.metric`, pid, accountID, period, pid, accountID, period)

	if err != nil {
		return nil
	}
	defer rows.Close()
	type groupedBreakdown struct {
		source string
		rows   map[string]*analyticsBreakdownRow
	}
	groups := map[string]*groupedBreakdown{}
	for rows.Next() {
		var metric, dimensionsJSON, source string
		var value int64
		if rows.Scan(&metric, &value, &dimensionsJSON, &source) != nil {
			continue
		}
		dimensions := map[string]string{}
		if json.Unmarshal([]byte(dimensionsJSON), &dimensions) != nil || len(dimensions) == 0 {
			continue
		}
		dimension := ""
		for _, candidate := range canonicalBreakdownOrder {
			if dimensions[candidate] != "" {
				dimension = candidate
				break
			}
		}
		if dimension == "" {
			continue
		}
		group := groups[dimension]
		if group == nil {
			group = &groupedBreakdown{source: source, rows: map[string]*analyticsBreakdownRow{}}
			groups[dimension] = group
		}
		_, key := canonicalDimensionsJSON(dimensions)
		row := group.rows[key]
		if row == nil {
			row = &analyticsBreakdownRow{Dimensions: dimensions, Metrics: map[string]int64{}}
			group.rows[key] = row
		}
		row.Metrics[metric] = value
	}
	out := []analyticsBreakdown{}
	for _, dimension := range canonicalBreakdownOrder {
		group := groups[dimension]
		if group == nil {
			continue
		}
		breakdown := analyticsBreakdown{Dimension: dimension, Status: "ok", Source: group.source}
		for _, row := range group.rows {
			breakdown.Rows = append(breakdown.Rows, *row)
		}
		sort.SliceStable(breakdown.Rows, func(i, j int) bool {
			return breakdown.Rows[i].Metrics["views"] > breakdown.Rows[j].Metrics["views"]
		})
		out = append(out, breakdown)
	}
	return out
}

func mergeAccountBreakdowns(fresh, stored []analyticsBreakdown) []analyticsBreakdown {
	byDimension := map[string]analyticsBreakdown{}
	for _, breakdown := range stored {
		byDimension[breakdown.Dimension] = breakdown
	}
	for _, breakdown := range fresh {
		if breakdown.Status == "ok" && len(breakdown.Rows) > 0 {
			byDimension[breakdown.Dimension] = breakdown
		} else if _, exists := byDimension[breakdown.Dimension]; !exists {
			byDimension[breakdown.Dimension] = breakdown
		}
	}
	out := make([]analyticsBreakdown, 0, len(byDimension))
	for _, dimension := range canonicalBreakdownOrder {
		if breakdown, ok := byDimension[dimension]; ok {
			out = append(out, breakdown)
		}
	}
	return out
}

func accountMetricSource(platform, kind string) string {
	switch platform {
	case "youtube":
		if kind == "series" {
			return "youtube_analytics"
		}
		if kind == "breakdown" {
			return "youtube_analytics_breakdown"
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
		if kind == "breakdown" {
			return "instagram_insights_breakdown"
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
	query := analyticsQueryFromArgs(args)
	if strings.TrimSpace(toString(args["range"])) == "" && strings.TrimSpace(toString(args["period"])) == "" && query.StartDate == "" && query.EndDate == "" {
		query.RangeDays = 30
	}
	if err := validateAnalyticsQuery(args, query); err != nil {
		return mcpError(err.Error()), nil
	}
	if _, batch := args["post_ids"]; batch && intArg(args, "post_id", 0) > 0 {
		return mcpError("use post_id or post_ids, not both"), nil
	}
	if ids := uniquePositiveInt64s(int64Values(args["post_ids"])); len(ids) > 0 {
		if len(ids) > 20 {
			return mcpError("post_ids supports at most 20 posts"), nil
		}
		results := []any{}
		for _, id := range ids {
			cp := map[string]any{}
			for k, v := range args {
				if k != "post_ids" {
					cp[k] = v
				}
			}
			cp["post_id"] = id
			result, err := a.toolPostMetrics(ctx, cp)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
		return map[string]any{"posts": results}, nil
	}
	ctx = ctx.WithProject(projectScope(ctx, args))
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
		        COALESCE(a.external_account_id,''), COALESCE(a.page_credentials,''),
		        COALESCE(a.provider_slug,'native'),
		        COALESCE(a.provider_account_id,''), COALESCE(t.provider_post_id,'')
		        , COALESCE(t.publish_operation_id,'')
		 FROM post_targets t
		 LEFT JOIN social_accounts a ON a.id=t.social_account_id AND a.project_id=?
		 WHERE t.post_id=?`,
		pid, postID,
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
		if err := rows.Scan(
			&r.TargetID, &r.SocialAccountID, &r.ExtPostID, &r.ExtURL, &platform, &connID,
			&r.ExternalAccountID, &r.PageCreds, &r.ProviderSlug, &r.ProviderAccountID,
			&r.ProviderPostID, &r.PublishOperationID,
		); err == nil {
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
		outcome := a.getPostMetricsQuery(ctx, r, query)
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
	ctx = ctx.WithProject(projectScope(ctx, args))
	id := int64(intArg(args, "social_account_id", 0))
	for _, key := range []string{"period", "range"} {
		if value := strings.TrimSpace(toString(args[key])); value != "" {
			if _, err := accountMetricsHistoryDays(value, defaultAccountMetricsHistoryDays); err != nil {
				return mcpError(err.Error()), nil
			}
		}
	}
	query := analyticsQueryFromArgs(args)
	if err := validateAnalyticsQuery(args, query); err != nil {
		return mcpError(err.Error()), nil
	}
	if len(query.Filters) > 1 {
		return mcpError("only one analytics breakdown filter can be applied at a time; issue separate queries for independent dimensions"), nil
	}
	providerPeriod := "day"
	pid := projectScope(ctx, args)
	ids := int64Values(args["social_account_ids"])
	if id > 0 {
		ids = append([]int64{id}, ids...)
	}
	profileID := int64(intArg(args, "profile_id", 0))
	multiRequest := len(ids) > 1 || (id <= 0 && (len(ids) > 0 || profileID > 0)) || containsFold(query.GroupBy, "social_account")
	if len(ids) == 0 && profileID > 0 {
		rows, err := ctx.AppDB().Query(
			`SELECT id FROM social_accounts WHERE project_id=? AND profile_id=? AND status='active' ORDER BY id`,
			pid, profileID,
		)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var accountID int64
			if rows.Scan(&accountID) == nil {
				ids = append(ids, accountID)
			}
		}
		rows.Close()
	}
	ids = uniquePositiveInt64s(ids)
	if len(ids) == 0 {
		return mcpError("social_account_id, social_account_ids, or profile_id required"), nil
	}
	if len(ids) > 50 {
		return mcpError("at most 50 accounts can be reported in one request"), nil
	}
	results := make([]accountMetricsResult, 0, len(ids))
	identities := map[string]int64{}
	aliases := map[int64]int64{}
	for _, accountID := range ids {
		accountQuery := query
		if accountQuery.Scope == "destination" {
			if b, e := loadDestinationAccount(ctx, pid, accountID); e == nil && adapterForDestinations(b.Platform, b.Provider).Kind != "" {
				if accountQuery.DestinationID == "" {
					accountQuery.DestinationID = b.DefaultID
				}
				if accountQuery.DestinationID != "" && accountQuery.DestinationID != b.DefaultID {
					if _, e := findDestination(ctx, b, accountQuery.DestinationID); e != nil {
						return mcpError(e.Error()), nil
					}
				}
			}
		}
		result := presentAccountReport(a.collectAndStoreAccountMetrics(ctx, pid, accountID, providerPeriod, accountQuery), accountQuery)
		if !boolArg(args, "include_raw", false) {
			result.Raw = nil
		}
		if result.Report != nil && result.Report.Scope == "provider_account" && result.Status == "ok" {
			if first, ok := identities[result.Report.Identity]; ok {
				aliases[accountID] = first
				continue
			}
			identities[result.Report.Identity] = accountID
		}
		results = append(results, result)
	}
	if !multiRequest && len(results) == 1 {
		return results[0], nil
	}
	metric := strings.ToLower(strings.TrimSpace(toString(args["metric"])))
	comparison := aggregateAccountComparison(results, metric, query.Filters)
	return map[string]any{
		"status":          "ok",
		"range":           query.period(),
		"accounts":        results,
		"account_aliases": aliases,
		"comparison":      comparison,
	}, nil
}

func int64Values(value any) []int64 {
	var out []int64
	switch values := value.(type) {
	case []any:
		for _, item := range values {
			if id := toInt64Loose(item); id > 0 {
				out = append(out, id)
			}
		}
	case []int64:
		out = append(out, values...)
	case []int:
		for _, id := range values {
			out = append(out, int64(id))
		}
	case string:
		for _, item := range strings.Split(values, ",") {
			if id, err := strconv.ParseInt(strings.TrimSpace(item), 10, 64); err == nil && id > 0 {
				out = append(out, id)
			}
		}
	}
	return out
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value > 0 && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func accountMetricsHistoryDays(period string, defaultDays int) (int, error) {
	period = strings.ToLower(strings.TrimSpace(period))
	if period == "" {
		return defaultDays, nil
	}
	if !strings.HasSuffix(period, "d") {
		return 0, fmt.Errorf("invalid period: expected Nd, for example 7d or 30d")
	}
	days, err := strconv.Atoi(strings.TrimSuffix(period, "d"))
	if err != nil || days < 1 || days > maxAccountMetricsHistoryDays {
		return 0, fmt.Errorf("invalid period: days must be between 1 and %d", maxAccountMetricsHistoryDays)
	}
	return days, nil
}

// ─── import ───────────────────────────────────────────────────────
//
// Internal-only — NOT exposed as an MCP tool. Agents shouldn't be
// Native history ingestion is shared by explicit panel actions and bounded
// MCP account_history_sync calls. Automatic provider policies remain separate.
// Observations do not overwrite managed post/delivery intent or schedule jobs.

type importResult struct {
	History         *historyReceipt `json:"history,omitempty"`
	AccountID       int64           `json:"account_id"`
	Platform        string          `json:"platform"`
	Imported        int             `json:"imported"`
	SkippedExisting int             `json:"skipped_existing"`
	Status          string          `json:"status"` // ok | unsupported | failed
	Reason          string          `json:"reason,omitempty"`
	Error           string          `json:"error,omitempty"`
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
	case "facebook", "instagram", "pinterest", "tiktok", "twitter", "youtube":
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
	if limit < 1 || limit > 200 {
		limit = 25
	}
	pages := (limit + 49) / 50
	size := limit / pages
	out, err := a.toolAccountHistorySync(ctx.WithProject(pid), map[string]any{"social_account_id": accountID, "mode": "recent", "max_pages": pages, "page_size": size})
	if err != nil {
		return importResult{AccountID: accountID, Status: "failed", Error: err.Error()}
	}
	if receipt, ok := out.(historyReceipt); ok {
		status := "ok"
		if receipt.Status == "failed" {
			status = "failed"
		}
		return importResult{AccountID: accountID, Platform: receipt.Platform, Status: status, Imported: receipt.Imported, SkippedExisting: receipt.Unchanged + receipt.Updated, Error: receipt.Error, History: &receipt}
	}
	if result, ok := out.(map[string]any); ok {
		if result["isError"] == true {
			return importResult{AccountID: accountID, Status: "failed", Error: toString(result["error"])}
		}
		return importResult{AccountID: accountID, Status: "unsupported", Reason: toString(result["limitation"])}
	}
	return importResult{AccountID: accountID, Status: "failed", Error: "Unexpected history result"}
}

func (a *App) insertImportedPost(
	ctx *sdk.AppCtx,
	pid string,
	accountID, profileID int64,
	body, platformPostID, platformURL, publishedAt string,
	mediaURLs []string,
) (bool, error) {
	return a.insertImportedPostWithOptions(
		ctx, pid, accountID, profileID,
		body, platformPostID, platformURL, publishedAt,
		mediaURLs, nil,
	)
}

func (a *App) insertImportedPostWithOptions(
	ctx *sdk.AppCtx,
	pid string,
	accountID, profileID int64,
	body, platformPostID, platformURL, publishedAt string,
	mediaURLs []string,
	targetOptions map[string]any,
) (bool, error) {
	b, err := loadHistoryAccountForRead(ctx.WithProject(pid), accountID)
	if err != nil {
		return false, err
	}
	native := normalizeNativeObservation(b, nativePost{ID: platformPostID, URL: optionalText(platformURL), PublishedAt: optionalText(publishedAt)})
	platformURL, publishedAt = valueOrEmpty(native.URL), valueOrEmpty(native.PublishedAt)
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
	var optionsJSON sql.NullString
	if len(targetOptions) > 0 {
		encoded, err := json.Marshal(targetOptions)
		if err != nil {
			_, _ = ctx.AppDB().Exec(`DELETE FROM posts WHERE id=?`, postID)
			return false, fmt.Errorf("encode imported post target options: %w", err)
		}
		optionsJSON = sql.NullString{String: string(encoded), Valid: true}
	}
	_, err = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status,
		                           platform_post_id, platform_url, published_at, options)
		 VALUES (?, ?, 'published', ?, ?, ?, ?)`,
		postID, accountID, platformPostID, nullable(platformURL), nullable(publishedAt), optionsJSON,
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
	ctx = ctx.WithProject(projectScope(ctx, args))
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
	unlock := lockSocialPost(ctx, postID)
	defer unlock()
	var currentBody, status string
	var approvalRequired bool
	err := ctx.AppDB().QueryRow(
		`SELECT body, status, approval_required FROM posts WHERE id=? AND project_id=?`,
		postID, pid,
	).Scan(&currentBody, &status, &approvalRequired)
	if err != nil {
		return mcpError("post not found"), nil
	}

	if approvalRequired {
		return mcpError("reviewed publications must be changed through a new draft and approval"), nil
	}
	if status != "published" && status != "partial" {
		return mcpError("use post_draft_update for drafts; publishing, scheduled, and review content cannot be edited here"), nil
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
	}
	{
		if _, err := ctx.AppDB().Exec(
			`UPDATE posts SET body=?, revision=revision+1, approved_revision=0, approval_status='not_requested', updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`,
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
			if _, explicit := requested["body"]; !explicit {
				requested["body"] = resolvedPostBody
			}
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
		if targeted || hasBody {
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
		case "pinterest":
			out = a.editPinterestPost(ctx, out, r.ConnID, eff, requested)
		case "tiktok", "instagram":
			out.Status = "unsupported"
			out.Reason = platformEditReason(r.Platform)
		default:
			out.Status = "unsupported"
			out.Reason = "no edit verb wired for this platform yet"
		}
		outcomes = append(outcomes, out)
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var revision int64
	if err = tx.QueryRow(`SELECT revision FROM posts WHERE id=?`, postID).Scan(&revision); err != nil {
		return nil, err
	}
	if err = recordPostRevisionTx(tx, postID, revision, workflowActor(args)); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	return map[string]any{
		"post_id":      postID,
		"updated_body": resolvedPostBody,
		"prior_status": status,
		"targets":      outcomes,
	}, nil
}

func (a *App) editPinterestPost(ctx *sdk.AppCtx, out targetEditOutcome, connID int64, eff, requested map[string]any) targetEditOutcome {
	input := map[string]any{"pin_id": out.PlatformPostID}
	fields := map[string]string{
		"body": "description", "title": "title", "link": "link",
		"alt_text": "alt_text", "board_section_id": "board_section_id",
	}
	for option, upstream := range fields {
		if _, changed := requested[option]; !changed {
			continue
		}
		value, ok := eff[option].(string)
		if !ok {
			value = toString(eff[option])
		}
		input[upstream] = value
	}
	if len(input) == 1 {
		out.Status = "skipped"
		out.Reason = "pinterest edit needs body, title, link, alt_text, or board_section_id"
		return out
	}
	if description, ok := input["description"].(string); ok && utf8.RuneCountInString(description) > 800 {
		out.Status, out.Error = "failed", "pinterest description exceeds 800 characters"
		return out
	}
	if title, ok := input["title"].(string); ok && utf8.RuneCountInString(title) > 100 {
		out.Status, out.Error = "failed", "pinterest title exceeds 100 characters"
		return out
	}
	if alt, ok := input["alt_text"].(string); ok && utf8.RuneCountInString(alt) > 500 {
		out.Status, out.Error = "failed", "pinterest alt text exceeds 500 characters"
		return out
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "update_pin", input)
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
	if _, present := requested["audience"]; present {
		out.Status = "failed"
		out.Error = "Audience choices can only be changed on drafts."
		return out
	}
	if audience, err := parseAudience(eff); err == nil && audience.Type == "members" {
		for _, key := range youtubeMetadataOptions {
			if _, present := requested[key]; present {
				out.Status = "failed"
				out.Error = "Edit membership video metadata and access in YouTube Studio; Social cannot verify these settings."
				return out
			}
		}
	}
	def := platforms["youtube"]
	thumbnail, err := a.resolveThumbnailOption(ctx, eff, mediaProjectID)
	if err != nil {
		out.Status, out.Error = "failed", err.Error()
		return out
	}
	metadataRequested := false
	for _, key := range youtubeMetadataOptions {
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
		// Re-read the video so the displayed visibility/release reflects
		// what YouTube now holds. Best effort: the edit already succeeded.
		_, _ = a.toolPostNativeInspect(ctx, map[string]any{"social_account_id": out.SocialAccountID, "native_post_id": out.PlatformPostID})
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

// youtubeMetadataOptions are the per-target keys that require an
// update_video call when edited.
var youtubeMetadataOptions = []string{"title", "body", "category", "visibility", "tags", "release_at", "contains_synthetic_media"}

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
	privacy := toString(current.Items[0].Status["privacyStatus"])
	if privacy != "public" && privacy != "private" && privacy != "unlisted" {
		return nil, errors.New("current YouTube privacy could not be read; metadata was not changed")
	}
	status := map[string]any{}
	for _, key := range []string{"privacyStatus", "publishAt", "license", "embeddable", "publicStatsViewable", "selfDeclaredMadeForKids", "containsSyntheticMedia"} {
		if v, exists := current.Items[0].Status[key]; exists {
			status[key] = v
		}
	}
	if _, ok := requested["visibility"]; ok {
		status["privacyStatus"] = strOption(eff, "visibility")
		if status["privacyStatus"] != "private" {
			delete(status, "publishAt")
		}
	}
	if _, ok := requested["release_at"]; ok {
		releaseAt, err := youtubeReleaseAt(eff)
		if err != nil {
			return nil, err
		}
		if releaseAt == "" {
			delete(status, "publishAt")
		} else {
			if at, _ := time.Parse(time.RFC3339, releaseAt); !at.After(time.Now()) {
				return nil, errors.New("YouTube release time must be in the future")
			}
			status["privacyStatus"], status["publishAt"] = "private", releaseAt
		}
	}
	// videos.update replaces the status part, and get_video readback can
	// omit containsSyntheticMedia even after YouTube accepted it. The
	// stored disclosure is the source of truth, so an unrelated edit
	// re-sends it instead of letting the omission clear it.
	if synthetic, ok := boolOption(eff, "contains_synthetic_media"); ok {
		status["containsSyntheticMedia"] = synthetic
	}
	input["status"] = status
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
	ctx = ctx.WithProject(projectScope(ctx, args))
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return mcpError("post_id required"), nil
	}
	forceLocal := boolArg(args, "force_local_only", false)
	unlock := lockSocialPost(ctx, postID)
	defer unlock()
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
	if status == "publishing" {
		return mcpError("publication is in flight; wait for confirmation before deleting"), nil
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
	if !forceLocal {
		outcomes = a.deletePostUpstream(ctx, postID)
		for _, out := range outcomes {
			if out.Status == "failed" {
				return map[string]any{"status": "partial", "deleted": false, "post_id": postID, "upstream": outcomes, "error": "remote deletion incomplete; retry deletion"}, nil
			}
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO provider_import_tombstones
		 (social_account_id, provider_slug, provider_post_id, reason)
		 SELECT t.social_account_id, a.provider_slug, t.provider_post_id, 'post_delete'
		   FROM post_targets t JOIN social_accounts a ON a.id=t.social_account_id
		  WHERE t.post_id=? AND COALESCE(a.provider_slug,'native')!='native'
		    AND COALESCE(t.provider_post_id,'')!=''`, postID,
	); err != nil {
		return nil, fmt.Errorf("record provider deletion: %w", err)
	}
	if _, err := tx.Exec(`UPDATE inbox_items SET post_id=NULL WHERE post_id=?`, postID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM social_metric_points WHERE post_id=?`, postID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM target_delivery_attempts WHERE target_id IN (SELECT id FROM post_targets WHERE post_id=?)`, postID); err != nil {
		return nil, err
	}
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
		return []targetDeleteOutcome{{Status: "failed", Error: err.Error()}}
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
		var deleted string
		_ = ctx.AppDB().QueryRow(`SELECT delete_status FROM post_targets WHERE id=?`, r.targetID).Scan(&deleted)
		if deleted == "deleted" {
			out.Status = "deleted"
			outcomes = append(outcomes, out)
			continue
		}
		if r.provider == zernioProviderSlug && r.providerPostID != "" && r.connID.Valid {
			out = a.deleteZernioPost(ctx, out, r.connID.Int64, r.providerPostID)
			outcomes = append(outcomes, out)
			continue
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
	for _, out := range outcomes {
		_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET delete_status=?,delete_error=? WHERE id=?`, out.Status, out.Error, out.TargetID)
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
		args["thread_limit"], _ = strconv.Atoi(r.URL.Query().Get("thread_limit"))
		args["thread_before"], _ = strconv.Atoi(r.URL.Query().Get("thread_before"))
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
			Body             string           `json:"body"`
			Mode             string           `json:"mode"`
			MediaStorageIDs  []int64          `json:"media_storage_ids"`
			MediaProjectID   string           `json:"media_project_id"`
			StorageProjectID string           `json:"storage_project_id"`
			Attachments      []map[string]any `json:"attachments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		args["body"] = body.Body
		if len(body.MediaStorageIDs) > 0 {
			args["media_storage_ids"] = body.MediaStorageIDs
		}
		if body.MediaProjectID != "" {
			args["media_project_id"] = body.MediaProjectID
		} else if body.StorageProjectID != "" {
			args["storage_project_id"] = body.StorageProjectID
		}
		if len(body.Attachments) > 0 {
			args["attachments"] = body.Attachments
		}
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
		ForceNew          bool   `json:"force_new"`
		ConnectionID      *int64 `json:"connection_id"`
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
	args["force_new"] = body.ForceNew
	if body.ConnectionID != nil {
		args["connection_id"] = *body.ConnectionID
	}
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
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	pendingStr := r.URL.Query().Get("pending")
	connStr := r.URL.Query().Get("conn_id")
	status := r.URL.Query().Get("status")
	requestProject := strings.TrimSpace(r.URL.Query().Get("project_id"))
	callbackToken := r.URL.Query().Get("callback_token")
	pendingID, _ := strconv.ParseInt(pendingStr, 10, 64)
	connID, _ := strconv.ParseInt(connStr, 10, 64)
	ready := false
	if pendingID > 0 && requestProject != "" && callbackToken != "" {
		row, rowErr := a.getPending(pendingID)
		if rowErr == nil && row.projectID == requestProject && !row.expired && row.status == "pending_oauth" &&
			oauthCallbackTokenMatches(row.callbackTokenHash, callbackToken) {
			if row.providerSlug == zernioProviderSlug && a.pendingConnectionAllowed(globalCtx, row, row.connectionID) {
				if doneConnID, ok := a.completeZernioOAuth(globalCtx, r, row); ok {
					connID = doneConnID
					ready = true
				}
			} else if connID > 0 && status == "ok" && a.pendingConnectionAllowed(globalCtx, row, connID) {
				res, err := globalCtx.AppDB().Exec(
					`UPDATE pending_accounts SET status='ready', callback_token_hash=''
					  WHERE id=? AND project_id=? AND status='pending_oauth' AND connection_id=?
					    AND callback_token_hash=? AND julianday(expires_at)>julianday(?)`,
					pendingID, row.projectID, connID, row.callbackTokenHash, pendingExpiry(time.Now().UTC()),
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
	} else {
		pendingID = 0
		connID = 0
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
	if row == nil || connID <= 0 || row.connectionID != connID {
		return false
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
	if len(parts) == 2 && parts[1] == "oauth_status" && r.Method == http.MethodGet {
		requestProject := strings.TrimSpace(r.URL.Query().Get("project_id"))
		row, err := a.getPending(id)
		if err != nil || requestProject == "" || row.projectID != requestProject {
			http.Error(w, "pending account not found", http.StatusNotFound)
			return
		}
		status := row.status
		if row.expired && status == "pending_oauth" {
			status = "expired"
		}
		writeJSON(w, map[string]any{"status": status})
		return
	}
	if len(parts) == 2 && parts[1] == "creator-info" && r.Method == http.MethodGet {
		a.handleTikTokCreatorInfo(w, r, id)
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
		args := scopedQueryArgs(r, "period", "range", "start_date", "end_date", "breakdowns", "group_by", "metric", "refresh",
			"scope", "destination_id", "sort_by", "limit", "compare", "published_in_range", "max_posts", "include_raw", "device", "os", "country", "region", "city", "age", "gender", "traffic_source", "audience_type", "content_type", "sharing_service", "video")
		if err := validateAnalyticsQuery(args, analyticsQueryFromArgs(args)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		refresh := strings.TrimSpace(strings.ToLower(toString(args["refresh"])))
		if refresh == "0" || refresh == "false" || refresh == "stored" {
			query := analyticsQueryFromArgs(args)
			if query.Scope == "destination" && query.DestinationID == "" {
				if b, e := loadDestinationAccount(globalCtx, projectScope(globalCtx, args), id); e == nil {
					query.DestinationID = b.DefaultID
				}
			}
			out := presentAccountReport(a.storedAccountMetrics(globalCtx, projectScope(globalCtx, args), id, query), query)
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
	if len(parts) == 2 && (parts[1] == "history-sync" || parts[1] == "history" || parts[1] == "inspect") {
		args := projectArgsFromRequest(r)
		if parts[1] == "history" {
			if r.Method != http.MethodGet {
				http.Error(w, "GET only", 405)
				return
			}
		} else {
			if r.Method != http.MethodPost {
				http.Error(w, "POST only", 405)
				return
			}
			var body struct {
				Mode     string `json:"mode"`
				Cursor   string `json:"cursor"`
				MaxPages int    `json:"max_pages"`
				NativeID string `json:"native_post_id"`
			}
			if e := json.NewDecoder(r.Body).Decode(&body); e != nil && e != io.EOF {
				http.Error(w, "invalid JSON", 400)
				return
			}
			if body.Mode != "" {
				args["mode"] = body.Mode
			}
			if body.Cursor != "" {
				args["cursor"] = body.Cursor
			}
			if body.MaxPages > 0 {
				args["max_pages"] = body.MaxPages
			}
			args["native_post_id"] = body.NativeID
		}
		args["social_account_id"] = id
		var out any
		var err error
		switch parts[1] {
		case "history":
			out, err = a.toolAccountHistoryStatus(globalCtx, args)
		case "history-sync":
			out, err = a.toolAccountHistorySync(globalCtx, args)
		case "inspect":
			out, err = a.toolPostNativeInspect(globalCtx, args)
		}
		if err != nil {
			http.Error(w, redactDeliverySecrets(err.Error()), 500)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "import" && r.Method == http.MethodPost {
		// Legacy bounded recent import; full sync uses history-sync / account_history_sync.
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
	if len(parts) == 2 && parts[1] == "provider-sync" && r.Method == http.MethodPost {
		var body struct {
			Mode string `json:"mode"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		args := projectArgsFromRequest(r)
		args["social_account_id"] = id
		args["mode"] = body.Mode
		out, err := a.toolAccountProviderSyncUpdate(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
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
		out, err := a.listPosts(globalCtx, scopedQueryArgs(r, "profile_id", "profile", "status", "limit", "offset", "from", "to", "social_account_id", "native_post_id", "query", "compact"), 1000)
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
		switch r.Method {
		case http.MethodGet:
			args := projectArgsFromRequest(r)
			post, err := a.loadPostByID(globalCtx, projectScope(globalCtx, args), id)
			if errors.Is(err, sql.ErrNoRows) {
				http.Error(w, "post not found", http.StatusNotFound)
				return
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, map[string]any{"post": post})
			return
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
			http.Error(w, "GET or DELETE only at this path", http.StatusMethodNotAllowed)
			return
		}
	}
	if len(parts) == 4 && parts[1] == "delivery" {
		a.handleTargetDeliveryAction(w, r, id, parts[2], parts[3])
		return
	}
	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		var body struct {
			TargetID int64 `json:"target_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid json", 400)
			return
		}
		args := projectArgsFromRequest(r)
		if body.TargetID != 0 {
			args["target_id"] = body.TargetID
		}
		args["post_id"] = id
		out, err := a.toolPostRetry(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		action := parts[1]
		var handler func(*sdk.AppCtx, map[string]any) (any, error)
		switch action {
		case "draft":
			handler = a.toolPostDraftUpdate
		case "submit":
			handler = a.toolPostDraftSubmit
		case "approve":
			handler = a.toolPostDraftApprove
		case "reject":
			handler = a.toolPostDraftReject
		case "publish":
			handler = a.toolPostDraftPublish
		}
		if handler != nil {
			raw := map[string]any{}
			if r.Body != nil {
				if err := json.NewDecoder(r.Body).Decode(&raw); err != nil && !errors.Is(err, io.EOF) {
					http.Error(w, "invalid json", http.StatusBadRequest)
					return
				}
			}
			raw["post_id"] = id
			actor := strings.TrimSpace(r.Header.Get("X-Apteva-Actor"))
			if actor == "" {
				actor = "panel"
			}
			raw["_caller"] = actor
			copyProjectArgs(raw, projectArgsFromRequest(r))
			out, err := handler(globalCtx, raw)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, out)
			return
		}
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
		args := scopedQueryArgs(r, "range", "start_date", "end_date", "refresh", "include_raw")
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
	ctx = ctx.WithProject(projectScope(ctx, args))
	postID := int64(intArg(args, "post_id", 0))
	if postID <= 0 {
		return map[string]any{"status": "failed", "error": "post_id required"}, nil
	}
	pid := projectScope(ctx, args)
	if pid == "" {
		return map[string]any{"status": "failed", "error": "project required"}, nil
	}
	if generation, ok := optionalInt64Arg(args, "schedule_generation"); ok {
		return a.publishScheduledPost(ctx, pid, postID, generation), nil
	}
	return a.publishScheduledPost(ctx, pid, postID), nil
}

// publishScheduledPost is shared by the brokered app_tool callback and the
// legacy HTTP route. It is idempotent: published targets are never claimed
// again, and a post is acknowledged only when every target was published.
func (a *App) publishScheduledPost(ctx *sdk.AppCtx, pid string, postID int64, expectedGeneration ...int64) map[string]any {
	ctx = ctx.WithProject(pid)
	unlock := lockSocialPost(ctx, postID)
	defer unlock()
	var scheduleAt string
	var generation int64
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(schedule_at,''),schedule_generation FROM posts WHERE id=? AND project_id=?`, postID, pid).Scan(&scheduleAt, &generation); err != nil {
		return map[string]any{"status": "failed", "error": "post not found"}
	}
	if (len(expectedGeneration) > 0 && expectedGeneration[0] != generation) || (scheduleAt != "" && !postScheduleDue(scheduleAt)) {
		return map[string]any{"status": "terminal", "outcome": "obsolete", "post_id": postID}
	}
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
	if postStatus != "scheduled" && postStatus != "publishing" && postStatus != "failed" && postStatus != "awaiting_audience_setup" {
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
		  WHERE post_id=? AND status='failed' AND retryable=1 AND attempts < 4
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

	var active, failed, published, terminal int
	var lastError, failureCode, existingPostID string
	if err := ctx.AppDB().QueryRow(
		`SELECT
		    COALESCE(SUM(CASE WHEN status IN ('pending','publishing') THEN 1 ELSE 0 END),0),
		    COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		    COALESCE(SUM(CASE WHEN status='published' THEN 1 ELSE 0 END),0),
		    COALESCE(SUM(CASE WHEN status='failed' AND retryable=0 THEN 1 ELSE 0 END),0),
		    COALESCE(MAX(CASE WHEN status='failed' THEN last_error END),''),
		    COALESCE(MAX(CASE WHEN status='failed' THEN failure_code END),''),
		    COALESCE(MAX(CASE WHEN status='failed' THEN existing_post_id END),'')
		 FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&active, &failed, &published, &terminal, &lastError, &failureCode, &existingPostID); err != nil {
		return map[string]any{"status": "error", "error": "check publish state: " + err.Error()}
	}
	if active > 0 {
		return map[string]any{"status": "error", "error": "publish still in progress", "post_id": postID}
	}
	if failed > 0 {
		if lastError == "" {
			lastError = "one or more social targets failed"
		}
		if terminal > 0 {
			// Jobs treats failed/error as retryable. terminal acknowledges the
			// callback while preserving the failed Social post for correction.
			return map[string]any{
				"status": "terminal", "outcome": "failed", "retryable": false,
				"error": lastError, "post_id": postID, "failed_targets": failed,
				"failure_code": failureCode, "existing_post_id": existingPostID,
			}
		}
		return map[string]any{"status": "failed", "error": lastError, "post_id": postID, "failed_targets": failed, "retryable": true}
	}
	var awaiting int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=? AND status='awaiting_audience_setup'`, postID).Scan(&awaiting)
	if awaiting > 0 {
		return map[string]any{"status": "terminal", "outcome": "awaiting_audience_setup", "post_id": postID, "awaiting_targets": awaiting, "retryable": false}
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
	if status != "published" && status != "terminal" {
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
			"inbox":            inboxCapabilitiesPayload(def.Inbox),
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
			"inbox": map[string]any{
				"dm_attachment_types": []string{
					inboxAttachmentImage,
					inboxAttachmentVideo,
					inboxAttachmentAudio,
					inboxAttachmentFile,
				},
				"dm_max_attachments": 1,
			},
		})
	}
	writeJSON(w, map[string]any{"platforms": out})
}

func inboxCapabilitiesPayload(caps inboxCaps) map[string]any {
	dmTypes := caps.DMAttachmentTypes
	if dmTypes == nil {
		dmTypes = []string{}
	}
	commentTypes := caps.CommentAttachmentTypes
	if commentTypes == nil {
		commentTypes = []string{}
	}
	return map[string]any{
		"dm_read":                  caps.DMsRead,
		"dm_write":                 caps.DMsWrite,
		"dm_attachment_types":      dmTypes,
		"dm_max_attachments":       caps.DMMaxAttachments,
		"comments_read":            caps.CommentsRead,
		"comments_write":           caps.CommentsWrite,
		"comment_attachment_types": commentTypes,
		"comment_max_attachments":  caps.CommentMaxAttachments,
		"private_reply":            caps.PrivateReply,
	}
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
	callbackTokenHash string
	expired           bool
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var row pendingRow
	var expiresAt string
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, project_id, platform, integration_slug, COALESCE(connection_id,0), status,
		        COALESCE(profile_id,0), COALESCE(provider_slug,''), COALESCE(provider_profile_id,''),
		        COALESCE(provider_state,''), COALESCE(provider_data,''), COALESCE(callback_token_hash,''), COALESCE(expires_at,'')
		 FROM pending_accounts WHERE id=?`, id,
	).Scan(&row.id, &row.projectID, &row.platform, &row.integrationSlug, &row.connectionID, &row.status, &row.profileID, &row.providerSlug, &row.providerProfileID, &row.providerState, &row.providerData, &row.callbackTokenHash, &expiresAt)
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
	const maxPagePages = 10
	const perPage = 100

	// Start with the platform-supplied args and a reasonably high limit.
	// The integration tool ignores unknown keys for GETs (they pass
	// through as query params), so adding limit is safe even if the
	// tool's input_schema doesn't declare it.
	args := map[string]any{}
	for k, v := range def.ListPagesArgs {
		args[k] = v
	}
	limitField := def.ListPagesLimitField
	if limitField == "" {
		limitField = "limit"
	}
	if _, hasLimit := args[limitField]; !hasLimit {
		args[limitField] = perPage
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
		itemsField := def.ListPagesItemsField
		if itemsField == "" {
			itemsField = "data"
		}
		var envelope map[string]any
		if err := json.Unmarshal(res.Data, &envelope); err != nil || envelope == nil {
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
		rawItems, _ := walkPath(envelope, itemsField).([]any)
		if rawItems == nil {
			return nil, fmt.Errorf("parse %s response: missing %s array", def.ListPagesTool, itemsField)
		}
		for _, rawItem := range rawItems {
			p, ok := rawItem.(map[string]any)
			if !ok {
				continue
			}
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
		cursorPath := def.ListPagesCursorResponseField
		if cursorPath == "" {
			cursorPath = "paging.cursors.after"
		}
		cursor := toString(walkPath(envelope, cursorPath))
		if cursor == "" {
			break
		}
		cursorArg := def.ListPagesCursorArg
		if cursorArg == "" {
			cursorArg = "after"
		}
		if toString(args[cursorArg]) == cursor {
			break
		}
		args[cursorArg] = cursor
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
		return ""
	}
	return ""
}

// cacheAvatar fetches an upstream avatar URL and writes it under
// data/avatars/<sha256><ext>, returning the panel-ready local URL
// "/api/apps/social/avatars/<filename>". Idempotent: same upstream
// content → same on-disk filename, second call is a near-no-op.
//
// Failures are logged but never bubble up — the caller stays
// resilient and omits the avatar if the safe cache fetch fails.
func (a *App) cacheAvatar(ctx *sdk.AppCtx, upstreamURL string) string {
	if upstreamURL == "" {
		return ""
	}
	if strings.HasPrefix(upstreamURL, "/api/apps/social/avatars/") {
		// Already cached (e.g. account_disconnect → reconnect on the
		// same connection re-runs finalize with our own URL).
		return upstreamURL
	}
	parsed, err := url.Parse(upstreamURL)
	if err != nil || validateAvatarURL(parsed) != nil {
		return ""
	}
	resp, err := a.avatarClientForRequest().Get(upstreamURL)
	if err != nil {
		ctx.Logger().Warn("avatar fetch failed", "url", redactedMediaURL(upstreamURL), "err", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		ctx.Logger().Warn("avatar fetch non-2xx", "url", redactedMediaURL(upstreamURL), "status", resp.StatusCode)
		return ""
	}
	// 2 MB cap — avatars are typically <50KB. A pathological response
	// is rejected instead of caching truncated bytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(body) > 2<<20 {
		ctx.Logger().Warn("avatar read failed", "url", redactedMediaURL(upstreamURL), "err", err)
		return ""
	}
	ext := extFromContentType(resp.Header.Get("Content-Type"))
	if ext == "" {
		ctx.Logger().Warn("avatar unknown content-type", "url", redactedMediaURL(upstreamURL), "ct", resp.Header.Get("Content-Type"))
		return ""
	}
	sum := sha256.Sum256(body)
	name := hex.EncodeToString(sum[:]) + ext
	dir := avatarsDir()
	if err := os.MkdirAll(dir, 0755); err != nil {
		ctx.Logger().Warn("avatar mkdir failed", "dir", dir, "err", err)
		return ""
	}
	path := filepath.Join(dir, name)
	// Skip the write if the file already exists with the right size.
	if st, err := os.Stat(path); err == nil && st.Size() == int64(len(body)) {
		return "/api/apps/social/avatars/" + name
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, body, 0644); err != nil {
		ctx.Logger().Warn("avatar write failed", "path", tmp, "err", err)
		return ""
	}
	if err := os.Rename(tmp, path); err != nil {
		ctx.Logger().Warn("avatar rename failed", "from", tmp, "to", path, "err", err)
		_ = os.Remove(tmp)
		return ""
	}
	ctx.Logger().Info("avatar cached", "url", redactedMediaURL(upstreamURL), "name", name, "bytes", len(body))
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
	if strings.HasSuffix(strings.ToLower(rest), ".svg") {
		http.Error(w, "SVG avatars are not served", http.StatusUnsupportedMediaType)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
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
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return "", ""
	}
	item := obj
	if data, ok := obj["data"].(map[string]any); ok {
		item = data
	}
	id := toString(item["id"])
	link := firstString(item, "permalink_url", "permalink", "share_url", "url", "platformPostUrl")
	return id, safePublicationURL(platform, link)
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
