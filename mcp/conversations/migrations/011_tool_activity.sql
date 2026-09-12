CREATE TABLE conversation_tool_activity (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL,
 call_id TEXT NOT NULL,
 name TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '',
 status TEXT NOT NULL DEFAULT 'running',
 started_at TEXT NOT NULL,
 ended_at TEXT NOT NULL DEFAULT '',
 revision INTEGER NOT NULL DEFAULT 1,
 UNIQUE(conversation_id,agent_id,thread_id,call_id,started_at)
);
CREATE INDEX tool_activity_chat ON conversation_tool_activity(conversation_id,id);
CREATE INDEX tool_activity_call ON conversation_tool_activity(conversation_id,agent_id,thread_id,call_id,started_at);
