package main

import (
	"database/sql"
	"errors"
	"os"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

func TestManifestPublishesTodoEvents(t *testing.T) {
	expected := []string{
		"todo.created",
		"todo.updated",
		"todo.completed",
		"todo.uncompleted",
		"todo.snoozed",
		"todo.deleted",
		"todo.list.created",
		"todo.list.updated",
		"todo.list.deleted",
		"todo.list_group.created",
		"todo.list_group.updated",
		"todo.list_group.deleted",
		"todo.tags.changed",
	}

	check := func(t *testing.T, name string, m sdk.Manifest) {
		t.Helper()
		got := map[string]bool{}
		for _, ev := range m.Provides.Publishes {
			got[ev.Name] = true
		}
		for _, topic := range expected {
			if !got[topic] {
				t.Fatalf("%s missing published event %q", name, topic)
			}
		}
	}

	check(t, "embedded manifest", (&App{}).Manifest())

	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	external, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	check(t, "apteva.yaml", *external)
}

func TestListTodosAllViewIsUnboundedByDefault(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-test"
	for i := 0; i < 250; i++ {
		if _, err := insertTodo(db, pid, &Todo{Title: "todo"}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := listTodos(db, pid, "all", nil, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 250 {
		t.Fatalf("all view returned %d todos, want 250", len(all))
	}

	limited, err := listTodos(db, pid, "all", nil, "", 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 50 {
		t.Fatalf("limited all view returned %d todos, want 50", len(limited))
	}
}

func TestListTodosHydratesTagsInBatches(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-batched-tags"
	for i := 0; i < 425; i++ {
		tags := []string{}
		if i == 0 {
			tags = []string{"alpha", "beta"}
		}
		if _, err := insertTodo(db, pid, &Todo{Title: "todo", Tags: tags}); err != nil {
			t.Fatal(err)
		}
	}

	all, err := listTodos(db, pid, "all", nil, "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 425 {
		t.Fatalf("all view returned %d todos, want 425", len(all))
	}
	for _, todo := range all {
		if todo.ID == 1 {
			if len(todo.Tags) != 2 || todo.Tags[0] != "alpha" || todo.Tags[1] != "beta" {
				t.Fatalf("tag hydration returned %#v", todo.Tags)
			}
			continue
		}
		if todo.Tags == nil {
			t.Fatalf("todo %d has nil tags; want an empty JSON array", todo.ID)
		}
	}
}

func TestTodayViewKeepsTodosSnoozedToLaterToday(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-snooze"
	const tzOffset = 120 // CEST

	nowS, dayEnd := dayBounds(time.Now(), tzOffset)
	now, err := time.Parse(time.RFC3339, nowS)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339, dayEnd)
	if err != nil {
		t.Fatal(err)
	}
	if !end.After(now.Add(2 * time.Hour)) {
		t.Skip("less than two hours left in the viewer's day")
	}
	laterToday := now.Add(time.Hour).Format(time.RFC3339)
	nextWeek := end.Add(7 * 24 * time.Hour).Format(time.RFC3339)

	// Snoozing rewrites due_at as well, which is how the panel's rows
	// arrive in practice.
	snoozedToday, err := insertTodo(db, pid, &Todo{Title: "snoozed to 11:00", DueAt: laterToday})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateTodoFields(db, pid, snoozedToday.ID, map[string]any{
		"snoozed_until": laterToday, "due_at": laterToday,
	}); err != nil {
		t.Fatal(err)
	}
	snoozedAway, err := insertTodo(db, pid, &Todo{Title: "snoozed to next week", DueAt: nextWeek})
	if err != nil {
		t.Fatal(err)
	}
	if err := updateTodoFields(db, pid, snoozedAway.ID, map[string]any{
		"snoozed_until": nextWeek, "due_at": nextWeek,
	}); err != nil {
		t.Fatal(err)
	}

	today, err := listTodos(db, pid, "today", nil, "", 0, tzOffset)
	if err != nil {
		t.Fatal(err)
	}
	if len(today) != 1 || today[0].ID != snoozedToday.ID {
		t.Fatalf("today view = %d rows (%v), want just the todo snoozed to later today", len(today), idsOf(today))
	}

	upcoming, err := listTodos(db, pid, "upcoming", nil, "", 0, tzOffset)
	if err != nil {
		t.Fatal(err)
	}
	if len(upcoming) != 1 || upcoming[0].ID != snoozedAway.ID {
		t.Fatalf("upcoming view = %d rows (%v), want the todo snoozed past today", len(upcoming), idsOf(upcoming))
	}

	// The bug this replaces: a snoozed todo counted in a pill but
	// present in no view at all.
	summary, err := summariseTodos(db, pid, tzOffset)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Today != len(today) || summary.Future != len(upcoming) || summary.Overdue != 0 {
		t.Fatalf("summary = %+v, want today=%d future=%d overdue=0", summary, len(today), len(upcoming))
	}
}

func TestTodayViewEndsAtTheViewersMidnight(t *testing.T) {
	db := openTestDB(t)
	const pid = "project-tz"

	// 23:00 UTC is tomorrow's small hours for a UTC+2 operator and
	// still this evening for everyone at or behind UTC.
	now := time.Date(2026, 8, 18, 10, 0, 0, 0, time.UTC)
	due := time.Date(2026, 8, 18, 23, 0, 0, 0, time.UTC).Format(time.RFC3339)
	if _, err := insertTodo(db, pid, &Todo{Title: "late-night", DueAt: due}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		tzOffset  int
		wantToday bool
	}{
		{"CEST — already tomorrow", 120, false},
		{"UTC", 0, true},
		{"US Pacific — still mid-afternoon", -420, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nowS, dayEnd := dayBounds(now, tc.tzOffset)
			clause, args, err := viewFilter("today", nowS, dayEnd)
			if err != nil {
				t.Fatal(err)
			}
			var n int
			q := `SELECT COUNT(*) FROM todos WHERE project_id = ?` + clause
			if err := db.QueryRow(q, append([]any{pid}, args...)...).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if got := n == 1; got != tc.wantToday {
				t.Fatalf("today contains the 23:00 UTC todo = %v, want %v (dayEnd %s)", got, tc.wantToday, dayEnd)
			}
		})
	}
}

func idsOf(todos []Todo) []int64 {
	out := []int64{}
	for _, t := range todos {
		out = append(out, t.ID)
	}
	return out
}

func TestInsertTodoRejectsListFromAnotherProject(t *testing.T) {
	db := openTestDB(t)
	list, err := insertList(db, "project-a", "Private", "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = insertTodo(db, "project-b", &Todo{Title: "wrong scope", ListID: &list.ID})
	if !errors.Is(err, errListNotFoundInScope) {
		t.Fatalf("insertTodo error = %v, want errListNotFoundInScope", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM todos WHERE project_id = ?`, "project-b").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("project-b has %d todos after rejected insert, want 0", count)
	}
}

func TestUpdateTodoFieldsCannotModifyAnotherProjectTags(t *testing.T) {
	db := openTestDB(t)
	todo, err := insertTodo(db, "project-a", &Todo{Title: "private", Tags: []string{"original"}})
	if err != nil {
		t.Fatal(err)
	}

	err = updateTodoFields(db, "project-b", todo.ID, map[string]any{"tags": []string{"leaked"}})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("updateTodoFields error = %v, want sql.ErrNoRows", err)
	}

	stored, err := getTodo(db, "project-a", todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Tags) != 1 || stored.Tags[0] != "original" {
		t.Fatalf("tags after rejected cross-project update = %#v", stored.Tags)
	}
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, path := range []string{
		"migrations/001_init.sql",
		"migrations/002_rename_projects_to_lists.sql",
		"migrations/003_list_groups.sql",
	} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	return db
}
