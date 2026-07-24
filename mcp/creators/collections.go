package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxCollectionPosts    = 500
	maxCollectionMetadata = 64 << 10
)

type Collection struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id"`
	SpaceID            int64           `json:"space_id"`
	Title              string          `json:"title"`
	Slug               string          `json:"slug"`
	Description        string          `json:"description"`
	Status             string          `json:"status"`
	CoverStorageFileID *int64          `json:"cover_storage_file_id,omitempty"`
	Metadata           json.RawMessage `json:"metadata"`
	SortOrder          int             `json:"sort_order"`
	PostCount          int             `json:"post_count"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
}

type OrderedPost struct {
	Position int  `json:"position"`
	Post     Post `json:"post"`
}

type CollectionPostView struct {
	Position    int             `json:"position"`
	Locked      bool            `json:"locked"`
	ID          int64           `json:"id"`
	Title       string          `json:"title"`
	Slug        string          `json:"slug"`
	Visibility  string          `json:"visibility"`
	TierIDs     json.RawMessage `json:"tier_ids"`
	PublishedAt string          `json:"published_at,omitempty"`
	Body        string          `json:"body,omitempty"`
	Attachments []Attachment    `json:"attachments,omitempty"`
}

func createCollection(ctx *sdk.AppCtx, pid string, spaceID int64, args map[string]any) (*Collection, error) {
	title := cleanString(args["title"])
	if title == "" {
		return nil, errors.New("title required")
	}
	if len(title) > 200 {
		return nil, errors.New("title must be 200 characters or fewer")
	}
	description := strArg(args, "description")
	if len(description) > 5000 {
		return nil, errors.New("description must be 5000 characters or fewer")
	}
	slug := slugify(title)
	if requested := strArg(args, "slug"); requested != "" {
		slug = slugify(requested)
	}
	status := strArg(args, "status")
	if status == "" {
		status = "draft"
	}
	if !validCollectionStatus(status) {
		return nil, fmt.Errorf("invalid collection status %q", status)
	}
	metadata, err := collectionMetadata(args["metadata"])
	if err != nil {
		return nil, err
	}
	coverID := int64Arg(args, "cover_storage_file_id")
	if coverID > 0 {
		if err := validateCollectionCover(ctx, pid, coverID); err != nil {
			return nil, err
		}
	}
	var cover any
	if coverID > 0 {
		cover = coverID
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO collections
		 (project_id, space_id, title, slug, description, status, cover_storage_file_id, metadata, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, spaceID, title, slug, description, status, cover, string(metadata), intArg(args, "sort_order", 0),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	collection, err := getCollection(ctx.AppDB(), pid, spaceID, id)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "collection.created", "agent", "collection", id, map[string]any{"title": title})
	}
	return collection, err
}

func listCollections(db *sql.DB, pid string, spaceID int64, status string) ([]Collection, error) {
	where := []string{"c.project_id=?", "c.space_id=?"}
	args := []any{pid, spaceID}
	if status != "" {
		if !validCollectionStatus(status) {
			return nil, fmt.Errorf("invalid collection status %q", status)
		}
		where = append(where, "c.status=?")
		args = append(args, status)
	} else {
		where = append(where, "c.status<>'archived'")
	}
	rows, err := db.Query(collectionSelect()+` WHERE `+strings.Join(where, " AND ")+` ORDER BY c.sort_order, c.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Collection{}
	for rows.Next() {
		collection, err := scanCollection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *collection)
	}
	return out, rows.Err()
}

func getCollection(db *sql.DB, pid string, spaceID, id int64) (*Collection, error) {
	return scanCollection(db.QueryRow(collectionSelect()+` WHERE c.project_id=? AND c.space_id=? AND c.id=?`, pid, spaceID, id))
}

func getCollectionBySlug(db *sql.DB, pid string, spaceID int64, slug string) (*Collection, error) {
	return scanCollection(db.QueryRow(collectionSelect()+` WHERE c.project_id=? AND c.space_id=? AND c.slug=?`, pid, spaceID, slug))
}

func collectionSelect() string {
	return `SELECT c.id, c.project_id, c.space_id, c.title, c.slug, c.description, c.status,
		c.cover_storage_file_id, c.metadata, c.sort_order,
		(SELECT COUNT(*) FROM collection_posts cp WHERE cp.collection_id=c.id),
		c.created_at, c.updated_at
		FROM collections c`
}

func scanCollection(row interface{ Scan(...any) error }) (*Collection, error) {
	var collection Collection
	var cover sql.NullInt64
	var metadata string
	if err := row.Scan(
		&collection.ID, &collection.ProjectID, &collection.SpaceID, &collection.Title,
		&collection.Slug, &collection.Description, &collection.Status, &cover, &metadata,
		&collection.SortOrder, &collection.PostCount, &collection.CreatedAt, &collection.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if cover.Valid {
		collection.CoverStorageFileID = &cover.Int64
	}
	collection.Metadata = rawJSON(metadata, "{}")
	return &collection, nil
}

func updateCollection(ctx *sdk.AppCtx, pid string, spaceID, id int64, patch map[string]any) (*Collection, error) {
	collection, err := getCollection(ctx.AppDB(), pid, spaceID, id)
	if err != nil || collection == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("collection %d not found", id)
	}
	sets := []string{}
	args := []any{}
	if raw, ok := patch["title"]; ok {
		title := cleanString(raw)
		if title == "" {
			return nil, errors.New("title cannot be empty")
		}
		if len(title) > 200 {
			return nil, errors.New("title must be 200 characters or fewer")
		}
		sets, args = append(sets, "title=?"), append(args, title)
	}
	if raw, ok := patch["slug"]; ok {
		requested := cleanString(raw)
		if requested == "" {
			return nil, errors.New("slug cannot be empty")
		}
		slug := slugify(requested)
		sets, args = append(sets, "slug=?"), append(args, slug)
	}
	if description, ok := patch["description"].(string); ok {
		if len(description) > 5000 {
			return nil, errors.New("description must be 5000 characters or fewer")
		}
		sets, args = append(sets, "description=?"), append(args, description)
	}
	if raw, ok := patch["status"]; ok {
		status := cleanString(raw)
		if !validCollectionStatus(status) {
			return nil, fmt.Errorf("invalid collection status %q", status)
		}
		sets, args = append(sets, "status=?"), append(args, status)
	}
	if raw, ok := patch["metadata"]; ok {
		metadata, err := collectionMetadata(raw)
		if err != nil {
			return nil, err
		}
		sets, args = append(sets, "metadata=?"), append(args, string(metadata))
	}
	if raw, ok := patch["cover_storage_file_id"]; ok {
		coverID := int64FromAny(raw)
		if coverID < 0 {
			return nil, errors.New("cover_storage_file_id must be a positive ID or 0 to clear")
		}
		if coverID > 0 {
			if err := validateCollectionCover(ctx, pid, coverID); err != nil {
				return nil, err
			}
			sets, args = append(sets, "cover_storage_file_id=?"), append(args, coverID)
		} else {
			sets = append(sets, "cover_storage_file_id=NULL")
		}
	}
	if raw, ok := patch["sort_order"]; ok {
		sets, args = append(sets, "sort_order=?"), append(args, int(int64FromAny(raw)))
	}
	if len(sets) == 0 {
		return collection, nil
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	args = append(args, pid, spaceID, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE collections SET `+strings.Join(sets, ", ")+` WHERE project_id=? AND space_id=? AND id=?`,
		args...,
	); err != nil {
		return nil, err
	}
	collection, err = getCollection(ctx.AppDB(), pid, spaceID, id)
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "collection.updated", "agent", "collection", id, patch)
	}
	return collection, err
}

func archiveCollection(ctx *sdk.AppCtx, pid string, spaceID, id int64) (*Collection, error) {
	collection, err := updateCollection(ctx, pid, spaceID, id, map[string]any{"status": "archived"})
	if err == nil {
		_ = logEvent(ctx, pid, spaceID, "collection.archived", "agent", "collection", id, nil)
	}
	return collection, err
}

func setCollectionPosts(ctx *sdk.AppCtx, pid string, spaceID, collectionID int64, postIDs []int64) ([]OrderedPost, error) {
	collection, err := getCollection(ctx.AppDB(), pid, spaceID, collectionID)
	if err != nil || collection == nil {
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("collection %d not found", collectionID)
	}
	if len(postIDs) > maxCollectionPosts {
		return nil, fmt.Errorf("a collection can contain at most %d posts", maxCollectionPosts)
	}
	seen := map[int64]bool{}
	for _, postID := range postIDs {
		if postID <= 0 || seen[postID] {
			return nil, errors.New("post_ids must contain unique positive IDs")
		}
		seen[postID] = true
		post, err := getPost(ctx.AppDB(), pid, spaceID, postID, false)
		if err != nil {
			return nil, err
		}
		if post == nil {
			return nil, fmt.Errorf("post %d does not belong to this creator space", postID)
		}
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM collection_posts WHERE project_id=? AND space_id=? AND collection_id=?`,
		pid, spaceID, collectionID,
	); err != nil {
		return nil, err
	}
	for i, postID := range postIDs {
		if _, err := tx.Exec(
			`INSERT INTO collection_posts (project_id, space_id, collection_id, post_id, position)
			 VALUES (?, ?, ?, ?, ?)`,
			pid, spaceID, collectionID, postID, i+1,
		); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(`UPDATE collections SET updated_at=CURRENT_TIMESTAMP WHERE id=?`, collectionID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	_ = logEvent(ctx, pid, spaceID, "collection.posts_set", "agent", "collection", collectionID, map[string]any{"post_ids": postIDs})
	return listCollectionPosts(ctx.AppDB(), pid, spaceID, collectionID, false)
}

func listCollectionPosts(db *sql.DB, pid string, spaceID, collectionID int64, publishedOnly bool) ([]OrderedPost, error) {
	where := []string{"cp.project_id=?", "cp.space_id=?", "cp.collection_id=?"}
	args := []any{pid, spaceID, collectionID}
	if publishedOnly {
		where = append(where, "p.status='published'", "p.visibility<>'private'")
	}
	rows, err := db.Query(
		`SELECT cp.position, `+qualifiedPostColumns("p")+`
		 FROM collection_posts cp
		 JOIN posts p ON p.id=cp.post_id AND p.project_id=cp.project_id AND p.space_id=cp.space_id
		 WHERE `+strings.Join(where, " AND ")+`
		 ORDER BY cp.position, cp.post_id`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	out := []OrderedPost{}
	for rows.Next() {
		var position int
		post, err := scanPostWithPosition(rows, &position)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, OrderedPost{Position: position, Post: *post})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	postPointers := make([]*Post, len(out))
	for i := range out {
		postPointers[i] = &out[i].Post
	}
	if err := hydratePostCollectionIDs(db, pid, spaceID, postPointers); err != nil {
		return nil, err
	}
	for i := range out {
		if !publishedOnly {
			attachments, err := listAttachments(db, pid, spaceID, out[i].Post.ID)
			if err != nil {
				return nil, err
			}
			out[i].Post.Attachments = attachments
		}
	}
	return out, nil
}

func qualifiedPostColumns(alias string) string {
	prefix := alias + "."
	return prefix + `id, ` + prefix + `project_id, ` + prefix + `space_id, ` +
		prefix + `title, ` + prefix + `slug, ` + prefix + `body, ` + prefix + `status, ` +
		prefix + `visibility, ` + prefix + `tier_ids_json, COALESCE(` + prefix + `published_at,''), ` +
		`COALESCE(` + prefix + `scheduled_at,''), ` + prefix + `created_at, ` + prefix + `updated_at`
}

func scanPostWithPosition(row interface{ Scan(...any) error }, position *int) (*Post, error) {
	var post Post
	var tierIDs string
	if err := row.Scan(
		position, &post.ID, &post.ProjectID, &post.SpaceID, &post.Title, &post.Slug,
		&post.Body, &post.Status, &post.Visibility, &tierIDs, &post.PublishedAt,
		&post.ScheduledAt, &post.CreatedAt, &post.UpdatedAt,
	); err != nil {
		return nil, err
	}
	post.TierIDs = rawJSON(tierIDs, "[]")
	return &post, nil
}

func listCollectionIDsForPost(db *sql.DB, pid string, spaceID, postID int64) ([]int64, error) {
	rows, err := db.Query(
		`SELECT collection_id FROM collection_posts
		 WHERE project_id=? AND space_id=? AND post_id=?
		 ORDER BY collection_id`,
		pid, spaceID, postID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func hydratePostCollectionIDs(db *sql.DB, pid string, spaceID int64, posts []*Post) error {
	if len(posts) == 0 {
		return nil
	}
	byID := make(map[int64]*Post, len(posts))
	args := make([]any, 0, len(posts)+2)
	args = append(args, pid, spaceID)
	placeholders := make([]string, 0, len(posts))
	for _, post := range posts {
		if post == nil {
			continue
		}
		post.CollectionIDs = []int64{}
		byID[post.ID] = post
		args = append(args, post.ID)
		placeholders = append(placeholders, "?")
	}
	if len(placeholders) == 0 {
		return nil
	}
	rows, err := db.Query(
		`SELECT post_id, collection_id
		 FROM collection_posts
		 WHERE project_id=? AND space_id=? AND post_id IN (`+strings.Join(placeholders, ",")+`)
		 ORDER BY post_id, collection_id`,
		args...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var postID, collectionID int64
		if err := rows.Scan(&postID, &collectionID); err != nil {
			return err
		}
		if post := byID[postID]; post != nil {
			post.CollectionIDs = append(post.CollectionIDs, collectionID)
		}
	}
	return rows.Err()
}

func setPostCollections(ctx *sdk.AppCtx, pid string, spaceID, postID int64, collectionIDs []int64) error {
	if err := validateCollectionIDs(ctx.AppDB(), pid, spaceID, collectionIDs); err != nil {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`DELETE FROM collection_posts WHERE project_id=? AND space_id=? AND post_id=?`,
		pid, spaceID, postID,
	); err != nil {
		return err
	}
	for _, collectionID := range collectionIDs {
		if _, err := tx.Exec(
			`INSERT INTO collection_posts (project_id, space_id, collection_id, post_id, position)
			 VALUES (?, ?, ?, ?, COALESCE((
			   SELECT MAX(position)+1 FROM collection_posts WHERE collection_id=?
			 ), 1))`,
			pid, spaceID, collectionID, postID, collectionID,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func validateCollectionIDs(db *sql.DB, pid string, spaceID int64, collectionIDs []int64) error {
	if len(collectionIDs) > 50 {
		return errors.New("a post can belong to at most 50 collections")
	}
	seen := map[int64]bool{}
	for _, collectionID := range collectionIDs {
		if collectionID <= 0 || seen[collectionID] {
			return errors.New("collection_ids must contain unique positive IDs")
		}
		seen[collectionID] = true
		collection, err := getCollection(db, pid, spaceID, collectionID)
		if err != nil {
			return err
		}
		if collection == nil {
			return fmt.Errorf("collection %d does not belong to this creator space", collectionID)
		}
	}
	return nil
}

func collectionPostViews(db *sql.DB, pid string, spaceID, collectionID int64, member *Member) ([]CollectionPostView, error) {
	ordered, err := listCollectionPosts(db, pid, spaceID, collectionID, true)
	if err != nil {
		return nil, err
	}
	views := make([]CollectionPostView, 0, len(ordered))
	for i := range ordered {
		post := &ordered[i].Post
		allowed := memberCanAccessPost(member, post)
		view := CollectionPostView{
			Position: ordered[i].Position,
			Locked:   !allowed,
			ID:       post.ID, Title: post.Title, Slug: post.Slug,
			Visibility: post.Visibility, TierIDs: post.TierIDs, PublishedAt: post.PublishedAt,
		}
		if allowed {
			attachments, err := listAttachments(db, pid, spaceID, post.ID)
			if err != nil {
				return nil, err
			}
			view.Body = post.Body
			view.Attachments = accessibleAttachments(member, post, attachments)
		}
		views = append(views, view)
	}
	return views, nil
}

func publicCollectionView(collection *Collection, space *Space) map[string]any {
	view := map[string]any{
		"id": collection.ID, "title": collection.Title, "slug": collection.Slug,
		"description": collection.Description, "metadata": collection.Metadata,
		"sort_order": collection.SortOrder, "post_count": collection.PostCount,
	}
	if collection.CoverStorageFileID != nil {
		view["cover_url"] = collectionCoverURL(space, collection)
	}
	return view
}

func collectionCoverURL(space *Space, collection *Collection) string {
	return "/api/apps/creators/public/" + url.PathEscape(space.Slug) +
		"/collections/" + url.PathEscape(collection.Slug) + "/cover?project_id=" +
		url.QueryEscape(space.ProjectID)
}

func serveCollectionCover(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, collection *Collection) {
	if collection.CoverStorageFileID == nil {
		httpErr(w, http.StatusNotFound, "collection cover not found")
		return
	}
	var out map[string]any
	if err := ctx.WithProject(collection.ProjectID).PlatformAPI().CallAppResult("storage", "files_get_url", map[string]any{
		"id": *collection.CoverStorageFileID, "ttl_seconds": 3600, "_project_id": collection.ProjectID,
	}, &out); err != nil {
		httpErr(w, http.StatusBadGateway, "collection cover unavailable")
		return
	}
	target, _ := out["url"].(string)
	if target == "" {
		httpErr(w, http.StatusBadGateway, "collection cover unavailable")
		return
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (a *App) handleCollections(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		collections, err := listCollections(ctx.AppDB(), pid, space.ID, r.URL.Query().Get("status"))
		writeOrErr(w, map[string]any{"collections": collections, "count": len(collections)}, err)
	case http.MethodPost:
		var args map[string]any
		if err := readJSON(r, &args); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		collection, err := createCollection(ctx, pid, space.ID, args)
		writeOrErr(w, map[string]any{"collection": collection}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCollectionItem(w http.ResponseWriter, r *http.Request) {
	ctx := contextForRequest(r)
	pid, err := projectFromRequest(ctx, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	space, err := spaceFromRequest(ctx, pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, tail, err := collectionIDFromPath(r.URL.Path)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case len(tail) == 0 && r.Method == http.MethodGet:
		collection, err := getCollection(ctx.AppDB(), pid, space.ID, id)
		if err != nil {
			writeOrErr(w, nil, err)
			return
		}
		if collection == nil {
			httpErr(w, http.StatusNotFound, "collection not found")
			return
		}
		posts, err := listCollectionPosts(ctx.AppDB(), pid, space.ID, id, false)
		writeOrErr(w, map[string]any{"collection": collection, "posts": posts}, err)
	case len(tail) == 0 && (r.Method == http.MethodPatch || r.Method == http.MethodPut):
		var patch map[string]any
		if err := readJSON(r, &patch); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		collection, err := updateCollection(ctx, pid, space.ID, id, patch)
		writeOrErr(w, map[string]any{"collection": collection}, err)
	case len(tail) == 0 && r.Method == http.MethodDelete:
		collection, err := archiveCollection(ctx, pid, space.ID, id)
		writeOrErr(w, map[string]any{"collection": collection}, err)
	case len(tail) == 1 && tail[0] == "posts" && r.Method == http.MethodPut:
		var body map[string]any
		if err := readJSON(r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		postIDs, err := int64Slice(body["post_ids"])
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		posts, err := setCollectionPosts(ctx, pid, space.ID, id, postIDs)
		writeOrErr(w, map[string]any{"posts": posts, "count": len(posts)}, err)
	case len(tail) == 1 && tail[0] == "archive" && r.Method == http.MethodPost:
		collection, err := archiveCollection(ctx, pid, space.ID, id)
		writeOrErr(w, map[string]any{"collection": collection}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handlePublicCollections(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, space *Space, parts []string) {
	if len(parts) == 2 {
		collections, err := listCollections(ctx.AppDB(), space.ProjectID, space.ID, "published")
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]map[string]any, 0, len(collections))
		for i := range collections {
			count, err := visibleCollectionPostCount(ctx.AppDB(), space.ProjectID, space.ID, collections[i].ID)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			collections[i].PostCount = count
			views = append(views, publicCollectionView(&collections[i], space))
		}
		writeJSON(w, map[string]any{"space": space, "collections": views, "count": len(views)})
		return
	}
	if len(parts) < 3 {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	collection, err := getCollectionBySlug(ctx.AppDB(), space.ProjectID, space.ID, parts[2])
	if err != nil || collection == nil || collection.Status != "published" {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	if len(parts) == 4 && parts[3] == "cover" {
		serveCollectionCover(w, r, ctx, collection)
		return
	}
	if len(parts) != 3 {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	posts, err := collectionPostViews(ctx.AppDB(), space.ProjectID, space.ID, collection.ID, nil)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	collection.PostCount = len(posts)
	writeJSON(w, map[string]any{"space": space, "collection": publicCollectionView(collection, space), "posts": posts})
}

func (a *App) handleMemberCollections(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, member *Member, parts []string) {
	space, err := getSpace(ctx.AppDB(), member.ProjectID, member.SpaceID)
	if err != nil || space == nil {
		httpErr(w, http.StatusNotFound, "creator space not found")
		return
	}
	if len(parts) == 2 {
		collections, err := listCollections(ctx.AppDB(), member.ProjectID, member.SpaceID, "published")
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		views := make([]map[string]any, 0, len(collections))
		for i := range collections {
			count, err := visibleCollectionPostCount(ctx.AppDB(), member.ProjectID, member.SpaceID, collections[i].ID)
			if err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			collections[i].PostCount = count
			views = append(views, publicCollectionView(&collections[i], space))
		}
		writeJSON(w, map[string]any{"member": redactMember(member), "collections": views, "count": len(views)})
		return
	}
	if len(parts) < 3 {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	collection, err := getCollectionBySlug(ctx.AppDB(), member.ProjectID, member.SpaceID, parts[2])
	if err != nil || collection == nil || collection.Status != "published" {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	if len(parts) == 4 && parts[3] == "cover" {
		serveCollectionCover(w, r, ctx, collection)
		return
	}
	if len(parts) != 3 {
		httpErr(w, http.StatusNotFound, "collection not found")
		return
	}
	posts, err := collectionPostViews(ctx.AppDB(), member.ProjectID, member.SpaceID, collection.ID, member)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	collection.PostCount = len(posts)
	writeJSON(w, map[string]any{"member": redactMember(member), "collection": publicCollectionView(collection, space), "posts": posts})
}

func visibleCollectionPostCount(db *sql.DB, pid string, spaceID, collectionID int64) (int, error) {
	var count int
	err := db.QueryRow(
		`SELECT COUNT(*)
		 FROM collection_posts cp
		 JOIN posts p ON p.id=cp.post_id AND p.project_id=cp.project_id AND p.space_id=cp.space_id
		 WHERE cp.project_id=? AND cp.space_id=? AND cp.collection_id=?
		   AND p.status='published' AND p.visibility<>'private'`,
		pid, spaceID, collectionID,
	).Scan(&count)
	return count, err
}

func (a *App) toolCollectionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	collection, err := createCollection(ctx, pid, space.ID, args)
	return map[string]any{"collection": collection}, err
}

func (a *App) toolCollectionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	collections, err := listCollections(ctx.AppDB(), pid, space.ID, strArg(args, "status"))
	return map[string]any{"collections": collections, "count": len(collections)}, err
}

func (a *App) toolCollectionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	var collection *Collection
	if id := int64Arg(args, "id"); id > 0 {
		collection, err = getCollection(ctx.AppDB(), pid, space.ID, id)
	} else if slug := strArg(args, "slug"); slug != "" {
		collection, err = getCollectionBySlug(ctx.AppDB(), pid, space.ID, slug)
	} else {
		return nil, errors.New("id or slug required")
	}
	if err != nil || collection == nil {
		return map[string]any{"collection": collection, "found": false}, err
	}
	posts, err := listCollectionPosts(ctx.AppDB(), pid, space.ID, collection.ID, false)
	return map[string]any{"collection": collection, "posts": posts, "found": true}, err
}

func (a *App) toolCollectionUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	patch, _ := args["patch"].(map[string]any)
	collection, err := updateCollection(ctx, pid, space.ID, int64Arg(args, "id"), patch)
	return map[string]any{"collection": collection}, err
}

func (a *App) toolCollectionSetPosts(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	postIDs, err := int64Slice(args["post_ids"])
	if err != nil {
		return nil, err
	}
	posts, err := setCollectionPosts(ctx, pid, space.ID, int64Arg(args, "id"), postIDs)
	return map[string]any{"posts": posts, "count": len(posts)}, err
}

func (a *App) toolCollectionArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := projectFromArgs(ctx, args)
	if err != nil {
		return nil, err
	}
	space, err := spaceFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	collection, err := archiveCollection(ctx, pid, space.ID, int64Arg(args, "id"))
	return map[string]any{"collection": collection}, err
}

func validateCollectionCover(ctx *sdk.AppCtx, pid string, fileID int64) error {
	file, err := getStorageFileMetadata(ctx, pid, fileID)
	if err != nil {
		return fmt.Errorf("storage.files_get: %w", err)
	}
	if file == nil || file.ID != fileID {
		return fmt.Errorf("storage file %d not found", fileID)
	}
	if !strings.HasPrefix(strings.ToLower(file.ContentType), "image/") {
		return errors.New("collection cover must be an image")
	}
	return nil
}

func collectionMetadata(value any) (json.RawMessage, error) {
	if value == nil {
		return json.RawMessage("{}"), nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("metadata must be a JSON object")
	}
	raw, err := json.Marshal(object)
	if err != nil {
		return nil, errors.New("metadata must be a JSON object")
	}
	if len(raw) > maxCollectionMetadata {
		return nil, fmt.Errorf("metadata exceeds %d bytes", maxCollectionMetadata)
	}
	return raw, nil
}

func int64Slice(value any) ([]int64, error) {
	switch values := value.(type) {
	case []int64:
		return append([]int64(nil), values...), nil
	case []int:
		ids := make([]int64, len(values))
		for i, value := range values {
			ids[i] = int64(value)
		}
		return ids, nil
	case []any:
		ids := make([]int64, len(values))
		for i, value := range values {
			switch number := value.(type) {
			case int:
				ids[i] = int64(number)
			case int64:
				ids[i] = number
			case float64:
				if number != float64(int64(number)) {
					return nil, errors.New("IDs must be an array of integers")
				}
				ids[i] = int64(number)
			default:
				return nil, errors.New("IDs must be an array of integers")
			}
		}
		return ids, nil
	default:
		return nil, errors.New("IDs must be an array of integers")
	}
}

func collectionIDsFromMap(values map[string]any) ([]int64, bool, error) {
	raw, ok := values["collection_ids"]
	if !ok {
		return nil, false, nil
	}
	ids, err := int64Slice(raw)
	return ids, true, err
}

func validCollectionStatus(status string) bool {
	switch status {
	case "draft", "published", "archived":
		return true
	default:
		return false
	}
}

func collectionIDFromPath(path string) (int64, []string, error) {
	parts := strings.Split(strings.Trim(strings.TrimPrefix(path, "/collections/"), "/"), "/")
	if len(parts) == 0 {
		return 0, nil, errors.New("collection id required")
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		return 0, nil, errors.New("valid collection id required")
	}
	return id, parts[1:], nil
}
