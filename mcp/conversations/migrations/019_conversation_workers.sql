-- Workers (spawned threads) linked to the conversation whose thread spawned
-- them. A spawn tool row keeps Core's tool span and the requested thread id
-- so the topology's spawned_by link can be matched to it exactly.
ALTER TABLE conversation_tool_activity ADD COLUMN tool_span_id TEXT NOT NULL DEFAULT '';
ALTER TABLE conversation_tool_activity ADD COLUMN spawn_thread_id TEXT NOT NULL DEFAULT '';
CREATE INDEX tool_activity_spawn ON conversation_tool_activity(agent_id,name,started_at);

CREATE TABLE conversation_workers (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 conversation_id TEXT NOT NULL REFERENCES conversations(id) ON DELETE CASCADE,
 agent_id INTEGER NOT NULL,
 thread_id TEXT NOT NULL,
 -- incarnation separates a reused thread id: the spawn tool span when Core
 -- reported one, otherwise the start time.
 incarnation TEXT NOT NULL,
 parent_thread_id TEXT NOT NULL DEFAULT '',
 spawn_call_id TEXT NOT NULL DEFAULT '',
 depth INTEGER NOT NULL DEFAULT 1,
 name TEXT NOT NULL DEFAULT '',
 purpose TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT 'working',
 current_json TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '',
 next_wake_at TEXT NOT NULL DEFAULT '',
 started_at TEXT NOT NULL DEFAULT '',
 last_activity_at TEXT NOT NULL DEFAULT '',
 ended_at TEXT NOT NULL DEFAULT '',
 outcome TEXT NOT NULL DEFAULT '',
 result TEXT NOT NULL DEFAULT '',
 result_truncated INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 1,
 UNIQUE(agent_id,thread_id,incarnation)
);
CREATE INDEX conversation_workers_chat ON conversation_workers(conversation_id,id);
CREATE INDEX conversation_workers_open ON conversation_workers(agent_id,ended_at);
