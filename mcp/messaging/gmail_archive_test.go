package main

import (
	"encoding/json"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func archiveTestCtx(t *testing.T) (*stubPlatform, *sdk.AppCtx) {
	t.Helper()
	platform := gmailTestPlatform()
	platform.replyByTool["modify_thread"] = gmailTestReply(map[string]any{"id": "ok"})
	ctx := newTestCtx(t, platform)
	if _, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	return platform, ctx
}

func insertArchiveMessage(t *testing.T, ctx *sdk.AppCtx, pid, slug string, conn int64, thread, at string) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_slug,provider_connection_id,provider_thread_id,created_at)
		VALUES(?,'email','in','customer@example.org','received',?,?,?,?)`, pid, slug, conn, thread, at)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func modifyThreadCalls(p *stubPlatform) []executeCall {
	var out []executeCall
	for _, c := range p.executeCalls {
		if c.Tool == "modify_thread" {
			out = append(out, c)
		}
	}
	return out
}

func runArchive(t *testing.T, ctx *sdk.AppCtx, ids ...int64) *gmailArchiveResult {
	t.Helper()
	list := []any{}
	for _, id := range ids {
		list = append(list, float64(id))
	}
	out, err := (&App{}).toolMessageThreadArchive(ctx, map[string]any{"message_ids": list})
	if err != nil {
		t.Fatal(err)
	}
	return out.(*gmailArchiveResult)
}

func TestMessageThreadArchive_RemovesInboxFromEachGmailThreadOnce(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	a1 := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-a", "2026-11-20T10:00:00Z")
	a2 := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-a", "2026-11-21T10:00:00Z")
	b := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-b", "2026-11-22T10:00:00Z")
	ses := insertArchiveMessage(t, ctx, "test-proj", "aws-ses", 1, "", "2026-11-22T11:00:00Z")

	got := runArchive(t, ctx, a1, a2, b, ses)
	if len(got.Archived) != 2 || got.Skipped != 1 || len(got.Failed) != 0 || got.Remaining != 0 {
		t.Fatalf("result=%+v", got)
	}
	calls := modifyThreadCalls(platform)
	if len(calls) != 2 {
		t.Fatalf("modify_thread calls=%+v", calls)
	}
	// Most recent thread first.
	if calls[0].Input["threadId"] != "thread-b" || calls[1].Input["threadId"] != "thread-a" {
		t.Fatalf("order=%v,%v", calls[0].Input["threadId"], calls[1].Input["threadId"])
	}
	for _, c := range calls {
		if c.ConnID != 3 {
			t.Fatalf("connection=%d", c.ConnID)
		}
		labels, _ := json.Marshal(c.Input["removeLabelIds"])
		if string(labels) != `["INBOX"]` || c.Input["addLabelIds"] != nil {
			t.Fatalf("labels=%s add=%v", labels, c.Input["addLabelIds"])
		}
	}
}

func TestMessageThreadArchive_SkipsOtherProjectsAndUnownedConnections(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	other := insertArchiveMessage(t, ctx, "other-proj", "gmail", 3, "thread-x", "2026-11-22T10:00:00Z")
	// Connection 9 is not bound to Messaging and has no sender in this project.
	unbound := insertArchiveMessage(t, ctx, "test-proj", "gmail", 9, "thread-y", "2026-11-22T10:00:00Z")
	got := runArchive(t, ctx, other, unbound, 999999)
	if len(got.Archived) != 0 || got.Skipped != 3 {
		t.Fatalf("result=%+v", got)
	}
	if calls := modifyThreadCalls(platform); len(calls) != 0 {
		t.Fatalf("must not touch Gmail: %+v", calls)
	}
}

func TestMessageThreadArchive_DeletedSenderStopsArchiving(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	id := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-a", "2026-11-22T10:00:00Z")
	if _, err := ctx.AppDB().Exec(`UPDATE senders SET deleted_at=CURRENT_TIMESTAMP WHERE project_id='test-proj' AND provider='gmail'`); err != nil {
		t.Fatal(err)
	}
	got := runArchive(t, ctx, id)
	if len(got.Archived) != 0 || got.Skipped != 1 || len(modifyThreadCalls(platform)) != 0 {
		t.Fatalf("result=%+v", got)
	}
}

func TestMessageThreadArchive_ReportsGmailFailures(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	platform.replyByTool["modify_thread"] = &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":{"message":"Requested entity was not found."}}`)}
	id := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-gone", "2026-11-22T10:00:00Z")
	got := runArchive(t, ctx, id)
	if len(got.Archived) != 0 || len(got.Failed) != 1 || got.Failed[0].ThreadID != "thread-gone" || got.Failed[0].Error == "" {
		t.Fatalf("result=%+v", got)
	}
}

func TestMessageThreadArchive_CapsThreadsPerCall(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	var ids []int64
	for i := 0; i < maxArchiveThreads+5; i++ {
		ids = append(ids, insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, fmt.Sprintf("thread-%03d", i), fmt.Sprintf("2026-11-%02dT10:00:00Z", 1+i%28)))
	}
	got := runArchive(t, ctx, ids...)
	if len(got.Archived) != maxArchiveThreads || got.Remaining != 5 || len(modifyThreadCalls(platform)) != maxArchiveThreads {
		t.Fatalf("archived=%d remaining=%d", len(got.Archived), got.Remaining)
	}
}

func TestMessageThreadArchive_ValidatesInput(t *testing.T) {
	_, ctx := archiveTestCtx(t)
	for _, args := range []map[string]any{
		{},
		{"message_ids": []any{}},
		{"message_ids": []any{"abc"}},
		{"message_ids": []any{float64(-1)}},
		{"message_ids": "1,2"},
	} {
		if _, err := (&App{}).toolMessageThreadArchive(ctx, args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestMessageThreadArchive_MarkReadRemovesUnreadOnly(t *testing.T) {
	platform, ctx := archiveTestCtx(t)
	id := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-a", "2026-11-22T10:00:00Z")
	out, err := (&App{}).toolMessageThreadArchive(ctx, map[string]any{"message_ids": []any{float64(id)}, "action": "mark_read"})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(*gmailArchiveResult)
	if got.Action != "mark_read" || len(got.Archived) != 1 {
		t.Fatalf("result=%+v", got)
	}
	calls := modifyThreadCalls(platform)
	labels, _ := json.Marshal(calls[0].Input["removeLabelIds"])
	if len(calls) != 1 || string(labels) != `["UNREAD"]` {
		t.Fatalf("mark_read must remove UNREAD only, got %s", labels)
	}
}

func TestMessageThreadArchive_DefaultActionIsArchive(t *testing.T) {
	_, ctx := archiveTestCtx(t)
	id := insertArchiveMessage(t, ctx, "test-proj", "gmail", 3, "thread-a", "2026-11-22T10:00:00Z")
	if got := runArchive(t, ctx, id); got.Action != "archive" {
		t.Fatalf("action=%q", got.Action)
	}
	if _, err := (&App{}).toolMessageThreadArchive(ctx, map[string]any{"message_ids": []any{float64(id)}, "action": "delete"}); err == nil {
		t.Fatal("unknown action must be rejected")
	}
}
