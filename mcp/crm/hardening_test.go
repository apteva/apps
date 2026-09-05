package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestListMembershipRejectsCrossProjectContact(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	owner := mustCreate(t, ctx, map[string]any{"display_name": "Owner"})
	victim, err := dbCreate(db, "other-project", map[string]any{
		"display_name": "Victim",
		"channels": []any{map[string]any{
			"kind": "email", "value": "victim@example.test", "is_primary": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := dbListCreate(db, "test-proj", &List{Name: "Private list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbListAddContact(db, "test-proj", list.ID, owner.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := dbListAddContact(db, "test-proj", list.ID, victim.ID, "test"); err == nil {
		t.Fatal("cross-project contact was added to list")
	}

	// Simulate a row that predates the new ownership trigger.
	if _, err := db.Exec(`DROP TRIGGER crm_membership_owner_insert`); err != nil {
		t.Fatal(err)
	}
	// Defense in depth: a legacy or manually-corrupted membership row must
	// not disclose the foreign project's contact through list reads.
	if _, err := db.Exec(
		`INSERT INTO contact_list_members (list_id, contact_id, project_id, source)
		 VALUES (?, ?, ?, 'legacy')`,
		list.ID, victim.ID, "test-proj",
	); err != nil {
		t.Fatal(err)
	}
	members, err := dbListMembers(db, "test-proj", list.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != owner.ID {
		t.Fatalf("members=%v, want only owner %d", members, owner.ID)
	}
}

func TestInboundDuplicateDoesNotCreateAnotherConversation(t *testing.T) {
	ctx := newTestCtx(t)
	payload := inboundPayload{
		MessageID:       9001,
		Channel:         channelEmail,
		From:            "human@example.test",
		To:              []string{"support@example.test"},
		Subject:         "Hello",
		BodyText:        "First delivery",
		MessageIDHeader: "<message-9001@example.test>",
	}
	first, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	if second["deduped"] != true {
		t.Fatalf("second delivery=%v, want deduped", second)
	}
	if second["conversation_id"] != first["conversation_id"] {
		t.Fatalf("conversation changed on retry: first=%v second=%v", first, second)
	}
	var contacts, conversations, activities int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contacts WHERE project_id = 'test-proj'`).Scan(&contacts)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_conversations WHERE project_id = 'test-proj'`).Scan(&conversations)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE project_id = 'test-proj' AND messaging_id = 9001`).Scan(&activities)
	if contacts != 1 || conversations != 1 || activities != 1 {
		t.Fatalf("contacts=%d conversations=%d activities=%d, want 1/1/1", contacts, conversations, activities)
	}
}

func TestMergeMovesAllContactOwnedRelations(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	winner := mustCreate(t, ctx, map[string]any{
		"display_name": "Winner",
		"channels": []any{map[string]any{
			"kind": "email", "value": "winner@example.test", "is_primary": true,
		}},
	})
	loser := mustCreate(t, ctx, map[string]any{
		"display_name": "Loser",
		"channels": []any{map[string]any{
			"kind": "phone", "value": "+12025550123", "is_primary": true,
		}},
	})
	list, err := dbListCreate(db, "test-proj", &List{Name: "Merge list"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbListAddContact(db, "test-proj", list.ID, loser.ID, "test"); err != nil {
		t.Fatal(err)
	}
	convo, err := db.Exec(
		`INSERT INTO contact_conversations
		 (project_id, contact_id, channel, started_at, last_activity_at)
		 VALUES ('test-proj', ?, 'email', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)`,
		loser.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	convoID, _ := convo.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO contact_activities
		 (project_id, contact_id, kind, body, occurred_at, conversation_id)
		 VALUES ('test-proj', ?, 'email_received', 'hello', CURRENT_TIMESTAMP, ?)`,
		loser.ID, convoID,
	); err != nil {
		t.Fatal(err)
	}
	segment, err := db.Exec(
		`INSERT INTO contact_segments
		 (project_id, name, kind, definition_json)
		 VALUES ('test-proj', 'Merge segment', 'static', '[]')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	segmentID, _ := segment.LastInsertId()
	if _, err := db.Exec(
		`INSERT INTO contact_segment_snapshots (segment_id, contact_id, project_id)
		 VALUES (?, ?, 'test-proj')`,
		segmentID, loser.ID,
	); err != nil {
		t.Fatal(err)
	}
	opp, err := dbOpportunityCreate(db, "test-proj", opportunityCreateInput{ContactID: loser.ID})
	if err != nil {
		t.Fatal(err)
	}

	if err := dbMerge(db, "test-proj", loser.ID, winner.ID, "duplicate", "test"); err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		query string
		args  []any
	}{
		{`SELECT COUNT(*) FROM contact_conversations WHERE id = ? AND contact_id = ?`, []any{convoID, winner.ID}},
		{`SELECT COUNT(*) FROM contact_activities WHERE conversation_id = ? AND contact_id = ?`, []any{convoID, winner.ID}},
		{`SELECT COUNT(*) FROM contact_list_members WHERE list_id = ? AND contact_id = ?`, []any{list.ID, winner.ID}},
		{`SELECT COUNT(*) FROM contact_segment_snapshots WHERE segment_id = ? AND contact_id = ?`, []any{segmentID, winner.ID}},
		{`SELECT COUNT(*) FROM crm_opportunities WHERE id = ? AND contact_id = ?`, []any{opp.ID, winner.ID}},
	}
	for _, check := range checks {
		var count int
		if err := db.QueryRow(check.query, check.args...).Scan(&count); err != nil || count != 1 {
			t.Fatalf("merge relation check %q: count=%d err=%v", check.query, count, err)
		}
	}
	merged, err := dbGetByID(db, "test-proj", winner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if merged.PrimaryEmail != "winner@example.test" || merged.PrimaryPhone != "+12025550123" {
		t.Fatalf("primary mirrors not rebuilt: email=%q phone=%q", merged.PrimaryEmail, merged.PrimaryPhone)
	}
}

func TestAttributeValidationAndProjectOwnership(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	contact := mustCreate(t, ctx, map[string]any{"display_name": "Local"})
	foreign, err := dbCreate(db, "other-project", map[string]any{"display_name": "Foreign"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dbDefineAttribute(
		db, "test-proj", "fit", "Fit", "select",
		[]any{"high", "medium", "low"}, false, 0,
	); err != nil {
		t.Fatal(err)
	}
	if err := dbSetAttribute(db, "test-proj", contact.ID, "fit", "high", "test"); err != nil {
		t.Fatal(err)
	}
	if err := dbSetAttribute(db, "test-proj", contact.ID, "fit", "invalid", "test"); err == nil {
		t.Fatal("invalid select value was accepted")
	}
	if err := dbSetAttribute(db, "test-proj", foreign.ID, "fit", "high", "test"); err == nil {
		t.Fatal("cross-project attribute write was accepted")
	}
	if _, err := dbDefineAttribute(
		db, "test-proj", "fit", "Fit", "text", nil, false, 0,
	); err == nil || !strings.Contains(err.Error(), "cannot be changed") {
		t.Fatalf("type change error=%v", err)
	}
}

func TestIntArgAcceptsHTTPQueryStrings(t *testing.T) {
	if got := intArg(map[string]any{"limit": "200"}, "limit", 50); got != 200 {
		t.Fatalf("intArg string=%d, want 200", got)
	}
}

func TestDecodeJSONBodyRejectsOversizedRequests(t *testing.T) {
	body := `{"value":"` + strings.Repeat("x", int(maxJSONBodyBytes)) + `"}`
	req := httptest.NewRequest("POST", "/", strings.NewReader(body))
	recorder := httptest.NewRecorder()
	var payload map[string]any
	if err := decodeJSONBody(recorder, req, &payload); err == nil {
		t.Fatal("oversized JSON request was accepted")
	}
}
