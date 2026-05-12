// Apteva Todo app — personal todo list.
//
// Sibling of `tasks` (the agent mission board). Where tasks is
// instance-scoped and agent-authored, todo is project-scoped and
// human-first: the human types into the panel, the agent helps via
// MCP tools (quick-add, list, snooze, complete).
//
// Organisation:
//   * project_id (column on every table) is the apteva project scope;
//     it never changes for a given install and is set from
//     APTEVA_PROJECT_ID at runtime.
//   * lists are the user-facing buckets ("Home", "Work"…). One todo
//     belongs to at most one list (list_id, nullable → inbox).
//   * tags are an orthogonal many-to-many concept for cross-cutting
//     context (#errand, #waiting…); use them when categorisation
//     spans lists.
//
// Recurrence in v0.2 supports FREQ=DAILY|WEEKLY|MONTHLY with an
// optional INTERVAL=. On complete of a recurring todo we don't mark
// done — we roll due_at forward and leave status=open. Anything more
// elaborate (BYDAY, COUNT, UNTIL) is a v0.3 problem.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: todo
display_name: Todo
version: 0.4.0
description: Personal todo list — human-first, agent-helpful.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app]
  integrations: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: todos_quick_add,  description: "Create a todo from one NL line." }
    - { name: todos_create,     description: "Create a todo with structured fields." }
    - { name: todos_list,       description: "List todos with view/list/tag filters." }
    - { name: todos_get,        description: "Read one todo." }
    - { name: todos_update,     description: "Update a todo." }
    - { name: todos_complete,   description: "Complete (or roll-forward, if recurring)." }
    - { name: todos_uncomplete, description: "Re-open a completed todo." }
    - { name: todos_snooze,     description: "Push due_at out." }
    - { name: todos_delete,     description: "Delete a todo." }
    - { name: lists_list,       description: "List the user-facing buckets." }
    - { name: lists_create,     description: "Create a list." }
    - { name: lists_update,     description: "Update a list." }
    - { name: lists_delete,     description: "Delete a list (todos move to inbox)." }
    - { name: list_groups_list,   description: "List list-groups (containers above lists)." }
    - { name: list_groups_create, description: "Create a list-group." }
    - { name: list_groups_update, description: "Update a list-group." }
    - { name: list_groups_delete, description: "Delete a list-group (member lists become ungrouped)." }
    - { name: tags_list,        description: "List tags with usage counts." }
  ui_panels:
    - slot: project.page
      label: Todo
      icon: check-square
      entry: /ui/TodoPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/todo
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/todo.db
  migrations: migrations/
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
		return errors.New("todo requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("todo mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ─────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/lists", Handler: a.handleLists},
		{Pattern: "/lists/", Handler: a.handleListsItem},
		{Pattern: "/list_groups", Handler: a.handleListGroups},
		{Pattern: "/list_groups/", Handler: a.handleListGroupsItem},
		{Pattern: "/todos", Handler: a.handleTodos},
		{Pattern: "/todos/", Handler: a.handleTodosItem},
		{Pattern: "/quick_add", Handler: a.handleQuickAdd},
		{Pattern: "/tags", Handler: a.handleTags},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "todos_quick_add",
			Description: "Create a todo from one natural-language line. Hints recognised inline: priority via 'p1'..'p4', due via 'today'/'tomorrow'/'mon'..'sun'/'next week', list via '#name' (created if missing), tags via '@name'. Args: text (required), source? (human|agent, default 'agent').",
			InputSchema: schemaObject(map[string]any{
				"text":   map[string]any{"type": "string"},
				"source": map[string]any{"type": "string", "enum": []string{"human", "agent"}},
			}, []string{"text"}),
			Handler: a.toolQuickAdd},
		{Name: "todos_create",
			Description: "Create a todo with structured fields. Args: title (required), notes?, list_id? (numeric), priority? (1-4, 4=lowest default), due_at? (RFC3339 or YYYY-MM-DD), rrule? (e.g. 'FREQ=DAILY' or 'FREQ=WEEKLY;INTERVAL=2'), tags? (array of names), source?.",
			InputSchema: schemaObject(map[string]any{
				"title":    map[string]any{"type": "string"},
				"notes":    map[string]any{"type": "string"},
				"list_id":  map[string]any{"type": "integer"},
				"priority": map[string]any{"type": "integer"},
				"due_at":   map[string]any{"type": "string"},
				"rrule":    map[string]any{"type": "string"},
				"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"source":   map[string]any{"type": "string"},
			}, []string{"title"}),
			Handler: a.toolTodosCreate},
		{Name: "todos_list",
			Description: "List todos. Args: view? (inbox|today|upcoming|overdue|all|done; default 'today'), list_id? (numeric), tag? (name), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"view":    map[string]any{"type": "string"},
				"list_id": map[string]any{"type": "integer"},
				"tag":     map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolTodosList},
		{Name: "todos_get",
			Description: "Read a single todo by id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosGet},
		{Name: "todos_update",
			Description: "Update a todo. Pass any subset of: title, notes, priority, due_at, list_id, rrule, tags. To clear due_at pass empty string. tags replaces the full set.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"title":    map[string]any{"type": "string"},
				"notes":    map[string]any{"type": "string"},
				"priority": map[string]any{"type": "integer"},
				"due_at":   map[string]any{"type": "string"},
				"list_id":  map[string]any{"type": "integer"},
				"rrule":    map[string]any{"type": "string"},
				"tags":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			}, []string{"id"}),
			Handler: a.toolTodosUpdate},
		{Name: "todos_complete",
			Description: "Mark a todo done. If the todo has an rrule, due_at rolls forward to the next occurrence and status stays open.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosComplete},
		{Name: "todos_uncomplete",
			Description: "Re-open a completed todo (status -> open, completed_at cleared).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosUncomplete},
		{Name: "todos_snooze",
			Description: "Push a todo's due_at out. Pass either 'until' (RFC3339) or 'for' shortcut: '1h','3h','tomorrow','next_week','weekend'.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"until": map[string]any{"type": "string"},
				"for":   map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolTodosSnooze},
		{Name: "todos_delete",
			Description: "Delete a todo permanently.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosDelete},
		{Name: "lists_list",
			Description: "List the user-facing buckets in this scope (archived included; filter client-side).",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolListsList},
		{Name: "lists_create",
			Description: "Create a list. Args: name (required), color? (#hex).",
			InputSchema: schemaObject(map[string]any{
				"name":  map[string]any{"type": "string"},
				"color": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolListsCreate},
		{Name: "lists_update",
			Description: "Update a list. Args: id (required), name?, color?, archived?, group_id? (0 to ungroup).",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"name":     map[string]any{"type": "string"},
				"color":    map[string]any{"type": "string"},
				"archived": map[string]any{"type": "boolean"},
				"group_id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolListsUpdate},
		{Name: "lists_delete",
			Description: "Delete a list. Existing todos drop list_id to NULL (move to inbox). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolListsDelete},
		{Name: "list_groups_list",
			Description: "List the list-groups in this scope (containers above lists). Lists with no group are 'ungrouped'.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolListGroupsList},
		{Name: "list_groups_create",
			Description: "Create a list-group. Args: name (required), color? (#hex, default #6b7280).",
			InputSchema: schemaObject(map[string]any{
				"name":  map[string]any{"type": "string"},
				"color": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolListGroupsCreate},
		{Name: "list_groups_update",
			Description: "Update a list-group. Args: id (required), name?, color?, archived?.",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"name":     map[string]any{"type": "string"},
				"color":    map[string]any{"type": "string"},
				"archived": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolListGroupsUpdate},
		{Name: "list_groups_delete",
			Description: "Delete a list-group. Member lists become ungrouped (group_id → NULL). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolListGroupsDelete},
		{Name: "tags_list",
			Description: "List tags in this scope with usage counts.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolTagsList},
	}
}

// ─── Models ──────────────────────────────────────────────────────

type List struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	GroupID   *int64 `json:"group_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
	SortOrder int64  `json:"sort_order"`
	CreatedAt string `json:"created_at"`
}

type ListGroup struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Archived  bool   `json:"archived"`
	SortOrder int64  `json:"sort_order"`
	CreatedAt string `json:"created_at"`
}

type Todo struct {
	ID           int64    `json:"id"`
	ProjectID    string   `json:"project_id"`
	ListID       *int64   `json:"list_id"`
	Title        string   `json:"title"`
	Notes        string   `json:"notes"`
	Priority     int      `json:"priority"`
	DueAt        string   `json:"due_at,omitempty"`
	SnoozedUntil string   `json:"snoozed_until,omitempty"`
	RRule        string   `json:"rrule,omitempty"`
	Status       string   `json:"status"`
	CompletedAt  string   `json:"completed_at,omitempty"`
	Source       string   `json:"source"`
	Tags         []string `json:"tags"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// ─── DB helpers ──────────────────────────────────────────────────

func projectScope() string {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid
	}
	return "default"
}

const listCols = `id, project_id, group_id, name, color, archived, sort_order, created_at`

func scanList(scan func(...any) error) (List, error) {
	var l List
	var grp sql.NullInt64
	var arch int
	if err := scan(&l.ID, &l.ProjectID, &grp, &l.Name, &l.Color, &arch, &l.SortOrder, &l.CreatedAt); err != nil {
		return l, err
	}
	if grp.Valid {
		v := grp.Int64
		l.GroupID = &v
	}
	l.Archived = arch == 1
	return l, nil
}

func listLists(db *sql.DB, pid string) ([]List, error) {
	rows, err := db.Query(
		`SELECT `+listCols+` FROM lists WHERE project_id = ? ORDER BY sort_order, id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []List{}
	for rows.Next() {
		l, err := scanList(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

func getList(db *sql.DB, pid string, id int64) (*List, error) {
	row := db.QueryRow(
		`SELECT `+listCols+` FROM lists WHERE id = ? AND project_id = ?`, id, pid)
	l, err := scanList(row.Scan)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func findListByName(db *sql.DB, pid, name string) (*List, error) {
	row := db.QueryRow(
		`SELECT `+listCols+` FROM lists
		  WHERE project_id = ? AND lower(name) = lower(?) LIMIT 1`,
		pid, name)
	l, err := scanList(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

func insertList(db *sql.DB, pid, name, color string) (*List, error) {
	if color == "" {
		color = "#3b82f6"
	}
	res, err := db.Exec(
		`INSERT INTO lists (project_id, name, color) VALUES (?, ?, ?)`,
		pid, name, color)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getList(db, pid, id)
}

// ─── List Group helpers ──────────────────────────────────────────

const listGroupCols = `id, project_id, name, color, archived, sort_order, created_at`

func scanListGroup(scan func(...any) error) (ListGroup, error) {
	var g ListGroup
	var arch int
	if err := scan(&g.ID, &g.ProjectID, &g.Name, &g.Color, &arch, &g.SortOrder, &g.CreatedAt); err != nil {
		return g, err
	}
	g.Archived = arch == 1
	return g, nil
}

func listGroups(db *sql.DB, pid string) ([]ListGroup, error) {
	rows, err := db.Query(
		`SELECT `+listGroupCols+` FROM list_groups WHERE project_id = ? ORDER BY sort_order, id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ListGroup{}
	for rows.Next() {
		g, err := scanListGroup(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, nil
}

func getListGroup(db *sql.DB, pid string, id int64) (*ListGroup, error) {
	row := db.QueryRow(
		`SELECT `+listGroupCols+` FROM list_groups WHERE id = ? AND project_id = ?`, id, pid)
	g, err := scanListGroup(row.Scan)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

func insertListGroup(db *sql.DB, pid, name, color string) (*ListGroup, error) {
	if color == "" {
		color = "#6b7280"
	}
	res, err := db.Exec(
		`INSERT INTO list_groups (project_id, name, color) VALUES (?, ?, ?)`,
		pid, name, color)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getListGroup(db, pid, id)
}

func upsertTag(db *sql.DB, pid, name string) (int64, error) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "@"))
	if name == "" {
		return 0, errors.New("empty tag")
	}
	if _, err := db.Exec(
		`INSERT OR IGNORE INTO tags (project_id, name) VALUES (?, ?)`, pid, name,
	); err != nil {
		return 0, err
	}
	var id int64
	err := db.QueryRow(
		`SELECT id FROM tags WHERE project_id = ? AND name = ?`, pid, name,
	).Scan(&id)
	return id, err
}

func setTodoTags(db *sql.DB, pid string, todoID int64, names []string) error {
	if _, err := db.Exec(`DELETE FROM todo_tags WHERE todo_id = ?`, todoID); err != nil {
		return err
	}
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		tagID, err := upsertTag(db, pid, n)
		if err != nil {
			return err
		}
		if _, err := db.Exec(
			`INSERT OR IGNORE INTO todo_tags (todo_id, tag_id) VALUES (?, ?)`,
			todoID, tagID,
		); err != nil {
			return err
		}
	}
	return nil
}

func tagsFor(db *sql.DB, todoID int64) ([]string, error) {
	rows, err := db.Query(
		`SELECT t.name FROM tags t
		   JOIN todo_tags tt ON tt.tag_id = t.id
		  WHERE tt.todo_id = ? ORDER BY t.name`, todoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, nil
}

func scanTodo(db *sql.DB, row *sql.Row) (*Todo, error) {
	var t Todo
	var due, snz, comp sql.NullString
	var ref sql.NullInt64
	if err := row.Scan(
		&t.ID, &t.ProjectID, &ref, &t.Title, &t.Notes, &t.Priority,
		&due, &snz, &t.RRule, &t.Status, &comp, &t.Source,
		&t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if ref.Valid {
		v := ref.Int64
		t.ListID = &v
	}
	t.DueAt = due.String
	t.SnoozedUntil = snz.String
	t.CompletedAt = comp.String
	tags, err := tagsFor(db, t.ID)
	if err != nil {
		return nil, err
	}
	t.Tags = tags
	return &t, nil
}

const todoCols = `id, project_id, list_id, title, notes, priority,
	due_at, snoozed_until, rrule, status, completed_at, source,
	created_at, updated_at`

func getTodo(db *sql.DB, pid string, id int64) (*Todo, error) {
	row := db.QueryRow(
		`SELECT `+todoCols+` FROM todos WHERE id = ? AND project_id = ?`, id, pid)
	return scanTodo(db, row)
}

func insertTodo(db *sql.DB, pid string, t *Todo) (*Todo, error) {
	if t.Source == "" {
		t.Source = "human"
	}
	if t.Priority == 0 {
		t.Priority = 4
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := db.Exec(
		`INSERT INTO todos
		  (project_id, list_id, title, notes, priority, due_at,
		   rrule, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, nullInt(t.ListID), t.Title, t.Notes, t.Priority,
		nullStr(t.DueAt), t.RRule, t.Source, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if len(t.Tags) > 0 {
		if err := setTodoTags(db, pid, id, t.Tags); err != nil {
			return nil, err
		}
	}
	return getTodo(db, pid, id)
}

// listTodos resolves a view shorthand into a SQL query. Snoozed
// todos hide from today/upcoming/overdue until snoozed_until passes.
func listTodos(db *sql.DB, pid, view string, listID *int64, tag string, limit int) ([]Todo, error) {
	if limit <= 0 {
		limit = 200
	}
	if view == "" {
		view = "today"
	}

	now := time.Now().UTC()
	todayEnd := now.Truncate(24 * time.Hour).Add(24 * time.Hour).Format(time.RFC3339)
	nowS := now.Format(time.RFC3339)

	q := `SELECT ` + todoCols + ` FROM todos WHERE project_id = ?`
	args := []any{pid}

	switch view {
	case "inbox":
		q += ` AND status = 'open' AND list_id IS NULL AND (due_at IS NULL OR due_at = '')`
	case "today":
		q += ` AND status = 'open'
		   AND (snoozed_until IS NULL OR snoozed_until = '' OR snoozed_until <= ?)
		   AND due_at IS NOT NULL AND due_at != '' AND due_at < ?`
		args = append(args, nowS, todayEnd)
	case "overdue":
		q += ` AND status = 'open'
		   AND (snoozed_until IS NULL OR snoozed_until = '' OR snoozed_until <= ?)
		   AND due_at IS NOT NULL AND due_at != '' AND due_at < ?`
		args = append(args, nowS, nowS)
	case "upcoming":
		q += ` AND status = 'open'
		   AND (snoozed_until IS NULL OR snoozed_until = '' OR snoozed_until <= ?)
		   AND due_at IS NOT NULL AND due_at != '' AND due_at >= ?`
		args = append(args, nowS, todayEnd)
	case "done":
		q += ` AND status = 'done'`
	case "all":
		q += ` AND status = 'open'`
	default:
		return nil, fmt.Errorf("unknown view: %s", view)
	}

	if listID != nil {
		q += ` AND list_id = ?`
		args = append(args, *listID)
	}
	if tag != "" {
		q += ` AND id IN (SELECT tt.todo_id FROM todo_tags tt
		                   JOIN tags t ON t.id = tt.tag_id
		                  WHERE t.project_id = ? AND lower(t.name) = lower(?))`
		args = append(args, pid, tag)
	}

	// Ordering: priority asc, then due_at asc (nulls last), then id desc.
	q += ` ORDER BY priority ASC,
	               CASE WHEN due_at IS NULL OR due_at = '' THEN 1 ELSE 0 END,
	               due_at ASC,
	               id DESC
	         LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Todo{}
	for rows.Next() {
		var t Todo
		var due, snz, comp sql.NullString
		var ref sql.NullInt64
		if err := rows.Scan(
			&t.ID, &t.ProjectID, &ref, &t.Title, &t.Notes, &t.Priority,
			&due, &snz, &t.RRule, &t.Status, &comp, &t.Source,
			&t.CreatedAt, &t.UpdatedAt,
		); err != nil {
			return nil, err
		}
		if ref.Valid {
			v := ref.Int64
			t.ListID = &v
		}
		t.DueAt, t.SnoozedUntil, t.CompletedAt = due.String, snz.String, comp.String
		out = append(out, t)
	}
	// Hydrate tags.
	for i := range out {
		tags, err := tagsFor(db, out[i].ID)
		if err == nil {
			out[i].Tags = tags
		}
	}
	return out, nil
}

func updateTodoFields(db *sql.DB, pid string, id int64, fields map[string]any) error {
	cols := []string{}
	args := []any{}
	push := func(col string, v any) {
		cols = append(cols, col+" = ?")
		args = append(args, v)
	}
	if v, ok := fields["title"]; ok {
		push("title", fmt.Sprint(v))
	}
	if v, ok := fields["notes"]; ok {
		push("notes", fmt.Sprint(v))
	}
	if v, ok := fields["priority"]; ok {
		p := int(toInt64(v))
		if p < 1 || p > 4 {
			return errors.New("priority must be 1..4")
		}
		push("priority", p)
	}
	if v, ok := fields["due_at"]; ok {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" {
			push("due_at", nil)
		} else {
			push("due_at", normaliseDue(s))
		}
	}
	if v, ok := fields["rrule"]; ok {
		push("rrule", fmt.Sprint(v))
	}
	if v, ok := fields["list_id"]; ok {
		ref := toInt64(v)
		if ref == 0 {
			push("list_id", nil)
		} else {
			if _, err := getList(db, pid, ref); err != nil {
				return fmt.Errorf("list %d not found in scope", ref)
			}
			push("list_id", ref)
		}
	}
	if v, ok := fields["snoozed_until"]; ok {
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "" {
			push("snoozed_until", nil)
		} else {
			push("snoozed_until", s)
		}
	}
	if v, ok := fields["status"]; ok {
		st := fmt.Sprint(v)
		if st != "open" && st != "done" && st != "cancelled" {
			return errors.New("status must be open|done|cancelled")
		}
		push("status", st)
		if st == "done" {
			push("completed_at", time.Now().UTC().Format(time.RFC3339))
		} else {
			push("completed_at", nil)
		}
	}
	if len(cols) == 0 && fields["tags"] == nil {
		return nil
	}
	if len(cols) > 0 {
		cols = append(cols, "updated_at = ?")
		args = append(args, time.Now().UTC().Format(time.RFC3339))
		args = append(args, id, pid)
		_, err := db.Exec(
			`UPDATE todos SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`,
			args...,
		)
		if err != nil {
			return err
		}
	}
	if v, ok := fields["tags"]; ok {
		names, err := toStringSlice(v)
		if err != nil {
			return err
		}
		return setTodoTags(db, pid, id, names)
	}
	return nil
}

// rollRecurring advances due_at to the next occurrence based on rrule.
// Supports FREQ=DAILY|WEEKLY|MONTHLY with optional INTERVAL=. Returns
// the new due_at; empty string means "no rule, just complete".
func rollRecurring(rrule, currentDue string) string {
	if rrule == "" {
		return ""
	}
	parts := map[string]string{}
	for _, kv := range strings.Split(rrule, ";") {
		if i := strings.Index(kv, "="); i > 0 {
			parts[strings.ToUpper(strings.TrimSpace(kv[:i]))] = strings.TrimSpace(kv[i+1:])
		}
	}
	freq := strings.ToUpper(parts["FREQ"])
	interval := 1
	if v, err := strconv.Atoi(parts["INTERVAL"]); err == nil && v > 0 {
		interval = v
	}
	base := time.Now().UTC()
	if currentDue != "" {
		if t, err := parseFlexible(currentDue); err == nil {
			base = t
		}
	}
	var next time.Time
	switch freq {
	case "DAILY":
		next = base.AddDate(0, 0, interval)
	case "WEEKLY":
		next = base.AddDate(0, 0, 7*interval)
	case "MONTHLY":
		next = base.AddDate(0, interval, 0)
	default:
		return ""
	}
	return next.Format(time.RFC3339)
}

// ─── HTTP handlers ───────────────────────────────────────────────

func (a *App) handleLists(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	switch r.Method {
	case http.MethodGet:
		out, err := listLists(ctx.AppDB(), projectScope())
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct{ Name, Color string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			http.Error(w, "name required", 400)
			return
		}
		l, err := insertList(ctx.AppDB(), projectScope(), body.Name, body.Color)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, l)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleListsItem(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/lists/")
	pid := projectScope()
	switch r.Method {
	case http.MethodPut:
		var fields map[string]any
		json.NewDecoder(r.Body).Decode(&fields)
		cols, args := []string{}, []any{}
		if v, ok := fields["name"]; ok {
			cols = append(cols, "name = ?")
			args = append(args, fmt.Sprint(v))
		}
		if v, ok := fields["color"]; ok {
			cols = append(cols, "color = ?")
			args = append(args, fmt.Sprint(v))
		}
		if v, ok := fields["archived"]; ok {
			arch := 0
			if b, ok := v.(bool); ok && b {
				arch = 1
			}
			cols = append(cols, "archived = ?")
			args = append(args, arch)
		}
		if v, ok := fields["group_id"]; ok {
			gid := toInt64(v)
			if gid == 0 {
				cols = append(cols, "group_id = NULL")
			} else {
				if _, err := getListGroup(ctx.AppDB(), pid, gid); err != nil {
					http.Error(w, fmt.Sprintf("group %d not found in scope", gid), 400)
					return
				}
				cols = append(cols, "group_id = ?")
				args = append(args, gid)
			}
		}
		if len(cols) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		args = append(args, id, pid)
		if _, err := ctx.AppDB().Exec(
			`UPDATE lists SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`,
			args...,
		); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		l, _ := getList(ctx.AppDB(), pid, id)
		writeJSON(w, l)
	case http.MethodDelete:
		// Detach todos first, then drop the row.
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if _, err := tx.Exec(
			`UPDATE todos SET list_id = NULL WHERE list_id = ? AND project_id = ?`,
			id, pid,
		); err != nil {
			tx.Rollback()
			http.Error(w, err.Error(), 500)
			return
		}
		if _, err := tx.Exec(
			`DELETE FROM lists WHERE id = ? AND project_id = ?`, id, pid,
		); err != nil {
			tx.Rollback()
			http.Error(w, err.Error(), 500)
			return
		}
		tx.Commit()
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PUT or DELETE", 405)
	}
}

func (a *App) handleTodos(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		view := r.URL.Query().Get("view")
		tag := r.URL.Query().Get("tag")
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		var ref *int64
		if s := r.URL.Query().Get("list_id"); s != "" {
			n, _ := strconv.ParseInt(s, 10, 64)
			ref = &n
		}
		out, err := listTodos(ctx.AppDB(), pid, view, ref, tag, limit)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Title    string   `json:"title"`
			Notes    string   `json:"notes"`
			ListID   int64    `json:"list_id"`
			Priority int      `json:"priority"`
			DueAt    string   `json:"due_at"`
			RRule    string   `json:"rrule"`
			Tags     []string `json:"tags"`
			Source   string   `json:"source"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if strings.TrimSpace(body.Title) == "" {
			http.Error(w, "title required", 400)
			return
		}
		t := &Todo{
			Title:    body.Title,
			Notes:    body.Notes,
			Priority: body.Priority,
			DueAt:    normaliseDue(body.DueAt),
			RRule:    body.RRule,
			Tags:     body.Tags,
			Source:   body.Source,
		}
		if body.ListID != 0 {
			t.ListID = &body.ListID
		}
		out, err := insertTodo(ctx.AppDB(), pid, t)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleTodosItem(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/todos/")
	pid := projectScope()
	rest := strings.TrimPrefix(r.URL.Path, fmt.Sprintf("/todos/%d", id))
	rest = strings.TrimPrefix(rest, "/")
	switch {
	case rest == "complete" && r.Method == http.MethodPost:
		out, err := completeTodo(ctx.AppDB(), pid, id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case rest == "uncomplete" && r.Method == http.MethodPost:
		if err := updateTodoFields(ctx.AppDB(), pid, id, map[string]any{"status": "open"}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out, _ := getTodo(ctx.AppDB(), pid, id)
		writeJSON(w, out)
	case rest == "snooze" && r.Method == http.MethodPost:
		var body struct{ Until, For string }
		json.NewDecoder(r.Body).Decode(&body)
		until, err := resolveSnooze(body.Until, body.For)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := updateTodoFields(ctx.AppDB(), pid, id, map[string]any{
			"snoozed_until": until, "due_at": until,
		}); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out, _ := getTodo(ctx.AppDB(), pid, id)
		writeJSON(w, out)
	case rest == "" && r.Method == http.MethodGet:
		out, err := getTodo(ctx.AppDB(), pid, id)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		writeJSON(w, out)
	case rest == "" && r.Method == http.MethodPut:
		var fields map[string]any
		json.NewDecoder(r.Body).Decode(&fields)
		if err := updateTodoFields(ctx.AppDB(), pid, id, fields); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		out, _ := getTodo(ctx.AppDB(), pid, id)
		writeJSON(w, out)
	case rest == "" && r.Method == http.MethodDelete:
		if _, err := ctx.AppDB().Exec(
			`DELETE FROM todos WHERE id = ? AND project_id = ?`, id, pid,
		); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method/path not supported", 405)
	}
}

func (a *App) handleQuickAdd(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", 405)
		return
	}
	ctx := mustCtx(r)
	var body struct {
		Text   string `json:"text"`
		Source string `json:"source"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	out, err := quickAdd(ctx.AppDB(), projectScope(), body.Text, body.Source)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleTags(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", 405)
		return
	}
	ctx := mustCtx(r)
	out, err := listTagsWithCounts(ctx.AppDB(), projectScope())
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// completeTodo handles the recurring-rollover case: if the todo has
// an rrule, we bump due_at and leave status=open; otherwise mark done.
func completeTodo(db *sql.DB, pid string, id int64) (*Todo, error) {
	t, err := getTodo(db, pid, id)
	if err != nil {
		return nil, err
	}
	if t.RRule != "" {
		next := rollRecurring(t.RRule, t.DueAt)
		if next != "" {
			if err := updateTodoFields(db, pid, id, map[string]any{
				"due_at":        next,
				"snoozed_until": "",
			}); err != nil {
				return nil, err
			}
			return getTodo(db, pid, id)
		}
	}
	if err := updateTodoFields(db, pid, id, map[string]any{"status": "done"}); err != nil {
		return nil, err
	}
	return getTodo(db, pid, id)
}

func listTagsWithCounts(db *sql.DB, pid string) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT t.id, t.name, COUNT(tt.todo_id)
		   FROM tags t LEFT JOIN todo_tags tt ON tt.tag_id = t.id
		  WHERE t.project_id = ?
		  GROUP BY t.id ORDER BY t.name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id int64
		var name string
		var n int
		if err := rows.Scan(&id, &name, &n); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "count": n})
	}
	return out, nil
}

// ─── Quick-add NL parser ─────────────────────────────────────────
//
// Recognised tokens (anywhere in the line, removed before storing):
//   p1 p2 p3 p4         priority (1=highest)
//   today               today end-of-day (RFC3339 UTC)
//   tomorrow            +1 day at 09:00
//   mon..sun            next occurrence of that weekday at 09:00
//   next week           +7 days at 09:00
//   #list-name          list (created if missing); first match wins
//   @tag                tag (lowercased); repeat for multiple
//
// The remaining text becomes the title. Trim is aggressive — multiple
// spaces collapse to one.
func quickAdd(db *sql.DB, pid, text, source string) (*Todo, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errors.New("text required")
	}
	if source == "" {
		source = "agent"
	}
	priority := 4
	var dueAt string
	var listID *int64
	tags := []string{}

	tokens := strings.Fields(text)
	keep := tokens[:0]
	for _, tok := range tokens {
		low := strings.ToLower(tok)
		switch {
		case low == "p1" || low == "p2" || low == "p3" || low == "p4":
			priority = int(low[1] - '0')
		case low == "today":
			dueAt = endOfDay(time.Now().UTC())
		case low == "tomorrow":
			dueAt = atNineAM(time.Now().UTC().AddDate(0, 0, 1))
		case low == "next" || low == "this":
			// Combined with following token below — defer.
			keep = append(keep, tok)
		case isWeekday(low):
			dueAt = atNineAM(nextWeekday(time.Now().UTC(), weekdayOf(low)))
		case strings.HasPrefix(tok, "#") && len(tok) > 1:
			name := tok[1:]
			l, err := findListByName(db, pid, name)
			if err != nil {
				return nil, err
			}
			if l == nil {
				l, err = insertList(db, pid, name, "")
				if err != nil {
					return nil, err
				}
			}
			id := l.ID
			listID = &id
		case strings.HasPrefix(tok, "@") && len(tok) > 1:
			tags = append(tags, strings.ToLower(tok[1:]))
		default:
			keep = append(keep, tok)
		}
	}
	// Two-token combos: "next week", "next mon", etc.
	final := []string{}
	for i := 0; i < len(keep); i++ {
		t := keep[i]
		low := strings.ToLower(t)
		if (low == "next" || low == "this") && i+1 < len(keep) {
			n := strings.ToLower(keep[i+1])
			switch {
			case n == "week":
				dueAt = atNineAM(time.Now().UTC().AddDate(0, 0, 7))
				i++
				continue
			case n == "month":
				dueAt = atNineAM(time.Now().UTC().AddDate(0, 1, 0))
				i++
				continue
			case isWeekday(n):
				dueAt = atNineAM(nextWeekday(time.Now().UTC(), weekdayOf(n)))
				i++
				continue
			}
		}
		final = append(final, t)
	}
	title := strings.TrimSpace(strings.Join(final, " "))
	if title == "" {
		return nil, errors.New("title empty after parsing")
	}
	t := &Todo{
		Title:    title,
		Priority: priority,
		DueAt:    dueAt,
		ListID:   listID,
		Tags:     tags,
		Source:   source,
	}
	return insertTodo(db, pid, t)
}

func isWeekday(s string) bool {
	switch strings.ToLower(s) {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun",
		"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday":
		return true
	}
	return false
}

func weekdayOf(s string) time.Weekday {
	switch strings.ToLower(s) {
	case "mon", "monday":
		return time.Monday
	case "tue", "tuesday":
		return time.Tuesday
	case "wed", "wednesday":
		return time.Wednesday
	case "thu", "thursday":
		return time.Thursday
	case "fri", "friday":
		return time.Friday
	case "sat", "saturday":
		return time.Saturday
	default:
		return time.Sunday
	}
}

func nextWeekday(from time.Time, target time.Weekday) time.Time {
	d := (int(target) - int(from.Weekday()) + 7) % 7
	if d == 0 {
		d = 7
	}
	return from.AddDate(0, 0, d)
}

func atNineAM(t time.Time) string {
	return time.Date(t.Year(), t.Month(), t.Day(), 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
}

func endOfDay(t time.Time) string {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 0, 0, time.UTC).Format(time.RFC3339)
}

// resolveSnooze accepts either an absolute RFC3339 'until' or a
// shorthand 'for' value. shorthand wins if 'until' is empty.
func resolveSnooze(until, forStr string) (string, error) {
	if until != "" {
		if _, err := time.Parse(time.RFC3339, until); err != nil {
			return "", fmt.Errorf("until: %w", err)
		}
		return until, nil
	}
	now := time.Now().UTC()
	switch strings.ToLower(strings.TrimSpace(forStr)) {
	case "1h":
		return now.Add(time.Hour).Format(time.RFC3339), nil
	case "3h":
		return now.Add(3 * time.Hour).Format(time.RFC3339), nil
	case "tomorrow":
		return atNineAM(now.AddDate(0, 0, 1)), nil
	case "next_week", "nextweek":
		return atNineAM(nextWeekday(now, time.Monday)), nil
	case "weekend":
		return atNineAM(nextWeekday(now, time.Saturday)), nil
	}
	return "", errors.New("snooze: pass 'until' (RFC3339) or 'for' (1h|3h|tomorrow|next_week|weekend)")
}

// normaliseDue accepts 'YYYY-MM-DD' or RFC3339; passes through
// anything that already parses, returns "" on garbage so the column
// doesn't accumulate junk.
func normaliseDue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return atNineAM(t)
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return s
	}
	return ""
}

func parseFlexible(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02", s)
}

// ─── MCP tool handlers ───────────────────────────────────────────

func (a *App) toolQuickAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	text, _ := args["text"].(string)
	source, _ := args["source"].(string)
	return quickAdd(ctx.AppDB(), projectScope(), text, source)
}

func (a *App) toolTodosCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	title, _ := args["title"].(string)
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("title required")
	}
	t := &Todo{
		Title:    title,
		Notes:    strArg(args, "notes", ""),
		Priority: int(toInt64(args["priority"])),
		DueAt:    normaliseDue(strArg(args, "due_at", "")),
		RRule:    strArg(args, "rrule", ""),
		Source:   strArg(args, "source", "agent"),
	}
	if v := toInt64(args["list_id"]); v != 0 {
		t.ListID = &v
	}
	if v, ok := args["tags"]; ok {
		names, err := toStringSlice(v)
		if err != nil {
			return nil, err
		}
		t.Tags = names
	}
	return insertTodo(ctx.AppDB(), projectScope(), t)
}

func (a *App) toolTodosList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	view := strArg(args, "view", "today")
	tag := strArg(args, "tag", "")
	limit := int(toInt64(args["limit"]))
	var ref *int64
	if v := toInt64(args["list_id"]); v != 0 {
		ref = &v
	}
	return listTodos(ctx.AppDB(), projectScope(), view, ref, tag, limit)
}

func (a *App) toolTodosGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	return getTodo(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolTodosUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := updateTodoFields(ctx.AppDB(), projectScope(), id, args); err != nil {
		return nil, err
	}
	return getTodo(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolTodosComplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	return completeTodo(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolTodosUncomplete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := updateTodoFields(ctx.AppDB(), projectScope(), id, map[string]any{"status": "open"}); err != nil {
		return nil, err
	}
	return getTodo(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolTodosSnooze(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	until, err := resolveSnooze(strArg(args, "until", ""), strArg(args, "for", ""))
	if err != nil {
		return nil, err
	}
	if err := updateTodoFields(ctx.AppDB(), projectScope(), id, map[string]any{
		"snoozed_until": until, "due_at": until,
	}); err != nil {
		return nil, err
	}
	return getTodo(ctx.AppDB(), projectScope(), id)
}

func (a *App) toolTodosDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM todos WHERE id = ? AND project_id = ?`, id, projectScope(),
	); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolListsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listLists(ctx.AppDB(), projectScope())
}

func (a *App) toolListsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return nil, errors.New("name required")
	}
	return insertList(ctx.AppDB(), projectScope(), name, strArg(args, "color", ""))
}

func (a *App) toolListsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope()
	cols, qa := []string{}, []any{}
	if v, ok := args["name"]; ok {
		cols = append(cols, "name = ?")
		qa = append(qa, fmt.Sprint(v))
	}
	if v, ok := args["color"]; ok {
		cols = append(cols, "color = ?")
		qa = append(qa, fmt.Sprint(v))
	}
	if v, ok := args["archived"]; ok {
		arch := 0
		if b, ok := v.(bool); ok && b {
			arch = 1
		}
		cols = append(cols, "archived = ?")
		qa = append(qa, arch)
	}
	if v, ok := args["group_id"]; ok {
		gid := toInt64(v)
		if gid == 0 {
			cols = append(cols, "group_id = NULL")
		} else {
			if _, err := getListGroup(ctx.AppDB(), pid, gid); err != nil {
				return nil, fmt.Errorf("group %d not found in scope", gid)
			}
			cols = append(cols, "group_id = ?")
			qa = append(qa, gid)
		}
	}
	if len(cols) > 0 {
		qa = append(qa, id, pid)
		if _, err := ctx.AppDB().Exec(
			`UPDATE lists SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`, qa...,
		); err != nil {
			return nil, err
		}
	}
	return getList(ctx.AppDB(), pid, id)
}

// ─── List Group HTTP + tool handlers ─────────────────────────────

func (a *App) handleListGroups(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	pid := projectScope()
	switch r.Method {
	case http.MethodGet:
		out, err := listGroups(ctx.AppDB(), pid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct{ Name, Color string }
		json.NewDecoder(r.Body).Decode(&body)
		if body.Name == "" {
			http.Error(w, "name required", 400)
			return
		}
		g, err := insertListGroup(ctx.AppDB(), pid, body.Name, body.Color)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, g)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleListGroupsItem(w http.ResponseWriter, r *http.Request) {
	ctx := mustCtx(r)
	id := pathSuffixInt(r.URL.Path, "/list_groups/")
	pid := projectScope()
	switch r.Method {
	case http.MethodPut:
		var fields map[string]any
		json.NewDecoder(r.Body).Decode(&fields)
		cols, args := []string{}, []any{}
		if v, ok := fields["name"]; ok {
			cols = append(cols, "name = ?")
			args = append(args, fmt.Sprint(v))
		}
		if v, ok := fields["color"]; ok {
			cols = append(cols, "color = ?")
			args = append(args, fmt.Sprint(v))
		}
		if v, ok := fields["archived"]; ok {
			arch := 0
			if b, ok := v.(bool); ok && b {
				arch = 1
			}
			cols = append(cols, "archived = ?")
			args = append(args, arch)
		}
		if len(cols) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		args = append(args, id, pid)
		if _, err := ctx.AppDB().Exec(
			`UPDATE list_groups SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`,
			args...,
		); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		g, _ := getListGroup(ctx.AppDB(), pid, id)
		writeJSON(w, g)
	case http.MethodDelete:
		// ON DELETE SET NULL on lists.group_id handles ungrouping.
		if _, err := ctx.AppDB().Exec(
			`DELETE FROM list_groups WHERE id = ? AND project_id = ?`, id, pid,
		); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PUT or DELETE", 405)
	}
}

func (a *App) toolListGroupsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listGroups(ctx.AppDB(), projectScope())
}

func (a *App) toolListGroupsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	if name == "" {
		return nil, errors.New("name required")
	}
	return insertListGroup(ctx.AppDB(), projectScope(), name, strArg(args, "color", ""))
}

func (a *App) toolListGroupsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope()
	cols, qa := []string{}, []any{}
	if v, ok := args["name"]; ok {
		cols = append(cols, "name = ?")
		qa = append(qa, fmt.Sprint(v))
	}
	if v, ok := args["color"]; ok {
		cols = append(cols, "color = ?")
		qa = append(qa, fmt.Sprint(v))
	}
	if v, ok := args["archived"]; ok {
		arch := 0
		if b, ok := v.(bool); ok && b {
			arch = 1
		}
		cols = append(cols, "archived = ?")
		qa = append(qa, arch)
	}
	if len(cols) > 0 {
		qa = append(qa, id, pid)
		if _, err := ctx.AppDB().Exec(
			`UPDATE list_groups SET `+strings.Join(cols, ", ")+` WHERE id = ? AND project_id = ?`, qa...,
		); err != nil {
			return nil, err
		}
	}
	return getListGroup(ctx.AppDB(), pid, id)
}

func (a *App) toolListGroupsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope()
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM list_groups WHERE id = ? AND project_id = ?`, id, pid,
	); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolListsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(
		`UPDATE todos SET list_id = NULL WHERE list_id = ? AND project_id = ?`, id, pid,
	); err != nil {
		tx.Rollback()
		return nil, err
	}
	if _, err := tx.Exec(`DELETE FROM lists WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "id": id}, nil
}

func (a *App) toolTagsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	out, err := listTagsWithCounts(ctx.AppDB(), projectScope())
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["name"].(string) < out[j]["name"].(string)
	})
	return out, nil
}

// ─── helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

func toStringSlice(v any) ([]string, error) {
	switch x := v.(type) {
	case []string:
		return x, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			out = append(out, fmt.Sprint(e))
		}
		return out, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("expected array, got %T", v)
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func pathSuffixInt(path, prefix string) int64 {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	n, _ := strconv.ParseInt(rest, 10, 64)
	return n
}

// schemaObject mirrors the helper used in the calendar app.
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

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// ─── main ────────────────────────────────────────────────────────

func main() {
	app := &App{}
	wrapped := wrapApp{app: app}
	sdk.Run(&wrapped)
}

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest             { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error      { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error      { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route            { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool               { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory     { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker              { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler  { return w.app.EventHandlers() }
