package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: notes
display_name: Notes
version: 0.1.1
description: Simple searchable notes for Apteva agents and human teams.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions: [db.write.app]
  integrations: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: notes_create,  description: "Create a note." }
    - { name: notes_get,     description: "Fetch one note by id." }
    - { name: notes_update,  description: "Patch a note." }
    - { name: notes_append,  description: "Append text to a note body." }
    - { name: notes_search,  description: "Search/list notes." }
    - { name: notes_archive, description: "Archive a note." }
  ui_panels:
    - slot: project.page
      label: Notes
      icon: sticky-note
      entry: /ui/NotesPanel.mjs
  publishes:
    - { name: note.created,  description: "A note was created." }
    - { name: note.updated,  description: "A note was updated." }
    - { name: note.appended, description: "Text was appended to a note." }
    - { name: note.archived, description: "A note was archived." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/notes }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/notes.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("notes requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("notes mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/notes", Handler: a.handleNotes},
		{Pattern: "/notes/", Handler: a.handleNoteItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "notes_create",
			Description: "Create a note. Args: title (required), body?, kind? (default note), tags? (array), source? (manual|agent|import or any string), metadata? (object).",
			InputSchema: schemaObject(map[string]any{
				"title":    sString(),
				"body":     sString(),
				"kind":     sString(),
				"tags":     sArray("string"),
				"source":   sString(),
				"metadata": map[string]any{"type": "object"},
			}, []string{"title"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "notes_get",
			Description: "Fetch one note by id. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "notes_update",
			Description: "Patch a note. Args: id, title?, body?, kind?, tags?, source?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"id":       sInteger(),
				"title":    sString(),
				"body":     sString(),
				"kind":     sString(),
				"tags":     sArray("string"),
				"source":   sString(),
				"metadata": map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolUpdate,
		},
		{
			Name:        "notes_append",
			Description: "Append text to a note body. Args: id, text.",
			InputSchema: schemaObject(map[string]any{"id": sInteger(), "text": sString()}, []string{"id", "text"}),
			Handler:     a.toolAppend,
		},
		{
			Name:        "notes_search",
			Description: "Search/list notes. Args: q?, kind?, tag?, status? (active|archived|all), limit?.",
			InputSchema: schemaObject(map[string]any{
				"q":      sString(),
				"kind":   sString(),
				"tag":    sString(),
				"status": sString(),
				"limit":  sInteger(),
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "notes_archive",
			Description: "Archive a note. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": sInteger()}, []string{"id"}),
			Handler:     a.toolArchive,
		},
	}
}

type Note struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id,omitempty"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	Source       string          `json:"source"`
	TagsJSON     json.RawMessage `json:"tags"`
	MetadataJSON json.RawMessage `json:"metadata"`
	CreatedBy    string          `json:"created_by,omitempty"`
	UpdatedBy    string          `json:"updated_by,omitempty"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	ArchivedAt   string          `json:"archived_at,omitempty"`
}

type notePatch struct {
	Title        *string
	Body         *string
	Kind         *string
	Source       *string
	TagsJSON     *string
	MetadataJSON *string
	UpdatedBy    *string
}

type noteFilter struct {
	Q      string
	Kind   string
	Tag    string
	Status string
	Limit  int
}

// --- HTTP handlers ----------------------------------------------------------

func (a *App) handleNotes(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	switch r.Method {
	case http.MethodGet:
		filter := noteFilter{
			Q:      strings.TrimSpace(r.URL.Query().Get("q")),
			Kind:   strings.TrimSpace(r.URL.Query().Get("kind")),
			Tag:    strings.TrimSpace(r.URL.Query().Get("tag")),
			Status: strings.TrimSpace(r.URL.Query().Get("status")),
			Limit:  intQuery(r, "limit", 100),
		}
		notes, err := listNotes(ctx.AppDB(), ctx.CurrentProject(), filter)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"notes": notes})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		note, err := createNoteFromArgs(ctx.AppDB(), ctx.CurrentProject(), body)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		emit(ctx, "note.created", note)
		writeJSON(w, map[string]any{"note": note})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleNoteItem(w http.ResponseWriter, r *http.Request) {
	ctx := requestCtx(r)
	rest := strings.TrimPrefix(r.URL.Path, "/notes/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, _ := strconv.ParseInt(firstPart(parts), 10, 64)
	if id == 0 {
		http.Error(w, "note id required", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case r.Method == http.MethodGet && action == "":
		note, err := getNote(ctx.AppDB(), ctx.CurrentProject(), id)
		if err != nil {
			httpError(w, err, http.StatusInternalServerError)
			return
		}
		if note == nil {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, map[string]any{"note": note})
	case r.Method == http.MethodPatch && action == "":
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		body["id"] = float64(id)
		note, err := updateNoteFromArgs(ctx.AppDB(), ctx.CurrentProject(), body)
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		emit(ctx, "note.updated", note)
		writeJSON(w, map[string]any{"note": note})
	case r.Method == http.MethodPost && action == "append":
		var body struct {
			Text string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		note, err := appendNote(ctx.AppDB(), ctx.CurrentProject(), id, body.Text, "")
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		emit(ctx, "note.appended", note)
		writeJSON(w, map[string]any{"note": note})
	case r.Method == http.MethodPost && action == "archive":
		note, err := archiveNote(ctx.AppDB(), ctx.CurrentProject(), id, "")
		if err != nil {
			httpError(w, err, http.StatusBadRequest)
			return
		}
		emit(ctx, "note.archived", note)
		writeJSON(w, map[string]any{"note": note})
	default:
		http.Error(w, "unsupported notes route", http.StatusMethodNotAllowed)
	}
}

// --- MCP tool handlers ------------------------------------------------------

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	note, err := createNoteFromArgs(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "note.created", note)
	return map[string]any{"note": note}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	note, err := getNote(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"))
	if err != nil {
		return nil, err
	}
	if note == nil {
		return nil, sql.ErrNoRows
	}
	return map[string]any{"note": note}, nil
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	note, err := updateNoteFromArgs(ctx.AppDB(), ctx.CurrentProject(), args)
	if err != nil {
		return nil, err
	}
	emit(ctx, "note.updated", note)
	return map[string]any{"note": note}, nil
}

func (a *App) toolAppend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	note, err := appendNote(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), stringArg(args, "text"), stringArg(args, "updated_by"))
	if err != nil {
		return nil, err
	}
	emit(ctx, "note.appended", note)
	return map[string]any{"note": note}, nil
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	notes, err := listNotes(ctx.AppDB(), ctx.CurrentProject(), noteFilter{
		Q:      stringArg(args, "q"),
		Kind:   stringArg(args, "kind"),
		Tag:    stringArg(args, "tag"),
		Status: stringArg(args, "status"),
		Limit:  intArg(args, "limit", 50),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"notes": notes}, nil
}

func (a *App) toolArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	note, err := archiveNote(ctx.AppDB(), ctx.CurrentProject(), int64Arg(args, "id"), stringArg(args, "updated_by"))
	if err != nil {
		return nil, err
	}
	emit(ctx, "note.archived", note)
	return map[string]any{"note": note}, nil
}

// --- DB layer ---------------------------------------------------------------

func createNoteFromArgs(db *sql.DB, projectID string, args map[string]any) (*Note, error) {
	title := strings.TrimSpace(stringArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	body := stringArg(args, "body")
	kind := cleanToken(stringArg(args, "kind"), "note")
	source := cleanToken(stringArg(args, "source"), "manual")
	tags, err := normalizeTags(args["tags"])
	if err != nil {
		return nil, err
	}
	meta, err := normalizeObject(args["metadata"])
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO notes
			(project_id, title, body, kind, status, source, tags_json, metadata_json, created_by, updated_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, ?, ?)`,
		projectID, title, body, kind, source, tags, meta, stringArg(args, "created_by"), stringArg(args, "updated_by"), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getNoteRequired(db, projectID, id)
}

func updateNoteFromArgs(db *sql.DB, projectID string, args map[string]any) (*Note, error) {
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	patch := notePatch{}
	if s, ok := stringPtrArg(args, "title"); ok {
		title := strings.TrimSpace(*s)
		if title == "" {
			return nil, errors.New("title cannot be empty")
		}
		patch.Title = &title
	}
	if s, ok := stringPtrArg(args, "body"); ok {
		patch.Body = s
	}
	if s, ok := stringPtrArg(args, "kind"); ok {
		kind := cleanToken(*s, "note")
		patch.Kind = &kind
	}
	if s, ok := stringPtrArg(args, "source"); ok {
		source := cleanToken(*s, "manual")
		patch.Source = &source
	}
	if _, ok := args["tags"]; ok {
		tags, err := normalizeTags(args["tags"])
		if err != nil {
			return nil, err
		}
		patch.TagsJSON = &tags
	}
	if _, ok := args["metadata"]; ok {
		meta, err := normalizeObject(args["metadata"])
		if err != nil {
			return nil, err
		}
		patch.MetadataJSON = &meta
	}
	if s, ok := stringPtrArg(args, "updated_by"); ok {
		patch.UpdatedBy = s
	}
	return updateNote(db, projectID, id, patch)
}

func getNoteRequired(db *sql.DB, projectID string, id int64) (*Note, error) {
	n, err := getNote(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if n == nil {
		return nil, sql.ErrNoRows
	}
	return n, nil
}

func getNote(db *sql.DB, projectID string, id int64) (*Note, error) {
	row := db.QueryRow(`
		SELECT id, project_id, title, body, kind, status, source, tags_json, metadata_json,
		       created_by, updated_by, created_at, updated_at, COALESCE(archived_at, '')
		FROM notes
		WHERE id = ? AND project_id = ?`,
		id, projectID)
	return scanNote(row)
}

func listNotes(db *sql.DB, projectID string, f noteFilter) ([]*Note, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	status := strings.TrimSpace(f.Status)
	if status == "" {
		status = "active"
	}
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if f.Kind != "" {
		where = append(where, "kind = ?")
		args = append(args, f.Kind)
	}
	if f.Tag != "" {
		where = append(where, "tags_json LIKE ?")
		args = append(args, "%"+quoteJSONFragment(f.Tag)+"%")
	}
	if f.Q != "" {
		like := "%" + escapeLike(f.Q) + "%"
		where = append(where, "(title LIKE ? ESCAPE '\\' OR body LIKE ? ESCAPE '\\' OR tags_json LIKE ? ESCAPE '\\')")
		args = append(args, like, like, like)
	}
	args = append(args, limit)
	rows, err := db.Query(`
		SELECT id, project_id, title, body, kind, status, source, tags_json, metadata_json,
		       created_by, updated_by, created_at, updated_at, COALESCE(archived_at, '')
		FROM notes
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY updated_at DESC, id DESC
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Note{}
	for rows.Next() {
		n, err := scanNote(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func updateNote(db *sql.DB, projectID string, id int64, p notePatch) (*Note, error) {
	sets := []string{}
	args := []any{}
	add := func(sql string, val any) {
		sets = append(sets, sql)
		args = append(args, val)
	}
	if p.Title != nil {
		add("title = ?", *p.Title)
	}
	if p.Body != nil {
		add("body = ?", *p.Body)
	}
	if p.Kind != nil {
		add("kind = ?", *p.Kind)
	}
	if p.Source != nil {
		add("source = ?", *p.Source)
	}
	if p.TagsJSON != nil {
		add("tags_json = ?", *p.TagsJSON)
	}
	if p.MetadataJSON != nil {
		add("metadata_json = ?", *p.MetadataJSON)
	}
	if p.UpdatedBy != nil {
		add("updated_by = ?", *p.UpdatedBy)
	}
	add("updated_at = ?", nowUTC())
	args = append(args, id, projectID)
	res, err := db.Exec(`UPDATE notes SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`, args...)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return getNoteRequired(db, projectID, id)
}

func appendNote(db *sql.DB, projectID string, id int64, text, updatedBy string) (*Note, error) {
	text = strings.TrimRight(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text required")
	}
	note, err := getNote(db, projectID, id)
	if err != nil {
		return nil, err
	}
	if note == nil {
		return nil, sql.ErrNoRows
	}
	body := note.Body
	if strings.TrimSpace(body) == "" {
		body = text
	} else {
		body = body + "\n\n" + text
	}
	return updateNote(db, projectID, id, notePatch{Body: &body, UpdatedBy: &updatedBy})
}

func archiveNote(db *sql.DB, projectID string, id int64, updatedBy string) (*Note, error) {
	now := nowUTC()
	res, err := db.Exec(`
		UPDATE notes
		SET status = 'archived', archived_at = ?, updated_at = ?, updated_by = ?
		WHERE id = ? AND project_id = ?`,
		now, now, updatedBy, id, projectID)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	return getNoteRequired(db, projectID, id)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanNote(s scanner) (*Note, error) {
	var n Note
	var tags, meta string
	err := s.Scan(&n.ID, &n.ProjectID, &n.Title, &n.Body, &n.Kind, &n.Status, &n.Source,
		&tags, &meta, &n.CreatedBy, &n.UpdatedBy, &n.CreatedAt, &n.UpdatedAt, &n.ArchivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	n.TagsJSON = json.RawMessage(defaultJSON(tags, "[]"))
	n.MetadataJSON = json.RawMessage(defaultJSON(meta, "{}"))
	return &n, nil
}

// --- helpers ---------------------------------------------------------------

func requestCtx(r *http.Request) *sdk.AppCtx {
	ctx := globalCtx
	if ctx == nil {
		return nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return ctx.WithProject(pid)
	}
	return ctx
}

func emit(ctx *sdk.AppCtx, topic string, note *Note) {
	if ctx == nil || note == nil {
		return
	}
	ctx.EmitWithProject(topic, note.ProjectID, map[string]any{
		"id":     note.ID,
		"title":  note.Title,
		"kind":   note.Kind,
		"status": note.Status,
		"tags":   json.RawMessage(defaultJSON(string(note.TagsJSON), "[]")),
	})
}

func normalizeTags(v any) (string, error) {
	if v == nil {
		return "[]", nil
	}
	var tags []string
	switch x := v.(type) {
	case []string:
		tags = x
	case []any:
		for _, item := range x {
			s := strings.TrimSpace(fmt.Sprint(item))
			if s != "" {
				tags = append(tags, s)
			}
		}
	case string:
		for _, part := range strings.Split(x, ",") {
			s := strings.TrimSpace(part)
			if s != "" {
				tags = append(tags, s)
			}
		}
	default:
		return "", errors.New("tags must be an array or comma-separated string")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || seen[tag] {
			continue
		}
		seen[tag] = true
		out = append(out, tag)
	}
	b, _ := json.Marshal(out)
	return string(b), nil
}

func normalizeObject(v any) (string, error) {
	if v == nil {
		return "{}", nil
	}
	switch x := v.(type) {
	case map[string]any:
		b, err := json.Marshal(x)
		return string(b), err
	case json.RawMessage:
		if !json.Valid(x) {
			return "", errors.New("metadata must be valid JSON")
		}
		return string(x), nil
	case string:
		x = strings.TrimSpace(x)
		if x == "" {
			return "{}", nil
		}
		if !json.Valid([]byte(x)) {
			return "", errors.New("metadata string must be valid JSON")
		}
		return x, nil
	default:
		return "", errors.New("metadata must be an object")
	}
}

func cleanToken(s, fallback string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return fallback
	}
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key]
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	}
	return ""
}

func stringPtrArg(args map[string]any, key string) (*string, bool) {
	v, ok := args[key]
	if !ok {
		return nil, false
	}
	s, ok := v.(string)
	if !ok {
		s = fmt.Sprint(v)
	}
	return &s, true
}

func int64Arg(args map[string]any, key string) int64 {
	switch x := args[key].(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func intArg(args map[string]any, key string, fallback int) int {
	n := int(int64Arg(args, key))
	if n == 0 {
		return fallback
	}
	return n
}

func intQuery(r *http.Request, key string, fallback int) int {
	raw := strings.TrimSpace(r.URL.Query().Get(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func firstPart(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}

func quoteJSONFragment(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func defaultJSON(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func httpError(w http.ResponseWriter, err error, status int) {
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), status)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func sString() map[string]any  { return map[string]any{"type": "string"} }
func sInteger() map[string]any { return map[string]any{"type": "integer"} }

func sArray(item string) map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": item}}
}
