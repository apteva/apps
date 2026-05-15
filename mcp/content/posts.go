// Posts + pages — DB layer, MCP tool handlers, REST handlers.
//
// Posts and pages share one table; `kind` distinguishes them. The
// canonical body is `body_blocks` (JSON tree); `body_html` is the
// rendered cache, cleared on update so the next read repopulates it.
// Status transitions are recorded in publish_events for the small
// audit surface the agent + dashboard need.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Post is the Go-side representation of a posts row. Times are RFC3339
// strings so JSON renderings are uniform across REST and MCP.
type Post struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	Kind            string `json:"kind"`
	Slug            string `json:"slug"`
	Locale          string `json:"locale,omitempty"`
	Status          string `json:"status"`
	Title           string `json:"title"`
	Excerpt         string `json:"excerpt,omitempty"`
	BodyBlocks      Document `json:"body_blocks"`
	BodyHTML        string `json:"body_html,omitempty"`
	Author          string `json:"author,omitempty"`
	FeaturedMediaID *int64 `json:"featured_media_id,omitempty"`
	ParentID        *int64 `json:"parent_id,omitempty"`
	MenuOrder       int    `json:"menu_order"`
	Template        string `json:"template,omitempty"`
	SEOTitle        string `json:"seo_title,omitempty"`
	SEODescription  string `json:"seo_description,omitempty"`
	SEOCanonical    string `json:"seo_canonical,omitempty"`
	OGImageMediaID  *int64 `json:"og_image_media_id,omitempty"`
	PublishedAt     string `json:"published_at,omitempty"`
	ScheduledAt     string `json:"scheduled_at,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// PostCreate captures the union of fields a caller can supply when
// creating a post. All optional except kind (defaulted to "post").
type PostCreate struct {
	Kind            string
	Slug            string
	Locale          string
	Title           string
	Excerpt         string
	Blocks          *Document
	Author          string
	FeaturedMediaID *int64
	ParentID        *int64
	MenuOrder       int
	Template        string
	SEOTitle        string
	SEODescription  string
	SEOCanonical    string
	OGImageMediaID  *int64
}

// PostPatch is a partial-update bag; nil fields mean "leave alone".
type PostPatch struct {
	Title           *string
	Excerpt         *string
	Blocks          *Document
	Author          *string
	FeaturedMediaID *int64
	ParentID        *int64
	MenuOrder       *int
	Template        *string
	Slug            *string
	Locale          *string
	SEOTitle        *string
	SEODescription  *string
	SEOCanonical    *string
	OGImageMediaID  *int64
}

// ── reserved slugs ─────────────────────────────────────────────────
//
// These segments are claimed by the public route table; allowing
// pages to use them would shadow rendering.
var reservedSlugs = map[string]bool{
	"":          true,
	"posts":     true,
	"category":  true,
	"tag":       true,
	"page":      true,
	"feed.xml":  true,
	"sitemap.xml": true,
	"_theme":    true,
	"_media":    true,
	"preview":   true,
	"api":       true,
	"health":    true,
}

// ── slug helpers ────────────────────────────────────────────────────

var slugRE = regexp.MustCompile(`[^a-z0-9-]+`)
var slugCollapseRE = regexp.MustCompile(`-+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRE.ReplaceAllString(s, "-")
	s = slugCollapseRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "untitled"
	}
	return s
}

// ensureUniqueSlug appends -2, -3, … until the slug is free in the
// (project_id, locale, kind) namespace.
func ensureUniqueSlug(db *sql.DB, projectID, locale, kind, base string, excludingID int64) (string, error) {
	if base == "" {
		base = "untitled"
	}
	slug := base
	for n := 2; n < 1000; n++ {
		var taken int
		q := `SELECT COUNT(*) FROM posts
		      WHERE project_id=? AND locale=? AND kind=? AND slug=?
		        AND deleted_at IS NULL AND id != ?`
		if err := db.QueryRow(q, projectID, locale, kind, slug, excludingID).Scan(&taken); err != nil {
			return "", err
		}
		if taken == 0 {
			return slug, nil
		}
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	return "", errors.New("unable to derive a unique slug after 1000 attempts")
}

// ── DB layer ────────────────────────────────────────────────────────

func dbCreatePost(db *sql.DB, projectID string, p PostCreate) (*Post, error) {
	if p.Kind == "" {
		p.Kind = "post"
	}
	if p.Kind != "post" && p.Kind != "page" {
		return nil, fmt.Errorf("kind must be post or page (got %q)", p.Kind)
	}
	if p.Locale == "" {
		p.Locale = "en"
	}
	base := p.Slug
	if base == "" {
		base = slugify(p.Title)
	} else {
		base = slugify(base)
	}
	if p.Kind == "page" && reservedSlugs[base] {
		return nil, fmt.Errorf("slug %q is reserved", base)
	}
	slug, err := ensureUniqueSlug(db, projectID, p.Locale, p.Kind, base, 0)
	if err != nil {
		return nil, err
	}

	doc := Document{Version: documentVersion}
	if p.Blocks != nil {
		doc = *p.Blocks
	}
	bodyJSON, err := encodeDocument(doc)
	if err != nil {
		return nil, err
	}

	res, err := db.Exec(`
		INSERT INTO posts (
			project_id, kind, slug, locale, status,
			title, excerpt, body_blocks, body_html, author,
			featured_media_id, parent_id, menu_order, template,
			seo_title, seo_description, seo_canonical, og_image_media_id
		) VALUES (?, ?, ?, ?, 'draft', ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID, p.Kind, slug, p.Locale,
		p.Title, p.Excerpt, bodyJSON, p.Author,
		nullableInt(p.FeaturedMediaID), nullableInt(p.ParentID), p.MenuOrder, p.Template,
		p.SEOTitle, p.SEODescription, p.SEOCanonical, nullableInt(p.OGImageMediaID))
	if err != nil {
		return nil, fmt.Errorf("insert post: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetPost(db, projectID, id)
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func dbGetPost(db *sql.DB, projectID string, id int64) (*Post, error) {
	row := db.QueryRow(postSelectSQL+` WHERE project_id=? AND id=? AND deleted_at IS NULL`,
		projectID, id)
	return scanPost(row)
}

func dbGetPostBySlug(db *sql.DB, projectID, kind, locale, slug string) (*Post, error) {
	row := db.QueryRow(postSelectSQL+`
		WHERE project_id=? AND kind=? AND locale=? AND slug=? AND deleted_at IS NULL`,
		projectID, kind, locale, slug)
	return scanPost(row)
}

const postSelectSQL = `
SELECT id, project_id, kind, slug, locale, status,
       title, excerpt, body_blocks, body_html, author,
       featured_media_id, parent_id, menu_order, template,
       seo_title, seo_description, seo_canonical, og_image_media_id,
       published_at, scheduled_at, created_at, updated_at
  FROM posts`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPost(row rowScanner) (*Post, error) {
	var p Post
	var bodyJSON, publishedAt, scheduledAt, createdAt, updatedAt sql.NullString
	var featured, parent, ogImg sql.NullInt64
	if err := row.Scan(
		&p.ID, &p.ProjectID, &p.Kind, &p.Slug, &p.Locale, &p.Status,
		&p.Title, &p.Excerpt, &bodyJSON, &p.BodyHTML, &p.Author,
		&featured, &parent, &p.MenuOrder, &p.Template,
		&p.SEOTitle, &p.SEODescription, &p.SEOCanonical, &ogImg,
		&publishedAt, &scheduledAt, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	if featured.Valid {
		v := featured.Int64
		p.FeaturedMediaID = &v
	}
	if parent.Valid {
		v := parent.Int64
		p.ParentID = &v
	}
	if ogImg.Valid {
		v := ogImg.Int64
		p.OGImageMediaID = &v
	}
	if publishedAt.Valid {
		p.PublishedAt = publishedAt.String
	}
	if scheduledAt.Valid {
		p.ScheduledAt = scheduledAt.String
	}
	if createdAt.Valid {
		p.CreatedAt = createdAt.String
	}
	if updatedAt.Valid {
		p.UpdatedAt = updatedAt.String
	}
	doc, err := parseDocument(bodyJSON.String)
	if err != nil {
		return nil, err
	}
	p.BodyBlocks = doc
	return &p, nil
}

// dbUpdatePost applies a PostPatch and creates a revision snapshot
// using the *prior* body so revisions form a true history.
func dbUpdatePost(db *sql.DB, projectID string, id int64, patch PostPatch, author, source, note string) (*Post, error) {
	prior, err := dbGetPost(db, projectID, id)
	if err != nil {
		return nil, err
	}

	priorBodyJSON, _ := encodeDocument(prior.BodyBlocks)
	if _, err := db.Exec(`INSERT INTO revisions (post_id, body_blocks, title, excerpt, author, source, note)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, priorBodyJSON, prior.Title, prior.Excerpt, author, source, note); err != nil {
		return nil, fmt.Errorf("snapshot revision: %w", err)
	}

	sets := []string{"updated_at = CURRENT_TIMESTAMP", "body_html = ''"}
	args := []any{}
	if patch.Title != nil {
		sets = append(sets, "title = ?")
		args = append(args, *patch.Title)
	}
	if patch.Excerpt != nil {
		sets = append(sets, "excerpt = ?")
		args = append(args, *patch.Excerpt)
	}
	if patch.Blocks != nil {
		bodyJSON, err := encodeDocument(*patch.Blocks)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "body_blocks = ?")
		args = append(args, bodyJSON)
	}
	if patch.Author != nil {
		sets = append(sets, "author = ?")
		args = append(args, *patch.Author)
	}
	if patch.FeaturedMediaID != nil {
		sets = append(sets, "featured_media_id = ?")
		args = append(args, *patch.FeaturedMediaID)
	}
	if patch.ParentID != nil {
		sets = append(sets, "parent_id = ?")
		args = append(args, *patch.ParentID)
	}
	if patch.MenuOrder != nil {
		sets = append(sets, "menu_order = ?")
		args = append(args, *patch.MenuOrder)
	}
	if patch.Template != nil {
		sets = append(sets, "template = ?")
		args = append(args, *patch.Template)
	}
	if patch.Slug != nil {
		newSlug := slugify(*patch.Slug)
		if prior.Kind == "page" && reservedSlugs[newSlug] {
			return nil, fmt.Errorf("slug %q is reserved", newSlug)
		}
		newSlug, err = ensureUniqueSlug(db, projectID, prior.Locale, prior.Kind, newSlug, id)
		if err != nil {
			return nil, err
		}
		sets = append(sets, "slug = ?")
		args = append(args, newSlug)
	}
	if patch.Locale != nil {
		sets = append(sets, "locale = ?")
		args = append(args, *patch.Locale)
	}
	if patch.SEOTitle != nil {
		sets = append(sets, "seo_title = ?")
		args = append(args, *patch.SEOTitle)
	}
	if patch.SEODescription != nil {
		sets = append(sets, "seo_description = ?")
		args = append(args, *patch.SEODescription)
	}
	if patch.SEOCanonical != nil {
		sets = append(sets, "seo_canonical = ?")
		args = append(args, *patch.SEOCanonical)
	}
	if patch.OGImageMediaID != nil {
		sets = append(sets, "og_image_media_id = ?")
		args = append(args, *patch.OGImageMediaID)
	}

	args = append(args, projectID, id)
	q := "UPDATE posts SET " + strings.Join(sets, ", ") + " WHERE project_id=? AND id=?"
	if _, err := db.Exec(q, args...); err != nil {
		return nil, fmt.Errorf("update post: %w", err)
	}
	invalidatePageCache()
	return dbGetPost(db, projectID, id)
}

// dbPublishPost flips a post to published (now or scheduled future).
func dbPublishPost(db *sql.DB, projectID string, id int64, scheduledAt string, source string) (*Post, error) {
	now := nowStamp()
	var event string
	var q string
	var args []any
	if scheduledAt != "" {
		event = "scheduled"
		q = `UPDATE posts SET status='scheduled', scheduled_at=?, updated_at=? WHERE project_id=? AND id=?`
		args = []any{scheduledAt, now, projectID, id}
	} else {
		event = "published"
		q = `UPDATE posts SET status='published', published_at=COALESCE(published_at, ?), scheduled_at=NULL, updated_at=? WHERE project_id=? AND id=?`
		args = []any{now, now, projectID, id}
	}
	if _, err := db.Exec(q, args...); err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}
	logPublishEvent(db, id, event, source, nil)
	invalidatePageCache()
	return dbGetPost(db, projectID, id)
}

func dbUnpublishPost(db *sql.DB, projectID string, id int64, source string) (*Post, error) {
	if _, err := db.Exec(`UPDATE posts SET status='draft', scheduled_at=NULL, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, projectID, id); err != nil {
		return nil, err
	}
	logPublishEvent(db, id, "unpublished", source, nil)
	invalidatePageCache()
	return dbGetPost(db, projectID, id)
}

func dbArchivePost(db *sql.DB, projectID string, id int64, source string) (*Post, error) {
	if _, err := db.Exec(`UPDATE posts SET status='archived', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`, projectID, id); err != nil {
		return nil, err
	}
	logPublishEvent(db, id, "archived", source, nil)
	invalidatePageCache()
	return dbGetPost(db, projectID, id)
}

func logPublishEvent(db *sql.DB, postID int64, event, source string, metadata map[string]any) {
	meta := "{}"
	if metadata != nil {
		b, _ := json.Marshal(metadata)
		meta = string(b)
	}
	_, _ = db.Exec(`INSERT INTO publish_events (post_id, event, source, metadata) VALUES (?, ?, ?, ?)`,
		postID, event, source, meta)
}

// ── search ──────────────────────────────────────────────────────────

type PostSearch struct {
	Q        string
	Status   string
	Kind     string
	TermSlug string
	ParentID *int64
	Author   string
	Locale   string
	Limit    int
	Offset   int
}

func dbSearchPosts(db *sql.DB, projectID string, s PostSearch) ([]Post, int, error) {
	if s.Limit <= 0 || s.Limit > 200 {
		s.Limit = 50
	}
	where := []string{"p.project_id = ?", "p.deleted_at IS NULL"}
	args := []any{projectID}
	if s.Status != "" {
		where = append(where, "p.status = ?")
		args = append(args, s.Status)
	}
	if s.Kind != "" {
		where = append(where, "p.kind = ?")
		args = append(args, s.Kind)
	}
	if s.Locale != "" {
		where = append(where, "p.locale = ?")
		args = append(args, s.Locale)
	}
	if s.Author != "" {
		where = append(where, "p.author = ?")
		args = append(args, s.Author)
	}
	if s.ParentID != nil {
		where = append(where, "p.parent_id = ?")
		args = append(args, *s.ParentID)
	}
	if s.Q != "" {
		where = append(where, "(p.title LIKE ? OR p.excerpt LIKE ? OR p.body_blocks LIKE ?)")
		like := "%" + s.Q + "%"
		args = append(args, like, like, like)
	}
	join := ""
	if s.TermSlug != "" {
		join = `JOIN post_terms pt ON pt.post_id = p.id
		        JOIN terms t ON t.id = pt.term_id AND t.slug = ? AND t.project_id = ?`
		// Term filter requires term args in a specific order: slug then
		// project_id come *before* the where args since the JOIN comes
		// before the WHERE in the prepared statement.
		args = append([]any{s.TermSlug, projectID}, args...)
	}

	countQ := "SELECT COUNT(*) FROM posts p " + join + " WHERE " + strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(countQ, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count: %w", err)
	}

	selQ := postSelectSQLWithAlias + " " + join + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY COALESCE(p.published_at, p.created_at) DESC LIMIT ? OFFSET ?"
	args = append(args, s.Limit, s.Offset)
	rows, err := db.Query(selQ, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("search: %w", err)
	}
	defer rows.Close()

	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *p)
	}
	return out, total, nil
}

const postSelectSQLWithAlias = `
SELECT p.id, p.project_id, p.kind, p.slug, p.locale, p.status,
       p.title, p.excerpt, p.body_blocks, p.body_html, p.author,
       p.featured_media_id, p.parent_id, p.menu_order, p.template,
       p.seo_title, p.seo_description, p.seo_canonical, p.og_image_media_id,
       p.published_at, p.scheduled_at, p.created_at, p.updated_at
  FROM posts p`

// ── revisions ────────────────────────────────────────────────────────

type Revision struct {
	ID          int64    `json:"id"`
	PostID      int64    `json:"post_id"`
	BodyBlocks  Document `json:"body_blocks"`
	Title       string   `json:"title"`
	Excerpt     string   `json:"excerpt"`
	SnapshotAt  string   `json:"snapshot_at"`
	Author      string   `json:"author,omitempty"`
	Source      string   `json:"source"`
	Note        string   `json:"note,omitempty"`
}

func dbListRevisions(db *sql.DB, postID int64, limit int) ([]Revision, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(`SELECT id, post_id, body_blocks, title, excerpt, snapshot_at, author, source, note
		FROM revisions WHERE post_id=? ORDER BY snapshot_at DESC LIMIT ?`, postID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		var bodyJSON string
		if err := rows.Scan(&r.ID, &r.PostID, &bodyJSON, &r.Title, &r.Excerpt, &r.SnapshotAt, &r.Author, &r.Source, &r.Note); err != nil {
			return nil, err
		}
		doc, _ := parseDocument(bodyJSON)
		r.BodyBlocks = doc
		out = append(out, r)
	}
	return out, nil
}

func dbRestoreRevision(db *sql.DB, projectID string, postID, revisionID int64, source string) (*Post, error) {
	var bodyJSON, title, excerpt string
	if err := db.QueryRow(`SELECT body_blocks, title, excerpt FROM revisions WHERE id=? AND post_id=?`,
		revisionID, postID).Scan(&bodyJSON, &title, &excerpt); err != nil {
		return nil, fmt.Errorf("revision %d not found: %w", revisionID, err)
	}
	doc, err := parseDocument(bodyJSON)
	if err != nil {
		return nil, err
	}
	patch := PostPatch{Blocks: &doc, Title: &title, Excerpt: &excerpt}
	return dbUpdatePost(globalCtx.AppDB(), projectID, postID, patch, "", source, fmt.Sprintf("restored from revision %d", revisionID))
}

// ── scheduled-publisher worker ──────────────────────────────────────
//
// Fires every minute (declared in main.go's Workers()). Picks rows
// where status='scheduled' AND scheduled_at <= now and flips them to
// 'published'. Idempotent — re-running on the same row is harmless
// because the WHERE clause excludes already-published rows.

func runScheduledPublisher(ctx *sdk.AppCtx) error {
	db := ctx.AppDB()
	now := nowStamp()

	rows, err := db.Query(`SELECT id, project_id FROM posts
		WHERE status='scheduled' AND scheduled_at IS NOT NULL AND scheduled_at <= ? AND deleted_at IS NULL`, now)
	if err != nil {
		return err
	}
	type pending struct{ id int64; pid string }
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.pid); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, p)
	}
	rows.Close()

	for _, p := range batch {
		if _, err := dbPublishPost(db, p.pid, p.id, "", "scheduler"); err != nil {
			ctx.Logger().Warn("scheduled publish failed", "post_id", p.id, "err", err.Error())
			continue
		}
		ctx.Logger().Info("scheduled publish", "post_id", p.id, "project_id", p.pid)
	}
	return nil
}

// ── MCP tool handlers ──────────────────────────────────────────────

func (a *App) toolPostsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pc := PostCreate{
		Kind:    asString(args["kind"]),
		Slug:    asString(args["slug"]),
		Locale:  asString(args["locale"]),
		Title:   asString(args["title"]),
		Excerpt: asString(args["excerpt"]),
		Author:  asString(args["author"]),
		Template: asString(args["template"]),
		SEOTitle: asString(args["seo_title"]),
		SEODescription: asString(args["seo_description"]),
		SEOCanonical: asString(args["seo_canonical"]),
	}
	if v, ok := asInt64(args["menu_order"]); ok {
		pc.MenuOrder = int(v)
	}
	if v, ok := asInt64(args["parent_id"]); ok && v > 0 {
		pc.ParentID = &v
	}
	if v, ok := asInt64(args["featured_media_id"]); ok && v > 0 {
		pc.FeaturedMediaID = &v
	}
	if v, ok := asInt64(args["og_image_media_id"]); ok && v > 0 {
		pc.OGImageMediaID = &v
	}
	if raw, ok := args["blocks"]; ok && raw != nil {
		doc, err := coerceBlocksArg(raw)
		if err != nil {
			return nil, err
		}
		pc.Blocks = &doc
	}
	post, err := dbCreatePost(ctx.AppDB(), pid, pc)
	if err != nil {
		return nil, err
	}
	ctx.Emit("post.created", map[string]any{"id": post.ID, "kind": post.Kind})
	return map[string]any{"post": post}, nil
}

// coerceBlocksArg accepts either a Document shape ({version, blocks})
// or a raw []Block slice (agent convenience).
func coerceBlocksArg(raw any) (Document, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return Document{}, err
	}
	var doc Document
	if err := json.Unmarshal(b, &doc); err == nil && doc.Blocks != nil {
		return doc, nil
	}
	var blocks []Block
	if err := json.Unmarshal(b, &blocks); err != nil {
		return Document{}, fmt.Errorf("blocks: expected {version,blocks} or array, got %T", raw)
	}
	return Document{Version: documentVersion, Blocks: blocks}, nil
}

func (a *App) toolPostsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	patch, err := buildPostPatch(args)
	if err != nil {
		return nil, err
	}
	post, err := dbUpdatePost(ctx.AppDB(), pid, id, patch, asString(args["author"]), asStringDefault(args["source"], "agent"), asString(args["note"]))
	if err != nil {
		return nil, err
	}
	ctx.Emit("post.updated", map[string]any{"id": id})
	return map[string]any{"post": post}, nil
}

func asStringDefault(v any, def string) string {
	s := asString(v)
	if s == "" {
		return def
	}
	return s
}

func buildPostPatch(args map[string]any) (PostPatch, error) {
	var p PostPatch
	if v, ok := args["title"].(string); ok {
		p.Title = &v
	}
	if v, ok := args["excerpt"].(string); ok {
		p.Excerpt = &v
	}
	if v, ok := args["author"].(string); ok {
		p.Author = &v
	}
	if v, ok := args["template"].(string); ok {
		p.Template = &v
	}
	if v, ok := args["slug"].(string); ok {
		p.Slug = &v
	}
	if v, ok := args["locale"].(string); ok {
		p.Locale = &v
	}
	if v, ok := args["seo_title"].(string); ok {
		p.SEOTitle = &v
	}
	if v, ok := args["seo_description"].(string); ok {
		p.SEODescription = &v
	}
	if v, ok := args["seo_canonical"].(string); ok {
		p.SEOCanonical = &v
	}
	if v, ok := asInt64(args["menu_order"]); ok {
		mo := int(v)
		p.MenuOrder = &mo
	}
	if v, ok := asInt64(args["parent_id"]); ok {
		p.ParentID = &v
	}
	if v, ok := asInt64(args["featured_media_id"]); ok {
		p.FeaturedMediaID = &v
	}
	if v, ok := asInt64(args["og_image_media_id"]); ok {
		p.OGImageMediaID = &v
	}
	if raw, ok := args["blocks"]; ok && raw != nil {
		doc, err := coerceBlocksArg(raw)
		if err != nil {
			return p, err
		}
		p.Blocks = &doc
	}
	return p, nil
}

func (a *App) toolPostsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		p, err := dbGetPost(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"post": p}, nil
	}
	slug := asString(args["slug"])
	if slug == "" {
		return nil, errors.New("id or slug required")
	}
	kind := asString(args["kind"])
	if kind == "" {
		kind = "post"
	}
	locale := asString(args["locale"])
	if locale == "" {
		locale = "en"
	}
	p, err := dbGetPostBySlug(ctx.AppDB(), pid, kind, locale, slug)
	if err != nil {
		return nil, err
	}
	return map[string]any{"post": p}, nil
}

func (a *App) toolPostsGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var post *Post
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		post, err = dbGetPost(ctx.AppDB(), pid, id)
	} else if slug := asString(args["slug"]); slug != "" {
		kind := asString(args["kind"])
		if kind == "" {
			kind = "post"
		}
		locale := asString(args["locale"])
		if locale == "" {
			locale = "en"
		}
		post, err = dbGetPostBySlug(ctx.AppDB(), pid, kind, locale, slug)
	} else {
		return nil, errors.New("id or slug required")
	}
	if err != nil {
		return nil, err
	}
	revs, _ := dbListRevisions(ctx.AppDB(), post.ID, 10)
	terms, _ := dbListPostTerms(ctx.AppDB(), post.ID)
	return map[string]any{"post": post, "revisions": revs, "terms": terms}, nil
}

func (a *App) toolPostsSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	s := PostSearch{
		Q:        asString(args["q"]),
		Status:   asString(args["status"]),
		Kind:     asString(args["kind"]),
		TermSlug: asString(args["term_slug"]),
		Author:   asString(args["author"]),
		Locale:   asString(args["locale"]),
	}
	if v, ok := asInt64(args["limit"]); ok {
		s.Limit = int(v)
	}
	if v, ok := asInt64(args["offset"]); ok {
		s.Offset = int(v)
	}
	if v, ok := asInt64(args["parent_id"]); ok {
		s.ParentID = &v
	}
	posts, total, err := dbSearchPosts(ctx.AppDB(), pid, s)
	if err != nil {
		return nil, err
	}
	return map[string]any{"posts": posts, "total": total}, nil
}

func (a *App) toolPostsPublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	scheduled := asString(args["scheduled_at"])
	post, err := dbPublishPost(ctx.AppDB(), pid, id, scheduled, asStringDefault(args["source"], "agent"))
	if err != nil {
		return nil, err
	}
	ctx.Emit("post.published", map[string]any{"id": id, "status": post.Status})
	return map[string]any{"post": post}, nil
}

func (a *App) toolPostsUnpublish(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	post, err := dbUnpublishPost(ctx.AppDB(), pid, id, asStringDefault(args["source"], "agent"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"post": post}, nil
}

func (a *App) toolPostsArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	post, err := dbArchivePost(ctx.AppDB(), pid, id, asStringDefault(args["source"], "agent"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"post": post}, nil
}

func (a *App) toolPostsSetHomepage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["page_id"])
	if !ok || id == 0 {
		return nil, errors.New("page_id required")
	}
	if err := dbSetSetting(ctx.AppDB(), pid, "homepage_page_id", strconv.FormatInt(id, 10)); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "homepage_page_id": id}, nil
}

func (a *App) toolPostsRevisionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	limit := 50
	if v, ok := asInt64(args["limit"]); ok {
		limit = int(v)
	}
	revs, err := dbListRevisions(ctx.AppDB(), id, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revisions": revs}, nil
}

func (a *App) toolPostsRevisionRestore(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id, ok := asInt64(args["id"])
	if !ok || id == 0 {
		return nil, errors.New("id required")
	}
	revID, ok := asInt64(args["revision_id"])
	if !ok || revID == 0 {
		return nil, errors.New("revision_id required")
	}
	post, err := dbRestoreRevision(ctx.AppDB(), pid, id, revID, asStringDefault(args["source"], "agent"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"post": post}, nil
}

// ── REST handlers ───────────────────────────────────────────────────

func (a *App) handleHTTPPostsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		s := PostSearch{
			Q:        r.URL.Query().Get("q"),
			Status:   r.URL.Query().Get("status"),
			Kind:     r.URL.Query().Get("kind"),
			TermSlug: r.URL.Query().Get("term_slug"),
			Author:   r.URL.Query().Get("author"),
			Locale:   r.URL.Query().Get("locale"),
		}
		if v := r.URL.Query().Get("limit"); v != "" {
			n, _ := strconv.Atoi(v)
			s.Limit = n
		}
		if v := r.URL.Query().Get("offset"); v != "" {
			n, _ := strconv.Atoi(v)
			s.Offset = n
		}
		posts, total, err := dbSearchPosts(ctx.AppDB(), pid, s)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"posts": posts, "total": total})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolPostsCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleHTTPPostItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/posts/")
	parts := strings.SplitN(rest, "/", 3)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	// Sub-routes: /api/posts/<id>/publish, /unpublish, /archive, /revisions, /blocks
	if len(parts) >= 2 {
		switch parts[1] {
		case "publish":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["id"] = id
			body["_project_id"] = pid
			out, err := a.toolPostsPublish(ctx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "unpublish":
			out, err := a.toolPostsUnpublish(ctx, map[string]any{"id": id, "_project_id": pid})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "archive":
			out, err := a.toolPostsArchive(ctx, map[string]any{"id": id, "_project_id": pid})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "revisions":
			revs, err := dbListRevisions(ctx.AppDB(), id, 50)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			httpJSON(w, map[string]any{"revisions": revs})
			return
		case "blocks":
			a.handleHTTPBlocks(w, r, ctx, pid, id, parts)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		post, err := dbGetPost(ctx.AppDB(), pid, id)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, map[string]any{"post": post})
	case http.MethodPatch, http.MethodPut:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["id"] = id
		body["_project_id"] = pid
		out, err := a.toolPostsUpdate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodDelete:
		out, err := a.toolPostsArchive(ctx, map[string]any{"id": id, "_project_id": pid})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Block REST endpoints — thin wrappers around the block_* tools so the
// dashboard editor can do partial updates without resending the whole
// body.
func (a *App) handleHTTPBlocks(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, postID int64, parts []string) {
	// parts[0] = post id; parts[1] = "blocks"; parts[2] = block id or
	// command suffix
	if r.Method == http.MethodGet {
		post, err := dbGetPost(ctx.AppDB(), pid, postID)
		if err != nil {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpJSON(w, map[string]any{"blocks": post.BodyBlocks})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body == nil {
		body = map[string]any{}
	}
	body["post_id"] = postID
	body["_project_id"] = pid

	// Convention: POST /blocks = insert, PATCH /blocks/<id> = update,
	// POST /blocks/<id>/move = move, DELETE /blocks/<id> = delete,
	// POST /blocks/<id>/duplicate, POST /blocks/replace_all
	var out any
	var err error
	switch {
	case len(parts) == 2 && r.Method == http.MethodPost:
		out, err = a.toolBlocksInsert(ctx, body)
	case len(parts) == 3 && parts[2] == "replace_all" && r.Method == http.MethodPost:
		out, err = a.toolBlocksReplaceAll(ctx, body)
	case len(parts) == 3 && r.Method == http.MethodPatch:
		body["block_id"] = parts[2]
		out, err = a.toolBlocksUpdate(ctx, body)
	case len(parts) == 3 && r.Method == http.MethodDelete:
		body["block_id"] = parts[2]
		out, err = a.toolBlocksDelete(ctx, body)
	default:
		httpErr(w, http.StatusNotFound, "no such block route")
		return
	}
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

// ── time parsing helper (kept here so callers don't need to import) ──
var _ = time.Now
