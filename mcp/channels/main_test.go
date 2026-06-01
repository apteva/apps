package main

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	sort.Strings(files)
	for _, file := range files {
		sqlBytes, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read migration %s: %v", file, err)
		}
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			t.Fatalf("run migration %s: %v", file, err)
		}
	}
	return db
}

func TestManifestParses(t *testing.T) {
	m := (&App{}).Manifest()
	if m.Name != "channels" {
		t.Fatalf("name = %q", m.Name)
	}
	if len(m.Provides.MCPTools) != 3 {
		t.Fatalf("MCP tools = %d, want 3", len(m.Provides.MCPTools))
	}
	if m.DB == nil {
		t.Fatal("manifest missing db block")
	}
}

func TestStoreChatMessageLifecycle(t *testing.T) {
	st := newStore(testDB(t))
	chat, err := st.EnsureDefaultChat(42, "proj-1")
	if err != nil {
		t.Fatalf("EnsureDefaultChat: %v", err)
	}
	if chat.ID != "default-42" || chat.ProjectID != "proj-1" {
		t.Fatalf("chat = %+v", chat)
	}

	userID := int64(7)
	userMsg, err := st.Append(chat.ID, "user", "hello", &userID, "", "final", nil)
	if err != nil {
		t.Fatalf("append user: %v", err)
	}
	agentMsg, err := st.Append(chat.ID, "agent", "hi", nil, "main", "final", []ChatComponent{
		{App: "storage", Name: "file-card", Props: map[string]any{"file_id": float64(9)}},
	})
	if err != nil {
		t.Fatalf("append agent: %v", err)
	}
	if agentMsg.ID <= userMsg.ID {
		t.Fatalf("message IDs not increasing: user=%d agent=%d", userMsg.ID, agentMsg.ID)
	}

	rows, err := st.ListMessages(chat.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	if len(rows[1].Components) != 1 || rows[1].Components[0].App != "storage" {
		t.Fatalf("components not round-tripped: %+v", rows[1].Components)
	}

	seen, err := st.MarkSeen(chat.ID, 999)
	if err != nil {
		t.Fatalf("MarkSeen: %v", err)
	}
	if seen != agentMsg.ID {
		t.Fatalf("seen = %d, want clamp to %d", seen, agentMsg.ID)
	}
}

func TestStoreDefaultNtfyLifecycle(t *testing.T) {
	st := newStore(testDB(t))
	ch, err := st.EnsureDefaultNtfy(42, "proj-1", "agent-42-test")
	if err != nil {
		t.Fatalf("EnsureDefaultNtfy: %v", err)
	}
	if ch.Channel != "ntfy" || ch.ThreadID != "agent-42-test" {
		t.Fatalf("ntfy channel = %+v", ch)
	}
	byTopic, err := st.GetNtfyByTopic("agent-42-test")
	if err != nil {
		t.Fatalf("GetNtfyByTopic: %v", err)
	}
	if byTopic.ID != ch.ID {
		t.Fatalf("topic lookup id = %q, want %q", byTopic.ID, ch.ID)
	}
}

func TestProjectNtfyCanBeCreatedWithoutAgent(t *testing.T) {
	st := newStore(testDB(t))
	ch, err := st.UpsertNtfyChannel(0, "proj-1", "Marco Phone", "marco-phone")
	if err != nil {
		t.Fatalf("UpsertNtfyChannel: %v", err)
	}
	if ch.AgentID != 0 || ch.Title != "Marco Phone" || ch.ThreadID != "marco-phone" {
		t.Fatalf("project ntfy channel = %+v", ch)
	}
	rows, err := st.ListChannelsForAgent(42, "proj-1")
	if err != nil {
		t.Fatalf("ListChannelsForAgent: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != ch.ID {
		t.Fatalf("rows = %+v, want project channel", rows)
	}
}

func TestNtfyPublishHTTP(t *testing.T) {
	st := newStore(testDB(t))
	ch, err := st.EnsureDefaultNtfy(42, "proj-1", "agent-42-test")
	if err != nil {
		t.Fatalf("EnsureDefaultNtfy: %v", err)
	}
	app := &App{store: st, hub: newHub()}
	req := httptest.NewRequest(http.MethodPost, "/ntfy/agent-42-test", strings.NewReader("hello phone"))
	req.Header.Set("Title", "Test")
	req.Header.Set("Tags", "white_check_mark,agent")
	rec := httptest.NewRecorder()

	app.handleNtfy(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	rows, err := st.ListMessages(ch.ID, 0, 10)
	if err != nil {
		t.Fatalf("ListMessages: %v", err)
	}
	if len(rows) != 1 || rows[0].Content != "hello phone" || rows[0].Role != "user" {
		t.Fatalf("rows = %+v", rows)
	}
	meta := ntfyMetaFromMessage(rows[0])
	if meta.Title != "Test" || len(meta.Tags) != 2 {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestChannelsCreateNtfyWithoutAgent(t *testing.T) {
	st := newStore(testDB(t))
	app := &App{store: st, hub: newHub()}
	req := httptest.NewRequest(http.MethodPost, "/channels?project_id=proj-1", strings.NewReader(`{"type":"ntfy","name":"Marco Phone","topic":"marco-phone"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	app.handleChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"agent_id":0`) || !strings.Contains(rec.Body.String(), `"id":"ntfy:marco-phone"`) {
		t.Fatalf("response = %s", rec.Body.String())
	}
}

func TestChannelsDeleteByChannelID(t *testing.T) {
	st := newStore(testDB(t))
	if _, err := st.UpsertNtfyChannel(0, "proj-1", "Marco Phone", "marco-phone"); err != nil {
		t.Fatalf("UpsertNtfyChannel: %v", err)
	}
	app := &App{store: st, hub: newHub()}
	req := httptest.NewRequest(http.MethodDelete, "/channels?project_id=proj-1&channel_id=ntfy:marco-phone", nil)
	rec := httptest.NewRecorder()

	app.handleChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetNtfyByTopic("marco-phone"); !errors.Is(err, errNotFound) {
		t.Fatalf("GetNtfyByTopic err = %v, want errNotFound", err)
	}
}

func TestChannelsListProjectWithoutAgent(t *testing.T) {
	st := newStore(testDB(t))
	if _, err := st.SetNtfyTopic(42, "proj-1", "agent-42-test"); err != nil {
		t.Fatalf("SetNtfyTopic: %v", err)
	}
	app := &App{store: st, hub: newHub()}
	req := httptest.NewRequest(http.MethodGet, "/channels?project_id=proj-1", nil)
	rec := httptest.NewRecorder()

	app.handleChannels(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"id":"ntfy:agent-42-test"`) {
		t.Fatalf("response missing ntfy channel: %s", rec.Body.String())
	}
}

func TestGenericMessageEventPayload(t *testing.T) {
	ch := Chat{ID: "ntfy-marco", AgentID: 0, ProjectID: "proj-1", Title: "Marco Phone", Channel: "ntfy", ThreadID: "marco"}
	msg := Message{ID: 12, ChatID: ch.ID, Role: "agent", Content: "done", Status: "final"}

	payload := messageCreatedPayload(ch, msg)

	if payload["channel_id"] != "ntfy:marco" || payload["channel_type"] != "ntfy" {
		t.Fatalf("channel payload fields = %+v", payload)
	}
	if payload["direction"] != "outbound" || payload["agent_id"] != nil {
		t.Fatalf("generic payload fields = %+v", payload)
	}
}
