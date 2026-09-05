package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestInboundRollbackAndRetry(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	if _, err := db.Exec(`CREATE TRIGGER injected_failure BEFORE INSERT ON conversation_participants BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	payload := inboundPayload{Channel: "email", From: "atomic@example.test", MessageID: 1234, BodyHTML: "<p>Hello <b>team</b></p><script>evil()</script><p>Final instruction</p>"}
	if _, err := ingestInbound(ctx, "test-proj", payload); err == nil {
		t.Fatal("expected injected failure")
	}
	for _, table := range []string{"contacts", "contact_channels", "contact_conversations", "contact_activities", "crm_event_outbox"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s after rollback: n=%d err=%v", table, n, err)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER injected_failure`); err != nil {
		t.Fatal(err)
	}
	first, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	if first["activity_id"] != second["activity_id"] || second["deduped"] != true {
		t.Fatalf("retry: %v / %v", first, second)
	}
	var text string
	if err := db.QueryRow(`SELECT body FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&text); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "Final instruction") || strings.Contains(text, "evil") || strings.Contains(text, "<") {
		t.Fatalf("html extraction: %q", text)
	}
}

func TestConcurrentInboundHasOneThread(t *testing.T) {
	ctx := newTestCtx(t)
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "concurrent@example.test", MessageID: 5678, BodyText: "hello"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"contacts", "contact_conversations", "contact_activities"} {
		var n int
		ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
		if n != 1 {
			t.Fatalf("%s count=%d", table, n)
		}
	}
}

func TestMessageSourceIDsDoNotCollide(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Source test"})
	for _, source := range []int64{101, 202} {
		if _, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, Kind: "email_received", Body: fmt.Sprint(source), MessagingID: 1, MessagingInstallID: source}); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []int64{101, 202} {
		a, err := dbActivityByMessagingID(ctx.AppDB(), "test-proj", 1, source)
		if err != nil || a == nil || a.Body != fmt.Sprint(source) {
			t.Fatalf("source=%d activity=%v err=%v", source, a, err)
		}
	}
}

func TestOutboxRetryKeepsStableEventID(t *testing.T) {
	ctx := newTestCtx(t)
	var status atomic.Int64
	status.Store(503)
	var ids []string
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event struct {
			Data map[string]any `json:"data"`
		}
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
		}
		ids = append(ids, anyString(event.Data["event_id"]))
		w.WriteHeader(int(status.Load()))
	}))
	defer gateway.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gateway.URL)
	t.Setenv("APTEVA_APP_TOKEN", "local-test-token")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	if _, err := ctx.AppDB().Exec(`INSERT INTO crm_event_outbox(project_id,topic,payload) VALUES('test-proj','contact.updated','{"id":1}')`); err != nil {
		t.Fatal(err)
	}
	if err := flushCRMEvents(context.Background(), ctx); err == nil {
		t.Fatal("expected retryable gateway failure")
	}
	var pending int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox WHERE delivered_at IS NULL`).Scan(&pending)
	if pending != 1 {
		t.Fatal("failed event lost")
	}
	status.Store(200)
	if err := flushCRMEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] == "" || ids[0] != ids[1] {
		t.Fatalf("event ids=%v", ids)
	}
	if err := flushCRMEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 {
		t.Fatal("acknowledged event replayed")
	}
}

func TestStaleChannelPatchRejected(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Before"})
	_, err := dbUpdate(ctx.AppDB(), "test-proj", c.ID, map[string]any{"first_name": "New", "expected_updated_at": c.UpdatedAt}, "test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = dbUpdate(ctx.AppDB(), "test-proj", c.ID, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "stale@example.test"}}, "expected_updated_at": c.UpdatedAt}, "test")
	if err == nil {
		t.Fatal("stale edit accepted")
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_channels WHERE contact_id=?`, c.ID).Scan(&n)
	if n != 0 {
		t.Fatal("stale channels written")
	}
}

func TestStaticSegmentMaterializesAndPages(t *testing.T) {
	ctx := newTestCtx(t)
	for i := 0; i < 3; i++ {
		mustCreate(t, ctx, map[string]any{"company": "A"})
	}
	s, err := dbSegmentCreate(ctx.AppDB(), "test-proj", &Segment{Name: "Frozen", Kind: "static", Definition: json.RawMessage(`[{"field":"company","value":"A"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	mustCreate(t, ctx, map[string]any{"company": "A"})
	first, total, err := dbSegmentEvalPage(ctx.AppDB(), "test-proj", s, 2, 0)
	if err != nil || total != 3 || len(first) != 2 {
		t.Fatalf("first page %v/%d err=%v", first, total, err)
	}
	second, total, err := dbSegmentEvalPage(ctx.AppDB(), "test-proj", s, 2, first[1])
	if err != nil || total != 3 || len(second) != 1 || second[0] <= first[1] {
		t.Fatalf("second page %v/%d err=%v", second, total, err)
	}
	_, err = dbSegmentCreate(ctx.AppDB(), "test-proj", &Segment{Name: "Bad ref", Definition: json.RawMessage(`[{"predicate":"in_list","list_id":999}]`)})
	if err == nil {
		t.Fatal("unknown list accepted")
	}
}

func TestOpportunityConcurrentCloseAndTitle(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Pipeline"})
	o, err := dbOpportunityCreate(ctx.AppDB(), "test-proj", opportunityCreateInput{ContactID: c.ID})
	if err != nil {
		t.Fatal(err)
	}
	var won int64
	if err := ctx.AppDB().QueryRow(`SELECT id FROM crm_pipeline_stages WHERE pipeline_id=? AND category='won'`, o.PipelineID).Scan(&won); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for _, patch := range []map[string]any{{"title": "Edited"}, {"stage_id": won}} {
		go func(p map[string]any) {
			<-start
			_, _, err := dbOpportunityUpdate(ctx.AppDB(), "test-proj", o.ID, p)
			errs <- err
		}(patch)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got, err := dbOpportunityGet(ctx.AppDB(), "test-proj", o.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "won" || got.ClosedAt == "" || got.Title != "Edited" {
		t.Fatalf("inconsistent opportunity: %+v", got)
	}
}

func TestLegacyMigrationRepairsArchiveAndPrimaries(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if filepath.Base(path) >= "014" {
			break
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	_, err = db.Exec(`INSERT INTO contacts(id,project_id,status,primary_email,deleted_at) VALUES(1,'p','archived','a@example.test',CURRENT_TIMESTAMP),(2,'p','merged','loser@example.test',NULL),(3,'p','active','a@example.test',NULL); INSERT INTO contact_channels(project_id,contact_id,kind,value,is_primary) VALUES('p',3,'email','a@example.test',1),('p',3,'email','loser@example.test',1)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range files {
		if filepath.Base(path) < "014" {
			continue
		}
		raw, _ := os.ReadFile(path)
		if _, err = db.Exec(string(raw)); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
	}
	var archives, primaries, mirrors, defs int
	db.QueryRow(`SELECT COUNT(*) FROM contacts WHERE status='archived' AND deleted_at IS NULL`).Scan(&archives)
	db.QueryRow(`SELECT COUNT(*) FROM contact_channels WHERE contact_id=3 AND is_primary=1`).Scan(&primaries)
	db.QueryRow(`SELECT COUNT(*) FROM contacts WHERE status='merged' AND primary_email IS NOT NULL`).Scan(&mirrors)
	db.QueryRow(`SELECT COUNT(*) FROM contact_attribute_defs WHERE project_id='p'`).Scan(&defs)
	if archives != 1 || primaries != 1 || mirrors != 0 || defs != 10 {
		t.Fatalf("archives=%d primaries=%d mirrors=%d defs=%d", archives, primaries, mirrors, defs)
	}
}

func TestDoNotContactBlocksSendAndAudience(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&crmRecordingPlatform{}))
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "dnc@example.test"}}})
	if err := dbSetAttribute(ctx.AppDB(), "test-proj", c.ID, "do_not_contact", true, "human"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).sendMessageImpl(ctx, map[string]any{"id": c.ID, "channel": "email", "body": "hello", "subject": "test", "from": "me@example.test"}, false); err == nil {
		t.Fatal("do_not_contact send accepted")
	}
	source, err := buildAudienceSource(ctx.AppDB(), "test-proj", map[string]any{"contact_id": c.ID})
	if err != nil {
		t.Fatal(err)
	}
	audience, err := resolveAudience(ctx.AppDB(), source, "test-proj", "email", 0, 100, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(audience.Recipients) != 0 || audience.ExcludedByReason["do_not_contact"] != 1 {
		t.Fatalf("audience=%+v", audience)
	}
}

func BenchmarkIndexedSuppressions(b *testing.B) {
	items := make([]messagingSuppression, 10000)
	for i := range items {
		items[i] = messagingSuppression{Channel: "email", Kind: "address", Address: fmt.Sprintf("u%d@example.test", i)}
	}
	index := indexSuppressions(items)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		index.match("email", "missing@example.test")
	}
}

func TestAutomatedInboundReviewPolicy(t *testing.T) {
	for _, policy := range []string{"ignore_new", "review_new"} {
		t.Run(policy, func(t *testing.T) {
			ctx := newTestCtx(t, tk.WithConfig(map[string]string{"automated_inbound_policy": policy}))
			result, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "noreply@example.test", MessageID: 771, BodyText: "Your receipt"})
			if err != nil {
				t.Fatal(err)
			}
			if policy == "ignore_new" {
				if result["ignored"] != true {
					t.Fatalf("expected ignored: %v", result)
				}
				return
			}
			var n int
			if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_tags WHERE tag_name='automated'`).Scan(&n); err != nil || n != 1 {
				t.Fatalf("review tag count=%d err=%v", n, err)
			}
			if result["activity_id"] == nil {
				t.Fatalf("history not retained: %v", result)
			}
		})
	}
}

func TestRecoveryPreservesSuppressionAndRejectsOldEvents(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&suppressionListPlatform{}))
	a := &App{}
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "recover@example.test", "is_primary": true}}})
	id := c.Channels[0].ID
	if err := a.handleMessagingDeliveryEvent(ctx, deliveryEvent("hard", "bounced", "recover@example.test", "2026-08-26T10:00:00Z", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_channel_delivery_state SET suppressed=1 WHERE channel_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := recoverChannelDelivery(ctx, "other-project", id, "email", "checked"); err == nil {
		t.Fatal("foreign recovery accepted")
	}
	if err := recoverChannelDelivery(ctx, "test-proj", id, "email", "verified corrected mailbox"); err != nil {
		t.Fatal(err)
	}
	state := emailDeliveryState(t, ctx, id)
	if state.Status != "active" || !state.Suppressed || state.Messageable {
		t.Fatalf("recovery removed suppression: %+v", state)
	}
	if err := a.handleMessagingDeliveryEvent(ctx, deliveryEvent("old-hard", "bounced", "recover@example.test", "2026-08-26T10:01:00Z", true)); err != nil {
		t.Fatal(err)
	}
	if state = emailDeliveryState(t, ctx, id); state.Status != "active" {
		t.Fatalf("stale evidence replaced recovery: %+v", state)
	}
	var n int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_delivery_recoveries WHERE channel_id=?`, id).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit=%d err=%v", n, err)
	}
}

func TestUnchangedSuppressionSnapshotDoesNotRewrite(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&suppressionListPlatform{}))
	a := &App{}
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "snapshot@example.test", "is_primary": true}}})
	if err := a.reconcileProjectSuppressions(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_rewrite BEFORE UPDATE ON contact_channel_delivery_state BEGIN SELECT RAISE(ABORT,'unchanged state rewritten'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.reconcileProjectSuppressions(ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if !emailDeliveryState(t, ctx, c.Channels[0].ID).Messageable {
		t.Fatal("unexpected blocked route")
	}
}

func TestOpportunityRejectsInvalidValuesAndDates(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Validation"})
	a := &App{}
	for _, extra := range []map[string]any{{"value": "not-a-number"}, {"expected_close_date": "tomorrow"}, {"closed_at": "yesterday"}} {
		extra["contact_id"] = c.ID
		if _, err := a.toolOpportunitiesCreate(ctx, extra); err == nil {
			t.Fatalf("invalid create accepted: %v", extra)
		}
	}
}

func TestEnrichmentCannotCrossMessageSources(t *testing.T) {
	pf := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	current := &Activity{MessagingID: 123, SourceDetail: `{"source_install_id":42}`}
	foreign := &Activity{MessagingID: 123, SourceDetail: `{"source_install_id":84}`}
	legacy := &Activity{MessagingID: 123}
	enrichActivitiesWithMessagingStatus(ctx, "test-proj", []*Activity{foreign, current, legacy})
	if current.MessageStatus == nil || foreign.MessageStatus != nil || legacy.MessageStatus != nil {
		t.Fatalf("source isolation: current=%v foreign=%v legacy=%v", current.MessageStatus, foreign.MessageStatus, legacy.MessageStatus)
	}
	// A repeated refresh shares the short-lived result.
	enrichActivitiesWithMessagingStatus(ctx, "test-proj", []*Activity{current})
	if len(pf.calls) != 1 {
		t.Fatalf("cache missed: %d calls", len(pf.calls))
	}
}

func TestRapidChannelEditsAdvanceRevision(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"first_name": "Revision"})
	var previous string
	for i := 0; i < 5; i++ {
		var err error
		c, err = dbUpdate(ctx.AppDB(), "test-proj", c.ID, map[string]any{"expected_updated_at": c.UpdatedAt, "channels": []any{map[string]any{"kind": "email", "value": fmt.Sprintf("revision%d@example.test", i)}}}, "test")
		if err != nil {
			t.Fatal(err)
		}
		if c.UpdatedAt == previous {
			t.Fatal("channel edit reused revision")
		}
		previous = c.UpdatedAt
	}
}
func TestMultiSelectContainsCanBeSavedAndEvaluated(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	c := mustCreate(t, ctx, map[string]any{"first_name": "Typed"})
	if _, err := dbDefineAttribute(db, "test-proj", "interests", "Interests", "multi_select", []any{"go", "react"}, false, 0); err != nil {
		t.Fatal(err)
	}
	if err := dbSetAttribute(db, "test-proj", c.ID, "interests", []any{"go"}, "test"); err != nil {
		t.Fatal(err)
	}
	segment, err := dbSegmentCreate(db, "test-proj", &Segment{Name: "Go users", Kind: "dynamic", Definition: json.RawMessage(`[{"predicate":"attribute","key":"interests","op":"contains","value":"go"}]`)})
	if err != nil {
		t.Fatal(err)
	}
	ids, _, err := dbSegmentEval(db, "test-proj", segment, 10)
	if err != nil || len(ids) != 1 || ids[0] != c.ID {
		t.Fatalf("members=%v err=%v", ids, err)
	}
}
