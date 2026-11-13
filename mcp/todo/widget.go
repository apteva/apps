package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

type widgetOption struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

var errWidgetSettings = errors.New("invalid widget settings")

type widgetList struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type widgetTodo struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Priority int    `json:"priority"`
	DueAt    string `json:"due_at"`
	RRule    string `json:"rrule"`
	ListID   int64  `json:"list_id"`
	ListName string `json:"list_name"`
}

type widgetPage struct {
	Title string       `json:"title"`
	Items []widgetTodo `json:"items"`
	Total int          `json:"total"`
	Lists []widgetList `json:"lists"`
}

func widgetOptions(db *sql.DB, pid string) ([]widgetOption, error) {
	rows, err := db.Query(`SELECT 'group:' || id, 'Group · ' || name FROM list_groups
		WHERE project_id = ? AND archived = 0
		UNION ALL SELECT 'list:' || l.id, 'List · ' || l.name FROM lists l
		WHERE l.project_id = ? AND l.archived = 0
		AND NOT EXISTS (SELECT 1 FROM list_groups g WHERE g.id = l.group_id
		AND g.project_id = l.project_id AND g.archived = 1)
		ORDER BY 2, 1`, pid, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []widgetOption{}
	for rows.Next() {
		var option widgetOption
		if err := rows.Scan(&option.Value, &option.Label); err != nil {
			return nil, err
		}
		out = append(out, option)
	}
	return out, rows.Err()
}

// Apply sorting before LIMIT and count the full selection, including undated
// and snoozed open tasks. A list widget is a work ledger, not a Today view.
func loadWidget(db *sql.DB, pid, target, order string, limit int) (*widgetPage, error) {
	kind, rawID, _ := strings.Cut(target, ":")
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id <= 0 || (kind != "list" && kind != "group") {
		return nil, fmt.Errorf("%w: choose a list or group", errWidgetSettings)
	}
	if order != "due" && order != "priority" {
		return nil, fmt.Errorf("%w: sort must be due or priority", errWidgetSettings)
	}
	if limit < 1 {
		limit = 8
	}
	if limit > 20 {
		limit = 20
	}
	page := &widgetPage{Items: []widgetTodo{}, Lists: []widgetList{}}
	filter := "l.id = ?"
	if kind == "group" {
		filter = "l.group_id = ?"
		err = db.QueryRow(`SELECT name FROM list_groups WHERE project_id = ? AND id = ? AND archived = 0`, pid, id).Scan(&page.Title)
	} else {
		err = db.QueryRow(`SELECT l.name FROM lists l WHERE l.project_id = ? AND l.id = ? AND l.archived = 0
			AND NOT EXISTS (SELECT 1 FROM list_groups g WHERE g.id = l.group_id AND g.project_id = l.project_id AND g.archived = 1)`, pid, id).Scan(&page.Title)
	}
	if err != nil {
		return nil, err
	}
	lists, err := db.Query(`SELECT l.id, l.name FROM lists l WHERE l.project_id = ? AND l.archived = 0 AND `+filter+` ORDER BY l.sort_order, l.id`, pid, id)
	if err != nil {
		return nil, err
	}
	for lists.Next() {
		var list widgetList
		if err := lists.Scan(&list.ID, &list.Name); err != nil {
			lists.Close()
			return nil, err
		}
		page.Lists = append(page.Lists, list)
	}
	err = lists.Err()
	lists.Close()
	if err != nil {
		return nil, err
	}
	from := ` FROM todos t JOIN lists l ON l.id = t.list_id AND l.project_id = t.project_id
		WHERE t.project_id = ? AND t.status = 'open' AND l.archived = 0 AND ` + filter
	if err := db.QueryRow(`SELECT COUNT(*)`+from, pid, id).Scan(&page.Total); err != nil {
		return nil, err
	}
	byDue := `CASE WHEN t.due_at IS NULL OR t.due_at = '' THEN 1 ELSE 0 END, t.due_at ASC`
	sorting := byDue + `, t.priority ASC`
	if order == "priority" {
		sorting = `t.priority ASC, ` + byDue
	}
	rows, err := db.Query(`SELECT t.id, t.title, t.priority, COALESCE(t.due_at, ''), t.rrule, l.id, l.name`+
		from+` ORDER BY `+sorting+`, t.id DESC LIMIT ?`, pid, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var item widgetTodo
		if err := rows.Scan(&item.ID, &item.Title, &item.Priority, &item.DueAt, &item.RRule, &item.ListID, &item.ListName); err != nil {
			return nil, err
		}
		page.Items = append(page.Items, item)
	}
	return page, rows.Err()
}

func (a *App) handleWidgetOptions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	ctx := mustCtx(r)
	options, err := widgetOptions(ctx.AppDB(), projectScope(ctx))
	if err != nil {
		http.Error(w, "Unable to load lists and groups", http.StatusInternalServerError)
		return
	}
	writeJSON(w, options)
}

func (a *App) handleWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	ctx := mustCtx(r)
	query := r.URL.Query()
	limit, _ := strconv.Atoi(query.Get("limit"))
	order := query.Get("sort")
	if order == "" {
		order = "due"
	}
	page, err := loadWidget(ctx.AppDB(), projectScope(ctx), query.Get("target"), order, limit)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "This list or group is unavailable. Configure the widget to choose another.", http.StatusNotFound)
		} else if errors.Is(err, errWidgetSettings) {
			// Invalid settings fail closed rather than silently showing all work.
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "Unable to load Todo list", http.StatusInternalServerError)
		}
		return
	}
	writeJSON(w, page)
}
