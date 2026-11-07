package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type publishedEvent struct {
	projectID, eventID, topic string
	data                      map[string]any
}

// busPlatform acknowledges events like the platform's event bus, or
// fails them while down is set.
type busPlatform struct {
	*recordingPlatform
	mu        sync.Mutex
	down      bool
	published []publishedEvent
}

func (p *busPlatform) PutAppEventSubscription(sdk.AppEventSubscription) error { return nil }
func (p *busPlatform) DeleteAppEventSubscription(string, string) error       { return nil }
func (p *busPlatform) ListAppEventSources(string) ([]sdk.AppEventSource, error) {
	return nil, nil
}
func (p *busPlatform) PublishAppEvent(projectID, eventID, topic string, data any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.down {
		return errors.New("event gateway HTTP 503")
	}
	payload, _ := data.(map[string]any)
	p.published = append(p.published, publishedEvent{projectID, eventID, topic, payload})
	return nil
}

func (p *busPlatform) setDown(down bool) {
	p.mu.Lock()
	p.down = down
	p.mu.Unlock()
}

func (p *busPlatform) events() []publishedEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]publishedEvent(nil), p.published...)
}

func durableFixture(t *testing.T) (*sdk.AppCtx, *busPlatform, int64) {
	t.Helper()
	pf := &busPlatform{recordingPlatform: newRecordingPlatform()}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithEmitter(tk.NewEmitRecorder()), tk.WithPlatform(pf))
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status) VALUES('test-proj','youtube',42,'UCone','One','active')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	// Items from now on are live, not backfill.
	inboxEventsSince(ctx.AppDB(), id)
	return ctx, pf, id
}

func pendingTopic(t *testing.T, ctx *sdk.AppCtx, table string, id int64) string {
	t.Helper()
	var topic string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(event_topic,'') FROM `+table+` WHERE id=?`, id).Scan(&topic); err != nil {
		t.Fatal(err)
	}
	return topic
}

// makeEventsDue lets the worker retry now instead of after the backoff.
func makeEventsDue(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	for _, table := range durableTables {
		if _, err := ctx.AppDB().Exec(`UPDATE ` + table.name + ` SET event_next_at=NULL WHERE event_topic IS NOT NULL`); err != nil {
			t.Fatal(err)
		}
	}
}

func liveComment(accountID int64, externalID string) inboxUpsertInput {
	return inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: accountID, Platform: "youtube", Kind: inboxKindComment, ExternalID: externalID, ExternalPostID: "vid1", Body: "hello", OccurredAt: time.Now().UTC()}
}

func TestInboxEventIsPublishedWithAStableIDAndCleared(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	id, inserted, err := recordInboxItem(ctx, liveComment(accountID, "c1"))
	if err != nil || !inserted {
		t.Fatalf("record: %v %v", inserted, err)
	}
	events := pf.events()
	if len(events) != 1 || events[0].topic != inboxEventReceived || events[0].eventID != durableEventID(durableInboxItems, id, 1) || events[0].projectID != "test-proj" {
		t.Fatalf("published %+v", events)
	}
	if events[0].data["inbox_item_id"] != float64(id) {
		t.Fatalf("payload %+v", events[0].data)
	}
	if topic := pendingTopic(t, ctx, "inbox_items", id); topic != "" {
		t.Fatalf("acknowledged event still pending: %q", topic)
	}
	// Re-polling the same comment queues nothing new.
	if _, inserted, _ := recordInboxItem(ctx, liveComment(accountID, "c1")); inserted {
		t.Fatal("re-poll inserted again")
	}
	if err := publishPendingEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(pf.events()) != 1 {
		t.Fatalf("re-poll published again: %+v", pf.events())
	}
}

func TestFailedPublishIsRetriedUnderTheSameID(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	pf.setDown(true)
	id, _, err := recordInboxItem(ctx, liveComment(accountID, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	if topic := pendingTopic(t, ctx, "inbox_items", id); topic != inboxEventReceived {
		t.Fatalf("unacknowledged event not kept: %q", topic)
	}
	var attempts int
	var next string
	_ = ctx.AppDB().QueryRow(`SELECT event_attempts, COALESCE(event_next_at,'') FROM inbox_items WHERE id=?`, id).Scan(&attempts, &next)
	if attempts != 1 || next == "" {
		t.Fatalf("failure not backed off: attempts=%d next=%q", attempts, next)
	}
	// Still backing off: the worker leaves it alone.
	pf.setDown(false)
	if err := publishPendingEvents(context.Background(), ctx); err != nil || len(pf.events()) != 0 {
		t.Fatalf("published during backoff: %v %+v", err, pf.events())
	}
	makeEventsDue(t, ctx)
	if err := publishPendingEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	events := pf.events()
	if len(events) != 1 || events[0].eventID != durableEventID(durableInboxItems, id, 1) {
		t.Fatalf("retry %+v", events)
	}
	if topic := pendingTopic(t, ctx, "inbox_items", id); topic != "" {
		t.Fatalf("still pending after ack: %q", topic)
	}
}

func TestEventQueuedBeforeACrashIsPublishedByTheWorker(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	// The row and its event committed, then the process stopped before
	// publishing: only the database knows about the event.
	id, _, err := upsertInboxItem(ctx.AppDB(), liveComment(accountID, "c1"))
	if err != nil {
		t.Fatal(err)
	}
	topic, payload := queuedEvent(inboxEventReceived, map[string]any{"inbox_item_id": id})
	if _, err := ctx.AppDB().Exec(`UPDATE inbox_items SET `+queueEventSQL+` WHERE id=?`, topic, payload, id); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).runEventPublisher(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	events := pf.events()
	if len(events) != 1 || events[0].topic != inboxEventReceived || events[0].eventID != durableEventID(durableInboxItems, id, 1) {
		t.Fatalf("worker published %+v", events)
	}
}

func TestLostAcknowledgmentResendsTheSameID(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	id, _, _ := upsertInboxItem(ctx.AppDB(), liveComment(accountID, "c1"))
	topic, payload := queuedEvent(inboxEventReceived, map[string]any{"inbox_item_id": id})
	_, _ = ctx.AppDB().Exec(`UPDATE inbox_items SET `+queueEventSQL+` WHERE id=?`, topic, payload, id)
	events, _ := loadPendingEvents(ctx.AppDB(), durableInboxItems, "id=?", id)
	// The platform accepted the event but the answer never arrived.
	_ = pf.PublishAppEvent("test-proj", durableEventID(durableInboxItems, id, events[0].seq), topic, map[string]any{})
	publishRowEvent(ctx, durableInboxItems, id)
	got := pf.events()
	if len(got) != 2 || got[0].eventID != got[1].eventID {
		t.Fatalf("resend must reuse the event ID so the platform drops it: %+v", got)
	}
}

func TestNewerEventIsNotClearedByAnOlderAck(t *testing.T) {
	ctx, _, accountID := durableFixture(t)
	id, _, _ := upsertInboxItem(ctx.AppDB(), liveComment(accountID, "c1"))
	queue := func(topic string) {
		t.Helper()
		topic, payload := queuedEvent(topic, map[string]any{"inbox_item_id": id})
		if _, err := ctx.AppDB().Exec(`UPDATE inbox_items SET `+queueEventSQL+` WHERE id=?`, topic, payload, id); err != nil {
			t.Fatal(err)
		}
	}
	queue(inboxEventReceived)
	older, _ := loadPendingEvents(ctx.AppDB(), durableInboxItems, "id=?", id)
	queue(inboxEventHumanReply)
	if err := deliverPendingEvent(ctx, older[0]); err != nil {
		t.Fatal(err)
	}
	if topic := pendingTopic(t, ctx, "inbox_items", id); topic != inboxEventHumanReply {
		t.Fatalf("newer event lost: %q", topic)
	}
}

func TestTargetFailureEventIsDurable(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	res, err := ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES('test-proj','hi','publishing')`)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := res.LastInsertId()
	res, err = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status) VALUES(?,?,'publishing')`, postID, accountID)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := res.LastInsertId()
	pf.setDown(true)
	(&App{}).markTargetFailure(ctx, targetID, "boom", publishFailure{Code: "upstream_error"})
	if topic := pendingTopic(t, ctx, "post_targets", targetID); topic != "target.failed" {
		t.Fatalf("failure event not kept: %q", topic)
	}
	pf.setDown(false)
	makeEventsDue(t, ctx)
	if err := publishPendingEvents(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	events := pf.events()
	if len(events) != 1 || events[0].topic != "target.failed" || events[0].projectID != "test-proj" || events[0].data["target_id"] != float64(targetID) {
		t.Fatalf("published %+v", events)
	}
}

func TestReconciledSendEventCommitsWithTheReply(t *testing.T) {
	ctx, pf, accountID := durableFixture(t)
	parentID, _, _ := upsertInboxItem(ctx.AppDB(), liveComment(accountID, "parent"))
	res, err := ctx.AppDB().Exec(`INSERT INTO inbox_send_intents(project_id,inbox_item_id,social_account_id,platform,kind,target_external_id,target_root_id,target_thread_id,request_hash,body_text,state)
		VALUES('test-proj',?,?,'youtube','comment','parent','parent','vid1','h',?,'uncertain')`, parentID, accountID, normalizeInboxText("thanks"))
	if err != nil {
		t.Fatal(err)
	}
	sendID, _ := res.LastInsertId()
	reply := liveComment(accountID, "reply")
	reply.Outbound, reply.ParentExternalID, reply.Body = true, "parent", "thanks"
	if _, _, err := recordInboxItem(ctx, reply); err != nil {
		t.Fatal(err)
	}
	var state string
	_ = ctx.AppDB().QueryRow(`SELECT state FROM inbox_send_intents WHERE id=?`, sendID).Scan(&state)
	events := pf.events()
	if state != inboxSendSent || len(events) != 1 || events[0].topic != inboxEventReplySent || events[0].eventID != durableEventID(durableInboxSends, sendID, 1) {
		t.Fatalf("state=%s events=%+v", state, events)
	}
}
