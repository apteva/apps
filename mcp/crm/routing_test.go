package main

// Tests for inbound routing rules (v0.8.0): the address-pattern matcher,
// rule evaluation (recipient/sender -> add list/tag), and the inbox query.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAddressMatchesPattern(t *testing.T) {
	cases := []struct {
		pattern, addr string
		want          bool
	}{
		{"", "a@b.com", true},
		{"*", "a@b.com", true},
		{"*@*", "a@b.com", true},
		{"alice@acme.com", "alice@acme.com", true},
		{"alice@acme.com", "bob@acme.com", false},
		{"@acme.com", "anyone@acme.com", true},
		{"@acme.com", "anyone@other.com", false},
		{"*@acme.com", "anyone@acme.com", true},
		{"support@*", "support@acme.com", true},
		{"support@*", "support@other.io", true},
		{"support@*", "sales@acme.com", false},
		{"ALICE@ACME.COM", "alice@acme.com", true}, // case-insensitive
		{"@acme.com", "", false},
	}
	for _, tc := range cases {
		if got := addressMatchesPattern(tc.pattern, tc.addr); got != tc.want {
			t.Errorf("addressMatchesPattern(%q, %q) = %v, want %v", tc.pattern, tc.addr, got, tc.want)
		}
	}
}

func TestApplyRoutingRules_RecipientAndSender(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	app := &App{}

	// A list to route into.
	listOut, err := app.toolListsCreate(ctx, map[string]any{"name": "Support"})
	if err != nil {
		t.Fatal(err)
	}
	listID := listOut.(map[string]any)["list"].(*List).ID

	// Rule 1: recipient support@acme.com -> add to Support list.
	if _, err := dbCreateRoutingRule(db, "test-proj", &routingRule{
		MatchRecipient: "support@acme.com", AddListID: &listID, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	// Rule 2: sender @vip.com -> tag vip.
	if _, err := dbCreateRoutingRule(db, "test-proj", &routingRule{
		MatchSender: "@vip.com", AddTag: "vip", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	c := mustCreate(t, ctx, map[string]any{"first_name": "Dana"})

	// Inbound to support@acme.com from someone@vip.com → both rules fire.
	acted, err := applyRoutingRules(db, "test-proj", c.ID, []string{"support@acme.com"}, "someone@vip.com")
	if err != nil {
		t.Fatal(err)
	}
	if len(acted.Lists) != 1 || acted.Lists[0] != listID {
		t.Errorf("expected list %d added, got %v", listID, acted.Lists)
	}
	if len(acted.Tags) != 1 || acted.Tags[0] != "vip" {
		t.Errorf("expected vip tag, got %v", acted.Tags)
	}
	if !contactHasTag(t, ctx, c.ID, "vip") {
		t.Errorf("contact should be tagged vip")
	}

	// Inbound to sales@acme.com from normal@x.com → neither rule matches.
	c2 := mustCreate(t, ctx, map[string]any{"first_name": "Eve"})
	acted2, _ := applyRoutingRules(db, "test-proj", c2.ID, []string{"sales@acme.com"}, "normal@x.com")
	if len(acted2.Lists) != 0 || len(acted2.Tags) != 0 {
		t.Errorf("no rule should match, got lists=%v tags=%v", acted2.Lists, acted2.Tags)
	}
}

func TestApplyRoutingRules_RequiresBothWhenBothSet(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	c := mustCreate(t, ctx, map[string]any{"first_name": "Sam"})

	// Rule requires recipient support@* AND sender @acme.com.
	if _, err := dbCreateRoutingRule(db, "test-proj", &routingRule{
		MatchRecipient: "support@*", MatchSender: "@acme.com", AddTag: "acme-support", Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}

	// Recipient matches but sender doesn't → no action.
	acted, _ := applyRoutingRules(db, "test-proj", c.ID, []string{"support@us.io"}, "x@other.com")
	if len(acted.Tags) != 0 {
		t.Errorf("AND rule should not fire when sender mismatches, got %v", acted.Tags)
	}
	// Both match → fires.
	acted, _ = applyRoutingRules(db, "test-proj", c.ID, []string{"support@us.io"}, "boss@acme.com")
	if len(acted.Tags) != 1 {
		t.Errorf("AND rule should fire when both match, got %v", acted.Tags)
	}
}

func TestInboxConversations(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	app := &App{}

	c := mustCreate(t, ctx, map[string]any{"display_name": "Tom"})
	openID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	closedID := mkConversation(t, ctx, "test-proj", c.ID, "email")
	if _, err := app.toolSetConversationStatus(ctx, map[string]any{"conversation_id": closedID, "status": "closed"}); err != nil {
		t.Fatal(err)
	}

	open, total, err := dbInboxConversations(db, "test-proj", "open", 50, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("inbox(open) total = %d, want 1", total)
	}
	if len(open) != 1 || open[0].ID != openID {
		t.Fatalf("inbox(open) should return only the open convo, got %d", len(open))
	}
	if open[0].ContactName != "Tom" {
		t.Errorf("inbox row should carry contact name, got %q", open[0].ContactName)
	}

	all, total, err := dbInboxConversations(db, "test-proj", "all", 50, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Fatalf("inbox(all) total = %d, want 2", total)
	}
	if len(all) != 2 {
		t.Fatalf("inbox(all) should return both, got %d", len(all))
	}
}

func TestInboxConversations_Filters(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	app := &App{}

	acme := mustCreate(t, ctx, map[string]any{
		"display_name": "Acme Buyer",
		"channels":     []any{map[string]any{"kind": "email", "value": "buyer@acme.com", "is_primary": true}},
	})
	beta := mustCreate(t, ctx, map[string]any{
		"display_name": "Beta Lead",
		"channels":     []any{map[string]any{"kind": "email", "value": "lead@beta.com", "is_primary": true}},
	})
	sms := mustCreate(t, ctx, map[string]any{
		"display_name": "SMS Lead",
		"channels":     []any{map[string]any{"kind": "phone", "value": "+15551230000", "is_primary": true}},
	})

	acmeConv := mkConversation(t, ctx, "test-proj", acme.ID, "email")
	betaConv := mkConversation(t, ctx, "test-proj", beta.ID, "email")
	smsConv := mkConversation(t, ctx, "test-proj", sms.ID, "sms")

	if err := dbConversationParticipantsAdd(db, "test-proj", acmeConv, "email", []conversationParticipant{
		{Role: "from", Address: "buyer@acme.com", ContactID: acme.ID},
		{Role: "to", Address: "support@our.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbConversationParticipantsAdd(db, "test-proj", betaConv, "email", []conversationParticipant{
		{Role: "from", Address: "lead@beta.com", ContactID: beta.ID},
		{Role: "to", Address: "sales@our.test"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dbConversationParticipantsAdd(db, "test-proj", smsConv, "sms", []conversationParticipant{
		{Role: "from", Address: "+15551230000", ContactID: sms.ID},
		{Role: "to", Address: "+15559990000"},
	}); err != nil {
		t.Fatal(err)
	}

	listOut, err := app.toolListsCreate(ctx, map[string]any{"name": "VIP"})
	if err != nil {
		t.Fatal(err)
	}
	listID := listOut.(map[string]any)["list"].(*List).ID
	if err := dbListAddContact(db, "test-proj", listID, acme.ID, "test"); err != nil {
		t.Fatal(err)
	}
	if err := dbAddTag(db, "test-proj", beta.ID, "prospect"); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		args map[string]any
		want int64
	}{
		{"from domain", map[string]any{"status": "all", "filters": []any{map[string]any{"field": "from", "op": "domain", "value": "acme.com"}}}, acmeConv},
		{"received at", map[string]any{"status": "all", "to": "sales@our.test"}, betaConv},
		{"channel", map[string]any{"status": "all", "channel": "sms"}, smsConv},
		{"list", map[string]any{"status": "all", "list_id": listID}, acmeConv},
		{"tag", map[string]any{"status": "all", "tag": "prospect"}, betaConv},
	}
	for _, tc := range cases {
		out, err := app.toolInbox(ctx, tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		rows := out.(map[string]any)["inbox"].([]*inboxRow)
		if len(rows) != 1 || rows[0].ID != tc.want {
			t.Fatalf("%s: got %v rows, want conversation %d", tc.name, len(rows), tc.want)
		}
	}

	globalCtx = ctx
	rawFilters := `[{"field":"from","op":"domain","value":"acme.com"}]`
	req := httptest.NewRequest(http.MethodGet, "/inbox?project_id=test-proj&status=all&filters="+url.QueryEscape(rawFilters), nil)
	w := httptest.NewRecorder()
	app.handleHTTPInbox(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("http filtered inbox status = %d body=%s", w.Code, w.Body.String())
	}
	var filtered struct {
		Inbox []*inboxRow `json:"inbox"`
		Total int         `json:"total"`
		Count int         `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 1 || filtered.Count != 1 || len(filtered.Inbox) != 1 || filtered.Inbox[0].ID != acmeConv {
		t.Fatalf("http filtered inbox = total %d count %d rows %d, want acme conversation %d", filtered.Total, filtered.Count, len(filtered.Inbox), acmeConv)
	}

	req = httptest.NewRequest(http.MethodGet, "/inbox?project_id=test-proj&status=all&limit=1&offset=1", nil)
	w = httptest.NewRecorder()
	app.handleHTTPInbox(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("http paged inbox status = %d body=%s", w.Code, w.Body.String())
	}
	var paged struct {
		Inbox  []*inboxRow `json:"inbox"`
		Total  int         `json:"total"`
		Count  int         `json:"count"`
		Offset int         `json:"offset"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &paged); err != nil {
		t.Fatal(err)
	}
	if paged.Total != 3 || paged.Count != 1 || paged.Offset != 1 || len(paged.Inbox) != 1 {
		t.Fatalf("http paged inbox = total %d count %d offset %d rows %d, want total 3 count 1 offset 1", paged.Total, paged.Count, paged.Offset, len(paged.Inbox))
	}
}
