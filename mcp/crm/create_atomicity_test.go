package main

import (
	"database/sql"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func countRowsForProject(t *testing.T, db *sql.DB, table, projectID string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM "+table+" WHERE project_id = ?", projectID,
	).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestCreate_InvalidAttributeLeavesNoPartialRows(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	listID := mkList(t, ctx, "Leads", "leads")

	_, err := app.toolCreate(ctx, map[string]any{
		"display_name": "Incomplete",
		"channels": []any{
			map[string]any{"kind": "email", "value": "incomplete@example.test"},
		},
		"tags":    []any{"prospect"},
		"list_id": listID,
		"attributes": []any{
			map[string]any{"key": "undefined_field", "value": "value"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "not defined") {
		t.Fatalf("error=%v, want undefined attribute validation error", err)
	}

	for _, table := range []string{
		"contacts", "contact_channels", "contact_tags", "contact_attributes", "contact_list_members",
	} {
		if got := countRowsForProject(t, ctx.AppDB(), table, "test-proj"); got != 0 {
			t.Fatalf("%s rows=%d, want 0 after rejected create", table, got)
		}
	}
}

func TestCreate_MidTransactionFailureRollsBackAllContactRows(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	listID := mkList(t, ctx, "Leads", "leads")
	if _, err := dbDefineAttribute(
		ctx.AppDB(), "test-proj", "industry", "Industry", "text", nil, false, 0,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`
		CREATE TRIGGER fail_forced_contact_attribute
		BEFORE INSERT ON contact_attributes
		WHEN NEW.source = 'force-error'
		BEGIN
			SELECT RAISE(ABORT, 'forced attribute insert failure');
		END`); err != nil {
		t.Fatal(err)
	}

	_, err := app.toolCreate(ctx, map[string]any{
		"display_name": "Rollback",
		"channels": []any{
			map[string]any{"kind": "email", "value": "rollback@example.test"},
		},
		"tags":    []any{"prospect"},
		"list_id": listID,
		"attributes": []any{
			map[string]any{"key": "industry", "value": "software", "source": "force-error"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "forced attribute insert failure") {
		t.Fatalf("error=%v, want forced transaction failure", err)
	}

	for _, table := range []string{
		"contacts", "contact_channels", "contact_tags", "contact_attributes", "contact_list_members",
	} {
		if got := countRowsForProject(t, ctx.AppDB(), table, "test-proj"); got != 0 {
			t.Fatalf("%s rows=%d, want 0 after rollback", table, got)
		}
	}
}

func TestCreate_CommitsContactAndRelationsTogether(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	listID := mkList(t, ctx, "Leads", "leads")
	if _, err := dbDefineAttribute(
		ctx.AppDB(), "test-proj", "industry", "Industry", "text", nil, false, 0,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := app.toolCreate(ctx, map[string]any{
		"display_name": "Complete",
		"channels": []any{
			map[string]any{"kind": "email", "value": "complete@example.test"},
		},
		"tags":    []any{"prospect"},
		"list_id": listID,
		"attributes": []any{
			map[string]any{"key": "industry", "value": "software"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{
		"contacts", "contact_channels", "contact_tags", "contact_attributes", "contact_list_members",
	} {
		if got := countRowsForProject(t, ctx.AppDB(), table, "test-proj"); got != 1 {
			t.Fatalf("%s rows=%d, want 1 after successful create", table, got)
		}
	}
}

func TestMessageToolsRejectMalformedInputBeforePlatformCalls(t *testing.T) {
	tests := []struct {
		name    string
		handler string
		args    map[string]any
	}{
		{name: "send missing contact id", handler: "send", args: map[string]any{"body": "hello"}},
		{name: "send rejects zero contact id", handler: "send", args: map[string]any{"id": 0, "body": "hello"}},
		{name: "send rejects negative contact id", handler: "send", args: map[string]any{"id": -4, "body": "hello"}},
		{name: "send rejects fractional contact id", handler: "send", args: map[string]any{"id": 1.5, "body": "hello"}},
		{name: "send missing content", handler: "send", args: map[string]any{"id": 1}},
		{name: "send rejects whitespace body", handler: "send", args: map[string]any{"id": 1, "body": "  \n "}},
		{name: "send rejects invalid template id", handler: "send", args: map[string]any{"id": 1, "body": "hello", "template_id": -1}},
		{name: "send rejects non-string body", handler: "send", args: map[string]any{"id": 1, "body": 42}},
		{name: "reply missing content", handler: "reply", args: map[string]any{"id": 1}},
		{name: "test send missing content", handler: "test", args: map[string]any{"id": 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			platform := &crmRecordingPlatform{}
			ctx := newTestCtx(t, tk.WithPlatform(platform))
			app := &App{}
			var err error
			switch tt.handler {
			case "send":
				_, err = app.toolSendMessage(ctx, tt.args)
			case "reply":
				_, err = app.toolReply(ctx, tt.args)
			case "test":
				_, err = app.toolSendTest(ctx, tt.args)
			}
			if err == nil {
				t.Fatal("expected validation error")
			}
			if platform.whoAmICalls != 0 || platform.getInstanceCalls != 0 || len(platform.calls) != 0 {
				t.Fatalf(
					"platform calls before validation: whoami=%d get_instance=%d call_app=%d",
					platform.whoAmICalls, platform.getInstanceCalls, len(platform.calls),
				)
			}
		})
	}
}

func TestMessageToolSchemasRequirePositiveIDAndContent(t *testing.T) {
	wanted := map[string]bool{
		"contacts_send_message": false,
		"contacts_reply":        false,
		"contacts_send_test":    false,
	}
	for _, tool := range (&App{}).MCPTools() {
		if _, ok := wanted[tool.Name]; !ok {
			continue
		}
		wanted[tool.Name] = true
		properties, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no properties", tool.Name)
		}
		idSchema, ok := properties["id"].(map[string]any)
		if !ok || int64FromAny(idSchema["minimum"]) != 1 {
			t.Fatalf("%s id schema=%#v, want minimum 1", tool.Name, idSchema)
		}
		if _, ok := properties["content_sid"]; !ok {
			t.Fatalf("%s schema missing content_sid", tool.Name)
		}
		if _, ok := properties["idempotency_key"]; !ok {
			t.Fatalf("%s schema missing idempotency_key", tool.Name)
		}
		if _, ok := properties["attachments"]; !ok {
			t.Fatalf("%s schema missing attachments", tool.Name)
		}
		if _, ok := properties["attachment_storage_ids"]; !ok {
			t.Fatalf("%s schema missing attachment_storage_ids", tool.Name)
		}
		templateSchema, ok := properties["template_id"].(map[string]any)
		if !ok || int64FromAny(templateSchema["minimum"]) != 0 {
			t.Fatalf("%s template_id schema=%#v, want compatibility minimum 0", tool.Name, templateSchema)
		}
		anyOf, ok := tool.InputSchema["anyOf"].([]any)
		if !ok || len(anyOf) != 6 {
			t.Fatalf("%s anyOf=%#v, want body/body_html/template/attachment alternatives", tool.Name, tool.InputSchema["anyOf"])
		}
		var templateBranch map[string]any
		for _, rawBranch := range anyOf {
			branch, _ := rawBranch.(map[string]any)
			required, _ := branch["required"].([]string)
			if len(required) == 1 && required[0] == "template_id" {
				templateBranch = branch
				break
			}
		}
		branchProperties, _ := templateBranch["properties"].(map[string]any)
		branchTemplate, _ := branchProperties["template_id"].(map[string]any)
		if int64FromAny(branchTemplate["minimum"]) != 1 {
			t.Fatalf("%s template-only branch=%#v, want minimum 1", tool.Name, templateBranch)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("tool %s not found", name)
		}
	}
}

func TestMessageToolDescriptionsWarnAboutExternalSideEffects(t *testing.T) {
	wanted := map[string]bool{
		"contacts_send_message": false,
		"contacts_reply":        false,
		"contacts_send_test":    false,
	}
	for _, tool := range (&App{}).MCPTools() {
		if _, ok := wanted[tool.Name]; !ok {
			continue
		}
		wanted[tool.Name] = true
		if !strings.Contains(tool.Description, "REAL EXTERNAL SEND") {
			t.Fatalf("%s description lacks side-effect warning: %q", tool.Name, tool.Description)
		}
		if !strings.Contains(tool.Description, "OMIT") || !strings.Contains(tool.Description, "template_id") {
			t.Fatalf("%s description lacks optional-field guidance: %q", tool.Name, tool.Description)
		}
		if tool.Name != "contacts_reply" && !strings.Contains(tool.Description, "messaging_senders_list") {
			t.Fatalf("%s description lacks read-only configuration guidance: %q", tool.Name, tool.Description)
		}
	}
	for name, found := range wanted {
		if !found {
			t.Fatalf("tool %s not found", name)
		}
	}
}
