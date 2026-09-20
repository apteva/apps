package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func mobileGET(t *testing.T, handler http.HandlerFunc, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	authorizeTestRequest(req)
	rec := httptest.NewRecorder()
	handler(rec, req)
	return rec
}

func TestMobileConversationSummaryIsOptIn(t *testing.T) {
	app, _, _ := newTestEnv(t)

	empty := mobileGET(t, app.handleChats, "/chats?view=summary")
	if empty.Code != http.StatusOK || strings.TrimSpace(empty.Body.String()) != `{"items":[]}` {
		t.Fatalf("empty summary status=%d body=%s", empty.Code, empty.Body.String())
	}

	conv := mkConversation(t, app, 41)
	summary := mobileGET(t, app.handleChats, "/chats?view=summary")
	var wrapped struct {
		Items []chatListEntry `json:"items"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &wrapped); err != nil {
		t.Fatal(err)
	}
	if len(wrapped.Items) != 1 || wrapped.Items[0].ID != conv.ID {
		t.Fatalf("summary=%s", summary.Body.String())
	}

	legacy := mobileGET(t, app.handleChats, "/chats")
	var rows []chatListEntry
	if err := json.Unmarshal(legacy.Body.Bytes(), &rows); err != nil {
		t.Fatalf("legacy chats is no longer an array: %v body=%s", err, legacy.Body.String())
	}
	if len(rows) != 1 || rows[0].ID != conv.ID {
		t.Fatalf("legacy chats=%+v", rows)
	}
}

func TestMobileMessageCursorPagesLatestRowsWithoutGaps(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	var inserted []*Message
	for _, content := range []string{"one", "two", "three", "four", "five"} {
		message, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: content})
		if err != nil {
			t.Fatal(err)
		}
		inserted = append(inserted, message)
	}

	type cursorResponse struct {
		Items      []Message `json:"items"`
		NextCursor string    `json:"next_cursor"`
	}
	read := func(target string) cursorResponse {
		t.Helper()
		rec := mobileGET(t, app.handleMessages, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
		var response cursorResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		return response
	}

	first := read("/messages?chat_id=" + conv.ID + "&pagination=cursor&limit=2")
	if len(first.Items) != 2 || first.Items[0].ID != inserted[3].ID || first.Items[1].ID != inserted[4].ID || first.NextCursor != strconv.FormatInt(inserted[3].ID, 10) {
		t.Fatalf("first page=%+v", first)
	}
	second := read("/messages?chat_id=" + conv.ID + "&pagination=cursor&limit=2&before=" + first.NextCursor)
	if len(second.Items) != 2 || second.Items[0].ID != inserted[1].ID || second.Items[1].ID != inserted[2].ID || second.NextCursor != strconv.FormatInt(inserted[1].ID, 10) {
		t.Fatalf("second page=%+v", second)
	}
	last := read("/messages?chat_id=" + conv.ID + "&pagination=cursor&limit=2&before=" + second.NextCursor)
	if len(last.Items) != 1 || last.Items[0].ID != inserted[0].ID || last.NextCursor != "" {
		t.Fatalf("last page=%+v", last)
	}

	legacy := mobileGET(t, app.handleMessages, "/messages?chat_id="+conv.ID+"&limit=2")
	var legacyRows []Message
	if err := json.Unmarshal(legacy.Body.Bytes(), &legacyRows); err != nil {
		t.Fatalf("legacy messages is no longer an array: %v body=%s", err, legacy.Body.String())
	}
}

func TestMobileSeenDefaultsToLatestVisibleMessage(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	first, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "visible"})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "latest"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "inbox only", InboxOnly: true}); err != nil {
		t.Fatal(err)
	}

	post := func(userID int64, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/seen", strings.NewReader(body))
		req.Header.Set("X-User-ID", strconv.FormatInt(userID, 10))
		req.Header.Set("X-Apteva-Project-ID", testProject)
		rec := httptest.NewRecorder()
		app.handleSeen(rec, req)
		return rec
	}
	if rec := post(1, `{"chat_id":"`+conv.ID+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("default seen status=%d body=%s", rec.Code, rec.Body.String())
	}
	var seen int64
	if err := app.store.db.QueryRow(`SELECT last_seen_id FROM read_marks WHERE user_id=1 AND conversation_id=?`, conv.ID).Scan(&seen); err != nil || seen != latest.ID {
		t.Fatalf("default last_seen_id=%d want=%d err=%v", seen, latest.ID, err)
	}

	if _, err := app.store.db.Exec(`INSERT INTO participants(conversation_id,user_id) VALUES(?,2)`, conv.ID); err != nil {
		t.Fatal(err)
	}
	if rec := post(2, `{"chat_id":"`+conv.ID+`","last_seen_id":`+strconv.FormatInt(first.ID, 10)+`}`); rec.Code != http.StatusOK {
		t.Fatalf("explicit seen status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := app.store.db.QueryRow(`SELECT last_seen_id FROM read_marks WHERE user_id=2 AND conversation_id=?`, conv.ID).Scan(&seen); err != nil || seen != first.ID {
		t.Fatalf("explicit last_seen_id=%d want=%d err=%v", seen, first.ID, err)
	}
}

type recordingSSEWriter struct {
	mu      sync.Mutex
	header  http.Header
	body    bytes.Buffer
	flushed chan struct{}
}

func newRecordingSSEWriter() *recordingSSEWriter {
	return &recordingSSEWriter{header: make(http.Header), flushed: make(chan struct{}, 8)}
}

func (w *recordingSSEWriter) Header() http.Header { return w.header }
func (w *recordingSSEWriter) WriteHeader(int)     {}
func (w *recordingSSEWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(p)
}
func (w *recordingSSEWriter) Flush() {
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}
func (w *recordingSSEWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.String()
}

func TestMobileSSEUsesNamedRevisionEventsAndReplaysUpdates(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv := mkConversation(t, app, 41)
	old, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "before"})
	if err != nil {
		t.Fatal(err)
	}
	initialRevision := old.Revision
	if _, err := app.store.db.Exec(`UPDATE messages SET content='after' WHERE id=?`, old.ID); err != nil {
		t.Fatal(err)
	}
	added, err := app.store.AppendMessage(&Message{ConversationID: conv.ID, Role: "agent", Content: "new"})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := app.store.GetMessage(old.ID)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "/stream?chat_id="+conv.ID+"&since="+strconv.FormatInt(initialRevision, 10), nil)
	authorizeTestRequest(request)
	ctx, cancel := context.WithCancel(request.Context())
	request = request.WithContext(ctx)
	writer := newRecordingSSEWriter()
	done := make(chan struct{})
	go func() {
		app.handleStream(writer, request)
		close(done)
	}()

	for i := 0; i < 2; i++ {
		select {
		case <-writer.flushed:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("stream did not flush replay")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream did not stop")
	}

	body := writer.String()
	if strings.Count(body, "event: message\n") != 2 ||
		!strings.Contains(body, "id: "+strconv.FormatInt(updated.Revision, 10)+"\n") ||
		!strings.Contains(body, "id: "+strconv.FormatInt(added.Revision, 10)+"\n") ||
		!strings.Contains(body, `"content":"after"`) || !strings.Contains(body, `"content":"new"`) {
		t.Fatalf("replay body:\n%s", body)
	}

	durable := httptest.NewRecorder()
	activity := httptest.NewRecorder()
	writeSSE(durable, *updated)
	if !strings.HasPrefix(durable.Body.String(), "event: message\nid: ") {
		t.Fatalf("durable frame=%q", durable.Body.String())
	}
	if !writeStreamSSE(activity, StreamFrame{Type: "stream", ConversationID: conv.ID, Text: "typing"}) {
		t.Fatal("stream frame did not encode")
	}
	if !strings.HasPrefix(activity.Body.String(), "event: stream\ndata: ") || strings.Contains(activity.Body.String(), "\nid:") {
		t.Fatalf("ephemeral frame=%q", activity.Body.String())
	}
}

func TestMobileCursorEndpointsPreserveConversationAuthorization(t *testing.T) {
	app, _, _ := newTestEnv(t)
	conv, err := app.store.CreateConversation(CreateConversationInput{ProjectID: "other-project", LeadAgentID: 41, Title: "Other", OwnerUserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	rec := mobileGET(t, app.handleMessages, "/messages?chat_id="+conv.ID+"&pagination=cursor&limit=50")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-project cursor status=%d body=%s", rec.Code, rec.Body.String())
	}
}
