package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func eventPayload(t *testing.T, rec *tk.EmitRecorder, topic string) map[string]any {
	t.Helper()
	events := rec.EventsByTopic(topic)
	if len(events) != 1 {
		t.Fatalf("%s events=%d, want 1; topics=%v", topic, len(events), emittedTopics(rec))
	}
	payload, ok := events[0].Data.(map[string]any)
	if !ok {
		t.Fatalf("%s payload type=%T, want map[string]any", topic, events[0].Data)
	}
	return payload
}

func requireListIDs(t *testing.T, payload map[string]any, field string, want []int64) {
	t.Helper()
	raw, ok := payload[field]
	if !ok {
		t.Fatalf("payload missing %s: %#v", field, payload)
	}
	got := []int64{}
	switch values := raw.(type) {
	case []int64:
		got = append(got, values...)
	case []any:
		for _, value := range values {
			got = append(got, int64FromAny(value))
		}
	default:
		t.Fatalf("%s=%#v (%T), want an integer array", field, raw, raw)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s=%v, want %v", field, got, want)
	}
}

func createContactOnLists(t *testing.T, ctx *sdk.AppCtx, listIDs ...int64) *Contact {
	t.Helper()
	refs := make([]any, len(listIDs))
	for i, id := range listIDs {
		refs[i] = float64(id)
	}
	out, err := (&App{}).toolCreate(ctx, map[string]any{
		"display_name": "List Context",
		"list_ids":     refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)["contact"].(*Contact)
}

func TestContactScopedEventsCarryActiveListSnapshot(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	first := mkList(t, ctx, "First", "first")
	second := mkList(t, ctx, "Second", "second")
	contact := createContactOnLists(t, ctx, second, first)
	rec.Reset()

	topics := []string{
		"contact.updated",
		"contact.channel.deliverability.changed",
		"contact.activity.added",
		"conversation.status.changed",
		"conversation.message.received",
		"opportunity.created",
	}
	for _, topic := range topics {
		payload := map[string]any{"contact_id": contact.ID}
		if topic == "contact.updated" {
			payload = map[string]any{"id": contact.ID}
		}
		emitCRMEvent(ctx, "test-proj", topic, payload)
	}

	for _, topic := range topics {
		payload := eventPayload(t, rec, topic)
		requireListIDs(t, payload, "list_ids", []int64{first, second})
		if eventNeedsAttribution(topic) {
			requireListIDs(t, payload, "attributed_list_ids", []int64{})
		}
	}
}

func TestListSnapshotExcludesArchivedLists(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	active := mkList(t, ctx, "Active", "active")
	archived := mkList(t, ctx, "Archived", "archived")
	contact := createContactOnLists(t, ctx, active, archived)
	if err := dbListArchive(ctx.AppDB(), "test-proj", archived); err != nil {
		t.Fatal(err)
	}
	rec.Reset()

	emitCRMEvent(ctx, "test-proj", "contact.updated", map[string]any{"id": contact.ID})
	requireListIDs(t, eventPayload(t, rec, "contact.updated"), "list_ids", []int64{active})
}

func TestActivityEventCarriesOutboundListAttribution(t *testing.T) {
	rec := tk.NewEmitRecorder()
	platform := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithEmitter(rec), tk.WithPlatform(platform))
	listID := mkList(t, ctx, "Campaign", "campaign")
	out, err := (&App{}).toolCreate(ctx, map[string]any{
		"display_name": "Outbound",
		"channels": []any{
			map[string]any{"kind": "phone", "value": "+12025550123", "is_primary": true},
		},
		"list_id": listID,
	})
	if err != nil {
		t.Fatal(err)
	}
	contact := out.(map[string]any)["contact"].(*Contact)
	rec.Reset()

	if _, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"id":      contact.ID,
		"channel": channelSMS,
		"body":    "Hello",
		"from":    "+12025550100",
		"list_id": listID,
	}); err != nil {
		t.Fatal(err)
	}

	payload := eventPayload(t, rec, "contact.activity.added")
	requireListIDs(t, payload, "list_ids", []int64{listID})
	requireListIDs(t, payload, "attributed_list_ids", []int64{listID})
}

func TestInboundActivityAndMessageCarryRoutingAttribution(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	out, err := app.toolListsCreate(ctx, map[string]any{
		"name": "Inbound", "slug": "inbound", "inbound_route_pattern": "sales-*",
	})
	if err != nil {
		t.Fatal(err)
	}
	listID := out.(map[string]any)["list"].(*List).ID
	rec.Reset()

	if _, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID:       88001,
		Channel:         channelEmail,
		From:            "lead@example.test",
		To:              []string{"sales@example.test"},
		MatchedPattern:  "sales-*",
		MessageIDHeader: "<list-context@example.test>",
		BodyText:        "Interested",
	}); err != nil {
		t.Fatal(err)
	}

	for _, topic := range []string{"contact.activity.added", "conversation.message.received"} {
		payload := eventPayload(t, rec, topic)
		requireListIDs(t, payload, "list_ids", []int64{listID})
		requireListIDs(t, payload, "attributed_list_ids", []int64{listID})
	}
}

func TestMergeAndDeleteEventsPreserveListSnapshots(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	app := &App{}
	winnerList := mkList(t, ctx, "Winner", "winner")
	loserList := mkList(t, ctx, "Loser", "loser")
	winner := createContactOnLists(t, ctx, winnerList)
	loser := createContactOnLists(t, ctx, loserList)
	rec.Reset()

	if _, err := app.toolMerge(ctx, map[string]any{"winner_id": winner.ID, "loser_id": loser.ID}); err != nil {
		t.Fatal(err)
	}
	merged := eventPayload(t, rec, "contact.merged")
	requireListIDs(t, merged, "winner_list_ids", []int64{winnerList, loserList})
	requireListIDs(t, merged, "loser_list_ids_before_merge", []int64{loserList})

	rec.Reset()
	globalCtx = ctx
	w := httptest.NewRecorder()
	app.handleHTTPArchive(w, httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/contacts/%d", winner.ID), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", w.Code, w.Body.String())
	}
	deleted := eventPayload(t, rec, "contact.deleted")
	requireListIDs(t, deleted, "list_ids_before_delete", []int64{winnerList, loserList})
}

func TestSegmentEventsCarryOptionalListScope(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	listID := mkList(t, ctx, "Audience", "audience")
	rec.Reset()

	if _, err := (&App{}).toolSegmentsCreate(ctx, map[string]any{
		"name": "Scoped", "kind": "dynamic", "list_id": listID, "definition": []any{},
	}); err != nil {
		t.Fatal(err)
	}
	payload := eventPayload(t, rec, "segment.created")
	if got := int64FromAny(payload["list_id"]); got != listID {
		t.Fatalf("list_id=%d, want %d", got, listID)
	}
}

func TestOpportunityEventCarriesContactListSnapshot(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, tk.WithEmitter(rec))
	listID := mkList(t, ctx, "Pipeline audience", "pipeline-audience")
	contact := createContactOnLists(t, ctx, listID)
	rec.Reset()

	if _, err := (&App{}).toolOpportunitiesCreate(ctx, map[string]any{
		"contact_id": contact.ID,
		"title":      "List-aware deal",
	}); err != nil {
		t.Fatal(err)
	}
	requireListIDs(t, eventPayload(t, rec, "opportunity.created"), "list_ids", []int64{listID})
}

func TestManifestDeclaresListContextFields(t *testing.T) {
	wants := map[string][]string{
		"contact.added":                          {"list_ids"},
		"contact.updated":                        {"list_ids"},
		"contact.channel.deliverability.changed": {"list_ids"},
		"contact.deleted":                        {"list_ids_before_delete"},
		"contact.merged":                         {"winner_list_ids", "loser_list_ids_before_merge"},
		"contact.activity.added":                 {"list_ids", "attributed_list_ids"},
		"conversation.status.changed":            {"list_ids"},
		"conversation.message.received":          {"list_ids", "attributed_list_ids"},
		"segment.created":                        {"list_id"},
		"segment.updated":                        {"list_id"},
		"segment.archived":                       {"list_id"},
		"segment.materialised":                   {"list_id"},
		"opportunity.created":                    {"list_ids"},
		"opportunity.updated":                    {"list_ids"},
		"opportunity.stage.changed":              {"list_ids"},
		"opportunity.status.changed":             {"list_ids"},
		"opportunity.won":                        {"list_ids"},
		"opportunity.lost":                       {"list_ids"},
	}
	declared := map[string]map[string]string{}
	for _, event := range (&App{}).Manifest().Provides.Publishes {
		declared[event.Name] = event.Payload
	}
	for topic, fields := range wants {
		for _, field := range fields {
			if _, ok := declared[topic][field]; !ok {
				t.Errorf("manifest %s missing %s", topic, field)
			}
		}
	}
}
