package main

// Tests for automated/no-reply sender classification (v0.7.0+):
// the classifier, inbound ignore-before-upsert wiring, and the default
// exclusion of existing automated contacts from contacts_list_messageable.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestIsAutomatedSender(t *testing.T) {
	cases := []struct {
		name    string
		from    string
		headers map[string]any
		want    bool
	}{
		{"plain human", "alice@example.com", nil, false},
		{"role address (not flagged)", "support@example.com", nil, false},
		{"noreply local-part", "noreply@tiktok.com", nil, true},
		{"no-reply hyphen", "no-reply@bank.com", nil, true},
		{"mailer-daemon", "MAILER-DAEMON@mx.example.com", nil, true},
		{"notifications", "notifications@github.com", nil, true},
		{"ses generated local-part", "010001904fd6d83f-12345678@email.amazonses.com", nil, true},
		{"auto-submitted header", "team@example.com", map[string]any{"Auto-Submitted": "auto-generated"}, true},
		{"auto-submitted no (real reply)", "team@example.com", map[string]any{"Auto-Submitted": "no"}, false},
		{"precedence bulk", "news@example.com", map[string]any{"Precedence": "bulk"}, true},
		{"list-unsubscribe", "news@example.com", map[string]any{"List-Unsubscribe": "<mailto:u@x.com>"}, true},
		{"null return-path", "x@example.com", map[string]any{"Return-Path": "<>"}, true},
		{"header case-insensitive", "x@example.com", map[string]any{"precedence": "list"}, true},
	}
	for _, tc := range cases {
		got, reason := isAutomatedSender("email", tc.from, tc.headers)
		if got != tc.want {
			t.Errorf("%s: isAutomatedSender(%q)=%v (reason=%q), want %v", tc.name, tc.from, got, reason, tc.want)
		}
	}
}

func TestInbound_IgnoresAutomatedSenderBeforeContactCreate(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx
	app := &App{}

	post := func(payload string) map[string]any {
		t.Helper()
		r := httptest.NewRequest("POST", "/inbound?project_id=test-proj", bytes.NewBufferString(payload))
		w := httptest.NewRecorder()
		app.handleInbound(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("inbound status=%d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	auto := post(`{"channel":"email","from":"010001904fd6d83f-12345678@email.amazonses.com","to":["support@acme.com"],"body_text":"delivery failure"}`)
	if auto["ignored"] != true {
		t.Fatalf("expected SES inbound to be ignored, got %v", auto)
	}
	if got := countContacts(t, ctx); got != 0 {
		t.Fatalf("automated inbound created %d contacts, want 0", got)
	}

	human := post(`{"channel":"email","from":"jane@startup.com","to":["support@acme.com"],"body_text":"hi, question about pricing"}`)
	if human["ignored"] == true || human["contact_id"] == nil {
		t.Fatalf("expected human inbound to create a contact, got %v", human)
	}
	if got := countContacts(t, ctx); got != 1 {
		t.Fatalf("human inbound contact count=%d, want 1", got)
	}
}

func TestListMessageable_ExcludesAutomatedByDefault(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	db := ctx.AppDB()

	human, _, err := dbUpsertByChannel(db, "test-proj", "email", "jane@startup.com", map[string]any{"display_name": "Jane"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	bot, _, err := dbUpsertByChannel(db, "test-proj", "email", "noreply@tiktok.com", map[string]any{"display_name": "TikTok"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := dbAddTag(db, "test-proj", bot.ID, tagAutomated); err != nil {
		t.Fatal(err)
	}

	ids := func(args map[string]any) map[int64]bool {
		out, err := app.toolListMessageable(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		set := map[int64]bool{}
		raw, _ := json.Marshal(out.(map[string]any)["contacts"])
		var rows []struct {
			ID int64 `json:"id"`
		}
		_ = json.Unmarshal(raw, &rows)
		for _, r := range rows {
			set[r.ID] = true
		}
		return set
	}

	def := ids(map[string]any{"channel": "email"})
	if !def[human.ID] {
		t.Errorf("human contact should be messageable")
	}
	if def[bot.ID] {
		t.Errorf("automated contact should be excluded by default")
	}

	inc := ids(map[string]any{"channel": "email", "include_automated": true})
	if !inc[human.ID] || !inc[bot.ID] {
		t.Errorf("include_automated=true should return both, got %v", inc)
	}
}

func contactHasTag(t *testing.T, ctx *sdk.AppCtx, contactID int64, tag string) bool {
	t.Helper()
	var n int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_tags WHERE contact_id = ? AND tag_name = ?`,
		contactID, tag).Scan(&n)
	return n > 0
}

func countContacts(t *testing.T, ctx *sdk.AppCtx) int {
	t.Helper()
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
