package main

import (
	"database/sql"
	"os"
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
	sqlBytes, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.Exec(string(sqlBytes)); err != nil {
		t.Fatalf("run migration: %v", err)
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
