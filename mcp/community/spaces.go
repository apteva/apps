// Spaces / threads / posts / reactions.
//
// Spaces are the unifying container — kind picks the rendering
// (feed=time-sorted, forum=thread-list, chat=scrollback). Threads live
// in spaces; posts live in threads. reactions are (post,member,emoji)
// triples; sending the same emoji twice toggles it off.
//
// last_post_at + post_count are denormalised onto threads so the panel
// can sort "most-recently-active" without joining posts.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Space struct {
	ID          string  `json:"id"`
	CommunityID string  `json:"community_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Kind        string  `json:"kind"`
	Visibility  string  `json:"visibility"`
	SortOrder   int64   `json:"sort_order"`
	CreatedAt   string  `json:"created_at"`
	ArchivedAt  *string `json:"archived_at,omitempty"`
}

type Thread struct {
	ID          string `json:"id"`
	CommunityID string `json:"community_id"`
	SpaceID     string `json:"space_id"`
	AuthorID    string `json:"author_id"`
	Title       string `json:"title"`
	Pinned      bool   `json:"pinned"`
	Locked      bool   `json:"locked"`
	CreatedAt   string `json:"created_at"`
	LastPostAt  string `json:"last_post_at"`
	PostCount   int64  `json:"post_count"`
}

type Post struct {
	ID          string            `json:"id"`
	CommunityID string            `json:"community_id"`
	ThreadID    string            `json:"thread_id"`
	AuthorID    string            `json:"author_id"`
	Body        string            `json:"body"`
	ReplyToID   *string           `json:"reply_to_id,omitempty"`
	RemovedAt   *string           `json:"removed_at,omitempty"`
	CreatedAt   string            `json:"created_at"`
	EditedAt    *string           `json:"edited_at,omitempty"`
	Reactions   []ReactionSummary `json:"reactions,omitempty"`
}

type ReactionSummary struct {
	Emoji string   `json:"emoji"`
	Count int      `json:"count"`
	By    []string `json:"by"`
}

// ─── tools ───────────────────────────────────────────────────────

func spacesTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "spaces_create",
			Description: "Create a space in a community. Args: community_id (required), slug (required), name (required), kind? (feed|forum|chat; default 'feed'), visibility? (public|members; default 'members').",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"slug":         map[string]any{"type": "string"},
				"name":         map[string]any{"type": "string"},
				"kind":         map[string]any{"type": "string"},
				"visibility":   map[string]any{"type": "string"},
			}, []string{"community_id", "slug", "name"}),
			Handler: toolSpacesCreate,
		},
		{
			Name:        "spaces_list",
			Description: "List spaces in a community. Archived hidden by default.",
			InputSchema: schemaObject(map[string]any{
				"community_id":     map[string]any{"type": "string"},
				"include_archived": map[string]any{"type": "boolean"},
			}, []string{"community_id"}),
			Handler: toolSpacesList,
		},
		{
			Name:        "spaces_add_member",
			Description: "Add a member to a space. Args: space_id (required), member_id (required), role? (member|moderator; default 'member'). Idempotent.",
			InputSchema: schemaObject(map[string]any{
				"space_id":  map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
				"role":      map[string]any{"type": "string"},
			}, []string{"space_id", "member_id"}),
			Handler: toolSpacesAddMember,
		},
		{
			Name:        "spaces_update",
			Description: "Update a space's name, visibility, or sort_order. Args: id (required), name?, visibility? (public|members), sort_order?.",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "string"},
				"name":       map[string]any{"type": "string"},
				"visibility": map[string]any{"type": "string"},
				"sort_order": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: toolSpacesUpdate,
		},
		{
			Name:        "spaces_archive",
			Description: "Soft-delete a space. Args: id (required).",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolSpacesArchive,
		},
	}
}

func threadsTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "threads_create",
			Description: "Open a new thread in a space. Args: space_id (required), author_id (required), title?, body? (creates the first post if non-empty).",
			InputSchema: schemaObject(map[string]any{
				"space_id":  map[string]any{"type": "string"},
				"author_id": map[string]any{"type": "string"},
				"title":     map[string]any{"type": "string"},
				"body":      map[string]any{"type": "string"},
			}, []string{"space_id", "author_id"}),
			Handler: toolThreadsCreate,
		},
		{
			Name:        "threads_list",
			Description: "List threads in a space, pinned first then last_post_at DESC. Args: space_id (required), limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"space_id": map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, []string{"space_id"}),
			Handler: toolThreadsList,
		},
		{
			Name:        "threads_pin",
			Description: "Pin or unpin a thread. Args: id (required), pinned (bool, required).",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "string"},
				"pinned": map[string]any{"type": "boolean"},
			}, []string{"id", "pinned"}),
			Handler: toolThreadsPin,
		},
		{
			Name:        "threads_lock",
			Description: "Lock or unlock a thread. Locked threads reject new posts. Args: id (required), locked (bool, required).",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "string"},
				"locked": map[string]any{"type": "boolean"},
			}, []string{"id", "locked"}),
			Handler: toolThreadsLock,
		},
	}
}

func toolSpacesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	cur, err := ensureSpaceVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["name"].(string); ok && v != "" {
		sets = append(sets, "name = ?")
		vals = append(vals, v)
	}
	if v, ok := args["visibility"].(string); ok && v != "" {
		if !spaceVisibilities[v] {
			return nil, fmt.Errorf("visibility %q invalid", v)
		}
		sets = append(sets, "visibility = ?")
		vals = append(vals, v)
	}
	if v, ok := intArg(args, "sort_order"); ok {
		sets = append(sets, "sort_order = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	vals = append(vals, id)
	if _, err := db.Exec(
		`UPDATE spaces SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...,
	); err != nil {
		return nil, err
	}
	s, err := ensureSpaceVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "space.updated", map[string]any{
		"community_id": cur.CommunityID,
		"space_id":     s.ID,
	})
	return s, nil
}

func toolSpacesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	s, err := ensureSpaceVisible(ctx, db, id)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(
		`UPDATE spaces SET archived_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	); err != nil {
		return nil, err
	}
	emit(ctx, "space.archived", map[string]any{
		"community_id": s.CommunityID,
		"space_id":     id,
	})
	return map[string]any{"ok": true}, nil
}

func toolThreadsPin(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	pinned, _ := args["pinned"].(bool)
	db := ctx.AppDB()
	if _, _, err := ensureThreadInVisibleSpace(ctx, db, id); err != nil {
		return nil, err
	}
	var v int
	if pinned {
		v = 1
	}
	if _, err := db.Exec(`UPDATE threads SET pinned = ? WHERE id = ?`, v, id); err != nil {
		return nil, err
	}
	t, err := loadThread(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "thread.pinned", map[string]any{
		"community_id": t.CommunityID,
		"space_id":     t.SpaceID,
		"thread_id":    t.ID,
		"pinned":       pinned,
	})
	return t, nil
}

func toolThreadsLock(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	locked, _ := args["locked"].(bool)
	db := ctx.AppDB()
	if _, _, err := ensureThreadInVisibleSpace(ctx, db, id); err != nil {
		return nil, err
	}
	var v int
	if locked {
		v = 1
	}
	if _, err := db.Exec(`UPDATE threads SET locked = ? WHERE id = ?`, v, id); err != nil {
		return nil, err
	}
	t, err := loadThread(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "thread.locked", map[string]any{
		"community_id": t.CommunityID,
		"space_id":     t.SpaceID,
		"thread_id":    t.ID,
		"locked":       locked,
	})
	return t, nil
}

func postsTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "posts_create",
			Description: "Post in a thread. Args: thread_id (required), author_id (required), body (required), reply_to_id?.",
			InputSchema: schemaObject(map[string]any{
				"thread_id":   map[string]any{"type": "string"},
				"author_id":   map[string]any{"type": "string"},
				"body":        map[string]any{"type": "string"},
				"reply_to_id": map[string]any{"type": "string"},
			}, []string{"thread_id", "author_id", "body"}),
			Handler: toolPostsCreate,
		},
		{
			Name:        "posts_list",
			Description: "List posts in a thread (oldest first) with reaction summary. Args: thread_id (required), limit? (default 200), include_removed? (default false).",
			InputSchema: schemaObject(map[string]any{
				"thread_id":       map[string]any{"type": "string"},
				"limit":           map[string]any{"type": "integer"},
				"include_removed": map[string]any{"type": "boolean"},
			}, []string{"thread_id"}),
			Handler: toolPostsList,
		},
		{
			Name:        "posts_edit",
			Description: "Edit a post's body. Author-only — caller_member_id must match the post's author_id.",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "string"},
				"body":             map[string]any{"type": "string"},
				"caller_member_id": map[string]any{"type": "string"},
			}, []string{"id", "body", "caller_member_id"}),
			Handler: toolPostsEdit,
		},
		{
			Name:        "posts_react",
			Description: "Add a reaction. Re-sending the same (post_id, member_id, emoji) toggles it off.",
			InputSchema: schemaObject(map[string]any{
				"post_id":   map[string]any{"type": "string"},
				"member_id": map[string]any{"type": "string"},
				"emoji":     map[string]any{"type": "string"},
			}, []string{"post_id", "member_id", "emoji"}),
			Handler: toolPostsReact,
		},
		{
			Name:        "posts_remove",
			Description: "Soft-delete a post. Author-only OR caller is a moderator of the post's space.",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "string"},
				"caller_member_id": map[string]any{"type": "string"},
			}, []string{"id", "caller_member_id"}),
			Handler: toolPostsRemove,
		},
	}
}

// ─── space handlers ──────────────────────────────────────────────

var spaceKinds = map[string]bool{"feed": true, "forum": true, "chat": true, "course": true}
var spaceVisibilities = map[string]bool{"public": true, "members": true}

func toolSpacesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	slug, err := mustStr(args, "slug")
	if err != nil {
		return nil, err
	}
	slug = strings.ToLower(slug)
	if !slugRE.MatchString(slug) {
		return nil, fmt.Errorf("slug %q invalid: must be lowercase, 2-63 chars, [a-z0-9-]", slug)
	}
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	kind := strArg(args, "kind", "feed")
	if !spaceKinds[kind] {
		return nil, fmt.Errorf("kind %q invalid: must be feed|forum|chat", kind)
	}
	vis := strArg(args, "visibility", "")
	if vis == "" {
		// Fall back to the install's configured default. Config knobs
		// are coarse-grained — per-space overrides win when explicit.
		vis = "members"
		if ctx != nil {
			if d := ctx.Config().Get("default_visibility"); d != "" {
				vis = d
			}
		}
	}
	if !spaceVisibilities[vis] {
		return nil, fmt.Errorf("visibility %q invalid: must be public|members", vis)
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	id := newID("s")
	_, err = db.Exec(
		`INSERT INTO spaces (id, community_id, slug, name, kind, visibility)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, communityID, slug, name, kind, vis,
	)
	if err != nil {
		return nil, fmt.Errorf("create space: %w", err)
	}
	s, err := loadSpace(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "space.created", map[string]any{
		"community_id": s.CommunityID,
		"space_id":     s.ID,
		"slug":         s.Slug,
		"kind":         s.Kind,
	})
	return s, nil
}

func toolSpacesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	communityID, err := mustStr(args, "community_id")
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), communityID); err != nil {
		return nil, err
	}
	includeArchived, _ := args["include_archived"].(bool)
	q := `SELECT ` + spaceCols + ` FROM spaces WHERE community_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY sort_order, created_at`
	rows, err := ctx.AppDB().Query(q, communityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Space{}
	for rows.Next() {
		s, err := scanSpace(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"spaces": out}, nil
}

var rolesValid = map[string]bool{"member": true, "moderator": true}

func toolSpacesAddMember(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	role := strArg(args, "role", "member")
	if !rolesValid[role] {
		return nil, fmt.Errorf("role %q invalid: must be member|moderator", role)
	}
	db := ctx.AppDB()
	s, err := ensureSpaceVisible(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	// Verify space + member belong to the same community to avoid the
	// quiet foot-gun of cross-community membership.
	var memberCommunity string
	if err := db.QueryRow(`SELECT community_id FROM members WHERE id = ?`, memberID).Scan(&memberCommunity); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("member %q not found", memberID)
		}
		return nil, err
	}
	if s.CommunityID != memberCommunity {
		return nil, errors.New("member and space belong to different communities")
	}
	// Idempotent: upsert on conflict.
	_, err = db.Exec(
		`INSERT INTO space_members (space_id, member_id, role) VALUES (?, ?, ?)
		 ON CONFLICT(space_id, member_id) DO UPDATE SET role = excluded.role`,
		spaceID, memberID, role,
	)
	if err != nil {
		return nil, fmt.Errorf("add member: %w", err)
	}
	emit(ctx, "space.member_added", map[string]any{
		"community_id": s.CommunityID,
		"space_id":     spaceID,
		"member_id":    memberID,
		"role":         role,
	})
	return map[string]any{"ok": true, "role": role}, nil
}

// ─── thread handlers ─────────────────────────────────────────────

func toolThreadsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	authorID, err := mustStr(args, "author_id")
	if err != nil {
		return nil, err
	}
	title := strArg(args, "title", "")
	body := strArg(args, "body", "")
	db := ctx.AppDB()
	s, err := ensureSpaceVisible(ctx, db, spaceID)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(db, s.CommunityID, authorID); err != nil {
		return nil, err
	}
	threadID := newID("t")
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO threads (id, community_id, space_id, author_id, title, post_count) VALUES (?, ?, ?, ?, ?, 0)`,
		threadID, s.CommunityID, spaceID, authorID, title,
	); err != nil {
		return nil, fmt.Errorf("create thread: %w", err)
	}
	var post *Post
	if strings.TrimSpace(body) != "" {
		p, err := insertPostTx(tx, s.CommunityID, threadID, authorID, body, "")
		if err != nil {
			return nil, err
		}
		post = &p
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	t, err := loadThread(db, threadID)
	if err != nil {
		return nil, err
	}
	emit(ctx, "thread.created", map[string]any{
		"community_id": t.CommunityID,
		"space_id":     t.SpaceID,
		"thread_id":    t.ID,
		"author_id":    t.AuthorID,
		"title":        t.Title,
	})
	if post != nil {
		emit(ctx, "post.created", map[string]any{
			"community_id": post.CommunityID,
			"thread_id":    post.ThreadID,
			"post_id":      post.ID,
			"author_id":    post.AuthorID,
			"preview":      preview(post.Body),
		})
	}
	return map[string]any{"thread": t, "first_post": post}, nil
}

func toolThreadsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	spaceID, err := mustStr(args, "space_id")
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 50, 200)
	if _, err := ensureSpaceVisible(ctx, ctx.AppDB(), spaceID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT `+threadCols+` FROM threads WHERE space_id = ?
		 ORDER BY pinned DESC, last_post_at DESC, id DESC LIMIT ?`,
		spaceID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Thread{}
	for rows.Next() {
		t, err := scanThread(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"threads": out}, nil
}

// ─── post handlers ───────────────────────────────────────────────

func toolPostsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	threadID, err := mustStr(args, "thread_id")
	if err != nil {
		return nil, err
	}
	authorID, err := mustStr(args, "author_id")
	if err != nil {
		return nil, err
	}
	body, err := mustStr(args, "body")
	if err != nil {
		return nil, err
	}
	replyTo := strArg(args, "reply_to_id", "")
	db := ctx.AppDB()
	t, _, err := ensureThreadWritable(ctx, db, threadID)
	if err != nil {
		return nil, err
	}
	if err := verifyMember(db, t.CommunityID, authorID); err != nil {
		return nil, err
	}
	if replyTo != "" {
		if err := verifyReplyTarget(db, threadID, replyTo); err != nil {
			return nil, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	p, err := insertPostTx(tx, t.CommunityID, threadID, authorID, body, replyTo)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit(ctx, "post.created", map[string]any{
		"community_id": p.CommunityID,
		"thread_id":    p.ThreadID,
		"post_id":      p.ID,
		"author_id":    p.AuthorID,
		"preview":      preview(p.Body),
	})
	return p, nil
}

func toolPostsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	threadID, err := mustStr(args, "thread_id")
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 200, 500)
	includeRemoved, _ := args["include_removed"].(bool)
	q := `SELECT ` + postCols + ` FROM posts WHERE thread_id = ?`
	if !includeRemoved {
		q += ` AND removed_at IS NULL`
	}
	q += ` ORDER BY created_at, rowid LIMIT ?`
	if _, _, err := ensureThreadInVisibleSpace(ctx, ctx.AppDB(), threadID); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(q, threadID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	posts := []Post{}
	postIDs := []string{}
	for rows.Next() {
		p, err := scanPost(rows.Scan)
		if err != nil {
			return nil, err
		}
		posts = append(posts, p)
		postIDs = append(postIDs, p.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load reaction summaries in one go.
	if len(postIDs) > 0 {
		reactions, err := loadReactionsForPosts(ctx.AppDB(), postIDs)
		if err != nil {
			return nil, err
		}
		for i := range posts {
			posts[i].Reactions = reactions[posts[i].ID]
		}
	}
	return map[string]any{"posts": posts}, nil
}

func toolPostsEdit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	body, err := mustStr(args, "body")
	if err != nil {
		return nil, err
	}
	caller, err := mustStr(args, "caller_member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	var author, communityID, threadID string
	var removed sql.NullString
	if err := db.QueryRow(
		`SELECT author_id, community_id, thread_id, removed_at FROM posts WHERE id = ?`, id,
	).Scan(&author, &communityID, &threadID, &removed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("post %q not found", id)
		}
		return nil, err
	}
	if removed.Valid {
		return nil, errors.New("post is removed")
	}
	if _, _, err := ensureThreadInVisibleSpace(ctx, db, threadID); err != nil {
		return nil, err
	}
	if author != caller {
		return nil, errors.New("only the author can edit this post")
	}
	if _, err := db.Exec(
		`UPDATE posts SET body = ?, edited_at = CURRENT_TIMESTAMP WHERE id = ?`,
		body, id,
	); err != nil {
		return nil, err
	}
	emit(ctx, "post.edited", map[string]any{
		"community_id": communityID,
		"thread_id":    threadID,
		"post_id":      id,
		"author_id":    author,
		"preview":      preview(body),
	})
	return loadPost(db, id)
}

func toolPostsReact(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	postID, err := mustStr(args, "post_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	emoji, err := mustStr(args, "emoji")
	if err != nil {
		return nil, err
	}
	emoji = strings.TrimSpace(emoji)
	if emoji == "" {
		return nil, errors.New("emoji is required")
	}
	db := ctx.AppDB()
	var communityID, threadID string
	if err := db.QueryRow(
		`SELECT community_id, thread_id FROM posts WHERE id = ?`, postID,
	).Scan(&communityID, &threadID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("post %q not found", postID)
		}
		return nil, err
	}
	if _, _, err := ensureThreadInVisibleSpace(ctx, db, threadID); err != nil {
		return nil, err
	}
	if err := verifyMember(db, communityID, memberID); err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Toggle atomically: delete if it exists, insert otherwise.
	res, err := tx.Exec(
		`DELETE FROM reactions WHERE post_id = ? AND member_id = ? AND emoji = ?`,
		postID, memberID, emoji,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	action := "added"
	if n == 0 {
		_, err = tx.Exec(
			`INSERT INTO reactions (post_id, member_id, emoji) VALUES (?, ?, ?)`,
			postID, memberID, emoji,
		)
		if err != nil {
			return nil, err
		}
	} else {
		action = "removed"
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit(ctx, "post.reacted", map[string]any{
		"community_id": communityID,
		"thread_id":    threadID,
		"post_id":      postID,
		"member_id":    memberID,
		"emoji":        emoji,
		"action":       action,
	})
	return map[string]any{"action": action, "emoji": emoji}, nil
}

func toolPostsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	caller, err := mustStr(args, "caller_member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	var author, communityID, threadID, spaceID string
	if err := db.QueryRow(
		`SELECT p.author_id, p.community_id, p.thread_id, t.space_id
		 FROM posts p JOIN threads t ON t.id = p.thread_id
		 WHERE p.id = ?`, id,
	).Scan(&author, &communityID, &threadID, &spaceID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("post %q not found", id)
		}
		return nil, err
	}
	if _, _, err := ensureThreadInVisibleSpace(ctx, db, threadID); err != nil {
		return nil, err
	}
	if author != caller {
		// Allow space moderators too.
		var role string
		err := db.QueryRow(
			`SELECT role FROM space_members WHERE space_id = ? AND member_id = ?`,
			spaceID, caller,
		).Scan(&role)
		if errors.Is(err, sql.ErrNoRows) || role != "moderator" {
			return nil, errors.New("only the author or a space moderator can remove this post")
		}
		if err != nil {
			return nil, err
		}
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`UPDATE posts SET removed_at = CURRENT_TIMESTAMP, body = '' WHERE id = ? AND removed_at IS NULL`, id,
	)
	if err != nil {
		return nil, err
	}
	changed, _ := res.RowsAffected()
	if changed > 0 {
		if _, err := tx.Exec(
			`UPDATE threads SET post_count = CASE WHEN post_count > 0 THEN post_count - 1 ELSE 0 END WHERE id = ?`,
			threadID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	emit(ctx, "post.removed", map[string]any{
		"community_id": communityID,
		"thread_id":    threadID,
		"post_id":      id,
	})
	return map[string]any{"ok": true}, nil
}

// ─── insertPostTx + reaction load ────────────────────────────────

func insertPostTx(tx *sql.Tx, communityID, threadID, authorID, body, replyToID string) (Post, error) {
	postID := newID("p")
	var replyArg any
	if replyToID != "" {
		replyArg = replyToID
	}
	if _, err := tx.Exec(
		`INSERT INTO posts (id, community_id, thread_id, author_id, body, reply_to_id) VALUES (?, ?, ?, ?, ?, ?)`,
		postID, communityID, threadID, authorID, body, replyArg,
	); err != nil {
		return Post{}, fmt.Errorf("insert post: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE threads SET last_post_at = CURRENT_TIMESTAMP, post_count = post_count + 1 WHERE id = ?`,
		threadID,
	); err != nil {
		return Post{}, fmt.Errorf("bump thread: %w", err)
	}
	row := tx.QueryRow(`SELECT `+postCols+` FROM posts WHERE id = ?`, postID)
	return scanPost(row.Scan)
}

func loadReactionsForPosts(db *sql.DB, postIDs []string) (map[string][]ReactionSummary, error) {
	if len(postIDs) == 0 {
		return map[string][]ReactionSummary{}, nil
	}
	placeholders := strings.Repeat("?,", len(postIDs))
	placeholders = strings.TrimRight(placeholders, ",")
	args := make([]any, len(postIDs))
	for i, id := range postIDs {
		args[i] = id
	}
	rows, err := db.Query(
		`SELECT post_id, emoji, member_id FROM reactions WHERE post_id IN (`+placeholders+`)
		 ORDER BY post_id, emoji, created_at`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Bucket by (post, emoji).
	type key struct{ post, emoji string }
	buckets := map[key]*ReactionSummary{}
	order := map[string][]key{}
	for rows.Next() {
		var p, e, m string
		if err := rows.Scan(&p, &e, &m); err != nil {
			return nil, err
		}
		k := key{p, e}
		if _, ok := buckets[k]; !ok {
			buckets[k] = &ReactionSummary{Emoji: e}
			order[p] = append(order[p], k)
		}
		buckets[k].Count++
		buckets[k].By = append(buckets[k].By, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string][]ReactionSummary{}
	for postID, keys := range order {
		for _, k := range keys {
			out[postID] = append(out[postID], *buckets[k])
		}
	}
	return out, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

const spaceCols = `id, community_id, slug, name, kind, visibility, sort_order, created_at, archived_at`

func scanSpace(scan func(...any) error) (Space, error) {
	var s Space
	var arch sql.NullString
	if err := scan(&s.ID, &s.CommunityID, &s.Slug, &s.Name, &s.Kind, &s.Visibility, &s.SortOrder, &s.CreatedAt, &arch); err != nil {
		return s, err
	}
	if arch.Valid {
		v := arch.String
		s.ArchivedAt = &v
	}
	return s, nil
}

func loadSpace(db *sql.DB, id string) (Space, error) {
	row := db.QueryRow(`SELECT `+spaceCols+` FROM spaces WHERE id = ?`, id)
	s, err := scanSpace(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return s, fmt.Errorf("space %q not found", id)
	}
	return s, err
}

const threadCols = `id, community_id, space_id, author_id, title, pinned, locked, created_at, last_post_at, post_count`

func scanThread(scan func(...any) error) (Thread, error) {
	var t Thread
	var pinned, locked int
	if err := scan(&t.ID, &t.CommunityID, &t.SpaceID, &t.AuthorID, &t.Title, &pinned, &locked, &t.CreatedAt, &t.LastPostAt, &t.PostCount); err != nil {
		return t, err
	}
	t.Pinned = pinned == 1
	t.Locked = locked == 1
	return t, nil
}

func loadThread(db *sql.DB, id string) (Thread, error) {
	row := db.QueryRow(`SELECT `+threadCols+` FROM threads WHERE id = ?`, id)
	t, err := scanThread(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("thread %q not found", id)
	}
	return t, err
}

const postCols = `id, community_id, thread_id, author_id, body, reply_to_id, removed_at, created_at, edited_at`

func scanPost(scan func(...any) error) (Post, error) {
	var p Post
	var reply, removed, edited sql.NullString
	if err := scan(&p.ID, &p.CommunityID, &p.ThreadID, &p.AuthorID, &p.Body, &reply, &removed, &p.CreatedAt, &edited); err != nil {
		return p, err
	}
	if reply.Valid {
		v := reply.String
		p.ReplyToID = &v
	}
	if removed.Valid {
		v := removed.String
		p.RemovedAt = &v
	}
	if edited.Valid {
		v := edited.String
		p.EditedAt = &v
	}
	return p, nil
}

func loadPost(db *sql.DB, id string) (Post, error) {
	row := db.QueryRow(`SELECT `+postCols+` FROM posts WHERE id = ?`, id)
	p, err := scanPost(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("post %q not found", id)
	}
	return p, err
}

func verifyReplyTarget(db *sql.DB, threadID, replyToID string) error {
	var parentThread string
	var removed sql.NullString
	err := db.QueryRow(
		`SELECT thread_id, removed_at FROM posts WHERE id = ?`, replyToID,
	).Scan(&parentThread, &removed)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("reply_to_id %q not found", replyToID)
	}
	if err != nil {
		return err
	}
	if parentThread != threadID {
		return errors.New("reply_to_id must belong to the same thread")
	}
	if removed.Valid {
		return errors.New("cannot reply to a removed post")
	}
	return nil
}

func verifyMember(db *sql.DB, communityID, memberID string) error {
	var status string
	err := db.QueryRow(
		`SELECT status FROM members WHERE id = ? AND community_id = ?`,
		memberID, communityID,
	).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("member %q not in community", memberID)
	}
	if err != nil {
		return err
	}
	if status != "active" {
		return fmt.Errorf("member %q is %s", memberID, status)
	}
	return nil
}

var newlineRE = regexp.MustCompile(`\s+`)

// preview returns a single-line, 140-char snippet for event-bus
// payloads — enough for the panel to render an unread badge without
// fetching the full body, but never the body itself.
func preview(body string) string {
	body = strings.TrimSpace(newlineRE.ReplaceAllString(body, " "))
	runes := []rune(body)
	if len(runes) <= 140 {
		return body
	}
	return string(runes[:137]) + "..."
}

// ─── HTTP stubs ──────────────────────────────────────────────────
// HTTP surface is read-only for the panel; writes go through MCP.

func (a *App) httpSpaces(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	communityID := r.URL.Query().Get("community_id")
	if communityID == "" {
		writeErr(w, 400, "community_id required")
		return
	}
	out, err := toolSpacesList(globalCtx, map[string]any{
		"community_id":     communityID,
		"include_archived": r.URL.Query().Get("include_archived") == "true",
	})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *App) httpThreads(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	spaceID := r.URL.Query().Get("space_id")
	if spaceID == "" {
		writeErr(w, 400, "space_id required")
		return
	}
	out, err := toolThreadsList(globalCtx, map[string]any{"space_id": spaceID})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}

func (a *App) httpPosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	threadID := r.URL.Query().Get("thread_id")
	if threadID == "" {
		writeErr(w, 400, "thread_id required")
		return
	}
	out, err := toolPostsList(globalCtx, map[string]any{"thread_id": threadID})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}
