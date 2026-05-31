// Apteva Community app — Circle/Skool-shaped community platform.
//
// 0.1 scope: multiple communities per install, members, spaces (feed/
// forum/chat), threads, posts, reactions, and DMs. Panel updates live
// via the platform event bus (ctx.Emit on every mutation). No hard
// dependencies on other apps.
//
// Each handler emits domain events the platform fans out to the panel:
//
//	community.created   member.joined        space.created
//	thread.created      post.created         post.reacted
//	post.edited         post.removed         dm.received
//
// Topics are stamped with the "community" app prefix by the platform
// before fanout, so callers in this file use the unprefixed form.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: community
display_name: Community
version: 0.2.1
description: |
  Circle/Skool-shaped community platform. Multiple communities per install,
  spaces (feed/forum/chat/course), members, threads, posts, reactions,
  in-app DMs, and courses (sections + lessons + progress). Panel updates
  live via the platform event bus.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/community
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/community/icon.svg
tags: [community, courses, membership, forum, dms]
scopes: [project, global]
min_apteva_version: "0.10.0"
requires:
  permissions: [db.write.app, platform.apps.call]
  integrations:
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.read, files.write]
      required: false
      hint: "Bind storage to attach lesson videos + post attachments. Files live under /.community/."
    - role: ffmpeg
      kind: app
      compatible_app_names: [ffmpeg]
      capabilities: [probe]
      required: false
      hint: "When bound, lessons_attach_video auto-fills duration_seconds via ffmpeg_probe."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: communities_create,  description: "Create a community." }
    - { name: communities_list,    description: "List communities in this scope." }
    - { name: communities_get,     description: "Fetch one community by id or slug." }
    - { name: communities_update,  description: "Update a community's name or description." }
    - { name: communities_archive, description: "Soft-delete a community." }
    - { name: members_create,      description: "Create a member in a community." }
    - { name: members_list,        description: "List members of a community." }
    - { name: members_get,         description: "Fetch one member by id or handle." }
    - { name: members_update,      description: "Update a member's display_name, bio, status, or contact_id." }
    - { name: spaces_create,       description: "Create a space (feed|forum|chat|course) in a community." }
    - { name: spaces_list,         description: "List spaces in a community." }
    - { name: spaces_update,       description: "Update a space's name, visibility, or sort_order." }
    - { name: spaces_archive,      description: "Soft-delete a space." }
    - { name: spaces_add_member,   description: "Add a member to a space." }
    - { name: threads_create,      description: "Open a new thread in a space." }
    - { name: threads_list,        description: "List threads in a space." }
    - { name: threads_pin,         description: "Pin or unpin a thread." }
    - { name: threads_lock,        description: "Lock or unlock a thread." }
    - { name: posts_create,        description: "Post in a thread. Reply by passing reply_to_id." }
    - { name: posts_list,          description: "List posts in a thread, oldest first." }
    - { name: posts_edit,          description: "Edit a post's body. Author only." }
    - { name: posts_react,         description: "Add a reaction to a post. Toggle off by re-sending the same emoji." }
    - { name: posts_remove,        description: "Soft-delete a post." }
    - { name: dms_open,            description: "Open (or fetch) a DM thread between two or more members." }
    - { name: dms_send,            description: "Send a message in a DM thread." }
    - { name: dms_list_threads,    description: "List DM threads a member participates in, with unread counts." }
    - { name: dms_get_thread,      description: "Fetch a DM thread with its messages." }
    - { name: dms_mark_read,       description: "Mark a member's read cursor in a DM thread up to now." }
    - { name: dms_unread_count,    description: "Total unread DM messages for a member across all threads." }
    - { name: courses_create,      description: "Create a course (sugar for spaces_create with kind=course)." }
    - { name: sections_create,     description: "Create a section inside a course." }
    - { name: sections_list,       description: "List sections of a course." }
    - { name: sections_reorder,    description: "Reorder sections within a course." }
    - { name: lessons_create,      description: "Create a lesson inside a section." }
    - { name: lessons_update,      description: "Update a lesson's title or body." }
    - { name: lessons_publish,     description: "Set or clear a lesson's published_at timestamp." }
    - { name: lessons_reorder,     description: "Reorder lessons within a section." }
    - { name: lessons_list,        description: "List lessons in a course." }
    - { name: lessons_get,         description: "Fetch one lesson with full body + caller progress." }
    - { name: lessons_attach_video, description: "Attach a storage file as the lesson's video." }
    - { name: lessons_mark_complete, description: "Mark a lesson complete (or in_progress) for a member." }
    - { name: lessons_progress,    description: "Get one member's progress across a course." }
    - { name: course_progress,     description: "Funnel across all members per lesson." }
    - { name: lesson_comments_post, description: "Post a comment on a lesson." }
    - { name: lesson_comments_list, description: "List comments on a lesson, oldest first." }
  ui_panels:
    - slot: project.page
      label: Community
      icon: users
      entry: /ui/CommunityPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/community
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/community.db
  migrations: migrations/
config_schema:
  - name: default_community_slug
    type: text
    default: "main"
    label: Default community slug
    description: "Slug seeded on first boot so single-community installs don't have to pick one. Empty disables auto-seed."
  - name: default_visibility
    type: text
    default: "members"
    label: Default space visibility
    description: "members | public — applied to newly created spaces when not specified."
  - name: lesson_storage_folder
    type: text
    default: ".community/lessons"
    label: Recommended storage folder for lesson videos
    description: "Surfaced in lessons_attach_video's docstring so callers organise uploads consistently."
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
		return errors.New("community requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("community mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ─────────────────────────────────────────────────
// Mirror the MCP tools — the panel hits these for reads, the bus
// for writes. Writes still go through MCP for the auth/permission
// gate; HTTP read-only is the fastest path for panel hydrate.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/communities", Handler: a.httpCommunities},
		{Pattern: "/members", Handler: a.httpMembers},
		{Pattern: "/spaces", Handler: a.httpSpaces},
		{Pattern: "/threads", Handler: a.httpThreads},
		{Pattern: "/posts", Handler: a.httpPosts},
		{Pattern: "/dms", Handler: a.httpDMs},
		{Pattern: "/sections", Handler: a.httpSections},
		{Pattern: "/lessons", Handler: a.httpLessons},
		{Pattern: "/lesson", Handler: a.httpLesson},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{}
	tools = append(tools, communitiesTools()...)
	tools = append(tools, membersTools()...)
	tools = append(tools, spacesTools()...)
	tools = append(tools, threadsTools()...)
	tools = append(tools, postsTools()...)
	tools = append(tools, dmsTools()...)
	tools = append(tools, coursesTools()...)
	return tools
}

// ─── helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

// intArg coerces a numeric arg from MCP (float64 over the wire) or
// from a Go test (int / int64). Returns (val, true) on success, the
// zero default and false otherwise.
func intArg(args map[string]any, key string) (int64, bool) {
	switch v := args[key].(type) {
	case float64:
		return int64(v), true
	case int:
		return int64(v), true
	case int64:
		return v, true
	}
	return 0, false
}

func mustStr(args map[string]any, key string) (string, error) {
	v, _ := args[key].(string)
	if strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// newID mints a TEXT id with the given short prefix. ~80 bits of entropy
// is enough for per-install collision avoidance and stays grep-able.
func newID(prefix string) string {
	var b [10]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is fatal at process boot; if it happens
		// mid-flight we surface zero-id and let the unique constraint
		// reject the row.
		return prefix + "_err"
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// scopeProject returns the project context for cross-cutting queries.
// Project-scoped installs always have a value; global installs may have
// "" when the dispatch isn't bound to one project — the caller decides
// whether that's a hard error.
func scopeProject(ctx *sdk.AppCtx) string {
	if ctx == nil {
		return ""
	}
	return ctx.CurrentProject()
}

// emit publishes a domain event. Pulled into a helper so future plumbing
// (rate limiting, batch coalescing) lives in one place.
func emit(ctx *sdk.AppCtx, topic string, payload map[string]any) {
	if ctx == nil {
		return
	}
	ctx.Emit(topic, payload)
}

func dbHandle() *sql.DB {
	if globalCtx == nil {
		return nil
	}
	return globalCtx.AppDB()
}

// ensureCommunityVisible returns nil when the community exists and isn't
// archived. Used by every cross-table tool to fail fast with a clean
// error instead of a downstream FK violation.
func ensureCommunityVisible(db *sql.DB, communityID string) error {
	var arch sql.NullString
	err := db.QueryRow(
		`SELECT archived_at FROM communities WHERE id = ?`, communityID,
	).Scan(&arch)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("community %q not found", communityID)
	}
	if err != nil {
		return err
	}
	if arch.Valid {
		return fmt.Errorf("community %q is archived", communityID)
	}
	return nil
}

// ─── main ────────────────────────────────────────────────────────

func main() {
	sdk.Run(&App{})
}
