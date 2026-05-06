package main

import "testing"

// One test per mutation — paranoid coverage so a future "I'll just
// rearrange this handler" change can't silently drop the event the
// dashboard panel subscribes to.

func TestEmit_TableCreated(t *testing.T) {
	ctx, rec := newTestCtxWithRecorder(t)
	app := &App{}
	booksTable(t, app, ctx)
	if got := rec.EventsByTopic(topicTableCreated); len(got) != 1 {
		t.Fatalf("expected 1 %s event, got %d", topicTableCreated, len(got))
	}
}

func TestEmit_RowInsertedAndUpdatedAndDeleted(t *testing.T) {
	ctx, rec := newTestCtxWithRecorder(t)
	app := &App{}
	booksTable(t, app, ctx)
	rec.Reset() // ignore the table.created event from the seed

	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "X"}, map[string]any{"title": "Y"}},
	})
	if got := rec.EventsByTopic(topicRowInserted); len(got) != 1 {
		t.Fatalf("rows_insert: expected 1 %s event, got %d", topicRowInserted, len(got))
	}
	insertEv := rec.EventsByTopic(topicRowInserted)[0]
	data := insertEv.Data.(map[string]any)
	if data["count"].(int) != 2 || data["table"] != "books" {
		t.Errorf("row.inserted payload: %+v", data)
	}

	mustCall(t, app, ctx, "rows_update", map[string]any{
		"table":  "books",
		"id":     int64(1),
		"fields": map[string]any{"title": "X-renamed"},
	})
	if got := rec.EventsByTopic(topicRowUpdated); len(got) != 1 {
		t.Errorf("rows_update: expected 1 %s event, got %d", topicRowUpdated, len(got))
	}

	mustCall(t, app, ctx, "rows_delete", map[string]any{"table": "books", "id": int64(2)})
	if got := rec.EventsByTopic(topicRowDeleted); len(got) != 1 {
		t.Errorf("rows_delete: expected 1 %s event, got %d", topicRowDeleted, len(got))
	}
}

func TestEmit_RowDeleteByFilter(t *testing.T) {
	ctx, rec := newTestCtxWithRecorder(t)
	app := &App{}
	booksTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "A", "finished": true}, map[string]any{"title": "B", "finished": false}},
	})
	rec.Reset()

	mustCall(t, app, ctx, "rows_delete", map[string]any{
		"table":   "books",
		"where":   []any{map[string]any{"col": "finished", "op": "eq", "value": true}},
		"confirm": true,
	})
	got := rec.EventsByTopic(topicRowDeleted)
	if len(got) != 1 {
		t.Fatalf("filter delete: expected 1 %s event, got %d", topicRowDeleted, len(got))
	}
	if got[0].Data.(map[string]any)["deleted"].(int64) != 1 {
		t.Errorf("filter delete payload: %+v", got[0].Data)
	}
}

func TestEmit_TableAlteredAndDropped(t *testing.T) {
	ctx, rec := newTestCtxWithRecorder(t)
	app := &App{}
	booksTable(t, app, ctx)
	rec.Reset()

	mustCall(t, app, ctx, "tables_alter", map[string]any{
		"name": "books",
		"add":  map[string]any{"name": "isbn", "type": "text"},
	})
	if got := rec.EventsByTopic(topicTableAltered); len(got) != 1 || got[0].Data.(map[string]any)["change"] != "add" {
		t.Errorf("table.altered (add): %+v", rec.EventsByTopic(topicTableAltered))
	}

	mustCall(t, app, ctx, "tables_drop", map[string]any{"name": "books", "confirm": true})
	if got := rec.EventsByTopic(topicTableDropped); len(got) != 1 {
		t.Errorf("expected 1 %s event, got %d", topicTableDropped, len(got))
	}
}

func TestEmit_NotFiredOnFailedMutation(t *testing.T) {
	ctx, rec := newTestCtxWithRecorder(t)
	app := &App{}
	booksTable(t, app, ctx)
	rec.Reset()

	// title is required — this batch fails the second row, so the
	// transaction rolls back and no row.inserted event should fire.
	_, err := callTool(app, ctx, "rows_insert", map[string]any{
		"table": "books",
		"rows":  []any{map[string]any{"title": "ok"}, map[string]any{"author": "no title"}},
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if got := rec.EventsByTopic(topicRowInserted); len(got) != 0 {
		t.Errorf("expected 0 row.inserted events on failure, got %d", len(got))
	}
}
