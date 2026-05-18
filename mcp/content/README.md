# Content (v1)

Block-based CMS for Apteva. Posts, pages, media, taxonomies, menus,
revisions, redirects — rendered server-side as themed HTML and exposed
as a headless REST API from the same database.

## What's in v1.0

- **Posts + pages** with hierarchical pages, per-page templates, SEO
  fields, scheduled publishing, soft-delete + archive.
- **Block-based bodies.** `posts.body_blocks` is the canonical body
  (JSON tree); markdown survives as one block type. 16 core block types
  shipped: `heading · paragraph · image · gallery · list · quote · code
  · embed · separator · columns · group · html · markdown · table ·
  button · cta`.
- **Block MCP tools** the agent uses for structured authoring —
  `blocks_insert`, `blocks_update`, `blocks_move`, `blocks_delete`,
  `blocks_replace_all`, etc. Every block has a stable id that survives
  reorder + revisions.
- **Server-rendered themed HTML** under `/`, `/posts/:slug`,
  `/category/:slug`, paginated index, `/feed.xml`, `/sitemap.xml`. Plus
  `/preview/:token` for draft previews.
- **Headless REST** at `/api/apps/content/*` — the same database,
  rendered as JSON; external frontends (Next, Astro, mobile) can pull
  posts/pages/blocks/media/terms/menus without touching the theme path.
- **Embedded default theme** bundled into the binary. Custom themes
  live in the bound `storage` app under `/.themes/<slug>/`.
- **Media metadata** in this app's DB; bytes go to the `storage` app at
  `/.media/<uuid>`.
- **Two install scopes:** `project` (one install per Apteva project)
  and `global` (one install across projects, partition by `project_id`).

## Deliberately deferred

- Custom blocks contributed by bound apps (`media-studio/generated`,
  `crm/subscribe`, `crm/contact-form`, `social/embed`). The block
  registry interface is in place; cross-app wiring lands in v1.1.
- `posts_generate_hero`, `posts_cross_post`, `posts_send_newsletter`,
  `posts_seo_audit` — bound-app helpers, v1.1.
- Block editor UI (React) — v1 ships a minimal panel that lists posts
  and creates drafts; full editor lands as a separate PR so the data +
  render path can be exercised first.
- Slash command insertion, reusable blocks, block patterns — v1.1.
- WordPress XML importer — v1.2.
- Comments, multi-author, real multilingual, Full-Site Editing — out of
  1.x scope. See main proposal for the reasoning.

## Local development

```bash
cd apps/mcp/content
go build .
APTEVA_PROJECT_ID=test ./content   # binds to :8080
curl http://localhost:8080/health

# headless REST
curl http://localhost:8080/api/posts?project_id=test

# render the homepage (uses the embedded default theme)
curl http://localhost:8080/
```

See `migrations/001_init.sql` for the schema and `tools.go::MCPTools()`
for the full agent surface.

## Layout

| File | Purpose |
|---|---|
| `apteva.yaml` | Manifest |
| `main.go` | App entrypoint, HTTP route table, OnMount |
| `tools.go` | MCP tool registry |
| `posts.go` | Posts/pages DB layer + tool handlers |
| `blocks.go` | Block tree manipulation + core registry |
| `terms.go` | Categories + tags |
| `media.go` | Media metadata, storage app passthrough |
| `menus.go` | Navigation menus |
| `redirects.go` | Redirect rules |
| `settings.go` | KV site settings |
| `themes.go` | Theme bundle loader (embedded + storage-backed) |
| `render.go` | html/template rendering pipeline |
| `http.go` | Public + REST HTTP handlers |
| `migrations/001_init.sql` | Initial schema |
| `themes_default/` | Bundled default theme (embedded via go:embed) |
| `ui/ContentPanel.mjs` | Minimal dashboard panel (v1 stub) |
