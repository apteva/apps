package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestContactsGetReturnsCompleteCurrentStateAndDeclaresExcludedHistory(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	if _, err := app.toolDefineAttribute(ctx, map[string]any{
		"key": "industry", "label": "Industry", "type": "text",
	}); err != nil {
		t.Fatal(err)
	}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice Example",
		"tags":         []any{"lead", "priority"},
		"attributes": []any{
			map[string]any{"key": "industry", "value": "Consulting"},
		},
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true},
		},
	})
	listID := mkList(t, ctx, "Prospects", "prospects")
	if _, err := app.toolListsAddContact(ctx, map[string]any{
		"list_id": listID, "contact_id": contact.ID, "source": "test",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := app.toolGet(ctx, map[string]any{"id": contact.ID})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	got := result["contact"].(*Contact)
	if len(got.Channels) != 1 || got.Channels[0].Value != "alice@example.test" {
		t.Fatalf("channels=%+v", got.Channels)
	}
	if !containsString(got.Tags, "lead") || !containsString(got.Tags, "priority") {
		t.Fatalf("tags=%v", got.Tags)
	}
	if len(got.Attributes) != 1 || got.Attributes[0].Key != "industry" || got.Attributes[0].Value != "Consulting" {
		t.Fatalf("attributes=%+v", got.Attributes)
	}
	lists := result["lists"].([]*List)
	if len(lists) != 1 || lists[0].ID != listID {
		t.Fatalf("lists=%+v", lists)
	}
	for _, included := range []string{"contact", "channels", "tags", "attributes", "lists"} {
		if !containsString(result["included"].([]string), included) {
			t.Errorf("included missing %q: %v", included, result["included"])
		}
	}
	for _, excluded := range []string{"activities", "conversations", "opportunities"} {
		if !containsString(result["excluded"].([]string), excluded) {
			t.Errorf("excluded missing %q: %v", excluded, result["excluded"])
		}
		if _, exists := result[excluded]; exists {
			t.Errorf("contacts_get unexpectedly returned excluded resource %q", excluded)
		}
	}
}

func TestContactsGetContextReportsCollectionTruncation(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{"display_name": "Alice Example"})
	base := time.Date(2026, 8, 16, 10, 0, 0, 0, time.UTC)

	for i := range 3 {
		if _, err := app.toolLogActivity(ctx, map[string]any{
			"contact_id":  contact.ID,
			"kind":        "note",
			"body":        fmt.Sprintf("Note %d", i+1),
			"occurred_at": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339),
			"source":      "test",
		}); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		when := base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339)
		if _, err := dbConversationCreate(
			tx, "test-proj", contact.ID, channelEmail,
			fmt.Sprintf("Thread %d", i+1), fmt.Sprintf("message-%d@example.test", i+1), when,
		); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if _, err := app.toolPipelinesList(ctx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if _, err := app.toolOpportunitiesCreate(ctx, map[string]any{
			"contact_id": contact.ID,
			"title":      fmt.Sprintf("Opportunity %d", i+1),
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := app.toolGetContext(ctx, map[string]any{
		"id":                 contact.ID,
		"activity_limit":     1,
		"conversation_limit": 1,
		"opportunity_limit":  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if len(result["activities"].([]*Activity)) != 1 ||
		len(result["conversations"].([]*Conversation)) != 1 ||
		len(result["opportunities"].([]*Opportunity)) != 1 {
		t.Fatalf("bounded collections were not limited: %+v", result)
	}
	if len(result["excluded"].([]string)) != 0 {
		t.Fatalf("context excluded=%v, want empty", result["excluded"])
	}

	info := result["collection_info"].(map[string]any)
	for _, resource := range []string{"activities", "conversations", "opportunities"} {
		item := info[resource].(map[string]any)
		if item["returned"] != 1 || item["total"] != 3 || item["limit"] != 1 || item["truncated"] != true {
			t.Errorf("%s info=%v, want returned=1 total=3 limit=1 truncated=true", resource, item)
		}
	}
}

func TestContactsGetContextLimitsAreBounded(t *testing.T) {
	args := map[string]any{"activity_limit": -1, "conversation_limit": 500, "opportunity_limit": 0}
	if got := boundedContactReadLimit(args, "activity_limit", 10); got != 10 {
		t.Fatalf("negative limit=%d, want default 10", got)
	}
	if got := boundedContactReadLimit(args, "conversation_limit", 10); got != 200 {
		t.Fatalf("large limit=%d, want max 200", got)
	}
	if got := boundedContactReadLimit(args, "opportunity_limit", 20); got != 20 {
		t.Fatalf("zero limit=%d, want default 20", got)
	}
}

func TestContactReadToolContractIsExplicit(t *testing.T) {
	tools := map[string]struct {
		description string
		schema      map[string]any
	}{}
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == "contacts_get" || tool.Name == "contacts_get_context" {
			tools[tool.Name] = struct {
				description string
				schema      map[string]any
			}{tool.Description, tool.InputSchema}
		}
	}
	get, ok := tools["contacts_get"]
	if !ok || !strings.Contains(get.description, "AUTHORITATIVE CURRENT-STATE READ") ||
		!strings.Contains(get.description, "deliberately excludes") ||
		!strings.Contains(get.description, "contacts_get_context") {
		t.Fatalf("contacts_get description is ambiguous: %q", get.description)
	}
	context, ok := tools["contacts_get_context"]
	if !ok || !strings.Contains(context.description, "AUTHORITATIVE PRE-FLIGHT READ") ||
		!strings.Contains(context.description, "collection_info") ||
		!strings.Contains(context.description, "truncated") {
		t.Fatalf("contacts_get_context description is ambiguous: %q", context.description)
	}
	properties, ok := context.schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("contacts_get_context schema has no properties: %#v", context.schema)
	}
	for _, key := range []string{"activity_limit", "conversation_limit", "opportunity_limit"} {
		property, ok := properties[key].(map[string]any)
		if !ok || intArg(property, "minimum", 0) != 1 || intArg(property, "maximum", 0) != 200 {
			t.Errorf("%s schema=%#v, want minimum=1 maximum=200", key, property)
		}
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
