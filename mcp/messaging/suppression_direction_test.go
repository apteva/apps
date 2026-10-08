package main

import (
	"database/sql"
	"errors"
	"os"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestOutboundOnlySuppressionBlocksSendButRoutesInbound(t *testing.T) {
	p := &stubPlatform{}
	ctx := newTestCtx(t, p)
	seedSESRecipient(t, ctx, "contact@example.com")
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if _, err := app.toolSuppressionAdd(ctx, map[string]any{"address": "customer@example.net", "channel": "email", "direction": "outbound", "reason": "unsubscribe"}); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"outbound", "inbound"} {
		out, err := app.toolSuppressionCheck(ctx, map[string]any{"address": "customer@example.net", "channel": "email", "direction": direction})
		if err != nil {
			t.Fatal(err)
		}
		result := out.(map[string]any)
		if result["suppressed"] != (direction == "outbound") || result["check_direction"] != direction {
			t.Fatalf("directional check: %v", result)
		}
	}
	_, err := app.toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "customer@example.net", "body": "must not send"})
	var blocked *recipientSuppressedError
	if !errors.As(err, &blocked) {
		t.Fatalf("outbound not blocked: %v", err)
	}
	if len(p.executeCalls) != 0 {
		t.Fatal("suppressed send reached provider")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,envelope_recipients,status,route_status,received_at)
		VALUES('test-proj','email','in','customer@example.net','["contact@example.com"]','["contact@example.com"]','received','pending','2026-10-08T08:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	msg, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil {
		t.Fatal(err)
	}
	if err = dispatchInbound(ctx, "test-proj", msg); err != nil {
		t.Fatal(err)
	}
	got, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || got.RouteStatus != "ok" || len(p.callAppCalls) != 1 {
		t.Fatalf("incoming reply lost: %+v, %v calls=%v", got, err, p.callAppCalls)
	}
}

func TestOutboundRequestNeverWeakensExistingBlocks(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	db := ctx.AppDB()
	app := &App{}
	if err := dbSuppressionUpsert(db, "test-proj", "email", "customer@example.net", "complaint", "ses"); err != nil {
		t.Fatal(err)
	}
	before, err := dbSuppressionGetExact(db, "test-proj", "email", "address", "customer@example.net")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = app.toolSuppressionAdd(ctx, map[string]any{"address": "customer@example.net", "channel": "email", "direction": "outbound", "reason": "unsubscribe", "source": "crm"}); err != nil {
		t.Fatal(err)
	}
	after, err := dbSuppressionGetExact(db, "test-proj", "email", "address", "customer@example.net")
	if err != nil || after.Direction != "both" || after.Reason != before.Reason || after.Source != before.Source || after.FirstSeen != before.FirstSeen {
		t.Fatalf("stronger block changed: before=%+v after=%+v %v", before, after, err)
	}
	if match, err := dbSuppressionMatch(db, "test-proj", "email", "customer@example.net", "inbound"); err != nil || match == nil {
		t.Fatalf("inbound block was lost: %v", err)
	}
	if match, err := dbSuppressionMatch(db, "other-project", "email", "customer@example.net"); err != nil || match != nil {
		t.Fatalf("cross-project suppression: %v", err)
	}
}

func TestInboundDirectionStillChecksDomainAfterOutboundAddressMatch(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	if err := dbSuppressionUpsertExec(ctx.AppDB(), "test-proj", "email", "address", "customer@example.net", "unsubscribe", "crm", "outbound"); err != nil {
		t.Fatal(err)
	}
	if err := dbSuppressionUpsertKind(ctx.AppDB(), "test-proj", "email", "domain", "example.net", "spam", "manual"); err != nil {
		t.Fatal(err)
	}
	match, err := dbSuppressionMatch(ctx.AppDB(), "test-proj", "email", "customer@example.net", "inbound")
	if err != nil || match == nil || match.Kind != "domain" || match.Direction != "both" {
		t.Fatalf("inbound domain fallback failed: %+v %v", match, err)
	}
	if err = dbSuppressionUpsert(ctx.AppDB(), "test-proj", "email", "customer@example.net", "complaint", "ses"); err != nil {
		t.Fatal(err)
	}
	match, err = dbSuppressionMatch(ctx.AppDB(), "test-proj", "email", "customer@example.net", "inbound")
	if err != nil || match == nil || match.Kind != "address" || match.Reason != "complaint" {
		t.Fatalf("legacy escalation failed: %+v %v", match, err)
	}
}

func TestSuppressionMigrationPreservesLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE suppressions(project_id TEXT,channel TEXT,address TEXT,reason TEXT,source TEXT,kind TEXT,first_seen TEXT,last_seen TEXT);
		INSERT INTO suppressions VALUES('legacy','email','keep@example.net','unsubscribe','manual','address','original','original')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/017_suppression_direction.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	row, err := dbSuppressionGetExact(db, "legacy", "email", "address", "keep@example.net")
	if err != nil || row == nil || row.Direction != "both" || row.Reason != "unsubscribe" || row.FirstSeen != "original" {
		t.Fatalf("legacy row changed: %+v %v", row, err)
	}
}

func TestDirectionalSuppressionEventsAndValidation(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, &stubPlatform{}, tk.WithEmitter(rec))
	app := &App{}
	for _, args := range []map[string]any{
		{"address": "a@example.net", "direction": "invalid"},
		{"address": "a@example.net", "direction": "inbound"},
	} {
		if _, err := app.toolSuppressionAdd(ctx, args); err == nil {
			t.Fatalf("invalid direction accepted: %v", args)
		}
	}
	if _, err := app.toolSuppressionCheck(ctx, map[string]any{"address": "a@example.net", "direction": "both"}); err == nil {
		t.Fatal("invalid check direction accepted")
	}
	if _, err := app.toolSuppressionAdd(ctx, map[string]any{"address": "a@example.net", "direction": "outbound"}); err != nil {
		t.Fatal(err)
	}
	events := rec.EventsByTopic("suppression.changed")
	if len(events) != 1 || events[0].Data.(map[string]any)["direction"] != "outbound" {
		t.Fatalf("missing event direction: %v", events)
	}
	listed, err := app.toolSuppressionList(ctx, map[string]any{})
	if err != nil || listed.(map[string]any)["suppressions"].([]Suppression)[0].Direction != "outbound" {
		t.Fatalf("missing list direction: %v %v", listed, err)
	}
}
