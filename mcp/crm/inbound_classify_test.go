package main

// Tests for automated/no-reply sender classification (v0.7.0):
// the classifier, the inbound auto-tag wiring, and the default
// exclusion of automated contacts from contacts_list_messageable.

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

func TestInbound_AutoTagsAutomatedSender(t *testing.T) {
	ctx := newTestCtx(t)
	globalCtx = ctx
	app := &App{}

	post := func(payload string) int64 {
		t.Helper()
		r := httptest.NewRequest("POST", "/inbound?project_id=test-proj", bytes.NewBufferString(payload))
		w := httptest.NewRecorder()
		app.handleInbound(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("inbound status=%d body=%s", w.Code, w.Body.String())
		}
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return int64(out["contact_id"].(float64))
	}

	autoID := post(`{"channel":"email","from":"noreply@tiktok.com","to":["support@acme.com"],"body_text":"new follower"}`)
	if !contactHasTag(t, ctx, autoID, tagAutomated) {
		t.Errorf("expected noreply@tiktok.com contact to be tagged %q", tagAutomated)
	}

	humanID := post(`{"channel":"email","from":"jane@startup.com","to":["support@acme.com"],"body_text":"hi, question about pricing"}`)
	if contactHasTag(t, ctx, humanID, tagAutomated) {
		t.Errorf("human sender should NOT be tagged automated")
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
