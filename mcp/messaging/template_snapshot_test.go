package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestTemplateSnapshotMigrationPreservesLegacyMessage(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	files, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if file.Name() >= "018_template_snapshots.sql" {
			continue
		}
		body, err := os.ReadFile("migrations/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatalf("%s: %v", file.Name(), err)
		}
	}
	if _, err := db.Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,body_text,status,template_id) VALUES(1,'legacy','whatsapp','out','+15551112222','["+15553334444"]','','delivered',46)`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/018_template_snapshots.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var savedBody, status string
	var snapshot sql.NullString
	if err := db.QueryRow(`SELECT body_text,status,template_snapshot FROM messages WHERE id=1`).Scan(&savedBody, &status, &snapshot); err != nil {
		t.Fatal(err)
	}
	if savedBody != "" || status != "delivered" || snapshot.Valid {
		t.Fatal("migration changed or fabricated legacy content")
	}
}

func TestTemplateSnapshotRenderingNeverGuessesOrRecursivelySubstitutes(t *testing.T) {
	tpl := &Template{ID: 46, Name: "reengage", BodyText: "Hi {{ 1 }}, this is {{2}}.", VarsSchema: json.RawMessage(`{"1":"WRONG example","2":"Default"}`)}
	s := captureTemplateSnapshot(tpl, "HX46", `{"1":"Roxy","2":"John {{1}}"}`, "", "", "")
	if s.Availability != "complete" || s.BodyText != "Hi Roxy, this is John {{1}}." {
		t.Fatalf("snapshot=%+v", s)
	}
	s = captureTemplateSnapshot(tpl, "HX46", `{"1":"Roxy"}`, "", "", "")
	if s.Availability != "incomplete" || len(s.MissingVariables) != 1 || s.MissingVariables[0] != "2" {
		t.Fatalf("missing values guessed from examples: %+v", s)
	}
	for _, vars := range []string{"", "not JSON"} {
		s = captureTemplateSnapshot(nil, "HXunknown", vars, "", "", "")
		if s.Availability != "unavailable" || s.BodyText != "" {
			t.Fatalf("unknown provider content fabricated: %+v", s)
		}
	}
	s = captureTemplateSnapshot(tpl, "", "", "Actual subject", "Actual local payload", "<p>Actual HTML</p>")
	if s.BodyText != "Actual local payload" || s.BodyHTML != "<p>Actual HTML</p>" || s.Subject != "Actual subject" {
		t.Fatalf("local payload was re-rendered: %+v", s)
	}
}

func TestTemplateSnapshotPersistsAcrossEditsDeletionAndIdempotentRetry(t *testing.T) {
	for _, providerBody := range []string{"", "Provider-confirmed Roxy"} {
		t.Run(providerBody, func(t *testing.T) {
			plat := newPhoneStub(nil)
			response, _ := json.Marshal(map[string]any{"sid": "MMsnapshot", "body": providerBody})
			plat.replyByTool["send_whatsapp"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: response}
			ctx := newTestCtx(t, plat)
			res, err := ctx.AppDB().Exec(`INSERT INTO templates(project_id,channel,name,body_text,vars_schema,provider_template_id,provider_status,var_style) VALUES('test-proj','whatsapp','reengage','Hi {{1}}, this is {{2}}.','{}','HX46','approved','numbered')`)
			if err != nil {
				t.Fatal(err)
			}
			tplID, _ := res.LastInsertId()
			args := map[string]any{"channel": "whatsapp", "from": "+15551112222", "to": "+15553334444", "template_id": tplID, "vars": map[string]any{"1": "Roxy", "2": "John"}, "idempotency_key": "snapshot-retry"}
			app := &App{}
			result, err := app.toolSendMessage(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			id := int64Arg(result.(map[string]any), "id")
			message, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
			if err != nil {
				t.Fatal(err)
			}
			want, source := "Hi Roxy, this is John.", "send_time_template"
			if providerBody != "" {
				want, source = providerBody, "provider_response"
			}
			if message.BodyText != "" || message.TemplateSnapshot == nil || message.TemplateSnapshot.BodyText != want || message.TemplateSnapshot.ContentSource != source || message.TemplateSnapshot.Variables["1"] != "Roxy" {
				t.Fatalf("snapshot/payload=%+v", message)
			}
			for _, c := range plat.executeCalls {
				if c.Tool == "send_whatsapp" {
					if _, ok := c.Input["Body"]; ok {
						t.Fatal("ContentSid request included Body")
					}
					if c.Input["ContentSid"] != "HX46" || c.Input["ContentVariables"] != `{"1":"Roxy","2":"John"}` {
						t.Fatalf("provider payload=%v", c.Input)
					}
				}
			}
			if _, err := ctx.AppDB().Exec(`UPDATE templates SET body_text='CHANGED',name='changed',deleted_at=CURRENT_TIMESTAMP WHERE id=?`, tplID); err != nil {
				t.Fatal(err)
			}
			beforeCalls := len(plat.executeCalls)
			retry, err := app.toolSendMessage(ctx, args)
			if err != nil || int64Arg(retry.(map[string]any), "id") != id || len(plat.executeCalls) != beforeCalls {
				t.Fatalf("retry sent again: %v %v", retry, err)
			}
			list, _, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Limit: 10})
			if err != nil || len(list) != 1 || list[0].TemplateSnapshot.BodyText != want || list[0].TemplateSnapshot.Name != "reengage" {
				t.Fatalf("mutable history: %v %v", list, err)
			}
			foreign, err := dbMessageGet(ctx.AppDB(), "other-project", id)
			if err != nil || foreign != nil {
				t.Fatalf("snapshot leaked across projects: %v %v", foreign, err)
			}
			if _, err := ctx.AppDB().Exec(`UPDATE messages SET template_snapshot=NULL WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			legacy, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
			if err != nil || legacy.TemplateSnapshot != nil {
				t.Fatalf("legacy content fabricated: %v %v", legacy, err)
			}
		})
	}
}

func TestMessagingReleaseSourceMatchesVersion(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Runtime.Source == nil || m.Runtime.Source.Repo != "github.com/apteva/apps" || m.Runtime.Source.Ref != "messaging/v"+m.Version || m.Runtime.Source.Entry != "mcp/messaging" {
		t.Fatalf("source release pin drift: %+v", m.Runtime.Source)
	}
}
