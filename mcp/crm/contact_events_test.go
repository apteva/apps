package main

import (
	"reflect"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func contactAddedEvent(t *testing.T, rec *tk.EmitRecorder) (string, map[string]any) {
	t.Helper()
	events := rec.EventsByTopic("contact.added")
	if len(events) != 1 {
		t.Fatalf("contact.added events=%d, want 1", len(events))
	}
	payload, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("contact.added payload type=%T, want map[string]any", events[0].Data)
	}
	if raw, ok := payload["list_ids"].([]any); ok {
		ids := make([]int64, 0, len(raw))
		for _, v := range raw {
			ids = append(ids, int64FromAny(v))
		}
		payload["list_ids"] = ids
	}
	return events[0].ProjectID, payload
}

func TestContactAddedIncludesActiveListIDs(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	first := mkList(t, ctx, "First", "first")
	second := mkList(t, ctx, "Second", "second")

	if _, err := app.toolCreate(ctx, map[string]any{
		"first_name": "Ada",
		"last_name":  "Lovelace",
		"list_ids":   []any{float64(first), float64(second)},
	}); err != nil {
		t.Fatal(err)
	}

	projectID, payload := contactAddedEvent(t, rec)
	if projectID != "test-proj" {
		t.Fatalf("project_id=%q, want test-proj", projectID)
	}
	if payload["first_name"] != "Ada" || payload["last_name"] != "Lovelace" {
		t.Fatalf("contact fields missing from payload: %#v", payload)
	}
	if got, ok := payload["list_ids"].([]int64); !ok || !reflect.DeepEqual(got, []int64{first, second}) {
		t.Fatalf("list_ids=%#v (%T), want [%d %d]", payload["list_ids"], payload["list_ids"], first, second)
	}
}

func TestContactAddedWaitsForUpsertListAssignment(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	listID := mkList(t, ctx, "Leads", "leads")

	if _, err := app.toolUpsertByChannel(ctx, map[string]any{
		"kind": "email", "value": "new@example.test", "list_id": "leads",
	}); err != nil {
		t.Fatal(err)
	}

	_, payload := contactAddedEvent(t, rec)
	if got, ok := payload["list_ids"].([]int64); !ok || !reflect.DeepEqual(got, []int64{listID}) {
		t.Fatalf("list_ids=%#v (%T), want [%d]", payload["list_ids"], payload["list_ids"], listID)
	}
}

func TestInboundContactAddedIncludesRoutedListIDs(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	out, err := app.toolListsCreate(ctx, map[string]any{
		"name": "Inbound leads", "slug": "inbound-leads", "inbound_route_pattern": "sales-*",
	})
	if err != nil {
		t.Fatal(err)
	}
	listID := out.(map[string]any)["list"].(*List).ID

	if _, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID:       77001,
		Channel:         channelEmail,
		From:            "prospect@example.test",
		To:              []string{"sales@example.test"},
		MatchedPattern:  "sales-*",
		MessageIDHeader: "<crm-contact-added-list-ids@example.test>",
		BodyText:        "Hello",
	}); err != nil {
		t.Fatal(err)
	}

	projectID, payload := contactAddedEvent(t, rec)
	if projectID != "test-proj" {
		t.Fatalf("project_id=%q, want test-proj", projectID)
	}
	if got, ok := payload["list_ids"].([]int64); !ok || !reflect.DeepEqual(got, []int64{listID}) {
		t.Fatalf("list_ids=%#v (%T), want [%d]", payload["list_ids"], payload["list_ids"], listID)
	}
}

func TestContactAddedWithoutListsUsesEmptyArray(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))

	if _, err := (&App{}).toolCreate(ctx, map[string]any{"display_name": "No List"}); err != nil {
		t.Fatal(err)
	}

	_, payload := contactAddedEvent(t, rec)
	got, ok := payload["list_ids"].([]int64)
	if !ok || got == nil || len(got) != 0 {
		t.Fatalf("list_ids=%#v (%T), want non-nil empty []int64", payload["list_ids"], payload["list_ids"])
	}
}
