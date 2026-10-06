package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestNativeMessagesWidgetContract(t *testing.T) {
	document, err := os.ReadFile("ui/surfaces/messages.json")
	if err != nil {
		t.Fatal(err)
	}
	surface, err := sdk.ParseNativeSurface(document)
	if err != nil {
		t.Fatalf("parse native messages surface: %v", err)
	}
	if surface.ID != "messages" || surface.Presentation != "widget" || surface.Context.Scope != sdk.NativeSurfaceContextProject {
		t.Fatalf("unexpected messages surface: %+v", surface)
	}
	if len(surface.Blocks) != 1 || surface.Blocks[0].Type != "list" {
		t.Fatalf("messages widget must contain exactly one conversation list: %+v", surface.Blocks)
	}
	if surface.Blocks[0].Empty == nil || surface.Blocks[0].Empty.Title != "No SMS or WhatsApp conversations" {
		t.Fatalf("unexpected empty state: %+v", surface.Blocks[0].Empty)
	}
	source := surface.DataSources["conversations"]
	if source.Request.Method != http.MethodGet || source.Request.Path != "/mobile/conversations" {
		t.Fatalf("unexpected conversation source: %+v", source.Request)
	}
	wantQuery := map[string]any{
		"default_channel":   "$settings.default_channel",
		"max_conversations": "$settings.max_conversations",
	}
	if !reflect.DeepEqual(source.Request.Query, wantQuery) {
		t.Fatalf("conversation query=%v want=%v", source.Request.Query, wantQuery)
	}
	if len(surface.Actions) != 0 || len(surface.Destinations) != 0 || surface.Navigation != nil {
		t.Fatalf("dashboard native widget must remain read-only: actions=%v destinations=%v navigation=%v", surface.Actions, surface.Destinations, surface.Navigation)
	}

	manifest := (&App{}).Manifest()
	widget := manifest.Provides.UIComponents[0]
	if widget.Native == nil {
		t.Fatal("native descriptor missing")
	}
	if widget.Native.Schema != surface.Schema || widget.Native.Entry != "/ui/surfaces/messages.json" {
		t.Fatalf("native descriptor mismatch: %+v", widget.Native)
	}
	if got, want := widget.RefreshTopics, []string{"message.sent", "message.received", "message.event"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("refresh topics=%v want=%v", got, want)
	}
	foundRoute := false
	for _, route := range (&App{}).HTTPRoutes() {
		if route.Pattern == "/mobile/conversations" {
			foundRoute = true
			if route.Method != http.MethodGet || route.NoAuth {
				t.Fatalf("native conversation route must be authenticated GET: %+v", route)
			}
		}
	}
	if !foundRoute {
		t.Fatal("native conversation route is not registered")
	}
}

func insertMobileMessage(t *testing.T, projectID, channel, direction, peer, body, status, at string) int64 {
	t.Helper()
	from := "+15550000000"
	to := `[` + jsonString(peer) + `]`
	receivedAt, sentAt := "", at
	if direction == "in" {
		from = peer
		to = `["+15550000000"]`
		receivedAt, sentAt = at, ""
	}
	result, err := globalCtx.AppDB().Exec(
		`INSERT INTO messages
		 (project_id, channel, direction, from_addr, to_addrs, body_text, status, created_at, sent_at, received_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
		projectID, channel, direction, from, to, body, status, at, sentAt, receivedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func jsonString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func requestMobileConversations(t *testing.T, target, projectID string) (*httptest.ResponseRecorder, mobileConversationsResponse) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, nil)
	if projectID != "" {
		request.Header.Set("X-Apteva-Project-ID", projectID)
	}
	recorder := httptest.NewRecorder()
	(&App{}).handleMobileConversations(recorder, request)
	var response mobileConversationsResponse
	if recorder.Code == http.StatusOK {
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
	}
	return recorder, response
}

func TestMobileConversationsGroupsSortsFiltersAndScopes(t *testing.T) {
	newTestCtx(t, nil)
	insertMobileMessage(t, "project-a", channelSMS, "in", "+34600000000", "Earlier", "received", "2026-09-21T07:00:00Z")
	latestSMS := insertMobileMessage(t, "project-a", channelSMS, "out", "+34600000000", "See you   tomorrow", "sent", "2026-09-21T07:45:00Z")
	insertMobileMessage(t, "project-a", channelWhatsApp, "in", "whatsapp:+34600000000", "WhatsApp hello", "received", "2026-09-21T07:30:00Z")
	insertMobileMessage(t, "project-a", channelSMS, "in", "+34611111111", "Newest", "received", "2026-09-21T08:00:00Z")
	insertMobileMessage(t, "project-a", channelEmail, "in", "person@example.com", "Email hidden", "received", "2026-09-21T09:00:00Z")
	insertMobileMessage(t, "project-b", channelSMS, "in", "+34999999999", "Project B secret", "received", "2026-09-21T10:00:00Z")
	if _, err := globalCtx.AppDB().Exec(
		`INSERT INTO delivery_events(message_id, kind, raw, occurred_at) VALUES (?, 'delivered', '{}', '2026-09-21T07:46:00Z')`, latestSMS,
	); err != nil {
		t.Fatal(err)
	}

	recorder, response := requestMobileConversations(t, "/mobile/conversations?project_id=project-b&default_channel=all&max_conversations=20", "project-a")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if len(response.Conversations) != 3 {
		t.Fatalf("conversations=%+v", response.Conversations)
	}
	if got := []string{response.Conversations[0].ID, response.Conversations[1].ID, response.Conversations[2].ID}; !reflect.DeepEqual(got, []string{
		"sms:+34611111111", "sms:+34600000000", "whatsapp:+34600000000",
	}) {
		t.Fatalf("conversation order=%v", got)
	}
	sms := response.Conversations[1]
	if sms.Preview != "You: See you tomorrow" || sms.LatestAt != "2026-09-21T07:45:00Z" || sms.Status != "delivered" || sms.Channel != "SMS" {
		t.Fatalf("unexpected latest SMS summary: %+v", sms)
	}
	if strings.Contains(recorder.Body.String(), "Earlier") || strings.Contains(recorder.Body.String(), "Project B secret") || strings.Contains(recorder.Body.String(), "attachment") {
		t.Fatalf("response leaked history, another project, or attachment data: %s", recorder.Body.String())
	}

	_, response = requestMobileConversations(t, "/mobile/conversations?default_channel=whatsapp&max_conversations=10", "project-a")
	if len(response.Conversations) != 1 || response.Conversations[0].ID != "whatsapp:+34600000000" || response.Conversations[0].Channel != "WhatsApp" {
		t.Fatalf("WhatsApp filter failed: %+v", response.Conversations)
	}
}

func TestMobileConversationsLimitClampEmptyAndMethod(t *testing.T) {
	newTestCtx(t, nil)
	for index := 0; index < 25; index++ {
		peer := fmt.Sprintf("+1555000%04d", index)
		insertMobileMessage(t, "project-a", channelSMS, "in", peer, "hello", "received", "2026-09-21T08:00:00Z")
	}
	_, response := requestMobileConversations(t, "/mobile/conversations?max_conversations=99", "project-a")
	if len(response.Conversations) != maxMobileConversationLimit {
		t.Fatalf("upper clamp returned %d conversations", len(response.Conversations))
	}
	_, response = requestMobileConversations(t, "/mobile/conversations?max_conversations=0", "project-a")
	if len(response.Conversations) != minMobileConversationLimit {
		t.Fatalf("lower clamp returned %d conversations", len(response.Conversations))
	}

	emptyRecorder, empty := requestMobileConversations(t, "/mobile/conversations", "empty-project")
	if emptyRecorder.Code != http.StatusOK || empty.Conversations == nil || len(empty.Conversations) != 0 || !strings.Contains(emptyRecorder.Body.String(), `"conversations":[]`) {
		t.Fatalf("empty response status=%d value=%+v body=%s", emptyRecorder.Code, empty, emptyRecorder.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/mobile/conversations", strings.NewReader(`{}`))
	request.Header.Set("X-Apteva-Project-ID", "project-a")
	recorder := httptest.NewRecorder()
	(&App{}).handleMobileConversations(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status=%d want=%d", recorder.Code, http.StatusMethodNotAllowed)
	}

	queryOnly, _ := requestMobileConversations(t, "/mobile/conversations?project_id=project-a", "")
	if queryOnly.Code != http.StatusBadRequest {
		t.Fatalf("query project context must not authorize native conversations: status=%d", queryOnly.Code)
	}
}
