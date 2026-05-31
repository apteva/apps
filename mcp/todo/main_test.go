package main

import (
	"database/sql"
	"os"
	"testing"

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

	all, err := listTodos(db, pid, "all", nil, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 250 {
		t.Fatalf("all view returned %d todos, want 250", len(all))
	}

	limited, err := listTodos(db, pid, "all", nil, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 50 {
		t.Fatalf("limited all view returned %d todos, want 50", len(limited))
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
