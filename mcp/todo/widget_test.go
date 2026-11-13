package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestWidgetManifest(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	external, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	embedded := (&App{}).Manifest()
	if !reflect.DeepEqual(embedded.Provides.UIComponents, external.Provides.UIComponents) {
		t.Fatal("embedded and installation widget manifests differ")
	}
	widgets := embedded.Provides.UIComponents
	if len(widgets) != 1 || widgets[0].Name != "todo-list" || widgets[0].SettingsUI == nil {
		t.Fatalf("widget registration: %#v", widgets)
	}
	if !reflect.DeepEqual(widgets[0].SupportedSizes, []string{"half", "full"}) {
		t.Fatal("widget must support half and full widths")
	}
}

func TestWidgetSelectionSortingAndCounts(t *testing.T) {
	db := openTestDB(t)
	const pid = "widget-project"
	g, err := insertListGroup(db, pid, "Personal", "")
	if err != nil {
		t.Fatal(err)
	}
	l, err := insertList(db, pid, "Website launch", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := insertList(db, pid, "Home", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE lists SET group_id = ? WHERE project_id = ?`, g.ID, pid); err != nil {
		t.Fatal(err)
	}
	add := func(title string, listID *int64, due string, priority int, status string) *Todo {
		t.Helper()
		row, err := insertTodo(db, pid, &Todo{Title: title, ListID: listID, DueAt: due, Priority: priority, Status: status})
		if err != nil {
			t.Fatal(err)
		}
		if status != "open" {
			if err := updateTodoFields(db, pid, row.ID, map[string]any{"status": status}); err != nil {
				t.Fatal(err)
			}
		}
		return row
	}
	add("Urgent later", &l.ID, "2026-12-20", 1, "open")
	early := add("Due first", &l.ID, "2026-11-01", 3, "open")
	add("No date", &l.ID, "", 2, "open")
	add("Already done", &l.ID, "2026-10-01", 1, "done")
	add("Cancelled", &l.ID, "2026-10-01", 1, "cancelled")
	add("Other list", &other.ID, "2026-12-01", 2, "open")
	add("Inbox", nil, "2026-10-01", 1, "open")

	listTarget := fmt.Sprintf("list:%d", l.ID)
	page, err := loadAllWidget(db, pid, listTarget, "due", 1)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != l.Name || page.Total != 3 || len(page.Items) != 1 || page.Items[0].ID != early.ID {
		t.Fatalf("due ordering/count before limit: %#v", page)
	}
	page, err = loadAllWidget(db, pid, listTarget, "priority", 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].Title != "Urgent later" || page.Items[1].Title != "No date" {
		t.Fatalf("priority ordering: %#v, %v", page, err)
	}
	page, err = loadAllWidget(db, pid, fmt.Sprintf("group:%d", g.ID), "due", 20)
	if err != nil || page.Total != 4 || len(page.Lists) != 2 {
		t.Fatalf("group selection: %#v, %v", page, err)
	}
	for _, item := range page.Items {
		if item.ListName == "" {
			t.Fatal("group rows need their list names")
		}
	}
	if _, err := completeTodo(db, pid, early.ID); err != nil {
		t.Fatal(err)
	}
	page, err = loadAllWidget(db, pid, listTarget, "due", 20)
	if err != nil || page.Total != 2 || page.Items[1].Title != "No date" {
		t.Fatalf("completion/undated ordering: %#v, %v", page, err)
	}
	if _, err := loadAllWidget(db, "another-project", listTarget, "due", 8); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-project selection must fail: %v", err)
	}
	if _, err := db.Exec(`UPDATE lists SET archived = 1 WHERE id = ?`, l.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllWidget(db, pid, listTarget, "due", 8); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("archived list selection must fail: %v", err)
	}
	page, err = loadAllWidget(db, pid, fmt.Sprintf("group:%d", g.ID), "due", 8)
	if err != nil || page.Total != 1 || len(page.Lists) != 1 {
		t.Fatalf("exclude archived lists in groups: %#v, %v", page, err)
	}
	if _, err := db.Exec(`UPDATE list_groups SET archived = 1 WHERE id = ?`, g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := loadAllWidget(db, pid, fmt.Sprintf("list:%d", other.ID), "due", 8); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("list in archived group must be unavailable: %v", err)
	}
	options, err := widgetOptions(db, pid)
	if err != nil || len(options) != 1 {
		t.Fatalf("archived targets must not be offered: %#v, %v", options, err)
	}
}

func TestWidgetDiscoveryAndLimits(t *testing.T) {
	db := openTestDB(t)
	l, err := insertList(db, "a", "Launch", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertListGroup(db, "a", "Personal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := insertList(db, "b", "Private", ""); err != nil {
		t.Fatal(err)
	}
	options, err := widgetOptions(db, "a")
	if err != nil || len(options) != 3 || options[1].Label != "Group · Personal" || options[2].Label != "List · Launch" {
		t.Fatalf("scoped discovery: %#v, %v", options, err)
	}
	for i := 0; i < 25; i++ {
		if _, err := insertTodo(db, "a", &Todo{Title: "Task", ListID: &l.ID}); err != nil {
			t.Fatal(err)
		}
	}
	target := fmt.Sprintf("list:%d", l.ID)
	page, err := loadAllWidget(db, "a", target, "due", 999)
	if err != nil || len(page.Items) != 20 || page.Total != 25 {
		t.Fatalf("bounded response with full count: %#v, %v", page, err)
	}
	for _, target := range []string{"list:0", "list:-1", "group:nope", "all:1", "list:99999999999999999999999"} {
		if _, err := loadAllWidget(db, "a", target, "due", 8); err == nil {
			t.Fatalf("invalid target %q should fail", target)
		}
	}
	if _, err := loadAllWidget(db, "a", target, "title; DROP TABLE todos", 8); err == nil {
		t.Fatal("invalid ordering must fail")
	}
}

func TestWidgetHTTP(t *testing.T) {
	db := openTestDB(t)
	l, err := insertList(db, "project-a", "Launch", "")
	if err != nil {
		t.Fatal(err)
	}
	previous := globalCtx
	t.Cleanup(func() { globalCtx = previous })
	globalCtx = sdk.NewAppCtxForTest(nil, db, nil, nil, nil).WithProject("project-a")
	app := &App{}
	for _, test := range []struct {
		method, path string
		status       int
		handler      http.HandlerFunc
	}{
		{"GET", "/widget?target=list:" + fmt.Sprint(l.ID), 200, app.handleWidget},
		{"GET", "/widget?target=list:999", 404, app.handleWidget},
		{"GET", "/widget?target=all", 200, app.handleWidget},
		{"GET", "/widget?target=list:1&sort=invalid", 400, app.handleWidget},
		{"POST", "/widget?target=list:1", 405, app.handleWidget},
		{"GET", "/widget/options", 200, app.handleWidgetOptions},
		{"POST", "/widget/options", 405, app.handleWidgetOptions},
	} {
		response := httptest.NewRecorder()
		test.handler(response, httptest.NewRequest(test.method, test.path, nil))
		if response.Code != test.status {
			t.Errorf("%s %s = %d, want %d: %s", test.method, test.path, response.Code, test.status, response.Body)
		}
	}
	globalCtx = globalCtx.WithProject("project-b")
	response := httptest.NewRecorder()
	app.handleWidget(response, httptest.NewRequest("GET", fmt.Sprintf("/widget?target=list:%d&project_id=project-a", l.ID), nil))
	if response.Code != 404 {
		t.Fatal("query parameters must not override the SDK project scope")
	}
}

func loadAllWidget(db *sql.DB, pid, target, order string, limit int) (*widgetPage, error) {
	return loadWidget(db, pid, target, order, limit, "all", 0, time.Now())
}

func TestWidgetDefaultTodayAcrossAllAreas(t *testing.T) {
	db := openTestDB(t)
	const pid = "today-widget"
	l, err := insertList(db, pid, "Work", "")
	if err != nil {
		t.Fatal(err)
	}
	g, err := insertListGroup(db, pid, "Personal", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := insertList(db, pid, "Home", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE lists SET group_id = ? WHERE id = ?`, g.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	// UTC is still Nov 13; the viewer at UTC+2 is already on Nov 14.
	now := time.Date(2026, 11, 13, 23, 0, 0, 0, time.UTC)
	add := func(title, due string, list *int64, snooze string) {
		t.Helper()
		row, err := insertTodo(db, pid, &Todo{Title: title, DueAt: due, ListID: list})
		if err != nil {
			t.Fatal(err)
		}
		if snooze != "" {
			if err := updateTodoFields(db, pid, row.ID, map[string]any{"snoozed_until": snooze}); err != nil {
				t.Fatal(err)
			}
		}
	}
	add("Overdue work", "2026-11-12T10:00:00Z", &l.ID, "")
	add("Today in group", "2026-11-14T18:00:00Z", &other.ID, "")
	add("Today Inbox", "2026-11-14T21:59:59Z", nil, "")
	add("Tomorrow Inbox", "2026-11-14T22:00:00Z", nil, "")
	add("No date", "", &l.ID, "")
	add("Snoozed tomorrow", "2026-11-12T10:00:00Z", &l.ID, "2026-11-15T10:00:00Z")
	add("Snoozed later today", "2026-11-12T10:00:00Z", &l.ID, "2026-11-14T16:00:00Z")
	page, err := loadWidget(db, pid, "", "due", 20, "", 120, now)
	if err != nil {
		t.Fatal(err)
	}
	if page.Title != "Todo" || page.Total != 4 || len(page.Items) != 4 || len(page.Options) != 4 {
		t.Fatalf("default Today + overdue across areas: %#v", page)
	}
	for _, item := range page.Items {
		if item.Title == "Today Inbox" && (item.ListID != 0 || item.ListName != "Inbox") {
			t.Fatal("Inbox task missing")
		}
	}
	late, err := loadWidget(db, pid, "all", "due", 20, "overdue", 120, now)
	if err != nil || late.Total != 1 || late.Items[0].Title != "Overdue work" {
		t.Fatalf("overdue: %#v %v", late, err)
	}
	future, err := loadWidget(db, pid, "all", "due", 20, "upcoming", 120, now)
	if err != nil || future.Total != 2 {
		t.Fatalf("upcoming: %#v %v", future, err)
	}
	all, err := loadWidget(db, pid, "all", "due", 20, "all", 120, now)
	if err != nil || all.Total != 7 {
		t.Fatalf("all open includes undated: %#v %v", all, err)
	}
	onlyGroup, err := loadWidget(db, pid, fmt.Sprintf("group:%d", g.ID), "due", 20, "today", 120, now)
	if err != nil || onlyGroup.Total != 1 || onlyGroup.Items[0].Title != "Today in group" {
		t.Fatalf("group filter: %#v %v", onlyGroup, err)
	}
	if _, err := loadWidget(db, pid, "all", "due", 20, "done", 120, now); !errors.Is(err, errWidgetSettings) {
		t.Fatalf("invalid view: %v", err)
	}
	if _, err := db.Exec(`UPDATE list_groups SET archived = 1 WHERE id = ?`, g.ID); err != nil {
		t.Fatal(err)
	}
	page, err = loadWidget(db, pid, "all", "due", 20, "today", 120, now)
	if err != nil || page.Total != 3 {
		t.Fatalf("archived groups excluded: %#v %v", page, err)
	}
}
