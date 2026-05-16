// Apteva Podcast v0.1.0 — podcast hosting + management.
//
// This app owns shows, episodes and the RSS feed. It does NOT own
// audio bytes (storage), audio probing/transcripts (media), download
// analytics (analytics), ingress (routes) or DNS (domains) — each of
// those is a sibling app reached via ctx.PlatformAPI().CallAppResult.
//
// HTTP surface:
//
//	/api/shows[/...]      panel + agent REST mirror      (auth)
//	/api/episodes[/...]   panel + agent REST mirror      (auth)
//	/feed/{slug}.xml      public RSS 2.0 + iTunes feed   (NoAuth)
//	/e/{guid}             download tracking redirect     (NoAuth)
//	/art/{kind}/{id}      cover-art passthrough          (NoAuth)
//
// File map:
//
//	main.go         App wiring — manifest, lifecycle, route/tool/worker lists
//	store.go        shows + episodes SQLite reads/writes
//	tools.go        MCP tool handlers + arg helpers
//	handlers.go     HTTP handlers + download dedupe
//	feed.go         RSS rendering + feed URL helpers + feed cache
//	integration.go  cross-app calls: storage, media, analytics, routes, domains
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ─────────────────────────────────────────────
//
// Trimmed copy of apteva.yaml — the runtime parses this; apteva.yaml
// is the source-of-truth the installer reads. Keep the two in sync.

const manifestYAML = `schema: apteva-app/v1
name: podcast
display_name: Podcast
version: 0.1.0
description: |
  Podcast hosting + management for Apteva. Owns shows, episodes and the
  RSS feed; composes storage (audio), media (probe + transcripts),
  analytics (downloads), routes + domains (custom feed hostname).
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - { name: storage,   reason: "Audio hosting" }
    - { name: media,     reason: "Audio probe + transcripts" }
    - { name: analytics, optional: true, reason: "Download analytics" }
    - { name: routes,    optional: true, reason: "Custom feed hostname" }
    - { name: domains,   optional: true, reason: "DNS auto-config" }
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: show_create,       description: "Create a podcast show." }
    - { name: show_update,       description: "Update a show by id." }
    - { name: show_get,          description: "Fetch one show by id." }
    - { name: show_list,         description: "List shows." }
    - { name: show_delete,       description: "Delete a show and its episodes." }
    - { name: episode_create,    description: "Create an episode (status 'draft')." }
    - { name: episode_update,    description: "Update an episode by id." }
    - { name: episode_get,       description: "Fetch one episode by id." }
    - { name: episode_list,      description: "List episodes." }
    - { name: episode_delete,    description: "Delete an episode." }
    - { name: episode_set_audio, description: "Attach audio; probe via media." }
    - { name: episode_publish,   description: "Publish an episode immediately." }
    - { name: episode_unpublish, description: "Move a published episode back to draft." }
    - { name: episode_schedule,  description: "Schedule an episode to publish later." }
    - { name: feed_get_url,      description: "Return a show's public RSS feed URL." }
    - { name: feed_validate,     description: "Dry-run feed health check." }
  ui_panels:
    - { slot: project.page, label: "Podcast", icon: mic, entry: /ui/PodcastPanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/podcast
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/podcast.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("podcast requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("podcast mounted", "data_dir", ctx.DataDir())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── Workers ───────────────────────────────────────────────────────

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "scheduled-publish",
			Schedule: "@every 1m",
			Run:      a.runScheduledPublish,
		},
	}
}

// runScheduledPublish flips episodes whose status='scheduled' and
// publish_at <= now to 'published', stamping published_at and busting
// the affected feeds' caches.
func (a *App) runScheduledPublish(ctx context.Context, app *sdk.AppCtx) error {
	due, err := dbListDueScheduled(app.AppDB())
	if err != nil {
		return err
	}
	for i := range due {
		ep := &due[i]
		if err := assertPublishable(ep); err != nil {
			app.Logger().Warn("scheduled episode not publishable, skipping",
				"episode", ep.ID, "err", err.Error())
			continue
		}
		now := sqliteTime(time.Now())
		if err := dbSetEpisodeStatus(app.AppDB(), ep.ID, "published", nil, &now); err != nil {
			app.Logger().Error("scheduled publish failed", "episode", ep.ID, "err", err.Error())
			continue
		}
		bustFeed(ep.ShowID)
		app.Logger().Info("episode auto-published", "episode", ep.ID, "show", ep.ShowID)
	}
	return nil
}

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Panel + agent REST mirror — auth required.
		{Pattern: "/api/shows", Handler: a.handleShowsCollection},
		{Pattern: "/api/shows/", Handler: a.handleShowItem},
		{Pattern: "/api/episodes", Handler: a.handleEpisodesCollection},
		{Pattern: "/api/episodes/", Handler: a.handleEpisodeItem},

		// Public RSS feed — podcast clients carry no APTEVA_APP_TOKEN.
		{Pattern: "/feed/", Handler: a.handlePublicFeed, NoAuth: true},

		// Public download tracking redirect: log the hit, 302 to the
		// storage enclosure URL. Reachable by every podcast player.
		{Pattern: "/e/", Handler: a.handleDownloadRedirect, NoAuth: true},

		// Public cover-art passthrough: resolves a storage file id to
		// a URL so <itunes:image> can carry an absolute href.
		{Pattern: "/art/", Handler: a.handleArt, NoAuth: true},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	str := map[string]any{"type": "string"}
	integer := map[string]any{"type": "integer"}
	boolean := map[string]any{"type": "boolean"}

	return []sdk.Tool{
		// ── shows ──
		{
			Name:        "show_create",
			Description: "Create a podcast show. Args: title (req), description?, author?, owner_email?, language? (default 'en'), category?, explicit? (default false), link?, podcast_type? ('episodic'|'serial'), image_file_id?, hostname?, slug?, project_id? (scope=global).",
			InputSchema: schemaObject(map[string]any{
				"title": str, "description": str, "author": str,
				"owner_email": str, "language": str, "category": str,
				"explicit": boolean, "link": str, "podcast_type": str,
				"image_file_id": str, "hostname": str, "slug": str,
				"project_id": str,
			}, []string{"title"}),
			Handler: a.toolShowCreate,
		},
		{
			Name:        "show_update",
			Description: "Update a show by id; only provided fields change.",
			InputSchema: schemaObject(map[string]any{
				"id": integer, "title": str, "description": str,
				"author": str, "owner_email": str, "language": str,
				"category": str, "explicit": boolean, "link": str,
				"podcast_type": str, "image_file_id": str,
				"hostname": str, "slug": str,
			}, []string{"id"}),
			Handler: a.toolShowUpdate,
		},
		{
			Name:        "show_get",
			Description: "Fetch one show by id, including its feed URL. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolShowGet,
		},
		{
			Name:        "show_list",
			Description: "List shows. Args: project_id?, limit? (default 100), offset? (default 0).",
			InputSchema: schemaObject(map[string]any{
				"project_id": str, "limit": integer, "offset": integer,
			}, nil),
			Handler: a.toolShowList,
		},
		{
			Name:        "show_delete",
			Description: "Delete a show and all its episodes. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolShowDelete,
		},

		// ── episodes ──
		{
			Name:        "episode_create",
			Description: "Create an episode (status starts 'draft'). Args: show_id (req), title (req), description? (show notes HTML), season_number?, episode_number?, episode_type? ('full'|'trailer'|'bonus', default 'full'), audio_file_id?, image_file_id?, guid?.",
			InputSchema: schemaObject(map[string]any{
				"show_id": integer, "title": str, "description": str,
				"season_number": integer, "episode_number": integer,
				"episode_type": str, "audio_file_id": str,
				"image_file_id": str, "guid": str,
			}, []string{"show_id", "title"}),
			Handler: a.toolEpisodeCreate,
		},
		{
			Name:        "episode_update",
			Description: "Update an episode by id; only provided fields change.",
			InputSchema: schemaObject(map[string]any{
				"id": integer, "title": str, "description": str,
				"season_number": integer, "episode_number": integer,
				"episode_type": str, "image_file_id": str,
			}, []string{"id"}),
			Handler: a.toolEpisodeUpdate,
		},
		{
			Name:        "episode_get",
			Description: "Fetch one episode by id. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolEpisodeGet,
		},
		{
			Name:        "episode_list",
			Description: "List episodes. Args: show_id? (filter), status? ('draft'|'scheduled'|'published'), limit? (default 100), offset? (default 0).",
			InputSchema: schemaObject(map[string]any{
				"show_id": integer, "status": str,
				"limit": integer, "offset": integer,
			}, nil),
			Handler: a.toolEpisodeList,
		},
		{
			Name:        "episode_delete",
			Description: "Delete an episode. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolEpisodeDelete,
		},
		{
			Name:        "episode_set_audio",
			Description: "Attach/replace an episode's audio file. Calls storage for byte length + mime and media for exact duration, caching them on the episode. Args: id (req), audio_file_id (req storage file id).",
			InputSchema: schemaObject(map[string]any{
				"id": integer, "audio_file_id": str,
			}, []string{"id", "audio_file_id"}),
			Handler: a.toolEpisodeSetAudio,
		},
		{
			Name:        "episode_publish",
			Description: "Publish an episode immediately. Requires an attached, probed audio file. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolEpisodePublish,
		},
		{
			Name:        "episode_unpublish",
			Description: "Move a published episode back to draft (drops it from the feed). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": integer}, []string{"id"}),
			Handler:     a.toolEpisodeUnpublish,
		},
		{
			Name:        "episode_schedule",
			Description: "Schedule an episode to publish at a future time. Args: id (req), publish_at (req RFC3339).",
			InputSchema: schemaObject(map[string]any{
				"id": integer, "publish_at": str,
			}, []string{"id", "publish_at"}),
			Handler: a.toolEpisodeSchedule,
		},

		// ── feed ──
		{
			Name:        "feed_get_url",
			Description: "Return the public RSS feed URL for a show. Args: show_id.",
			InputSchema: schemaObject(map[string]any{"show_id": integer}, []string{"show_id"}),
			Handler:     a.toolFeedGetURL,
		},
		{
			Name:        "feed_validate",
			Description: "Dry-run feed health check: reports episodes missing audio/duration, and show-level fields directories reject feeds for. Args: show_id.",
			InputSchema: schemaObject(map[string]any{"show_id": integer}, []string{"show_id"}),
			Handler:     a.toolFeedValidate,
		},
	}
}

// ─── helpers ───────────────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "encode: %v\n", err)
	}
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(msg))
}
