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
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: social
display_name: Social
version: 0.2.8
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
	// ProfileTool — integration tool that returns the authorising
	// user's own identity (used to seed display_name/avatar for
	// platforms without page-selection). Empty = use a default label.
	ProfileTool          string
	ProfileNameField     string
	ProfileAvatarField   string
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
	},
	"instagram": {
		Platform:        "instagram",
		IntegrationSlug: "instagram-api",
		DisplayName:     "Instagram Business",
		// Two-step: create_media_container({imageUrl|videoUrl, caption,
		// instagramAccountId}) then publish_media_container({containerId,
		// instagramAccountId}). Caption is the body; media required.
		Strategy:        "instagram_two_step",
		PostTool:        "create_media_container",
		PublishTool:     "publish_media_container",
		BodyField:       "caption",
		MediaURLField:   "imageUrl",
		ExternalIDField: "instagramAccountId",
		MediaRequired:   true,
		MediaType:       "image",
		ListPagesTool:   "list_accounts",
		PageIDField:     "instagramAccountId",
		PageNameField:   "username",
		PageAvatarField: "profile_picture_url",
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
		MediaType:          "video",
		ProfileTool:        "get_creator_info",
		ProfileNameField:   "creator_username",
		ProfileAvatarField: "creator_avatar_url",
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
		Strategy:           "youtube_unsupported",
		PostTool:           "upload_video",
		MediaRequired:      true,
		MediaType:          "video",
		ProfileTool:        "get_my_channel",
		ProfileNameField:   "snippet.title",
		ProfileAvatarField: "snippet.thumbnails.default.url",
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
		// Jobs callback — the jobs app POSTs here when a scheduled
		// publish fires. Body: {"post_id": N}. Idempotent per post:
		// running it twice on a published post is a no-op (publishPostTargets
		// only acts on status='pending' targets).
		{Pattern: "/jobs/publish_post", Handler: a.handleJobPublishPost},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
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
			Name:        "post_create",
			Description: "Create a post and publish (or schedule) it to N social accounts. Args: body, social_account_ids[], schedule_at? (RFC3339; omit = post now), media_storage_ids? (file ids from the storage app).",
			InputSchema: schemaObject(map[string]any{
				"body":               map[string]any{"type": "string"},
				"social_account_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"schedule_at":        map[string]any{"type": "string"},
				"media_storage_ids":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"body", "social_account_ids"}),
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
	}
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
		err := ctx.AppDB().QueryRow(
			`SELECT connection_id FROM social_accounts
			 WHERE project_id=? AND platform=? AND status='active'
			 ORDER BY id DESC LIMIT 1`,
			pid, def.Platform,
		).Scan(&existingConnID)
		if err != nil || existingConnID == 0 {
			if conns, lerr := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{
				ProjectID: pid,
				AppSlug:   def.IntegrationSlug,
			}); lerr == nil {
				for _, c := range conns {
					if c.Status == "active" {
						existingConnID = c.ID
						break
					}
				}
			}
		}
		if existingConnID > 0 {
			res, err := ctx.AppDB().Exec(
				`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
				 VALUES (?, ?, ?, ?, 'ready', ?)`,
				pid, def.Platform, def.IntegrationSlug, existingConnID,
				time.Now().UTC().Add(10*time.Minute),
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
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at)
		 VALUES (?, ?, ?, 'pending_oauth', ?)`,
		pid, def.Platform, def.IntegrationSlug, now.Add(10*time.Minute),
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
		return mcpError("pending account not found: " + err.Error()), nil
	}
	if row.connectionID == 0 {
		return mcpError("OAuth not yet complete — open the authorize_url first, then re-call this tool"), nil
	}
	def := platforms[row.platform]
	if def.ListPagesTool == "" {
		// Platforms without setup step: signal "ready, no picker needed".
		return map[string]any{
			"pages":           []any{},
			"requires_picker": false,
			"hint":            fmt.Sprintf("%s has no page-selection step — call account_finalize with this pending_account_id (no page_id needed)", def.DisplayName),
		}, nil
	}
	pages, err := a.fetchPages(ctx, row.connectionID, def)
	if err != nil {
		return mcpError("fetch pages failed: " + err.Error()), nil
	}
	return map[string]any{
		"pages":           pages,
		"requires_picker": true,
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

	// Insert the finalized social_account row.
	pid := os.Getenv("APTEVA_PROJECT_ID")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, avatar_url, status)
		 VALUES (?, ?, ?, ?, ?, ?, 'active')`,
		pid, def.Platform, row.connectionID, nullable(pageID), displayName, nullable(avatar),
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
	q := `SELECT id, platform, connection_id, COALESCE(external_account_id,''), display_name,
	             COALESCE(avatar_url,''), status, created_at
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
	q += " ORDER BY id DESC"
	rows, err := ctx.AppDB().Query(q, qArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var (
			id, connID                                            int64
			platform, externalID, name, avatar, status, createdAt string
		)
		if err := rows.Scan(&id, &platform, &connID, &externalID, &name, &avatar, &status, &createdAt); err != nil {
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

func (a *App) toolPostCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	body, _ := args["body"].(string)
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("body required")
	}
	rawAccts, _ := args["social_account_ids"].([]any)
	if len(rawAccts) == 0 {
		return nil, errors.New("social_account_ids required (at least one)")
	}
	acctIDs := make([]int64, 0, len(rawAccts))
	for _, v := range rawAccts {
		switch n := v.(type) {
		case float64:
			acctIDs = append(acctIDs, int64(n))
		case int64:
			acctIDs = append(acctIDs, n)
		case int:
			acctIDs = append(acctIDs, int64(n))
		}
	}
	scheduleAt, _ := args["schedule_at"].(string)
	mediaIDsRaw, _ := args["media_storage_ids"].([]any)
	mediaIDs := []int64{}
	for _, v := range mediaIDsRaw {
		switch n := v.(type) {
		case float64:
			mediaIDs = append(mediaIDs, int64(n))
		case int64:
			mediaIDs = append(mediaIDs, n)
		case int:
			mediaIDs = append(mediaIDs, int64(n))
		}
	}
	mediaJSON, _ := json.Marshal(mediaIDs)

	pid := os.Getenv("APTEVA_PROJECT_ID")
	status := "publishing"
	if scheduleAt != "" {
		status = "scheduled"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, media_storage_ids, schedule_at, status)
		 VALUES (?, ?, ?, ?, ?)`,
		pid, body, string(mediaJSON), nullable(scheduleAt), status,
	)
	if err != nil {
		return nil, err
	}
	postID, _ := res.LastInsertId()

	// Fan out: one target row per requested social account.
	for _, aid := range acctIDs {
		_, err := ctx.AppDB().Exec(
			`INSERT INTO post_targets (post_id, social_account_id) VALUES (?, ?)`,
			postID, aid,
		)
		if err != nil {
			ctx.Logger().Warn("create post_target failed", "post", postID, "account", aid, "err", err)
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
		if err := a.scheduleJob(ctx, postID, scheduleAt); err != nil {
			ctx.Logger().Warn("schedule via jobs failed", "post", postID, "err", err)
			// Roll the post back to draft so the operator can retry.
			_, _ = ctx.AppDB().Exec(`UPDATE posts SET status='failed' WHERE id=?`, postID)
			return mcpError("scheduling failed (is the jobs app bound?): " + err.Error()), nil
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
	body             string
	mediaURLs        []string // already-resolved public URLs
}

// publishPostTargets walks every pending target on a post and tries to
// publish it. Each target runs through the platform-specific strategy
// (single, instagram_two_step, tiktok, …). Media URLs are resolved up
// front via storage.files_get_url so each strategy gets a flat list.
func (a *App) publishPostTargets(ctx *sdk.AppCtx, postID int64) {
	// Load the post's media ids once — every target gets the same media.
	mediaIDs := a.loadPostMedia(ctx, postID)
	mediaURLs, mediaErr := a.resolveMediaURLs(ctx, mediaIDs)
	if mediaErr != nil {
		ctx.Logger().Warn("resolve media urls", "post", postID, "err", mediaErr)
		// Don't abort — text-only platforms can still publish; media-
		// required platforms will fall through to the strategy's own
		// "no media" branch and surface the error per-target.
	}

	rows, err := ctx.AppDB().Query(
		`SELECT t.id, t.social_account_id, a.platform, a.connection_id,
		        COALESCE(a.external_account_id,''), p.body
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
		if err := rows.Scan(&j.targetID, &acctID, &j.platform, &j.connID, &j.extID, &j.body); err == nil {
			j.mediaURLs = mediaURLs
			jobs = append(jobs, j)
		}
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
		if def.MediaRequired && len(j.mediaURLs) == 0 {
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
	case "youtube_unsupported":
		return "", "", errors.New("YouTube video upload via the resumable-upload protocol lands in v0.2 — connect the channel and we'll wire it up")
	default: // "single" or empty
		return a.publishSingle(ctx, def, j)
	}
}

// publishSingle covers Twitter / Facebook / LinkedIn — a single
// integration tool call with a flat input shape.
func (a *App) publishSingle(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	bodyField := def.BodyField
	if bodyField == "" {
		bodyField = "text"
	}
	input := map[string]any{bodyField: j.body}
	if def.ExternalIDField != "" && j.extID != "" {
		input[def.ExternalIDField] = j.extID
	}
	if def.MediaURLField != "" && len(j.mediaURLs) > 0 {
		input[def.MediaURLField] = j.mediaURLs[0]
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
	if err != nil {
		return "", "", err
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	id, url := extractPostIdentity(def.Platform, out.Data)
	return id, url, nil
}

// publishInstagram runs the two-step IG dance: create_media_container
// (with imageUrl + caption) → publish_media_container (with the
// containerId returned by step 1).
func (a *App) publishInstagram(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.mediaURLs) == 0 {
		return "", "", errors.New("instagram requires media")
	}
	// Step 1: create_media_container.
	containerInput := map[string]any{
		"caption":            j.body,
		"imageUrl":           j.mediaURLs[0],
		"instagramAccountId": j.extID,
	}
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
	// Step 2: publish_media_container.
	publishInput := map[string]any{
		"containerId":        containerID,
		"instagramAccountId": j.extID,
	}
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

// publishTikTok builds the nested {post_info, source_info} shape from
// our flat (body, media_url) inputs. Caption goes to post_info.title;
// media goes to source_info.video_url with PULL_FROM_URL.
func (a *App) publishTikTok(ctx *sdk.AppCtx, def platformDef, j publishJob) (string, string, error) {
	if len(j.mediaURLs) == 0 {
		return "", "", errors.New("tiktok requires a video URL")
	}
	input := map[string]any{
		"post_info": map[string]any{
			"title":         j.body,
			"privacy_level": "PUBLIC_TO_EVERYONE", // sensible default; future: per-target override
		},
		"source_info": map[string]any{
			"source":    "PULL_FROM_URL",
			"video_url": j.mediaURLs[0],
		},
	}
	out, err := ctx.PlatformAPI().ExecuteIntegrationTool(j.connID, def.PostTool, input)
	if err != nil {
		return "", "", err
	}
	if out == nil || !out.Success {
		return "", "", upstreamError(out)
	}
	// TikTok returns a publish_id (async processing); the actual post
	// URL isn't known until the worker polls get_publish_status. v0.1
	// records the publish_id; v0.2 schedules a follow-up status check.
	pubID := extractTikTokPublishID(out.Data)
	return pubID, "", nil
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

// resolveMediaURLs turns storage file ids into absolute, publicly
// fetchable URLs. Calls storage.files_get_url for each id, gets a
// signed relative URL, and prefixes APTEVA_PUBLIC_URL (or, for local
// dev, http://127.0.0.1:5280). The URL must be reachable from the
// social platform's servers — for local dev, point APTEVA_PUBLIC_URL
// at an ngrok tunnel.
func (a *App) resolveMediaURLs(ctx *sdk.AppCtx, ids []int64) ([]string, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	publicBase := os.Getenv("APTEVA_PUBLIC_URL")
	if publicBase == "" {
		publicBase = "http://127.0.0.1:5280"
	}
	publicBase = strings.TrimRight(publicBase, "/")

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		res, err := ctx.PlatformAPI().CallApp("storage", "files_get_url", map[string]any{
			"id":          id,
			"ttl_seconds": 3600,
		})
		if err != nil {
			return nil, fmt.Errorf("storage files_get_url(%d): %w", id, err)
		}
		// Storage's MCP response is wrapped in {content:[{type:text, text:json}]}.
		// The inner JSON is {url, expires_at, file_id}.
		rel := extractStorageGetURL(res)
		if rel == "" {
			return nil, fmt.Errorf("storage files_get_url(%d) returned no url: %s", id, string(res))
		}
		// Storage returns a relative path — combine with the platform
		// proxy prefix and the public base.
		if strings.HasPrefix(rel, "http://") || strings.HasPrefix(rel, "https://") {
			out = append(out, rel)
		} else if strings.HasPrefix(rel, "/api/apps/storage/") {
			out = append(out, publicBase+rel)
		} else {
			// Path is /files/<id>/content?…  — prefix with the proxy path.
			out = append(out, publicBase+"/api/apps/storage"+rel)
		}
	}
	return out, nil
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

// scheduleJob hands off to the jobs app: register an HTTP-target job
// that calls back into /api/apps/social/jobs/publish_post when its
// scheduled time arrives. Idempotency keyed on post_id so dupes coalesce.
func (a *App) scheduleJob(ctx *sdk.AppCtx, postID int64, scheduleAt string) error {
	jobsBound := ctx.IntegrationFor("jobs")
	if jobsBound == nil {
		return errors.New("jobs app not bound — bind it at install time to enable durable scheduling")
	}
	payload := map[string]any{"post_id": postID}
	payloadJSON, _ := json.Marshal(payload)
	res, err := ctx.PlatformAPI().CallApp("jobs", "jobs_schedule", map[string]any{
		"name": fmt.Sprintf("social.publish_post.%d", postID),
		"schedule": map[string]any{
			"kind":   "once",
			"run_at": scheduleAt,
		},
		"target": map[string]any{
			"kind": "http",
			"url":  "/api/apps/social/jobs/publish_post",
			"body": string(payloadJSON),
		},
		"idempotency_key": fmt.Sprintf("social.post.%d", postID),
		"max_retries":     3,
		"backoff_seconds": 60,
		"owner_app":       "social",
	})
	if err != nil {
		return err
	}
	_ = res // best-effort; the job's id isn't tracked in our DB today
	return nil
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
	q := `SELECT id, body, COALESCE(media_storage_ids,'[]'), COALESCE(schedule_at,''),
	             status, created_at, COALESCE(published_at,'')
	      FROM posts WHERE project_id=?`
	qArgs := []any{pid}
	if statusFilter != "" {
		q += " AND status=?"
		qArgs = append(qArgs, statusFilter)
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
			id                                                   int64
			body, mediaJSON, schedAt, status, createdAt, pubAt   string
		)
		if err := rows.Scan(&id, &body, &mediaJSON, &schedAt, &status, &createdAt, &pubAt); err != nil {
			continue
		}
		var mediaIDs []int64
		_ = json.Unmarshal([]byte(mediaJSON), &mediaIDs)
		targets := a.loadTargets(ctx, id)
		out = append(out, map[string]any{
			"id":                id,
			"body":              body,
			"media_storage_ids": mediaIDs,
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
		var body struct {
			Body              string  `json:"body"`
			SocialAccountIDs  []int64 `json:"social_account_ids"`
			ScheduleAt        string  `json:"schedule_at"`
			MediaStorageIDs   []int64 `json:"media_storage_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		args := map[string]any{
			"body":               body.Body,
			"social_account_ids": int64SliceToAny(body.SocialAccountIDs),
		}
		if body.ScheduleAt != "" {
			args["schedule_at"] = body.ScheduleAt
		}
		if len(body.MediaStorageIDs) > 0 {
			args["media_storage_ids"] = int64SliceToAny(body.MediaStorageIDs)
		}
		out, err := a.toolPostCreate(globalCtx, args)
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
	if len(parts) == 2 && parts[1] == "retry" && r.Method == http.MethodPost {
		out, err := a.toolPostRetry(globalCtx, map[string]any{"post_id": id})
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
		out = append(out, map[string]any{
			"platform":         def.Platform,
			"display_name":     def.DisplayName,
			"integration_slug": def.IntegrationSlug,
			"requires_picker":  def.ListPagesTool != "",
			"available":        available,
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
}

func (a *App) getPending(id int64) (*pendingRow, error) {
	var row pendingRow
	err := globalCtx.AppDB().QueryRow(
		`SELECT id, platform, integration_slug, COALESCE(connection_id,0), status
		 FROM pending_accounts WHERE id=?`, id,
	).Scan(&row.id, &row.platform, &row.integrationSlug, &row.connectionID, &row.status)
	if err != nil {
		return nil, err
	}
	return &row, nil
}

type pageEntry struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Avatar string `json:"avatar_url"`
}

// fetchPages calls the integration's list_pages tool and normalises the
// result via the platformDef's PageIDField / PageNameField / PageAvatarField.
// Supports dotted paths in field names ("picture.data.url" → walk objects).
func (a *App) fetchPages(ctx *sdk.AppCtx, connID int64, def platformDef) ([]pageEntry, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ListPagesTool, map[string]any{})
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
	// Most integrations return {data: [...]} — common Graph/Twitter shape.
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &envelope); err != nil || envelope.Data == nil {
		// Fall back to "raw is the array".
		var raw []map[string]any
		if err2 := json.Unmarshal(res.Data, &raw); err2 != nil {
			return nil, fmt.Errorf("parse list_pages response: %w", err)
		}
		envelope.Data = raw
	}
	pages := make([]pageEntry, 0, len(envelope.Data))
	for _, p := range envelope.Data {
		pages = append(pages, pageEntry{
			ID:     toString(walkPath(p, def.PageIDField)),
			Name:   toString(walkPath(p, def.PageNameField)),
			Avatar: toString(walkPath(p, def.PageAvatarField)),
		})
	}
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
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, def.ProfileTool, map[string]any{})
	if err != nil || res == nil || !res.Success {
		return nil, err
	}
	var raw map[string]any
	_ = json.Unmarshal(res.Data, &raw)
	// Twitter returns {data: {...}}. Try inner first.
	if inner, ok := raw["data"].(map[string]any); ok {
		raw = inner
	}
	return &profileEntry{
		Name:   toString(walkPath(raw, def.ProfileNameField)),
		Avatar: toString(walkPath(raw, def.ProfileAvatarField)),
	}, nil
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

func platformKeys() []string {
	out := make([]string, 0, len(platforms))
	for k := range platforms {
		out = append(out, k)
	}
	return out
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
