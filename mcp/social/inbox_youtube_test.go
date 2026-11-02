package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func youtubeInboxFixture(t *testing.T) (*App, *sdk.AppCtx, *recordingPlatform, *tk.EmitRecorder, int64) {
	t.Helper()
	pf := newRecordingPlatform()
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithEmitter(rec), tk.WithPlatform(pf))
	globalCtx = ctx
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status) VALUES('test-proj','youtube',42,'UCone','One','active')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return &App{}, ctx, pf, rec, id
}

func ytOK(body any) *sdk.ExecuteResult {
	raw, _ := json.Marshal(body)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
}

func ytFail(status int, reason string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: false, Status: status, Data: json.RawMessage(fmt.Sprintf(`{"error":{"code":%d,"errors":[{"reason":%q}]}}`, status, reason))}
}

func ytComment(id, parent, author, text string, at time.Time) map[string]any {
	snippet := map[string]any{"textOriginal": text, "authorDisplayName": "@" + author, "authorChannelId": map[string]any{"value": author}, "publishedAt": at.UTC().Format(time.RFC3339), "videoId": "vid1"}
	if parent != "" {
		snippet["parentId"] = parent
	}
	return map[string]any{"id": id, "snippet": snippet}
}

func ytThread(top map[string]any, total int, replies ...map[string]any) map[string]any {
	thread := map[string]any{"id": top["id"], "snippet": map[string]any{"videoId": "vid1", "totalReplyCount": total, "topLevelComment": top}}
	if len(replies) > 0 {
		thread["replies"] = map[string]any{"comments": replies}
	}
	return thread
}

func ytPage(next string, threads ...map[string]any) *sdk.ExecuteResult {
	body := map[string]any{"items": threads}
	if next != "" {
		body["nextPageToken"] = next
	}
	return ytOK(body)
}

func ytSync(t *testing.T, a *App, ctx *sdk.AppCtx, id int64, scheduled bool) map[string]any {
	t.Helper()
	args := map[string]any{"social_account_ids": []any{id}}
	if scheduled {
		args[inboxScheduledArg] = true
	}
	out, err := a.toolInboxSync(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	var body map[string]any
	_ = json.Unmarshal(raw, &body)
	return body["results"].([]any)[0].(map[string]any)
}

func topics(rec *tk.EmitRecorder, prefix string) []tk.EmittedEvent {
	var out []tk.EmittedEvent
	for _, e := range rec.Events() {
		if strings.HasPrefix(e.Topic, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func eventsNamed(rec *tk.EmitRecorder, topic string) []map[string]any {
	var out []map[string]any
	for _, e := range rec.Events() {
		if e.Topic == topic {
			out = append(out, e.Data.(map[string]any))
		}
	}
	return out
}

func toolCalls(pf *recordingPlatform, tool string) []executeCall {
	var out []executeCall
	for _, c := range pf.executeCalls {
		if c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

func inboxRow(t *testing.T, ctx *sdk.AppCtx, externalID string) (parent, status string) {
	t.Helper()
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(parent_external_id,''), status FROM inbox_items WHERE external_id=?`, externalID).Scan(&parent, &status); err != nil {
		t.Fatalf("item %s: %v", externalID, err)
	}
	return parent, status
}

func TestYouTubeSyncBackfillsQuietlyThenEmitsNewItemsOnce(t *testing.T) {
	a, ctx, pf, rec, id := youtubeInboxFixture(t)
	old := time.Now().Add(-30 * 24 * time.Hour)
	// Thread A embeds one of three replies: the complete list is fetched.
	threadA := ytThread(ytComment("A", "", "UCviewer", "First!", old), 3, ytComment("A.1", "A", "UCviewer2", "agree", old))
	threadB := ytThread(ytComment("B", "", "UCone", "Pinned note from the channel", old), 0)
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", threadA, threadB), ytPage("")}
	pf.executeQueues["list_comment_replies"] = []*sdk.ExecuteResult{
		ytOK(map[string]any{"items": []any{ytComment("A.1", "A", "UCviewer2", "agree", old), ytComment("A.2", "A", "UCviewer3", "same", old)}, "nextPageToken": "r2"}),
		ytOK(map[string]any{"items": []any{ytComment("A.3", "A", "UCone", "thanks all", old)}}),
	}
	result := ytSync(t, a, ctx, id, false)
	if result["status"] != "ok" || result["coverage"] != coverageComplete {
		t.Fatalf("first sync = %+v", result)
	}
	if calls := toolCalls(pf, "list_comment_replies"); len(calls) != 2 || calls[0].Input["parentId"] != "A" || calls[1].Input["pageToken"] != "r2" {
		t.Fatalf("reply pages = %+v", calls)
	}
	if first := toolCalls(pf, "list_channel_comment_threads")[0].Input; first["allThreadsRelatedToChannelId"] != "UCone" || first["order"] != "time" {
		t.Fatalf("thread query = %+v", first)
	}
	for _, ext := range []string{"A", "A.1", "A.2", "A.3", "B"} {
		inboxRow(t, ctx, ext)
	}
	if parent, _ := inboxRow(t, ctx, "A.3"); parent != "A" {
		t.Fatalf("reply parent = %q", parent)
	}
	// Old comments are backfill: one summary, no per-item triggers.
	if got := eventsNamed(rec, inboxEventReceived); len(got) != 0 {
		t.Fatalf("backfill emitted items: %+v", got)
	}
	if got := eventsNamed(rec, inboxEventHumanReply); len(got) != 0 {
		t.Fatalf("backfilled owner reply emitted: %+v", got)
	}
	synced := eventsNamed(rec, inboxEventSynced)
	if len(synced) != 1 || synced[0]["backfilled"] != 5 || synced[0]["received"] != 0 {
		t.Fatalf("synced = %+v", synced)
	}

	// A new thread arrives; the old ones are seen again.
	now := time.Now()
	threadC := ytThread(ytComment("C", "", "UCviewer4", "When is part 2?", now), 0)
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", threadC, threadA, threadB), ytPage("")}
	pf.executeQueues["list_comment_replies"] = nil
	ytSync(t, a, ctx, id, false)
	received := eventsNamed(rec, inboxEventReceived)
	if len(received) != 1 || received[0]["external_id"] != "C" || received[0]["platform"] != "youtube" || received[0]["thread_id"] != "vid1" {
		t.Fatalf("received = %+v", received)
	}
	if _, leaked := received[0]["body"]; leaked {
		t.Fatal("event payload carries the message body")
	}
	// Re-polling the same data emits nothing new.
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", threadC, threadA, threadB), ytPage("")}
	before := len(rec.Events())
	ytSync(t, a, ctx, id, false)
	if extra := rec.Events()[before:]; len(extra) != 0 {
		t.Fatalf("re-poll emitted %+v", extra)
	}

	// The channel answers in YouTube Studio: a human intervention.
	studioReply := ytComment("C.1", "C", "UCone", "Next week!", time.Now())
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", ytThread(ytComment("C", "", "UCviewer4", "When is part 2?", now), 1, studioReply)), ytPage("")}
	ytSync(t, a, ctx, id, false)
	human := eventsNamed(rec, inboxEventHumanReply)
	if len(human) != 1 || human[0]["external_id"] != "C.1" || human[0]["parent_external_id"] != "C" {
		t.Fatalf("human reply = %+v", human)
	}
	if _, status := inboxRow(t, ctx, "C"); status != inboxStatusReplied {
		t.Fatalf("answered comment status = %q", status)
	}
}

func TestYouTubeOlderThreadsWithNewRepliesArePickedUp(t *testing.T) {
	a, ctx, pf, rec, id := youtubeInboxFixture(t)
	old := time.Now().Add(-90 * 24 * time.Hour)
	recent := time.Now().Add(-time.Minute)
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", ytThread(ytComment("N", "", "UCv", "new", recent), 0)), ytPage("")}
	ytSync(t, a, ctx, id, false)
	// Next sync: the head reaches what was already seen, and the rolling
	// pass finds an old thread that got a new reply.
	oldThread := ytThread(ytComment("O", "", "UCv", "old question", old), 2, ytComment("O.1", "O", "UCv2", "old answer", old))
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{
		ytPage("more", ytThread(ytComment("X", "", "UCv", "older", old), 0)),
		ytPage("", oldThread),
	}
	pf.executeQueues["list_comment_replies"] = []*sdk.ExecuteResult{ytOK(map[string]any{"items": []any{ytComment("O.1", "O", "UCv2", "old answer", old), ytComment("O.2", "O", "UCv3", "still relevant?", time.Now())}})}
	result := ytSync(t, a, ctx, id, false)
	if result["status"] != "ok" {
		t.Fatalf("sync = %+v", result)
	}
	if calls := toolCalls(pf, "list_channel_comment_threads"); len(calls) != 4 {
		t.Fatalf("head did not stop at seen threads: %d calls", len(calls))
	}
	var ids []any
	for _, e := range eventsNamed(rec, inboxEventReceived) {
		ids = append(ids, e["external_id"])
	}
	if fmt.Sprint(ids) != "[N O.2]" {
		t.Fatalf("received = %v", ids)
	}
}

func TestYouTubePermissionAndCatalogFailuresReportCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		res        *sdk.ExecuteResult
	}{
		{"scope missing", coverageNeedsReconnect, ytFail(403, "insufficientPermissions")},
		{"old catalog", coverageNeedsUpdate, &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":"tool not found: list_channel_comment_threads"}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, ctx, pf, rec, id := youtubeInboxFixture(t)
			pf.executeResponses["list_channel_comment_threads"] = tc.res
			result := ytSync(t, a, ctx, id, false)
			if result["coverage"] != tc.want {
				t.Fatalf("sync = %+v", result)
			}
			changed := eventsNamed(rec, inboxEventCoverage)
			if len(changed) != 1 || changed[0]["coverage"] != tc.want {
				t.Fatalf("coverage events = %+v", changed)
			}
			out, err := a.toolInboxCoverage(ctx, map[string]any{"social_account_ids": []any{id}})
			if err != nil {
				t.Fatal(err)
			}
			cov := out.(map[string]any)["accounts"].([]*inboxAccountCoverage)[0]
			if cov.Coverage != tc.want || len(cov.Missing) == 0 || cov.LastError == "" || cov.LastSuccessAt != "" || !cov.Supports["comments_write"] || cov.Supports["dm_read"] {
				t.Fatalf("coverage = %+v", cov)
			}
			// Recovery emits once more, and records the success.
			pf.executeResponses["list_channel_comment_threads"] = ytPage("")
			ytSync(t, a, ctx, id, false)
			if changed := eventsNamed(rec, inboxEventCoverage); len(changed) != 2 || changed[1]["coverage"] != coverageComplete {
				t.Fatalf("recovery events = %+v", changed)
			}
			cov, _ = loadInboxCoverage(ctx.AppDB(), "test-proj", id)
			if cov.LastSuccessAt == "" {
				t.Fatal("last success not recorded")
			}
		})
	}
}

func TestYouTubeQuotaPausesAndScheduledPollsAreThrottled(t *testing.T) {
	a, ctx, pf, _, id := youtubeInboxFixture(t)
	pf.executeResponses["list_channel_comment_threads"] = ytFail(403, "quotaExceeded")
	if result := ytSync(t, a, ctx, id, false); result["status"] != "partial" {
		t.Fatalf("quota sync = %+v", result)
	}
	calls := len(toolCalls(pf, "list_channel_comment_threads"))
	ytSync(t, a, ctx, id, false)
	if len(toolCalls(pf, "list_channel_comment_threads")) != calls {
		t.Fatal("polling continued after quotaExceeded")
	}

	b, ctx2, pf2, _, id2 := youtubeInboxFixture(t)
	pf2.executeResponses["list_channel_comment_threads"] = ytPage("")
	ytSync(t, b, ctx2, id2, true)
	calls = len(toolCalls(pf2, "list_channel_comment_threads"))
	if result := ytSync(t, b, ctx2, id2, true); result["status"] != "skipped" {
		t.Fatalf("scheduled re-poll = %+v", result)
	}
	if len(toolCalls(pf2, "list_channel_comment_threads")) != calls {
		t.Fatal("scheduled poll ran inside the interval")
	}
	ytSync(t, b, ctx2, id2, false)
	if len(toolCalls(pf2, "list_channel_comment_threads")) == calls {
		t.Fatal("manual inbox_sync was throttled")
	}
}

func seedYouTubeComment(t *testing.T, ctx *sdk.AppCtx, accountID int64, externalID, parent, author string) int64 {
	t.Helper()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: accountID, Platform: "youtube", Kind: inboxKindComment, ExternalID: externalID, ParentExternalID: parent, ExternalPostID: "vid1", AuthorExternalID: author, Body: "question"})
	if err != nil {
		t.Fatal(err)
	}
	return itemID
}

func reply(t *testing.T, a *App, ctx *sdk.AppCtx, args map[string]any) inboxOutcome {
	t.Helper()
	out, err := a.toolInboxReply(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	outcome, ok := out.(inboxOutcome)
	if !ok {
		t.Fatalf("unexpected result %#v", out)
	}
	return outcome
}

func TestYouTubeReplyGoesUnderTheTopLevelCommentWithAReceipt(t *testing.T) {
	a, ctx, pf, rec, id := youtubeInboxFixture(t)
	seedYouTubeComment(t, ctx, id, "T", "", "UCv")
	itemID := seedYouTubeComment(t, ctx, id, "T.1", "T", "UCv2")
	pf.executeResponses["reply_to_comment"] = ytOK(ytComment("T.9", "T", "UCone", "Thanks!", time.Now()))
	out := reply(t, a, ctx, map[string]any{"id": itemID, "body": "Thanks!"})
	if out.Status != "ok" || out.ExternalID != "T.9" || out.SendID == 0 || out.SendState != inboxSendSent || !strings.Contains(out.Permalink, "lc=T.9") {
		t.Fatalf("outcome = %+v", out)
	}
	sent := toolCalls(pf, "reply_to_comment")[0].Input["snippet"].(map[string]any)
	if sent["parentId"] != "T" || sent["textOriginal"] != "Thanks!" {
		t.Fatalf("reply went to %+v", sent)
	}
	if len(toolCalls(pf, "post_comment")) != 0 {
		t.Fatal("reply created a new top-level comment")
	}
	if parent, _ := inboxRow(t, ctx, "T.9"); parent != "T" {
		t.Fatalf("receipt parent = %q", parent)
	}
	if _, status := inboxRow(t, ctx, "T.1"); status != inboxStatusReplied {
		t.Fatalf("answered item status = %q", status)
	}
	if events := eventsNamed(rec, inboxEventReplySent); len(events) != 1 || events[0]["external_id"] != "T.9" || events[0]["send_id"] != out.SendID {
		t.Fatalf("reply events = %+v", events)
	}
	// Sync later sees our own reply: no human-intervention event.
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", ytThread(ytComment("T", "", "UCv", "question", time.Now()), 2, ytComment("T.1", "T", "UCv2", "question", time.Now()), ytComment("T.9", "T", "UCone", "Thanks!", time.Now()))), ytPage("")}
	ytSync(t, a, ctx, id, false)
	if human := eventsNamed(rec, inboxEventHumanReply); len(human) != 0 {
		t.Fatalf("own reply flagged as human: %+v", human)
	}
}

func TestInboxReplyIdempotencyAndDuplicatePrevention(t *testing.T) {
	a, ctx, pf, _, id := youtubeInboxFixture(t)
	itemID := seedYouTubeComment(t, ctx, id, "T", "", "UCv")
	pf.executeResponses["reply_to_comment"] = ytOK(ytComment("T.9", "T", "UCone", "Thanks!", time.Now()))
	first := reply(t, a, ctx, map[string]any{"id": itemID, "body": "Thanks!", "idempotency_key": "agent-run-1"})
	again := reply(t, a, ctx, map[string]any{"id": itemID, "body": "Thanks!", "idempotency_key": "agent-run-1"})
	if !again.Replayed || again.Status != "ok" || again.ExternalID != "T.9" || again.SendID != first.SendID {
		t.Fatalf("replay = %+v", again)
	}
	if n := len(toolCalls(pf, "reply_to_comment")); n != 1 {
		t.Fatalf("idempotent retry sent again: %d calls", n)
	}
	if out, _ := a.toolInboxReply(ctx, map[string]any{"id": itemID, "body": "Different", "idempotency_key": "agent-run-1"}); !strings.Contains(fmt.Sprint(out), "already used") {
		t.Fatalf("key reuse for different reply = %#v", out)
	}
	// Without a key, the same text to the same item right after is a duplicate.
	dup := reply(t, a, ctx, map[string]any{"id": itemID, "body": "  Thanks! "})
	if dup.Status != "skipped" || !strings.Contains(dup.Reason, "duplicate") {
		t.Fatalf("duplicate = %+v", dup)
	}
	if n := len(toolCalls(pf, "reply_to_comment")); n != 1 {
		t.Fatalf("duplicate sent: %d calls", n)
	}
	// A different text is a different reply.
	if other := reply(t, a, ctx, map[string]any{"id": itemID, "body": "And welcome!"}); other.Status != "ok" {
		t.Fatalf("other = %+v", other)
	}
}

func TestInboxReplyTimeoutIsUncertainUntilSyncConfirmsIt(t *testing.T) {
	a, ctx, pf, rec, id := youtubeInboxFixture(t)
	itemID := seedYouTubeComment(t, ctx, id, "T", "", "UCv")
	pf.executeErr = errors.New("context deadline exceeded")
	out := reply(t, a, ctx, map[string]any{"id": itemID, "body": "On it", "idempotency_key": "k1"})
	pf.executeErr = nil
	if out.Status != "failed" || out.SendState != inboxSendUncertain {
		t.Fatalf("timeout = %+v", out)
	}
	if events := eventsNamed(rec, inboxEventReplyUnsure); len(events) != 1 {
		t.Fatalf("uncertain events = %+v", events)
	}
	// A retry does not send while the first attempt is unconfirmed.
	retry := reply(t, a, ctx, map[string]any{"id": itemID, "body": "On it", "idempotency_key": "k1"})
	if retry.Status != "skipped" || retry.SendState != inboxSendUncertain || len(toolCalls(pf, "reply_to_comment")) != 1 {
		t.Fatalf("retry = %+v", retry)
	}
	// Sync finds the reply upstream: the send is confirmed, not a human.
	pf.executeQueues["list_channel_comment_threads"] = []*sdk.ExecuteResult{ytPage("", ytThread(ytComment("T", "", "UCv", "question", time.Now()), 1, ytComment("T.5", "T", "UCone", "On   it", time.Now()))), ytPage("")}
	ytSync(t, a, ctx, id, false)
	if human := eventsNamed(rec, inboxEventHumanReply); len(human) != 0 {
		t.Fatalf("our uncertain send was flagged as human: %+v", human)
	}
	sent := eventsNamed(rec, inboxEventReplySent)
	if len(sent) != 1 || sent[0]["reconciled"] != true || sent[0]["external_id"] != "T.5" || sent[0]["idempotency_key"] != "k1" {
		t.Fatalf("reconciled = %+v", sent)
	}
	confirmed := reply(t, a, ctx, map[string]any{"id": itemID, "body": "On it", "idempotency_key": "k1"})
	if confirmed.Status != "ok" || confirmed.ExternalID != "T.5" || !confirmed.Replayed {
		t.Fatalf("confirmed = %+v", confirmed)
	}
	if _, status := inboxRow(t, ctx, "T"); status != inboxStatusReplied {
		t.Fatalf("item status = %q", status)
	}
}

func TestInboxReplyDefiniteFailureCanBeRetried(t *testing.T) {
	a, ctx, pf, rec, id := youtubeInboxFixture(t)
	itemID := seedYouTubeComment(t, ctx, id, "T", "", "UCv")
	pf.executeQueues["reply_to_comment"] = []*sdk.ExecuteResult{ytFail(403, "commentsDisabled"), ytOK(ytComment("T.2", "T", "UCone", "hi", time.Now()))}
	failed := reply(t, a, ctx, map[string]any{"id": itemID, "body": "hi", "idempotency_key": "k"})
	if failed.Status != "failed" || failed.SendState != inboxSendFailed || !strings.Contains(failed.Error, "disabled") {
		t.Fatalf("failed = %+v", failed)
	}
	if events := eventsNamed(rec, inboxEventReplyFailed); len(events) != 1 {
		t.Fatalf("failed events = %+v", events)
	}
	retried := reply(t, a, ctx, map[string]any{"id": itemID, "body": "hi", "idempotency_key": "k"})
	if retried.Status != "ok" || retried.SendID != failed.SendID || len(toolCalls(pf, "reply_to_comment")) != 2 {
		t.Fatalf("retry = %+v", retried)
	}
}

func TestYouTubeModeration(t *testing.T) {
	a, ctx, pf, _, id := youtubeInboxFixture(t)
	viewer := seedYouTubeComment(t, ctx, id, "V", "", "UCv")
	own := seedYouTubeComment(t, ctx, id, "V.1", "V", "UCone")
	hidden, err := a.toolInboxHide(ctx, map[string]any{"id": viewer})
	if err != nil || hidden.(inboxOutcome).Status != "ok" {
		t.Fatalf("hide = %+v %v", hidden, err)
	}
	if call := toolCalls(pf, "set_comment_moderation_status")[0].Input; call["id"] != "V" || call["moderationStatus"] != "rejected" {
		t.Fatalf("moderation = %+v", call)
	}
	deleted, _ := a.toolInboxDelete(ctx, map[string]any{"id": viewer})
	if deleted.(inboxOutcome).Status != "unsupported" || len(toolCalls(pf, "delete_comment")) != 0 {
		t.Fatalf("viewer delete = %+v", deleted)
	}
	deleted, _ = a.toolInboxDelete(ctx, map[string]any{"id": own})
	if deleted.(inboxOutcome).Status != "ok" || toolCalls(pf, "delete_comment")[0].Input["id"] != "V.1" {
		t.Fatalf("own delete = %+v", deleted)
	}
}

func TestTikTokInboxIsExplicitlyUnsupportedWithReasons(t *testing.T) {
	a, ctx, pf, _, _ := youtubeInboxFixture(t)
	res, _ := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','tiktok',7,'Tok','active')`)
	tiktok, _ := res.LastInsertId()
	result := ytSync(t, a, ctx, tiktok, false)
	if result["status"] != "unsupported" || !strings.Contains(fmt.Sprint(result["reason"]), "API for Business") || len(pf.executeCalls) != 0 {
		t.Fatalf("tiktok sync = %+v", result)
	}
	cov, err := loadInboxCoverage(ctx.AppDB(), "test-proj", tiktok)
	if err != nil || cov.Coverage != coverageUnsupported || len(cov.Missing) != 2 || cov.Supports["comments_read"] {
		t.Fatalf("tiktok coverage = %+v %v", cov, err)
	}
	if out := reply(t, a, ctx, map[string]any{"id": seedTikTokItem(t, ctx, tiktok), "body": "hi"}); out.Status != "unsupported" || len(pf.executeCalls) != 0 {
		t.Fatalf("tiktok reply = %+v", out)
	}
}

func seedTikTokItem(t *testing.T, ctx *sdk.AppCtx, accountID int64) int64 {
	t.Helper()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: accountID, Platform: "tiktok", Kind: inboxKindComment, ExternalID: "tt1"})
	if err != nil {
		t.Fatal(err)
	}
	return itemID
}

// Every poller saves through recordInboxItem, so a Facebook DM and the
// Page's own answer from the Facebook app emit like YouTube comments do.
func TestRecordInboxItemEmitsForAnyPlatform(t *testing.T) {
	_, ctx, _, rec, _ := youtubeInboxFixture(t)
	res, _ := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','facebook',9,'Page','active')`)
	page, _ := res.LastInsertId()
	resetInboxCounts(ctx.AppDB(), page)
	in := inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: page, Platform: "facebook", Kind: inboxKindDM, ExternalID: "m1", ExternalPostID: "conv1", AuthorExternalID: "psid", OccurredAt: time.Now()}
	if _, inserted, err := recordInboxItem(ctx, in); err != nil || !inserted {
		t.Fatal(err)
	}
	if _, inserted, _ := recordInboxItem(ctx, in); inserted {
		t.Fatal("re-poll inserted again")
	}
	answer := inboxUpsertInput{ProjectID: "test-proj", SocialAccountID: page, Platform: "facebook", Kind: inboxKindDM, ExternalID: "m2", ExternalPostID: "conv1", Outbound: true, OccurredAt: time.Now()}
	if _, _, err := recordInboxItem(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if got := eventsNamed(rec, inboxEventReceived); len(got) != 1 || got[0]["platform"] != "facebook" || got[0]["thread_id"] != "conv1" {
		t.Fatalf("received = %+v", got)
	}
	if got := eventsNamed(rec, inboxEventHumanReply); len(got) != 1 || got[0]["external_id"] != "m2" {
		t.Fatalf("human = %+v", got)
	}
	if counts := takeInboxCounts(ctx.AppDB(), page); counts.Received != 1 || counts.HumanReplies != 1 {
		t.Fatalf("counts = %+v", counts)
	}
	for _, e := range rec.Events() {
		if e.ProjectID != "test-proj" {
			t.Fatalf("event outside the item's project: %+v", e)
		}
	}
}
