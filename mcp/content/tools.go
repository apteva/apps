// MCP tool registry — the agent's surface. Tool implementations live
// in their respective per-domain files (posts.go, blocks_tools.go,
// terms.go, …); this file is just the catalog.

package main

import sdk "github.com/apteva/app-sdk"

func (a *App) mcpTools() []sdk.Tool {
	return []sdk.Tool{
		// ── Posts + pages ───────────────────────────────────────
		{
			Name:        "posts_search",
			Description: "Filtered post/page search. Args: q (free text), status, kind ('post'|'page'), term_slug, parent_id, author, locale, limit (default 50), offset.",
			InputSchema: schemaObject(map[string]any{
				"q":         map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
				"kind":      map[string]any{"type": "string"},
				"term_slug": map[string]any{"type": "string"},
				"parent_id": map[string]any{"type": "integer"},
				"author":    map[string]any{"type": "string"},
				"locale":    map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
				"offset":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPostsSearch,
		},
		{
			Name:        "posts_get",
			Description: "Fetch one post or page (snapshot only). Args: id OR (slug, kind?, locale?).",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"slug":   map[string]any{"type": "string"},
				"kind":   map[string]any{"type": "string"},
				"locale": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolPostsGet,
		},
		{
			Name:        "posts_get_context",
			Description: "Snapshot + revisions + terms — the agent's pre-flight read before edits.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"slug":   map[string]any{"type": "string"},
				"kind":   map[string]any{"type": "string"},
				"locale": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolPostsGetContext,
		},
		{
			Name:        "posts_create",
			Description: "Create a post or page. Args: kind ('post'|'page', default 'post'), title, excerpt, slug, locale, blocks ([] or {version,blocks}), author, template, terms ([term_id]), featured_media_id, parent_id, menu_order, seo_title, seo_description, seo_canonical, og_image_media_id.",
			InputSchema: schemaObject(map[string]any{
				"kind":              map[string]any{"type": "string"},
				"title":             map[string]any{"type": "string"},
				"excerpt":           map[string]any{"type": "string"},
				"slug":              map[string]any{"type": "string"},
				"locale":            map[string]any{"type": "string"},
				"blocks":            map[string]any{"type": "array"},
				"author":            map[string]any{"type": "string"},
				"template":          map[string]any{"type": "string"},
				"featured_media_id": map[string]any{"type": "integer"},
				"parent_id":         map[string]any{"type": "integer"},
				"menu_order":        map[string]any{"type": "integer"},
				"seo_title":         map[string]any{"type": "string"},
				"seo_description":   map[string]any{"type": "string"},
				"seo_canonical":     map[string]any{"type": "string"},
				"og_image_media_id": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolPostsCreate,
		},
		{
			Name:        "posts_update",
			Description: "Partial-patch a post or page. Creates a revision. Args: id (required), plus any subset of title, excerpt, slug, locale, blocks, author, template, featured_media_id, parent_id, menu_order, seo_*, og_image_media_id.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"title":  map[string]any{"type": "string"},
				"blocks": map[string]any{"type": "array"},
			}, []string{"id"}),
			Handler: a.toolPostsUpdate,
		},
		{
			Name:        "posts_publish",
			Description: "Publish a post immediately or schedule for a future RFC3339 time. Args: id, scheduled_at (optional).",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"scheduled_at": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolPostsPublish,
		},
		{
			Name:        "posts_unpublish",
			Description: "Move a post back to draft.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPostsUnpublish,
		},
		{
			Name:        "posts_archive",
			Description: "Archive (soft-delete) a post.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPostsArchive,
		},
		{
			Name:        "posts_set_homepage",
			Description: "Pin a page as the site homepage. Args: page_id.",
			InputSchema: schemaObject(map[string]any{
				"page_id": map[string]any{"type": "integer"},
			}, []string{"page_id"}),
			Handler: a.toolPostsSetHomepage,
		},
		{
			Name:        "posts_revisions_list",
			Description: "List revisions for a post. Args: id, limit (default 50).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"limit": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolPostsRevisionsList,
		},
		{
			Name:        "posts_revision_restore",
			Description: "Restore a prior revision as the current body. Args: id, revision_id.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"revision_id": map[string]any{"type": "integer"},
			}, []string{"id", "revision_id"}),
			Handler: a.toolPostsRevisionRestore,
		},

		// ── Blocks ────────────────────────────────────────────
		{
			Name:        "blocks_get",
			Description: "Return the block tree of a post.",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
			}, []string{"post_id"}),
			Handler: a.toolBlocksGet,
		},
		{
			Name:        "blocks_insert",
			Description: "Insert a block. Position: one of after_id, before_id, or index (with optional parent_id for nesting inside a container). Args: post_id, type, attrs (object), inner (optional array of child blocks), after_id|before_id|index, parent_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id":   map[string]any{"type": "integer"},
				"type":      map[string]any{"type": "string"},
				"attrs":     map[string]any{"type": "object"},
				"inner":     map[string]any{"type": "array"},
				"after_id":  map[string]any{"type": "string"},
				"before_id": map[string]any{"type": "string"},
				"index":     map[string]any{"type": "integer"},
				"parent_id": map[string]any{"type": "string"},
			}, []string{"post_id", "type"}),
			Handler: a.toolBlocksInsert,
		},
		{
			Name:        "blocks_update",
			Description: "Update a block's attrs or inner. Args: post_id, block_id, attrs?, inner?.",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"block_id": map[string]any{"type": "string"},
				"attrs":    map[string]any{"type": "object"},
				"inner":    map[string]any{"type": "array"},
			}, []string{"post_id", "block_id"}),
			Handler: a.toolBlocksUpdate,
		},
		{
			Name:        "blocks_move",
			Description: "Move a block to a new position in its parent. Args: post_id, block_id, plus one of after_id/before_id/index/parent_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id":   map[string]any{"type": "integer"},
				"block_id":  map[string]any{"type": "string"},
				"after_id":  map[string]any{"type": "string"},
				"before_id": map[string]any{"type": "string"},
				"index":     map[string]any{"type": "integer"},
				"parent_id": map[string]any{"type": "string"},
			}, []string{"post_id", "block_id"}),
			Handler: a.toolBlocksMove,
		},
		{
			Name:        "blocks_delete",
			Description: "Remove a block from the tree by id.",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"block_id": map[string]any{"type": "string"},
			}, []string{"post_id", "block_id"}),
			Handler: a.toolBlocksDelete,
		},
		{
			Name:        "blocks_replace_all",
			Description: "Atomically replace the entire body. Args: post_id, blocks (array or {version,blocks}).",
			InputSchema: schemaObject(map[string]any{
				"post_id": map[string]any{"type": "integer"},
				"blocks":  map[string]any{"type": "array"},
			}, []string{"post_id", "blocks"}),
			Handler: a.toolBlocksReplaceAll,
		},
		{
			Name:        "blocks_duplicate",
			Description: "Duplicate a block; returns the new block_id.",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"block_id": map[string]any{"type": "string"},
			}, []string{"post_id", "block_id"}),
			Handler: a.toolBlocksDuplicate,
		},
		{
			Name:        "blocks_registry",
			Description: "List the installed block types with their attribute schemas. Args: category (optional filter).",
			InputSchema: schemaObject(map[string]any{
				"category": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolBlocksRegistry,
		},

		// ── Taxonomy ──────────────────────────────────────────
		{
			Name:        "terms_create",
			Description: "Create a category or tag. Args: kind ('category'|'tag'), name, slug (optional), parent_id (optional), description.",
			InputSchema: schemaObject(map[string]any{
				"kind":        map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
				"parent_id":   map[string]any{"type": "integer"},
				"description": map[string]any{"type": "string"},
			}, []string{"kind", "name"}),
			Handler: a.toolTermsCreate,
		},
		{
			Name:        "terms_list",
			Description: "List terms. Args: kind (optional filter), q (optional name/slug search).",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{"type": "string"},
				"q":    map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolTermsList,
		},
		{
			Name:        "terms_assign",
			Description: "Assign terms to a post. Args: post_id, term_ids (array of integers).",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"term_ids": map[string]any{"type": "array"},
			}, []string{"post_id", "term_ids"}),
			Handler: a.toolTermsAssign,
		},
		{
			Name:        "terms_unassign",
			Description: "Remove terms from a post. Args: post_id, term_ids.",
			InputSchema: schemaObject(map[string]any{
				"post_id":  map[string]any{"type": "integer"},
				"term_ids": map[string]any{"type": "array"},
			}, []string{"post_id", "term_ids"}),
			Handler: a.toolTermsUnassign,
		},

		// ── Media ─────────────────────────────────────────────
		{
			Name:        "media_upload",
			Description: "Upload media. Args: one of bytes_b64 / url, plus filename (optional), alt, caption, source. Requires the storage app to be bound.",
			InputSchema: schemaObject(map[string]any{
				"bytes_b64": map[string]any{"type": "string"},
				"url":       map[string]any{"type": "string"},
				"filename":  map[string]any{"type": "string"},
				"alt":       map[string]any{"type": "string"},
				"caption":   map[string]any{"type": "string"},
				"source":    map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolMediaUpload,
		},
		{
			Name:        "media_list",
			Description: "List media items. Args: kind (optional), q (optional), limit, offset.",
			InputSchema: schemaObject(map[string]any{
				"kind":   map[string]any{"type": "string"},
				"q":      map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
				"offset": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolMediaList,
		},
		{
			Name:        "media_set_meta",
			Description: "Update alt text or caption on a media item. Args: id, alt?, caption?.",
			InputSchema: schemaObject(map[string]any{
				"id":      map[string]any{"type": "integer"},
				"alt":     map[string]any{"type": "string"},
				"caption": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolMediaSetMeta,
		},

		// ── Menus ─────────────────────────────────────────────
		{
			Name:        "menus_create",
			Description: "Create a navigation menu. Args: slug, name.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
				"name": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: a.toolMenusCreate,
		},
		{
			Name:        "menus_list",
			Description: "List menus in this project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolMenusList,
		},
		{
			Name:        "menus_get",
			Description: "Fetch one menu by id or slug; includes nested items.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolMenusGet,
		},
		{
			Name:        "menus_set_items",
			Description: "Replace a menu's items atomically. Args: menu_id, items (nested array: [{label, target_kind, target_id|target_url, children?}, …]).",
			InputSchema: schemaObject(map[string]any{
				"menu_id": map[string]any{"type": "integer"},
				"items":   map[string]any{"type": "array"},
			}, []string{"menu_id", "items"}),
			Handler: a.toolMenusSetItems,
		},

		// ── Redirects + settings + themes ───────────────────
		{
			Name:        "redirects_create",
			Description: "Create a redirect rule. Args: from_path, to_path, code (301|302, default 301).",
			InputSchema: schemaObject(map[string]any{
				"from_path": map[string]any{"type": "string"},
				"to_path":   map[string]any{"type": "string"},
				"code":      map[string]any{"type": "integer"},
			}, []string{"from_path", "to_path"}),
			Handler: a.toolRedirectsCreate,
		},
		{
			Name:        "redirects_list",
			Description: "List redirect rules.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolRedirectsList,
		},
		{
			Name:        "settings_get",
			Description: "Read settings. Args: keys (optional array of specific keys); omit to return all.",
			InputSchema: schemaObject(map[string]any{
				"keys": map[string]any{"type": "array"},
			}, nil),
			Handler: a.toolSettingsGet,
		},
		{
			Name:        "settings_set",
			Description: "Write a setting. Args: key, value (any JSON-encodable).",
			InputSchema: schemaObject(map[string]any{
				"key":   map[string]any{"type": "string"},
				"value": map[string]any{},
			}, []string{"key"}),
			Handler: a.toolSettingsSet,
		},
		{
			Name:        "themes_list",
			Description: "List installed themes.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolThemesList,
		},
		{
			Name:        "themes_set_active",
			Description: "Switch the active theme by slug. v1.0 only supports the bundled 'default'.",
			InputSchema: schemaObject(map[string]any{
				"slug": map[string]any{"type": "string"},
			}, []string{"slug"}),
			Handler: a.toolThemesSetActive,
		},
	}
}
